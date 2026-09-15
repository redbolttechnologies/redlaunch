package config

import (
	"testing"
)

func TestLoadAcceptsSSHKeysConfiguration(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	t.Setenv("SSH_AUTHORIZED_KEYS_PATH", "/root/.ssh/authorized_keys")
	t.Setenv("SSH_KEYS_USERNAME", "deploy")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SSHAuthorizedKeysPath != "/root/.ssh/authorized_keys" || cfg.SSHKeysUsername != "deploy" {
		t.Fatalf("SSH keys configuration = %#v", cfg)
	}
}

func TestLoadDefaultsSSHKeysUsername(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	t.Setenv("SSH_KEYS_USERNAME", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SSHKeysUsername != "root" {
		t.Fatalf("SSH_KEYS_USERNAME = %q, want root", cfg.SSHKeysUsername)
	}
}

func TestLoadRejectsInvalidSSHKeysConfiguration(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "relative path", key: "SSH_AUTHORIZED_KEYS_PATH", value: "keys/authorized_keys"},
		{name: "root path", key: "SSH_AUTHORIZED_KEYS_PATH", value: "/"},
		{name: "username", key: "SSH_KEYS_USERNAME", value: "bad user"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setBackupConfigTestEnvironment(t)
			t.Setenv(testCase.key, testCase.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() returned nil error for invalid %s", testCase.key)
			}
		})
	}
}
