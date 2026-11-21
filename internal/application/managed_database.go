package application

import (
	"errors"
	"strings"
	"time"
)

const (
	// ManagedDatabaseProviderPostgres is the provider enabled with the managed
	// databases feature. Its container is provisioned when the feature is
	// enabled; the other providers are provisioned on first use.
	ManagedDatabaseProviderPostgres = "postgres"
	// ManagedDatabaseProviderMariaDB and ManagedDatabaseProviderMySQL run in
	// their own shared containers, started when their first database is created.
	ManagedDatabaseProviderMariaDB = "mariadb"
	ManagedDatabaseProviderMySQL   = "mysql"
	// ManagedDatabaseDefaultVersion is used to pre-fill the enable dialog.
	ManagedDatabaseDefaultVersion = "17"
	// ManagedDatabaseMariaDBVersion and ManagedDatabaseMySQLVersion are the
	// official image tags used when those providers are first provisioned.
	ManagedDatabaseMariaDBVersion = "11.4"
	ManagedDatabaseMySQLVersion   = "8.4"
	// maxMySQLUsernameLength is the longest login MySQL 8.4 accepts.
	maxMySQLUsernameLength = 32
)

// ManagedDatabaseProviders lists the supported providers in display order.
var ManagedDatabaseProviders = []string{
	ManagedDatabaseProviderPostgres,
	ManagedDatabaseProviderMariaDB,
	ManagedDatabaseProviderMySQL,
}

// ManagedDatabaseProviderName returns the display name of a provider.
func ManagedDatabaseProviderName(provider string) string {
	switch provider {
	case ManagedDatabaseProviderMariaDB:
		return "MariaDB"
	case ManagedDatabaseProviderMySQL:
		return "MySQL"
	default:
		return "Postgres"
	}
}

var (
	ErrManagedDatabasesDisabled            = errors.New("managed databases are not enabled")
	ErrManagedDatabasesAlreadyEnabled      = errors.New("managed databases are already enabled")
	ErrManagedDatabaseProviderInvalid      = errors.New("database provider is invalid")
	ErrManagedDatabaseProviderMismatch     = errors.New("database user belongs to a different provider")
	ErrManagedDatabaseEngineNotProvisioned = errors.New("database engine has not been started yet")
	ErrManagedDatabaseEngineNotReady       = errors.New("database engine did not become ready")
	ErrManagedDatabaseUserOwnsDatabase     = errors.New("the database user owns a database")
	ErrManagedDatabaseWorkspaceUnsupported = errors.New("the SQL workspace is only available for Postgres databases")
	ErrManagedDatabaseUserTooLong          = errors.New("database user is too long for this provider")
	ErrManagedDatabaseAlreadyExists        = errors.New("managed database already exists")
	ErrManagedDatabaseNotFound             = errors.New("managed database not found")
	ErrManagedDatabaseUserAlreadyExists    = errors.New("managed database user already exists")
	ErrManagedDatabaseUserNotFound         = errors.New("managed database user not found")
	ErrManagedDatabaseDefaultUserInUse     = errors.New("the default database user cannot be deleted")
)

