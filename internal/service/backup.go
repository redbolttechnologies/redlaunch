package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"redlaunch/internal/application"
)

const backupDirectoryMode os.FileMode = 0o700

// BackupRepository is the persistence capability required by database backup
// operations.
type BackupRepository interface {
	Get(context.Context, int64) (application.Application, error)
	ListServices(context.Context, int64) ([]application.Service, error)
	GetBackupSchedule(context.Context, int64) (application.BackupSchedule, error)
	SaveBackupSchedule(context.Context, application.BackupSchedule) error
	CreateBackup(context.Context, application.Backup) (application.Backup, error)
	ListBackups(context.Context, int64) ([]application.Backup, error)
	DeleteBackup(context.Context, int64, string) error
}

type backupLeaseRepository interface {
	AcquireBackupLease(context.Context, int64, string, string, time.Time, time.Time) error
	ReleaseBackupLease(context.Context, int64, string) error
}

type backupStatusRepository interface {
	UpdateBackupStatus(context.Context, int64, time.Time, string, int64) error
}

type backupDeletionStateRepository interface {
	IsApplicationDeletionActive(context.Context, int64) (bool, error)
}

const backupLeaseDuration = 30 * time.Minute
const backupTemporaryRetention = 24 * time.Hour

// backupOperationTimeout bounds every lease-holding backup operation below the
// lease window so a live owner can never still be running when its lease
// expires. Web jobs carry a shorter 15-minute tracked-job deadline; this cap
// exists for direct service callers and the backup-run CLI, which otherwise
// pass an unbounded signal context. The generated systemd unit kills overruns
// at the same boundary. Lease expiry then only handles true process death,
// never a still-running owner.
const backupOperationTimeout = 25 * time.Minute

// backup systemd execution timeout, kept equal to the operation cap so the
// manager and systemd agree on the overrun boundary.
const backupSystemdTimeout = "1500"

// PostgreSQLBackupRunner performs a database dump or restore without exposing
// credentials to the host command line and can inspect the service runtime
// state before a manual dump.
type PostgreSQLBackupRunner interface {
	BackupPostgreSQL(context.Context, string, string, string) error
	RestorePostgreSQL(context.Context, string, string, string) error
	IsServiceRunning(context.Context, string, string) (bool, error)
}

// BackupScheduler installs or removes one isolated systemd service/timer pair.
type BackupScheduler interface {
	Install(context.Context, string, string, string, string) error
	Disable(context.Context, string, string) error
}

// BackupConfig configures backup storage and scheduled execution. When
// ExecutablePrefix is set, it is prepended to the backup command in the
// systemd unit (for example, docker exec <container> for a containerized
// Redlaunch process).
type BackupConfig struct {
	ProjectsRoot     string
	BackupRoot       string
	DatabasePath     string
	Executable       string
	ExecutablePrefix []string
	Runner           PostgreSQLBackupRunner
	Scheduler        BackupScheduler
	Clock            func() time.Time
}

// BackupService coordinates database backup configuration, systemd schedules,
// backup files, retention, and restores.
type BackupService struct {
	repository       BackupRepository
	projectsRoot     string
	backupRoot       string
	databasePath     string
	executable       string
	executablePrefix []string
	runner           PostgreSQLBackupRunner
	scheduler        BackupScheduler
	clock            func() time.Time
	mu               sync.Mutex
}

