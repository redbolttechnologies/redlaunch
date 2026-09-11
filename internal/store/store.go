// Package store implements SQLite persistence for application metadata.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	"redlaunch/internal/application"
)

// ErrNotFound is kept as a store-level alias for callers that already use the
// persistence package while allowing higher layers to depend on the domain
// error instead.
var ErrNotFound = application.ErrNotFound

// Store is the SQLite-backed application repository.
type Store struct {
	db *sql.DB
}

// Open opens a SQLite database, applies pending migrations, and serializes
// access through one connection to keep the small application write-friendly.
func Open(ctx context.Context, databasePath string) (*Store, error) {
	if strings.TrimSpace(databasePath) == "" {
		return nil, errors.New("database path is required")
	}

	databasePath, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if err := ensureParent(databasePath); err != nil {
		return nil, err
	}

	dsn := "file:" + filepath.ToSlash(databasePath) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return store, nil
}

func ensureParent(databasePath string) error {
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o750); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	return nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// AddAuthorizedEmail adds one normalized email address to the login
// allowlist. The table stores no authentication secrets.
func (s *Store) AddAuthorizedEmail(ctx context.Context, email string) error {
	email, err := application.ValidateEmail(email)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO authorized_emails (email, created_at)
		VALUES (?, ?)`, email, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueConstraint(err) {
			return application.ErrAuthorizedEmailAlreadyExists
		}
		return fmt.Errorf("add authorized email: %w", err)
	}
	return nil
}

// IsAuthorizedEmail reports whether an email address is in the login
// allowlist.
func (s *Store) IsAuthorizedEmail(ctx context.Context, email string) (bool, error) {
	email, err := application.ValidateEmail(email)
	if err != nil {
		return false, err
	}
	var exists int
	err = s.db.QueryRowContext(ctx, `
		SELECT 1
		FROM authorized_emails
		WHERE email = ?
		LIMIT 1`, email).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check authorized email: %w", err)
	}
	return exists == 1, nil
}

// ListAuthorizedEmails returns the login allowlist in stable alphabetical
// order. It is primarily useful to administrative tooling and tests.
func (s *Store) ListAuthorizedEmails(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT email
		FROM authorized_emails
		ORDER BY email ASC`)
	if err != nil {
		return nil, fmt.Errorf("list authorized emails: %w", err)
	}
	defer rows.Close()

	var emails []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, fmt.Errorf("scan authorized email: %w", err)
		}
		emails = append(emails, email)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate authorized emails: %w", err)
	}
	return emails, nil
}

// GetRedlaunchPublicAccess returns the installation-wide public-access
// settings for the Redlaunch management interface.
func (s *Store) GetRedlaunchPublicAccess(ctx context.Context) (application.RedlaunchPublicAccess, error) {
	var settings application.RedlaunchPublicAccess
	var enabled int
	err := s.db.QueryRowContext(ctx, `
		SELECT public_access_enabled, public_access_domain
		FROM redlaunch_settings
		WHERE id = 1`).Scan(&enabled, &settings.Domain)
	if err != nil {
		return application.RedlaunchPublicAccess{}, fmt.Errorf("get Redlaunch public access settings: %w", err)
	}
	settings.Enabled = enabled == 1
	return settings, nil
}

