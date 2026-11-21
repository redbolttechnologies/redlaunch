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

// This opt-in test uses a disposable Postgres with two logical databases, as in
// the managed databases cluster, and checks that each backup holds only the
// database it was taken for and restores into that database.
func TestPostgreSQLDatabaseBackupAndRestoreIntegration(t *testing.T) {
	if os.Getenv("REDLAUNCH_DOCKER_CONFIG_TEST") != "1" {
		t.Skip("set REDLAUNCH_DOCKER_CONFIG_TEST=1 to run disposable Docker checks")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	name := fmt.Sprintf("redbolt-backup-test-%d", time.Now().UnixNano())
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
	for _, sql := range []string{`CREATE DATABASE shop`, `CREATE DATABASE "odd name"`} {
		if _, err := runner.ExecPostgresSQL(ctx, project, "postgres", sql); err != nil {
			t.Fatal(err)
		}
	}
	// Each database has a table the others lack, and the default one has none.
	for database, table := range map[string]string{"shop": "orders", "odd name": "metrics"} {
		if _, err := runner.QueryPostgresDatabase(ctx, project, "postgres", database, "admin", "CREATE TABLE "+table+" (value integer); INSERT INTO "+table+" VALUES (7);"); err != nil {
			t.Fatal(err)
		}
	}

	dump := func(database string) string {
		destination := filepath.Join(t.TempDir(), "backup.sql")
		if err := runner.BackupPostgreSQLDatabase(ctx, project, "postgres", database, destination); err != nil {
			t.Fatalf("backup %q: %v", database, err)
		}
		contents, err := os.ReadFile(destination)
		if err != nil {
			t.Fatal(err)
		}
		return string(contents)
	}
	shop, odd := dump("shop"), dump("odd name")
	if !strings.Contains(shop, "orders") || strings.Contains(shop, "metrics") {
		t.Fatal("the shop backup does not hold exactly the shop database")
	}
	if !strings.Contains(odd, "metrics") || strings.Contains(odd, "orders") {
		t.Fatal("the backup of a name with a space does not hold exactly that database")
	}

	// Restore replays into the named database, not the default one.
	if _, err := runner.QueryPostgresDatabase(ctx, project, "postgres", "shop", "admin", "DROP TABLE orders;"); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "shop.sql")
	if err := os.WriteFile(source, []byte(shop), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runner.RestorePostgreSQLDatabase(ctx, project, "postgres", "shop", source); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if output, err := runner.QueryPostgresDatabase(ctx, project, "postgres", "shop", "admin", "SELECT value FROM orders;"); err != nil || output != "value\n7\n" {
		t.Fatalf("restored data = %q, %v", output, err)
	}
	if output, err := runner.QueryPostgresDatabase(ctx, project, "postgres", "admin", "admin", "SELECT count(*) FROM pg_tables WHERE tablename = 'orders';"); err != nil || output != "count\n0\n" {
		t.Fatalf("the default database received the restore: %q, %v", output, err)
	}
}
