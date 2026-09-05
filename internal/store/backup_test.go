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
