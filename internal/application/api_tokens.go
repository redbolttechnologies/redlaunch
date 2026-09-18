package application

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// APITokenPrefix namespaces presented API tokens so they are
	// recognizable in configuration and logs without revealing anything.
	APITokenPrefix = "rlr_"
	// APITokenSecretBytes is the entropy of each generated token.
	APITokenSecretBytes = 32
	// APITokenPrefixLength is the length of the stored identification hint
	// ("rlr_" plus the first 8 secret characters).
	APITokenPrefixLength = 12

	MaxAPITokenDisplayNameLength = 100
	// MaxAPITokenLifetimeDays bounds the lifetime callers may request at
	// creation. Zero means the token never expires.
	MaxAPITokenLifetimeDays = 366

	// APITokenScopeRun authorizes one-off service runs in the pinned
	// application. It is the only scope issued in v1.
	APITokenScopeRun = "run"
)

var (
	ErrAPITokenDisplayNameRequired = errors.New("API token display name is required")
	ErrAPITokenDisplayNameTooLong  = errors.New("API token display name is too long")
	ErrAPITokenDisplayNameInvalid  = errors.New("API token display name is invalid")
	ErrAPITokenNotFound            = errors.New("API token not found")
	ErrAPITokenApplicationRequired = errors.New("API token application is required")
	ErrAPITokenApplicationNotFound = errors.New("API token application was not found")
	ErrAPITokenExpiryInvalid       = errors.New("API token expiry is invalid")
	ErrAPITokenInvalid             = errors.New("API token is invalid")
	ErrAPITokenExpired             = errors.New("API token has expired")
	ErrAPITokenScopeInvalid        = errors.New("API token scope is invalid")
	ErrAPITokenHashMissing         = errors.New("API token verifier is missing")
)

// APIToken is one operator-managed machine credential. Only metadata is
// persisted; the plaintext token is returned transiently at creation time and
// must never be stored or logged. A token authorizes one-off service runs in
// its pinned application, nothing else: it cannot read environment files,
// run arbitrary commands, or reach other applications.
type APIToken struct {
	ID            int64
	DisplayName   string
	Prefix        string
	ApplicationID int64
	Scope         string
	CreatedAt     time.Time
	ExpiresAt     *time.Time
	LastUsedAt    *time.Time
}

// APITokenInput carries the operator-supplied values for token creation.
// ExpiresInDays of zero creates a token that never expires; otherwise it must
// be within 1 and MaxAPITokenLifetimeDays.
type APITokenInput struct {
	DisplayName   string
	ApplicationID int64
	ExpiresInDays int
}

// APITokenSetup carries the one-time plaintext handoff. Plaintext must never
// be persisted or logged.
type APITokenSetup struct {
	Token     APIToken
	Plaintext string
}

// ValidateAPITokenDisplayName trims and validates the operator-supplied label
// shown in the API token list. The value is display-only.
func ValidateAPITokenDisplayName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", ErrAPITokenDisplayNameRequired
	}
	if utf8.RuneCountInString(name) > MaxAPITokenDisplayNameLength {
		return "", ErrAPITokenDisplayNameTooLong
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return "", ErrAPITokenDisplayNameInvalid
		}
	}
	return name, nil
}

// ValidateAPITokenExpiry validates the requested lifetime in days. Zero means
// the token never expires.
func ValidateAPITokenExpiry(days int) error {
	if days < 0 || days > MaxAPITokenLifetimeDays {
		return ErrAPITokenExpiryInvalid
	}
	return nil
}

// FormatAPIToken renders the presented token from its secret hex encoding.
func FormatAPIToken(secretHex string) string {
	return APITokenPrefix + secretHex
}

// SplitAPIToken validates the shape of a presented token without touching
// storage. It reports whether the value could be a Redlaunch API token.
func SplitAPIToken(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, APITokenPrefix) {
		return "", false
	}
	secret := strings.TrimPrefix(trimmed, APITokenPrefix)
	if len(secret) != APITokenSecretBytes*2 {
		return "", false
	}
	if _, err := hex.DecodeString(secret); err != nil {
		return "", false
	}
	return trimmed, true
}

// Expired reports whether the token is past its expiry. Tokens without an
// expiry never expire.
func (t APIToken) Expired(now time.Time) bool {
	return t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)
}
