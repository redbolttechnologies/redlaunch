package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

// managedSQL runs administrative SQL inside one engine's container.
type managedSQL func(ctx context.Context, statement string) (string, error)

// managedEngineDriver holds everything that differs between database engines:
// administrative SQL, error classification, connection strings and the
// backup/restore tools. Orchestration, metadata and locking stay in
// ManagedDatabases. Drivers return the application sentinels for the
// "already exists" cases and sanitize runner diagnostics themselves.
type managedEngineDriver interface {
	userExists(ctx context.Context, run managedSQL, username string) (bool, error)
	// createOwner creates the login that will own a new database.
	createOwner(ctx context.Context, run managedSQL, username, password string) error
	dropUser(ctx context.Context, run managedSQL, username string) error
	createUser(ctx context.Context, run managedSQL, username, password string) error
	setPassword(ctx context.Context, run managedSQL, username, password string) error
	grant(ctx context.Context, run managedSQL, database, username string) error
	revoke(ctx context.Context, run managedSQL, database, username string) error
	// createDatabase creates the database and gives owner full access to it.
	createDatabase(ctx context.Context, run managedSQL, name, owner string) error
	// restrictDatabase closes any default access to a newly created database.
	restrictDatabase(ctx context.Context, run managedSQL, name string) error
	// dropDatabase removes the database and every access listed in grantees.
	// A database that is already gone is not an error.
	dropDatabase(ctx context.Context, run managedSQL, name string, grantees []string) error
	connectionURI(host, username, database string) string
	backup(ctx context.Context, runner ManagedDatabaseRunner, directory, service, database, destination string) error
	restore(ctx context.Context, runner ManagedDatabaseRunner, directory, service, source string) error
}

// managedEngine describes one provider's shared container project under core/.
type managedEngine struct {
	provider  string
	directory string
	service   string
	container string
	volume    string
	// lazy engines are provisioned when their first database is created;
	// Postgres is provisioned when managed databases are enabled.
	lazy           bool
	defaultVersion string
	// image, healthcheck and environmentPrefix render the Compose project of
	// lazy engines. The prefix names the image's *_ROOT_PASSWORD variables.
	image             string
	healthcheck       string
	environmentPrefix string
	driver            managedEngineDriver
	run               func(runner ManagedDatabaseRunner, directory string) managedSQL
	port              string
}

var managedEngines = map[string]managedEngine{
	application.ManagedDatabaseProviderPostgres: {
		provider:       application.ManagedDatabaseProviderPostgres,
		directory:      managedDatabasesDir,
		service:        managedDatabaseServiceName,
		container:      managedDatabaseContainerName,
		volume:         managedDatabaseVolumeName,
		defaultVersion: application.ManagedDatabaseDefaultVersion,
		driver:         postgresDriver{},
		port:           "5432",
		run: func(runner ManagedDatabaseRunner, directory string) managedSQL {
			return func(ctx context.Context, statement string) (string, error) {
				return runner.ExecPostgresSQL(ctx, directory, managedDatabaseServiceName, statement)
			}
		},
	},
	application.ManagedDatabaseProviderMariaDB: mysqlFamilyEngine(
		application.ManagedDatabaseProviderMariaDB, compose.MySQLFlavorMariaDB, application.ManagedDatabaseMariaDBVersion,
		"mariadb", "MARIADB", `["CMD", "healthcheck.sh", "--connect", "--innodb_initialized"]`),
	application.ManagedDatabaseProviderMySQL: mysqlFamilyEngine(
		application.ManagedDatabaseProviderMySQL, compose.MySQLFlavorMySQL, application.ManagedDatabaseMySQLVersion,
		"mysql", "MYSQL", `["CMD-SHELL", "MYSQL_PWD=\"$$MYSQL_ROOT_PASSWORD\" mysqladmin ping --protocol=tcp --host=127.0.0.1 --user=root --silent"]`),
}

