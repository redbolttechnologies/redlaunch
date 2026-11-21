package store

import (
	"testing"
	"time"

	"redlaunch/internal/application"
)

func TestManagedDatabaseClusterRoundTrip(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	cluster, err := database.GetManagedDatabaseCluster(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cluster.Enabled {
		t.Fatal("fresh cluster enabled = true, want false")
	}
	if err := database.SaveManagedDatabaseCluster(t.Context(), application.ManagedDatabaseCluster{
		Enabled:     true,
		Provider:    application.ManagedDatabaseProviderPostgres,
		Version:     "17",
		DefaultUser: "redlaunch",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.GetManagedDatabaseCluster(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Enabled || loaded.Version != "17" || loaded.DefaultUser != "redlaunch" {
		t.Fatalf("cluster = %#v", loaded)
	}
}

func TestManagedDatabasesAndUsersRoundTrip(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.CreateManagedDatabase(t.Context(), application.ManagedDatabase{Name: "app", Owner: "redlaunch"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID < 1 {
		t.Fatal("database ID was not assigned")
	}
	if _, err := database.CreateManagedDatabase(t.Context(), application.ManagedDatabase{Name: "app"}); err != application.ErrManagedDatabaseAlreadyExists {
		t.Fatalf("duplicate error = %v, want already exists", err)
	}
	items, err := database.ListManagedDatabases(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "app" {
		t.Fatalf("databases = %#v", items)
	}
	if err := database.CreateManagedDatabaseUser(t.Context(), "app", application.ManagedDatabaseProviderPostgres, []string{"app"}); err != nil {
		t.Fatal(err)
	}
	detail, err := database.GetManagedDatabaseUser(t.Context(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Databases) != 1 || detail.Databases[0] != "app" {
		t.Fatalf("grants = %#v", detail.Databases)
	}
	if err := database.SetManagedDatabaseGrants(t.Context(), "app", []string{}); err != nil {
		t.Fatal(err)
	}
	cleared, err := database.GetManagedDatabaseUser(t.Context(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Databases) != 0 {
		t.Fatalf("cleared grants = %#v", cleared.Databases)
	}
	if err := database.DeleteManagedDatabaseUser(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteManagedDatabase(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
}

func TestManagedBackupScheduleRoundTrip(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.CreateManagedDatabase(t.Context(), application.ManagedDatabase{Name: "app", Owner: "redlaunch"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SaveManagedBackupSchedule(t.Context(), application.BackupSchedule{
		ServiceID:      created.ID,
		Enabled:        true,
		ScheduleType:   application.BackupScheduleDaily,
		Hour:           3,
		Minute:         0,
		RetentionDays:  14,
		BackupLocation: "/tmp/backups",
	}); err != nil {
		t.Fatal(err)
	}
	schedule, err := database.GetManagedBackupSchedule(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !schedule.Enabled || schedule.ScheduleType != application.BackupScheduleDaily {
		t.Fatalf("schedule = %#v", schedule)
	}
	if err := database.UpdateManagedBackupStatus(t.Context(), created.ID, time.Now().UTC(), "successful", 42); err != nil {
		t.Fatal(err)
	}
	backup, err := database.CreateManagedBackup(t.Context(), application.Backup{ServiceID: created.ID, FileName: "backup-20240101-000000.000000000Z.sql"})
	if err != nil {
		t.Fatal(err)
	}
	if backup.ID < 1 {
		t.Fatal("backup ID was not assigned")
	}
	backups, err := database.ListManagedBackups(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %#v", backups)
	}
	if err := database.DeleteManagedBackup(t.Context(), created.ID, backup.FileName); err != nil {
		t.Fatal(err)
	}
}

func TestManagedDatabaseCreationWithUserIsAtomic(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.CreateManagedDatabaseWithUser(t.Context(), application.ManagedDatabase{Name: "analytics", Owner: "reporter"}, "reporter"); err != nil {
		t.Fatal(err)
	}
	user, err := database.GetManagedDatabaseUser(t.Context(), "reporter")
	if err != nil || len(user.Databases) != 1 || user.Databases[0] != "analytics" {
		t.Fatal("new user does not have exactly its database grant")
	}
	if _, err := database.CreateManagedDatabaseWithUser(t.Context(), application.ManagedDatabase{Name: "other", Owner: "reporter"}, "reporter"); err != application.ErrManagedDatabaseUserAlreadyExists {
		t.Fatal("duplicate username was not rejected")
	}
	if _, err := database.GetManagedDatabase(t.Context(), "other"); err != application.ErrManagedDatabaseNotFound {
		t.Fatal("failed user creation persisted a database")
	}
	user, err = database.GetManagedDatabaseUser(t.Context(), "reporter")
	if err != nil || len(user.Databases) != 1 || user.Databases[0] != "analytics" {
		t.Fatal("failed creation changed existing grants")
	}
}

func TestManagedDatabaseProvidersRoundTrip(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if _, err := database.CreateManagedDatabase(t.Context(), application.ManagedDatabase{Name: "legacy", Owner: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateManagedDatabaseWithUser(t.Context(), application.ManagedDatabase{
		Name: "shop", Owner: "shop", Provider: application.ManagedDatabaseProviderMariaDB,
	}, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateManagedDatabase(t.Context(), application.ManagedDatabase{Name: "bad", Provider: "oracle"}); err != application.ErrManagedDatabaseProviderInvalid {
		t.Fatalf("invalid provider error = %v, want %v", err, application.ErrManagedDatabaseProviderInvalid)
	}
	if _, err := database.CreateManagedDatabaseWithUser(t.Context(), application.ManagedDatabase{
		Name: "billing", Provider: application.ManagedDatabaseProviderMySQL,
	}, "a-login-name-longer-than-thirty-two-bytes"); err != application.ErrManagedDatabaseUserTooLong {
		t.Fatalf("long MySQL login error = %v, want %v", err, application.ErrManagedDatabaseUserTooLong)
	}

	items, err := database.ListManagedDatabases(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	providers := map[string]string{}
	for _, item := range items {
		providers[item.Name] = item.Provider
	}
	if len(items) != 2 || providers["legacy"] != application.ManagedDatabaseProviderPostgres || providers["shop"] != application.ManagedDatabaseProviderMariaDB {
		t.Fatalf("database providers = %#v", providers)
	}
	user, err := database.GetManagedDatabaseUser(t.Context(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if user.User.Provider != application.ManagedDatabaseProviderMariaDB {
		t.Fatalf("user provider = %q, want mariadb", user.User.Provider)
	}
}

func TestManagedDatabaseEngineRoundTrip(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	engine, err := database.GetManagedDatabaseEngine(t.Context(), application.ManagedDatabaseProviderMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if engine.Provisioned {
		t.Fatalf("fresh engine = %#v, want not provisioned", engine)
	}
	if err := database.SaveManagedDatabaseEngine(t.Context(), application.ManagedDatabaseEngine{Provider: application.ManagedDatabaseProviderMySQL, Version: "8.4"}); err != nil {
		t.Fatal(err)
	}
	engine, err = database.GetManagedDatabaseEngine(t.Context(), application.ManagedDatabaseProviderMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if !engine.Provisioned || engine.Version != "8.4" || engine.CreatedAt.IsZero() {
		t.Fatalf("engine = %#v", engine)
	}
	if err := database.SaveManagedDatabaseEngine(t.Context(), application.ManagedDatabaseEngine{Provider: "oracle", Version: "1"}); err == nil {
		t.Fatal("saving an unknown provider succeeded, want error")
	}
}

func TestManagedDatabaseProviderMigrationKeepsExistingRowsOnPostgres(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	// Rewind to the schema that existed before migration 29.
	for _, statement := range []string{
		`DROP TABLE managed_database_engines`,
		`ALTER TABLE managed_databases DROP COLUMN provider`,
		`ALTER TABLE managed_database_users DROP COLUMN provider`,
		`DELETE FROM schema_migrations WHERE version = 29`,
		`INSERT INTO managed_databases (name, owner, created_at) VALUES ('old', 'old', '2026-01-02T03:04:05Z')`,
		`INSERT INTO managed_database_users (username, created_at) VALUES ('old', '2026-01-02T03:04:05Z')`,
	} {
		if _, err := database.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := database.migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	item, err := database.GetManagedDatabase(t.Context(), "old")
	if err != nil {
		t.Fatal(err)
	}
	user, err := database.GetManagedDatabaseUser(t.Context(), "old")
	if err != nil {
		t.Fatal(err)
	}
	if item.Provider != application.ManagedDatabaseProviderPostgres || user.User.Provider != application.ManagedDatabaseProviderPostgres {
		t.Fatalf("migrated providers = %q and %q, want postgres", item.Provider, user.User.Provider)
	}
}