// NewBackupService constructs a backup service with an isolated backup root.
func NewBackupService(repository BackupRepository, config BackupConfig) (*BackupService, error) {
	if repository == nil {
		return nil, errors.New("backup repository is required")
	}
	if config.Runner == nil {
		return nil, errors.New("PostgreSQL backup runner is required")
	}
	projectsRoot, err := absoluteManagedPath(config.ProjectsRoot, "projects root")
	if err != nil {
		return nil, err
	}
	backupRoot := config.BackupRoot
	if strings.TrimSpace(backupRoot) == "" {
		backupRoot = filepath.Join(projectsRoot, "backups")
	}
	backupRoot, err = absoluteManagedPath(backupRoot, "backup root")
	if err != nil {
		return nil, err
	}
	applicationsRoot := filepath.Join(projectsRoot, applicationsDir)
	relativeBackupRoot, err := filepath.Rel(applicationsRoot, backupRoot)
	if err != nil {
		return nil, fmt.Errorf("check backup root: %w", err)
	}
	if relativeBackupRoot == "." || (relativeBackupRoot != ".." && !strings.HasPrefix(relativeBackupRoot, ".."+string(filepath.Separator))) {
		return nil, errors.New("backup root must remain outside managed application directories")
	}
	databasePath := ""
	if strings.TrimSpace(config.DatabasePath) != "" {
		databasePath, err = filepath.Abs(config.DatabasePath)
		if err != nil {
			return nil, fmt.Errorf("resolve database path: %w", err)
		}
	}
	executable := strings.TrimSpace(config.Executable)
	if executable == "" {
		executable = "redlaunch"
	}
	executablePrefix := make([]string, len(config.ExecutablePrefix))
	for index, argument := range config.ExecutablePrefix {
		if strings.TrimSpace(argument) == "" {
			return nil, errors.New("backup executable prefix contains an empty argument")
		}
		executablePrefix[index] = argument
	}
	scheduler := config.Scheduler
	if scheduler == nil {
		scheduler = noBackupScheduler{}
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &BackupService{
		repository:       repository,
		projectsRoot:     projectsRoot,
		backupRoot:       backupRoot,
		databasePath:     databasePath,
		executable:       executable,
		executablePrefix: executablePrefix,
		runner:           config.Runner,
		scheduler:        scheduler,
		clock:            clock,
	}, nil
}

// GetBackupDetails returns the schedule and recorded backup files for one
// PostgreSQL-compatible database service.
func (s *BackupService) GetBackupDetails(ctx context.Context, applicationID int64, serviceName string) (application.BackupDetails, error) {
	item, service, err := s.findDatabaseService(ctx, applicationID, serviceName)
	if err != nil {
		return application.BackupDetails{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	schedule, err := s.scheduleForService(ctx, service.ID, s.backupLocation(item, service))
	if err != nil {
		return application.BackupDetails{}, err
	}
	backups, err := s.repository.ListBackups(ctx, service.ID)
	if err != nil {
		return application.BackupDetails{}, fmt.Errorf("list backups: %w", err)
	}
	backups = validBackupsForService(backups, service.ID)
	return application.BackupDetails{Schedule: schedule, Backups: backups}, nil
}

// UpdateBackupSchedule saves a database backup schedule and synchronizes its
// isolated systemd timer. Disabling an existing schedule stops the timer and
// one-shot service before the disabled state is persisted.
func (s *BackupService) UpdateBackupSchedule(ctx context.Context, applicationID int64, serviceName string, input application.BackupScheduleInput) error {
	item, service, err := s.findDatabaseService(ctx, applicationID, serviceName)
	if err != nil {
		return err
	}
	if err := s.ensureApplicationNotDeleting(ctx, applicationID); err != nil {
		return err
	}

	release, err := s.acquireLease(ctx, service.ID, "schedule")
	if err != nil {
		return err
	}
	defer release()

	s.mu.Lock()
	defer s.mu.Unlock()

	location := s.backupLocation(item, service)
	current, err := s.scheduleForService(ctx, service.ID, location)
	if err != nil {
		return err
	}
	if !input.Enabled {
		if current.Enabled {
			if err := s.scheduler.Disable(ctx, backupServiceUnitName(applicationID, service.ID), backupTimerUnitName(applicationID, service.ID)); err != nil {
				return fmt.Errorf("disable scheduled backups: %w", err)
			}
		}
		current.Enabled = false
		current.BackupLocation = location
		return s.repository.SaveBackupSchedule(ctx, current)
	}

	validated, err := application.ValidateBackupScheduleInput(input)
	if err != nil {
		return err
	}
	candidate := current
	candidate.ServiceID = service.ID
	candidate.Enabled = true
	candidate.ScheduleType = validated.ScheduleType
	candidate.Hour = validated.Hour
	candidate.Minute = validated.Minute
	candidate.Weekday = validated.Weekday
	candidate.RetentionDays = validated.RetentionDays
	candidate.BackupLocation = location
	serviceUnitName := backupServiceUnitName(applicationID, service.ID)
	timerUnitName := backupTimerUnitName(applicationID, service.ID)
	serviceContents, timerContents := s.renderUnits(applicationID, service.ID, candidate)
	if err := s.scheduler.Install(ctx, serviceUnitName, serviceContents, timerUnitName, timerContents); err != nil {
		return fmt.Errorf("enable scheduled backups: %w", err)
	}
	if err := s.repository.SaveBackupSchedule(ctx, candidate); err != nil {
		_ = s.scheduler.Disable(ctx, serviceUnitName, timerUnitName)
		return fmt.Errorf("save backup schedule: %w", err)
	}
	return nil
}

// DisableApplicationSchedules stops all database timers owned by an
// application and persists the disabled state. It is used by the durable
// deletion workflow before Docker resources or metadata are removed.
func (s *BackupService) DisableApplicationSchedules(ctx context.Context, applicationID int64) error {
	if applicationID < 1 {
		return application.ErrNotFound
	}
	services, err := s.repository.ListServices(ctx, applicationID)
	if err != nil {
		return fmt.Errorf("list services for backup schedule cleanup: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, service := range services {
		if !application.IsDatabaseServiceType(service.Type) {
			continue
		}
		schedule, err := s.repository.GetBackupSchedule(ctx, service.ID)
		if errors.Is(err, application.ErrBackupScheduleNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read backup schedule for cleanup: %w", err)
		}
		if !schedule.Enabled {
			continue
		}
		if err := s.scheduler.Disable(ctx, backupServiceUnitName(applicationID, service.ID), backupTimerUnitName(applicationID, service.ID)); err != nil {
			return fmt.Errorf("disable scheduled backups for service %s: %w", service.Name, err)
		}
		schedule.Enabled = false
		if err := s.repository.SaveBackupSchedule(ctx, schedule); err != nil {
			return fmt.Errorf("save disabled backup schedule for service %s: %w", service.Name, err)
		}
	}
	return nil
}

// DisableServiceBackupSchedule stops one database service's timer and persists
// the disabled state. Service deletion uses this before removing metadata so
// the external systemd timer cannot outlive its schedule row.
func (s *BackupService) DisableServiceBackupSchedule(ctx context.Context, applicationID, serviceID int64) error {
	if applicationID < 1 || serviceID < 1 {
		return application.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	schedule, err := s.repository.GetBackupSchedule(ctx, serviceID)
	if errors.Is(err, application.ErrBackupScheduleNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read backup schedule for cleanup: %w", err)
	}
	if !schedule.Enabled {
		return nil
	}
	if err := s.scheduler.Disable(ctx, backupServiceUnitName(applicationID, serviceID), backupTimerUnitName(applicationID, serviceID)); err != nil {
		return fmt.Errorf("disable scheduled backups: %w", err)
	}
	schedule.Enabled = false
	if err := s.repository.SaveBackupSchedule(ctx, schedule); err != nil {
		return fmt.Errorf("save disabled backup schedule: %w", err)
	}
	return nil
}

// RunBackupNow creates a backup immediately when the database service is
// running, whether or not a schedule is enabled.
func (s *BackupService) RunBackupNow(ctx context.Context, applicationID int64, serviceName string) (application.Backup, error) {
	ctx, cancel := withBackupOperationDeadline(ctx)
	defer cancel()
	item, service, err := s.findDatabaseService(ctx, applicationID, serviceName)
	if err != nil {
		return application.Backup{}, err
	}
	if err := s.ensureApplicationNotDeleting(ctx, applicationID); err != nil {
		return application.Backup{}, err
	}
	release, err := s.acquireLease(ctx, service.ID, "backup")
	if err != nil {
		return application.Backup{}, err
	}
	defer release()
	if err := s.ensureServiceRunning(ctx, item, service); err != nil {
		return application.Backup{}, err
	}
	return s.runBackup(ctx, item, service, false)
}

// RunScheduledBackup runs one backup for a systemd service unit. The schedule
// is checked again so a queued one-shot invocation cannot run after a user has
// disabled the timer.
func (s *BackupService) RunScheduledBackup(ctx context.Context, applicationID, serviceID int64) (application.Backup, error) {
	ctx, cancel := withBackupOperationDeadline(ctx)
	defer cancel()
	item, service, err := s.findDatabaseServiceByID(ctx, applicationID, serviceID)
	if err != nil {
		return application.Backup{}, err
	}
	if err := s.ensureApplicationNotDeleting(ctx, applicationID); err != nil {
		return application.Backup{}, err
	}
	release, err := s.acquireLease(ctx, service.ID, "backup")
	if err != nil {
		return application.Backup{}, err
	}
	defer release()
	return s.runBackup(ctx, item, service, true)
}

// RestoreBackup restores a recorded backup file into its own database service.
func (s *BackupService) RestoreBackup(ctx context.Context, applicationID int64, serviceName, fileName string) error {
	ctx, cancel := withBackupOperationDeadline(ctx)
	defer cancel()
	item, service, err := s.findDatabaseService(ctx, applicationID, serviceName)
	if err != nil {
		return err
	}
	if err := s.ensureApplicationNotDeleting(ctx, applicationID); err != nil {
		return err
	}
	fileName, err = application.ValidateBackupFileName(fileName)
	if err != nil {
		return err
	}

	release, err := s.acquireLease(ctx, service.ID, "restore")
	if err != nil {
		return err
	}
	defer release()

	backups, err := s.repository.ListBackups(ctx, service.ID)
	if err != nil {
		return fmt.Errorf("list backups for restore: %w", err)
	}
	var selected application.Backup
	for _, backup := range backups {
		if backup.ServiceID == service.ID && backup.FileName == fileName {
			selected = backup
			break
		}
	}
	if selected.FileName == "" {
		return application.ErrBackupNotFound
	}
	location := s.backupLocation(item, service)
	if _, err := inspectBackupDirectory(s.backupRoot, item.FolderName, service.Name); err != nil {
		return fmt.Errorf("inspect backup location: %w", err)
	}
	path, err := safeBackupPath(location, selected.FileName)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect backup file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("backup file must be a regular file")
	}
	if err := validatePlainSQLDump(path); err != nil {
		return err
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return fmt.Errorf("resolve application directory for restore: %w", err)
	}
	if err := s.runner.RestorePostgreSQL(ctx, directory, service.Name, path); err != nil {
		return fmt.Errorf("restore backup: %w", err)
	}
	return nil
}

// DeleteBackup removes a recorded backup file and its database record. The
// file is removed first so a failed filesystem operation never loses the
// record of a backup that still exists.
func (s *BackupService) DeleteBackup(ctx context.Context, applicationID int64, serviceName, fileName string) error {
	ctx, cancel := withBackupOperationDeadline(ctx)
	defer cancel()
	item, service, err := s.findDatabaseService(ctx, applicationID, serviceName)
	if err != nil {
		return err
	}
	if err := s.ensureApplicationNotDeleting(ctx, applicationID); err != nil {
		return err
	}
	fileName, err = application.ValidateBackupFileName(fileName)
	if err != nil {
		return err
	}

	release, err := s.acquireLease(ctx, service.ID, "delete")
	if err != nil {
		return err
	}
	defer release()

	backups, err := s.repository.ListBackups(ctx, service.ID)
	if err != nil {
		return fmt.Errorf("list backups for delete: %w", err)
	}
	var found bool
	for _, backup := range backups {
		if backup.ServiceID == service.ID && backup.FileName == fileName {
			found = true
			break
		}
	}
	if !found {
		return application.ErrBackupNotFound
	}

	directory, err := inspectBackupDirectory(s.backupRoot, item.FolderName, service.Name)
	if errors.Is(err, os.ErrNotExist) {
		return s.deleteBackupRecord(ctx, service.ID, fileName)
	}
	if err != nil {
		return fmt.Errorf("inspect backup location: %w", err)
	}
	path, err := safeBackupPath(directory, fileName)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return s.deleteBackupRecord(ctx, service.ID, fileName)
	}
	if err != nil {
		return fmt.Errorf("inspect backup file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("backup file must be a regular file")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete backup file: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync backup directory after delete: %w", err)
	}
	return s.deleteBackupRecord(ctx, service.ID, fileName)
}

// OpenBackup opens a recorded backup file for streaming to a client. The
// caller owns the returned reader and must close it.
func (s *BackupService) OpenBackup(ctx context.Context, applicationID int64, serviceName, fileName string) (io.ReadCloser, application.Backup, error) {
	item, service, err := s.findDatabaseService(ctx, applicationID, serviceName)
	if err != nil {
		return nil, application.Backup{}, err
	}
	fileName, err = application.ValidateBackupFileName(fileName)
	if err != nil {
		return nil, application.Backup{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	backups, err := s.repository.ListBackups(ctx, service.ID)
	if err != nil {
		return nil, application.Backup{}, fmt.Errorf("list backups for download: %w", err)
	}
	var selected application.Backup
	for _, backup := range backups {
		if backup.ServiceID == service.ID && backup.FileName == fileName {
			selected = backup
			break
		}
	}
	if selected.FileName == "" {
		return nil, application.Backup{}, application.ErrBackupNotFound
	}

	directory, err := inspectBackupDirectory(s.backupRoot, item.FolderName, service.Name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, application.Backup{}, application.ErrBackupNotFound
	}
	if err != nil {
		return nil, application.Backup{}, fmt.Errorf("inspect backup location: %w", err)
	}
	path, err := safeBackupPath(directory, selected.FileName)
	if err != nil {
		return nil, application.Backup{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, application.Backup{}, application.ErrBackupNotFound
	}
	if err != nil {
		return nil, application.Backup{}, fmt.Errorf("inspect backup file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, application.Backup{}, errors.New("backup file must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, application.Backup{}, fmt.Errorf("open backup file: %w", err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, application.Backup{}, fmt.Errorf("inspect opened backup file: %w", err)
	}
	if !fileInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, application.Backup{}, errors.New("backup file must be a regular file")
	}
	selected.SizeBytes = fileInfo.Size()
	return file, selected, nil
}

func (s *BackupService) deleteBackupRecord(ctx context.Context, serviceID int64, fileName string) error {
	if err := s.repository.DeleteBackup(ctx, serviceID, fileName); err != nil {
		return fmt.Errorf("delete backup record: %w", err)
	}
	return nil
}

func (s *BackupService) ensureServiceRunning(ctx context.Context, item application.Application, service application.Service) error {
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return fmt.Errorf("resolve application directory for backup status: %w", err)
	}
	running, err := s.runner.IsServiceRunning(ctx, directory, service.Name)
	if err != nil {
		return fmt.Errorf("check database service status: %w", err)
	}
	if !running {
		return application.ErrBackupServiceNotRunning
	}
	return nil
}

func (s *BackupService) runBackup(ctx context.Context, item application.Application, service application.Service, requireEnabled bool) (application.Backup, error) {
	location := s.backupLocation(item, service)
	schedule, err := s.scheduleForService(ctx, service.ID, location)
	if err != nil {
		return application.Backup{}, err
	}
	if requireEnabled && !schedule.Enabled {
		return application.Backup{}, application.ErrBackupScheduleDisabled
	}
	if err := ensureBackupDirectory(s.backupRoot, item.FolderName, service.Name); err != nil {
		return application.Backup{}, fmt.Errorf("prepare backup location: %w", err)
	}

	now := s.clock().UTC()
	if err := recoverAbandonedTemporaryBackups(location, now); err != nil {
		return application.Backup{}, fmt.Errorf("recover temporary backup files: %w", err)
	}
	temporary, err := os.CreateTemp(location, ".redlaunch-backup-*.sql")
	if err != nil {
		return application.Backup{}, fmt.Errorf("create temporary backup file: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return application.Backup{}, fmt.Errorf("prepare temporary backup file: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, fmt.Errorf("resolve application directory for backup: %w", err)
	}
	if err := s.runner.BackupPostgreSQL(ctx, directory, service.Name, temporaryPath); err != nil {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, fmt.Errorf("create backup: %w", err)
	}
	info, err := os.Lstat(temporaryPath)
	if err != nil {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, fmt.Errorf("inspect completed backup: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, errors.New("completed backup is not a regular file")
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, fmt.Errorf("protect completed backup: %w", err)
	}
	if err := syncRegularFile(temporaryPath); err != nil {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, fmt.Errorf("sync completed backup: %w", err)
	}

	fileName, path, err := commitBackupFile(temporaryPath, location, now)
	if err != nil {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, fmt.Errorf("save completed backup: %w", err)
	}
	removeTemporary = false

	backup, err := s.repository.CreateBackup(ctx, application.Backup{
		ServiceID: service.ID,
		FileName:  fileName,
		CreatedAt: now,
		SizeBytes: info.Size(),
	})
	if err != nil {
		removeErr := os.Remove(path)
		if removeErr == nil {
			removeErr = syncDirectory(location)
		}
		s.recordBackupFailure(ctx, schedule, location, now)
		if removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove unrecorded backup file: %w", removeErr))
		}
		return application.Backup{}, fmt.Errorf("record completed backup: %w", err)
	}
	schedule.ServiceID = service.ID
	schedule.BackupLocation = location
	if err := s.updateBackupStatus(ctx, schedule, now, "successful", backup.SizeBytes); err != nil {
		return application.Backup{}, fmt.Errorf("save backup status: %w", err)
	}
	if err := s.applyRetention(ctx, service.ID, location, schedule.RetentionDays, now); err != nil {
		return backup, fmt.Errorf("apply backup retention: %w", err)
	}
	return backup, nil
}

func (s *BackupService) applyRetention(ctx context.Context, serviceID int64, location string, retentionDays int, now time.Time) error {
	if retentionDays < 1 {
		return nil
	}
	backups, err := s.repository.ListBackups(ctx, serviceID)
	if err != nil {
		return err
	}
	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	for _, backup := range backups {
		if backup.CreatedAt.IsZero() || !backup.CreatedAt.Before(cutoff) {
			continue
		}
		fileName, err := application.ValidateBackupFileName(backup.FileName)
		if err != nil {
			continue
		}
		path, err := safeBackupPath(location, fileName)
		if err != nil {
			continue
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			// The record is stale, but removing it keeps the UI honest.
		} else if err != nil {
			return err
		} else {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return errors.New("expired backup is not a regular file")
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			if err := syncDirectory(location); err != nil {
				return fmt.Errorf("sync backup directory after retention cleanup: %w", err)
			}
		}
		if err := s.repository.DeleteBackup(ctx, serviceID, fileName); err != nil && !errors.Is(err, application.ErrBackupNotFound) {
			return err
		}
	}
	return nil
}

func (s *BackupService) recordBackupFailure(ctx context.Context, schedule application.BackupSchedule, location string, at time.Time) {
	schedule.BackupLocation = location
	recoveryContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.updateBackupStatus(recoveryContext, schedule, at, "failed", 0)
}

func (s *BackupService) updateBackupStatus(ctx context.Context, schedule application.BackupSchedule, at time.Time, status string, sizeBytes int64) error {
	if updater, ok := s.repository.(backupStatusRepository); ok {
		if _, err := s.repository.GetBackupSchedule(ctx, schedule.ServiceID); errors.Is(err, application.ErrBackupScheduleNotFound) {
			// The first backup may be the operation that creates the default
			// schedule row. Persist only the default settings before applying
			// status fields through the narrow update method.
			if err := s.repository.SaveBackupSchedule(ctx, application.BackupSchedule{
				ServiceID:      schedule.ServiceID,
				ScheduleType:   schedule.ScheduleType,
				Hour:           schedule.Hour,
				Minute:         schedule.Minute,
				Weekday:        schedule.Weekday,
				RetentionDays:  schedule.RetentionDays,
				BackupLocation: schedule.BackupLocation,
			}); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		return updater.UpdateBackupStatus(ctx, schedule.ServiceID, at, status, sizeBytes)
	}
	schedule.LastBackupAt = at
	schedule.LastBackupStatus = status
	schedule.LastBackupSize = sizeBytes
	return s.repository.SaveBackupSchedule(ctx, schedule)
}

func (s *BackupService) scheduleForService(ctx context.Context, serviceID int64, location string) (application.BackupSchedule, error) {
	schedule, err := s.repository.GetBackupSchedule(ctx, serviceID)
	if errors.Is(err, application.ErrBackupScheduleNotFound) {
		return application.BackupSchedule{
			ServiceID:      serviceID,
			ScheduleType:   application.BackupScheduleDaily,
			Hour:           application.BackupDefaultHour,
			Minute:         application.BackupDefaultMinute,
			RetentionDays:  application.BackupDefaultRetention,
			BackupLocation: location,
		}, nil
	}
	if err != nil {
		return application.BackupSchedule{}, err
	}
	schedule.ServiceID = serviceID
	schedule.BackupLocation = location
	if schedule.ScheduleType == "" {
		schedule.ScheduleType = application.BackupScheduleDaily
	}
	if schedule.RetentionDays == 0 {
		schedule.RetentionDays = application.BackupDefaultRetention
	}
	return schedule, nil
}

func (s *BackupService) findDatabaseService(ctx context.Context, applicationID int64, serviceName string) (application.Application, application.Service, error) {
	if applicationID < 1 {
		return application.Application{}, application.Service{}, application.ErrNotFound
	}
	serviceName, err := application.ValidateServiceName(serviceName)
	if err != nil {
		return application.Application{}, application.Service{}, err
	}
	item, err := s.repository.Get(ctx, applicationID)
	if err != nil {
		return application.Application{}, application.Service{}, err
	}
	if _, err := application.ValidateFolderName(item.FolderName); err != nil {
		return application.Application{}, application.Service{}, fmt.Errorf("validate stored application folder: %w", err)
	}
	services, err := s.repository.ListServices(ctx, applicationID)
	if err != nil {
		return application.Application{}, application.Service{}, err
	}
	for _, service := range services {
		if service.Name != serviceName {
			continue
		}
		if !application.IsDatabaseServiceType(service.Type) {
			return application.Application{}, application.Service{}, application.ErrBackupUnsupported
		}
		if service.ID < 1 {
			return application.Application{}, application.Service{}, application.ErrServiceNotFound
		}
		return item, service, nil
	}
	return application.Application{}, application.Service{}, application.ErrServiceNotFound
}

func (s *BackupService) findDatabaseServiceByID(ctx context.Context, applicationID, serviceID int64) (application.Application, application.Service, error) {
	if applicationID < 1 || serviceID < 1 {
		return application.Application{}, application.Service{}, application.ErrNotFound
	}
	item, err := s.repository.Get(ctx, applicationID)
	if err != nil {
		return application.Application{}, application.Service{}, err
	}
	if _, err := application.ValidateFolderName(item.FolderName); err != nil {
		return application.Application{}, application.Service{}, fmt.Errorf("validate stored application folder: %w", err)
	}
	services, err := s.repository.ListServices(ctx, applicationID)
	if err != nil {
		return application.Application{}, application.Service{}, err
	}
	for _, service := range services {
		if service.ID != serviceID {
			continue
		}
		if !application.IsDatabaseServiceType(service.Type) {
			return application.Application{}, application.Service{}, application.ErrBackupUnsupported
		}
		if _, err := application.ValidateServiceName(service.Name); err != nil {
			return application.Application{}, application.Service{}, application.ErrServiceNotFound
		}
		return item, service, nil
	}
	return application.Application{}, application.Service{}, application.ErrServiceNotFound
}

func (s *BackupService) managedApplicationDirectory(item application.Application) (string, error) {
	folderName, err := application.ValidateFolderName(item.FolderName)
	if err != nil {
		return "", fmt.Errorf("validate stored application folder: %w", err)
	}
	directory := filepath.Join(s.projectsRoot, applicationsDir, folderName)
	applicationsRoot := filepath.Join(s.projectsRoot, applicationsDir)
	relative, err := filepath.Rel(applicationsRoot, directory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("application directory is outside the managed applications directory")
	}
	if err := checkManagedAncestors(applicationsRoot, directory); err != nil {
		return "", err
	}
	// The applications root itself must remain a real directory: replacing it
	// with a symlink after setup would redirect every managed path outside
	// the projects tree while each individual Lstat still looks normal.
	if parentInfo, err := os.Lstat(applicationsRoot); err == nil && parentInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("managed applications directory must not be a symlink")
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return "", application.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("application path is not a directory")
	}
	if err := checkResolvedDirectoryContainment(applicationsRoot, directory); err != nil {
		return "", err
	}
	return directory, nil
}

func (s *BackupService) backupLocation(item application.Application, service application.Service) string {
	return filepath.Join(s.backupRoot, item.FolderName, service.Name)
}

func (s *BackupService) renderUnits(applicationID, serviceID int64, schedule application.BackupSchedule) (string, string) {
	serviceUnitName := backupServiceUnitName(applicationID, serviceID)
	serviceArgs := append([]string(nil), s.executablePrefix...)
	serviceArgs = append(serviceArgs, s.executable, "backup-run", "--application-id", strconv.FormatInt(applicationID, 10), "--service-id", strconv.FormatInt(serviceID, 10))
	if s.databasePath != "" {
		serviceArgs = append(serviceArgs, "--db-path", s.databasePath)
	}
	serviceArgs = append(serviceArgs, "--projects-root", s.projectsRoot, "--backup-root", s.backupRoot)
	execStart := make([]string, 0, len(serviceArgs))
	for _, arg := range serviceArgs {
		execStart = append(execStart, quoteSystemdArgument(arg))
	}
	serviceContents := "[Unit]\n" +
		"Description=Redlaunch database backup service\n" +
		"After=docker.service\n\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		// Bound the dump below the 30-minute backup lease so a live owner can
		// never still be running when its lease expires. Lease expiry then
		// only handles true process death.
		"TimeoutStartSec=" + backupSystemdTimeout + "\n" +
		"ExecStart=" + strings.Join(execStart, " ") + "\n" +
		"PrivateTmp=true\n" +
		"NoNewPrivileges=true\n"
	timerContents := "[Unit]\n" +
		"Description=Redlaunch database backup timer\n\n" +
		"[Timer]\n" +
		"OnCalendar=" + backupCalendarExpression(schedule) + "\n" +
		"Persistent=true\n" +
		"Unit=" + serviceUnitName + "\n\n" +
		"[Install]\n" +
		"WantedBy=timers.target\n"
	return serviceContents, timerContents
}

func backupServiceUnitName(applicationID, serviceID int64) string {
	return "redlaunch-backup-a" + strconv.FormatInt(applicationID, 10) + "-s" + strconv.FormatInt(serviceID, 10) + ".service"
}

func backupTimerUnitName(applicationID, serviceID int64) string {
	return "redlaunch-backup-a" + strconv.FormatInt(applicationID, 10) + "-s" + strconv.FormatInt(serviceID, 10) + ".timer"
}

func backupCalendarExpression(schedule application.BackupSchedule) string {
	switch schedule.ScheduleType {
	case application.BackupScheduleHourly:
		return fmt.Sprintf("*-*-* *:%02d:00", schedule.Minute)
	case application.BackupScheduleWeekly:
		return fmt.Sprintf("%s *-*-* %02d:%02d:00", systemdWeekday(schedule.Weekday), schedule.Hour, schedule.Minute)
	default:
		return fmt.Sprintf("*-*-* %02d:%02d:00", schedule.Hour, schedule.Minute)
	}
}

func systemdWeekday(value string) string {
	switch strings.ToLower(value) {
	case "monday":
		return "Mon"
	case "tuesday":
		return "Tue"
	case "wednesday":
		return "Wed"
	case "thursday":
		return "Thu"
	case "friday":
		return "Fri"
	case "saturday":
		return "Sat"
	case "sunday":
		return "Sun"
	default:
		return "Mon"
	}
}

func quoteSystemdArgument(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, character := range value {
		switch character {
		case '\\', '"':
			builder.WriteByte('\\')
			builder.WriteRune(character)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case '%':
			// Percent specifiers are expanded by systemd in ExecStart. Escape
			// them so configured paths remain literal arguments.
			builder.WriteString(`%%`)
		default:
			builder.WriteRune(character)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

func absoluteManagedPath(value, label string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must not be empty", label)
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	path = filepath.Clean(path)
	if path == string(filepath.Separator) {
		return "", fmt.Errorf("%s must not be the filesystem root", label)
	}
	return path, nil
}

func ensureBackupDirectory(root, folderName, serviceName string) error {
	if _, err := application.ValidateFolderName(folderName); err != nil {
		return err
	}
	if _, err := application.ValidateServiceName(serviceName); err != nil {
		return err
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return err
	}
	applicationDirectory := filepath.Join(root, folderName)
	if err := ensurePrivateDirectory(applicationDirectory); err != nil {
		return err
	}
	return ensurePrivateDirectory(filepath.Join(applicationDirectory, serviceName))
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, backupDirectoryMode); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("backup path must be a directory")
	}
	if err := os.Chmod(path, backupDirectoryMode); err != nil {
		return err
	}
	return nil
}

func inspectBackupDirectory(root, folderName, serviceName string) (string, error) {
	if _, err := application.ValidateFolderName(folderName); err != nil {
		return "", err
	}
	if _, err := application.ValidateServiceName(serviceName); err != nil {
		return "", err
	}
	for _, path := range []string{root, filepath.Join(root, folderName), filepath.Join(root, folderName, serviceName)} {
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("backup path must be a directory")
		}
	}
	return filepath.Join(root, folderName, serviceName), nil
}

func safeBackupPath(directory, fileName string) (string, error) {
	fileName, err := application.ValidateBackupFileName(fileName)
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, fileName)
	relative, err := filepath.Rel(directory, path)
	if err != nil || relative != fileName || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("backup file is outside the backup directory")
	}
	return path, nil
}

// validatePlainSQLDump establishes the supported restore format contract:
// only plain-text SQL dumps produced by pg_dump may be restored through the
// single-transaction psql path. Custom/tar/directory archives start with the
// PGDMP magic or binary framing and are rejected before any database work.
func validatePlainSQLDump(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("inspect backup file: %w", err)
	}
	defer file.Close()
	header := make([]byte, 512)
	n, err := file.Read(header)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("inspect backup file: %w", err)
	}
	header = header[:n]
	if bytes.HasPrefix(header, []byte("PGDMP")) {
		return application.ErrBackupFormatUnsupported
	}
	if bytes.IndexByte(header, 0) >= 0 {
		return application.ErrBackupFormatUnsupported
	}
	return nil
}

func nextBackupFileName(directory string, at time.Time) (string, error) {
	base := "backup-" + at.UTC().Format("20060102-150405.000000000Z")
	for index := 0; index < 1000; index++ {
		name := base + ".sql"
		if index > 0 {
			name = base + "-" + strconv.Itoa(index) + ".sql"
		}
		path, err := safeBackupPath(directory, name)
		if err != nil {
			return "", err
		}
		_, err = os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate a unique backup file name")
}

func safeTemporaryBackupPath(directory, fileName string) (string, error) {
	if fileName == "" || filepath.Base(fileName) != fileName || strings.ContainsAny(fileName, `/\\`) {
		return "", errors.New("temporary backup file is outside the backup directory")
	}
	path := filepath.Join(directory, fileName)
	relative, err := filepath.Rel(directory, path)
	if err != nil || relative != fileName || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("temporary backup file is outside the backup directory")
	}
	return path, nil
}

func commitBackupFile(temporaryPath, directory string, at time.Time) (string, string, error) {
	base := "backup-" + at.UTC().Format("20060102-150405.000000000Z")
	for index := 0; index < 1000; index++ {
		fileName := base + ".sql"
		if index > 0 {
			fileName = base + "-" + strconv.Itoa(index) + ".sql"
		}
		path, err := safeBackupPath(directory, fileName)
		if err != nil {
			return "", "", err
		}
		// Hard-linking the completed temporary file reserves the final name
		// atomically and never overwrites a concurrent backup.
		if err := os.Link(temporaryPath, path); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", "", err
		}
		if err := os.Remove(temporaryPath); err != nil {
			_ = os.Remove(path)
			return "", "", err
		}
		if err := syncDirectory(directory); err != nil {
			cleanupErr := os.Remove(path)
			if cleanupErr == nil {
				cleanupErr = syncDirectory(directory)
			}
			if cleanupErr != nil {
				return "", "", errors.Join(err, fmt.Errorf("remove unpublished backup file: %w", cleanupErr))
			}
			return "", "", err
		}
		return fileName, path, nil
	}
	return "", "", errors.New("could not allocate a unique backup file name")
}

func recoverAbandonedTemporaryBackups(directory string, now time.Time) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	removed := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".redlaunch-backup-") || !strings.HasSuffix(name, ".sql") {
			continue
		}
		path, err := safeTemporaryBackupPath(directory, name)
		if err != nil {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		if now.Sub(info.ModTime()) <= backupTemporaryRetention {
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		removed = true
	}
	if removed {
		return syncDirectory(directory)
	}
	return nil
}

func syncRegularFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}

