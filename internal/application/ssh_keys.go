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
// Every key grants full shell access as the dedicated redlaunch user.
type ServerSSHKey struct {
	ID             int64
	DisplayName    string
	PublicKey      string
	KeyFingerprint string
	CreatedAt      time.Time
}

// ServerSSHKeyInput carries the operator-supplied values for key creation.
type ServerSSHKeyInput struct {
	DisplayName string
}

// ServerSSHKeySetup carries the one-time private key handoff. The private key
// must never be persisted or logged. HostKeys carries the server's public
// OpenSSH host keys (for example Ed25519) so external automation can pin
// SSH_KNOWN_HOSTS with StrictHostKeyChecking; it is public key material only
// and may be empty when the host keys are not provisioned to the manager.
type ServerSSHKeySetup struct {
	Key        ServerSSHKey
	PrivateKey string
	HostKeys   []SSHHostKey
}

// SSHHostKey is one public host key of the server's own sshd (port 22). Only
// the public half is ever exposed; the comment from the .pub file is stripped.
// PublicKey holds the canonical "algorithm base64" form without a comment.
type SSHHostKey struct {
	Algorithm   string
	PublicKey   string
	Fingerprint string
}

// KnownHostsLine returns the known_hosts entry for this host key and the
// caller-supplied server host (IP or DNS as used by CI). The host value is
// validated to the same narrow set used for server hosts elsewhere.
func (k SSHHostKey) KnownHostsLine(host string) (string, error) {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" || len(trimmed) > 253 || strings.ContainsAny(trimmed, " \t\n\r\f\v/\\:@[];,&|$`\"'<>|*?") {
		return "", errors.New("SSH host name is invalid")
	}
	for _, character := range trimmed {
		if character < 0x20 || character == 0x7f {
			return "", errors.New("SSH host name is invalid")
		}
	}
	publicKey := strings.TrimSpace(k.PublicKey)
	if publicKey == "" {
		return "", errors.New("SSH host key is invalid")
	}
	return trimmed + " " + publicKey, nil
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
