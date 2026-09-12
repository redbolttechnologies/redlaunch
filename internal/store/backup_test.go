package store

import (
	"errors"
	"testing"
	"time"

	"redlaunch/internal/application"
)

func TestStorePersistsBackupScheduleAndFiles(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	item, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := database.CreateService(t.Context(), application.Service{
		ApplicationID: item.ID,
		Name:          "db",
		Type:          application.ServiceTypePostgreSQL,
	})
	if err != nil {
		t.Fatal(err)
	}
	lastBackupAt := time.Date(2026, time.September, 1, 3, 0, 0, 0, time.UTC)
	wantSchedule := application.BackupSchedule{
		ServiceID:        service.ID,
		Enabled:          true,
		ScheduleType:     application.BackupScheduleWeekly,
		Hour:             3,
		Minute:           5,
		Weekday:          "sunday",
		RetentionDays:    14,
		BackupLocation:   "/var/backups/redlaunch/status-page/db",
		LastBackupAt:     lastBackupAt,
		LastBackupStatus: "successful",
		LastBackupSize:   184 * 1024 * 1024,
	}
	if err := database.SaveBackupSchedule(t.Context(), wantSchedule); err != nil {
		t.Fatal(err)
	}
	gotSchedule, err := database.GetBackupSchedule(t.Context(), service.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotSchedule != wantSchedule {
		t.Fatalf("GetBackupSchedule() = %#v, want %#v", gotSchedule, wantSchedule)
	}

	wantBackup := application.Backup{
		ServiceID: service.ID,
		FileName:  "backup-20260901-030000.000000000Z.sql",
		CreatedAt: lastBackupAt,
		SizeBytes: wantSchedule.LastBackupSize,
	}
	created, err := database.CreateBackup(t.Context(), wantBackup)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID < 1 {
		t.Fatalf("created backup ID = %d, want positive ID", created.ID)
	}
	backups, err := database.ListBackups(t.Context(), service.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || backups[0] != created {
		t.Fatalf("ListBackups() = %#v, want %#v", backups, []application.Backup{created})
	}
	if err := database.DeleteBackup(t.Context(), service.ID, created.FileName); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteBackup(t.Context(), service.ID, created.FileName); !errors.Is(err, application.ErrBackupNotFound) {
		t.Fatalf("DeleteBackup(missing) error = %v, want %v", err, application.ErrBackupNotFound)
	}
}

func TestStoreBackupScheduleAndFilesCascadeWithService(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	item, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := database.CreateService(t.Context(), application.Service{ApplicationID: item.ID, Name: "db"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SaveBackupSchedule(t.Context(), application.BackupSchedule{ServiceID: service.ID, ScheduleType: application.BackupScheduleDaily, RetentionDays: 14, BackupLocation: "/backups"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateBackup(t.Context(), application.Backup{ServiceID: service.ID, FileName: "backup-20260901-030000.000000000Z.sql", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteService(t.Context(), item.ID, service.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetBackupSchedule(t.Context(), service.ID); !errors.Is(err, application.ErrBackupScheduleNotFound) {
		t.Fatalf("GetBackupSchedule(after service delete) error = %v, want %v", err, application.ErrBackupScheduleNotFound)
	}
	backups, err := database.ListBackups(t.Context(), service.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("ListBackups(after service delete) = %#v, want no backups", backups)
	}
}

func TestStoreBackupLeasesAreExclusiveAndExpire(t *testing.T) {
	databasePath := t.TempDir() + "/redlaunch.db"
	first, err := Open(t.Context(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := Open(t.Context(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })

	item, err := first.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := first.CreateService(t.Context(), application.Service{ApplicationID: item.ID, Name: "db", Type: application.ServiceTypePostgreSQL})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 11, 10, 0, 0, 0, time.UTC)
	if err := first.AcquireBackupLease(t.Context(), service.ID, "backup", "first", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := second.AcquireBackupLease(t.Context(), service.ID, "restore", "second", now, now.Add(time.Hour)); !errors.Is(err, application.ErrBackupOperationInProgress) {
		t.Fatalf("second lease error = %v, want %v", err, application.ErrBackupOperationInProgress)
	}
	if err := second.AcquireBackupLease(t.Context(), service.ID, "restore", "second", now.Add(2*time.Hour), now.Add(3*time.Hour)); err != nil {
		t.Fatalf("expired lease was not reclaimed: %v", err)
	}
	if err := first.ReleaseBackupLease(t.Context(), service.ID, "first"); err != nil {
		t.Fatal(err)
	}
	if err := second.ReleaseBackupLease(t.Context(), service.ID, "second"); err != nil {
		t.Fatal(err)
	}
}

func TestStoreBackupStatusDoesNotOverwriteScheduleFields(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	item, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := database.CreateService(t.Context(), application.Service{ApplicationID: item.ID, Name: "db", Type: application.ServiceTypePostgreSQL})
	if err != nil {
		t.Fatal(err)
	}
	want := application.BackupSchedule{
		ServiceID:      service.ID,
		Enabled:        true,
		ScheduleType:   application.BackupScheduleWeekly,
		Hour:           4,
		Minute:         7,
		Weekday:        "sunday",
		RetentionDays:  30,
		BackupLocation: "/backups/status-page/db",
	}
	if err := database.SaveBackupSchedule(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.September, 11, 4, 7, 0, 0, time.UTC)
	if err := database.UpdateBackupStatus(t.Context(), service.ID, at, "successful", 1234); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetBackupSchedule(t.Context(), service.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled != want.Enabled || got.ScheduleType != want.ScheduleType || got.Hour != want.Hour || got.Minute != want.Minute || got.Weekday != want.Weekday || got.RetentionDays != want.RetentionDays || got.BackupLocation != want.BackupLocation {
		t.Fatalf("schedule fields changed during status update: got %#v, want settings from %#v", got, want)
	}
	if !got.LastBackupAt.Equal(at) || got.LastBackupStatus != "successful" || got.LastBackupSize != 1234 {
		t.Fatalf("backup status = %#v, want completion metadata", got)
	}
}

func TestStoreApplicationDeletionIntentSurvivesMetadataRemoval(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	item, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 11, 10, 0, 0, 0, time.UTC)
	intent, err := database.BeginApplicationDeletion(t.Context(), item, now)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Stage != "schedules" || intent.State != "running" {
		t.Fatalf("initial deletion intent = %#v, want running schedules stage", intent)
	}
	if err := database.DeleteApplication(t.Context(), item.ID); err != nil {
		t.Fatal(err)
	}
	active, err := database.IsApplicationDeletionActive(t.Context(), item.ID)
	if err != nil || !active {
		t.Fatalf("active deletion after metadata removal = (%t, %v), want true", active, err)
	}
	if err := database.UpdateApplicationDeletion(t.Context(), item.ID, "complete", "complete", "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	active, err = database.IsApplicationDeletionActive(t.Context(), item.ID)
	if err != nil || active {
		t.Fatalf("active deletion after completion = (%t, %v), want false", active, err)
	}
	got, err := database.GetApplicationDeletion(t.Context(), item.ID)
	if err != nil || got.FolderName != item.FolderName || got.State != "complete" {
		t.Fatalf("deletion tombstone = %#v, %v, want completed tombstone", got, err)
	}
}
