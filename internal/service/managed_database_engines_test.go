package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func createMariaDB(t *testing.T, managed *ManagedDatabases, name, owner string) application.ManagedDatabaseCreation {
	t.Helper()
	result, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Provider: application.ManagedDatabaseProviderMariaDB, Name: name, Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMySQLQuotingEscapesHostileValues(t *testing.T) {
	if got := mysqlQuoteLiteral(`o'brien\`); got != `'o''brien\\'` {
		t.Fatalf("literal = %s", got)
	}
	if got := mysqlQuoteIdentifier("a`b"); got != "`a``b`" {
		t.Fatalf("identifier = %s", got)
	}
	if got := mysqlQuoteLogin("shop"); got != `'shop'@'%'` {
		t.Fatalf("login = %s", got)
	}
	// "_" is a GRANT wildcard; an unescaped name would also match shopXdb.
	if got := mysqlQuoteGrantTarget("shop_db"); got != "`shop\\_db`.*" {
		t.Fatalf("grant target = %s", got)
	}
}

func TestCreateMariaDBProvisionsEngineOnFirstUse(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	var stages []string
	result, err := managed.CreateDatabaseWithProgress(t.Context(), application.ManagedDatabaseCreateInput{
		Provider: application.ManagedDatabaseProviderMariaDB, Name: "shop catalog", Owner: "catalog_app",
	}, func(stage, _ string) { stages = append(stages, stage) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(stages, ",") != "configuration,files,start,ready,database" {
		t.Fatalf("progress stages = %v", stages)
	}
	if result.Provider != application.ManagedDatabaseProviderMariaDB || result.Credentials == nil || result.Credentials.Username != "catalog_app" {
		t.Fatalf("creation = %#v", result)
	}

	directory := filepath.Join(managed.projectsRoot, "core", "databases-mariadb")
	compose, err := os.ReadFile(filepath.Join(directory, "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"image: mariadb:11.4",
		"container_name: redbolt-databases-mariadb",
		`- "redlaunch.managed=true"`,
		"- vars.env", "- secrets.env",
		"name: redbolt-databases-mariadb-data",
		"/var/lib/mysql",
	} {
		if !strings.Contains(string(compose), required) {
			t.Fatalf("compose file lacks %q:\n%s", required, compose)
		}
	}
	vars, _ := os.ReadFile(filepath.Join(directory, "vars.env"))
	secrets, _ := os.ReadFile(filepath.Join(directory, "secrets.env"))
	if string(vars) != "MARIADB_ROOT_HOST=127.0.0.1\n" || !strings.HasPrefix(string(secrets), "MARIADB_ROOT_PASSWORD=") || strings.Contains(string(vars), "PASSWORD") {
		t.Fatalf("env files: vars=%q secrets=%q", vars, secrets)
	}
	if info, err := os.Stat(filepath.Join(directory, "secrets.env")); err != nil || info.Mode().Perm() != envFileMode {
		t.Fatalf("secrets mode = %v, %v", info, err)
	}
	if runner.upDirs[len(runner.upDirs)-1] != directory {
		t.Fatalf("up directories = %v", runner.upDirs)
	}
	engine, err := managed.repository.GetManagedDatabaseEngine(t.Context(), application.ManagedDatabaseProviderMariaDB)
	if err != nil || !engine.Provisioned || engine.Version != "11.4" {
		t.Fatalf("engine = %#v, %v", engine, err)
	}

	statements := strings.Join(runner.mysqlCalls, "\n")
	for _, required := range []string{
		"SELECT 1 FROM mysql.user WHERE User = 'catalog_app' AND Host = '%' LIMIT 1",
		"CREATE USER 'catalog_app'@'%' IDENTIFIED BY '",
		"CREATE DATABASE `shop catalog`",
		"GRANT ALL PRIVILEGES ON `shop catalog`.* TO 'catalog_app'@'%'",
	} {
		if !strings.Contains(statements, required) {
			t.Fatalf("missing statement %q in:\n%s", required, statements)
		}
	}
	if len(runner.execCalls) != 0 {
		t.Fatalf("MariaDB creation ran Postgres SQL: %v", runner.execCalls)
	}

	second, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Provider: "mariadb", Name: "other"})
	if err != nil || second.Credentials == nil {
		t.Fatalf("second creation = %#v, %v", second, err)
	}
	if len(runner.upDirs) != 2 {
		t.Fatalf("engine was provisioned again: up calls = %v", runner.upDirs)
	}
}

func TestProvisioningKeepsExistingRootPassword(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	directory := filepath.Join(managed.projectsRoot, "core", "databases-mysql")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "secrets.env"), []byte("MYSQL_ROOT_PASSWORD=initialized-volume-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Provider: "mysql", Name: "billing"}); err != nil {
		t.Fatal(err)
	}
	secrets, _ := os.ReadFile(filepath.Join(directory, "secrets.env"))
	if string(secrets) != "MYSQL_ROOT_PASSWORD=initialized-volume-password\n" {
		t.Fatalf("secrets were rewritten: %q", secrets)
	}
}

func TestMySQLProvisioningWaitsForReadiness(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	attempts := 0
	runner.mysqlFunc = func(sql string) (string, error) {
		if sql == "SELECT 1" {
			attempts++
			if attempts < 3 {
				return "", errors.New("connection refused")
			}
		}
		return "", nil
	}
	managed.readyInterval = 0
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Provider: "mysql", Name: "billing"}); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("readiness attempts = %d, want 3", attempts)
	}
}

