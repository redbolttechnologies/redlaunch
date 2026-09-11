package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validateStagedCompose asks the Docker adapter to validate the Compose file
// currently staged on disk. The caller owns the snapshots and must restore
// them when validation or a later metadata operation fails.
func (s *Applications) validateStagedCompose(ctx context.Context, directory string) error {
	reader, ok := s.runner.(composeProjectConfigReader)
	if !ok {
		// Small test adapters and integrations that predate configuration
		// validation remain usable. The production CommandRunner implements the
		// reader and always validates managed mutations.
		return nil
	}
	if _, err := reader.ConfigServices(ctx, directory); err != nil {
		return fmt.Errorf("validate staged Compose project: %w", err)
	}
	return nil
}

// findApplicationComposeFile returns the existing Compose file for an
// application. Older managed projects may use either supported Compose file
// name; a missing file is left to the caller to handle.
func findApplicationComposeFile(directory string) (string, error) {
	for _, name := range []string{"compose.yml", "compose.yaml"} {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", name)
		}
		return path, nil
	}
	return "", nil
}

// addServiceEnvironmentFiles adds relative env_file entries to a service
// block. It is used only for migrating legacy generated database services to
// scoped files; aliases and unsupported inline forms are rejected rather than
// rewritten ambiguously.
func addServiceEnvironmentFiles(contents, serviceName string, files ...string) (string, error) {
	lines := strings.Split(contents, "\n")
	servicesIndex, servicesValue, ok := findTopLevelYAMLKey(lines, "services")
	if !ok || strings.TrimSpace(servicesValue) != "" {
		return "", errors.New("Compose services must be a block mapping")
	}
	servicesEnd := topLevelBlockEnd(lines, servicesIndex)
	serviceOffset := findYAMLKeyAtIndent(lines[servicesIndex+1:servicesEnd], serviceName, 2)
	if serviceOffset < 0 {
		return "", fmt.Errorf("Compose service %q was not found", serviceName)
	}
	serviceIndex := servicesIndex + 1 + serviceOffset
	serviceEnd := servicesEnd
	for index := serviceIndex + 1; index < servicesEnd; index++ {
		if _, ok := yamlKeyAtIndent(lines[index], 2); ok {
			serviceEnd = index
			break
		}
	}
	fieldIndex := -1
	fieldEnd := serviceEnd
	fieldValue := ""
	for index := serviceIndex + 1; index < serviceEnd; index++ {
		key, value, ok := yamlKeyAtIndentWithValue(lines[index], 4)
		if !ok || key != "env_file" {
			continue
		}
		fieldIndex = index
		fieldValue = strings.TrimSpace(value)
		for next := index + 1; next < serviceEnd; next++ {
			if _, ok := yamlKeyAtIndent(lines[next], 4); ok {
				fieldEnd = next
				break
			}
		}
		break
	}

	entries := make([]string, 0, len(files)+2)
	if fieldIndex >= 0 {
		if fieldValue != "" {
			if strings.HasPrefix(fieldValue, "*") || strings.HasPrefix(fieldValue, "&") || strings.HasPrefix(fieldValue, "[") || strings.HasPrefix(fieldValue, "{") {
				return "", errors.New("service env_file uses an unsupported YAML alias or collection")
			}
			entries = append(entries, normalizeImportedYAMLScalar(importedYAMLValueWithoutComment(fieldValue)))
		} else {
			for _, line := range lines[fieldIndex+1 : fieldEnd] {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				if !strings.HasPrefix(trimmed, "-") {
					return "", errors.New("service env_file must be a list")
				}
				entry := normalizeImportedYAMLScalar(importedYAMLValueWithoutComment(strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))))
				if entry != "" {
					if strings.HasPrefix(entry, "*") || strings.HasPrefix(entry, "&") || strings.Contains(entry, "[") || strings.Contains(entry, "{") {
						return "", errors.New("service env_file uses an unsupported YAML alias or collection")
					}
					entries = append(entries, entry)
				}
			}
		}
	}
	for _, file := range files {
		if file == "" {
			continue
		}
		found := false
		for _, entry := range entries {
			if entry == file {
				found = true
				break
			}
		}
		if !found {
			entries = append(entries, file)
		}
	}
	if fieldIndex < 0 {
		block := []string{"    env_file:"}
		for _, entry := range entries {
			block = append(block, "      - "+entry)
		}
		return strings.Join(insertYAMLBlock(lines, serviceEnd, block), "\n"), nil
	}
	replacement := []string{"    env_file:"}
	for _, entry := range entries {
		replacement = append(replacement, "      - "+entry)
	}
	return strings.Join(replaceYAMLSection(lines, fieldIndex, fieldEnd, replacement), "\n"), nil
}

func replaceYAMLSection(lines []string, start, end int, replacement []string) []string {
	result := make([]string, 0, len(lines)-(end-start)+len(replacement))
	result = append(result, lines[:start]...)
	result = append(result, replacement...)
	result = append(result, lines[end:]...)
	return result
}

