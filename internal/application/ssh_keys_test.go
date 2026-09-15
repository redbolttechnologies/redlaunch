package application

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSSHKeyDisplayName(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr error
	}{
		{name: "trims whitespace", value: "  GHA migrator workflow access  ", want: "GHA migrator workflow access"},
		{name: "required", value: "   ", wantErr: ErrSSHKeyDisplayNameRequired},
		{name: "too long", value: strings.Repeat("a", MaxSSHKeyDisplayNameLength+1), wantErr: ErrSSHKeyDisplayNameTooLong},
		{name: "control character", value: "key\nname", wantErr: ErrSSHKeyDisplayNameInvalid},
		{name: "allows shell metacharacters as display only", value: "deploy; rm -rf /", want: "deploy; rm -rf /"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateSSHKeyDisplayName(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateSSHKeyDisplayName() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ValidateSSHKeyDisplayName() = %q, want %q", got, tt.want)
			}
		})
	}
}