func TestMySQLCreationFailureRemovesNewLoginAndDatabase(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	createMariaDB(t, managed, "seed", "seed")
	runner.mysqlCalls = nil
	runner.mysqlFunc = func(sql string) (string, error) {
		if strings.HasPrefix(sql, "GRANT ALL PRIVILEGES") {
			return "", errors.New("grant failed")
		}
		return "", nil
	}
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Provider: "mariadb", Name: "analytics"}); err == nil {
		t.Fatal("creation succeeded despite failed grant")
	}
	statements := strings.Join(runner.mysqlCalls, "\n")
	if !strings.Contains(statements, "DROP DATABASE IF EXISTS `analytics`") || !strings.Contains(statements, "DROP USER IF EXISTS 'analytics'@'%'") {
		t.Fatalf("cleanup statements missing:\n%s", statements)
	}
	if _, err := managed.repository.GetManagedDatabase(t.Context(), "analytics"); !errors.Is(err, application.ErrManagedDatabaseNotFound) {
		t.Fatal("failed creation persisted database metadata")
	}
}

func TestMySQLExistingOwnerKeepsCredentials(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	createMariaDB(t, managed, "first", "shared")
	runner.mysqlCalls = nil
	runner.mysqlFunc = func(sql string) (string, error) {
		if strings.HasPrefix(sql, "SELECT 1 FROM mysql.user") {
			return "1", nil
		}
		return "", nil
	}
	result, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Provider: "mariadb", Name: "second", Owner: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Credentials != nil {
		t.Fatal("existing login received replacement credentials")
	}
	for _, statement := range runner.mysqlCalls {
		if strings.Contains(statement, "CREATE USER") || strings.Contains(statement, "ALTER USER") {
			t.Fatalf("existing login was modified: %s", statement)
		}
	}
}

func TestCreateDatabaseRejectsOwnerFromAnotherProvider(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	// "redlaunch" is the Postgres default user.
	_, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Provider: "mysql", Name: "billing", Owner: "redlaunch"})
	if !errors.Is(err, application.ErrManagedDatabaseProviderMismatch) {
		t.Fatalf("error = %v, want provider mismatch", err)
	}
	if runner.upCalls != 1 {
		t.Fatalf("rejected request started a container: up calls = %d", runner.upCalls)
	}
}

