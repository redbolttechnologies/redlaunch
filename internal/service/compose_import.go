package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	if _, ok := s.runner.(composeProjectConfigReader); !ok {
		return nil, errors.New("Compose project configuration reader is not configured")
	}

	parsedServices, err := parseImportedComposeServices(string(contents))
	if err != nil {
		return nil, err
	}

	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	defer lease.release()

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
	var referencedEnvSnapshots []managedFileSnapshot
	rollbackFiles := func() error {
		rollbackErrs := []error{
			restoreManagedFile(composeSnapshot),
			restoreManagedFile(varsSnapshot),
			restoreManagedFile(secretsSnapshot),
		}
		for _, snapshot := range referencedEnvSnapshots {
			rollbackErrs = append(rollbackErrs, restoreManagedFile(snapshot))
		}
		return errors.Join(rollbackErrs...)
	}

	processedContents, err := addManagedFieldsToImportedCompose(string(contents), parsedServices, item.ID)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("prepare imported Compose project: %w", err), rollbackFiles())
	}
	if err := writeManagedFile(composePath, processedContents, composeMode); err != nil {
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
	referencedEnvSnapshots, err = ensureImportedReferencedEnvFiles(directory, processedContents)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create referenced environment files: %w", err), rollbackFiles())
	}
	if err := populateImportedInterpolationPlaceholders(directory, processedContents); err != nil {
		return nil, errors.Join(fmt.Errorf("create imported interpolation placeholders: %w", err), rollbackFiles())
	}

	configuredServices, err := s.runner.(composeProjectConfigReader).ConfigServices(ctx, directory)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("validate imported Compose project: %w", err), rollbackFiles())
	}
	configuredByName, err := matchImportedComposeServices(parsedServices, configuredServices)
	if err != nil {
		return nil, errors.Join(err, rollbackFiles())
	}

	metadata := make([]application.Service, 0, len(parsedServices))
	for _, parsedService := range parsedServices {
		configured := configuredByName[parsedService.name]
		metadata = append(metadata, application.Service{
			ApplicationID: item.ID,
			Name:          parsedService.name,
			Type:          application.ServiceTypeApplication,
			ImageName:     importedImageName(configured.Image),
			CreatedAt:     time.Now().UTC(),
		})
	}

	if batch, ok := s.serviceRepository.(applicationServiceBatchRepository); ok {
		created, createErr := batch.CreateServices(ctx, metadata)
		if createErr != nil {
			return nil, errors.Join(fmt.Errorf("persist imported service metadata: %w", createErr), rollbackFiles())
		}
		return created, nil
	}

	created := make([]application.Service, 0, len(metadata))
	for _, item := range metadata {
		createdService, createErr := s.serviceRepository.CreateService(ctx, item)
		if createErr != nil {
			compensationCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			rollbackErr := rollbackImportedServiceMetadata(compensationCtx, deleter, item.ApplicationID, created)
			cancel()
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
		var err error
		block, err = ensureImportedServiceNetworks(block, service.name)
		if err != nil {
			return "", err
		}
		lines = replaceImportedYAMLLines(lines, service.start, service.end, block)
	}
	lines = ensureImportedSharedNetwork(lines)
	return strings.Join(lines, "\n"), nil
}

