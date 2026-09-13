package service

import (
	"fmt"
	"strings"

	"redlaunch/internal/application"
)

// PreviewImportedCompose parses an uploaded Compose file and returns the
// services, volumes, and networks it defines with their actual settings.
// Managed-service detection (PostgreSQL/Redis) is best-effort and based on
// the image reference plus strong container signals. The preview is
// read-only: it never touches the filesystem or Docker, and it uses the same
// line-based parser as the import itself without adding a YAML dependency.
func PreviewImportedCompose(contents string) (*application.ComposeImportPreview, error) {
	if len(contents) == 0 || strings.TrimSpace(contents) == "" {
		return nil, application.ErrComposeFileRequired
	}
	if len(contents) > application.MaxComposeFileSize {
		return nil, application.ErrComposeFileTooLarge
	}
	lines := strings.Split(contents, "\n")
	parsedServices, err := parseImportedComposeServices(contents)
	if err != nil {
		return nil, err
	}
	preview := &application.ComposeImportPreview{}
	for _, parsed := range parsedServices {
		block := append([]string(nil), lines[parsed.start:parsed.end]...)
		details := describeImportedServiceBlock(block)
		image := importedServiceScalar(block, "image")
		detectedType, reason := detectImportedServiceType(image, block, details)
		preview.Services = append(preview.Services, application.ComposeImportServicePreview{
			Name:            parsed.name,
			Image:           image,
			DetectedType:    detectedType,
			DetectionReason: reason,
			ContainerName:   importedServiceScalar(block, "container_name"),
			Restart:         importedServiceScalar(block, "restart"),
			Command:         importedServiceCommand(block),
			Build:           importedServiceBuild(block),
			Ports:           details.ports,
			Volumes:         details.volumes,
			Networks:        details.networks,
			EnvFiles:        details.envFiles,
			Environment:     details.environment,
			DependsOn:       details.dependsOn,
		})
	}

	volumes, err := parseImportedTopLevelResources(contents, "volumes")
	if err != nil {
		return nil, err
	}
	networks, err := parseImportedTopLevelResources(contents, "networks")
	if err != nil {
		return nil, err
	}
	volumeRefs := importedServiceVolumeRefs(lines, parsedServices)
	networkRefs := importedServiceNetworkRefs(lines, parsedServices)
	for _, volume := range volumes {
		preview.Volumes = append(preview.Volumes, application.ComposeImportResourcePreview{
			Name:   volume.name,
			Config: volume.config,
			UsedBy: sortedNames(volumeRefs[volume.name]),
		})
	}
	for _, network := range networks {
		preview.Networks = append(preview.Networks, application.ComposeImportResourcePreview{
			Name:   network.name,
			Config: network.config,
			UsedBy: sortedNames(networkRefs[network.name]),
		})
	}
	return preview, nil
}

type importedServiceDetails struct {
	ports       []string
	volumes     []string
	networks    []string
	envFiles    []string
	environment []string
	dependsOn   []string
}

func describeImportedServiceBlock(block []string) importedServiceDetails {
	return importedServiceDetails{
		ports:       importedServiceListDisplay(block, "ports"),
		volumes:     importedServiceListDisplay(block, "volumes"),
		networks:    importedServiceListDisplay(block, "networks"),
		envFiles:    importedServiceListDisplay(block, "env_file"),
		environment: importedServiceListDisplay(block, "environment"),
		dependsOn:   importedServiceDependsOn(block),
	}
}

func importedServiceScalar(block []string, field string) string {
	_, _, value, ok := importedYAMLField(block, field)
	if !ok {
		return ""
	}
	return strings.TrimSpace(strings.Trim(importedYAMLValueWithoutComment(value), "\"'"))
}

func importedServiceCommand(block []string) string {
	fieldIndex, fieldEnd, value, ok := importedYAMLField(block, "command")
	if !ok {
		return ""
	}
	if trimmed := strings.TrimSpace(importedYAMLValueWithoutComment(value)); trimmed != "" {
		return trimmed
	}
	entries := importedServiceListEntries(block[fieldIndex+1:fieldEnd])
	if len(entries) == 0 {
		return ""
	}
	return strings.Join(entries, " ")
}