func TestCreateDatabaseNamesAreUniqueAcrossProviders(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Provider: "mariadb", Name: "shop", Owner: "shop_maria"}); !errors.Is(err, application.ErrManagedDatabaseAlreadyExists) {
		t.Fatalf("error = %v, want already exists", err)
	}
}

func TestCreateDatabaseValidatesProviderAndMySQLLoginLength(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Provider: "oracle", Name: "shop"}); !errors.Is(err, application.ErrManagedDatabaseProviderInvalid) {
		t.Fatalf("provider error = %v", err)
	}
	longName := strings.Repeat("a", 40)
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Provider: "mysql", Name: longName}); !errors.Is(err, application.ErrManagedDatabaseUserTooLong) {
		t.Fatalf("long login error = %v", err)
	}
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Provider: "mariadb", Name: longName}); err != nil {
		t.Fatalf("MariaDB accepts long logins: %v", err)
	}
}

func TestStoppedMySQLEngineIsNotRestartedImplicitly(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	createMariaDB(t, managed, "first", "first")
	runner.running = false
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Provider: "mariadb", Name: "second"}); !errors.Is(err, application.ErrDatabaseServiceNotRunning) {
		t.Fatalf("error = %v, want not running", err)
	}
}

func TestDropMySQLDatabaseRevokesEveryGrantee(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	createMariaDB(t, managed, "shop_db", "owner_login")
	if err := managed.CreateUser(t.Context(), application.ManagedDatabaseUserInput{Provider: "mariadb", Username: "reader", Password: "pw", Databases: []string{"shop_db"}}); err != nil {
		t.Fatal(err)
	}
	runner.mysqlCalls = nil
	if err := managed.DropDatabase(t.Context(), "shop_db"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"DROP DATABASE IF EXISTS `shop_db`",
		"REVOKE ALL PRIVILEGES ON `shop\\_db`.* FROM 'owner_login'@'%'",
		"REVOKE ALL PRIVILEGES ON `shop\\_db`.* FROM 'reader'@'%'",
	}
	if strings.Join(runner.mysqlCalls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("drop statements:\n%s\nwant:\n%s", strings.Join(runner.mysqlCalls, "\n"), strings.Join(want, "\n"))
	}
	if len(runner.execCalls) != 0 {
		t.Fatalf("MariaDB drop ran Postgres SQL: %v", runner.execCalls)
	}
	if _, err := managed.repository.GetManagedDatabase(t.Context(), "shop_db"); !errors.Is(err, application.ErrManagedDatabaseNotFound) {
		t.Fatalf("metadata error = %v", err)
	}
}