// ManagedDatabaseCluster is the singleton server-level database cluster. It
// lives outside the applications scope and is backed by a dedicated Compose
// project under core/databases.
type ManagedDatabaseCluster struct {
	Enabled     bool
	Provider    string
	Version     string
	DefaultUser string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ManagedDatabaseEngine is one provider's shared container. Postgres is
// provisioned when managed databases are enabled; MariaDB and MySQL are
// provisioned when their first database is created. Runtime fields are
// best-effort and may be empty when Docker is unreachable.
type ManagedDatabaseEngine struct {
	Provider      string
	Version       string
	Provisioned   bool
	Running       bool
	Status        string
	ContainerName string
	ImageName     string
	Databases     int
	CreatedAt     time.Time
}

// Name returns the provider's display name.
func (e ManagedDatabaseEngine) Name() string {
	return ManagedDatabaseProviderName(e.Provider)
}

// ManagedDatabase is one logical database inside a provider's shared container.
type ManagedDatabase struct {
	ID        int64
	Name      string
	Owner     string
	Provider  string
	CreatedAt time.Time
}

// ManagedDatabaseCreation returns credentials only when a login was created.
// Credentials must never be persisted or included in diagnostics.
type ManagedDatabaseCreation struct {
	ManagedDatabase
	Credentials *ManagedDatabaseCredentials
}

// ManagedDatabaseCredentials contains the one-time generated login credentials.
type ManagedDatabaseCredentials struct {
	Username string
	Password string
}

// ManagedDatabaseUser is one login role inside the shared cluster. Passwords
// are never persisted in SQLite.
type ManagedDatabaseUser struct {
	Username  string
	Provider  string
	CreatedAt time.Time
}

// ManagedDatabaseUserDetail couples a user with the databases it may access.
type ManagedDatabaseUserDetail struct {
	User      ManagedDatabaseUser
	Databases []string
}

// ManagedDatabaseConnection is a Docker-network connection URI for a managed
// login. The password is a placeholder; stored passwords cannot be retrieved.
type ManagedDatabaseConnection struct {
	Username string
	URI      string
}

// ManagedDatabaseEnableInput contains the values from the enable dialog.
type ManagedDatabaseEnableInput struct {
	Provider    string
	Version     string
	DefaultUser string
	Password    string
}

// ManagedDatabaseCreateInput contains the values for creating a logical DB.
type ManagedDatabaseCreateInput struct {
	// RequireExistingOwner prevents generating credentials for machine requests.
	RequireExistingOwner bool
	// Provider selects the database engine; empty means Postgres.
	Provider string
	Name     string
	Owner    string
}

// ManagedDatabaseUserInput contains the values for creating a database user.
type ManagedDatabaseUserInput struct {
	// Provider selects the database engine; empty means Postgres.
	Provider  string
	Username  string
	Password  string
	Databases []string
}

// ValidateManagedDatabaseProvider accepts only Postgres for now.
func ValidateManagedDatabaseProvider(value string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(value))
	if provider == "" {
		provider = ManagedDatabaseProviderPostgres
	}
	if provider != ManagedDatabaseProviderPostgres {
		return "", ErrManagedDatabaseProviderInvalid
	}
	return provider, nil
}

// ValidateManagedDatabaseEngine accepts every supported provider. An empty
// value selects Postgres, which keeps older API clients working.
func ValidateManagedDatabaseEngine(value string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(value))
	if provider == "" {
		return ManagedDatabaseProviderPostgres, nil
	}
	for _, supported := range ManagedDatabaseProviders {
		if provider == supported {
			return provider, nil
		}
	}
	return "", ErrManagedDatabaseProviderInvalid
}

// ValidateManagedDatabaseLogin applies the login-name limits of one provider on
// top of the shared username rules. MySQL 8.4 rejects logins above 32 bytes.
func ValidateManagedDatabaseLogin(provider, value string) (string, error) {
	username, err := ValidateManagedDatabaseUsername(value)
	if err != nil {
		return "", err
	}
	if provider == ManagedDatabaseProviderMySQL && len(username) > maxMySQLUsernameLength {
		return "", ErrManagedDatabaseUserTooLong
	}
	return username, nil
}

// ValidateManagedDatabaseCreateInput validates a logical database request and
// resolves its defaults: Postgres for an empty provider and the database name
// for an empty owner.
func ValidateManagedDatabaseCreateInput(input ManagedDatabaseCreateInput) (ManagedDatabaseCreateInput, error) {
	provider, err := ValidateManagedDatabaseEngine(input.Provider)
	if err != nil {
		return ManagedDatabaseCreateInput{}, err
	}
	name, err := ValidateManagedDatabaseName(input.Name)
	if err != nil {
		return ManagedDatabaseCreateInput{}, err
	}
	owner := strings.TrimSpace(input.Owner)
	if owner == "" {
		owner = name
	}
	owner, err = ValidateManagedDatabaseLogin(provider, owner)
	if err != nil {
		return ManagedDatabaseCreateInput{}, err
	}
	return ManagedDatabaseCreateInput{
		RequireExistingOwner: input.RequireExistingOwner,
		Provider:             provider,
		Name:                 name,
		Owner:                owner,
	}, nil
}

