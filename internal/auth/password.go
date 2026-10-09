package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	passwordMemory      = 19 * 1024 // KiB; OWASP's minimum Argon2id configuration.
	passwordIterations  = 2
	passwordParallelism = 1
	passwordSaltSize    = 16
	passwordKeySize     = 32
)

var ErrPasswordInvalid = errors.New("password must contain 8–128 characters without line breaks")

func ValidatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 8 || utf8.RuneCountInString(password) > 128 || strings.ContainsAny(password, "\r\n") {
		return ErrPasswordInvalid
	}
	return nil
}

// HashPassword encodes the algorithm, cost, random salt and key in PHC format.
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, passwordSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("could not generate password salt")
	}
	key := argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemory, passwordParallelism, passwordKeySize)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", passwordMemory, passwordIterations, passwordParallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(password, encoded string) bool {
	if ValidatePassword(password) != nil || len(encoded) > 256 {
		return false
	}
	parts := strings.Split(encoded, "$")
	// Accept only our supported costs, bounding memory/CPU even for a damaged DB.
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=19456,t=2,p=1" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != passwordSaltSize {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) != passwordKeySize {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemory, passwordParallelism, passwordKeySize)
	return subtle.ConstantTimeCompare(got, want) == 1
}
