package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

const (
	managedDatabasesDir           = "databases"
	managedDatabaseServiceName    = "postgres"
	managedDatabaseContainerName  = "redbolt-databases"
	managedDatabaseVolumeKey      = "data"
	managedDatabaseVolumeName     = "redbolt-databases-data"
	managedDatabaseProjectLockKey = "managed-databases"
)

// ManagedDatabaseRepository is the persistence capability required by the
// server-level managed databases service.
type ManagedDatabaseRepository interface {
	GetManagedDatabaseCluster(context.Context) (application.ManagedDatabaseCluster, error)
	SaveManagedDatabaseCluster(context.Context, application.ManagedDatabaseCluster) error
	ListManagedDatabases(context.Context) ([]application.ManagedDatabase, error)
	GetManagedDatabase(context.Context, string) (application.ManagedDatabase, error)
	CreateManagedDatabase(context.Context, application.ManagedDatabase) (application.ManagedDatabase, error)
	CreateManagedDatabaseWithUser(context.Context, application.ManagedDatabase, string) (application.ManagedDatabase, error)
	DeleteManagedDatabase(context.Context, string) error
	ListManagedDatabaseUsers(context.Context) ([]application.ManagedDatabaseUserDetail, error)
	GetManagedDatabaseUser(context.Context, string) (application.ManagedDatabaseUserDetail, error)
	CreateManagedDatabaseUser(context.Context, string, []string) error
	SetManagedDatabaseGrants(context.Context, string, []string) error
	DeleteManagedDatabaseUser(context.Context, string) error
	GetManagedBackupSchedule(context.Context, int64) (application.BackupSchedule, error)
	SaveManagedBackupSchedule(context.Context, application.BackupSchedule) error
	UpdateManagedBackupStatus(context.Context, int64, time.Time, string, int64) error
	CreateManagedBackup(context.Context, application.Backup) (application.Backup, error)
	ListManagedBackups(context.Context, int64) ([]application.Backup, error)
	DeleteManagedBackup(context.Context, int64, string) error
	AcquireManagedBackupLease(context.Context, int64, string, string, time.Time, time.Time) error
	ReleaseManagedBackupLease(context.Context, int64, string) error
}

// ManagedDatabaseRunner performs Docker operations for the shared cluster.
type ManagedDatabaseRunner interface {
	EnsureNetwork(context.Context, string) error
	Up(ctx context.Context, projectDir string) error
	ConfigServices(ctx context.Context, projectDir string) ([]compose.ConfiguredService, error)
	IsServiceRunning(ctx context.Context, projectDir, serviceName string) (bool, error)
	BackupPostgreSQL(ctx context.Context, projectDir, serviceName, destination string) error
	RestorePostgreSQL(ctx context.Context, projectDir, serviceName, source string) error
	ExecPostgresSQL(ctx context.Context, projectDir, serviceName, sql string) (string, error)
	Start(ctx context.Context, projectDir, serviceName string) error
	Stop(ctx context.Context, projectDir, serviceName string) error
	Restart(ctx context.Context, projectDir, serviceName string) error
	Logs(ctx context.Context, projectDir, serviceName string, tail int) (string, error)
	OpenLogs(ctx context.Context, projectDir, serviceName string) (io.ReadCloser, error)
	ListServices(ctx context.Context, projectDir string) ([]compose.ServiceRuntime, error)
}

// ManagedDatabaseConfig configures the server-level database cluster.
type ManagedDatabaseConfig struct {
	ProjectsRoot     string
	BackupRoot       string
	DatabasePath     string
	Executable       string
	ExecutablePrefix []string
	Runner           ManagedDatabaseRunner
	Scheduler        BackupScheduler
	Clock            func() time.Time
}

// ManagedDatabases coordinates the server-level PostgreSQL cluster, logical
// databases, roles, and per-database backups.
type ManagedDatabases struct {
	repository       ManagedDatabaseRepository
	projectsRoot     string
	backupRoot       string
	databasePath     string
	executable       string
	executablePrefix []string
	runner           ManagedDatabaseRunner
	scheduler        BackupScheduler
	clock            func() time.Time
	projectLocks     *projectLockManager
	mu               sync.Mutex
}

// NewManagedDatabases constructs the managed databases service.
func NewManagedDatabases(repository ManagedDatabaseRepository, config ManagedDatabaseConfig) (*ManagedDatabases, error) {
	if repository == nil {
		return nil, errors.New("managed database repository is required")
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
			return nil, errors.New("managed database executable prefix contains an empty argument")
		}
		executablePrefix[index] = argument
	}
	if config.Runner == nil {
		return nil, errors.New("managed database runner is required")
	}
	scheduler := config.Scheduler
	if scheduler == nil {
		scheduler = noBackupScheduler{}
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &ManagedDatabases{
		repository:       repository,
		projectsRoot:     projectsRoot,
		backupRoot:       backupRoot,
		databasePath:     databasePath,
		executable:       executable,
		executablePrefix: executablePrefix,
		runner:           config.Runner,
		scheduler:        scheduler,
		clock:            clock,
		projectLocks:     newProjectLockManager(),
	}, nil
}

// GetCluster returns the stored cluster configuration.
func (s *ManagedDatabases) GetCluster(ctx context.Context) (application.ManagedDatabaseCluster, error) {
	return s.repository.GetManagedDatabaseCluster(ctx)
}

// EnableCluster enables the cluster without progress reporting.
func (s *ManagedDatabases) EnableCluster(ctx context.Context, input application.ManagedDatabaseEnableInput) error {
	return s.EnableClusterWithProgress(ctx, input, nil)
}

// IsEnabled reports whether managed databases are enabled.
func (s *ManagedDatabases) IsEnabled(ctx context.Context) (bool, error) {
	cluster, err := s.repository.GetManagedDatabaseCluster(ctx)
	if err != nil {
		return false, err
	}
	return cluster.Enabled, nil
}

