package application

import (
	"errors"
	"strings"
)

var (
	ErrLocalUserExists   = errors.New("user email already exists")
	ErrLocalUserNotFound = errors.New("local user not found")
)

// LocalUser is a persisted login credential. Never render or log PasswordHash.
type LocalUser struct {
	ID                int64
	Email             string
	Username          string
	PasswordHash      string
	CredentialVersion int64
}

// ValidateUsername canonicalizes the ASCII login identifier.
func ValidateUsername(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 1 || len(value) > 64 {
		return "", errors.New("username must contain 1–64 letters, numbers, dots, underscores or hyphens")
	}
	for i, c := range value {
		alphanumeric := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if !alphanumeric && (i == 0 || !strings.ContainsRune("._-", c)) {
			return "", errors.New("username must start with a letter or number and contain only letters, numbers, dots, underscores or hyphens")
		}
	}
	return value, nil
}