// UpdateRedlaunchPublicAccess persists the installation-wide public-access
// settings as one atomic SQLite update.
func (s *Store) UpdateRedlaunchPublicAccess(ctx context.Context, settings application.RedlaunchPublicAccess) error {
	enabled := 0
	if settings.Enabled {
		enabled = 1
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE redlaunch_settings
		SET public_access_enabled = ?, public_access_domain = ?
		WHERE id = 1`, enabled, settings.Domain)
	if err != nil {
		return fmt.Errorf("update Redlaunch public access settings: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated Redlaunch settings count: %w", err)
	}
	if affected != 1 {
		return errors.New("Redlaunch public access settings are not initialized")
	}
	return nil
}

// List returns applications in creation order.
func (s *Store) List(ctx context.Context) ([]application.Application, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, folder_name, created_at,
			(SELECT COUNT(*) FROM services WHERE application_id = applications.id)
		FROM applications
		ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	defer rows.Close()

	var applications []application.Application
	for rows.Next() {
		var item application.Application
		var createdAt string
		if err := rows.Scan(&item.ID, &item.Name, &item.FolderName, &createdAt, &item.ServiceCount); err != nil {
			return nil, fmt.Errorf("scan application: %w", err)
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse application timestamp: %w", err)
		}
		applications = append(applications, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applications: %w", err)
	}
	return applications, nil
}

// ListApplications is an explicit alias for callers that prefer repository
// methods named after the resource.
func (s *Store) ListApplications(ctx context.Context) ([]application.Application, error) {
	return s.List(ctx)
}

// Get returns one application by its database ID.
func (s *Store) Get(ctx context.Context, id int64) (application.Application, error) {
	var item application.Application
	var createdAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, folder_name, created_at,
			(SELECT COUNT(*) FROM services WHERE application_id = applications.id)
		FROM applications
		WHERE id = ?`, id).Scan(&item.ID, &item.Name, &item.FolderName, &createdAt, &item.ServiceCount)
	if errors.Is(err, sql.ErrNoRows) {
		return application.Application{}, ErrNotFound
	}
	if err != nil {
		return application.Application{}, fmt.Errorf("get application: %w", err)
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return application.Application{}, fmt.Errorf("parse application timestamp: %w", err)
	}
	return item, nil
}

// ListServices returns an application's service metadata in creation order.
func (s *Store) ListServices(ctx context.Context, applicationID int64) ([]application.Service, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, application_id, name, service_type, image_name, postgres_version, database_name, database_user,
			redis_version, redis_port, redis_persist_to_disk, created_at
		FROM services
		WHERE application_id = ?
		ORDER BY id ASC`, applicationID)
	if err != nil {
		return nil, fmt.Errorf("list application services: %w", err)
	}
	defer rows.Close()

	var services []application.Service
	for rows.Next() {
		var item application.Service
		var createdAt string
		var redisPersistToDisk int
		if err := rows.Scan(
			&item.ID,
			&item.ApplicationID,
			&item.Name,
			&item.Type,
			&item.ImageName,
			&item.PostgresVersion,
			&item.DatabaseName,
			&item.DatabaseUser,
			&item.RedisVersion,
			&item.RedisPort,
			&redisPersistToDisk,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan application service: %w", err)
		}
		item.RedisPersistToDisk = redisPersistToDisk != 0
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse application service timestamp: %w", err)
		}
		services = append(services, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate application services: %w", err)
	}
	return services, nil
}

// GetBackupSchedule returns the schedule stored for one service.
func (s *Store) GetBackupSchedule(ctx context.Context, serviceID int64) (application.BackupSchedule, error) {
	var schedule application.BackupSchedule
	var enabled int
	var lastBackupAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT service_id, enabled, schedule_type, hour, minute, weekday,
			retention_days, backup_location, last_backup_at, last_backup_status,
			last_backup_size
		FROM backup_schedules
		WHERE service_id = ?`, serviceID).Scan(
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
		return application.BackupSchedule{}, fmt.Errorf("get backup schedule: %w", err)
	}
	schedule.Enabled = enabled != 0
	if lastBackupAt != "" {
		schedule.LastBackupAt, err = time.Parse(time.RFC3339Nano, lastBackupAt)
		if err != nil {
			return application.BackupSchedule{}, fmt.Errorf("parse backup timestamp: %w", err)
		}
	}
	return schedule, nil
}