// ensureImportedServiceNetworks attaches one imported service to the shared
// application network in addition to any custom networks it already uses.
// The Caddy proxy resolves routable containers through redlaunch-common, so a
// service without an explicit default attachment would be unreachable after
// import even though `docker compose up` succeeds on its isolated network.
func ensureImportedServiceNetworks(block []string, serviceName string) ([]string, error) {
	fieldIndex, fieldEnd, value, ok := importedYAMLField(block, "networks")
	if !ok {
		return insertImportedServiceField(block, []string{
			"    networks:",
			"      - default",
		}), nil
	}

	trimmed := strings.TrimSpace(importedYAMLValueWithoutComment(value))
	if strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "&") {
		return nil, fmt.Errorf("%w: service %q uses an unsupported YAML alias or anchor for networks", application.ErrComposeFileInvalid, serviceName)
	}
	if trimmed != "" {
		if trimmed == "{}" || trimmed == "[]" {
			return replaceImportedYAMLLines(block, fieldIndex, fieldEnd, []string{
				"    networks:",
				"      - default",
			}), nil
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			entries := splitImportedInlineValues(trimmed)
			for _, entry := range entries {
				entry = strings.TrimSpace(entry)
				if strings.HasPrefix(entry, "*") || strings.HasPrefix(entry, "&") || strings.Contains(entry, "{") || strings.Contains(entry, "}") {
					return nil, fmt.Errorf("%w: service %q uses an unsupported YAML alias or flow mapping for networks", application.ErrComposeFileInvalid, serviceName)
				}
				if normalizeImportedYAMLScalar(entry) == "default" {
					return block, nil
				}
			}
			replacement := []string{"    networks:"}
			for _, entry := range entries {
				if entry = strings.TrimSpace(entry); entry != "" {
					replacement = append(replacement, "      - "+entry)
				}
			}
			replacement = append(replacement, "      - default")
			return replaceImportedYAMLLines(block, fieldIndex, fieldEnd, replacement), nil
		}
		if strings.Contains(trimmed, "{") || strings.Contains(trimmed, "}") {
			return nil, fmt.Errorf("%w: service %q uses an unsupported flow mapping for networks", application.ErrComposeFileInvalid, serviceName)
		}
		if normalizeImportedYAMLScalar(trimmed) == "default" {
			return replaceImportedYAMLLines(block, fieldIndex, fieldEnd, []string{
				"    networks:",
				"      - default",
			}), nil
		}
		return replaceImportedYAMLLines(block, fieldIndex, fieldEnd, []string{
			"    networks:",
			"      - " + trimmed,
			"      - default",
		}), nil
	}

	hasList := false
	hasMapping := false
	for _, line := range block[fieldIndex+1 : fieldEnd] {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "#") {
			continue
		}
		if strings.HasPrefix(trimmedLine, "-") {
			hasList = true
			entry := strings.TrimSpace(strings.TrimPrefix(trimmedLine, "-"))
			entry = importedYAMLValueWithoutComment(entry)
			if strings.HasPrefix(entry, "*") || strings.HasPrefix(entry, "&") {
				return nil, fmt.Errorf("%w: service %q uses an unsupported YAML alias or anchor for networks", application.ErrComposeFileInvalid, serviceName)
			}
			if separator := strings.IndexByte(entry, ':'); separator >= 0 {
				entry = strings.TrimSpace(entry[:separator])
			}
			if normalizeImportedYAMLScalar(entry) == "default" {
				return block, nil
			}
			continue
		}
		if key, value, ok := yamlKeyAtIndentWithValue(line, 6); ok {
			hasMapping = true
			if normalizeImportedYAMLKey(key) == "default" {
				return block, nil
			}
			if trimmedValue := strings.TrimSpace(value); strings.HasPrefix(trimmedValue, "*") || strings.HasPrefix(trimmedValue, "&") {
				return nil, fmt.Errorf("%w: service %q uses an unsupported YAML alias or anchor for networks", application.ErrComposeFileInvalid, serviceName)
			}
			continue
		}
	}
	if !hasList && !hasMapping {
		return replaceImportedYAMLLines(block, fieldIndex, fieldEnd, []string{
			"    networks:",
			"      - default",
		}), nil
	}
	if hasMapping && !hasList {
		return insertImportedYAMLLines(block, importedYAMLAppendIndex(block, fieldIndex, fieldEnd), []string{"      default: {}"}), nil
	}
	return insertImportedYAMLLines(block, importedYAMLAppendIndex(block, fieldIndex, fieldEnd), []string{"      - default"}), nil
}

