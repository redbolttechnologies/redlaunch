package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueryPostgresDatabaseUsesExplicitArguments(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "compose.yml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\ncase \" $* \" in *\" config \"*) printf '{}\\n';; *\" exec \"*) printf 'value\\n1\\n';; *) printf '\\n';; esac\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	sql := "SELECT '$(touch unsafe); secret';"
	output, err := (CommandRunner{Binary: binary}).QueryPostgresDatabase(context.Background(), project, "postgres", "shop", "shop owner", sql)
	if err != nil {
		t.Fatal(err)
	}
	if output != "value\n1\n" {
		t.Fatalf("unexpected CSV: %q", output)
	}
	args, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	want := expectedComposeArguments(project, "exec", "-T", "-e", "PGOPTIONS=-c statement_timeout=30000 -c lock_timeout=5000", "postgres", "psql", "-X", "--csv", "--quiet", "--pset", "footer=off", "--pset", "null=NULL", "--set", "ON_ERROR_STOP=1", "--username=shop owner", "--dbname=shop", "-c", sql)
	if strings.TrimSpace(string(args)) != strings.Join(want, "\n") {
		t.Fatalf("unexpected query arguments: %s", args)
	}
}

func TestQueryPostgresDatabaseRejectsPSQLCommands(t *testing.T) {
	for _, sql := range []string{`\! whoami`, " \n\\i secrets.env", "", "SELECT\x00"} {
		if _, err := (CommandRunner{}).QueryPostgresDatabase(t.Context(), t.TempDir(), "postgres", "shop", "owner", sql); err == nil {
			t.Fatal("accepted psql command or invalid SQL")
		}
	}
}

func TestQueryPostgresDatabaseIntegration(t *testing.T) {
	if os.Getenv("REDLAUNCH_DOCKER_CONFIG_TEST") != "1" {
		t.Skip("set REDLAUNCH_DOCKER_CONFIG_TEST=1 to run disposable Docker checks")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	name := fmt.Sprintf("redbolt-workspace-test-%d", time.Now().UnixNano())
	project := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"vars.env":    "POSTGRES_USER=admin\nPOSTGRES_DB=admin\n",
		"secrets.env": "POSTGRES_PASSWORD=disposable-test-password\n",
		"compose.yml": `services:
  postgres:
    image: postgres:17
    container_name: ` + name + `
    labels:
      redlaunch.managed: "true"
    env_file:
      - vars.env
      - secrets.env
    tmpfs:
      - /var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "admin", "-d", "admin"]
      interval: 1s
      timeout: 5s
      retries: 30
`,
	}
	for file, contents := range files {
		if err := os.WriteFile(filepath.Join(project, file), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	compose := func(ctx context.Context, args ...string) error {
		cmd := composeCommand(ctx, "docker", project, filepath.Join(project, "compose.yml"), args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("disposable Compose operation: %w: %s", err, output)
		}
		return nil
	}
	if err := compose(ctx, "config", "--quiet"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := compose(cleanup, "down", "--volumes", "--remove-orphans"); err != nil {
			t.Error(err)
		}
	})
	if err := compose(ctx, "up", "-d", "--wait", "--wait-timeout", "60"); err != nil {
		t.Fatal(err)
	}
	runner := CommandRunner{}
	for _, sql := range []string{`CREATE ROLE shop LOGIN`, `CREATE DATABASE shop OWNER shop`} {
		if _, err := runner.ExecPostgresSQL(ctx, project, "postgres", sql); err != nil {
			t.Fatalf("could not initialize disposable database: %v", err)
		}
	}
	query := func(sql string) (string, error) {
		return runner.QueryPostgresDatabase(ctx, project, "postgres", "shop", "shop", sql)
	}
	if output, err := query(`CREATE TABLE sample (value text); INSERT INTO sample VALUES ('hello, world');`); err != nil || output != "" {
		t.Fatalf("write output: %q, %v", output, err)
	}
	if output, err := query(`SELECT * FROM sample LIMIT 1000;`); err != nil || output != "value\n\"hello, world\"\n" {
		t.Fatalf("CSV output: %q, %v", output, err)
	}
	if output, err := query(`SELECT * FROM sample WHERE false;`); err != nil || output != "value\n" {
		t.Fatalf("empty result output: %q, %v", output, err)
	}
	for _, sql := range []string{`COPY sample TO '/tmp/unsafe';`, `COPY sample TO PROGRAM 'true';`, `SET ROLE admin;`} {
		if _, err := query(sql); err == nil {
			t.Fatal("database owner gained administrative/host access")
		}
	}
}