func mysqlFamilyEngine(provider string, flavor compose.MySQLFlavor, version, image, prefix, healthcheck string) managedEngine {
	return managedEngine{
		provider:          provider,
		directory:         managedDatabasesDir + "-" + provider,
		service:           provider,
		container:         managedDatabaseContainerName + "-" + provider,
		volume:            managedDatabaseContainerName + "-" + provider + "-data",
		lazy:              true,
		defaultVersion:    version,
		image:             image,
		healthcheck:       healthcheck,
		environmentPrefix: prefix,
		driver:            mysqlDriver{flavor: flavor, provider: provider},
		port:              "3306",
		run: func(runner ManagedDatabaseRunner, directory string) managedSQL {
			return func(ctx context.Context, statement string) (string, error) {
				return runner.ExecMySQLSQL(ctx, directory, provider, flavor, statement)
			}
		},
	}
}

func managedEngineFor(provider string) (managedEngine, error) {
	validated, err := application.ValidateManagedDatabaseEngine(provider)
	if err != nil {
		return managedEngine{}, err
	}
	return managedEngines[validated], nil
}

// renderCompose renders the Compose project of a lazily provisioned engine.
// Every value is a fixed constant or a validated image tag.
func (e managedEngine) renderCompose(version string) string {
	return `services:
  ` + e.service + `:
    image: ` + e.image + `:` + version + `
    container_name: ` + e.container + `
    restart: unless-stopped
    env_file:
      - vars.env
      - secrets.env
    healthcheck:
      test: ` + e.healthcheck + `
      interval: 10s
      timeout: 5s
      retries: 5
      start_period: 30s
    volumes:
      - ` + managedDatabaseVolumeKey + `:/var/lib/mysql
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
    name: ` + e.volume + `
`
}

// postgresDriver implements managedEngineDriver for the shared Postgres cluster.
type postgresDriver struct{}

func (postgresDriver) userExists(ctx context.Context, run managedSQL, username string) (bool, error) {
	output, err := run(ctx, "SELECT 1 FROM pg_roles WHERE rolname = "+postgresQuoteLiteral(username))
	if err != nil {
		// Consult Postgres rather than metadata: a role may have been created externally.
		return false, fmt.Errorf("inspect managed database user: %w", sanitizeManagedError(err))
	}
	return strings.TrimSpace(output) == "1", nil
}

func (postgresDriver) createOwner(ctx context.Context, run managedSQL, username, password string) error {
	// Snapshot PUBLIC access for existing roles before removing it. New roles
	// then need explicit database access, while existing roles retain theirs.
	statement := "BEGIN; " + restrictManagedDatabasePublicAccess + " CREATE ROLE " + postgresQuoteIdentifier(username) +
		" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD " + postgresQuoteLiteral(password) + "; COMMIT;"
	if _, err := run(ctx, statement); err != nil {
		if isManagedAlreadyExists(err) {
			return application.ErrManagedDatabaseUserAlreadyExists
		}
		return fmt.Errorf("create managed database user: %w", sanitizePostgresError(err, password))
	}
	return nil
}

func (postgresDriver) dropUser(ctx context.Context, run managedSQL, username string) error {
	if _, err := run(ctx, "DROP ROLE "+postgresQuoteIdentifier(username)); err != nil {
		if isManagedNotExists(err) {
			// The role is already gone; callers still remove metadata.
			return nil
		}
		return fmt.Errorf("delete managed database user: %w", sanitizeManagedError(err))
	}
	return nil
}

func (postgresDriver) createUser(ctx context.Context, run managedSQL, username, password string) error {
	if _, err := run(ctx, "CREATE ROLE "+postgresQuoteIdentifier(username)+" LOGIN PASSWORD "+postgresQuoteLiteral(password)); err != nil {
		if isManagedAlreadyExists(err) {
			return application.ErrManagedDatabaseUserAlreadyExists
		}
		return fmt.Errorf("create managed database user: %w", sanitizePostgresError(err, password))
	}
	return nil
}

func (postgresDriver) setPassword(ctx context.Context, run managedSQL, username, password string) error {
	if _, err := run(ctx, "ALTER ROLE "+postgresQuoteIdentifier(username)+" WITH PASSWORD "+postgresQuoteLiteral(password)); err != nil {
		return fmt.Errorf("update managed database password: %w", sanitizePostgresError(err, password))
	}
	return nil
}

func (postgresDriver) grant(ctx context.Context, run managedSQL, database, username string) error {
	if _, err := run(ctx, "GRANT ALL PRIVILEGES ON DATABASE "+postgresQuoteIdentifier(database)+" TO "+postgresQuoteIdentifier(username)); err != nil {
		return fmt.Errorf("grant managed database access: %w", sanitizeManagedError(err))
	}
	return nil
}