// SaveBackupSchedule creates or updates one service's backup schedule.
func (s *Store) SaveBackupSchedule(ctx context.Context, schedule application.BackupSchedule) error {
	enabled := 0
	if schedule.Enabled {
		enabled = 1
	}
	lastBackupAt := ""
	if !schedule.LastBackupAt.IsZero() {
		lastBackupAt = schedule.LastBackupAt.Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO backup_schedules (
			service_id, enabled, schedule_type, hour, minute, weekday,
			retention_days, backup_location, last_backup_at, last_backup_status,
			last_backup_size
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(service_id) DO UPDATE SET
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
		return fmt.Errorf("save backup schedule: %w", err)
	}
	return nil
}

// CreateBackup records one completed backup file.
func (s *Store) CreateBackup(ctx context.Context, backup application.Backup) (application.Backup, error) {
	if backup.CreatedAt.IsZero() {
		backup.CreatedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO backups (service_id, file_name, created_at, size_bytes)
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
		return application.Backup{}, fmt.Errorf("create backup record: %w", err)
	}
	backup.ID, err = result.LastInsertId()
	if err != nil {
		return application.Backup{}, fmt.Errorf("read backup ID: %w", err)
	}
	return backup, nil
}

// ListBackups returns completed backup files newest first.
func (s *Store) ListBackups(ctx context.Context, serviceID int64) ([]application.Backup, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, service_id, file_name, created_at, size_bytes
		FROM backups
		WHERE service_id = ?
		ORDER BY created_at DESC, id DESC`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	defer rows.Close()

	var backups []application.Backup
	for rows.Next() {
		var backup application.Backup
		var createdAt string
		if err := rows.Scan(&backup.ID, &backup.ServiceID, &backup.FileName, &createdAt, &backup.SizeBytes); err != nil {
			return nil, fmt.Errorf("scan backup: %w", err)
		}
		backup.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse backup timestamp: %w", err)
		}
		backups = append(backups, backup)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate backups: %w", err)
	}
	return backups, nil
}

// DeleteBackup removes one backup record scoped to its service.
func (s *Store) DeleteBackup(ctx context.Context, serviceID int64, fileName string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM backups
		WHERE service_id = ? AND file_name = ?`, serviceID, fileName)
	if err != nil {
		return fmt.Errorf("delete backup record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted backup count: %w", err)
	}
	if affected == 0 {
		return application.ErrBackupNotFound
	}
	return nil
}

// ListDomains returns an application's domain names in creation order.
func (s *Store) ListDomains(ctx context.Context, applicationID int64) ([]application.Domain, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, application_id, name
		FROM domains
		WHERE application_id = ?
		ORDER BY id ASC`, applicationID)
	if err != nil {
		return nil, fmt.Errorf("list application domains: %w", err)
	}
	defer rows.Close()

	var domains []application.Domain
	for rows.Next() {
		var item application.Domain
		if err := rows.Scan(&item.ID, &item.ApplicationID, &item.Name); err != nil {
			return nil, fmt.Errorf("scan application domain: %w", err)
		}
		domains = append(domains, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate application domains: %w", err)
	}
	return domains, nil
}

// GetDomain returns one domain associated with an application.
func (s *Store) GetDomain(ctx context.Context, applicationID, domainID int64) (application.Domain, error) {
	var item application.Domain
	err := s.db.QueryRowContext(ctx, `
		SELECT id, application_id, name
		FROM domains
		WHERE application_id = ? AND id = ?`, applicationID, domainID).Scan(&item.ID, &item.ApplicationID, &item.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return application.Domain{}, application.ErrDomainNotFound
	}
	if err != nil {
		return application.Domain{}, fmt.Errorf("get application domain: %w", err)
	}
	return item, nil
}

// ListRoutings returns the mappings for one application's associated domain
// in creation order.
func (s *Store) ListRoutings(ctx context.Context, applicationID, domainID int64) ([]application.Routing, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.application_id, r.domain_id, d.name, r.subdomain,
			r.path, r.service_name, r.service_port, r.service_path
		FROM routings r
		JOIN domains d ON d.id = r.domain_id AND d.application_id = r.application_id
		WHERE r.application_id = ? AND r.domain_id = ?
		ORDER BY r.id ASC`, applicationID, domainID)
	if err != nil {
		return nil, fmt.Errorf("list application routings: %w", err)
	}
	defer rows.Close()

	return scanRoutings(rows)
}

// ListAllRoutings returns all mappings with their associated domain names.
// It is used when regenerating the shared Caddy configuration.
func (s *Store) ListAllRoutings(ctx context.Context) ([]application.Routing, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.application_id, r.domain_id, d.name, r.subdomain,
			r.path, r.service_name, r.service_port, r.service_path
		FROM routings r
		JOIN domains d ON d.id = r.domain_id AND d.application_id = r.application_id
		ORDER BY r.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all routings: %w", err)
	}
	defer rows.Close()

	return scanRoutings(rows)
}

