package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

type composeProjectConfigReader interface {
	ConfigServices(context.Context, string) ([]compose.ConfiguredService, error)
}

type importedComposeService struct {
	name  string
	start int
	end   int
}

// ImportDockerComposeProject replaces an empty application's Compose project
// with an uploaded definition and registers each Compose service in metadata.
// The Compose file remains the source of truth for service configuration;
// Redlaunch only adds the fields required for managed containers.
func (s *Applications) ImportDockerComposeProject(ctx context.Context, applicationID int64, contents []byte) ([]application.Service, error) {
	if len(contents) == 0 || strings.TrimSpace(string(contents)) == "" {
		return nil, application.ErrComposeFileRequired
	}
	if len(contents) > application.MaxComposeFileSize {
		return nil, application.ErrComposeFileTooLarge
	}
	if s.detailsRepository == nil {
		return nil, errors.New("application details repository is not configured")
	}
	if s.serviceRepository == nil {
		return nil, errors.New("application service repository is not configured")
	}
	deleter, ok := s.serviceRepository.(applicationServiceDeletionRepository)
	if !ok {
		return nil, errors.New("application service deletion repository is not configured")
	}
	configReader, ok := s.runner.(composeProjectConfigReader)
	if !ok {
		return nil, errors.New("Compose project configuration reader is not configured")
	}

	parsedServices, err := parseImportedComposeServices(string(contents))
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	registeredServices, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return nil, fmt.Errorf("list application services before import: %w", err)
	}
	if len(registeredServices) > 0 {
		return nil, application.ErrComposeServicesAlreadyExist
	}

	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return nil, err
	}
	if err := validateImportedComposePolicy(string(contents), directory); err != nil {
		return nil, err
	}
	composePath, err := findApplicationComposeFile(directory)
	if err != nil {
		return nil, fmt.Errorf("find application Compose file: %w", err)
	}
	if composePath == "" {
		composePath = filepath.Join(directory, "compose.yml")
	}
	composeSnapshot, err := snapshotManagedFile(composePath)
	if err != nil {
		return nil, fmt.Errorf("read application Compose file: %w", err)
	}
	varsPath := filepath.Join(directory, varsEnvFile)
	varsSnapshot, err := snapshotManagedFile(varsPath)
	if err != nil {
		return nil, fmt.Errorf("read application variables file: %w", err)
	}
	secretsPath := filepath.Join(directory, secretsEnvFile)
	secretsSnapshot, err := snapshotManagedFile(secretsPath)
	if err != nil {
		return nil, fmt.Errorf("read application secrets file: %w", err)
	}

	composeMode := os.FileMode(0o644)
	if composeSnapshot.exists {
		composeMode = composeSnapshot.mode
	}
	rollbackFiles := func() error {
		return errors.Join(
			restoreManagedFile(composeSnapshot),
			restoreManagedFile(varsSnapshot),
			restoreManagedFile(secretsSnapshot),
		)
	}

	if err := writeManagedFile(composePath, string(contents), composeMode); err != nil {
		return nil, fmt.Errorf("write imported Compose file: %w", err)
	}
	if !varsSnapshot.exists {
		if err := writeManagedFile(varsPath, "", envFileMode); err != nil {
			return nil, errors.Join(fmt.Errorf("create application variables file: %w", err), rollbackFiles())
		}
	}
	if !secretsSnapshot.exists {
		if err := writeManagedFile(secretsPath, "", envFileMode); err != nil {
			return nil, errors.Join(fmt.Errorf("create application secrets file: %w", err), rollbackFiles())
		}
	}

	configuredServices, err := configReader.ConfigServices(ctx, directory)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("validate imported Compose project: %w", err), rollbackFiles())
	}
	configuredByName, err := matchImportedComposeServices(parsedServices, configuredServices)
	if err != nil {
		return nil, errors.Join(err, rollbackFiles())
	}

	processedContents, err := addManagedFieldsToImportedCompose(string(contents), parsedServices, item.ID)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("prepare imported Compose project: %w", err), rollbackFiles())
	}
	if err := writeManagedFile(composePath, processedContents, composeMode); err != nil {
		return nil, errors.Join(fmt.Errorf("write managed Compose file: %w", err), rollbackFiles())
	}

	created := make([]application.Service, 0, len(parsedServices))
	for _, parsedService := range parsedServices {
		configured := configuredByName[parsedService.name]
		createdService, createErr := s.serviceRepository.CreateService(ctx, application.Service{
			ApplicationID: item.ID,
			Name:          parsedService.name,
			Type:          application.ServiceTypeApplication,
			ImageName:     importedImageName(configured.Image),
			CreatedAt:     time.Now().UTC(),
		})
		if createErr != nil {
			rollbackErr := rollbackImportedServiceMetadata(ctx, deleter, item.ID, created)
			return nil, errors.Join(fmt.Errorf("persist imported service metadata: %w", createErr), rollbackErr, rollbackFiles())
		}
		created = append(created, createdService)
	}
	return created, nil
}

