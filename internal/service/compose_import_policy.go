package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"redlaunch/internal/application"
)

var unsupportedImportedComposeKeys = map[string]struct{}{
	"cap_add":             {},
	"cache_from":          {},
	"cache_to":            {},
	"cgroup":              {},
	"cgroup_parent":       {},
	"configs":             {},
	"credential_spec":     {},
	"device_cgroup_rules": {},
	"devices":             {},
	"driver_opts":         {},
	"extends":             {},
	"gpus":                {},
	"include":             {},
	"additional_contexts": {},
	"isolation":           {},
	"ipc":                 {},
	"network_mode":        {},
	"pid":                 {},
	"privileged":          {},
	"runtime":             {},
	"security_opt":        {},
	"secrets":             {},
	"ssh":                 {},
	"sysctls":             {},
	"uts":                 {},
	"userns_mode":         {},
	"volumes_from":        {},
}

// validateImportedComposePolicy checks the small Compose subset Redlaunch can
// safely own. It deliberately runs before the upload is written and before
// Docker Compose is invoked, so rejected imports leave both files and service
// metadata untouched.
func validateImportedComposePolicy(contents string, applicationRoot string) error {
	root, err := filepath.Abs(filepath.Clean(applicationRoot))
	if err != nil {
		return fmt.Errorf("%w: resolve application root: %v", application.ErrComposeFileInvalid, err)
	}
	lines := strings.Split(contents, "\n")
	topLevelSection := ""
	for index, line := range lines {
		indent, key, value, ok := importedPolicyYAMLKey(line)
		if !ok {
			continue
		}
		if indent == 0 {
			topLevelSection = key
		}
		if _, unsupported := unsupportedImportedComposeKeys[key]; unsupported {
			return importedComposePolicyError(index, "the %s field is not supported", key)
		}
		if (indent == 0 && key == "name") || (key == "name" && (topLevelSection == "volumes" || topLevelSection == "networks")) {
			return importedComposePolicyError(index, "top-level name is not supported; Redlaunch assigns the project identity")
		}
		if key == "external" && strings.EqualFold(value, "true") && topLevelSection == "volumes" {
			return importedComposePolicyError(index, "external volumes are not supported")
		}

		switch key {
		case "build":
			if value != "" {
				if err := validateImportedLocalPath(root, value, "build context"); err != nil {
					return importedComposePolicyError(index, "%v", err)
				}
			}
		case "env_file":
			for _, entry := range importedPolicyFieldEntries(lines, index, indent, value) {
				entry = importedPolicyPathEntry(entry)
				if err := validateImportedLocalPath(root, entry, "env_file"); err != nil {
					return importedComposePolicyError(index, "%v", err)
				}
			}
		case "context", "dockerfile", "path":
			if err := validateImportedLocalPath(root, value, key); err != nil {
				return importedComposePolicyError(index, "%v", err)
			}
		case "source":
			if err := validateImportedVolumeSource(root, value); err != nil {
				return importedComposePolicyError(index, "%v", err)
			}
		case "target":
			if isDockerSocketPath(value) {
				return importedComposePolicyError(index, "container Docker socket mounts are not supported")
			}
		case "volumes":
			if indent > 0 {
				for _, entry := range importedPolicyFieldEntries(lines, index, indent, value) {
					if err := validateImportedVolumeSource(root, entry); err != nil {
						return importedComposePolicyError(index, "%v", err)
					}
				}
			}
		}
	}
	return nil
}

func importedComposePolicyError(line int, format string, args ...any) error {
	return fmt.Errorf("%w: line %d: %s", application.ErrComposeFileInvalid, line+1, fmt.Sprintf(format, args...))
}

func importedPolicyYAMLKey(line string) (int, string, string, bool) {
	if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
		return 0, "", "", false
	}
	indent := len(line) - len(strings.TrimLeft(line, " "))
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "-") {
		return 0, "", "", false
	}
	separator := strings.IndexByte(trimmed, ':')
	if separator < 1 {
		return 0, "", "", false
	}
	key := normalizeImportedYAMLKey(trimmed[:separator])
	if key == "" {
		return 0, "", "", false
	}
	return indent, strings.ToLower(key), importedYAMLValueWithoutComment(trimmed[separator+1:]), true
}

func importedPolicyFieldEntries(lines []string, fieldIndex, fieldIndent int, value string) []string {
	if strings.TrimSpace(value) != "" {
		if strings.HasPrefix(strings.TrimSpace(value), "[") {
			return splitImportedInlineValues(strings.TrimSpace(value))
		}
		return []string{strings.TrimSpace(value)}
	}
	entries := make([]string, 0)
	for index := fieldIndex + 1; index < len(lines); index++ {
		line := lines[index]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent <= fieldIndent {
			break
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "-") {
			entry := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if entry != "" {
				entries = append(entries, entry)
			}
			continue
		}
		if _, key, nestedValue, ok := importedPolicyYAMLKey(line); ok && key == "path" {
			entries = append(entries, nestedValue)
		}
	}
	return entries
}

func validateImportedLocalPath(root, raw, field string) error {
	value := strings.TrimSpace(strings.Trim(raw, "\"'"))
	if value == "" {
		return fmt.Errorf("%s must name a local path", field)
	}
	if strings.Contains(value, "://") || strings.HasPrefix(value, "~") || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.Contains(value, "$") {
		return fmt.Errorf("%s must be a local path within the application directory", field)
	}
	candidate := filepath.Join(root, filepath.Clean(value))
	if err := ensurePathWithinRoot(root, candidate); err != nil {
		return fmt.Errorf("%s escapes the application directory: %w", field, err)
	}
	return nil
}

func ensurePathWithinRoot(root, candidate string) error {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path is outside the application directory")
	}
	probe := candidate
	for {
		_, err := os.Lstat(probe)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(probe)
			if err != nil {
				return err
			}
			resolvedRelative, err := filepath.Rel(root, resolved)
			if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
				return fmt.Errorf("path resolves outside the application directory")
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return nil
		}
		probe = parent
		if probe == root {
			return nil
		}
	}
}

func validateImportedVolumeSource(root, raw string) error {
	value := strings.TrimSpace(strings.Trim(raw, "\"'"))
	if value == "" || strings.HasPrefix(value, "type:") || strings.HasPrefix(value, "source:") {
		return nil
	}
	if strings.Contains(value, "://") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") || strings.Contains(value, "\\") || strings.Contains(value, "$") {
		return fmt.Errorf("volume source must be a named volume or a local path within the application directory")
	}
	if isDockerSocketPath(value) || filepath.Base(strings.TrimSuffix(value, "/")) == "docker.sock" {
		return fmt.Errorf("container Docker socket mounts are not supported")
	}
	if strings.HasPrefix(value, ".") {
		separator := strings.IndexByte(value, ':')
		if separator >= 0 {
			value = value[:separator]
		}
		return validateImportedLocalPath(root, value, "volume source")
	}
	return nil
}

func isDockerSocketPath(raw string) bool {
	value := strings.TrimSpace(strings.Trim(raw, "\"'"))
	if separator := strings.IndexByte(value, ':'); separator >= 0 {
		value = value[:separator]
	}
	value = strings.TrimSuffix(value, "/")
	return value == "/var/run/docker.sock" || value == "/run/docker.sock" || value == "docker.sock" || strings.HasSuffix(value, "/docker.sock")
}

func importedPolicyPathEntry(value string) string {
	key, mappedValue, ok := splitImportedMappingEntry(strings.TrimSpace(value))
	if ok && strings.EqualFold(normalizeImportedYAMLKey(key), "path") {
		return mappedValue
	}
	return value
}