// ensureImportedSharedNetwork points the top-level default network at the
// shared redlaunch-common bridge and preserves every custom network. Without
// this an imported project keeps Compose's isolated per-project default
// network, so the proxy's Docker DNS lookup for redbolt-<id>-<service> fails
// with "server misbehaving" even while the container is running.
func ensureImportedSharedNetwork(lines []string) []string {
	shared := []string{
		"  default:",
		"    external: true",
		"    name: " + applicationNetworkName,
	}
	sectionIndex, sectionValue, ok := findImportedTopLevelYAMLKey(lines, "networks")
	if !ok {
		return appendYAMLBlock(lines, append([]string{"networks:"}, shared...))
	}
	sectionEnd := topLevelBlockEnd(lines, sectionIndex)
	switch importedYAMLValueWithoutComment(sectionValue) {
	case "", "{}", "[]":
		if importedYAMLValueWithoutComment(sectionValue) != "" {
			return replaceImportedYAMLLines(lines, sectionIndex, sectionEnd, append([]string{"networks:"}, shared...))
		}
	default:
		return replaceImportedYAMLLines(lines, sectionIndex, sectionEnd, append([]string{"networks:"}, shared...))
	}
	for index := sectionIndex + 1; index < sectionEnd; index++ {
		key, _, ok := yamlKeyAtIndentWithValue(lines[index], 2)
		if !ok || normalizeImportedYAMLKey(key) != "default" {
			continue
		}
		defaultEnd := sectionEnd
		for next := index + 1; next < sectionEnd; next++ {
			if _, ok := yamlKeyAtIndent(lines[next], 2); ok {
				defaultEnd = next
				break
			}
		}
		return replaceImportedYAMLLines(lines, index, defaultEnd, shared)
	}
	return insertImportedYAMLLines(lines, importedYAMLAppendIndex(lines, sectionIndex, sectionEnd), shared)
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

// populateImportedInterpolationPlaceholders appends NAME= placeholders to the
// managed environment files for interpolation variables the staged Compose
// contents reference without a fallback value (for example ${DB_PASSWORD}).
// Without a placeholder the variable silently resolves to an empty string and
// services such as PostgreSQL fail at runtime with no hint in the UI about
// which value is missing. Variables with a default (${VAR:-fallback}) resolve
// on their own and are skipped, as are variables already defined in one of
// the project environment files. Secret-like names go to secrets.env, the
// rest to vars.env. Empty placeholders do not change Compose resolution; they
// only make the missing configuration visible and editable.
func populateImportedInterpolationPlaceholders(directory, processedContents string) error {
	required := requiredImportedInterpolationVariables(processedContents)
	if len(required) == 0 {
		return nil
	}
	defined := make(map[string]struct{})
	for _, name := range []string{".env", varsEnvFile, secretsEnvFile} {
		contents, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(contents), "\n") {
			if key, ok := parseEnvironmentKey(line); ok {
				defined[key] = struct{}{}
			}
		}
	}
	varsPlaceholders := make([]string, 0)
	secretsPlaceholders := make([]string, 0)
	for name := range required {
		if _, exists := defined[name]; exists {
			continue
		}
		if importedInterpolationVariableIsSecret(name) {
			secretsPlaceholders = append(secretsPlaceholders, name)
		} else {
			varsPlaceholders = append(varsPlaceholders, name)
		}
	}
	if len(varsPlaceholders) == 0 && len(secretsPlaceholders) == 0 {
		return nil
	}
	sort.Strings(varsPlaceholders)
	sort.Strings(secretsPlaceholders)
	if len(varsPlaceholders) > 0 {
		if err := appendImportedInterpolationPlaceholders(filepath.Join(directory, varsEnvFile), varsPlaceholders); err != nil {
			return err
		}
	}
	if len(secretsPlaceholders) > 0 {
		if err := appendImportedInterpolationPlaceholders(filepath.Join(directory, secretsEnvFile), secretsPlaceholders); err != nil {
			return err
		}
	}
	return nil
}

func appendImportedInterpolationPlaceholders(path string, names []string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(contents)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	var additions strings.Builder
	additions.WriteString("# Added by Compose import: required variables without defaults. Set values before starting services.\n")
	for _, name := range names {
		additions.WriteString(name + "=\n")
	}
	return writeManagedFile(path, text+additions.String(), envFileMode)
}

// requiredImportedInterpolationVariables returns the names referenced as
// ${NAME}, ${NAME?...}, ${NAME+...}, or bare $NAME in the Compose contents,
// excluding references that carry a fallback value (${NAME:-...},
// ${NAME-...}, ${NAME:=...}, ${NAME=...}) and escaped $$ sequences. It
// mirrors the scanning rules of the Compose runner's interpolation allowlist
// so both agree on which names a file references.
func requiredImportedInterpolationVariables(contents string) map[string]struct{} {
	required := make(map[string]struct{})
	for index := 0; index < len(contents); index++ {
		if contents[index] != '$' || index+1 >= len(contents) {
			continue
		}
		if contents[index+1] == '$' {
			index++
			continue
		}
		if contents[index+1] == '{' {
			rest := contents[index+2:]
			closing := strings.IndexByte(rest, '}')
			if closing < 0 {
				break
			}
			if name, fallback, ok := splitImportedInterpolationInner(rest[:closing]); ok && !fallback {
				required[name] = struct{}{}
			}
			index += closing + 2
			continue
		}
		end := index + 1
		for end < len(contents) && isImportedInterpolationNameCharacter(contents[end], end == index+1) {
			end++
		}
		if end > index+1 {
			if name := contents[index+1 : end]; validImportedInterpolationName(name) {
				required[name] = struct{}{}
			}
			index = end - 1
		}
	}
	return required
}