// GetRouting returns one mapping scoped to its application and domain.
func (s *Store) GetRouting(ctx context.Context, applicationID, domainID, routingID int64) (application.Routing, error) {
	var item application.Routing
	err := s.db.QueryRowContext(ctx, `
		SELECT r.id, r.application_id, r.domain_id, d.name, r.subdomain,
			r.path, r.service_name, r.service_port, r.service_path
		FROM routings r
		JOIN domains d ON d.id = r.domain_id AND d.application_id = r.application_id
		WHERE r.application_id = ? AND r.domain_id = ? AND r.id = ?`, applicationID, domainID, routingID).
		Scan(&item.ID, &item.ApplicationID, &item.DomainID, &item.DomainName, &item.Subdomain, &item.Path, &item.ServiceName, &item.ServicePort, &item.ServicePath)
	if errors.Is(err, sql.ErrNoRows) {
		return application.Routing{}, application.ErrRoutingNotFound
	}
	if err != nil {
		return application.Routing{}, fmt.Errorf("get application routing: %w", err)
	}
	return item, nil
}

func scanRoutings(rows *sql.Rows) ([]application.Routing, error) {
	var routings []application.Routing
	for rows.Next() {
		var item application.Routing
		if err := rows.Scan(
			&item.ID,
			&item.ApplicationID,
			&item.DomainID,
			&item.DomainName,
			&item.Subdomain,
			&item.Path,
			&item.ServiceName,
			&item.ServicePort,
			&item.ServicePath,
		); err != nil {
			return nil, fmt.Errorf("scan application routing: %w", err)
		}
		routings = append(routings, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate application routings: %w", err)
	}
	return routings, nil
}

// Create persists an application and returns it with its database ID.
func (s *Store) Create(ctx context.Context, item application.Application) (application.Application, error) {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}

	var exists int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1
		FROM applications
		WHERE name = ? OR folder_name = ?
		LIMIT 1`, item.Name, item.FolderName).Scan(&exists)
	if err == nil {
		return application.Application{}, application.ErrAlreadyExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return application.Application{}, fmt.Errorf("check application uniqueness: %w", err)
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO applications (name, folder_name, created_at)
		VALUES (?, ?, ?)`, item.Name, item.FolderName, item.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueConstraint(err) {
			return application.Application{}, application.ErrAlreadyExists
		}
		return application.Application{}, fmt.Errorf("create application: %w", err)
	}
	item.ID, err = result.LastInsertId()
	if err != nil {
		return application.Application{}, fmt.Errorf("read application ID: %w", err)
	}
	return item, nil
}

// CreateApplication is an explicit alias for callers that prefer repository
// methods named after the resource.
func (s *Store) CreateApplication(ctx context.Context, item application.Application) (application.Application, error) {
	return s.Create(ctx, item)
}

// DeleteApplication removes an application's metadata. Related services,
// domains, routings, backup schedules, and backup records are removed by the
// database's foreign-key cascades.
func (s *Store) DeleteApplication(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM applications
		WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete application: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted application count: %w", err)
	}
	if affected == 0 {
		return application.ErrNotFound
	}
	return nil
}

// CreateService persists service metadata and returns it with its database ID.
// Secrets are deliberately not part of the service record.
func (s *Store) CreateService(ctx context.Context, item application.Service) (application.Service, error) {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	if item.Type == "" {
		item.Type = "generic"
	}
	result, err := s.db.ExecContext(ctx, serviceInsertSQL,
		item.ApplicationID,
		item.Name,
		item.Type,
		item.ImageName,
		item.PostgresVersion,
		item.DatabaseName,
		item.DatabaseUser,
		item.RedisVersion,
		item.RedisPort,
		redisPersistValue(item.RedisPersistToDisk),
		item.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return application.Service{}, application.ErrServiceAlreadyExists
		}
		return application.Service{}, fmt.Errorf("create application service: %w", err)
	}
	item.ID, err = result.LastInsertId()
	if err != nil {
		return application.Service{}, fmt.Errorf("read application service ID: %w", err)
	}
	return item, nil
}

