package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type backupRepositoryFake struct {
	item        application.Application
	services    []application.Service
	schedule    application.BackupSchedule
	hasSchedule bool
	backups     []application.Backup
}

func (r *backupRepositoryFake) Get(context.Context, int64) (application.Application, error) {
	return r.item, nil
}

func (r *backupRepositoryFake) ListServices(context.Context, int64) ([]application.Service, error) {
	return r.services, nil
}

func (r *backupRepositoryFake) GetBackupSchedule(context.Context, int64) (application.BackupSchedule, error) {
	if !r.hasSchedule {
		return application.BackupSchedule{}, application.ErrBackupScheduleNotFound
	}
	return r.schedule, nil
}

func (r *backupRepositoryFake) SaveBackupSchedule(_ context.Context, schedule application.BackupSchedule) error {
	r.schedule = schedule
	r.hasSchedule = true
	return nil
}

func (r *backupRepositoryFake) CreateBackup(_ context.Context, backup application.Backup) (application.Backup, error) {
	backup.ID = int64(len(r.backups) + 1)
	r.backups = append(r.backups, backup)
	return backup, nil
}

func (r *backupRepositoryFake) ListBackups(context.Context, int64) ([]application.Backup, error) {
	return append([]application.Backup(nil), r.backups...), nil
}

func (r *backupRepositoryFake) DeleteBackup(_ context.Context, serviceID int64, fileName string) error {
	for index, backup := range r.backups {
		if backup.ServiceID == serviceID && backup.FileName == fileName {
			r.backups = append(r.backups[:index], r.backups[index+1:]...)
			return nil
		}
	}
	return application.ErrBackupNotFound
}

type backupRunnerFake struct {
	backupDestination string
	restoreSource     string
	backupErr         error
	restoreErr        error
	serviceRunning    bool
	serviceStatusErr  error
}

func (r *backupRunnerFake) BackupPostgreSQL(_ context.Context, projectDir, serviceName, destination string) error {
	r.backupDestination = projectDir + "/" + serviceName + ":" + destination
	if r.backupErr != nil {
		return r.backupErr
	}
	return os.WriteFile(destination, []byte("-- backup\n"), 0o600)
}

func (r *backupRunnerFake) RestorePostgreSQL(_ context.Context, _ string, _ string, source string) error {
	r.restoreSource = source
	return r.restoreErr
}

func (r *backupRunnerFake) IsServiceRunning(_ context.Context, _ string, _ string) (bool, error) {
	return r.serviceRunning, r.serviceStatusErr
}

type backupSchedulerFake struct {
	serviceUnitName string
	serviceContents string
	timerUnitName   string
	timerContents   string
	disabledService string
	disabledTimer   string
	installErr      error
	disableErr      error
}

func (s *backupSchedulerFake) Install(_ context.Context, serviceUnitName, serviceContents, timerUnitName, timerContents string) error {
	s.serviceUnitName = serviceUnitName
	s.serviceContents = serviceContents
	s.timerUnitName = timerUnitName
	s.timerContents = timerContents
	return s.installErr
}

func (s *backupSchedulerFake) Disable(_ context.Context, serviceUnitName, timerUnitName string) error {
	s.disabledService = serviceUnitName
	s.disabledTimer = timerUnitName
	return s.disableErr
}