func importedServiceBuild(block []string) string {
	fieldIndex, fieldEnd, value, ok := importedYAMLField(block, "build")
	if !ok {
		return ""
	}
	if trimmed := strings.TrimSpace(importedYAMLValueWithoutComment(value)); trimmed != "" {
		return trimmed
	}
	entries := make([]string, 0)
	for _, line := range block[fieldIndex+1 : fieldEnd] {
		trimmed := strings.TrimSpace(importedYAMLValueWithoutComment(line))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			entry := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if entry != "" {
				entries = append(entries, entry)
			}
			continue
		}
		entries = append(entries, trimmed)
	}
	return strings.Join(entries, ", ")
}

// importedServiceListDisplay returns the display entries for a list-like
// service field (ports, volumes, networks, env_file, environment). Mapping
// forms are kept as trimmed "key: value" lines; list continuations that
// belong to a long-syntax entry are folded into that entry so the summary
// shows the actual settings instead of only the first line.
func importedServiceListDisplay(block []string, field string) []string {
	fieldIndex, fieldEnd, value, ok := importedYAMLField(block, field)
	if !ok {
		return nil
	}
	if trimmed := strings.TrimSpace(importedYAMLValueWithoutComment(value)); trimmed != "" {
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			return splitImportedInlineValues(trimmed)
		}
		if trimmed == "{}" || trimmed == "[]" {
			return nil
		}
		return []string{trimmed}
	}
	return importedServiceListEntries(block[fieldIndex+1 : fieldEnd])
}

func importedServiceListEntries(lines []string) []string {
	entries := make([]string, 0)
	var current *string
	flush := func() {
		if current != nil {
			if trimmed := strings.TrimSpace(*current); trimmed != "" {
				entries = append(entries, trimmed)
			}
			current = nil
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			flush()
			entry := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			entry = importedYAMLValueWithoutComment(entry)
			value := entry
			current = &value
			continue
		}
		if current != nil {
			// Long-syntax continuation (for example ports or volume objects).
			continued := *current + " " + importedYAMLValueWithoutComment(trimmed)
			*current = continued
			continue
		}
		entries = append(entries, importedYAMLValueWithoutComment(trimmed))
	}
	flush()
	return entries
}

func importedServiceDependsOn(block []string) []string {
	fieldIndex, fieldEnd, value, ok := importedYAMLField(block, "depends_on")
	if !ok {
		return nil
	}
	if trimmed := strings.TrimSpace(importedYAMLValueWithoutComment(value)); trimmed != "" {
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			return splitImportedInlineValues(trimmed)
		}
		if trimmed == "{}" || trimmed == "[]" {
			return nil
		}
		return []string{trimmed}
	}
	entries := make([]string, 0)
	for _, line := range block[fieldIndex+1 : fieldEnd] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			entry := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			entry = importedYAMLValueWithoutComment(entry)
			if separator := strings.IndexByte(entry, ':'); separator >= 0 {
				entry = strings.TrimSpace(entry[:separator])
			}
			if entry != "" {
				entries = append(entries, entry)
			}
			continue
		}
		if key, _, ok := yamlKeyAtIndentWithValue(line, 6); ok {
			entries = append(entries, normalizeImportedYAMLKey(key))
			continue
		}
		if key, _, ok := yamlKeyAtIndentWithValue(line, 8); ok {
			_ = key
			continue
		}
		entries = append(entries, importedYAMLValueWithoutComment(trimmed))
	}
	return entries
}

type importedTopLevelResource struct {
	name   string
	config []string
}