func validBackupsForService(backups []application.Backup, serviceID int64) []application.Backup {
	valid := make([]application.Backup, 0, len(backups))
	for _, backup := range backups {
		if backup.ServiceID != serviceID {
			continue
		}
		fileName, err := application.ValidateBackupFileName(backup.FileName)
		if err != nil {
			continue
		}
		backup.FileName = fileName
		valid = append(valid, backup)
	}
	return valid
}

type noBackupScheduler struct{}

func newBackupLeaseToken() (string, error) {
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	return hex.EncodeToString(token), nil
}

func (s *BackupService) acquireLease(ctx context.Context, serviceID int64, operation string) (func(), error) {
	leaseRepository, ok := s.repository.(backupLeaseRepository)
	if !ok {
		return func() {}, nil
	}
	token, err := newBackupLeaseToken()
	if err != nil {
		return nil, fmt.Errorf("create backup lease token: %w", err)
	}
	now := s.clock().UTC()
	if err := leaseRepository.AcquireBackupLease(ctx, serviceID, operation, token, now, now.Add(backupLeaseDuration)); err != nil {
		return nil, err
	}
	return func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = leaseRepository.ReleaseBackupLease(cleanupContext, serviceID, token)
	}, nil
}

func (s *BackupService) ensureApplicationNotDeleting(ctx context.Context, applicationID int64) error {
	stateRepository, ok := s.repository.(backupDeletionStateRepository)
	if !ok {
		return nil
	}
	active, err := stateRepository.IsApplicationDeletionActive(ctx, applicationID)
	if err != nil {
		return fmt.Errorf("check application deletion state: %w", err)
	}
	if active {
		return application.ErrApplicationDeletionInProgress
	}
	return nil
}

// withBackupOperationDeadline caps a lease-holding operation below the lease
// window. Callers with an earlier deadline (such as the 15-minute web tracked
// jobs) keep their own shorter bound; unbounded callers such as backup-run
// and direct service uses gain the 25-minute cap. The returned cancel must be
// deferred by the caller.
func withBackupOperationDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= backupOperationTimeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, backupOperationTimeout)
}

func (noBackupScheduler) Install(context.Context, string, string, string, string) error {
	return nil
}

func (noBackupScheduler) Disable(context.Context, string, string) error {
	return nil
}
