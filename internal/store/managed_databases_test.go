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
	if err := database.CreateManagedDatabaseUser(t.Context(), "app", []string{"app"}); err != nil {
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
