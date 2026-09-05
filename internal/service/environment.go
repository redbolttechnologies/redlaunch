package service

import (
	"fmt"
	"strconv"
	"strings"

	"redlaunch/internal/application"
)

func readEnvironmentFile(path string, sensitive bool) ([]application.EnvironmentVariable, bool, error) {
	snapshot, err := snapshotManagedFile(path)
	if err != nil {
		return nil, false, err
	}
	if !snapshot.exists {
		return nil, false, nil
	}
	return parseEnvironmentFile(string(snapshot.contents), sensitive), true, nil
}

func parseEnvironmentFile(contents string, sensitive bool) []application.EnvironmentVariable {
	contents = strings.ReplaceAll(contents, "\r\n", "\n")
	lines := strings.Split(contents, "\n")
	variables := make([]application.EnvironmentVariable, 0, len(lines))
	for index, line := range lines {
		if index == 0 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		key, value, ok := parseEnvironmentEntry(line)
		if !ok {
			continue
		}
		variables = append(variables, application.EnvironmentVariable{
			Key:       key,
			Value:     value,
			Sensitive: sensitive,
		})
	}
	return variables
}

func parseEnvironmentEntry(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	separator := strings.IndexByte(trimmed, '=')
	if separator < 1 {
		return "", "", false
	}
	key := strings.TrimSpace(trimmed[:separator])
	if !validEnvironmentKey(key) {
		return "", "", false
	}
	return key, parseEnvironmentValue(trimmed[separator+1:]), true
}