func (postgresDriver) revoke(ctx context.Context, run managedSQL, database, username string) error {
	if _, err := run(ctx, "REVOKE ALL PRIVILEGES ON DATABASE "+postgresQuoteIdentifier(database)+" FROM "+postgresQuoteIdentifier(username)); err != nil {
		return fmt.Errorf("revoke managed database access: %w", sanitizeManagedError(err))
	}
	return nil
}

func (postgresDriver) createDatabase(ctx context.Context, run managedSQL, name, owner string) error {
	if _, err := run(ctx, "CREATE DATABASE "+postgresQuoteIdentifier(name)+" OWNER "+postgresQuoteIdentifier(owner)); err != nil {
		if isManagedAlreadyExists(err) {
			return application.ErrManagedDatabaseAlreadyExists
		}
		return fmt.Errorf("create managed database: %w", sanitizeManagedError(err))
	}
	return nil
}

func (postgresDriver) restrictDatabase(ctx context.Context, run managedSQL, name string) error {
	// Future databases must not reopen PUBLIC access for previously created users.
	if _, err := run(ctx, "REVOKE CONNECT, TEMPORARY ON DATABASE "+postgresQuoteIdentifier(name)+" FROM PUBLIC"); err != nil {
		return fmt.Errorf("restrict managed database access: %w", sanitizeManagedError(err))
	}
	return nil
}

func (postgresDriver) dropDatabase(ctx context.Context, run managedSQL, name string, _ []string) error {
	if _, err := run(ctx, "DROP DATABASE "+postgresQuoteIdentifier(name)); err != nil {
		if isManagedNotExists(err) {
			// The live database is already gone; callers still remove metadata.
			return nil
		}
		return fmt.Errorf("drop managed database: %w", sanitizeManagedError(err))
	}
	return nil
}

func (postgresDriver) connectionURI(host, username, database string) string {
	connection := url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(username, "PASSWORD"),
		Host:   host + ":5432",
		Path:   "/" + database,
	}
	return connection.String()
}

func (postgresDriver) backup(ctx context.Context, runner ManagedDatabaseRunner, directory, service, _, destination string) error {
	return runner.BackupPostgreSQL(ctx, directory, service, destination)
}

func (postgresDriver) restore(ctx context.Context, runner ManagedDatabaseRunner, directory, service, source string) error {
	return runner.RestorePostgreSQL(ctx, directory, service, source)
}

// mysqlDriver implements managedEngineDriver for MariaDB and MySQL. Logins are
// always 'name'@'%' so applications on the Docker network can connect. The
// root account is limited to localhost and used only through the container.
type mysqlDriver struct {
	flavor   compose.MySQLFlavor
	provider string
}

const mysqlLoginHost = "'%'"

func mysqlQuoteIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func mysqlQuoteLiteral(value string) string {
	return `'` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `'`, `''`) + `'`
}

func mysqlQuoteLogin(username string) string {
	return mysqlQuoteLiteral(username) + "@" + mysqlLoginHost
}

