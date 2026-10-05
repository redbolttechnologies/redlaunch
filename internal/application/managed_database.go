package application

import (
	"errors"
	"strings"
	"time"
)

const (
	// ManagedDatabaseProviderPostgres is the only supported server-level
	// database provider for now. The provider value is persisted so future
	// providers can be added without a schema change.
	ManagedDatabaseProviderPostgres = "postgres"
	// ManagedDatabaseDefaultVersion is used to pre-fill the enable dialog.
	ManagedDatabaseDefaultVersion = "17"
)

var (
	ErrManagedDatabasesDisabled         = errors.New("managed databases are not enabled")
	ErrManagedDatabasesAlreadyEnabled   = errors.New("managed databases are already enabled")
	ErrManagedDatabaseProviderInvalid   = errors.New("database provider is invalid")
	ErrManagedDatabaseAlreadyExists     = errors.New("managed database already exists")
	ErrManagedDatabaseNotFound          = errors.New("managed database not found")
	ErrManagedDatabaseUserAlreadyExists = errors.New("managed database user already exists")
	ErrManagedDatabaseUserNotFound      = errors.New("managed database user not found")
	ErrManagedDatabaseDefaultUserInUse  = errors.New("the default database user cannot be deleted")
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

// ManagedDatabase is one logical database inside the shared cluster.
type ManagedDatabase struct {
	ID        int64
	Name      string
	Owner     string
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

// ManagedDatabaseStatus describes the shared container for the dashboard.
// Runtime fields are best-effort and may be empty when Docker is unreachable.
type ManagedDatabaseStatus struct {
	Running       bool
	Status        string
	ContainerName string
	ImageName     string
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
	Name  string
	Owner string
}

// ManagedDatabaseUserInput contains the values for creating a database user.
type ManagedDatabaseUserInput struct {
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
	username, err := ValidateManagedDatabaseUsername(input.Username)
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
	return ManagedDatabaseUserInput{Username: username, Password: password, Databases: databases}, nil
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
