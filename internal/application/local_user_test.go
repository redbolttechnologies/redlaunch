package application

import (
	"strings"
	"testing"
)

func TestValidateUsername(t *testing.T) {
	for input, want := range map[string]string{" ADMIN ": "admin", "Dev.User_1-2": "dev.user_1-2", strings.Repeat("a", 64): strings.Repeat("a", 64)} {
		got, err := ValidateUsername(input)
		if err != nil || got != want {
			t.Errorf("ValidateUsername(%q) = %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"", " ", strings.Repeat("a", 65), "../root", ".admin", "a/b", "a b", "a@b", "a\x00b", "é", "<script>"} {
		if _, err := ValidateUsername(input); err == nil {
			t.Errorf("accepted invalid username %q", input)
		}
	}
}
