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

func TestValidateSSHKeyPermitOpen(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr error
	}{
		{name: "empty means unrestricted", value: "", want: ""},
		{name: "whitespace means unrestricted", value: "   ", want: ""},
		{name: "loopback target", value: "127.0.0.1:5432", want: "127.0.0.1:5432"},
		{name: "non-loopback host rejected", value: "db:5432", wantErr: ErrSSHKeyPermitOpenInvalid},
		{name: "public host rejected", value: "203.0.113.10:5432", wantErr: ErrSSHKeyPermitOpenInvalid},
		{name: "missing port rejected", value: "127.0.0.1", wantErr: ErrSSHKeyPermitOpenInvalid},
		{name: "port zero rejected", value: "127.0.0.1:0", wantErr: ErrSSHKeyPermitOpenInvalid},
		{name: "port too large rejected", value: "127.0.0.1:65536", wantErr: ErrSSHKeyPermitOpenInvalid},
		{name: "non-numeric port rejected", value: "127.0.0.1:postgres", wantErr: ErrSSHKeyPermitOpenInvalid},
		{name: "options injection rejected", value: `127.0.0.1:5432",command="id`, wantErr: ErrSSHKeyPermitOpenInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateSSHKeyPermitOpen(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateSSHKeyPermitOpen() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ValidateSSHKeyPermitOpen() = %q, want %q", got, tt.want)
			}
		})
	}
}
