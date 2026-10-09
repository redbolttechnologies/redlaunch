package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise actual prompts, configuration and credential transport, excluding
// host SSH-user setup. Docker is a fixture executable; no daemon state is used.
func TestSetupScriptLocalDefaultAndOptionalGoogle(t *testing.T) {
	source, err := os.ReadFile("../../scripts/setup.sh")
	if err != nil {
		t.Fatal(err)
	}
	prefix, _, found := strings.Cut(string(source), "# Dedicated login user")
	if !found {
		t.Fatal("setup boundary missing")
	}
	_, suffix, found := strings.Cut(string(source), "\nensure_redlaunch_ssh_user\nensure_redlaunch_host_keys\n")
	if !found {
		t.Fatal("setup provisioning boundary missing")
	}
	prefix += suffix
	for _, google := range []bool{false, true} {
		name := "local-only"
		if google {
			name = "with-google"
		}
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			bin := filepath.Join(directory, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			docker := `#!/usr/bin/env bash
set -eu
[[ -z ${local_password+x} && -z ${password_confirmation+x} ]] || exit 1
printf '%s\n' "$*" >> "$SETUP_CALLS"
if [[ "$*" == *--password-stdin* ]]; then
 IFS= read -r value
 printf '%s' "$value" > "$SETUP_INPUT"
fi
if [[ "$*" == *auth-validate-password* && "$*" == *"--email not-an-email "* ]]; then exit 3; fi
`
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0o700); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(directory, "setup.sh")
			if err := os.WriteFile(script, []byte(prefix), 0o700); err != nil {
				t.Fatal(err)
			}
			password := " a '$#\\ passphrase "
			input := "not-an-email\nshort\n" + password + "\nwrong confirmation\n" + password + "\n" + password + "\n\n"
			if google {
				input = strings.TrimSuffix(input, "\n") + "y\nclient-id\nclient-secret\n"
			}
			input += "admin@example.com\n"
			command := exec.Command("bash", script)
			command.Dir = directory
			command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "SETUP_CALLS="+filepath.Join(directory, "calls"), "SETUP_INPUT="+filepath.Join(directory, "input"), "local_password=old-exported-value", "password_confirmation=old-exported-value")
			command.Stdin = strings.NewReader(input)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("setup failed: %v (output omitted to protect input)", err)
			}
			if strings.Contains(string(output), password) {
				t.Fatal("password printed")
			}
			if !strings.Contains(string(output), "Passwords do not match") || !strings.Contains(string(output), "First user email address:") {
				t.Fatal("missing prompts")
			}
			env, err := os.ReadFile(filepath.Join(directory, ".env"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(env), password) || strings.Contains(string(env), "PASSWORD=") {
				t.Fatal("password persisted in configuration")
			}
			want := "GOOGLE_CLIENT_ID=''"
			if google {
				want = "GOOGLE_CLIENT_ID='client-id'"
			}
			if !strings.Contains(string(env), want) {
				t.Fatal("wrong Google settings")
			}
			if !google && strings.Contains(string(output), "Google Client ID:") {
				t.Fatal("optional Google prompted by default")
			}
			got, err := os.ReadFile(filepath.Join(directory, "input"))
			if err != nil || string(got) != password {
				t.Fatal("password altered before stdin transport", err)
			}
			calls, err := os.ReadFile(filepath.Join(directory, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "auth-create-user --email admin@example.com --password-stdin") || strings.Contains(string(calls), password) {
				t.Fatal("unsafe/missing provisioning command")
			}
			if strings.Contains(string(calls), "auth-add-email") {
				t.Fatal("Google needs no separate allowlist provisioning")
			}
			for _, file := range []string{".env", "vars.env", "secrets.env"} {
				info, err := os.Stat(filepath.Join(directory, file))
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatal("unsafe configuration permissions", file, err)
				}
			}
		})
	}
}