// splitImportedInterpolationInner splits the inside of a ${...} reference
// into its variable name and whether it carries a fallback value.
// Operators that resolve to a value when the variable is unset (-, :-, =,
// :=) count as a fallback; plain references and the ?, :?, +, :+ forms still
// need a configured value.
func splitImportedInterpolationInner(inner string) (string, bool, bool) {
	separator := strings.IndexAny(inner, ":?+-=")
	if separator < 0 {
		if !validImportedInterpolationName(inner) {
			return "", false, false
		}
		return inner, false, true
	}
	name := inner[:separator]
	if !validImportedInterpolationName(name) {
		return "", false, false
	}
	remainder := inner[separator:]
	return name, strings.HasPrefix(remainder, "-") || strings.HasPrefix(remainder, ":-") ||
		strings.HasPrefix(remainder, "=") || strings.HasPrefix(remainder, ":="), true
}

func validImportedInterpolationName(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character == '_' || index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func isImportedInterpolationNameCharacter(character byte, first bool) bool {
	return character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character == '_' || !first && character >= '0' && character <= '9'
}

// importedInterpolationVariableIsSecret reports whether an interpolation
// variable name looks like a credential that belongs in secrets.env rather
// than vars.env. It mirrors the runner's diagnostic redaction keywords so
// classification stays consistent across the codebase.
func importedInterpolationVariableIsSecret(name string) bool {
	upper := strings.ToUpper(name)
	for _, part := range []string{"PASSWORD", "SECRET", "TOKEN", "API_KEY", "PRIVATE_KEY", "CREDENTIAL"} {
		if strings.Contains(upper, part) {
			return true
		}
	}
	return false
}

// ensureImportedReferencedEnvFiles creates empty files for local env_file
// references in the staged Compose contents that do not yet exist. Docker
// Compose fails the project-wide `config` validation (used for ownership
// checks before every mutating operation) when a referenced env_file is
// missing, which would leave an otherwise valid import permanently
// unstartable. Only paths already validated as local to the application
// directory are touched; anything else is left for policy validation.
func ensureImportedReferencedEnvFiles(directory, processedContents string) ([]managedFileSnapshot, error) {
	lines := strings.Split(processedContents, "\n")
	seen := make(map[string]struct{})
	var created []managedFileSnapshot
	for index, line := range lines {
		indent, key, value, ok := importedPolicyYAMLKey(line)
		if !ok || key != "env_file" {
			continue
		}
		for _, entry := range importedPolicyFieldEntries(lines, index, indent, value) {
			raw := strings.TrimSpace(importedPolicyPathEntry(entry))
			if raw == "" {
				continue
			}
			// Skip flow-mapping fragments that policy rejects elsewhere;
			// they must not become filesystem paths.
			if strings.Contains(raw, "{") || strings.Contains(raw, "}") {
				continue
			}
			trimmed := strings.TrimSpace(strings.Trim(raw, "\"'"))
			if trimmed == "" {
				continue
			}
			if err := validateImportedLocalPath(directory, trimmed, "env_file"); err != nil {
				continue
			}
			cleaned := filepath.Clean(strings.TrimSpace(strings.Trim(trimmed, "\"'")))
			candidate := filepath.Join(directory, cleaned)
			if _, exists := seen[candidate]; exists {
				continue
			}
			seen[candidate] = struct{}{}
			snapshot, err := snapshotManagedFile(candidate)
			if err != nil {
				return created, err
			}
			if snapshot.exists {
				continue
			}
			parent := filepath.Dir(candidate)
			if parent != filepath.Clean(directory) {
				if err := ensureDirectory(parent); err != nil {
					return created, err
				}
			}
			if err := writeManagedFile(candidate, "", envFileMode); err != nil {
				return created, err
			}
			created = append(created, snapshot)
		}
	}
	return created, nil
}