// EnableClusterWithProgress writes the shared Compose project and starts the
// global Postgres container. The callback is synchronous and may be nil.
func (s *ManagedDatabases) EnableClusterWithProgress(ctx context.Context, input application.ManagedDatabaseEnableInput, progress func(stage, message string)) error {
	reportManagedProgress(progress, "configuration", "Preparing managed database configuration")
	validated, err := application.ValidateManagedDatabaseEnableInput(input)
	if err != nil {
		return err
	}
	lease, err := s.projectLocks.acquire(ctx, managedDatabaseProjectLockKey)
	if err != nil {
		return err
	}
	defer lease.release()

	existing, err := s.repository.GetManagedDatabaseCluster(ctx)
	if err != nil {
		return err
	}
	if existing.Enabled {
		return application.ErrManagedDatabasesAlreadyEnabled
	}
	if err := s.runner.EnsureNetwork(ctx, applicationNetworkName); err != nil {
		return fmt.Errorf("ensure managed databases network: %w", sanitizeManagedError(err))
	}

	password := validated.Password
	if password == "" {
		password, err = generateDatabasePassword()
		if err != nil {
			return fmt.Errorf("generate managed database password: %w", err)
		}
	}

	directory := filepath.Join(s.projectsRoot, coreDir, managedDatabasesDir)
	if err := ensureDirectory(filepath.Join(s.projectsRoot, coreDir)); err != nil {
		return fmt.Errorf("ensure core directory: %w", err)
	}
	if err := ensureDirectory(directory); err != nil {
		return fmt.Errorf("ensure managed databases directory: %w", err)
	}

	reportManagedProgress(progress, "files", "Writing the managed database Compose project")
	composeContents := renderManagedDatabaseCompose(validated.Version)
	varsContents := "POSTGRES_DB=" + formatEnvironmentValue(validated.DefaultUser) + "\n" +
		"POSTGRES_USER=" + formatEnvironmentValue(validated.DefaultUser) + "\n"
	secretsContents := "POSTGRES_PASSWORD=" + formatEnvironmentValue(password) + "\n"
	if err := writeManagedFile(filepath.Join(directory, "compose.yml"), composeContents, 0o644); err != nil {
		return fmt.Errorf("write managed database Compose file: %w", err)
	}
	if err := writeManagedFile(filepath.Join(directory, varsEnvFile), varsContents, envFileMode); err != nil {
		return fmt.Errorf("write managed database variables file: %w", err)
	}
	if err := writeManagedFile(filepath.Join(directory, secretsEnvFile), secretsContents, envFileMode); err != nil {
		return fmt.Errorf("write managed database secrets file: %w", err)
	}
	if err := s.validateStagedCompose(ctx, directory); err != nil {
		return err
	}

	reportManagedProgress(progress, "metadata", "Saving managed database configuration")
	now := time.Now().UTC()
	createdAt := existing.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	if err := s.repository.SaveManagedDatabaseCluster(ctx, application.ManagedDatabaseCluster{
		Enabled:     true,
		Provider:    validated.Provider,
		Version:     validated.Version,
		DefaultUser: validated.DefaultUser,
		CreatedAt:   createdAt,
		UpdatedAt:   now,
	}); err != nil {
		return err
	}
	if _, err := s.repository.GetManagedDatabase(ctx, validated.DefaultUser); err != nil {
		if errors.Is(err, application.ErrManagedDatabaseNotFound) {
			if _, err := s.repository.CreateManagedDatabase(ctx, application.ManagedDatabase{
				Name:      validated.DefaultUser,
				Owner:     validated.DefaultUser,
				CreatedAt: now,
			}); err != nil && !errors.Is(err, application.ErrManagedDatabaseAlreadyExists) {
				return fmt.Errorf("record default managed database: %w", err)
			}
		} else {
			return err
		}
	}
	if _, err := s.repository.GetManagedDatabaseUser(ctx, validated.DefaultUser); err != nil {
		if errors.Is(err, application.ErrManagedDatabaseUserNotFound) {
			if err := s.repository.CreateManagedDatabaseUser(ctx, validated.DefaultUser, []string{validated.DefaultUser}); err != nil && !errors.Is(err, application.ErrManagedDatabaseUserAlreadyExists) {
				return fmt.Errorf("record default managed database user: %w", err)
			}
		} else {
			return err
		}
	}

	reportManagedProgress(progress, "start", "Starting the managed database container")
	if err := s.runner.Up(ctx, directory); err != nil {
		return fmt.Errorf("start managed databases: %w", err)
	}
	return nil
}

// DisableCluster stops the shared container but retains data, files, and
// metadata. Backup files remain operator-managed artifacts.
func (s *ManagedDatabases) DisableCluster(ctx context.Context) error {
	lease, err := s.projectLocks.acquire(ctx, managedDatabaseProjectLockKey)
	if err != nil {
		return err
	}
	defer lease.release()

	cluster, err := s.repository.GetManagedDatabaseCluster(ctx)
	if err != nil {
		return err
	}
	if !cluster.Enabled {
		return application.ErrManagedDatabasesDisabled
	}
	directory, err := s.managedDirectory()
	if err != nil {
		return err
	}
	if err := s.runner.Stop(ctx, directory, managedDatabaseServiceName); err != nil {
		return fmt.Errorf("stop managed databases: %w", sanitizeManagedError(err))
	}
	cluster.Enabled = false
	cluster.UpdatedAt = time.Now().UTC()
	if err := s.repository.SaveManagedDatabaseCluster(ctx, cluster); err != nil {
		return err
	}
	return nil
}

// StartCluster starts the shared container when it is enabled.
func (s *ManagedDatabases) StartCluster(ctx context.Context) error {
	if err := s.requireEnabled(ctx); err != nil {
		return err
	}
	directory, err := s.managedDirectory()
	if err != nil {
		return err
	}
	if err := s.runner.EnsureNetwork(ctx, applicationNetworkName); err != nil {
		return fmt.Errorf("ensure managed databases network: %w", sanitizeManagedError(err))
	}
	// Up also recovers an enabled cluster whose first launch failed before
	// creating its container. Existing environment files and volumes are reused.
	if err := s.runner.Up(ctx, directory); err != nil {
		return fmt.Errorf("start managed databases: %w", sanitizeManagedError(err))
	}
	return nil
}

// StopCluster stops the shared container while keeping it enabled.
func (s *ManagedDatabases) StopCluster(ctx context.Context) error {
	if err := s.requireEnabled(ctx); err != nil {
		return err
	}
	directory, err := s.managedDirectory()
	if err != nil {
		return err
	}
	if err := s.runner.Stop(ctx, directory, managedDatabaseServiceName); err != nil {
		return fmt.Errorf("stop managed databases: %w", sanitizeManagedError(err))
	}
	return nil
}

// RestartCluster restarts the shared container while keeping it enabled.
func (s *ManagedDatabases) RestartCluster(ctx context.Context) error {
	if err := s.requireEnabled(ctx); err != nil {
		return err
	}
	directory, err := s.managedDirectory()
	if err != nil {
		return err
	}
	if err := s.runner.Restart(ctx, directory, managedDatabaseServiceName); err != nil {
		return fmt.Errorf("restart managed databases: %w", sanitizeManagedError(err))
	}
	return nil
}

