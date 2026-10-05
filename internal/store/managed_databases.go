package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"redlaunch/internal/application"
)

// GetManagedDatabaseCluster returns the singleton server-level cluster. A
// missing row means managed databases have never been enabled.
func (s *Store) GetManagedDatabaseCluster(ctx context.Context) (application.ManagedDatabaseCluster, error) {
	var cluster application.ManagedDatabaseCluster
	var enabled int
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT enabled, provider, version, default_user, created_at, updated_at
		FROM managed_database_cluster
		WHERE id = 1`).Scan(&enabled, &cluster.Provider, &cluster.Version, &cluster.DefaultUser, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return application.ManagedDatabaseCluster{}, nil
	}
	if err != nil {
		return application.ManagedDatabaseCluster{}, fmt.Errorf("get managed database cluster: %w", err)
	}
	cluster.Enabled = enabled != 0
	if createdAt != "" {
		cluster.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return application.ManagedDatabaseCluster{}, fmt.Errorf("parse managed cluster creation time: %w", err)
		}
	}
	if updatedAt != "" {
		cluster.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return application.ManagedDatabaseCluster{}, fmt.Errorf("parse managed cluster update time: %w", err)
		}
	}
	return cluster, nil
}

// SaveManagedDatabaseCluster persists the singleton cluster row.
func (s *Store) SaveManagedDatabaseCluster(ctx context.Context, cluster application.ManagedDatabaseCluster) error {
	enabled := 0
	if cluster.Enabled {
		enabled = 1
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	createdAt := now
	if !cluster.CreatedAt.IsZero() {
		createdAt = cluster.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO managed_database_cluster (id, enabled, provider, version, default_user, created_at, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			enabled = excluded.enabled,
			provider = excluded.provider,
			version = excluded.version,
			default_user = excluded.default_user,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at`,
		enabled, cluster.Provider, cluster.Version, cluster.DefaultUser, createdAt, now)
	if err != nil {
		return fmt.Errorf("save managed database cluster: %w", err)
	}
	return nil
}

// ListManagedDatabases returns logical databases in name order.
func (s *Store) ListManagedDatabases(ctx context.Context) ([]application.ManagedDatabase, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, owner, created_at
		FROM managed_databases
		ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list managed databases: %w", err)
	}
	defer rows.Close()
	var items []application.ManagedDatabase
	for rows.Next() {
		var item application.ManagedDatabase
		var createdAt string
		if err := rows.Scan(&item.ID, &item.Name, &item.Owner, &createdAt); err != nil {
			return nil, fmt.Errorf("scan managed database: %w", err)
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse managed database timestamp: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed databases: %w", err)
	}
	return items, nil
}

// GetManagedDatabase returns one logical database by name.
func (s *Store) GetManagedDatabase(ctx context.Context, name string) (application.ManagedDatabase, error) {
	name, err := application.ValidateManagedDatabaseName(name)
	if err != nil {
		return application.ManagedDatabase{}, err
	}
	var item application.ManagedDatabase
	var createdAt string
	err = s.db.QueryRowContext(ctx, `
		SELECT id, name, owner, created_at
		FROM managed_databases
		WHERE name = ?`, name).Scan(&item.ID, &item.Name, &item.Owner, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return application.ManagedDatabase{}, application.ErrManagedDatabaseNotFound
	}
	if err != nil {
		return application.ManagedDatabase{}, fmt.Errorf("get managed database: %w", err)
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return application.ManagedDatabase{}, fmt.Errorf("parse managed database timestamp: %w", err)
	}
	return item, nil
}

// CreateManagedDatabase records one logical database.
func (s *Store) CreateManagedDatabase(ctx context.Context, item application.ManagedDatabase) (application.ManagedDatabase, error) {
	return s.CreateManagedDatabaseWithUser(ctx, item, "")
}