const serviceInsertSQL = `
		INSERT INTO services (
			application_id, name, service_type, image_name, postgres_version,
			database_name, database_user, redis_version, redis_port,
			redis_persist_to_disk, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func redisPersistValue(persist bool) int {
	if persist {
		return 1
	}
	return 0
}

// CreateServices persists a set of service records in one SQLite transaction.
// Compose import uses this path so metadata cannot be partially registered.
func (s *Store) CreateServices(ctx context.Context, items []application.Service) ([]application.Service, error) {
	if len(items) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin application service transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	created := make([]application.Service, 0, len(items))
	for _, item := range items {
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if item.Type == "" {
			item.Type = "generic"
		}
		result, err := tx.ExecContext(ctx, serviceInsertSQL,
			item.ApplicationID,
			item.Name,
			item.Type,
			item.ImageName,
			item.PostgresVersion,
			item.DatabaseName,
			item.DatabaseUser,
			item.RedisVersion,
			item.RedisPort,
			redisPersistValue(item.RedisPersistToDisk),
			item.CreatedAt.Format(time.RFC3339Nano),
		)
		if err != nil {
			if isUniqueConstraint(err) {
				return nil, application.ErrServiceAlreadyExists
			}
			return nil, fmt.Errorf("create application service: %w", err)
		}
		item.ID, err = result.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("read application service ID: %w", err)
		}
		created = append(created, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit application service transaction: %w", err)
	}
	return created, nil
}

// DeleteService removes one service's metadata from an application.
func (s *Store) DeleteService(ctx context.Context, applicationID int64, serviceName string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM services
		WHERE application_id = ? AND name = ?`, applicationID, serviceName)
	if err != nil {
		return fmt.Errorf("delete application service: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted application service count: %w", err)
	}
	if affected == 0 {
		return application.ErrServiceNotFound
	}
	return nil
}

// CreateDomain persists a domain name associated with an application.
func (s *Store) CreateDomain(ctx context.Context, item application.Domain) (application.Domain, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO domains (application_id, name)
		VALUES (?, ?)`, item.ApplicationID, item.Name)
	if err != nil {
		if isUniqueConstraint(err) {
			return application.Domain{}, application.ErrDomainAlreadyExists
		}
		return application.Domain{}, fmt.Errorf("create application domain: %w", err)
	}
	item.ID, err = result.LastInsertId()
	if err != nil {
		return application.Domain{}, fmt.Errorf("read application domain ID: %w", err)
	}
	return item, nil
}

// DeleteDomain removes one domain name from an application.
func (s *Store) DeleteDomain(ctx context.Context, applicationID int64, domainName string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM domains
		WHERE application_id = ? AND name = ?`, applicationID, domainName)
	if err != nil {
		return fmt.Errorf("delete application domain: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted application domain count: %w", err)
	}
	if affected == 0 {
		return application.ErrDomainNotFound
	}
	return nil
}

// CreateRouting persists one mapping for an application's associated domain.
func (s *Store) CreateRouting(ctx context.Context, item application.Routing) (application.Routing, error) {
	servicePort, err := application.ValidateRoutingPort(item.ServicePort)
	if err != nil {
		return application.Routing{}, err
	}
	item.ServicePort = servicePort
	var exists int
	err = s.db.QueryRowContext(ctx, `
		SELECT 1
		FROM domains
		WHERE application_id = ? AND id = ?`, item.ApplicationID, item.DomainID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return application.Routing{}, application.ErrDomainNotFound
	}
	if err != nil {
		return application.Routing{}, fmt.Errorf("check routing domain: %w", err)
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO routings (application_id, domain_id, subdomain, path, service_name, service_port, service_path)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		item.ApplicationID,
		item.DomainID,
		item.Subdomain,
		item.Path,
		item.ServiceName,
		item.ServicePort,
		item.ServicePath,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return application.Routing{}, application.ErrRoutingAlreadyExists
		}
		return application.Routing{}, fmt.Errorf("create application routing: %w", err)
	}
	item.ID, err = result.LastInsertId()
	if err != nil {
		return application.Routing{}, fmt.Errorf("read application routing ID: %w", err)
	}
	return item, nil
}

// UpdateRouting changes one mapping scoped to its application and domain.
func (s *Store) UpdateRouting(ctx context.Context, item application.Routing) error {
	servicePort, err := application.ValidateRoutingPort(item.ServicePort)
	if err != nil {
		return err
	}
	item.ServicePort = servicePort
	result, err := s.db.ExecContext(ctx, `
		UPDATE routings
		SET subdomain = ?, path = ?, service_name = ?, service_port = ?, service_path = ?
		WHERE id = ? AND application_id = ? AND domain_id = ?`,
		item.Subdomain,
		item.Path,
		item.ServiceName,
		item.ServicePort,
		item.ServicePath,
		item.ID,
		item.ApplicationID,
		item.DomainID,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return application.ErrRoutingAlreadyExists
		}
		return fmt.Errorf("update application routing: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated application routing count: %w", err)
	}
	if affected == 0 {
		return application.ErrRoutingNotFound
	}
	return nil
}

