package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

// This opt-in test uses a disposable cluster with no host ports or persistent
// volumes. It checks real Postgres ACLs, not just the generated SQL text.
func TestManagedDatabaseCreationPostgresPermissions(t *testing.T) {
	if os.Getenv("REDLAUNCH_DOCKER_CONFIG_TEST") != "1" {
		t.Skip("set REDLAUNCH_DOCKER_CONFIG_TEST=1 to run disposable Docker checks")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	project := fmt.Sprintf("redlaunch-db-test-%d", time.Now().UnixNano())
	directory := filepath.Join(t.TempDir(), "core", "postgres-test")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"vars.env":    "POSTGRES_USER=redlaunch\nPOSTGRES_DB=redlaunch\n",
		"secrets.env": "POSTGRES_PASSWORD=disposable-integration-password\n",
		"compose.yml": `services:
  postgres:
    image: postgres:17
    labels:
      redlaunch.managed: "true"
    env_file:
      - vars.env
      - secrets.env
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "redlaunch", "-d", "redlaunch"]
      interval: 1s
      timeout: 5s
      retries: 30
`,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	compose := func(ctx context.Context, args ...string) (string, error) {
		command := exec.CommandContext(ctx, "docker", append([]string{"compose", "--project-directory", directory, "-p", project}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("Docker test command failed: %w: %s", err, output)
		}
		return strings.TrimSpace(string(output)), nil
	}
	if _, err := compose(ctx, "config", "--quiet"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := compose(cleanup, "down", "--volumes", "--remove-orphans"); err != nil {
			t.Error("failed to clean up disposable Postgres project")
		}
	})
	if _, err := compose(ctx, "up", "-d", "--wait", "--wait-timeout", "60"); err != nil {
		t.Fatal(err)
	}
	sql := func(statement string) (string, error) {
		return compose(ctx, "exec", "-T", "postgres", "psql", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-U", "redlaunch", "-d", "redlaunch", "-c", statement)
	}
	if _, err := sql(`CREATE ROLE legacy LOGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := sql(`CREATE DATABASE legacy_db OWNER redlaunch`); err != nil {
		t.Fatal(err)
	}
	runner := &managedDatabasesRunnerFake{running: true, execFunc: sql}
	managed := enabledManagedDatabasesForCreation(t, runner)
	first, err := managed.CreateDatabase(ctx, application.ManagedDatabaseCreateInput{Name: "analytics"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Credentials == nil {
		t.Fatal("no credentials for new role")
	}
	second, err := managed.CreateDatabase(ctx, application.ManagedDatabaseCreateInput{Name: "other", Owner: "other_user"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Credentials == nil {
		t.Fatal("no credentials for second role")
	}
	expectedSQL := `SELECT has_database_privilege('analytics', 'analytics', 'CONNECT')
 AND NOT has_database_privilege('analytics', 'redlaunch', 'CONNECT')
 AND NOT has_database_privilege('analytics', 'legacy_db', 'CONNECT')
 AND NOT has_database_privilege('analytics', 'other', 'CONNECT')
 AND NOT has_database_privilege('analytics', 'template1', 'CONNECT')
 AND NOT has_database_privilege('other_user', 'analytics', 'CONNECT')
 AND has_database_privilege('legacy', 'legacy_db', 'CONNECT')
 AND has_database_privilege('legacy', 'legacy_db', 'TEMPORARY')
 AND NOT (SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls FROM pg_roles WHERE rolname = 'analytics')`
	output, err := sql(expectedSQL)
	if err != nil || output != "t" {
		t.Fatal("real Postgres privileges did not isolate new roles or preserve existing roles")
	}
	// Verify actual authenticated read/write operations as the generated user.
	output, err = compose(ctx, "exec", "-T", "-e", "PGPASSWORD="+first.Credentials.Password, "postgres", "psql", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-h", "127.0.0.1", "-U", "analytics", "-d", "analytics", "-c", "CREATE TABLE sample (value integer); INSERT INTO sample VALUES (1); UPDATE sample SET value=2; SELECT value FROM sample; DELETE FROM sample;")
	if err != nil || !strings.Contains(output, "\n2\n") {
		t.Fatal("generated user could not authenticate and read/write its database")
	}
	if _, err := compose(ctx, "exec", "-T", "-e", "PGPASSWORD="+first.Credentials.Password, "postgres", "psql", "-X", "-h", "127.0.0.1", "-U", "analytics", "-d", "other", "-c", "SELECT 1"); err == nil {
		t.Fatal("generated user connected to another database")
	}
	// Reusing an existing role must preserve its password and attributes.
	existing, err := managed.CreateDatabase(ctx, application.ManagedDatabaseCreateInput{Name: "reused", Owner: "analytics"})
	if err != nil || existing.Credentials != nil {
		t.Fatal("existing role received new credentials")
	}
	if _, err := compose(ctx, "exec", "-T", "-e", "PGPASSWORD="+first.Credentials.Password, "postgres", "psql", "-X", "-h", "127.0.0.1", "-U", "analytics", "-d", "reused", "-c", "SELECT 1"); err != nil {
		t.Fatal("existing user password was changed")
	}
}