func parseImportedComposeServices(contents string) ([]importedComposeService, error) {
	lines := strings.Split(contents, "\n")
	servicesIndex, servicesValue, ok := findImportedTopLevelYAMLKey(lines, "services")
	if !ok {
		return nil, fmt.Errorf("%w: top-level services is required", application.ErrComposeFileInvalid)
	}
	switch importedYAMLValueWithoutComment(servicesValue) {
	case "{}", "[]":
		return nil, application.ErrComposeProjectHasNoServices
	case "":
	default:
		return nil, fmt.Errorf("%w: services must be a block mapping", application.ErrComposeFileInvalid)
	}

	servicesEnd := topLevelBlockEnd(lines, servicesIndex)
	services := make([]importedComposeService, 0)
	seen := make(map[string]struct{})
	for index := servicesIndex + 1; index < servicesEnd; {
		key, value, ok := yamlKeyAtIndentWithValue(lines[index], 2)
		if !ok {
			index++
			continue
		}
		name, err := application.ValidateServiceName(normalizeImportedYAMLKey(key))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", application.ErrComposeFileInvalid, err)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("%w: duplicate service %q", application.ErrComposeFileInvalid, name)
		}
		if value := importedYAMLValueWithoutComment(value); value != "" && value != "{}" && !strings.HasPrefix(value, "&") {
			return nil, fmt.Errorf("%w: service %q must be a block mapping", application.ErrComposeFileInvalid, name)
		}

		serviceEnd := servicesEnd
		for next := index + 1; next < servicesEnd; next++ {
			if _, ok := yamlKeyAtIndent(lines[next], 2); ok {
				serviceEnd = next
				break
			}
		}
		services = append(services, importedComposeService{name: name, start: index, end: serviceEnd})
		seen[name] = struct{}{}
		index = serviceEnd
	}
	if len(services) == 0 {
		return nil, application.ErrComposeProjectHasNoServices
	}
	return services, nil
}

func matchImportedComposeServices(parsed []importedComposeService, configured []compose.ConfiguredService) (map[string]compose.ConfiguredService, error) {
	parsedByName := make(map[string]struct{}, len(parsed))
	for _, service := range parsed {
		parsedByName[service.name] = struct{}{}
	}
	if len(configured) == 0 {
		return nil, application.ErrComposeProjectHasNoServices
	}
	configuredByName := make(map[string]compose.ConfiguredService, len(configured))
	for _, configuredService := range configured {
		name, err := application.ValidateServiceName(configuredService.Name)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid configured service name", application.ErrComposeFileInvalid)
		}
		if _, exists := configuredByName[name]; exists {
			return nil, fmt.Errorf("%w: duplicate configured service %q", application.ErrComposeFileInvalid, name)
		}
		if _, exists := parsedByName[name]; !exists {
			return nil, fmt.Errorf("%w: configured service %q was not found in the uploaded file", application.ErrComposeFileInvalid, name)
		}
		configuredService.Name = name
		configuredByName[name] = configuredService
	}
	if len(configuredByName) != len(parsed) {
		return nil, fmt.Errorf("%w: Compose service list changed while importing", application.ErrComposeFileInvalid)
	}
	return configuredByName, nil
}

