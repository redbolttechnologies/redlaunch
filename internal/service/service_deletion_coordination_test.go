package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/store"
)

type serviceDeletionSchedulerFake struct {
	disabled [][2]int64
}

func (f *serviceDeletionSchedulerFake) DisableApplicationSchedules(context.Context, int64) error {
	return nil
}

func (f *serviceDeletionSchedulerFake) DisableServiceBackupSchedule(_ context.Context, applicationID, serviceID int64) error {
	f.disabled = append(f.disabled, [2]int64{applicationID, serviceID})
	return nil
}

func newServiceDeletionTestSetup(t *testing.T, ctx context.Context) (*store.Store, *Applications, *serviceRuntimeRunner, *serviceDeletionSchedulerFake, application.Application, application.Service) {
	t.Helper()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "redlaunch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	projectsRoot := filepath.Join(t.TempDir(), "projects")
	runner := &serviceRuntimeRunner{}
	applications, err := NewApplications(database, projectsRoot, runner)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := &serviceDeletionSchedulerFake{}
	applications.SetApplicationDeletionDependencies(scheduler, nil)
	prepareRoutingProxy(t, projectsRoot)

	item, err := applications.Create(ctx, "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}
	service, err := database.CreateService(ctx, application.Service{
		ApplicationID: item.ID,
		Name:          "db",
		Type:          application.ServiceTypePostgreSQL,
		ImageName:     "postgres:17",
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(projectsRoot, applicationsDir, item.FolderName)
	compose := "services:\n  db:\n    image: postgres:17\n"
	if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	return database, applications, runner, scheduler, item, service
}

// TestDeleteServiceCoordinatesBackupRoutingAndProxy verifies the R05 coordinated
// workflow: the timer is disabled, routing rows are removed with a Caddy
// reload, and backup files are retained while metadata cascades.
func TestDeleteServiceCoordinatesBackupRoutingAndProxy(t *testing.T) {
	ctx := t.Context()
	database, applications, runner, scheduler, item, service := newServiceDeletionTestSetup(t, ctx)

	if err := database.SaveBackupSchedule(ctx, application.BackupSchedule{
		ServiceID: service.ID, Enabled: true, ScheduleType: application.BackupScheduleDaily,
		Hour: 3, Minute: 0, RetentionDays: 14, BackupLocation: "/tmp/nowhere",
	}); err != nil {
		t.Fatal(err)
	}
	domain, err := database.CreateDomain(ctx, application.Domain{ApplicationID: item.ID, Name: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applications.CreateRouting(ctx, item.ID, domain.ID, application.RoutingInput{
		Subdomain: "db", Path: "/", ServiceName: "db", ServicePort: 5432, ServicePath: "/",
	}); err != nil {
		t.Fatal(err)
	}
	reloadsAfterCreate := len(runner.reloads)

	backupDir := filepath.Join(t.TempDir(), "backups", item.FolderName, service.Name)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(backupDir, "2026-09-01-030000.sql")
	if err := os.WriteFile(retained, []byte("dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateBackup(ctx, application.Backup{
		ServiceID: service.ID, FileName: "2026-09-01-030000.sql",
		CreatedAt: time.Now().UTC(), SizeBytes: 4,
	}); err != nil {
		t.Fatal(err)
	}

	var stages []string
	if err := applications.DeleteServiceWithProgress(ctx, item.ID, "db", func(stage, _ string) {
		stages = append(stages, stage)
	}); err != nil {
		t.Fatalf("DeleteServiceWithProgress() = %v", err)
	}

	if len(scheduler.disabled) != 1 || scheduler.disabled[0] != [2]int64{item.ID, service.ID} {
		t.Fatalf("disabled service timers = %v, want one for (%d, %d)", scheduler.disabled, item.ID, service.ID)
	}
	routings, err := database.ListAllRoutings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, routing := range routings {
		if routing.ApplicationID == item.ID && routing.ServiceName == "db" {
			t.Fatalf("routing row for deleted service remains: %#v", routing)
		}
	}
	if len(runner.reloads) <= reloadsAfterCreate {
		t.Fatalf("Caddy reloads after service deletion = %d, want more than %d after routing removal", len(runner.reloads), reloadsAfterCreate)
	}
	if _, err := os.Stat(retained); err != nil {
		t.Fatalf("retained backup file stat error = %v, want file preserved", err)
	}
	if _, err := database.GetBackupSchedule(ctx, service.ID); !errors.Is(err, application.ErrBackupScheduleNotFound) {
		t.Fatalf("backup schedule after service deletion = %v, want not found (cascaded)", err)
	}
	intent, err := database.GetServiceDeletion(ctx, item.ID, "db")
	if err != nil || intent.State != "complete" {
		t.Fatalf("service deletion intent = %#v, %v; want complete", intent, err)
	}
	joined := strings.Join(stages, "\x00")
	for _, want := range []string{"schedules", "stop", "remove", "compose", "routing", "metadata"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("deletion stages = %v, want stage %q", stages, want)
		}
	}
}

// TestDeleteServiceRefusesConflictingBackupLease proves service deletion
// coordinates with running backups instead of interrupting them.
func TestDeleteServiceRefusesConflictingBackupLease(t *testing.T) {
	ctx := t.Context()
	database, applications, _, _, item, service := newServiceDeletionTestSetup(t, ctx)

	now := time.Now().UTC()
	if err := database.AcquireBackupLease(ctx, service.ID, "backup", "holder", now, now.Add(backupLeaseDuration)); err != nil {
		t.Fatal(err)
	}
	if err := applications.DeleteServiceWithProgress(ctx, item.ID, "db", nil); !errors.Is(err, application.ErrBackupOperationInProgress) {
		t.Fatalf("DeleteServiceWithProgress(conflicting lease) = %v, want %v", err, application.ErrBackupOperationInProgress)
	}
	services, err := database.ListServices(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("services after refused deletion = %#v, want the service retained", services)
	}

	if err := database.ReleaseBackupLease(ctx, service.ID, "holder"); err != nil {
		t.Fatal(err)
	}
	if err := applications.DeleteServiceWithProgress(ctx, item.ID, "db", nil); err != nil {
		t.Fatalf("DeleteServiceWithProgress(after lease release) = %v", err)
	}
}

// TestDeleteServiceResumesAfterMetadataRemoval covers the crash window where
// metadata is gone but routing and Compose state remain: a retry must finish
// without requiring the service to be registered.
func TestDeleteServiceResumesAfterMetadataRemoval(t *testing.T) {
	ctx := t.Context()
	database, applications, _, _, item, service := newServiceDeletionTestSetup(t, ctx)

	domain, err := database.CreateDomain(ctx, application.Domain{ApplicationID: item.ID, Name: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateRouting(ctx, application.Routing{
		ApplicationID: item.ID, DomainID: domain.ID, Subdomain: "db",
		Path: "/", ServiceName: "db", ServicePort: 5432, ServicePath: "/",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := database.BeginServiceDeletion(ctx, item.ID, service.Name, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"stop", "remove", "compose", "routing"} {
		if err := database.UpdateServiceDeletion(ctx, item.ID, service.Name, stage, serviceDeletionStateRunning, "", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DeleteService(ctx, item.ID, service.Name); err != nil {
		t.Fatal(err)
	}

	if err := applications.DeleteServiceWithProgress(ctx, item.ID, service.Name, nil); err != nil {
		t.Fatalf("resumed service deletion = %v", err)
	}
	routings, err := database.ListAllRoutings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, routing := range routings {
		if routing.ApplicationID == item.ID && routing.ServiceName == service.Name {
			t.Fatalf("routing row remains after resumed deletion: %#v", routing)
		}
	}
	intent, err := database.GetServiceDeletion(ctx, item.ID, service.Name)
	if err != nil || intent.State != "complete" {
		t.Fatalf("service deletion intent = %#v, %v; want complete", intent, err)
	}
}
