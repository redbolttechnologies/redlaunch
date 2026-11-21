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
	"redlaunch/internal/compose"
	"redlaunch/internal/store"
)

// This opt-in test starts the real MariaDB and MySQL images through the real
// Compose runner. It uses the production container names, so it refuses to run
// while a Redlaunch instance on the same Docker host already owns them.
func TestManagedDatabaseMySQLFamilyAgainstRealImages(t *testing.T) {
	if os.Getenv("REDLAUNCH_DOCKER_CONFIG_TEST") != "1" {
		t.Skip("set REDLAUNCH_DOCKER_CONFIG_TEST=1 to run disposable Docker checks")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is unavailable")
	}
	for _, provider := range []string{application.ManagedDatabaseProviderMariaDB, application.ManagedDatabaseProviderMySQL} {
		t.Run(provider, func(t *testing.T) { runMySQLFamilyIntegration(t, provider) })
	}
}

func runMySQLFamilyIntegration(t *testing.T, provider string) {
	engine := managedEngines[provider]
	if output, _ := exec.Command("docker", "ps", "-a", "--filter", "name=^"+engine.container+"$", "--format", "{{.Names}}").Output(); strings.TrimSpace(string(output)) != "" {
		t.Skipf("container %s already exists on this Docker host", engine.container)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()

	projectsRoot := filepath.Join(t.TempDir(), "projects")
	backupRoot := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "redlaunch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	managed, err := NewManagedDatabases(database, ManagedDatabaseConfig{ProjectsRoot: projectsRoot, BackupRoot: backupRoot, Runner: compose.CommandRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SaveManagedDatabaseCluster(ctx, application.ManagedDatabaseCluster{Enabled: true, Provider: "postgres", Version: "17", DefaultUser: "redlaunch"}); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(projectsRoot, "core", engine.directory)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		down := exec.CommandContext(cleanup, "docker", "compose", "--project-directory", directory, "-p", compose.ProjectName(directory), "down", "--volumes", "--remove-orphans")
		if output, err := down.CombinedOutput(); err != nil {
			t.Errorf("clean up %s: %v: %s", provider, err, output)
		}
	})

	client := map[string]string{application.ManagedDatabaseProviderMariaDB: "mariadb", application.ManagedDatabaseProviderMySQL: "mysql"}[provider]
	// as runs SQL over TCP as a generated login, the way an application would.
	as := func(username, password, databaseName, statement string) (string, error) {
		args := []string{"exec", "-e", "MYSQL_PWD=" + password, engine.container, client, "--protocol=tcp", "--host=127.0.0.1", "--user=" + username, "--batch", "--skip-column-names"}
		if databaseName != "" {
			args = append(args, "--database="+databaseName)
		}
		// Only stdout is compared: the client warns on stderr about passwordless logins.
		output, err := exec.CommandContext(ctx, "docker", append(args, "--execute="+statement)...).Output()
		return strings.TrimSpace(string(output)), err
	}

	// First use provisions the container and waits until it accepts queries.
	var stages []string
	first, err := managed.CreateDatabaseWithProgress(ctx, application.ManagedDatabaseCreateInput{Provider: provider, Name: "shop_db", Owner: "shop_user"}, func(stage, _ string) { stages = append(stages, stage) })
	if err != nil {
		t.Fatalf("create first database: %v", err)
	}
	if first.Credentials == nil || strings.Join(stages, ",") != "configuration,files,start,ready,database" {
		t.Fatalf("credentials = %v, stages = %v", first.Credentials, stages)
	}
	creds := first.Credentials
	if output, err := as(creds.Username, creds.Password, "shop_db", "CREATE TABLE sample (value INT); INSERT INTO sample VALUES (1); UPDATE sample SET value = 2; SELECT value FROM sample"); err != nil || output != "2" {
		t.Fatalf("generated login could not read and write its database: %v: %s", err, output)
	}

	// "_" is a GRANT wildcard: a login for shop_db must not reach shopXdb.
	other, err := managed.CreateDatabase(ctx, application.ManagedDatabaseCreateInput{Provider: provider, Name: "shopXdb", Owner: "other_user"})
	if err != nil || other.Credentials == nil {
		t.Fatalf("create second database: %v", err)
	}
	if output, err := as(creds.Username, creds.Password, "shopXdb", "SELECT 1"); err == nil {
		t.Fatalf("login for shop_db reached shopXdb: %s", output)
	}
	if output, err := as(other.Credentials.Username, other.Credentials.Password, "shop_db", "SELECT 1"); err == nil {
		t.Fatalf("other login reached shop_db: %s", output)
	}

	// Root must not be reachable from the Docker network.
	secrets, err := os.ReadFile(filepath.Join(directory, "secrets.env"))
	if err != nil {
		t.Fatal(err)
	}
	rootPassword := strings.TrimSpace(strings.SplitN(string(secrets), "=", 2)[1])
	if output, err := exec.CommandContext(ctx, "docker", "exec", "-e", "MYSQL_PWD="+rootPassword, engine.container, client, "--protocol=tcp", "--host="+engine.container, "--user=root", "--execute=SELECT 1").CombinedOutput(); err == nil {
		t.Fatalf("root accepted a non-local connection: %s", output)
	}

	// Backup, damage, restore.
	backup, err := managed.RunBackupNow(ctx, "shop_db")
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	dump, err := os.ReadFile(filepath.Join(backupRoot, "managed-databases", "shop_db", backup.FileName))
	if err != nil || !strings.Contains(string(dump), "CREATE TABLE `sample`") || strings.Contains(string(dump), "shopXdb") {
		t.Fatalf("dump does not contain exactly the selected database: %v", err)
	}
	if _, err := as(creds.Username, creds.Password, "shop_db", "DROP TABLE sample"); err != nil {
		t.Fatal(err)
	}
	if err := managed.RestoreBackup(ctx, "shop_db", backup.FileName); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if output, err := as(creds.Username, creds.Password, "shop_db", "SELECT value FROM sample"); err != nil || output != "2" {
		t.Fatalf("restored data = %q, %v", output, err)
	}

	// Users and grants through the service.
	if err := managed.CreateUser(ctx, application.ManagedDatabaseUserInput{Provider: provider, Username: "reader", Password: "o'brien\\pw", Databases: []string{"shop_db"}}); err != nil {
		t.Fatal(err)
	}
	if output, err := as("reader", "o'brien\\pw", "shop_db", "SELECT value FROM sample"); err != nil || output != "2" {
		t.Fatalf("reader = %q, %v", output, err)
	}
	if err := managed.UpdateUserPassword(ctx, "reader", "rotated-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := as("reader", "o'brien\\pw", "shop_db", "SELECT 1"); err == nil {
		t.Fatal("old password still works")
	}
	if err := managed.SetUserPermissions(ctx, "reader", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := as("reader", "rotated-pw", "shop_db", "SELECT 1"); err == nil {
		t.Fatal("revoked login still reaches the database")
	}
	if err := managed.SetUserPermissions(ctx, "reader", []string{"shop_db"}); err != nil {
		t.Fatal(err)
	}

	// Dropping a database removes its privileges, so a new database with the
	// same name starts closed.
	if err := managed.DropDatabase(ctx, "shop_db"); err != nil {
		t.Fatal(err)
	}
	if _, err := managed.CreateDatabase(ctx, application.ManagedDatabaseCreateInput{Provider: provider, Name: "shop_db", Owner: "other_user", RequireExistingOwner: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := as("reader", "rotated-pw", "shop_db", "SELECT 1"); err == nil {
		t.Fatal("privileges survived DROP DATABASE")
	}
	if _, err := as(creds.Username, creds.Password, "shop_db", "SELECT 1"); err == nil {
		t.Fatal("the dropped database's owner kept access")
	}
	if err := managed.DeleteUser(ctx, "reader"); err != nil {
		t.Fatal(err)
	}
	if err := managed.RestartCluster(ctx, provider); err != nil {
		t.Fatal(err)
	}
	engines, err := managed.GetEngines(ctx)
	if err != nil {
		t.Fatal(fmt.Sprint(err))
	}
	for _, item := range engines {
		if item.Provider == provider && (!item.Provisioned || item.Databases != 2) {
			t.Fatalf("engine = %#v", item)
		}
	}
}
