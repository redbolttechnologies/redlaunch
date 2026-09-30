package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommandRunnerExecPostgresSQLPassesStatementAsSingleArgument ensures the
// SQL text travels as one exec argument ($0) so user-controlled values are
// never parsed by the container shell, and authentication reuses the
// container environment instead of the host command line.
func TestCommandRunnerExecPostgresSQLPassesStatementAsSingleArgument(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\ncase \" $* \" in *\" config \"*) printf '{}\\n';; *\" exec \"*) printf '%s\\n' ' 1 ';; *) printf '\\n';; esac\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	statement := `ALTER ROLE "shop" WITH PASSWORD 'o''brien'; rm -rf /`
	output, err := (CommandRunner{Binary: binary}).ExecPostgresSQL(context.Background(), projectDir, "db", statement)
	if err != nil {
		t.Fatal(err)
	}
	if output != "1" {
		t.Fatalf("exec output = %q, want trimmed query result", output)
	}
	args, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(args)), "\n")
	want := expectedComposeArguments(projectDir, "exec", "-T", "db", "sh", "-c", postgresExecWrapper, statement)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("exec arguments = %#v, want %#v", got, want)
	}
	for _, arg := range got {
		if strings.Contains(arg, "o''brien") && arg != statement {
			t.Fatalf("credential statement leaked outside its single argument: %#v", got)
		}
	}
}

func TestCommandRunnerExecPostgresSQLRejectsEmptyStatement(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (CommandRunner{}).ExecPostgresSQL(context.Background(), projectDir, "db", "  "); err == nil {
		t.Fatal("ExecPostgresSQL(empty) error = nil, want an error")
	}
	if _, err := (CommandRunner{}).ExecPostgresSQL(context.Background(), projectDir, "", "SELECT 1"); err == nil {
		t.Fatal("ExecPostgresSQL(empty service) error = nil, want an error")
	}
}

func TestPostgresExecWrapperUsesContainerEnvironment(t *testing.T) {
	for _, expected := range []string{`"$POSTGRES_USER"`, `"$POSTGRES_DB"`, `"$0"`, "ON_ERROR_STOP=1"} {
		if !strings.Contains(postgresExecWrapper, expected) {
			t.Fatalf("postgres exec wrapper = %q, want %q", postgresExecWrapper, expected)
		}
	}
}