func TestMySQLUsersAreScopedToTheirProvider(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	createMariaDB(t, managed, "shop", "shop")
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: "pgdb"}); err != nil {
		t.Fatal(err)
	}
	err := managed.CreateUser(t.Context(), application.ManagedDatabaseUserInput{Provider: "mariadb", Username: "reader", Password: "pw", Databases: []string{"pgdb"}})
	if !errors.Is(err, application.ErrManagedDatabaseProviderMismatch) {
		t.Fatalf("cross-engine grant error = %v", err)
	}
	if err := managed.CreateUser(t.Context(), application.ManagedDatabaseUserInput{Provider: "mariadb", Username: "reader", Password: "o'brien", Databases: []string{"shop"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(runner.mysqlCalls, "\n"), `CREATE USER 'reader'@'%' IDENTIFIED BY 'o''brien'`) {
		t.Fatalf("create user statement missing: %v", runner.mysqlCalls)
	}
	if err := managed.CreateUser(t.Context(), application.ManagedDatabaseUserInput{Provider: "mysql", Username: "reader", Password: "pw"}); !errors.Is(err, application.ErrManagedDatabaseUserAlreadyExists) {
		t.Fatalf("duplicate login error = %v", err)
	}

	runner.mysqlCalls, runner.execCalls = nil, nil
	if err := managed.UpdateUserPassword(t.Context(), "reader", "new-secret"); err != nil {
		t.Fatal(err)
	}
	if err := managed.SetUserPermissions(t.Context(), "reader", []string{}); err != nil {
		t.Fatal(err)
	}
	if err := managed.SetUserPermissions(t.Context(), "reader", []string{"pgdb"}); !errors.Is(err, application.ErrManagedDatabaseProviderMismatch) {
		t.Fatalf("cross-engine permission error = %v", err)
	}
	statements := strings.Join(runner.mysqlCalls, "\n")
	for _, required := range []string{`ALTER USER 'reader'@'%' IDENTIFIED BY 'new-secret'`, "REVOKE ALL PRIVILEGES ON `shop`.* FROM 'reader'@'%'"} {
		if !strings.Contains(statements, required) {
			t.Fatalf("missing %q in:\n%s", required, statements)
		}
	}
	if len(runner.execCalls) != 0 {
		t.Fatalf("MariaDB user operations ran Postgres SQL: %v", runner.execCalls)
	}
	if err := managed.DeleteUser(t.Context(), "shop"); !errors.Is(err, application.ErrManagedDatabaseUserOwnsDatabase) {
		t.Fatalf("owner deletion error = %v", err)
	}
	if err := managed.DeleteUser(t.Context(), "reader"); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLPasswordsAreRedactedFromErrors(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	createMariaDB(t, managed, "shop", "shop")
	runner.mysqlFunc = func(sql string) (string, error) {
		if strings.HasPrefix(sql, "CREATE USER") {
			return "", errors.New("syntax error near " + sql)
		}
		return "", nil
	}
	err := managed.CreateUser(t.Context(), application.ManagedDatabaseUserInput{Provider: "mariadb", Username: "reader", Password: "hunter2-secret"})
	if err == nil || strings.Contains(err.Error(), "hunter2-secret") {
		t.Fatalf("error = %v, want redacted failure", err)
	}
}

func TestMySQLConnectionsUseEngineHostAndScheme(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	createMariaDB(t, managed, "shop", "shop")
	connections, err := managed.GetDatabaseConnections(t.Context(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 1 || connections[0].Username != "shop" || connections[0].URI != "mysql://shop:PASSWORD@redbolt-databases-mariadb:3306/shop" {
		t.Fatalf("connections = %#v", connections)
	}
	// The Postgres default user must not leak into another engine's list.
	for _, connection := range connections {
		if connection.Username == "redlaunch" {
			t.Fatal("Postgres default user listed for a MariaDB database")
		}
	}
}

func TestMySQLBackupAndRestoreUseTheDatabaseEngine(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	createMariaDB(t, managed, "shop", "shop")
	backup, err := managed.RunBackupNow(t.Context(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.mysqlBackups) != 1 || runner.mysqlBackups[0] != "shop" {
		t.Fatalf("MySQL backups = %v", runner.mysqlBackups)
	}
	if err := managed.RestoreBackup(t.Context(), "shop", backup.FileName); err != nil {
		t.Fatal(err)
	}
	if runner.mysqlRestore != 1 {
		t.Fatalf("MySQL restores = %d", runner.mysqlRestore)
	}
}

func TestSQLWorkspaceIsPostgresOnly(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	createMariaDB(t, managed, "shop", "shop")
	if _, _, err := managed.GetDatabaseWorkspace(t.Context(), "shop"); !errors.Is(err, application.ErrManagedDatabaseWorkspaceUnsupported) {
		t.Fatalf("workspace error = %v", err)
	}
}

func TestGetEnginesReportsProvisioningAndCounts(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	createMariaDB(t, managed, "shop", "shop")
	engines, err := managed.GetEngines(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(engines) != 3 {
		t.Fatalf("engines = %#v", engines)
	}
	postgres, mariadb, mysql := engines[0], engines[1], engines[2]
	if postgres.Provider != "postgres" || !postgres.Provisioned || postgres.Version != "17" || postgres.Databases != 1 {
		t.Fatalf("postgres = %#v", postgres)
	}
	if !mariadb.Provisioned || mariadb.Version != "11.4" || mariadb.Databases != 1 || !mariadb.Running {
		t.Fatalf("mariadb = %#v", mariadb)
	}
	if mysql.Provisioned || mysql.Version != "8.4" || mysql.Running || mysql.Databases != 0 {
		t.Fatalf("mysql = %#v", mysql)
	}
}

func TestClusterActionsRequireProvisionedEngine(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	if err := managed.StopCluster(t.Context(), "mysql"); !errors.Is(err, application.ErrManagedDatabaseEngineNotProvisioned) {
		t.Fatalf("stop error = %v", err)
	}
	if _, _, err := managed.GetLogs(t.Context(), "mysql"); !errors.Is(err, application.ErrManagedDatabaseEngineNotProvisioned) {
		t.Fatalf("logs error = %v", err)
	}
	if err := managed.StopCluster(t.Context(), "oracle"); !errors.Is(err, application.ErrManagedDatabaseProviderInvalid) {
		t.Fatalf("invalid provider error = %v", err)
	}
	createMariaDB(t, managed, "shop", "shop")
	if err := managed.StopCluster(t.Context(), "mariadb"); err != nil {
		t.Fatal(err)
	}
	if got := runner.stopped[len(runner.stopped)-1]; !strings.HasSuffix(got, "databases-mariadb") {
		t.Fatalf("stopped %q", got)
	}
}

func TestMachineRequestsStartAnEngineBeforeCreatingDatabases(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	input := application.ManagedDatabaseCreateInput{Provider: "mysql", Name: "billing", Owner: "app", RequireExistingOwner: true}
	if _, err := managed.CreateDatabase(t.Context(), input); !errors.Is(err, application.ErrManagedDatabaseEngineNotProvisioned) {
		t.Fatalf("create error = %v, want not provisioned", err)
	}
	if runner.upCalls != 1 || len(runner.mysqlCalls) != 0 {
		t.Fatalf("rejected request started a container: up=%d statements=%v", runner.upCalls, runner.mysqlCalls)
	}
	if err := managed.StartCluster(t.Context(), "mysql"); err != nil {
		t.Fatal(err)
	}
	engine, err := managed.repository.GetManagedDatabaseEngine(t.Context(), "mysql")
	if err != nil || !engine.Provisioned {
		t.Fatalf("engine = %#v, %v", engine, err)
	}
	if err := managed.CreateUser(t.Context(), application.ManagedDatabaseUserInput{Provider: "mysql", Username: "app", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	runner.mysqlFunc = func(sql string) (string, error) {
		if strings.HasPrefix(sql, "SELECT 1 FROM mysql.user") {
			return "1", nil
		}
		return "", nil
	}
	if _, err := managed.CreateDatabase(t.Context(), input); err != nil {
		t.Fatal(err)
	}
}

func TestDisableClusterStopsEveryProvisionedEngine(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	createMariaDB(t, managed, "shop", "shop")
	if err := managed.DisableCluster(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(runner.stopped) != 2 || !strings.HasSuffix(runner.stopped[0], "core/databases") || !strings.HasSuffix(runner.stopped[1], "core/databases-mariadb") {
		t.Fatalf("stopped = %v", runner.stopped)
	}
}

func TestPostgresBackupAndRestoreTargetTheSelectedDatabase(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	for _, name := range []string{"shop", "analytics"} {
		if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	shop, err := managed.RunBackupNow(t.Context(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managed.RunBackupNow(t.Context(), "analytics"); err != nil {
		t.Fatal(err)
	}
	if err := managed.RestoreBackup(t.Context(), "shop", shop.FileName); err != nil {
		t.Fatal(err)
	}
	if strings.Join(runner.postgresBackups, ",") != "shop,analytics" || strings.Join(runner.postgresRestores, ",") != "shop" {
		t.Fatalf("backups = %v restores = %v, want the selected databases", runner.postgresBackups, runner.postgresRestores)
	}
}
