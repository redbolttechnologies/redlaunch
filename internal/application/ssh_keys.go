package application

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxSSHKeyDisplayNameLength = 100

var (
	ErrSSHKeyDisplayNameRequired    = errors.New("SSH key display name is required")
	ErrSSHKeyDisplayNameTooLong     = errors.New("SSH key display name is too long")
	ErrSSHKeyDisplayNameInvalid     = errors.New("SSH key display name is invalid")
	ErrSSHKeyNotFound               = errors.New("SSH key not found")
	ErrSSHKeyServiceRequired        = errors.New("SSH key service restriction is incomplete")
	ErrSSHKeyServiceNotFound        = errors.New("SSH key service was not found")
	ErrSSHKeyServiceHasNoTargetPort = errors.New("SSH key service publishes no host port")
	ErrSSHKeyPermitOpenInvalid      = errors.New("SSH key forwarding target is invalid")
)

// ServerSSHKey is one operator-managed host access key. Only the public key
// is persisted; the private key is returned transiently at creation time.
// An empty PermitOpen means full shell access; otherwise the key is
// tunnel-only and OpenSSH limits forwarding to PermitOpen ("127.0.0.1:PORT").
// ApplicationID and ServiceName record which managed service the restriction
// was resolved from; they are display metadata, while PermitOpen is the
// enforced snapshot.
type ServerSSHKey struct {
	ID             int64
	DisplayName    string
	PublicKey      string
	KeyFingerprint string
	ApplicationID  int64
	ServiceName    string
	PermitOpen     string
	CreatedAt      time.Time
}

// ServerSSHKeyInput carries the operator-supplied values for key creation.
// An ApplicationID of zero with an empty ServiceName creates an unrestricted
// key; otherwise both must identify one managed service to restrict to.
type ServerSSHKeyInput struct {
	DisplayName   string
	ApplicationID int64
	ServiceName   string
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

// ValidateSSHKeyPermitOpen validates a tunnel restriction target and returns
// its canonical form. Only loopback targets are accepted so restricted keys
// can only reach services published on the host itself. The empty value is
// valid and means unrestricted.
func ValidateSSHKeyPermitOpen(value string) (string, error) {
	target := strings.TrimSpace(value)
	if target == "" {
		return "", nil
	}
	host, portText, ok := strings.Cut(target, ":")
	if !ok || strings.TrimSpace(host) != "127.0.0.1" {
		return "", ErrSSHKeyPermitOpenInvalid
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil || port < 1 || port > 65535 {
		return "", ErrSSHKeyPermitOpenInvalid
	}
	return "127.0.0.1:" + strconv.Itoa(port), nil
}
