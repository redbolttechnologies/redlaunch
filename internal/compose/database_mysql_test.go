package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMySQLDocker records the arguments and stdin of the final docker exec and
// answers Compose config calls with an empty document and everything else
// (ownership checks) with empty output.
func fakeMySQLDocker(t *testing.T, execOutput string) (projectDir, binary string) {
	t.Helper()
	projectDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary = filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\ncase \" $* \" in *\" config \"*) printf '{}\\n';; *\" exec \"*) printf '%s\\n' \"$@\" > \"${0%/*}/args\"; cat > \"${0%/*}/stdin\"; printf '%s' '" + execOutput + "';; *) ;; esac\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return projectDir, binary
}

func recordedMySQLExec(t *testing.T, binary string) (args []string, stdin string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n"), string(input)
}

func TestCommandRunnerExecMySQLSQLSendsStatementsOnStdin(t *testing.T) {
	for _, test := range []struct {
		flavor   MySQLFlavor
		client   string
		password string
	}{
		{MySQLFlavorMariaDB, "mariadb", "MARIADB_ROOT_PASSWORD"},
		{MySQLFlavorMySQL, "mysql", "MYSQL_ROOT_PASSWORD"},
	} {
		t.Run(string(test.flavor), func(t *testing.T) {
			projectDir, binary := fakeMySQLDocker(t, " 1 \n")
			statement := "CREATE USER 'shop'@'%' IDENTIFIED BY 'o''brien'; DROP DATABASE `x`"
			output, err := (CommandRunner{Binary: binary}).ExecMySQLSQL(context.Background(), projectDir, "db", test.flavor, statement)
			if err != nil {
				t.Fatal(err)
			}
			if output != "1" {
				t.Fatalf("output = %q, want trimmed result", output)
			}
			args, stdin := recordedMySQLExec(t, binary)
			script := `MYSQL_PWD="$` + test.password + `" exec ` + test.client + ` --protocol=tcp --host=127.0.0.1 --user=root --default-character-set=utf8mb4 --batch --skip-column-names`
			want := expectedComposeArguments(projectDir, "exec", "-T", "db", "sh", "-c", script)
			if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("exec arguments = %#v, want %#v", args, want)
			}
			if stdin != statement {
				t.Fatalf("stdin = %q, want the statement", stdin)
			}
			for _, arg := range args {
				if strings.Contains(arg, "o''brien") {
					t.Fatalf("statement leaked into the command line: %#v", args)
				}
			}
		})
	}
}

func TestCommandRunnerExecMySQLSQLRejectsInvalidArguments(t *testing.T) {
	projectDir, binary := fakeMySQLDocker(t, "")
	runner := CommandRunner{Binary: binary}
	if _, err := runner.ExecMySQLSQL(context.Background(), projectDir, "db", MySQLFlavorMySQL, " "); err == nil {
		t.Fatal("empty statement error = nil")
	}
	if _, err := runner.ExecMySQLSQL(context.Background(), projectDir, "", MySQLFlavorMySQL, "SELECT 1"); err == nil {
		t.Fatal("empty service error = nil")
	}
	if _, err := runner.ExecMySQLSQL(context.Background(), projectDir, "db", MySQLFlavor("oracle"), "SELECT 1"); err == nil {
		t.Fatal("unknown flavor error = nil")
	}
}

func TestCommandRunnerBackupMySQLPassesDatabaseAsOneArgument(t *testing.T) {
	projectDir, binary := fakeMySQLDocker(t, "-- SQL dump\n")
	destination := filepath.Join(t.TempDir(), "backup.sql")
	database := "-weird name; rm -rf /"
	if err := (CommandRunner{Binary: binary}).BackupMySQL(context.Background(), projectDir, "db", MySQLFlavorMariaDB, database, destination); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "-- SQL dump\n" {
		t.Fatalf("backup contents = %q", contents)
	}
	args, _ := recordedMySQLExec(t, binary)
	script := `MYSQL_PWD="$MARIADB_ROOT_PASSWORD" exec mariadb-dump --protocol=tcp --host=127.0.0.1 --user=root --default-character-set=utf8mb4 --single-transaction --routines --events --triggers --databases -- "$0"`
	want := expectedComposeArguments(projectDir, "exec", "-T", "db", "sh", "-c", script, database)
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("backup arguments = %#v, want %#v", args, want)
	}
}

func TestCommandRunnerRestoreMySQLReadsManagedBackup(t *testing.T) {
	projectDir, binary := fakeMySQLDocker(t, "")
	source := filepath.Join(t.TempDir(), "backup.sql")
	if err := os.WriteFile(source, []byte("restore this\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (CommandRunner{Binary: binary}).RestoreMySQL(context.Background(), projectDir, "db", MySQLFlavorMySQL, source); err != nil {
		t.Fatal(err)
	}
	args, stdin := recordedMySQLExec(t, binary)
	script := `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql --protocol=tcp --host=127.0.0.1 --user=root --default-character-set=utf8mb4`
	want := expectedComposeArguments(projectDir, "exec", "-T", "db", "sh", "-c", script)
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("restore arguments = %#v, want %#v", args, want)
	}
	if stdin != "restore this\n" {
		t.Fatalf("restore stdin = %q", stdin)
	}
}
