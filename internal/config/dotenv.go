package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
)

const (
	dotEnvFileEnvironmentVariable = "DOTENV_FILE"
	defaultDotEnvFile             = ".env"
	dotEnvScannerBufferSize       = 1024 * 1024
)

// loadEnvironment returns the process environment augmented with values from
// a dotenv file. Existing process environment values intentionally win over
// values from the file, including explicitly empty values.
func loadEnvironment() (map[string]string, error) {
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[key] = value
		}
	}

	dotEnvPath, explicitlyConfigured := os.LookupEnv(dotEnvFileEnvironmentVariable)
	if !explicitlyConfigured {
		dotEnvPath = defaultDotEnvFile
	}
	dotEnvPath = strings.TrimSpace(dotEnvPath)
	if dotEnvPath == "" {
		return environment, nil
	}

	file, err := os.Open(dotEnvPath)
	if err != nil {
		if !explicitlyConfigured && errors.Is(err, os.ErrNotExist) {
			return environment, nil
		}
		return nil, fmt.Errorf("load dotenv file %q: %w", dotEnvPath, err)
	}
	defer file.Close()

	values, err := parseDotEnv(file)
	if err != nil {
		return nil, fmt.Errorf("load dotenv file %q: %w", dotEnvPath, err)
	}
	for key, value := range values {
		if _, exists := environment[key]; !exists {
			environment[key] = value
		}
	}
	return environment, nil
}

func parseDotEnv(reader io.Reader) (map[string]string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), dotEnvScannerBufferSize)

	values := make(map[string]string)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\uFEFF"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export") && len(line) > len("export") && unicode.IsSpace(rune(line[len("export")])) {
			line = strings.TrimSpace(line[len("export"):])
		}

		equalsIndex := strings.IndexByte(line, '=')
		if equalsIndex <= 0 {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", lineNumber)
		}

		key := strings.TrimSpace(line[:equalsIndex])
		if !validDotEnvKey(key) {
			return nil, fmt.Errorf("line %d: invalid variable name %q", lineNumber, key)
		}

		value, err := parseDotEnvValue(strings.TrimSpace(line[equalsIndex+1:]))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	return values, nil
}

func parseDotEnvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	switch raw[0] {
	case '\'':
		end := strings.IndexByte(raw[1:], '\'')
		if end < 0 {
			return "", errors.New("unterminated single-quoted value")
		}
		end++
		if !validDotEnvSuffix(raw[end+1:]) {
			return "", errors.New("unexpected text after quoted value")
		}
		return raw[1:end], nil
	case '"':
		end := closingDoubleQuote(raw)
		if end < 0 {
			return "", errors.New("unterminated double-quoted value")
		}
		if !validDotEnvSuffix(raw[end+1:]) {
			return "", errors.New("unexpected text after quoted value")
		}
		value, err := strconv.Unquote(raw[:end+1])
		if err != nil {
			return "", fmt.Errorf("invalid double-quoted value: %w", err)
		}
		return value, nil
	default:
		return unquotedDotEnvValue(raw), nil
	}
}

func closingDoubleQuote(value string) int {
	escaped := false
	for index := 1; index < len(value); index++ {
		switch {
		case escaped:
			escaped = false
		case value[index] == '\\':
			escaped = true
		case value[index] == '"':
			return index
		}
	}
	return -1
}

func validDotEnvSuffix(suffix string) bool {
	suffix = strings.TrimSpace(suffix)
	return suffix == "" || strings.HasPrefix(suffix, "#")
}

func unquotedDotEnvValue(raw string) string {
	for index := 0; index < len(raw); index++ {
		if raw[index] == '#' && (index == 0 || unicode.IsSpace(rune(raw[index-1]))) {
			return strings.TrimSpace(raw[:index])
		}
	}
	return strings.TrimSpace(raw)
}

func validDotEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for index := 0; index < len(key); index++ {
		character := key[index]
		if index == 0 {
			if !isDotEnvKeyStart(character) {
				return false
			}
			continue
		}
		if !isDotEnvKeyStart(character) && !(character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func isDotEnvKeyStart(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character == '_'
}