// DeleteRouting removes one mapping scoped to its application and domain.
func (s *Store) DeleteRouting(ctx context.Context, applicationID, domainID, routingID int64) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM routings
		WHERE id = ? AND application_id = ? AND domain_id = ?`, routingID, applicationID, domainID)
	if err != nil {
		return fmt.Errorf("delete application routing: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted application routing count: %w", err)
	}
	if affected == 0 {
		return application.ErrRoutingNotFound
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	var applicationMigrationApplied int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 1`).Scan(&applicationMigrationApplied); err != nil {
		return fmt.Errorf("check application migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS applications (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			folder_name TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create applications table: %w", err)
	}
	if applicationMigrationApplied == 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, applied_at)
			VALUES (1, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record application migration: %w", err)
		}
	}

	var servicesMigrationApplied int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 2`).Scan(&servicesMigrationApplied); err != nil {
		return fmt.Errorf("check services migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS services (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			application_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE (application_id, name),
			FOREIGN KEY (application_id) REFERENCES applications (id) ON DELETE CASCADE
		)`); err != nil {
		return fmt.Errorf("create services table: %w", err)
	}
	if servicesMigrationApplied == 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, applied_at)
			VALUES (2, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record services migration: %w", err)
		}
	}

	var serviceMetadataMigrationApplied int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 3`).Scan(&serviceMetadataMigrationApplied); err != nil {
		return fmt.Errorf("check service metadata migration: %w", err)
	}
	if serviceMetadataMigrationApplied == 0 {
		for _, statement := range []string{
			`ALTER TABLE services ADD COLUMN service_type TEXT NOT NULL DEFAULT 'generic'`,
			`ALTER TABLE services ADD COLUMN postgres_version TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE services ADD COLUMN database_name TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE services ADD COLUMN database_user TEXT NOT NULL DEFAULT ''`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("add service metadata column: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, applied_at)
			VALUES (3, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record service metadata migration: %w", err)
		}
	}

	var redisServiceMetadataMigrationApplied int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 4`).Scan(&redisServiceMetadataMigrationApplied); err != nil {
		return fmt.Errorf("check Redis service metadata migration: %w", err)
	}
	if redisServiceMetadataMigrationApplied == 0 {
		for _, statement := range []string{
			`ALTER TABLE services ADD COLUMN redis_version TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE services ADD COLUMN redis_port TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE services ADD COLUMN redis_persist_to_disk INTEGER NOT NULL DEFAULT 0`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("add Redis service metadata column: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, applied_at)
			VALUES (4, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record Redis service metadata migration: %w", err)
		}
	}

	var domainsMigrationApplied int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 5`).Scan(&domainsMigrationApplied); err != nil {
		return fmt.Errorf("check domains migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS domains (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			application_id INTEGER NOT NULL,
			name TEXT NOT NULL COLLATE NOCASE,
			UNIQUE (application_id, name),
			FOREIGN KEY (application_id) REFERENCES applications (id) ON DELETE CASCADE
		)`); err != nil {
		return fmt.Errorf("create domains table: %w", err)
	}
	if domainsMigrationApplied == 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, applied_at)
			VALUES (5, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record domains migration: %w", err)
		}
	}

	var routingsMigrationApplied int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 6`).Scan(&routingsMigrationApplied); err != nil {
		return fmt.Errorf("check routings migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS routings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			application_id INTEGER NOT NULL,
			domain_id INTEGER NOT NULL,
			subdomain TEXT NOT NULL COLLATE NOCASE,
			path TEXT NOT NULL,
			service_name TEXT NOT NULL,
			service_path TEXT NOT NULL,
			UNIQUE (application_id, domain_id, subdomain, path),
			FOREIGN KEY (application_id) REFERENCES applications (id) ON DELETE CASCADE,
			FOREIGN KEY (domain_id) REFERENCES domains (id) ON DELETE CASCADE
		)`); err != nil {
		return fmt.Errorf("create routings table: %w", err)
	}
	if routingsMigrationApplied == 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, applied_at)
			VALUES (6, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record routings migration: %w", err)
		}
	}

	var applicationImageMigrationApplied int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 7`).Scan(&applicationImageMigrationApplied); err != nil {
		return fmt.Errorf("check application image migration: %w", err)
	}
	if applicationImageMigrationApplied == 0 {
		if _, err := tx.ExecContext(ctx, `
			ALTER TABLE services ADD COLUMN image_name TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add application image metadata column: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, applied_at)
			VALUES (7, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record application image migration: %w", err)
		}
	}

	var backupsMigrationApplied int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 8`).Scan(&backupsMigrationApplied); err != nil {
		return fmt.Errorf("check backups migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS backup_schedules (
			service_id INTEGER PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 0,
			schedule_type TEXT NOT NULL DEFAULT 'daily',
			hour INTEGER NOT NULL DEFAULT 3,
			minute INTEGER NOT NULL DEFAULT 0,
			weekday TEXT NOT NULL DEFAULT '',
			retention_days INTEGER NOT NULL DEFAULT 14,
			backup_location TEXT NOT NULL,
			last_backup_at TEXT NOT NULL DEFAULT '',
			last_backup_status TEXT NOT NULL DEFAULT '',
			last_backup_size INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (service_id) REFERENCES services (id) ON DELETE CASCADE
		)`); err != nil {
		return fmt.Errorf("create backup schedules table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS backups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			service_id INTEGER NOT NULL,
			file_name TEXT NOT NULL,
			created_at TEXT NOT NULL,
			size_bytes INTEGER NOT NULL,
			UNIQUE (service_id, file_name),
			FOREIGN KEY (service_id) REFERENCES services (id) ON DELETE CASCADE
		)`); err != nil {
		return fmt.Errorf("create backups table: %w", err)
	}
	if backupsMigrationApplied == 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, applied_at)
			VALUES (8, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record backups migration: %w", err)
		}
	}

	var authorizedEmailsMigrationApplied int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 9`).Scan(&authorizedEmailsMigrationApplied); err != nil {
		return fmt.Errorf("check authorized emails migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS authorized_emails (
			email TEXT PRIMARY KEY COLLATE NOCASE,
			created_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create authorized emails table: %w", err)
	}
	if authorizedEmailsMigrationApplied == 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, applied_at)
			VALUES (9, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record authorized emails migration: %w", err)
		}
	}

	var redlaunchSettingsMigrationApplied int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 10`).Scan(&redlaunchSettingsMigrationApplied); err != nil {
		return fmt.Errorf("check Redlaunch settings migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS redlaunch_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			public_access_enabled INTEGER NOT NULL DEFAULT 0 CHECK (public_access_enabled IN (0, 1)),
			public_access_domain TEXT NOT NULL DEFAULT ''
		)`); err != nil {
		return fmt.Errorf("create Redlaunch settings table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO redlaunch_settings (id)
		VALUES (1)`); err != nil {
		return fmt.Errorf("initialize Redlaunch settings: %w", err)
	}
	if redlaunchSettingsMigrationApplied == 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, applied_at)
			VALUES (10, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record Redlaunch settings migration: %w", err)
		}
	}

	var githubActionsMigrationApplied int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 11`).Scan(&githubActionsMigrationApplied); err != nil {
		return fmt.Errorf("check GitHub Actions migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS github_actions_integrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			application_id INTEGER NOT NULL UNIQUE,
			repository TEXT NOT NULL,
			branch TEXT NOT NULL,
			dockerfile TEXT NOT NULL,
			build_context TEXT NOT NULL,
			service_name TEXT NOT NULL,
			image_name TEXT NOT NULL,
			server_host TEXT NOT NULL,
			server_port INTEGER NOT NULL,
			ssh_username TEXT NOT NULL,
			public_key TEXT NOT NULL,
			key_fingerprint TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY (application_id) REFERENCES applications (id) ON DELETE CASCADE
		)`); err != nil {
		return fmt.Errorf("create GitHub Actions integrations table: %w", err)
	}
	if githubActionsMigrationApplied == 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, applied_at)
			VALUES (11, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record GitHub Actions migration: %w", err)
		}
	}

	var routingPortMigrationApplied int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM schema_migrations
		WHERE version = 12`).Scan(&routingPortMigrationApplied); err != nil {
		return fmt.Errorf("check routing port migration: %w", err)
	}
	if routingPortMigrationApplied == 0 {
		if _, err := tx.ExecContext(ctx, `
			ALTER TABLE routings ADD COLUMN service_port INTEGER NOT NULL DEFAULT 80`); err != nil {
			return fmt.Errorf("add routing service port column: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, applied_at)
			VALUES (12, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record routing port migration: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func isUniqueConstraint(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}