func validateEnvironmentFileContents(contents []byte) error {
	if len(contents) > application.MaxEnvironmentFileSize {
		return application.ErrEnvironmentImportFileTooLarge
	}

	text := strings.ReplaceAll(string(contents), "\r\n", "\n")
	seen := make(map[string]struct{})
	for index, line := range strings.Split(text, "\n") {
		if index == 0 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		key, value, ok := parseEnvironmentEntry(line)
		if !ok || !validEnvironmentValueSyntax(environmentEntryValue(line)) {
			return fmt.Errorf("%w: invalid entry on line %d", application.ErrEnvironmentImportInvalid, index+1)
		}
		if _, err := application.ValidateEnvironmentVariableValue(value); err != nil {
			return fmt.Errorf("%w: invalid value on line %d", application.ErrEnvironmentImportInvalid, index+1)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: duplicate entry on line %d", application.ErrEnvironmentImportInvalid, index+1)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func environmentEntryValue(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	separator := strings.IndexByte(trimmed, '=')
	if separator < 0 {
		return ""
	}
	return trimmed[separator+1:]
}

func validEnvironmentValueSyntax(raw string) bool {
	value := strings.TrimSpace(raw)
	if value == "" || value[0] != '\'' && value[0] != '"' {
		return true
	}

	quote := value[0]
	closing := environmentQuoteEnd(value, quote)
	if closing < 1 {
		return false
	}
	trailing := strings.TrimSpace(value[closing+1:])
	if trailing != "" && !strings.HasPrefix(trailing, "#") {
		return false
	}
	if quote == '"' {
		if _, err := strconv.Unquote(value[:closing+1]); err != nil {
			return false
		}
	}
	return true
}

func parseEnvironmentValue(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}

	quote := value[0]
	if quote == '\'' || quote == '"' {
		if closing := environmentQuoteEnd(value, quote); closing >= 1 {
			trailing := strings.TrimSpace(value[closing+1:])
			if trailing == "" || strings.HasPrefix(trailing, "#") {
				quoted := value[:closing+1]
				if quote == '\'' {
					return quoted[1 : len(quoted)-1]
				}
				if unquoted, err := strconv.Unquote(quoted); err == nil {
					return unquoted
				}
			}
		}
	}

	for index := 1; index < len(value); index++ {
		if value[index] != '#' || value[index-1] != ' ' && value[index-1] != '\t' {
			continue
		}
		value = value[:index]
		break
	}
	return strings.TrimSpace(value)
}

func environmentQuoteEnd(value string, quote byte) int {
	escaped := false
	for index := 1; index < len(value); index++ {
		character := value[index]
		if quote == '"' && character == '\\' && !escaped {
			escaped = true
			continue
		}
		if character == quote && !escaped {
			return index
		}
		escaped = false
	}
	return -1
}

func updateEnvironmentFile(contents, originalName, name, value string) (string, error) {
	lineEnding := "\n"
	if strings.Contains(contents, "\r\n") {
		lineEnding = "\r\n"
		contents = strings.ReplaceAll(contents, "\r\n", "\n")
	}
	bom := ""
	if strings.HasPrefix(contents, "\ufeff") {
		bom = "\ufeff"
		contents = strings.TrimPrefix(contents, "\ufeff")
	}
	trailingNewline := strings.HasSuffix(contents, "\n")
	if trailingNewline {
		contents = strings.TrimSuffix(contents, "\n")
	}
	lines := strings.Split(contents, "\n")
	target := -1
	for index, line := range lines {
		key, _, ok := parseEnvironmentEntry(line)
		if !ok {
			continue
		}
		if key == name && key != originalName {
			return "", application.ErrEnvironmentVariableAlreadyExists
		}
		if key == originalName {
			if target >= 0 {
				return "", application.ErrEnvironmentVariableDuplicate
			}
			target = index
		}
	}
	if target < 0 {
		return "", application.ErrEnvironmentVariableNotFound
	}

	lines[target] = formatEnvironmentEntry(lines[target], name, value)
	updated := strings.Join(lines, "\n")
	if trailingNewline {
		updated += "\n"
	}
	if lineEnding != "\n" {
		updated = strings.ReplaceAll(updated, "\n", lineEnding)
	}
	return bom + updated, nil
}

func appendEnvironmentVariable(contents, name, value string) (string, error) {
	lineEnding := "\n"
	if strings.Contains(contents, "\r\n") {
		lineEnding = "\r\n"
		contents = strings.ReplaceAll(contents, "\r\n", "\n")
	}
	bom := ""
	if strings.HasPrefix(contents, "\ufeff") {
		bom = "\ufeff"
		contents = strings.TrimPrefix(contents, "\ufeff")
	}
	for _, line := range strings.Split(contents, "\n") {
		key, _, ok := parseEnvironmentEntry(line)
		if ok && key == name {
			return "", application.ErrEnvironmentVariableAlreadyExists
		}
	}

	entry := name + "=" + formatEnvironmentValue(value)
	if contents == "" {
		return bom + entry + lineEnding, nil
	}
	if strings.HasSuffix(contents, "\n") {
		contents += entry + "\n"
	} else {
		contents += "\n" + entry + "\n"
	}
	if lineEnding != "\n" {
		contents = strings.ReplaceAll(contents, "\n", lineEnding)
	}
	return bom + contents, nil
}

func deleteEnvironmentVariable(contents, name string) (string, error) {
	lineEnding := "\n"
	if strings.Contains(contents, "\r\n") {
		lineEnding = "\r\n"
		contents = strings.ReplaceAll(contents, "\r\n", "\n")
	}
	bom := ""
	if strings.HasPrefix(contents, "\ufeff") {
		bom = "\ufeff"
		contents = strings.TrimPrefix(contents, "\ufeff")
	}
	trailingNewline := strings.HasSuffix(contents, "\n")
	if trailingNewline {
		contents = strings.TrimSuffix(contents, "\n")
	}

	lines := strings.Split(contents, "\n")
	target := -1
	for index, line := range lines {
		key, _, ok := parseEnvironmentEntry(line)
		if !ok || key != name {
			continue
		}
		if target >= 0 {
			return "", application.ErrEnvironmentVariableDuplicate
		}
		target = index
	}
	if target < 0 {
		return "", application.ErrEnvironmentVariableNotFound
	}

	lines = append(lines[:target:target], lines[target+1:]...)
	updated := strings.Join(lines, "\n")
	if len(lines) > 0 && trailingNewline {
		updated += "\n"
	}
	if lineEnding != "\n" {
		updated = strings.ReplaceAll(updated, "\n", lineEnding)
	}
	return bom + updated, nil
}

func formatEnvironmentEntry(line, name, value string) string {
	leading := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	trimmed := strings.TrimSpace(line)
	export := ""
	if strings.HasPrefix(trimmed, "export ") {
		export = "export "
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	separator := strings.IndexByte(trimmed, '=')
	comment := ""
	if separator >= 0 {
		comment = environmentCommentSuffix(trimmed[separator+1:])
	}
	return leading + export + name + "=" + formatEnvironmentValue(value) + comment
}

func environmentCommentSuffix(raw string) string {
	var quote byte
	escaped := false
	for index := 0; index < len(raw); index++ {
		character := raw[index]
		if quote != 0 {
			if quote == '"' && character == '\\' && !escaped {
				escaped = true
				continue
			}
			if character == quote && !escaped {
				quote = 0
			}
			escaped = false
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character != '#' || index > 0 && raw[index-1] != ' ' && raw[index-1] != '\t' {
			continue
		}
		start := index
		for start > 0 && (raw[start-1] == ' ' || raw[start-1] == '\t') {
			start--
		}
		return raw[start:]
	}
	return ""
}