func rollbackImportedServiceMetadata(ctx context.Context, deleter applicationServiceDeletionRepository, applicationID int64, created []application.Service) error {
	var rollbackErr error
	for index := len(created) - 1; index >= 0; index-- {
		if err := deleter.DeleteService(ctx, applicationID, created[index].Name); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove imported service %q metadata: %w", created[index].Name, err))
		}
	}
	return rollbackErr
}

func importedImageName(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	if _, err := application.ValidateImageName(image); err != nil {
		return ""
	}
	return image
}

func addManagedFieldsToImportedCompose(contents string, services []importedComposeService, applicationID int64) (string, error) {
	lines := strings.Split(contents, "\n")
	for index := len(services) - 1; index >= 0; index-- {
		service := services[index]
		block := append([]string(nil), lines[service.start:service.end]...)
		if err := rejectImportedManagedFieldAliases(block, service.name); err != nil {
			return "", err
		}
		block = normalizeImportedServiceHeader(block)
		block = ensureImportedContainerName(block, managedContainerNamePrefix+strconv.FormatInt(applicationID, 10)+"-"+service.name)
		block = ensureImportedEnvFiles(block)
		block = ensureImportedManagedLabel(block)
		lines = replaceImportedYAMLLines(lines, service.start, service.end, block)
	}
	return strings.Join(lines, "\n"), nil
}

func rejectImportedManagedFieldAliases(lines []string, serviceName string) error {
	for _, field := range []string{"env_file", "labels"} {
		_, _, value, ok := importedYAMLField(lines, field)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "*") || strings.HasPrefix(value, "&") {
			return fmt.Errorf("%w: service %q uses an unsupported YAML alias or anchor for %s", application.ErrComposeFileInvalid, serviceName, field)
		}
	}
	return nil
}

func normalizeImportedServiceHeader(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	_, value, ok := yamlKeyAtIndentWithValue(lines[0], 2)
	if ok && importedYAMLValueWithoutComment(value) == "{}" {
		if separator := strings.IndexByte(lines[0], ':'); separator >= 0 {
			lines[0] = lines[0][:separator+1]
		}
	}
	return lines
}

func ensureImportedContainerName(lines []string, containerName string) []string {
	fieldIndex, _, _, ok := importedYAMLField(lines, "container_name")
	if ok {
		lines[fieldIndex] = "    container_name: " + containerName
		return lines
	}
	return insertImportedServiceField(lines, []string{"    container_name: " + containerName})
}

func ensureImportedEnvFiles(lines []string) []string {
	fieldIndex, fieldEnd, value, ok := importedYAMLField(lines, "env_file")
	if !ok {
		return insertImportedServiceField(lines, []string{
			"    env_file:",
			"      - vars.env",
			"      - secrets.env",
		})
	}

	entries := importedYAMLListEntries(lines[fieldIndex+1:fieldEnd], value)
	if strings.TrimSpace(value) == "" {
		missing := make([]string, 0, 2)
		for _, required := range []string{"vars.env", "secrets.env"} {
			if !importedYAMLListContains(entries, required) {
				missing = append(missing, "      - "+required)
			}
		}
		if len(missing) == 0 {
			return lines
		}
		return insertImportedYAMLLines(lines, importedYAMLAppendIndex(lines, fieldIndex, fieldEnd), missing)
	}

	replacement := []string{"    env_file:"}
	for _, entry := range entries {
		replacement = append(replacement, "      - "+entry)
	}
	for _, required := range []string{"vars.env", "secrets.env"} {
		if !importedYAMLListContains(entries, required) {
			replacement = append(replacement, "      - "+required)
		}
	}
	return replaceImportedYAMLLines(lines, fieldIndex, fieldEnd, replacement)
}