// CreateManagedDatabaseWithUser atomically records a database and, when supplied,
// its newly created login with access to this database only.
func (s *Store) CreateManagedDatabaseWithUser(ctx context.Context, item application.ManagedDatabase, username string) (application.ManagedDatabase, error) {
	name, err := application.ValidateManagedDatabaseName(item.Name)
	if err != nil {
		return application.ManagedDatabase{}, err
	}
	item.Name = name
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	if username != "" {
		username, err = application.ValidateManagedDatabaseUsername(username)
		if err != nil {
			return application.ManagedDatabase{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.ManagedDatabase{}, fmt.Errorf("begin managed database creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO managed_databases (name, owner, created_at)
		VALUES (?, ?, ?)`, item.Name, item.Owner, item.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueConstraint(err) {
			return application.ManagedDatabase{}, application.ErrManagedDatabaseAlreadyExists
		}
		return application.ManagedDatabase{}, fmt.Errorf("create managed database: %w", err)
	}
	item.ID, err = result.LastInsertId()
	if err != nil {
		return application.ManagedDatabase{}, fmt.Errorf("read managed database ID: %w", err)
	}
	if username != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO managed_database_users (username, created_at) VALUES (?, ?)`, username, item.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			if isUniqueConstraint(err) {
				return application.ManagedDatabase{}, application.ErrManagedDatabaseUserAlreadyExists
			}
			return application.ManagedDatabase{}, fmt.Errorf("record managed database user: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO managed_database_grants (username, database_name, created_at) VALUES (?, ?, ?)`, username, item.Name, item.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return application.ManagedDatabase{}, fmt.Errorf("record managed database access: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return application.ManagedDatabase{}, fmt.Errorf("commit managed database creation: %w", err)
	}
	return item, nil
}

// DeleteManagedDatabase removes one logical database and its grants. Backup
// schedules and files are retained as operator-managed artifacts, matching
// application service deletion semantics.
func (s *Store) DeleteManagedDatabase(ctx context.Context, name string) error {
	name, err := application.ValidateManagedDatabaseName(name)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin managed database deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `DELETE FROM managed_databases WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete managed database: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted managed database count: %w", err)
	}
	if affected == 0 {
		return application.ErrManagedDatabaseNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM managed_database_grants WHERE database_name = ?`, name); err != nil {
		return fmt.Errorf("delete managed database grants: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit managed database deletion: %w", err)
	}
	return nil
}

// ListManagedDatabaseUsers returns database users in name order with grants.
func (s *Store) ListManagedDatabaseUsers(ctx context.Context) ([]application.ManagedDatabaseUserDetail, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT username, created_at
		FROM managed_database_users
		ORDER BY username ASC`)
	if err != nil {
		return nil, fmt.Errorf("list managed database users: %w", err)
	}
	defer rows.Close()
	var users []application.ManagedDatabaseUser
	for rows.Next() {
		var user application.ManagedDatabaseUser
		var createdAt string
		if err := rows.Scan(&user.Username, &createdAt); err != nil {
			return nil, fmt.Errorf("scan managed database user: %w", err)
		}
		user.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse managed database user timestamp: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed database users: %w", err)
	}
	details := make([]application.ManagedDatabaseUserDetail, 0, len(users))
	for _, user := range users {
		grants, err := s.listManagedDatabaseGrants(ctx, user.Username)
		if err != nil {
			return nil, err
		}
		details = append(details, application.ManagedDatabaseUserDetail{User: user, Databases: grants})
	}
	return details, nil
}

// GetManagedDatabaseUser returns one user with its grants.
func (s *Store) GetManagedDatabaseUser(ctx context.Context, username string) (application.ManagedDatabaseUserDetail, error) {
	username, err := application.ValidateManagedDatabaseUsername(username)
	if err != nil {
		return application.ManagedDatabaseUserDetail{}, err
	}
	var user application.ManagedDatabaseUser
	var createdAt string
	err = s.db.QueryRowContext(ctx, `
		SELECT username, created_at
		FROM managed_database_users
		WHERE username = ?`, username).Scan(&user.Username, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return application.ManagedDatabaseUserDetail{}, application.ErrManagedDatabaseUserNotFound
	}
	if err != nil {
		return application.ManagedDatabaseUserDetail{}, fmt.Errorf("get managed database user: %w", err)
	}
	user.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return application.ManagedDatabaseUserDetail{}, fmt.Errorf("parse managed database user timestamp: %w", err)
	}
	grants, err := s.listManagedDatabaseGrants(ctx, user.Username)
	if err != nil {
		return application.ManagedDatabaseUserDetail{}, err
	}
	return application.ManagedDatabaseUserDetail{User: user, Databases: grants}, nil
}

// CreateManagedDatabaseUser records one login role and its initial grants.
func (s *Store) CreateManagedDatabaseUser(ctx context.Context, username string, databases []string) error {
	username, err := application.ValidateManagedDatabaseUsername(username)
	if err != nil {
		return err
	}
	grants, err := application.ValidateManagedDatabaseGrants(databases)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin managed database user creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO managed_database_users (username, created_at)
		VALUES (?, ?)`, username, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		if isUniqueConstraint(err) {
			return application.ErrManagedDatabaseUserAlreadyExists
		}
		return fmt.Errorf("create managed database user: %w", err)
	}
	for _, database := range grants {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO managed_database_grants (username, database_name, created_at)
			VALUES (?, ?, ?)`, username, database, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("grant managed database access: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit managed database user creation: %w", err)
	}
	return nil
}

// SetManagedDatabaseGrants replaces one user's allowed databases.
func (s *Store) SetManagedDatabaseGrants(ctx context.Context, username string, databases []string) error {
	username, err := application.ValidateManagedDatabaseUsername(username)
	if err != nil {
		return err
	}
	grants, err := application.ValidateManagedDatabaseGrants(databases)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin managed database grant update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM managed_database_users WHERE username = ?`, username).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return application.ErrManagedDatabaseUserNotFound
		}
		return fmt.Errorf("check managed database user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM managed_database_grants WHERE username = ?`, username); err != nil {
		return fmt.Errorf("clear managed database grants: %w", err)
	}
	for _, database := range grants {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO managed_database_grants (username, database_name, created_at)
			VALUES (?, ?, ?)`, username, database, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("grant managed database access: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit managed database grant update: %w", err)
	}
	return nil
}

// DeleteManagedDatabaseUser removes one login role and its grants.
func (s *Store) DeleteManagedDatabaseUser(ctx context.Context, username string) error {
	username, err := application.ValidateManagedDatabaseUsername(username)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin managed database user deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM managed_database_grants WHERE username = ?`, username); err != nil {
		return fmt.Errorf("delete managed database grants: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM managed_database_users WHERE username = ?`, username)
	if err != nil {
		return fmt.Errorf("delete managed database user: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted managed database user count: %w", err)
	}
	if affected == 0 {
		return application.ErrManagedDatabaseUserNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit managed database user deletion: %w", err)
	}
	return nil
}

func (s *Store) listManagedDatabaseGrants(ctx context.Context, username string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT database_name
		FROM managed_database_grants
		WHERE username = ?
		ORDER BY database_name ASC`, username)
	if err != nil {
		return nil, fmt.Errorf("list managed database grants: %w", err)
	}
	defer rows.Close()
	grants := []string{}
	for rows.Next() {
		var database string
		if err := rows.Scan(&database); err != nil {
			return nil, fmt.Errorf("scan managed database grant: %w", err)
		}
		grants = append(grants, database)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed database grants: %w", err)
	}
	return grants, nil
}

// GetManagedBackupSchedule returns the schedule stored for one managed DB.
func (s *Store) GetManagedBackupSchedule(ctx context.Context, databaseID int64) (application.BackupSchedule, error) {
	var schedule application.BackupSchedule
	var enabled int
	var lastBackupAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT database_id, enabled, schedule_type, hour, minute, weekday,
			retention_days, backup_location, last_backup_at, last_backup_status,
			last_backup_size
		FROM managed_database_backup_schedules
		WHERE database_id = ?`, databaseID).Scan(
		&schedule.ServiceID,
		&enabled,
		&schedule.ScheduleType,
		&schedule.Hour,
		&schedule.Minute,
		&schedule.Weekday,
		&schedule.RetentionDays,
		&schedule.BackupLocation,
		&lastBackupAt,
		&schedule.LastBackupStatus,
		&schedule.LastBackupSize,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return application.BackupSchedule{}, application.ErrBackupScheduleNotFound
	}
	if err != nil {
		return application.BackupSchedule{}, fmt.Errorf("get managed backup schedule: %w", err)
	}
	schedule.Enabled = enabled != 0
	if lastBackupAt != "" {
		schedule.LastBackupAt, err = time.Parse(time.RFC3339Nano, lastBackupAt)
		if err != nil {
			return application.BackupSchedule{}, fmt.Errorf("parse managed backup timestamp: %w", err)
		}
	}
	return schedule, nil
}

// SaveManagedBackupSchedule creates or updates one managed DB schedule.
func (s *Store) SaveManagedBackupSchedule(ctx context.Context, schedule application.BackupSchedule) error {
	enabled := 0
	if schedule.Enabled {
		enabled = 1
	}
	lastBackupAt := ""
	if !schedule.LastBackupAt.IsZero() {
		lastBackupAt = schedule.LastBackupAt.Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO managed_database_backup_schedules (
			database_id, enabled, schedule_type, hour, minute, weekday,
			retention_days, backup_location, last_backup_at, last_backup_status,
			last_backup_size
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(database_id) DO UPDATE SET
			enabled = excluded.enabled,
			schedule_type = excluded.schedule_type,
			hour = excluded.hour,
			minute = excluded.minute,
			weekday = excluded.weekday,
			retention_days = excluded.retention_days,
			backup_location = excluded.backup_location,
			last_backup_at = excluded.last_backup_at,
			last_backup_status = excluded.last_backup_status,
			last_backup_size = excluded.last_backup_size`,
		schedule.ServiceID,
		enabled,
		schedule.ScheduleType,
		schedule.Hour,
		schedule.Minute,
		schedule.Weekday,
		schedule.RetentionDays,
		schedule.BackupLocation,
		lastBackupAt,
		schedule.LastBackupStatus,
		schedule.LastBackupSize,
	)
	if err != nil {
		return fmt.Errorf("save managed backup schedule: %w", err)
	}
	return nil
}

// UpdateManagedBackupStatus changes only completion metadata.
func (s *Store) UpdateManagedBackupStatus(ctx context.Context, databaseID int64, at time.Time, status string, sizeBytes int64) error {
	lastBackupAt := ""
	if !at.IsZero() {
		lastBackupAt = at.UTC().Format(time.RFC3339Nano)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE managed_database_backup_schedules
		SET last_backup_at = ?, last_backup_status = ?, last_backup_size = ?
		WHERE database_id = ?`, lastBackupAt, status, sizeBytes, databaseID)
	if err != nil {
		return fmt.Errorf("update managed backup status: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated managed backup status count: %w", err)
	}
	if affected == 0 {
		return application.ErrBackupScheduleNotFound
	}
	return nil
}

// CreateManagedBackup records one completed managed backup file.
func (s *Store) CreateManagedBackup(ctx context.Context, backup application.Backup) (application.Backup, error) {
	if backup.CreatedAt.IsZero() {
		backup.CreatedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO managed_database_backups (service_id, file_name, created_at, size_bytes)
		VALUES (?, ?, ?, ?)`,
		backup.ServiceID,
		backup.FileName,
		backup.CreatedAt.Format(time.RFC3339Nano),
		backup.SizeBytes,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return application.Backup{}, application.ErrBackupAlreadyExists
		}
		return application.Backup{}, fmt.Errorf("create managed backup record: %w", err)
	}
	backup.ID, err = result.LastInsertId()
	if err != nil {
		return application.Backup{}, fmt.Errorf("read managed backup ID: %w", err)
	}
	return backup, nil
}

// ListManagedBackups returns completed backup files newest first.
func (s *Store) ListManagedBackups(ctx context.Context, databaseID int64) ([]application.Backup, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, service_id, file_name, created_at, size_bytes
		FROM managed_database_backups
		WHERE service_id = ?
		ORDER BY created_at DESC, id DESC`, databaseID)
	if err != nil {
		return nil, fmt.Errorf("list managed backups: %w", err)
	}
	defer rows.Close()
	var backups []application.Backup
	for rows.Next() {
		var backup application.Backup
		var createdAt string
		if err := rows.Scan(&backup.ID, &backup.ServiceID, &backup.FileName, &createdAt, &backup.SizeBytes); err != nil {
			return nil, fmt.Errorf("scan managed backup: %w", err)
		}
		backup.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse managed backup timestamp: %w", err)
		}
		backups = append(backups, backup)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed backups: %w", err)
	}
	return backups, nil
}

// DeleteManagedBackup removes one backup record scoped to its database.
func (s *Store) DeleteManagedBackup(ctx context.Context, databaseID int64, fileName string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM managed_database_backups
		WHERE service_id = ? AND file_name = ?`, databaseID, fileName)
	if err != nil {
		return fmt.Errorf("delete managed backup record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted managed backup count: %w", err)
	}
	if affected == 0 {
		return application.ErrBackupNotFound
	}
	return nil
}

// AcquireManagedBackupLease reserves one managed database for an operation.
func (s *Store) AcquireManagedBackupLease(ctx context.Context, databaseID int64, operation, token string, now, expiresAt time.Time) error {
	if databaseID < 1 || strings.TrimSpace(operation) == "" || strings.TrimSpace(token) == "" {
		return errors.New("backup lease arguments are invalid")
	}
	nowText := now.UTC().UnixNano()
	expiresText := expiresAt.UTC().UnixNano()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin managed backup lease: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM managed_database_backup_leases
		WHERE database_id = ? AND CAST(expires_at AS INTEGER) <= CAST(? AS INTEGER)`, databaseID, nowText); err != nil {
		return fmt.Errorf("reclaim expired managed backup lease: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO managed_database_backup_leases (database_id, operation, token, acquired_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`, databaseID, operation, token, nowText, expiresText); err != nil {
		if isUniqueConstraint(err) {
			return application.ErrBackupOperationInProgress
		}
		return fmt.Errorf("acquire managed backup lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit managed backup lease: %w", err)
	}
	return nil
}

// ReleaseManagedBackupLease releases only the lease matching the token.
func (s *Store) ReleaseManagedBackupLease(ctx context.Context, databaseID int64, token string) error {
	if databaseID < 1 || strings.TrimSpace(token) == "" {
		return errors.New("backup lease arguments are invalid")
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM managed_database_backup_leases
		WHERE database_id = ? AND token = ?`, databaseID, token); err != nil {
		return fmt.Errorf("release managed backup lease: %w", err)
	}
	return nil
}