// GetStatus returns best-effort runtime state for the shared container.
func (s *ManagedDatabases) GetStatus(ctx context.Context) (application.ManagedDatabaseStatus, error) {
	var status application.ManagedDatabaseStatus
	if err := s.requireEnabled(ctx); err != nil {
		return status, err
	}
	directory, err := s.managedDirectory()
	if err != nil {
		return status, err
	}
	running, err := s.runner.IsServiceRunning(ctx, directory, managedDatabaseServiceName)
	if err == nil {
		status.Running = running
	}
	runtime, err := s.runner.ListServices(ctx, directory)
	if err != nil {
		if status.Running {
			status.Status = "running"
		}
		return status, nil
	}
	for _, item := range runtime {
		if item.ServiceName != managedDatabaseServiceName {
			continue
		}
		status.ContainerName = item.ContainerName
		status.Status = item.Status
		status.ImageName = item.Image
		if status.Status == "" && status.Running {
			status.Status = "running"
		}
		break
	}
	return status, nil
}

// GetLogs returns the recent shared container logs.
func (s *ManagedDatabases) GetLogs(ctx context.Context) (string, bool, error) {
	if err := s.requireEnabled(ctx); err != nil {
		return "", false, err
	}
	directory, err := s.managedDirectory()
	if err != nil {
		return "", false, err
	}
	logs, err := s.runner.Logs(ctx, directory, managedDatabaseServiceName, application.ServiceLogLineLimit)
	if err != nil {
		return "", false, nil
	}
	return logs, true, nil
}

// OpenLogs opens a bounded log stream for downloads.
func (s *ManagedDatabases) OpenLogs(ctx context.Context) (io.ReadCloser, error) {
	if err := s.requireEnabled(ctx); err != nil {
		return nil, err
	}
	directory, err := s.managedDirectory()
	if err != nil {
		return nil, err
	}
	return s.runner.OpenLogs(ctx, directory, managedDatabaseServiceName)
}

// ListDatabases returns logical databases in name order.
func (s *ManagedDatabases) ListDatabases(ctx context.Context) ([]application.ManagedDatabase, error) {
	if err := s.requireEnabled(ctx); err != nil {
		return nil, err
	}
	return s.repository.ListManagedDatabases(ctx)
}

