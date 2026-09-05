package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandRunnerBackupPostgreSQLUsesContainerEnvironmentAndWritesSQL(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\nprintf '%s\\n' '-- SQL dump'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "backup.sql")
	if err := (CommandRunner{Binary: binary}).BackupPostgreSQL(context.Background(), projectDir, "db", destination); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "-- SQL dump\n" {
		t.Fatalf("backup contents = %q, want SQL dump", contents)
	}
	args, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(args)), "\n")
	want := []string{"compose", "-f", "compose.yml", "exec", "-T", "db", "sh", "-c", postgresDumpScript}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("backup arguments = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerRestorePostgreSQLReadsManagedBackup(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\ncat > \"${0%/*}/stdin\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "backup.sql")
	if err := os.WriteFile(source, []byte("restore this\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (CommandRunner{Binary: binary}).RestorePostgreSQL(context.Background(), projectDir, "db", source); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != "restore this\n" {
		t.Fatalf("restore stdin = %q, want source contents", stdin)
	}
	args, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(args)), "\n")
	want := []string{"compose", "-f", "compose.yml", "exec", "-T", "db", "sh", "-c", postgresRestoreScript}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("restore arguments = %#v, want %#v", got, want)
	}
}