func parseImportedTopLevelResources(contents, section string) ([]importedTopLevelResource, error) {
	lines := strings.Split(contents, "\n")
	sectionIndex, sectionValue, ok := findImportedTopLevelYAMLKey(lines, section)
	if !ok {
		return nil, nil
	}
	switch importedYAMLValueWithoutComment(sectionValue) {
	case "", "{}", "[]":
		if importedYAMLValueWithoutComment(sectionValue) != "" {
			return nil, nil
		}
	default:
		return nil, fmt.Errorf("%w: %s must be a block mapping", application.ErrComposeFileInvalid, section)
	}
	sectionEnd := topLevelBlockEnd(lines, sectionIndex)
	resources := make([]importedTopLevelResource, 0)
	seen := make(map[string]struct{})
	for index := sectionIndex + 1; index < sectionEnd; {
		key, value, ok := yamlKeyAtIndentWithValue(lines[index], 2)
		if !ok {
			index++
			continue
		}
		name, err := application.ValidateServiceName(normalizeImportedYAMLKey(key))
		if err != nil {
			return nil, fmt.Errorf("%w: invalid %s name: %v", application.ErrComposeFileInvalid, section, err)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("%w: duplicate %s %q", application.ErrComposeFileInvalid, section, name)
		}
		if value := importedYAMLValueWithoutComment(value); value != "" && value != "{}" {
			// Flow mappings can smuggle explicit resource names past the
			// line parser, so they stay rejected by policy. Surface the
			// same shape here so the preview does not silently misread it.
			if strings.Contains(value, "{") || strings.Contains(value, "}") {
				return nil, fmt.Errorf("%w: %s %q must be a block mapping", application.ErrComposeFileInvalid, section, name)
			}
		}
		resourceEnd := sectionEnd
		for next := index + 1; next < sectionEnd; next++ {
			if _, ok := yamlKeyAtIndent(lines[next], 2); ok {
				resourceEnd = next
				break
			}
		}
		config := make([]string, 0)
		for _, line := range lines[index+1 : resourceEnd] {
			trimmed := strings.TrimSpace(importedYAMLValueWithoutComment(line))
			if trimmed == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			config = append(config, trimmed)
		}
		resources = append(resources, importedTopLevelResource{name: name, config: config})
		seen[name] = struct{}{}
		index = resourceEnd
	}
	return resources, nil
}

// detectImportedServiceType maps a Compose service to the managed type
// Redlaunch would register. Image references are the primary signal;
// well-known Postgres/Redis container signals (data paths, init variables,
// redis-server commands) act as a fallback for custom images.
func detectImportedServiceType(image string, block []string, details importedServiceDetails) (string, string) {
	repository := importedImageRepository(image)
	if isPostgresImageRepository(repository) {
		return application.ServiceTypePostgreSQL, "image " + strings.TrimSpace(image) + " looks like PostgreSQL"
	}
	if isRedisImageRepository(repository) {
		return application.ServiceTypeRedis, "image " + strings.TrimSpace(image) + " looks like Redis"
	}
	blockText := strings.ToLower(strings.Join(block, "\n"))
	hasPostgresVolume := false
	for _, volume := range details.volumes {
		if strings.Contains(strings.ToLower(volume), "/var/lib/postgresql/data") {
			hasPostgresVolume = true
			break
		}
	}
	hasPostgresEnv := false
	for _, entry := range details.environment {
		upper := strings.ToUpper(entry)
		if strings.HasPrefix(upper, "POSTGRES_DB") || strings.HasPrefix(upper, "POSTGRES_USER") || strings.HasPrefix(upper, "POSTGRES_PASSWORD") {
			hasPostgresEnv = true
			break
		}
	}
	if !hasPostgresEnv && strings.Contains(blockText, "postgres_db") {
		hasPostgresEnv = true
	}
	hasRedisCommand := strings.Contains(blockText, "redis-server")
	hasRedisEnv := false
	for _, entry := range details.environment {
		if strings.HasPrefix(strings.ToUpper(entry), "REDIS_PASSWORD") {
			hasRedisEnv = true
			break
		}
	}
	if hasPostgresVolume || (hasPostgresEnv && repository == "") || (hasPostgresEnv && hasPostgresVolume) {
		return application.ServiceTypePostgreSQL, "container settings reference PostgreSQL data or init variables"
	}
	if hasRedisCommand || hasRedisEnv {
		return application.ServiceTypeRedis, "container settings reference a Redis server or password"
	}
	if hasPostgresEnv {
		return application.ServiceTypePostgreSQL, "environment suggests PostgreSQL; confirm before managing it as a database"
	}
	return application.ServiceTypeApplication, ""
}

func importedImageRepository(image string) string {
	trimmed := strings.TrimSpace(image)
	if trimmed == "" {
		return ""
	}
	if at := strings.IndexByte(trimmed, '@'); at >= 0 {
		trimmed = trimmed[:at]
	}
	name := trimmed
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		if colon := strings.LastIndexByte(name, ':'); colon > slash {
			tag := name[colon+1:]
			if !strings.Contains(tag, "/") {
				name = name[:colon]
			}
		}
	} else if colon := strings.LastIndexByte(name, ':'); colon >= 0 {
		name = name[:colon]
	}
	name = strings.ToLower(strings.TrimSpace(name))
	return name
}

func importedImageBase(repository string) string {
	repository = strings.ToLower(strings.TrimSpace(repository))
	if repository == "" {
		return ""
	}
	if slash := strings.LastIndexByte(repository, '/'); slash >= 0 {
		return repository[slash+1:]
	}
	return repository
}