func ensureImportedManagedLabel(lines []string) []string {
	fieldIndex, fieldEnd, value, ok := importedYAMLField(lines, "labels")
	if !ok {
		return insertImportedServiceField(lines, []string{
			"    labels:",
			"      - \"redlaunch.managed=true\"",
		})
	}

	trimmedValue := strings.TrimSpace(value)
	if trimmedValue == "" {
		kind := importedLabelBlockKind(lines[fieldIndex+1 : fieldEnd])
		switch kind {
		case "mapping":
			for index := fieldIndex + 1; index < fieldEnd; index++ {
				key, _, ok := yamlKeyAtIndentWithValue(lines[index], 6)
				if ok && normalizeImportedYAMLKey(key) == "redlaunch.managed" {
					lines[index] = "      redlaunch.managed: \"true\""
					return lines
				}
			}
			return insertImportedYAMLLines(lines, importedYAMLAppendIndex(lines, fieldIndex, fieldEnd), []string{"      redlaunch.managed: \"true\""})
		case "list":
			for index := fieldIndex + 1; index < fieldEnd; index++ {
				trimmed := strings.TrimSpace(lines[index])
				if !strings.HasPrefix(trimmed, "-") {
					continue
				}
				label := normalizeImportedYAMLScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "-")))
				if strings.HasPrefix(label, "redlaunch.managed=") {
					indent := lines[index][:len(lines[index])-len(strings.TrimLeft(lines[index], " \t"))]
					lines[index] = indent + "- \"redlaunch.managed=true\""
					return lines
				}
			}
			return insertImportedYAMLLines(lines, importedYAMLAppendIndex(lines, fieldIndex, fieldEnd), []string{"      - \"redlaunch.managed=true\""})
		default:
			return insertImportedYAMLLines(lines, importedYAMLAppendIndex(lines, fieldIndex, fieldEnd), []string{"      - \"redlaunch.managed=true\""})
		}
	}

	if strings.HasPrefix(trimmedValue, "[") {
		entries := importedYAMLListEntries(nil, trimmedValue)
		return replaceImportedLabelsWithList(lines, fieldIndex, fieldEnd, entries)
	}
	if strings.HasPrefix(trimmedValue, "{") {
		entries := splitImportedInlineValues(trimmedValue)
		replacement := []string{"    labels:"}
		managedFound := false
		for _, entry := range entries {
			key, labelValue, ok := splitImportedMappingEntry(entry)
			if !ok {
				continue
			}
			if normalizeImportedYAMLKey(key) == "redlaunch.managed" {
				labelValue = "\"true\""
				managedFound = true
			}
			replacement = append(replacement, "      "+key+": "+labelValue)
		}
		if !managedFound {
			replacement = append(replacement, "      redlaunch.managed: \"true\"")
		}
		return replaceImportedYAMLLines(lines, fieldIndex, fieldEnd, replacement)
	}

	return replaceImportedLabelsWithList(lines, fieldIndex, fieldEnd, []string{trimmedValue})
}

func replaceImportedLabelsWithList(lines []string, fieldIndex, fieldEnd int, entries []string) []string {
	replacement := []string{"    labels:"}
	managedFound := false
	for _, entry := range entries {
		label := normalizeImportedYAMLScalar(entry)
		if strings.HasPrefix(label, "redlaunch.managed=") {
			entry = "\"redlaunch.managed=true\""
			managedFound = true
		}
		replacement = append(replacement, "      - "+entry)
	}
	if !managedFound {
		replacement = append(replacement, "      - \"redlaunch.managed=true\"")
	}
	return replaceImportedYAMLLines(lines, fieldIndex, fieldEnd, replacement)
}

func importedYAMLField(lines []string, wanted string) (int, int, string, bool) {
	for index := 1; index < len(lines); index++ {
		key, value, ok := yamlKeyAtIndentWithValue(lines[index], 4)
		if !ok || normalizeImportedYAMLKey(key) != wanted {
			continue
		}
		end := len(lines)
		for next := index + 1; next < len(lines); next++ {
			if _, ok := yamlKeyAtIndent(lines[next], 4); ok {
				end = next
				break
			}
		}
		return index, end, importedYAMLValueWithoutComment(value), true
	}
	return 0, 0, "", false
}