// CreateDatabase creates a database and creates its owner when the role is absent.
func (s *ManagedDatabases) CreateDatabase(ctx context.Context, input application.ManagedDatabaseCreateInput) (application.ManagedDatabaseCreation, error) {
	var result application.ManagedDatabaseCreation
	if err := s.requireEnabled(ctx); err != nil {
		return result, err
	}
	name, err := application.ValidateManagedDatabaseName(input.Name)
	if err != nil {
		return result, err
	}
	owner := strings.TrimSpace(input.Owner)
	if owner == "" {
		owner = name
	}
	owner, err = application.ValidateManagedDatabaseUsername(owner)
	if err != nil {
		return result, err
	}
	lease, err := s.projectLocks.acquire(ctx, managedDatabaseProjectLockKey)
	if err != nil {
		return result, err
	}
	defer lease.release()
	if _, err := s.repository.GetManagedDatabase(ctx, name); err == nil {
		return result, application.ErrManagedDatabaseAlreadyExists
	} else if !errors.Is(err, application.ErrManagedDatabaseNotFound) {
		return result, err
	}

	directory, err := s.managedDirectory()
	if err != nil {
		return result, err
	}
	running, err := s.runner.IsServiceRunning(ctx, directory, managedDatabaseServiceName)
	if err != nil {
		return result, fmt.Errorf("inspect managed database state: %w", err)
	}
	if !running {
		return result, application.ErrDatabaseServiceNotRunning
	}
	// Consult Postgres rather than metadata: a role may have been created externally.
	exists, err := s.execSQL(ctx, directory, "SELECT 1 FROM pg_roles WHERE rolname = "+postgresQuoteLiteral(owner))
	if err != nil {
		return result, fmt.Errorf("inspect managed database user: %w", sanitizeManagedError(err))
	}
	password := ""
	if strings.TrimSpace(exists) != "1" {
		password, err = generateDatabasePassword()
		if err != nil {
			return result, fmt.Errorf("generate database user password: %w", err)
		}
		// Snapshot PUBLIC access for existing roles before removing it. New roles
		// then need explicit database access, while existing roles retain theirs.
		statement := "BEGIN; " + restrictManagedDatabasePublicAccess + " CREATE ROLE " + postgresQuoteIdentifier(owner) +
			" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD " + postgresQuoteLiteral(password) + "; COMMIT;"
		if _, err := s.execSQL(ctx, directory, statement); err != nil {
			if !isManagedAlreadyExists(err) {
				return result, fmt.Errorf("create managed database user: %w", sanitizePostgresError(err, password))
			}
			// An externally created role won the race. Never rotate its password.
			password = ""
		}
	}
	createdDatabase := false
	complete := false
	defer func() {
		if complete {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if createdDatabase {
			_, _ = s.execSQL(cleanupCtx, directory, "DROP DATABASE "+postgresQuoteIdentifier(name))
		}
		if password != "" {
			_, _ = s.execSQL(cleanupCtx, directory, "DROP ROLE "+postgresQuoteIdentifier(owner))
		}
	}()
	if _, err := s.execSQL(ctx, directory, "CREATE DATABASE "+postgresQuoteIdentifier(name)+" OWNER "+postgresQuoteIdentifier(owner)); err != nil {
		if isManagedAlreadyExists(err) {
			return result, application.ErrManagedDatabaseAlreadyExists
		}
		return result, fmt.Errorf("create managed database: %w", sanitizeManagedError(err))
	}
	createdDatabase = true
	// Future databases must not reopen PUBLIC access for previously created users.
	if _, err := s.execSQL(ctx, directory, "REVOKE CONNECT, TEMPORARY ON DATABASE "+postgresQuoteIdentifier(name)+" FROM PUBLIC"); err != nil {
		return result, fmt.Errorf("restrict managed database access: %w", sanitizeManagedError(err))
	}
	newUsername := ""
	if password != "" {
		newUsername = owner
	}
	created, err := s.repository.CreateManagedDatabaseWithUser(ctx, application.ManagedDatabase{
		Name: name, Owner: owner, CreatedAt: time.Now().UTC(),
	}, newUsername)
	if err != nil {
		return result, err
	}
	complete = true
	result.ManagedDatabase = created
	if password != "" {
		result.Credentials = &application.ManagedDatabaseCredentials{Username: owner, Password: password}
	}
	return result, nil
}

// PostgreSQL privileges are additive: revoking a privilege from one role cannot
// override PUBLIC. Preserve the legacy grants for existing roles, then remove
// PUBLIC CONNECT/TEMPORARY on every connectable database (including template1).
// This runs in the same transaction as role creation.
const restrictManagedDatabasePublicAccess = `DO $redlaunch$
DECLARE db record; privilege record; existing_role record;
BEGIN
  FOR db IN SELECT datname, datacl, datdba FROM pg_database WHERE datallowconn LOOP
    FOR privilege IN SELECT privilege_type FROM aclexplode(COALESCE(db.datacl, acldefault('d', db.datdba)))
      WHERE grantee = 0 AND privilege_type IN ('CONNECT', 'TEMPORARY') LOOP
      FOR existing_role IN SELECT rolname FROM pg_roles WHERE NOT rolsuper LOOP
        EXECUTE format('GRANT %s ON DATABASE %I TO %I', privilege.privilege_type, db.datname, existing_role.rolname);
      END LOOP;
    END LOOP;
    EXECUTE format('REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC', db.datname);
  END LOOP;
END;
$redlaunch$;`

// DropDatabase removes a logical database.
func (s *ManagedDatabases) DropDatabase(ctx context.Context, name string) error {
	if err := s.requireEnabled(ctx); err != nil {
		return err
	}
	validated, err := application.ValidateManagedDatabaseName(name)
	if err != nil {
		return err
	}
	name = validated
	lease, err := s.projectLocks.acquire(ctx, managedDatabaseProjectLockKey)
	if err != nil {
		return err
	}
	defer lease.release()

	item, err := s.repository.GetManagedDatabase(ctx, name)
	if err != nil {
		return err
	}
	directory, err := s.managedDirectory()
	if err != nil {
		return err
	}
	// Refuse to drop while a backup operation holds the lease.
	release, err := s.acquireDatabaseLease(ctx, item.ID, "drop")
	if err != nil {
		return err
	}
	defer release()
	if _, err := s.execSQL(ctx, directory, "DROP DATABASE "+postgresQuoteIdentifier(name)); err != nil {
		if isManagedNotExists(err) {
			// The live database is already gone; still remove metadata.
		} else {
			return fmt.Errorf("drop managed database: %w", sanitizeManagedError(err))
		}
	}
	if err := s.repository.DeleteManagedDatabase(ctx, name); err != nil {
		return err
	}
	return nil
}

// ListUsers returns database users with their allowed databases.
func (s *ManagedDatabases) ListUsers(ctx context.Context) ([]application.ManagedDatabaseUserDetail, error) {
	if err := s.requireEnabled(ctx); err != nil {
		return nil, err
	}
	return s.repository.ListManagedDatabaseUsers(ctx)
}

// GetDatabaseConnections lists connection templates for managed logins with
// access through ownership, explicit grants, or the default administrator role.
func (s *ManagedDatabases) GetDatabaseConnections(ctx context.Context, name string) ([]application.ManagedDatabaseConnection, error) {
	cluster, err := s.GetCluster(ctx)
	if err != nil {
		return nil, err
	}
	if !cluster.Enabled {
		return nil, application.ErrManagedDatabasesDisabled
	}
	database, err := s.repository.GetManagedDatabase(ctx, name)
	if err != nil {
		return nil, err
	}
	users, err := s.repository.ListManagedDatabaseUsers(ctx)
	if err != nil {
		return nil, err
	}
	var connections []application.ManagedDatabaseConnection
	for _, user := range users {
		username := user.User.Username
		if username != cluster.DefaultUser && username != database.Owner && !slices.Contains(user.Databases, database.Name) {
			continue
		}
		connection := url.URL{
			Scheme: "postgresql",
			User:   url.UserPassword(username, "PASSWORD"),
			Host:   managedDatabaseContainerName + ":5432",
			Path:   "/" + database.Name,
		}
		connections = append(connections, application.ManagedDatabaseConnection{Username: username, URI: connection.String()})
	}
	return connections, nil
}

// CreateUser creates a login role and grants database access.
func (s *ManagedDatabases) CreateUser(ctx context.Context, input application.ManagedDatabaseUserInput) error {
	if err := s.requireEnabled(ctx); err != nil {
		return err
	}
	validated, err := application.ValidateManagedDatabaseUserInput(input)
	if err != nil {
		return err
	}
	lease, err := s.projectLocks.acquire(ctx, managedDatabaseProjectLockKey)
	if err != nil {
		return err
	}
	defer lease.release()

	directory, err := s.managedDirectory()
	if err != nil {
		return err
	}
	running, err := s.runner.IsServiceRunning(ctx, directory, managedDatabaseServiceName)
	if err != nil {
		return fmt.Errorf("inspect managed database state: %w", err)
	}
	if !running {
		return application.ErrDatabaseServiceNotRunning
	}
	if _, err := s.execSQL(ctx, directory, "CREATE ROLE "+postgresQuoteIdentifier(validated.Username)+" LOGIN PASSWORD "+postgresQuoteLiteral(validated.Password)); err != nil {
		if isManagedAlreadyExists(err) {
			return application.ErrManagedDatabaseUserAlreadyExists
		}
		return fmt.Errorf("create managed database user: %w", sanitizeManagedError(err))
	}
	for _, database := range validated.Databases {
		if _, err := s.execSQL(ctx, directory, "GRANT ALL PRIVILEGES ON DATABASE "+postgresQuoteIdentifier(database)+" TO "+postgresQuoteIdentifier(validated.Username)); err != nil {
			_, _ = s.execSQL(ctx, directory, "DROP ROLE "+postgresQuoteIdentifier(validated.Username))
			return fmt.Errorf("grant managed database access: %w", sanitizeManagedError(err))
		}
	}
	if err := s.repository.CreateManagedDatabaseUser(ctx, validated.Username, validated.Databases); err != nil {
		_, _ = s.execSQL(ctx, directory, "DROP ROLE "+postgresQuoteIdentifier(validated.Username))
		return err
	}
	return nil
}

// UpdateUserPassword changes a login role password.
func (s *ManagedDatabases) UpdateUserPassword(ctx context.Context, username, password string) error {
	if err := s.requireEnabled(ctx); err != nil {
		return err
	}
	username, err := application.ValidateManagedDatabaseUsername(username)
	if err != nil {
		return err
	}
	password, err = application.ValidateDatabasePassword(password)
	if err != nil {
		return err
	}
	if strings.TrimSpace(password) == "" {
		return application.ErrDatabasePasswordInvalid
	}
	if _, err := s.repository.GetManagedDatabaseUser(ctx, username); err != nil {
		return err
	}
	directory, err := s.managedDirectory()
	if err != nil {
		return err
	}
	if _, err := s.execSQL(ctx, directory, "ALTER ROLE "+postgresQuoteIdentifier(username)+" WITH PASSWORD "+postgresQuoteLiteral(password)); err != nil {
		return fmt.Errorf("update managed database password: %w", sanitizeManagedError(err))
	}
	return nil
}

// SetUserPermissions replaces which databases a user may access.
func (s *ManagedDatabases) SetUserPermissions(ctx context.Context, username string, databases []string) error {
	if err := s.requireEnabled(ctx); err != nil {
		return err
	}
	username, err := application.ValidateManagedDatabaseUsername(username)
	if err != nil {
		return err
	}
	grants, err := application.ValidateManagedDatabaseGrants(databases)
	if err != nil {
		return err
	}
	current, err := s.repository.GetManagedDatabaseUser(ctx, username)
	if err != nil {
		return err
	}
	lease, err := s.projectLocks.acquire(ctx, managedDatabaseProjectLockKey)
	if err != nil {
		return err
	}
	defer lease.release()

	directory, err := s.managedDirectory()
	if err != nil {
		return err
	}
	previous := make(map[string]struct{}, len(current.Databases))
	for _, database := range current.Databases {
		previous[database] = struct{}{}
	}
	wanted := make(map[string]struct{}, len(grants))
	for _, database := range grants {
		wanted[database] = struct{}{}
	}
	for database := range wanted {
		if _, ok := previous[database]; ok {
			continue
		}
		if _, err := s.execSQL(ctx, directory, "GRANT ALL PRIVILEGES ON DATABASE "+postgresQuoteIdentifier(database)+" TO "+postgresQuoteIdentifier(username)); err != nil {
			return fmt.Errorf("grant managed database access: %w", sanitizeManagedError(err))
		}
	}
	for database := range previous {
		if _, ok := wanted[database]; ok {
			continue
		}
		if _, err := s.execSQL(ctx, directory, "REVOKE ALL PRIVILEGES ON DATABASE "+postgresQuoteIdentifier(database)+" FROM "+postgresQuoteIdentifier(username)); err != nil {
			return fmt.Errorf("revoke managed database access: %w", sanitizeManagedError(err))
		}
	}
	if err := s.repository.SetManagedDatabaseGrants(ctx, username, grants); err != nil {
		return err
	}
	return nil
}

// DeleteUser removes a login role.
func (s *ManagedDatabases) DeleteUser(ctx context.Context, username string) error {
	if err := s.requireEnabled(ctx); err != nil {
		return err
	}
	username, err := application.ValidateManagedDatabaseUsername(username)
	if err != nil {
		return err
	}
	cluster, err := s.repository.GetManagedDatabaseCluster(ctx)
	if err != nil {
		return err
	}
	if username == cluster.DefaultUser {
		return application.ErrManagedDatabaseDefaultUserInUse
	}
	if _, err := s.repository.GetManagedDatabaseUser(ctx, username); err != nil {
		return err
	}
	lease, err := s.projectLocks.acquire(ctx, managedDatabaseProjectLockKey)
	if err != nil {
		return err
	}
	defer lease.release()

	directory, err := s.managedDirectory()
	if err != nil {
		return err
	}
	if _, err := s.execSQL(ctx, directory, "DROP ROLE "+postgresQuoteIdentifier(username)); err != nil {
		if isManagedNotExists(err) {
			// Role already gone live; still remove metadata.
		} else {
			return fmt.Errorf("delete managed database user: %w", sanitizeManagedError(err))
		}
	}
	if err := s.repository.DeleteManagedDatabaseUser(ctx, username); err != nil {
		return err
	}
	return nil
}

func (s *ManagedDatabases) requireEnabled(ctx context.Context) error {
	cluster, err := s.repository.GetManagedDatabaseCluster(ctx)
	if err != nil {
		return err
	}
	if !cluster.Enabled {
		return application.ErrManagedDatabasesDisabled
	}
	return nil
}

func (s *ManagedDatabases) managedDirectory() (string, error) {
	directory := filepath.Join(s.projectsRoot, coreDir, managedDatabasesDir)
	coreRoot := filepath.Join(s.projectsRoot, coreDir)
	relative, err := filepath.Rel(coreRoot, directory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("managed databases directory is outside the managed core directory")
	}
	if err := checkManagedAncestors(coreRoot, directory); err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", application.ErrManagedDatabasesDisabled
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("managed databases directory is not a directory")
	}
	return directory, nil
}