func isPostgresImageRepository(repository string) bool {
	if repository == "" {
		return false
	}
	base := importedImageBase(repository)
	switch base {
	case "postgres", "postgresql", "pgvector", "postgis", "timescaledb", "timescale":
		return true
	}
	if strings.Contains(repository, "postgres") {
		return true
	}
	if strings.Contains(repository, "pgvector") || strings.Contains(repository, "postgis") {
		return true
	}
	return false
}

func isRedisImageRepository(repository string) bool {
	if repository == "" {
		return false
	}
	base := importedImageBase(repository)
	switch base {
	case "redis", "redis-stack", "redis-stack-server":
		return true
	}
	if strings.Contains(repository, "/redis") || strings.HasPrefix(repository, "redis:") {
		return true
	}
	if repository == "bitnami/redis" || strings.HasSuffix(repository, "/redis") {
		return true
	}
	return false
}

func importedServiceVolumeRefs(lines []string, services []importedComposeService) map[string]map[string]struct{} {
	refs := make(map[string]map[string]struct{})
	for _, service := range services {
		for _, name := range composeVolumeNamesFromServiceBlock(lines, service.start, service.end) {
			if refs[name] == nil {
				refs[name] = make(map[string]struct{})
			}
			refs[name][service.name] = struct{}{}
		}
	}
	return refs
}

func importedServiceNetworkRefs(lines []string, services []importedComposeService) map[string]map[string]struct{} {
	refs := make(map[string]map[string]struct{})
	for _, service := range services {
		for _, name := range composeNetworkNamesFromServiceBlock(lines, service.start, service.end) {
			if refs[name] == nil {
				refs[name] = make(map[string]struct{})
			}
			refs[name][service.name] = struct{}{}
		}
	}
	return refs
}

func composeNetworkNamesFromServiceBlock(lines []string, start, end int) []string {
	networksIndex := -1
	for index := start + 1; index < end; index++ {
		key, ok := yamlKeyAtIndent(lines[index], 4)
		if ok && key == "networks" {
			networksIndex = index
			break
		}
	}
	if networksIndex == -1 {
		return nil
	}
	_, value, ok := yamlKeyAtIndentWithValue(lines[networksIndex], 4)
	if !ok {
		return nil
	}
	if trimmed := strings.TrimSpace(importedYAMLValueWithoutComment(value)); trimmed != "" {
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			names := make([]string, 0)
			for _, entry := range splitImportedInlineValues(trimmed) {
				if name, err := application.ValidateServiceName(normalizeImportedYAMLKey(strings.TrimSpace(entry))); err == nil {
					names = append(names, name)
				}
			}
			return names
		}
		if trimmed == "{}" || trimmed == "[]" {
			return nil
		}
		if name, err := application.ValidateServiceName(normalizeImportedYAMLKey(trimmed)); err == nil {
			return []string{name}
		}
		return nil
	}
	networksEnd := end
	for index := networksIndex + 1; index < end; index++ {
		if _, ok := yamlKeyAtIndent(lines[index], 4); ok {
			networksEnd = index
			break
		}
	}
	seen := make(map[string]struct{})
	var names []string
	for index := networksIndex + 1; index < networksEnd; index++ {
		trimmed := strings.TrimSpace(lines[index])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			entry := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			entry = importedYAMLValueWithoutComment(entry)
			if separator := strings.IndexByte(entry, ':'); separator >= 0 {
				entry = strings.TrimSpace(entry[:separator])
			}
			if name, err := application.ValidateServiceName(normalizeImportedYAMLKey(entry)); err == nil {
				if _, exists := seen[name]; !exists {
					seen[name] = struct{}{}
					names = append(names, name)
				}
			}
			continue
		}
		if key, _, ok := yamlKeyAtIndentWithValue(lines[index], 6); ok {
			if name, err := application.ValidateServiceName(normalizeImportedYAMLKey(key)); err == nil {
				if _, exists := seen[name]; !exists {
					seen[name] = struct{}{}
					names = append(names, name)
				}
			}
		}
	}
	return names
}

func sortedNames(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	for left := 1; left < len(names); left++ {
		for right := left; right > 0 && names[right] < names[right-1]; right-- {
			names[right], names[right-1] = names[right-1], names[right]
		}
	}
	return names
}
