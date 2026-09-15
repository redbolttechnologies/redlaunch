package application

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxSSHKeyDisplayNameLength = 100

var (
	ErrSSHKeyDisplayNameRequired = errors.New("SSH key display name is required")
	ErrSSHKeyDisplayNameTooLong  = errors.New("SSH key display name is too long")
	ErrSSHKeyDisplayNameInvalid  = errors.New("SSH key display name is invalid")
	ErrSSHKeyNotFound            = errors.New("SSH key not found")
)

// ServerSSHKey is one operator-managed host access key. Only the public key
// is persisted; the private key is returned transiently at creation time.
type ServerSSHKey struct {
	ID             int64
	DisplayName    string
	PublicKey      string
	KeyFingerprint string
	CreatedAt      time.Time
}

// ServerSSHKeySetup carries the one-time private key handoff. The private key
// must never be persisted or logged.
type ServerSSHKeySetup struct {
	Key        ServerSSHKey
	PrivateKey string
}

// ValidateSSHKeyDisplayName trims and validates the operator-supplied label
// shown in the SSH keys list. The value is display-only and never reaches a
// shell or an authorized_keys comment.
func ValidateSSHKeyDisplayName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", ErrSSHKeyDisplayNameRequired
	}
	if utf8.RuneCountInString(name) > MaxSSHKeyDisplayNameLength {
		return "", ErrSSHKeyDisplayNameTooLong
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return "", ErrSSHKeyDisplayNameInvalid
		}
	}
	return name, nil
}