func newBackupServiceTest(t *testing.T, repository *backupRepositoryFake, runner *backupRunnerFake, scheduler *backupSchedulerFake) *BackupService {
	t.Helper()
	runner.serviceRunning = true
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	applicationDirectory := filepath.Join(projectsRoot, applicationsDir, repository.item.FolderName)
	if err := os.MkdirAll(applicationDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(applicationDirectory, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	backups, err := NewBackupService(repository, BackupConfig{
		ProjectsRoot: projectsRoot,
		BackupRoot:   filepath.Join(root, "backups"),
		DatabasePath: filepath.Join(root, "redlaunch.db"),
		Executable:   "/usr/local/bin/redlaunch",
		ExecutablePrefix: []string{
			"/usr/bin/docker", "exec", "redbolt-redlaunch",
		},
		Runner:    runner,
		Scheduler: scheduler,
		Clock: func() time.Time {
			return time.Date(2026, time.September, 1, 3, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return backups
}

func TestNewBackupServiceRejectsBackupRootInsideManagedApplicationDirectory(t *testing.T) {
	root := t.TempDir()
	repository := &backupRepositoryFake{item: application.Application{FolderName: "status-page"}}
	_, err := NewBackupService(repository, BackupConfig{
		ProjectsRoot: filepath.Join(root, "projects"),
		BackupRoot:   filepath.Join(root, "projects", applicationsDir, "status-page", "backups"),
		Runner:       &backupRunnerFake{},
	})
	if err == nil || !strings.Contains(err.Error(), "outside managed application directories") {
		t.Fatalf("NewBackupService() error = %v, want backup-root boundary error", err)
	}
}

func TestBackupServiceEnablesAndDisablesIsolatedWeeklySchedule(t *testing.T) {
	repository := &backupRepositoryFake{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
	}
	runner := &backupRunnerFake{}
	scheduler := &backupSchedulerFake{}
	backups := newBackupServiceTest(t, repository, runner, scheduler)

	if err := backups.UpdateBackupSchedule(t.Context(), 7, "db", application.BackupScheduleInput{
		Enabled:       true,
		ScheduleType:  application.BackupScheduleWeekly,
		Hour:          3,
		Minute:        5,
		Weekday:       "sunday",
		RetentionDays: 14,
	}); err != nil {
		t.Fatal(err)
	}
	if !repository.schedule.Enabled || repository.schedule.ScheduleType != application.BackupScheduleWeekly || repository.schedule.Weekday != "sunday" {
		t.Fatalf("saved backup schedule = %#v, want enabled weekly Sunday schedule", repository.schedule)
	}
	if scheduler.serviceUnitName != "redlaunch-backup-a7-s11.service" || scheduler.timerUnitName != "redlaunch-backup-a7-s11.timer" {
		t.Fatalf("systemd units = (%q, %q), want isolated application/service IDs", scheduler.serviceUnitName, scheduler.timerUnitName)
	}
	if !strings.Contains(scheduler.timerContents, "OnCalendar=Sun *-*-* 03:05:00") || !strings.Contains(scheduler.serviceContents, `"/usr/bin/docker" "exec" "redbolt-redlaunch" "/usr/local/bin/redlaunch"`) || !strings.Contains(scheduler.serviceContents, `"--application-id" "7"`) || !strings.Contains(scheduler.serviceContents, `"--service-id" "11"`) {
		t.Fatalf("systemd units do not contain the weekly schedule or resource IDs: service=%s timer=%s", scheduler.serviceContents, scheduler.timerContents)
	}

	if err := backups.UpdateBackupSchedule(t.Context(), 7, "db", application.BackupScheduleInput{}); err != nil {
		t.Fatal(err)
	}
	if repository.schedule.Enabled {
		t.Fatalf("saved disabled schedule = %#v, want disabled", repository.schedule)
	}
	if scheduler.disabledService != "redlaunch-backup-a7-s11.service" || scheduler.disabledTimer != "redlaunch-backup-a7-s11.timer" {
		t.Fatalf("disabled systemd units = (%q, %q), want the enabled schedule units", scheduler.disabledService, scheduler.disabledTimer)
	}
	if backupServiceUnitName(7, 11) == backupServiceUnitName(8, 11) {
		t.Fatal("systemd service unit names collide across applications")
	}
}

func TestBackupServiceRunsRecordsAndRestoresPostgreSQLBackup(t *testing.T) {
	repository := &backupRepositoryFake{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
	}
	runner := &backupRunnerFake{}
	backups := newBackupServiceTest(t, repository, runner, &backupSchedulerFake{})

	created, err := backups.RunBackupNow(t.Context(), 7, "db")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != 1 || created.ServiceID != 11 || !strings.HasPrefix(created.FileName, "backup-20260901-030000") || created.SizeBytes == 0 {
		t.Fatalf("created backup = %#v, want recorded SQL backup", created)
	}
	if repository.schedule.BackupLocation == "" || !strings.HasSuffix(repository.schedule.BackupLocation, filepath.Join("status-page", "db")) {
		t.Fatalf("backup location = %q, want service-scoped location", repository.schedule.BackupLocation)
	}
	backupPath := filepath.Join(repository.schedule.BackupLocation, created.FileName)
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions = %o, want 600", info.Mode().Perm())
	}
	locationInfo, err := os.Stat(repository.schedule.BackupLocation)
	if err != nil {
		t.Fatal(err)
	}
	if locationInfo.Mode().Perm() != backupDirectoryMode {
		t.Fatalf("backup directory permissions = %o, want %o", locationInfo.Mode().Perm(), backupDirectoryMode)
	}
	if repository.schedule.LastBackupStatus != "successful" || repository.schedule.LastBackupSize != created.SizeBytes {
		t.Fatalf("backup status = %#v, want successful status and size", repository.schedule)
	}

	details, err := backups.GetBackupDetails(t.Context(), 7, "db")
	if err != nil || len(details.Backups) != 1 {
		t.Fatalf("GetBackupDetails() = %#v, %v, want one backup", details, err)
	}
	if err := backups.RestoreBackup(t.Context(), 7, "db", created.FileName); err != nil {
		t.Fatal(err)
	}
	if runner.restoreSource != backupPath {
		t.Fatalf("restore source = %q, want %q", runner.restoreSource, backupPath)
	}
	if err := backups.RestoreBackup(t.Context(), 7, "db", "../outside.sql"); !errors.Is(err, application.ErrBackupFileNameInvalid) {
		t.Fatalf("RestoreBackup(traversal) error = %v, want %v", err, application.ErrBackupFileNameInvalid)
	}
}

func TestBackupServiceRecoversOnlyAbandonedTemporaryFilesAndNeverOverwrites(t *testing.T) {
	repository := &backupRepositoryFake{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
	}
	runner := &backupRunnerFake{}
	backups := newBackupServiceTest(t, repository, runner, &backupSchedulerFake{})
	location := filepath.Join(backups.backupRoot, "status-page", "db")
	if err := os.MkdirAll(location, backupDirectoryMode); err != nil {
		t.Fatal(err)
	}
	oldTemporary := filepath.Join(location, ".redlaunch-backup-abandoned.sql")
	if err := os.WriteFile(oldTemporary, []byte("abandoned"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldAt := time.Date(2026, time.August, 1, 3, 0, 0, 0, time.UTC)
	if err := os.Chtimes(oldTemporary, oldAt, oldAt); err != nil {
		t.Fatal(err)
	}
	currentName := "backup-20260901-030000.000000000Z.sql"
	currentPath := filepath.Join(location, currentName)
	if err := os.WriteFile(currentPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	created, err := backups.RunBackupNow(t.Context(), 7, "db")
	if err != nil {
		t.Fatal(err)
	}
	if created.FileName != "backup-20260901-030000.000000000Z-1.sql" {
		t.Fatalf("backup filename = %q, want collision-safe suffix", created.FileName)
	}
	if _, err := os.Stat(oldTemporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned temporary file stat = %v, want not found", err)
	}
	contents, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "existing" {
		t.Fatalf("existing backup contents = %q, want unchanged", contents)
	}
}

func TestBackupServiceRefusesManualBackupWhenPostgreSQLServiceIsStopped(t *testing.T) {
	repository := &backupRepositoryFake{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
	}
	runner := &backupRunnerFake{}
	backups := newBackupServiceTest(t, repository, runner, &backupSchedulerFake{})
	runner.serviceRunning = false

	if _, err := backups.RunBackupNow(t.Context(), 7, "db"); !errors.Is(err, application.ErrBackupServiceNotRunning) {
		t.Fatalf("RunBackupNow(stopped) error = %v, want %v", err, application.ErrBackupServiceNotRunning)
	}
	if runner.backupDestination != "" {
		t.Fatalf("backup runner destination = %q, want no backup command", runner.backupDestination)
	}
}

func TestBackupServiceDeletesPostgreSQLBackupFileAndRecord(t *testing.T) {
	repository := &backupRepositoryFake{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
	}
	backups := newBackupServiceTest(t, repository, &backupRunnerFake{}, &backupSchedulerFake{})

	created, err := backups.RunBackupNow(t.Context(), 7, "db")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repository.schedule.BackupLocation, created.FileName)
	if err := backups.DeleteBackup(t.Context(), 7, "db", created.FileName); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("deleted backup file stat error = %v, want not exist", err)
	}
	if len(repository.backups) != 0 {
		t.Fatalf("backup records after delete = %#v, want none", repository.backups)
	}
	if err := backups.DeleteBackup(t.Context(), 7, "db", created.FileName); !errors.Is(err, application.ErrBackupNotFound) {
		t.Fatalf("DeleteBackup(missing record) error = %v, want %v", err, application.ErrBackupNotFound)
	}
}

func TestBackupServiceDoesNotDeleteSymlinkedBackup(t *testing.T) {
	repository := &backupRepositoryFake{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
		backups: []application.Backup{{
			ServiceID: 11,
			FileName:  "backup-20260901-030000.000000000Z.sql",
		}},
	}
	backups := newBackupServiceTest(t, repository, &backupRunnerFake{}, &backupSchedulerFake{})
	location := filepath.Join(backups.backupRoot, repository.item.FolderName, "db")
	if err := ensureBackupDirectory(backups.backupRoot, repository.item.FolderName, "db"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.sql")
	if err := os.WriteFile(target, []byte("-- do not remove\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(location, repository.backups[0].FileName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := backups.DeleteBackup(t.Context(), 7, "db", repository.backups[0].FileName); err == nil {
		t.Fatal("DeleteBackup(symlink) error = nil, want regular-file error")
	}
	if reader, _, err := backups.OpenBackup(t.Context(), 7, "db", repository.backups[0].FileName); err == nil {
		_ = reader.Close()
		t.Fatal("OpenBackup(symlink) error = nil, want regular-file error")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("symlink stat error = %v, want symlink preserved", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("symlink target stat error = %v, want target preserved", err)
	}
	if len(repository.backups) != 1 {
		t.Fatalf("backup records after symlink delete = %#v, want record preserved", repository.backups)
	}
}

func TestBackupServiceOpensRecordedBackupForDownload(t *testing.T) {
	repository := &backupRepositoryFake{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
	}
	backups := newBackupServiceTest(t, repository, &backupRunnerFake{}, &backupSchedulerFake{})
	created, err := backups.RunBackupNow(t.Context(), 7, "db")
	if err != nil {
		t.Fatal(err)
	}

	reader, downloaded, err := backups.OpenBackup(t.Context(), 7, "db", created.FileName)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "-- backup\n" {
		t.Fatalf("downloaded backup = %q, want backup contents", content)
	}
	if downloaded.FileName != created.FileName || downloaded.SizeBytes != int64(len(content)) {
		t.Fatalf("download metadata = %#v, want file name and current size", downloaded)
	}
}

func TestBackupServiceScheduledRunRefusesDisabledScheduleAndRecordsFailure(t *testing.T) {
	repository := &backupRepositoryFake{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
		schedule: application.BackupSchedule{
			ServiceID:     11,
			Enabled:       false,
			ScheduleType:  application.BackupScheduleDaily,
			Hour:          3,
			Minute:        0,
			RetentionDays: 14,
		},
		hasSchedule: true,
	}
	runner := &backupRunnerFake{backupErr: errors.New("database unavailable")}
	backups := newBackupServiceTest(t, repository, runner, &backupSchedulerFake{})

	if _, err := backups.RunScheduledBackup(t.Context(), 7, 11); !errors.Is(err, application.ErrBackupScheduleDisabled) {
		t.Fatalf("RunScheduledBackup(disabled) error = %v, want %v", err, application.ErrBackupScheduleDisabled)
	}

	if _, err := backups.RunBackupNow(t.Context(), 7, "db"); err == nil {
		t.Fatal("RunBackupNow() error = nil, want runner error")
	}
	if repository.schedule.LastBackupStatus != "failed" || repository.schedule.LastBackupSize != 0 {
		t.Fatalf("failed backup schedule = %#v, want failed status", repository.schedule)
	}
}

func TestBackupOperationDeadlineBoundsUnboundedCallers(t *testing.T) {
	ctx, cancel := withBackupOperationDeadline(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("withBackupOperationDeadline(background) has no deadline, want 25-minute cap")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > backupOperationTimeout {
		t.Fatalf("unbounded operation deadline in %v, want (0, %v]", remaining, backupOperationTimeout)
	}
	if backupOperationTimeout >= backupLeaseDuration {
		t.Fatalf("operation timeout %v must stay below lease duration %v", backupOperationTimeout, backupLeaseDuration)
	}

	short, shortCancel := context.WithTimeout(context.Background(), time.Minute)
	defer shortCancel()
	capped, cancelCapped := withBackupOperationDeadline(short)
	defer cancelCapped()
	if deadline, ok := capped.Deadline(); !ok || time.Until(deadline) > time.Minute {
		t.Fatalf("short caller deadline = %v, want the caller's own 1-minute bound preserved", deadline)
	}
}

func TestBackupUnitsBoundExecutionBelowLeaseExpiry(t *testing.T) {
	backups := newBackupServiceTest(t, &backupRepositoryFake{}, &backupRunnerFake{}, &backupSchedulerFake{})
	serviceContents, _ := backups.renderUnits(7, 11, application.BackupSchedule{})
	if !strings.Contains(serviceContents, "TimeoutStartSec="+backupSystemdTimeout) {
		t.Fatalf("backup service unit does not bound execution:\n%s", serviceContents)
	}
}

func TestDisableServiceBackupScheduleIsIdempotent(t *testing.T) {
	repository := &backupRepositoryFake{
		schedule:    application.BackupSchedule{ServiceID: 11, Enabled: true},
		hasSchedule: true,
	}
	scheduler := &backupSchedulerFake{}
	backups := newBackupServiceTest(t, repository, &backupRunnerFake{}, scheduler)
	if err := backups.DisableServiceBackupSchedule(context.Background(), 7, 11); err != nil {
		t.Fatal(err)
	}
	if scheduler.disabledService == "" || scheduler.disabledTimer == "" {
		t.Fatalf("disabled units = %q/%q, want service and timer names", scheduler.disabledService, scheduler.disabledTimer)
	}
	if repository.schedule.Enabled {
		t.Fatal("schedule still enabled after service-targeted disable")
	}
	if err := backups.DisableServiceBackupSchedule(context.Background(), 7, 11); err != nil {
		t.Fatalf("second DisableServiceBackupSchedule() = %v, want nil", err)
	}
	if err := backups.DisableServiceBackupSchedule(context.Background(), 7, 999); err != nil {
		t.Fatalf("DisableServiceBackupSchedule(missing schedule) = %v, want nil", err)
	}
}

func TestRestoreBackupRejectsNonPlainSQLDumps(t *testing.T) {
	repository := &backupRepositoryFake{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
		backups: []application.Backup{
			{ID: 1, ServiceID: 11, FileName: "backup-custom.sql", CreatedAt: time.Now().UTC(), SizeBytes: 5},
			{ID: 2, ServiceID: 11, FileName: "backup-binary.sql", CreatedAt: time.Now().UTC(), SizeBytes: 7},
		},
	}
	runner := &backupRunnerFake{}
	backups := newBackupServiceTest(t, repository, runner, &backupSchedulerFake{})
	location := filepath.Join(backups.backupRoot, "status-page", "db")
	if err := os.MkdirAll(location, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(location, "backup-custom.sql"), []byte("PGDMP-custom"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(location, "backup-binary.sql"), []byte{'S', 'E', 'T', 0, 'x'}, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, fileName := range []string{"backup-custom.sql", "backup-binary.sql"} {
		if err := backups.RestoreBackup(t.Context(), 7, "db", fileName); !errors.Is(err, application.ErrBackupFormatUnsupported) {
			t.Fatalf("RestoreBackup(%s) = %v, want %v", fileName, err, application.ErrBackupFormatUnsupported)
		}
	}
	if runner.restoreSource != "" {
		t.Fatalf("restore reached the database runner with source %q, want no database work for rejected formats", runner.restoreSource)
	}
}

func TestRestoreBackupAcceptsPlainSQLDumps(t *testing.T) {
	repository := &backupRepositoryFake{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
		backups: []application.Backup{
			{ID: 1, ServiceID: 11, FileName: "backup-plain.sql", CreatedAt: time.Now().UTC(), SizeBytes: 8},
		},
	}
	runner := &backupRunnerFake{}
	backups := newBackupServiceTest(t, repository, runner, &backupSchedulerFake{})
	location := filepath.Join(backups.backupRoot, "status-page", "db")
	if err := os.MkdirAll(location, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(location, "backup-plain.sql"), []byte("-- plain SQL dump\nSELECT 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backups.RestoreBackup(t.Context(), 7, "db", "backup-plain.sql"); err != nil {
		t.Fatalf("RestoreBackup(plain SQL) = %v, want nil", err)
	}
	if runner.restoreSource == "" {
		t.Fatal("plain SQL restore did not reach the database runner")
	}
}

func TestBackupServiceMapsMissingControllerToSchedulerUnavailable(t *testing.T) {
	missing := fmt.Errorf("systemd controller %q: %w", "systemctl", exec.ErrNotFound)
	repository := &backupRepositoryFake{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
	}
	runner := &backupRunnerFake{}
	scheduler := &backupSchedulerFake{installErr: missing}
	backups := newBackupServiceTest(t, repository, runner, scheduler)

	err := backups.UpdateBackupSchedule(t.Context(), 7, "db", application.BackupScheduleInput{
		Enabled:       true,
		ScheduleType:  application.BackupScheduleDaily,
		Hour:          3,
		Minute:        5,
		RetentionDays: 14,
	})
	if !errors.Is(err, application.ErrBackupSchedulerUnavailable) {
		t.Fatalf("UpdateBackupSchedule(missing controller) error = %v, want %v", err, application.ErrBackupSchedulerUnavailable)
	}
	if repository.hasSchedule && repository.schedule.Enabled {
		t.Fatal("schedule was persisted despite missing systemd controller")
	}

	repository.hasSchedule = true
	repository.schedule = application.BackupSchedule{ServiceID: 11, Enabled: true, ScheduleType: application.BackupScheduleDaily, Hour: 3, Minute: 5, RetentionDays: 14}
	scheduler.installErr = nil
	scheduler.disableErr = missing
	if err := backups.UpdateBackupSchedule(t.Context(), 7, "db", application.BackupScheduleInput{}); !errors.Is(err, application.ErrBackupSchedulerUnavailable) {
		t.Fatalf("UpdateBackupSchedule(disable, missing controller) error = %v, want %v", err, application.ErrBackupSchedulerUnavailable)
	}
	if !repository.schedule.Enabled {
		t.Fatal("disabled state was persisted despite missing systemd controller")
	}
}