// removeServiceFromCompose removes one service mapping and its unused named
// volumes while preserving the rest of the Compose file, including unrelated
// formatting and comments.
func removeServiceFromCompose(contents, serviceName string) (string, error) {
	lines := strings.Split(contents, "\n")
	servicesIndex, servicesValue, ok := findTopLevelYAMLKey(lines, "services")
	if !ok {
		return "", errors.New("Compose file does not define top-level services")
	}
	if value := strings.TrimSpace(servicesValue); value != "" && value != "{}" {
		return "", errors.New("Compose services must be a block mapping")
	}
	if strings.TrimSpace(servicesValue) == "{}" {
		return contents, nil
	}

	servicesEnd := topLevelBlockEnd(lines, servicesIndex)
	serviceOffset := findYAMLKeyAtIndent(lines[servicesIndex+1:servicesEnd], serviceName, 2)
	if serviceOffset == -1 {
		return contents, nil
	}
	serviceIndex := servicesIndex + 1 + serviceOffset
	serviceEnd := servicesEnd
	for index := serviceIndex + 1; index < servicesEnd; index++ {
		if _, ok := yamlKeyAtIndent(lines[index], 2); ok {
			serviceEnd = index
			break
		}
	}
	serviceVolumeNames := composeVolumeNamesFromServiceBlock(lines, serviceIndex, serviceEnd)

	removeStart := serviceIndex
	if removeStart > servicesIndex+1 && lines[removeStart-1] == "" {
		removeStart--
	}
	removeEnd := serviceEnd
	if removeEnd > serviceIndex && lines[removeEnd-1] == "" {
		// Keep one blank line between the services block and the next top-level
		// Compose section.
		removeEnd--
	}
	lines = removeYAMLLines(lines, removeStart, removeEnd)
	var err error
	for _, volumeName := range serviceVolumeNames {
		lines, err = removeUnusedComposeVolume(lines, volumeName)
		if err != nil {
			return "", err
		}
	}

	remainingServicesEnd := topLevelBlockEnd(lines, servicesIndex)
	if !hasYAMLKeyAtIndent(lines[servicesIndex+1:remainingServicesEnd], 2) {
		lines = replaceYAMLLine(lines, servicesIndex, []string{"services: {}"})
	}
	return strings.Join(lines, "\n"), nil
}

func composeVolumeNamesFromServiceBlock(lines []string, start, end int) []string {
	volumesIndex := -1
	for index := start + 1; index < end; index++ {
		key, ok := yamlKeyAtIndent(lines[index], 4)
		if ok && key == "volumes" {
			volumesIndex = index
			break
		}
	}
	if volumesIndex == -1 {
		return nil
	}

	volumesEnd := end
	for index := volumesIndex + 1; index < end; index++ {
		if _, ok := yamlKeyAtIndent(lines[index], 4); ok {
			volumesEnd = index
			break
		}
	}

	seen := make(map[string]struct{})
	var names []string
	for index := volumesIndex + 1; index < volumesEnd; index++ {
		trimmed := strings.TrimSpace(lines[index])
		if strings.HasPrefix(trimmed, "-") {
			if name, ok := composeNamedVolumeFromShortSyntax(strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))); ok {
				if _, exists := seen[name]; !exists {
					seen[name] = struct{}{}
					names = append(names, name)
				}
			}
		}
		if key, value, ok := yamlKeyAtIndentWithValue(lines[index], 8); ok && key == "source" {
			if name, ok := composeNamedVolume(value); ok {
				if _, exists := seen[name]; !exists {
					seen[name] = struct{}{}
					names = append(names, name)
				}
			}
		}
	}
	return names
}

func removeUnusedComposeVolume(lines []string, volumeName string) ([]string, error) {
	if composeVolumeReferencedByServices(lines, volumeName) {
		return lines, nil
	}

	volumesIndex, volumesValue, ok := findTopLevelYAMLKey(lines, "volumes")
	if !ok || strings.TrimSpace(volumesValue) == "{}" {
		return lines, nil
	}
	if value := strings.TrimSpace(volumesValue); value != "" {
		return nil, errors.New("Compose volumes must be a block mapping")
	}

	volumesEnd := topLevelBlockEnd(lines, volumesIndex)
	volumeOffset := findYAMLKeyAtIndent(lines[volumesIndex+1:volumesEnd], volumeName, 2)
	if volumeOffset == -1 {
		return lines, nil
	}
	volumeIndex := volumesIndex + 1 + volumeOffset
	volumeEnd := volumesEnd
	for index := volumeIndex + 1; index < volumesEnd; index++ {
		if _, ok := yamlKeyAtIndent(lines[index], 2); ok {
			volumeEnd = index
			break
		}
	}

	removeStart := volumeIndex
	if removeStart > volumesIndex+1 && lines[removeStart-1] == "" {
		removeStart--
	}
	removeEnd := volumeEnd
	if removeEnd > volumeIndex && lines[removeEnd-1] == "" {
		// Keep one blank line between the volumes block and the next top-level
		// Compose section.
		removeEnd--
	}
	lines = removeYAMLLines(lines, removeStart, removeEnd)

	volumesIndex, _, ok = findTopLevelYAMLKey(lines, "volumes")
	if !ok {
		return lines, nil
	}
	volumesEnd = topLevelBlockEnd(lines, volumesIndex)
	if !hasYAMLKeyAtIndent(lines[volumesIndex+1:volumesEnd], 2) {
		lines = removeYAMLSection(lines, volumesIndex, volumesEnd)
	}
	return lines, nil
}