// mysqlQuoteGrantTarget quotes a database name for GRANT and REVOKE. There
// "_" and "%" are wildcards unless escaped, so an unescaped "shop_db" would
// also grant access to "shopXdb".
func mysqlQuoteGrantTarget(database string) string {
	escaped := strings.ReplaceAll(strings.ReplaceAll(database, `\`, `\\`), "_", `\_`)
	return mysqlQuoteIdentifier(escaped) + ".*"
}

func isMySQLAlreadyExists(err error) bool {
	lowered := strings.ToLower(err.Error())
	return strings.Contains(lowered, "database exists") || strings.Contains(lowered, "already exists") ||
		strings.Contains(lowered, "operation create user failed")
}

func (mysqlDriver) userExists(ctx context.Context, run managedSQL, username string) (bool, error) {
	output, err := run(ctx, "SELECT 1 FROM mysql.user WHERE User = "+mysqlQuoteLiteral(username)+" AND Host = "+mysqlLoginHost+" LIMIT 1")
	if err != nil {
		return false, fmt.Errorf("inspect managed database user: %w", sanitizeManagedError(err))
	}
	return strings.TrimSpace(output) == "1", nil
}

func (d mysqlDriver) createOwner(ctx context.Context, run managedSQL, username, password string) error {
	return d.createUser(ctx, run, username, password)
}

func (mysqlDriver) dropUser(ctx context.Context, run managedSQL, username string) error {
	if _, err := run(ctx, "DROP USER IF EXISTS "+mysqlQuoteLogin(username)); err != nil {
		return fmt.Errorf("delete managed database user: %w", sanitizeManagedError(err))
	}
	return nil
}

func (mysqlDriver) createUser(ctx context.Context, run managedSQL, username, password string) error {
	if _, err := run(ctx, "CREATE USER "+mysqlQuoteLogin(username)+" IDENTIFIED BY "+mysqlQuoteLiteral(password)); err != nil {
		if isMySQLAlreadyExists(err) {
			return application.ErrManagedDatabaseUserAlreadyExists
		}
		return fmt.Errorf("create managed database user: %w", sanitizePostgresError(err, password))
	}
	return nil
}

func (mysqlDriver) setPassword(ctx context.Context, run managedSQL, username, password string) error {
	if _, err := run(ctx, "ALTER USER "+mysqlQuoteLogin(username)+" IDENTIFIED BY "+mysqlQuoteLiteral(password)); err != nil {
		return fmt.Errorf("update managed database password: %w", sanitizePostgresError(err, password))
	}
	return nil
}

func (mysqlDriver) grant(ctx context.Context, run managedSQL, database, username string) error {
	if _, err := run(ctx, "GRANT ALL PRIVILEGES ON "+mysqlQuoteGrantTarget(database)+" TO "+mysqlQuoteLogin(username)); err != nil {
		return fmt.Errorf("grant managed database access: %w", sanitizeManagedError(err))
	}
	return nil
}

func (mysqlDriver) revoke(ctx context.Context, run managedSQL, database, username string) error {
	if _, err := run(ctx, "REVOKE ALL PRIVILEGES ON "+mysqlQuoteGrantTarget(database)+" FROM "+mysqlQuoteLogin(username)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such grant") {
			return nil
		}
		return fmt.Errorf("revoke managed database access: %w", sanitizeManagedError(err))
	}
	return nil
}

func (d mysqlDriver) createDatabase(ctx context.Context, run managedSQL, name, owner string) error {
	if _, err := run(ctx, "CREATE DATABASE "+mysqlQuoteIdentifier(name)); err != nil {
		if isMySQLAlreadyExists(err) {
			return application.ErrManagedDatabaseAlreadyExists
		}
		return fmt.Errorf("create managed database: %w", sanitizeManagedError(err))
	}
	if err := d.grant(ctx, run, name, owner); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managedCleanupTimeout)
		defer cancel()
		_, _ = run(cleanupCtx, "DROP DATABASE IF EXISTS "+mysqlQuoteIdentifier(name))
		return err
	}
	return nil
}

func (mysqlDriver) restrictDatabase(context.Context, managedSQL, string) error {
	// New MySQL and MariaDB databases are closed by default.
	return nil
}

func (d mysqlDriver) dropDatabase(ctx context.Context, run managedSQL, name string, grantees []string) error {
	if _, err := run(ctx, "DROP DATABASE IF EXISTS "+mysqlQuoteIdentifier(name)); err != nil {
		return fmt.Errorf("drop managed database: %w", sanitizeManagedError(err))
	}
	// Privileges outlive DROP DATABASE and would apply to a database later
	// created with the same name, so remove them explicitly.
	for _, grantee := range grantees {
		if err := d.revoke(ctx, run, name, grantee); err != nil {
			return err
		}
	}
	return nil
}

func (mysqlDriver) connectionURI(host, username, database string) string {
	// MariaDB and MySQL clients both accept the mysql:// scheme.
	connection := url.URL{
		Scheme: "mysql",
		User:   url.UserPassword(username, "PASSWORD"),
		Host:   host + ":3306",
		Path:   "/" + database,
	}
	return connection.String()
}

func (d mysqlDriver) backup(ctx context.Context, runner ManagedDatabaseRunner, directory, service, database, destination string) error {
	return runner.BackupMySQL(ctx, directory, service, d.flavor, database, destination)
}

func (d mysqlDriver) restore(ctx context.Context, runner ManagedDatabaseRunner, directory, service, source string) error {
	return runner.RestoreMySQL(ctx, directory, service, d.flavor, source)
}