func (s *ManagedDatabases) validateStagedCompose(ctx context.Context, directory string) error {
	reader, ok := any(s.runner).(interface {
		ConfigServices(context.Context, string) ([]compose.ConfiguredService, error)
	})
	if !ok {
		return nil
	}
	if _, err := reader.ConfigServices(ctx, directory); err != nil {
		return fmt.Errorf("validate staged Compose project: %w", err)
	}
	return nil
}

func (s *ManagedDatabases) execSQL(ctx context.Context, directory, statement string) (string, error) {
	output, err := s.runner.ExecPostgresSQL(ctx, directory, managedDatabaseServiceName, statement)
	if err != nil {
		return "", err
	}
	return output, nil
}

func renderManagedDatabaseCompose(version string) string {
	return `services:
  ` + managedDatabaseServiceName + `:
    image: postgres:` + version + `
    container_name: ` + managedDatabaseContainerName + `
    restart: unless-stopped
    env_file:
      - vars.env
      - secrets.env
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U \"$$POSTGRES_USER\" -d \"$$POSTGRES_DB\""]
      interval: 10s
      timeout: 5s
      retries: 5
      start_period: 30s
    volumes:
      - ` + managedDatabaseVolumeKey + `:/var/lib/postgresql/data
    networks:
      - redlaunch-common
    labels:
      - "redlaunch.managed=true"

networks:
  redlaunch-common:
    external: true
    name: redlaunch-common

volumes:
  ` + managedDatabaseVolumeKey + `:
    name: ` + managedDatabaseVolumeName + `
`
}

func reportManagedProgress(progress func(stage, message string), stage, message string) {
	if progress != nil {
		progress(stage, message)
	}
}

func sanitizeManagedError(err error) error {
	if err == nil {
		return nil
	}
	return sanitizePostgresError(err)
}

func isManagedAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	lowered := strings.ToLower(err.Error())
	return strings.Contains(lowered, "already exists")
}

func isManagedNotExists(err error) bool {
	if err == nil {
		return false
	}
	lowered := strings.ToLower(err.Error())
	return strings.Contains(lowered, "does not exist") || strings.Contains(lowered, "not exist")
}