func composeVolumeReferencedByServices(lines []string, volumeName string) bool {
	servicesIndex, servicesValue, ok := findTopLevelYAMLKey(lines, "services")
	if !ok || strings.TrimSpace(servicesValue) != "" {
		return false
	}
	servicesEnd := topLevelBlockEnd(lines, servicesIndex)
	for index := servicesIndex + 1; index < servicesEnd; index++ {
		if _, ok := yamlKeyAtIndent(lines[index], 2); !ok {
			continue
		}
		serviceEnd := servicesEnd
		for candidate := index + 1; candidate < servicesEnd; candidate++ {
			if _, ok := yamlKeyAtIndent(lines[candidate], 2); ok {
				serviceEnd = candidate
				break
			}
		}
		for _, name := range composeVolumeNamesFromServiceBlock(lines, index, serviceEnd) {
			if name == volumeName {
				return true
			}
		}
		index = serviceEnd - 1
	}
	return false
}

func composeNamedVolumeFromShortSyntax(value string) (string, bool) {
	value = strings.TrimSpace(strings.Trim(value, "\"'"))
	if value == "" || strings.HasPrefix(value, "#") {
		return "", false
	}
	separator := strings.IndexByte(value, ':')
	if separator == -1 {
		return "", false
	}
	if separator+1 < len(value) && (value[separator+1] == ' ' || value[separator+1] == '\t') {
		// Mapping-style list entries such as "- type: volume" are handled by
		// the long syntax source field below, not as short volume strings.
		return "", false
	}
	return composeNamedVolume(value[:separator])
}

func composeNamedVolume(value string) (string, bool) {
	value = strings.TrimSpace(strings.Trim(value, "\"'"))
	if value == "" || strings.ContainsAny(value, "/\\:$") || strings.HasPrefix(value, ".") || strings.HasPrefix(value, "~") {
		return "", false
	}
	for index, character := range value {
		if (index == 0 && !isASCIIAlphaNumeric(character)) || (index > 0 && !isASCIIAlphaNumeric(character) && character != '_' && character != '-' && character != '.') {
			return "", false
		}
	}
	return value, true
}

func yamlKeyAtIndentWithValue(line string, indent int) (string, string, bool) {
	key, ok := yamlKeyAtIndent(line, indent)
	if !ok {
		return "", "", false
	}
	remainder := line[indent:]
	separator := strings.IndexByte(remainder, ':')
	if separator == -1 {
		return "", "", false
	}
	return key, strings.TrimSpace(remainder[separator+1:]), true
}

func removeYAMLSection(lines []string, start, end int) []string {
	hadTrailingNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	removeStart := start
	if removeStart > 0 && lines[removeStart-1] == "" {
		removeStart--
	}
	result := removeYAMLLines(lines, removeStart, end)
	if hadTrailingNewline && (len(result) == 0 || result[len(result)-1] != "") {
		result = append(result, "")
	}
	return result
}

func findYAMLKeyAtIndent(lines []string, wanted string, indent int) int {
	for index, line := range lines {
		key, ok := yamlKeyAtIndent(line, indent)
		if ok && key == wanted {
			return index
		}
	}
	return -1
}

func hasYAMLKeyAtIndent(lines []string, indent int) bool {
	for _, line := range lines {
		if _, ok := yamlKeyAtIndent(line, indent); ok {
			return true
		}
	}
	return false
}

func yamlKeyAtIndent(line string, indent int) (string, bool) {
	prefix := strings.Repeat(" ", indent)
	if !strings.HasPrefix(line, prefix) || strings.HasPrefix(line, prefix+" ") || strings.HasPrefix(line, prefix+"\t") {
		return "", false
	}
	remainder := line[indent:]
	trimmed := strings.TrimSpace(remainder)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	key, _, ok := parseIndentedYAMLKey(remainder)
	return key, ok
}

func removeYAMLLines(lines []string, start, end int) []string {
	result := make([]string, 0, len(lines)-(end-start))
	result = append(result, lines[:start]...)
	result = append(result, lines[end:]...)
	return result
}