func insertImportedServiceField(lines, field []string) []string {
	index := len(lines)
	for index > 1 && strings.TrimSpace(lines[index-1]) == "" {
		index--
	}
	return insertImportedYAMLLines(lines, index, field)
}

func importedYAMLAppendIndex(lines []string, fieldIndex, fieldEnd int) int {
	index := fieldEnd
	for index > fieldIndex+1 && strings.TrimSpace(lines[index-1]) == "" {
		index--
	}
	return index
}

func importedYAMLListEntries(block []string, value string) []string {
	value = strings.TrimSpace(value)
	if value != "" {
		if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
			return splitImportedInlineValues(value)
		}
		if value == "{}" || value == "[]" {
			return nil
		}
		return []string{value}
	}
	entries := make([]string, 0)
	for _, line := range block {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "-") {
			entry := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if entry != "" {
				entries = append(entries, entry)
			}
		}
	}
	return entries
}

func importedYAMLListContains(entries []string, wanted string) bool {
	for _, entry := range entries {
		if normalizeImportedYAMLScalar(entry) == wanted {
			return true
		}
	}
	return false
}

func importedLabelBlockKind(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			return "list"
		}
		if _, ok := yamlKeyAtIndent(line, 6); ok {
			return "mapping"
		}
		return "unknown"
	}
	return "unknown"
}

func splitImportedInlineValues(value string) []string {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return nil
	}
	value = strings.TrimSpace(value[1 : len(value)-1])
	if value == "" {
		return nil
	}
	entries := make([]string, 0)
	start := 0
	quote := byte(0)
	escaped := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if escaped {
			escaped = false
			continue
		}
		if quote != 0 {
			if character == '\\' && quote == '"' {
				escaped = true
			} else if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == ',' {
			entry := strings.TrimSpace(value[start:index])
			if entry != "" {
				entries = append(entries, entry)
			}
			start = index + 1
		}
	}
	if entry := strings.TrimSpace(value[start:]); entry != "" {
		entries = append(entries, entry)
	}
	return entries
}

func splitImportedMappingEntry(entry string) (string, string, bool) {
	quote := byte(0)
	escaped := false
	for index := 0; index < len(entry); index++ {
		character := entry[index]
		if escaped {
			escaped = false
			continue
		}
		if quote != 0 {
			if character == '\\' && quote == '"' {
				escaped = true
			} else if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == ':' {
			key := strings.TrimSpace(entry[:index])
			value := strings.TrimSpace(entry[index+1:])
			if key == "" {
				return "", "", false
			}
			return key, value, true
		}
	}
	return "", "", false
}

func normalizeImportedYAMLKey(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func normalizeImportedYAMLScalar(value string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(value), "\"'"))
}

func importedYAMLValueWithoutComment(value string) string {
	value = strings.TrimSpace(value)
	quote := byte(0)
	escaped := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if escaped {
			escaped = false
			continue
		}
		if quote != 0 {
			if character == '\\' && quote == '"' {
				escaped = true
			} else if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == '#' && (index == 0 || value[index-1] == ' ' || value[index-1] == '\t') {
			return strings.TrimSpace(value[:index])
		}
	}
	return value
}

func findImportedTopLevelYAMLKey(lines []string, wanted string) (int, string, bool) {
	for index, line := range lines {
		key, value, ok := parseTopLevelYAMLKey(line)
		if ok && normalizeImportedYAMLKey(key) == wanted {
			return index, value, true
		}
	}
	return 0, "", false
}

func insertImportedYAMLLines(lines []string, index int, additions []string) []string {
	result := make([]string, 0, len(lines)+len(additions))
	result = append(result, lines[:index]...)
	result = append(result, additions...)
	result = append(result, lines[index:]...)
	return result
}

func replaceImportedYAMLLines(lines []string, start, end int, replacement []string) []string {
	result := make([]string, 0, len(lines)-(end-start)+len(replacement))
	result = append(result, lines[:start]...)
	result = append(result, replacement...)
	result = append(result, lines[end:]...)
	return result
}
