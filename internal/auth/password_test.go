package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashSaltAndVerification(t *testing.T) {
	password := " a '$#\\ passphrase "
	first, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatal("hashes must use Argon2id and unique salts")
	}
	if !verifyPassword(password, first) || verifyPassword(strings.TrimSpace(password), first) || verifyPassword("incorrect password", first) {
		t.Fatal("incorrect password comparison")
	}
	for _, hash := range []string{"", "$argon2id$v=19$m=999999999,t=2,p=1$x$x", "$argon2id$v=19$m=19456,t=2,p=1$bad$bad", strings.Repeat("x", 300)} {
		if verifyPassword(password, hash) {
			t.Fatal("accepted malformed hash")
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	for _, password := range []string{"password", "        ", "12345678", "éééééééé", strings.Repeat("界", 128), "quotes'$#\\"} {
		if err := ValidatePassword(password); err != nil {
			t.Errorf("valid password rejected: %v", err)
		}
	}
	for _, password := range []string{"", "1234567", strings.Repeat("x", 129), "password\n", "password\r", "password\xff"} {
		if ValidatePassword(password) == nil {
			t.Error("invalid password accepted")
		}
	}
}
