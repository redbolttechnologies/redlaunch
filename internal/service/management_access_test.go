package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/config"
)

func accessModeFixture(t *testing.T, mode, script string) (*SelfUpdateService, string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services:\n  app:\n    image: redlaunch:local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("# startup settings\r\nexport MANAGEMENT_ACCESS_MODE = 'ssh-only' # keep comment\r\nTEST_ONLY_VALUE=\"${UNCHANGED:-sample}\"\r\nAPP_BIND_ADDRESS=127.0.0.1\r\n"), 0640); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	docker := filepath.Join(dir, "docker-test")
	source := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\ncase \"$*\" in\n  inspect*) echo 'No such container' >&2; exit 1;;\n  *'config --format json'*) %s\n    printf '%%s\\n' '{\"services\":{\"app\":{\"environment\":{\"MANAGEMENT_ACCESS_MODE\":\"%s\"}}}}'; exit 0;;\nesac\n%s\nexit 0\n", calls, script, mode, script)
	if err := os.WriteFile(docker, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewSelfUpdateService(dir, "", docker)
	if err != nil {
		t.Fatal(err)
	}
	return s, path, calls
}

func TestManagementAccessModePreservesConfigurationAndQueuesOnlyRecreation(t *testing.T) {
	for _, mode := range []string{config.AccessModeSSHOnly, config.AccessModeManagedHTTPS} {
		t.Run(mode, func(t *testing.T) {
			s, path, calls := accessModeFixture(t, mode, `case "$*" in *config*|*'up -d --no-build'*) test -z "${MANAGEMENT_ACCESS_MODE+x}" && test -z "${GOOGLE_CLIENT_SECRET+x}" || exit 1;; esac`)
			t.Setenv("MANAGEMENT_ACCESS_MODE", "stale-value")
			t.Setenv("GOOGLE_CLIENT_SECRET", "test-only-value")
			if err := s.QueueManagementAccessMode(t.Context(), mode); err != nil {
				t.Fatal(err)
			}
			contents, _ := os.ReadFile(path)
			if !strings.Contains(string(contents), "# keep comment\r\nTEST_ONLY_VALUE=\"${UNCHANGED:-sample}\"\r\nAPP_BIND_ADDRESS=127.0.0.1\r\n") {
				t.Fatal("unrelated configuration or formatting changed")
			}
			value, err := findEnvironmentVariable(string(contents), "MANAGEMENT_ACCESS_MODE")
			if err != nil || value != mode {
				t.Fatal("startup mode was not saved")
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0600 {
				t.Fatal("configuration permissions are not private")
			}
			commands, _ := os.ReadFile(calls)
			if !strings.Contains(string(commands), "config --format json") || !strings.Contains(string(commands), "manager-restart/compose.yml up -d --no-build") {
				t.Fatalf("missing validation or helper launch: %s", commands)
			}
			if strings.Contains(string(commands), "--build") || strings.Contains(string(commands), "git") {
				t.Fatal("mode change rebuilt or updated code")
			}
			project := filepath.Join(s.projectsRoot, "core", "manager-restart")
			yaml, _ := os.ReadFile(filepath.Join(project, "compose.yml"))
			for _, part := range []string{"redlaunch.managed: \"true\"", "env_file: [vars.env, secrets.env]", "container_name: redbolt-redlaunch-updater", "management-restart-run"} {
				if !strings.Contains(string(yaml), part) {
					t.Fatalf("helper missing %s", part)
				}
			}
			for _, name := range []string{varsEnvFile, secretsEnvFile} {
				data, err := os.ReadFile(filepath.Join(project, name))
				if err != nil || len(data) != 0 {
					t.Fatal("helper environment must be present and empty")
				}
			}
		})
	}
}

func TestManagementAccessModeRollsBackFailuresWithoutCommandOutput(t *testing.T) {
	for _, script := range []string{
		"case \"$*\" in *'config --format json'*) echo 'TEST_ONLY_OUTPUT' >&2; exit 1;; esac",
		"case \"$*\" in *'up -d --no-build'*) echo 'TEST_ONLY_OUTPUT' >&2; exit 1;; esac",
	} {
		t.Run(script, func(t *testing.T) {
			s, path, _ := accessModeFixture(t, "managed-https", script)
			original, _ := os.ReadFile(path)
			err := s.QueueManagementAccessMode(t.Context(), "managed-https")
			if err == nil || strings.Contains(err.Error(), "TEST_ONLY_OUTPUT") {
				t.Fatal("failure must return a safe error")
			}
			after, _ := os.ReadFile(path)
			if string(original) != string(after) {
				t.Fatal("configuration was not restored")
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0640 {
				t.Fatal("rollback did not restore permissions")
			}
		})
	}
}

func TestManagementAccessModeRejectsUnsafeConfiguration(t *testing.T) {
	for _, input := range []string{"MANAGEMENT_ACCESS_MODE=ssh-only\nMANAGEMENT_ACCESS_MODE=managed-https\n", "TEST_ONLY_VALUE='multi\nline'\n"} {
		s, path, _ := accessModeFixture(t, "managed-https", "")
		os.WriteFile(path, []byte(input), 0600)
		if err := s.QueueManagementAccessMode(t.Context(), "managed-https"); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
		after, _ := os.ReadFile(path)
		if string(after) != input {
			t.Fatal("rejected configuration changed")
		}
	}
	s, path, _ := accessModeFixture(t, "managed-https", "")
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("TEST_ONLY_VALUE=sample\n"), 0600)
	os.Remove(path)
	os.Symlink(outside, path)
	if err := s.QueueManagementAccessMode(t.Context(), "managed-https"); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := s.QueueManagementAccessMode(t.Context(), "managed-https; echo bad"); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestManagementAccessModeAppendsMissingSettingAndRejectsComposeOverride(t *testing.T) {
	s, path, _ := accessModeFixture(t, "managed-https", "")
	os.WriteFile(path, []byte("# untouched\nAPP_BIND_ADDRESS=127.0.0.1\n"), 0600)
	if err := s.QueueManagementAccessMode(t.Context(), "managed-https"); err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(contents), "# untouched\nAPP_BIND_ADDRESS=127.0.0.1\n") {
		t.Fatal("append changed existing values")
	}
	s, path, _ = accessModeFixture(t, "ssh-only", "")
	before, _ := os.ReadFile(path)
	if err := s.QueueManagementAccessMode(t.Context(), "managed-https"); err == nil {
		t.Fatal("hard-coded Compose override accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("override failure did not roll back")
	}
}

func TestManagementRestartHelperRecreatesOnlyAppAndHonorsCancellation(t *testing.T) {
	s, _, calls := accessModeFixture(t, "managed-https", "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.RunManagementRestart(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled helper ran")
	}
	if err := s.RunManagementRestart(t.Context()); err != nil {
		t.Fatal(err)
	}
	commands, _ := os.ReadFile(calls)
	if !strings.HasSuffix(strings.TrimSpace(string(commands)), "up -d --no-build --pull never --no-deps --force-recreate app") {
		t.Fatal("restart did not target only app")
	}
}

func TestManagementRestartProjectComposeConfig(t *testing.T) {
	if os.Getenv("REDLAUNCH_DOCKER_CONFIG_TEST") != "1" {
		t.Skip("opt-in Docker Compose validation")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker CLI is not installed")
	}
	s, _, _ := accessModeFixture(t, "managed-https", "")
	directory := filepath.Join(s.directory, "checkout $literal with spaces")
	project, err := s.writeManagementRestartProject(directory)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(docker, "compose", "--project-directory", project, "config", "--format", "json")
	output, err := command.Output()
	if err != nil {
		t.Fatal("generated helper Compose configuration is invalid", err)
	}
	var resolved struct {
		Services map[string]struct {
			Command    []string `json:"command"`
			WorkingDir string   `json:"working_dir"`
		} `json:"services"`
	}
	if err := json.Unmarshal(output, &resolved); err != nil {
		t.Fatal(err)
	}
	helper := resolved.Services["restart"]
	// `compose config` retains escaped dollars for round-trip serialization;
	// Compose unescapes them when creating the container.
	if len(helper.Command) != 2 || strings.ReplaceAll(helper.Command[1], "$$", "$") != directory || strings.ReplaceAll(helper.WorkingDir, "$$", "$") != directory {
		t.Fatalf("Compose changed the literal helper path: command=%q working_dir=%q expected=%q", helper.Command, helper.WorkingDir, directory)
	}
}

func TestManagementAccessModeSingleFlightsWithUpdates(t *testing.T) {
	s, path, _ := accessModeFixture(t, "managed-https", "")
	s.mu.Lock()
	err := s.QueueManagementAccessMode(t.Context(), "managed-https")
	s.mu.Unlock()
	if err == nil {
		t.Fatal("concurrent installation change accepted")
	}
	if err := s.QueueManagementAccessMode(t.Context(), "managed-https"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := s.QueueManagementAccessMode(t.Context(), "ssh-only"); err == nil {
		t.Fatal("second restart accepted")
	}
	if err := s.QueueUpdateWithProgress(t.Context(), nil); err == nil {
		t.Fatal("update accepted after restart queued")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("duplicate request changed saved configuration")
	}
}

func TestManagementAccessModeDoesNotOverwriteConcurrentOperatorEdits(t *testing.T) {
	s, path, _ := accessModeFixture(t, "managed-https", `case "$*" in *'config --format json'*) printf 'OPERATOR_SETTING=keep\n' >> "$(dirname "$0")/.env"; exit 1;; esac`)
	if err := s.QueueManagementAccessMode(t.Context(), "managed-https"); err == nil || !strings.Contains(err.Error(), "changed concurrently") {
		t.Fatal("concurrent edit not detected")
	}
	contents, _ := os.ReadFile(path)
	if !strings.Contains(string(contents), "OPERATOR_SETTING=keep") {
		t.Fatal("operator edit discarded")
	}
}