func (s *ManagedDatabases) acquireDatabaseLease(ctx context.Context, databaseID int64, operation string) (func(), error) {
	token, err := newBackupLeaseToken()
	if err != nil {
		return nil, fmt.Errorf("create managed backup lease token: %w", err)
	}
	now := s.clock().UTC()
	if err := s.repository.AcquireManagedBackupLease(ctx, databaseID, operation, token, now, now.Add(backupLeaseDuration)); err != nil {
		return nil, err
	}
	return func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.repository.ReleaseManagedBackupLease(cleanupContext, databaseID, token)
	}, nil
}

// GetBackupDetails returns the schedule and recorded files for one database.
func (s *ManagedDatabases) GetBackupDetails(ctx context.Context, databaseName string) (application.BackupDetails, error) {
	item, err := s.findManagedDatabase(ctx, databaseName)
	if err != nil {
		return application.BackupDetails{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	schedule, err := s.scheduleForDatabase(ctx, item.ID, s.backupLocation(item))
	if err != nil {
		return application.BackupDetails{}, err
	}
	backups, err := s.repository.ListManagedBackups(ctx, item.ID)
	if err != nil {
		return application.BackupDetails{}, fmt.Errorf("list managed backups: %w", err)
	}
	backups = validBackupsForService(backups, item.ID)
	return application.BackupDetails{Schedule: schedule, Backups: backups}, nil
}

// UpdateBackupSchedule saves a schedule and syncs its systemd timer.
func (s *ManagedDatabases) UpdateBackupSchedule(ctx context.Context, databaseName string, input application.BackupScheduleInput) error {
	item, err := s.findManagedDatabase(ctx, databaseName)
	if err != nil {
		return err
	}
	release, err := s.acquireDatabaseLease(ctx, item.ID, "schedule")
	if err != nil {
		return err
	}
	defer release()

	s.mu.Lock()
	defer s.mu.Unlock()

	location := s.backupLocation(item)
	current, err := s.scheduleForDatabase(ctx, item.ID, location)
	if err != nil {
		return err
	}
	if !input.Enabled {
		if current.Enabled {
			if err := s.scheduler.Disable(ctx, managedBackupServiceUnitName(item.ID), managedBackupTimerUnitName(item.ID)); err != nil {
				return mapBackupSchedulerError("disable managed scheduled backups", err)
			}
		}
		current.Enabled = false
		current.BackupLocation = location
		return s.repository.SaveManagedBackupSchedule(ctx, current)
	}
	validated, err := application.ValidateBackupScheduleInput(input)
	if err != nil {
		return err
	}
	candidate := current
	candidate.ServiceID = item.ID
	candidate.Enabled = true
	candidate.ScheduleType = validated.ScheduleType
	candidate.Hour = validated.Hour
	candidate.Minute = validated.Minute
	candidate.Weekday = validated.Weekday
	candidate.RetentionDays = validated.RetentionDays
	candidate.BackupLocation = location
	serviceUnitName := managedBackupServiceUnitName(item.ID)
	timerUnitName := managedBackupTimerUnitName(item.ID)
	serviceContents, timerContents := s.renderManagedUnits(item.ID, candidate)
	if err := s.scheduler.Install(ctx, serviceUnitName, serviceContents, timerUnitName, timerContents); err != nil {
		return mapBackupSchedulerError("enable managed scheduled backups", err)
	}
	if err := s.repository.SaveManagedBackupSchedule(ctx, candidate); err != nil {
		_ = s.scheduler.Disable(ctx, serviceUnitName, timerUnitName)
		return fmt.Errorf("save managed backup schedule: %w", err)
	}
	return nil
}

// DisableDatabaseBackupSchedule stops one database timer for deletion flows.
func (s *ManagedDatabases) DisableDatabaseBackupSchedule(ctx context.Context, databaseID int64) error {
	if databaseID < 1 {
		return application.ErrManagedDatabaseNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	schedule, err := s.repository.GetManagedBackupSchedule(ctx, databaseID)
	if errors.Is(err, application.ErrBackupScheduleNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read managed backup schedule for cleanup: %w", err)
	}
	if !schedule.Enabled {
		return nil
	}
	if err := s.scheduler.Disable(ctx, managedBackupServiceUnitName(databaseID), managedBackupTimerUnitName(databaseID)); err != nil {
		return mapBackupSchedulerError("disable managed scheduled backups", err)
	}
	schedule.Enabled = false
	if err := s.repository.SaveManagedBackupSchedule(ctx, schedule); err != nil {
		return fmt.Errorf("save disabled managed backup schedule: %w", err)
	}
	return nil
}

// RunBackupNow creates a backup immediately.
func (s *ManagedDatabases) RunBackupNow(ctx context.Context, databaseName string) (application.Backup, error) {
	ctx, cancel := withBackupOperationDeadline(ctx)
	defer cancel()
	item, err := s.findManagedDatabase(ctx, databaseName)
	if err != nil {
		return application.Backup{}, err
	}
	release, err := s.acquireDatabaseLease(ctx, item.ID, "backup")
	if err != nil {
		return application.Backup{}, err
	}
	defer release()
	if err := s.ensureClusterRunning(ctx); err != nil {
		return application.Backup{}, err
	}
	return s.runBackup(ctx, item, false)
}

// RunScheduledBackup runs one backup for a systemd unit.
func (s *ManagedDatabases) RunScheduledBackup(ctx context.Context, databaseID int64) (application.Backup, error) {
	ctx, cancel := withBackupOperationDeadline(ctx)
	defer cancel()
	item, err := s.findManagedDatabaseByID(ctx, databaseID)
	if err != nil {
		return application.Backup{}, err
	}
	release, err := s.acquireDatabaseLease(ctx, item.ID, "backup")
	if err != nil {
		return application.Backup{}, err
	}
	defer release()
	return s.runBackup(ctx, item, true)
}

// RestoreBackup restores a recorded backup file into its database.
func (s *ManagedDatabases) RestoreBackup(ctx context.Context, databaseName, fileName string) error {
	ctx, cancel := withBackupOperationDeadline(ctx)
	defer cancel()
	item, err := s.findManagedDatabase(ctx, databaseName)
	if err != nil {
		return err
	}
	fileName, err = application.ValidateBackupFileName(fileName)
	if err != nil {
		return err
	}
	release, err := s.acquireDatabaseLease(ctx, item.ID, "restore")
	if err != nil {
		return err
	}
	defer release()

	backups, err := s.repository.ListManagedBackups(ctx, item.ID)
	if err != nil {
		return fmt.Errorf("list managed backups for restore: %w", err)
	}
	var selected application.Backup
	for _, backup := range backups {
		if backup.ServiceID == item.ID && backup.FileName == fileName {
			selected = backup
			break
		}
	}
	if selected.FileName == "" {
		return application.ErrBackupNotFound
	}
	location := s.backupLocation(item)
	if _, err := inspectBackupDirectory(s.backupRoot, "managed-databases", item.Name); err != nil {
		return fmt.Errorf("inspect managed backup location: %w", err)
	}
	path, err := safeBackupPath(location, selected.FileName)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect managed backup file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("backup file must be a regular file")
	}
	if err := validatePlainSQLDump(path); err != nil {
		return err
	}
	directory, err := s.managedDirectory()
	if err != nil {
		return fmt.Errorf("resolve managed databases directory for restore: %w", err)
	}
	// Restore targets the logical database by restoring into the shared
	// cluster service; the dump itself selects the database contents.
	if err := s.runner.RestorePostgreSQL(ctx, directory, managedDatabaseServiceName, path); err != nil {
		return fmt.Errorf("restore managed backup: %w", err)
	}
	return nil
}

// DeleteBackup removes a recorded backup file and its record.
func (s *ManagedDatabases) DeleteBackup(ctx context.Context, databaseName, fileName string) error {
	ctx, cancel := withBackupOperationDeadline(ctx)
	defer cancel()
	item, err := s.findManagedDatabase(ctx, databaseName)
	if err != nil {
		return err
	}
	fileName, err = application.ValidateBackupFileName(fileName)
	if err != nil {
		return err
	}
	release, err := s.acquireDatabaseLease(ctx, item.ID, "delete")
	if err != nil {
		return err
	}
	defer release()

	backups, err := s.repository.ListManagedBackups(ctx, item.ID)
	if err != nil {
		return fmt.Errorf("list managed backups for delete: %w", err)
	}
	var found bool
	for _, backup := range backups {
		if backup.ServiceID == item.ID && backup.FileName == fileName {
			found = true
			break
		}
	}
	if !found {
		return application.ErrBackupNotFound
	}
	directory, err := inspectBackupDirectory(s.backupRoot, "managed-databases", item.Name)
	if errors.Is(err, os.ErrNotExist) {
		return s.deleteBackupRecord(ctx, item.ID, fileName)
	}
	if err != nil {
		return fmt.Errorf("inspect managed backup location: %w", err)
	}
	path, err := safeBackupPath(directory, fileName)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return s.deleteBackupRecord(ctx, item.ID, fileName)
	}
	if err != nil {
		return fmt.Errorf("inspect managed backup file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("backup file must be a regular file")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete managed backup file: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync managed backup directory after delete: %w", err)
	}
	return s.deleteBackupRecord(ctx, item.ID, fileName)
}

// OpenBackup opens a recorded backup file for streaming.
func (s *ManagedDatabases) OpenBackup(ctx context.Context, databaseName, fileName string) (io.ReadCloser, application.Backup, error) {
	item, err := s.findManagedDatabase(ctx, databaseName)
	if err != nil {
		return nil, application.Backup{}, err
	}
	fileName, err = application.ValidateBackupFileName(fileName)
	if err != nil {
		return nil, application.Backup{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	backups, err := s.repository.ListManagedBackups(ctx, item.ID)
	if err != nil {
		return nil, application.Backup{}, fmt.Errorf("list managed backups for download: %w", err)
	}
	var selected application.Backup
	for _, backup := range backups {
		if backup.ServiceID == item.ID && backup.FileName == fileName {
			selected = backup
			break
		}
	}
	if selected.FileName == "" {
		return nil, application.Backup{}, application.ErrBackupNotFound
	}
	directory, err := inspectBackupDirectory(s.backupRoot, "managed-databases", item.Name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, application.Backup{}, application.ErrBackupNotFound
	}
	if err != nil {
		return nil, application.Backup{}, fmt.Errorf("inspect managed backup location: %w", err)
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
		return nil, application.Backup{}, fmt.Errorf("inspect managed backup file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, application.Backup{}, errors.New("backup file must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, application.Backup{}, fmt.Errorf("open managed backup file: %w", err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, application.Backup{}, fmt.Errorf("inspect opened managed backup file: %w", err)
	}
	if !fileInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, application.Backup{}, errors.New("backup file must be a regular file")
	}
	selected.SizeBytes = fileInfo.Size()
	return file, selected, nil
}

func (s *ManagedDatabases) deleteBackupRecord(ctx context.Context, databaseID int64, fileName string) error {
	if err := s.repository.DeleteManagedBackup(ctx, databaseID, fileName); err != nil {
		return fmt.Errorf("delete managed backup record: %w", err)
	}
	return nil
}

func (s *ManagedDatabases) ensureClusterRunning(ctx context.Context) error {
	directory, err := s.managedDirectory()
	if err != nil {
		return fmt.Errorf("resolve managed databases directory for backup status: %w", err)
	}
	running, err := s.runner.IsServiceRunning(ctx, directory, managedDatabaseServiceName)
	if err != nil {
		return fmt.Errorf("check managed database status: %w", err)
	}
	if !running {
		return application.ErrBackupServiceNotRunning
	}
	return nil
}

func (s *ManagedDatabases) runBackup(ctx context.Context, item application.ManagedDatabase, requireEnabled bool) (application.Backup, error) {
	location := s.backupLocation(item)
	schedule, err := s.scheduleForDatabase(ctx, item.ID, location)
	if err != nil {
		return application.Backup{}, err
	}
	if requireEnabled && !schedule.Enabled {
		return application.Backup{}, application.ErrBackupScheduleDisabled
	}
	if err := ensureBackupDirectory(s.backupRoot, "managed-databases", item.Name); err != nil {
		return application.Backup{}, fmt.Errorf("prepare managed backup location: %w", err)
	}
	now := s.clock().UTC()
	if err := recoverAbandonedTemporaryBackups(location, now); err != nil {
		return application.Backup{}, fmt.Errorf("recover temporary managed backup files: %w", err)
	}
	temporary, err := os.CreateTemp(location, ".redlaunch-backup-*.sql")
	if err != nil {
		return application.Backup{}, fmt.Errorf("create temporary managed backup file: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return application.Backup{}, fmt.Errorf("prepare temporary managed backup file: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	directory, err := s.managedDirectory()
	if err != nil {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, fmt.Errorf("resolve managed databases directory for backup: %w", err)
	}
	if err := s.runner.BackupPostgreSQL(ctx, directory, managedDatabaseServiceName, temporaryPath); err != nil {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, fmt.Errorf("create managed backup: %w", err)
	}
	info, err := os.Lstat(temporaryPath)
	if err != nil {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, fmt.Errorf("inspect completed managed backup: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, errors.New("completed backup is not a regular file")
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, fmt.Errorf("protect completed managed backup: %w", err)
	}
	if err := syncRegularFile(temporaryPath); err != nil {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, fmt.Errorf("sync completed managed backup: %w", err)
	}
	fileName, path, err := commitBackupFile(temporaryPath, location, now)
	if err != nil {
		s.recordBackupFailure(ctx, schedule, location, now)
		return application.Backup{}, fmt.Errorf("save completed managed backup: %w", err)
	}
	removeTemporary = false
	backup, err := s.repository.CreateManagedBackup(ctx, application.Backup{
		ServiceID: item.ID,
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
			err = errors.Join(err, fmt.Errorf("remove unrecorded managed backup file: %w", removeErr))
		}
		return application.Backup{}, fmt.Errorf("record completed managed backup: %w", err)
	}
	schedule.ServiceID = item.ID
	schedule.BackupLocation = location
	if err := s.updateBackupStatus(ctx, schedule, now, "successful", backup.SizeBytes); err != nil {
		return application.Backup{}, fmt.Errorf("save managed backup status: %w", err)
	}
	if err := s.applyRetention(ctx, item.ID, location, schedule.RetentionDays, now); err != nil {
		return backup, fmt.Errorf("apply managed backup retention: %w", err)
	}
	return backup, nil
}

func (s *ManagedDatabases) applyRetention(ctx context.Context, databaseID int64, location string, retentionDays int, now time.Time) error {
	if retentionDays < 1 {
		return nil
	}
	backups, err := s.repository.ListManagedBackups(ctx, databaseID)
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
			// Stale record; remove it.
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
				return fmt.Errorf("sync managed backup directory after retention cleanup: %w", err)
			}
		}
		if err := s.repository.DeleteManagedBackup(ctx, databaseID, fileName); err != nil && !errors.Is(err, application.ErrBackupNotFound) {
			return err
		}
	}
	return nil
}

func (s *ManagedDatabases) recordBackupFailure(ctx context.Context, schedule application.BackupSchedule, location string, at time.Time) {
	schedule.BackupLocation = location
	recoveryContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.updateBackupStatus(recoveryContext, schedule, at, "failed", 0)
}

func (s *ManagedDatabases) updateBackupStatus(ctx context.Context, schedule application.BackupSchedule, at time.Time, status string, sizeBytes int64) error {
	if _, err := s.repository.GetManagedBackupSchedule(ctx, schedule.ServiceID); errors.Is(err, application.ErrBackupScheduleNotFound) {
		if err := s.repository.SaveManagedBackupSchedule(ctx, application.BackupSchedule{
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
	return s.repository.UpdateManagedBackupStatus(ctx, schedule.ServiceID, at, status, sizeBytes)
}

func (s *ManagedDatabases) scheduleForDatabase(ctx context.Context, databaseID int64, location string) (application.BackupSchedule, error) {
	schedule, err := s.repository.GetManagedBackupSchedule(ctx, databaseID)
	if errors.Is(err, application.ErrBackupScheduleNotFound) {
		return application.BackupSchedule{
			ServiceID:      databaseID,
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
	schedule.ServiceID = databaseID
	schedule.BackupLocation = location
	if schedule.ScheduleType == "" {
		schedule.ScheduleType = application.BackupScheduleDaily
	}
	if schedule.RetentionDays == 0 {
		schedule.RetentionDays = application.BackupDefaultRetention
	}
	return schedule, nil
}

func (s *ManagedDatabases) findManagedDatabase(ctx context.Context, name string) (application.ManagedDatabase, error) {
	if err := s.requireEnabled(ctx); err != nil {
		return application.ManagedDatabase{}, err
	}
	name, err := application.ValidateManagedDatabaseName(name)
	if err != nil {
		return application.ManagedDatabase{}, err
	}
	return s.repository.GetManagedDatabase(ctx, name)
}

func (s *ManagedDatabases) findManagedDatabaseByID(ctx context.Context, databaseID int64) (application.ManagedDatabase, error) {
	if err := s.requireEnabled(ctx); err != nil {
		return application.ManagedDatabase{}, application.ErrManagedDatabaseNotFound
	}
	if databaseID < 1 {
		return application.ManagedDatabase{}, application.ErrManagedDatabaseNotFound
	}
	databases, err := s.repository.ListManagedDatabases(ctx)
	if err != nil {
		return application.ManagedDatabase{}, err
	}
	for _, item := range databases {
		if item.ID == databaseID {
			return item, nil
		}
	}
	return application.ManagedDatabase{}, application.ErrManagedDatabaseNotFound
}

func (s *ManagedDatabases) backupLocation(item application.ManagedDatabase) string {
	return filepath.Join(s.backupRoot, "managed-databases", item.Name)
}

func (s *ManagedDatabases) renderManagedUnits(databaseID int64, schedule application.BackupSchedule) (string, string) {
	serviceUnitName := managedBackupServiceUnitName(databaseID)
	serviceArgs := append([]string(nil), s.executablePrefix...)
	serviceArgs = append(serviceArgs, s.executable, "managed-backup-run", "--database-id", strconv.FormatInt(databaseID, 10))
	if s.databasePath != "" {
		serviceArgs = append(serviceArgs, "--db-path", s.databasePath)
	}
	serviceArgs = append(serviceArgs, "--projects-root", s.projectsRoot, "--backup-root", s.backupRoot)
	execStart := make([]string, 0, len(serviceArgs))
	for _, arg := range serviceArgs {
		execStart = append(execStart, quoteSystemdArgument(arg))
	}
	serviceContents := "[Unit]\n" +
		"Description=Redlaunch managed database backup service\n" +
		"After=docker.service\n\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"TimeoutStartSec=" + backupSystemdTimeout + "\n" +
		"ExecStart=" + strings.Join(execStart, " ") + "\n" +
		"PrivateTmp=true\n" +
		"NoNewPrivileges=true\n"
	timerContents := "[Unit]\n" +
		"Description=Redlaunch managed database backup timer\n\n" +
		"[Timer]\n" +
		"OnCalendar=" + backupCalendarExpression(schedule) + "\n" +
		"Persistent=true\n" +
		"Unit=" + serviceUnitName + "\n\n" +
		"[Install]\n" +
		"WantedBy=timers.target\n"
	return serviceContents, timerContents
}

func managedBackupServiceUnitName(databaseID int64) string {
	return "redlaunch-backup-managed-" + strconv.FormatInt(databaseID, 10) + ".service"
}

func managedBackupTimerUnitName(databaseID int64) string {
	return "redlaunch-backup-managed-" + strconv.FormatInt(databaseID, 10) + ".timer"
}