// ValidateManagedDatabaseEnableInput validates the enable dialog without
// touching storage or Docker.
func ValidateManagedDatabaseEnableInput(input ManagedDatabaseEnableInput) (ManagedDatabaseEnableInput, error) {
	provider, err := ValidateManagedDatabaseProvider(input.Provider)
	if err != nil {
		return ManagedDatabaseEnableInput{}, err
	}
	version, err := ValidatePostgresVersion(input.Version)
	if err != nil {
		return ManagedDatabaseEnableInput{}, err
	}
	defaultUser, err := ValidateDatabaseUser(input.DefaultUser)
	if err != nil {
		return ManagedDatabaseEnableInput{}, err
	}
	password, err := ValidateDatabasePassword(input.Password)
	if err != nil {
		return ManagedDatabaseEnableInput{}, err
	}
	return ManagedDatabaseEnableInput{
		Provider:    provider,
		Version:     version,
		DefaultUser: defaultUser,
		Password:    password,
	}, nil
}

// ValidateManagedDatabaseName reuses the PostgreSQL database-name rules.
func ValidateManagedDatabaseName(value string) (string, error) {
	return ValidateDatabaseName(value)
}

// ValidateManagedDatabaseOwner reuses the PostgreSQL role-name rules. An
// empty owner means the database name.
func ValidateManagedDatabaseOwner(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return ValidateManagedDatabaseUsername(value)
}

// ValidateManagedDatabaseUsername accepts the same names as logical databases.
// Managed roles are quoted in SQL, unlike users passed to image initialization.
func ValidateManagedDatabaseUsername(value string) (string, error) {
	username, err := ValidateManagedDatabaseName(value)
	switch {
	case errors.Is(err, ErrDatabaseNameRequired):
		return "", ErrDatabaseUserRequired
	case errors.Is(err, ErrDatabaseNameTooLong):
		return "", ErrDatabaseUserTooLong
	case err != nil:
		return "", ErrDatabaseUserInvalid
	default:
		return username, nil
	}
}

// ValidateManagedDatabaseUserInput validates user creation input.
func ValidateManagedDatabaseUserInput(input ManagedDatabaseUserInput) (ManagedDatabaseUserInput, error) {
	provider, err := ValidateManagedDatabaseEngine(input.Provider)
	if err != nil {
		return ManagedDatabaseUserInput{}, err
	}
	username, err := ValidateManagedDatabaseLogin(provider, input.Username)
	if err != nil {
		return ManagedDatabaseUserInput{}, err
	}
	password, err := ValidateDatabasePassword(input.Password)
	if err != nil {
		return ManagedDatabaseUserInput{}, err
	}
	if strings.TrimSpace(password) == "" {
		return ManagedDatabaseUserInput{}, ErrDatabasePasswordInvalid
	}
	databases := make([]string, 0, len(input.Databases))
	seen := make(map[string]struct{}, len(input.Databases))
	for _, name := range input.Databases {
		validated, err := ValidateDatabaseName(name)
		if err != nil {
			return ManagedDatabaseUserInput{}, err
		}
		if _, ok := seen[validated]; ok {
			continue
		}
		seen[validated] = struct{}{}
		databases = append(databases, validated)
	}
	return ManagedDatabaseUserInput{Provider: provider, Username: username, Password: password, Databases: databases}, nil
}

// ValidateManagedDatabaseGrants validates the database list from the
// permissions form. An empty list revokes all database access.
func ValidateManagedDatabaseGrants(databases []string) ([]string, error) {
	grants := make([]string, 0, len(databases))
	seen := make(map[string]struct{}, len(databases))
	for _, name := range databases {
		if strings.TrimSpace(name) == "" {
			continue
		}
		validated, err := ValidateDatabaseName(name)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[validated]; ok {
			continue
		}
		seen[validated] = struct{}{}
		grants = append(grants, validated)
	}
	if grants == nil {
		grants = []string{}
	}
	return grants, nil
}
