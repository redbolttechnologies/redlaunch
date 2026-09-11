// Package application contains the domain types and validation for managed
// Docker Compose applications.
package application

import (
	"errors"
	"net"
	"net/mail"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxNameLength             = 64
	MaxFolderNameLength       = 64
	MaxServiceNameLength      = 64
	MaxImageNameLength        = 255
	MaxPostgresVersionLength  = 64
	MaxRedisVersionLength     = 64
	MaxDatabaseNameLength     = 63
	MaxDatabaseUserLength     = 63
	MaxDatabasePasswordLength = 256
	MaxRedisPortLength        = 5
	MaxRedisPasswordLength    = 256
	MaxDomainNameLength       = 253
	MaxRoutingSubdomainLength = 253
	MaxRoutingPathLength      = 2048
	MaxBackupFileNameLength   = 255
	MaxEmailLength            = 320
	MaxBackupRetentionDays    = 3650
	MaxComposeFileSize        = 4 << 20
	MaxEnvironmentFileSize    = 1 << 20
	LocalRegistryAddress      = "localhost:5000"
	ServiceTypeApplication    = "application"
	ServiceTypePostgreSQL     = "postgresql"
	ServiceTypeRedis          = "redis"
	BackupScheduleHourly      = "hourly"
	BackupScheduleDaily       = "daily"
	BackupScheduleWeekly      = "weekly"
	BackupDefaultHour         = 3
	BackupDefaultMinute       = 0
	BackupDefaultRetention    = 14
	LogLineLimit              = 1000
	ProxyLogLineLimit         = LogLineLimit
	ServiceLogLineLimit       = LogLineLimit
)

const (
	MaxApplicationCommandLength      = 4096
	MaxApplicationVolumeSourceLength = 255
	MaxApplicationVolumeTargetLength = 255
	MaxApplicationMountOptionsLength = 128
)

var (
	ErrNameRequired                     = errors.New("application name is required")
	ErrNameTooLong                      = errors.New("application name is too long")
	ErrNameInvalid                      = errors.New("application name contains a control character")
	ErrFolderNameRequired               = errors.New("application folder name is required")
	ErrFolderNameTooLong                = errors.New("application folder name is too long")
	ErrFolderNameInvalid                = errors.New("application folder name is invalid")
	ErrServiceNameRequired              = errors.New("service name is required")
	ErrServiceNameTooLong               = errors.New("service name is too long")
	ErrServiceNameInvalid               = errors.New("service name is invalid")
	ErrImageNameRequired                = errors.New("image name is required")
	ErrImageNameTooLong                 = errors.New("image name is too long")
	ErrImageNameInvalid                 = errors.New("image name is invalid")
	ErrPostgresVersionRequired          = errors.New("PostgreSQL version is required")
	ErrPostgresVersionTooLong           = errors.New("PostgreSQL version is too long")
	ErrPostgresVersionInvalid           = errors.New("PostgreSQL version is invalid")
	ErrRedisVersionRequired             = errors.New("Redis version is required")
	ErrRedisVersionTooLong              = errors.New("Redis version is too long")
	ErrRedisVersionInvalid              = errors.New("Redis version is invalid")
	ErrRedisPortRequired                = errors.New("Redis port is required")
	ErrRedisPortTooLong                 = errors.New("Redis port is too long")
	ErrRedisPortInvalid                 = errors.New("Redis port is invalid")
	ErrRedisPasswordTooLong             = errors.New("Redis password is too long")
	ErrRedisPasswordInvalid             = errors.New("Redis password is invalid")
	ErrDatabaseNameRequired             = errors.New("database name is required")
	ErrDatabaseNameTooLong              = errors.New("database name is too long")
	ErrDatabaseNameInvalid              = errors.New("database name is invalid")
	ErrDatabaseUserRequired             = errors.New("database user is required")
	ErrDatabaseUserTooLong              = errors.New("database user is too long")
	ErrDatabaseUserInvalid              = errors.New("database user is invalid")
	ErrDatabasePasswordTooLong          = errors.New("database password is too long")
	ErrDatabasePasswordInvalid          = errors.New("database password is invalid")
	ErrDomainNameRequired               = errors.New("domain name is required")
	ErrDomainNameTooLong                = errors.New("domain name is too long")
	ErrDomainNameInvalid                = errors.New("domain name is invalid")
	ErrDomainAlreadyExists              = errors.New("domain already exists")
	ErrDomainNotFound                   = errors.New("domain not found")
	ErrRoutingSubdomainTooLong          = errors.New("routing subdomain is too long")
	ErrRoutingSubdomainInvalid          = errors.New("routing subdomain is invalid")
	ErrRoutingHostTooLong               = errors.New("routing host is too long")
	ErrRoutingPathRequired              = errors.New("routing path is required")
	ErrRoutingPathTooLong               = errors.New("routing path is too long")
	ErrRoutingPathInvalid               = errors.New("routing path is invalid")
	ErrRoutingPortInvalid               = errors.New("routing service port is invalid")
	ErrRoutingAlreadyExists             = errors.New("routing already exists")
	ErrRoutingNotFound                  = errors.New("routing not found")
	ErrServiceAlreadyExists             = errors.New("service already exists")
	ErrDatabaseServiceTypeAlreadyExists = errors.New("application already has a database service of this type")
	ErrDatabaseCredentialsAmbiguous     = errors.New("database service credentials are ambiguous")
	ErrDatabaseCredentialsConflict      = errors.New("database service credentials conflict with existing data")
	ErrServiceNotFound                  = errors.New("service not found")
	ErrEnvironmentVariableNameRequired  = errors.New("environment variable name is required")
	ErrEnvironmentVariableNameInvalid   = errors.New("environment variable name is invalid")
	ErrEnvironmentVariableValueInvalid  = errors.New("environment variable value is invalid")
	ErrEnvironmentVariableNotFound      = errors.New("environment variable not found")
	ErrEnvironmentVariableAlreadyExists = errors.New("environment variable already exists")
	ErrEnvironmentVariableDuplicate     = errors.New("environment variable is duplicated")
	ErrEnvironmentFileNotFound          = errors.New("environment file not found")
	ErrEnvironmentImportFileRequired    = errors.New("environment file import requires at least one file")
	ErrEnvironmentImportFileTooLarge    = errors.New("environment file import is too large")
	ErrEnvironmentImportInvalid         = errors.New("environment file import is invalid")
	ErrAlreadyExists                    = errors.New("application already exists")
	ErrNotFound                         = errors.New("application not found")
	ErrBackupScheduleTypeInvalid        = errors.New("backup schedule type is invalid")
	ErrBackupHourInvalid                = errors.New("backup hour is invalid")
	ErrBackupMinuteInvalid              = errors.New("backup minute is invalid")
	ErrBackupWeekdayInvalid             = errors.New("backup weekday is invalid")
	ErrBackupRetentionInvalid           = errors.New("backup retention is invalid")
	ErrBackupFileNameInvalid            = errors.New("backup file name is invalid")
	ErrBackupNotFound                   = errors.New("backup not found")
	ErrBackupAlreadyExists              = errors.New("backup already exists")
	ErrBackupScheduleNotFound           = errors.New("backup schedule not found")
	ErrBackupScheduleDisabled           = errors.New("backup schedule is disabled")
	ErrBackupServiceNotRunning          = errors.New("database service is not running")
	ErrBackupUnsupported                = errors.New("scheduled backups are not supported for this service")
	ErrEmailRequired                    = errors.New("email address is required")
	ErrEmailTooLong                     = errors.New("email address is too long")
	ErrEmailInvalid                     = errors.New("email address is invalid")
	ErrAuthorizedEmailAlreadyExists     = errors.New("authorized email already exists")
	ErrRedlaunchPublicDomainRequired    = errors.New("Redlaunch public domain is required")
	ErrRedlaunchPublicDomainTooLong     = errors.New("Redlaunch public domain is too long")
	ErrRedlaunchPublicDomainInvalid     = errors.New("Redlaunch public domain is invalid")
	ErrComposeFileRequired              = errors.New("Docker Compose file is required")
	ErrComposeFileTooLarge              = errors.New("Docker Compose file is too large")
	ErrComposeFileInvalid               = errors.New("Docker Compose file is invalid")
	ErrComposeProjectHasNoServices      = errors.New("Docker Compose file does not define any services")
	ErrComposeServicesAlreadyExist      = errors.New("application already has services")
	ErrGitHubActionsRepositoryRequired  = errors.New("GitHub repository is required")
	ErrGitHubActionsRepositoryInvalid   = errors.New("GitHub repository is invalid")
	ErrGitHubActionsBranchRequired      = errors.New("GitHub Actions branch is required")
	ErrGitHubActionsBranchInvalid       = errors.New("GitHub Actions branch is invalid")
	ErrGitHubActionsPathInvalid         = errors.New("GitHub Actions path is invalid")
	ErrGitHubActionsHostRequired        = errors.New("GitHub Actions server host is required")
	ErrGitHubActionsHostInvalid         = errors.New("GitHub Actions server host is invalid")
	ErrGitHubActionsImageRequired       = errors.New("GitHub Actions image repository is required")
	ErrGitHubActionsImageInvalid        = errors.New("GitHub Actions image repository is invalid")
	ErrGitHubActionsServiceRequired     = errors.New("GitHub Actions service is required")
	ErrGitHubActionsNotConfigured       = errors.New("GitHub Actions deployment is not configured")
)

var (
	ErrApplicationEntrypointTooLong             = errors.New("application entrypoint is too long")
	ErrApplicationEntrypointInvalid             = errors.New("application entrypoint is invalid")
	ErrApplicationHealthcheckCommandTooLong     = errors.New("application healthcheck command is too long")
	ErrApplicationHealthcheckCommandInvalid     = errors.New("application healthcheck command is invalid")
	ErrApplicationHealthcheckIntervalInvalid    = errors.New("application healthcheck interval is invalid")
	ErrApplicationHealthcheckTimeoutInvalid     = errors.New("application healthcheck timeout is invalid")
	ErrApplicationHealthcheckRetriesInvalid     = errors.New("application healthcheck retries are invalid")
	ErrApplicationHealthcheckStartPeriodInvalid = errors.New("application healthcheck start period is invalid")
	ErrApplicationDependencyServiceRequired     = errors.New("application dependency service is required")
	ErrApplicationDependencyServiceInvalid      = errors.New("application dependency service is invalid")
	ErrApplicationDependencyServiceNotFound     = errors.New("application dependency service was not found")
	ErrApplicationDependencyConditionInvalid    = errors.New("application dependency condition is invalid")
	ErrApplicationDependencyDuplicate           = errors.New("application dependency is duplicated")
	ErrApplicationDependencySelf                = errors.New("application service cannot depend on itself")
	ErrApplicationRestartPolicyInvalid          = errors.New("application restart policy is invalid")
	ErrApplicationPortMappingInvalid            = errors.New("application port mapping is invalid")
	ErrApplicationVolumeSourceRequired          = errors.New("application volume source is required")
	ErrApplicationVolumeSourceInvalid           = errors.New("application volume source is invalid")
	ErrApplicationVolumeTargetRequired          = errors.New("application volume target is required")
	ErrApplicationVolumeTargetInvalid           = errors.New("application volume target is invalid")
	ErrApplicationVolumeOptionsInvalid          = errors.New("application volume options are invalid")
)

const (
	ApplicationRestartPolicyNo            = "no"
	ApplicationRestartPolicyAlways        = "always"
	ApplicationRestartPolicyOnFailure     = "on-failure"
	ApplicationRestartPolicyUnlessStopped = "unless-stopped"

	ApplicationDependencyConditionStarted               = "service_started"
	ApplicationDependencyConditionHealthy               = "service_healthy"
	ApplicationDependencyConditionCompletedSuccessfully = "service_completed_successfully"
)

// Application is the metadata needed to locate a managed application.
type Application struct {
	ID         int64
	Name       string
	FolderName string
	CreatedAt  time.Time
	// ServiceCount is derived from the application's registered services.
	ServiceCount int
}

// GitHubActionsInput contains the repository-specific values used to render
// a build-and-push workflow. It deliberately contains no credentials.
type GitHubActionsInput struct {
	Repository   string
	Branch       string
	Dockerfile   string
	BuildContext string
	ServiceName  string
	ImageName    string
	ServerHost   string
}

// GitHubActionsIntegration stores the non-secret state of one repository
// integration. The private client key is never persisted.
type GitHubActionsIntegration struct {
	ID             int64
	ApplicationID  int64
	Repository     string
	Branch         string
	Dockerfile     string
	BuildContext   string
	ServiceName    string
	ImageName      string
	ServerHost     string
	ServerPort     int
	SSHUsername    string
	PublicKey      string
	KeyFingerprint string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// GitHubActionsSetup is returned only immediately after provisioning. The
// private key is intentionally transient and must not be stored or logged.
type GitHubActionsSetup struct {
	Integration  GitHubActionsIntegration
	PrivateKey   string
	KnownHosts   string
	HostKey      string
	Workflow     string
	Instructions []string
}

// ValidateGitHubActionsInput normalizes and validates the values used by the
// GitHub Actions setup wizard. Paths are restricted to the repository's
// relative namespace and hosts are restricted to IP addresses or DNS names.
func ValidateGitHubActionsInput(input GitHubActionsInput) (GitHubActionsInput, error) {
	repository, err := ValidateGitHubRepository(input.Repository)
	if err != nil {
		return GitHubActionsInput{}, err
	}
	branch, err := ValidateGitHubActionsBranch(input.Branch)
	if err != nil {
		return GitHubActionsInput{}, err
	}
	dockerfile, err := ValidateGitHubActionsPath(input.Dockerfile, false)
	if err != nil {
		return GitHubActionsInput{}, err
	}
	buildContext, err := ValidateGitHubActionsPath(input.BuildContext, true)
	if err != nil {
		return GitHubActionsInput{}, err
	}
	serviceName, err := ValidateServiceName(input.ServiceName)
	if err != nil {
		return GitHubActionsInput{}, ErrGitHubActionsServiceRequired
	}
	imageName, err := ValidateGitHubActionsImageName(input.ImageName)
	if err != nil {
		return GitHubActionsInput{}, err
	}
	serverHost, err := ValidateGitHubActionsHost(input.ServerHost)
	if err != nil {
		return GitHubActionsInput{}, err
	}
	return GitHubActionsInput{
		Repository:   repository,
		Branch:       branch,
		Dockerfile:   dockerfile,
		BuildContext: buildContext,
		ServiceName:  serviceName,
		ImageName:    imageName,
		ServerHost:   serverHost,
	}, nil
}

// ValidateGitHubRepository accepts the owner/repository form used by GitHub.
func ValidateGitHubRepository(value string) (string, error) {
	repository := strings.TrimSpace(value)
	if repository == "" {
		return "", ErrGitHubActionsRepositoryRequired
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || !validGitHubName(parts[0]) || !validGitHubName(parts[1]) {
		return "", ErrGitHubActionsRepositoryInvalid
	}
	return parts[0] + "/" + parts[1], nil
}

func validGitHubName(value string) bool {
	if value == "" || len(value) > 100 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if !isASCIIAlphaNumeric(character) && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

// ValidateGitHubActionsBranch accepts a branch path without shell or YAML
// metacharacters. The value remains case-sensitive because Git refs are.
func ValidateGitHubActionsBranch(value string) (string, error) {
	branch := strings.TrimSpace(value)
	if branch == "" {
		return "", ErrGitHubActionsBranchRequired
	}
	if len(branch) > 255 || branch == "." || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, `\\ ~^:?*[]`) || containsControlCharacter(branch) {
		return "", ErrGitHubActionsBranchInvalid
	}
	for _, character := range branch {
		if !isASCIIAlphaNumeric(character) && character != '-' && character != '_' && character != '.' && character != '/' {
			return "", ErrGitHubActionsBranchInvalid
		}
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || component == "." || strings.HasSuffix(component, ".lock") {
			return "", ErrGitHubActionsBranchInvalid
		}
	}
	return branch, nil
}

// ValidateGitHubActionsPath accepts a repository-relative path. A build
// context may be "."; Dockerfile paths may not be empty.
func ValidateGitHubActionsPath(value string, allowDot bool) (string, error) {
	pathValue := strings.TrimSpace(value)
	if pathValue == "" && allowDot {
		pathValue = "."
	}
	if pathValue == "" || strings.Contains(pathValue, "\\") || containsControlCharacter(pathValue) {
		return "", ErrGitHubActionsPathInvalid
	}
	cleaned := path.Clean(pathValue)
	if cleaned == "." && !allowDot || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", ErrGitHubActionsPathInvalid
	}
	for _, component := range strings.Split(cleaned, "/") {
		if component == ".." || component == "" || containsControlCharacter(component) {
			return "", ErrGitHubActionsPathInvalid
		}
	}
	return cleaned, nil
}

// ValidateGitHubActionsHost accepts a public IP address or DNS host name.
func ValidateGitHubActionsHost(value string) (string, error) {
	host := strings.TrimSpace(value)
	if host == "" {
		return "", ErrGitHubActionsHostRequired
	}
	if containsControlCharacter(host) {
		return "", ErrGitHubActionsHostInvalid
	}
	if parsedIP := net.ParseIP(host); parsedIP != nil {
		return parsedIP.String(), nil
	}
	if len(host) > 253 || strings.ContainsAny(host, `/\\:@[]`) {
		return "", ErrGitHubActionsHostInvalid
	}
	if strings.HasSuffix(host, ".") {
		host = strings.TrimSuffix(host, ".")
	}
	if host == "" {
		return "", ErrGitHubActionsHostInvalid
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrGitHubActionsHostInvalid
		}
		for _, character := range label {
			if !isASCIIAlphaNumeric(character) && character != '-' {
				return "", ErrGitHubActionsHostInvalid
			}
		}
	}
	return strings.ToLower(host), nil
}

// ValidateGitHubActionsImageName validates a repository path without a
// registry, tag, or digest. The workflow adds the local registry and SHA tag.
func ValidateGitHubActionsImageName(value string) (string, error) {
	image := strings.TrimSpace(strings.ToLower(value))
	if image == "" {
		return "", ErrGitHubActionsImageRequired
	}
	if strings.ContainsAny(image, `:@\\$;{}#'"`) || strings.Contains(image, "://") || len(image) > MaxImageNameLength {
		return "", ErrGitHubActionsImageInvalid
	}
	if firstComponent := strings.SplitN(image, "/", 2)[0]; looksLikeImageRegistry(firstComponent) {
		return "", ErrGitHubActionsImageInvalid
	}
	if !validImageRepositoryName(image) {
		return "", ErrGitHubActionsImageInvalid
	}
	return image, nil
}

// Domain is a domain name associated with an application.
type Domain struct {
	ID            int64
	ApplicationID int64
	Name          string
}

// RedlaunchPublicAccess controls whether the Redlaunch management interface
// is published through the managed Caddy proxy.
type RedlaunchPublicAccess struct {
	Enabled bool
	Domain  string
}

// RedlaunchPublicAccessInput contains the public-access settings supplied by
// the settings form.
type RedlaunchPublicAccessInput struct {
	Enabled bool
	Domain  string
}

// Routing is one request-path mapping for an application's associated domain.
// DomainName is populated by repository reads and is not stored separately.
type Routing struct {
	ID            int64
	ApplicationID int64
	DomainID      int64
	DomainName    string
	Subdomain     string
	Path          string
	ServiceName   string
	ServicePort   int
	ServicePath   string
}

// RoutingInput contains the user-supplied values needed to create or update a
// routing. The selected domain is supplied separately by the service method.
type RoutingInput struct {
	Subdomain   string
	Path        string
	ServiceName string
	ServicePort int
	ServicePath string
}

// Service is the metadata for one service in an application's Compose file.
// The Compose definition remains the operational source while this metadata
// gives the application UI a stable database-backed service list.
type Service struct {
	ID                 int64
	ApplicationID      int64
	Name               string
	Type               string
	ImageName          string
	PostgresVersion    string
	DatabaseName       string
	DatabaseUser       string
	RedisVersion       string
	RedisPort          string
	RedisPersistToDisk bool
	CreatedAt          time.Time
	// Runtime details are populated best-effort from Docker and are not
	// persisted as service metadata.
	ContainerName      string
	ContainerCreatedAt time.Time
	Status             string
	Ports              string
}

// EnvironmentVariable is one environment value for a managed service or
// application. Sensitive values remain available to the server for
// operational use, but callers should honor Sensitive when deciding what to
// display.
type EnvironmentVariable struct {
	Key       string
	Value     string
	Sensitive bool
}

// EnvironmentFiles contains the values read from an application's managed
// environment files. Availability is tracked separately because an older or
// manually-created application may not have both files yet.
type EnvironmentFiles struct {
	Variables          []EnvironmentVariable
	Secrets            []EnvironmentVariable
	VariablesAvailable bool
	SecretsAvailable   bool
}

// EnvironmentFileImportInput contains the uploaded managed environment files.
// A file is replaced only when its corresponding Provided field is true; an
// empty provided file intentionally clears that managed file.
type EnvironmentFileImportInput struct {
	Variables         []byte
	VariablesProvided bool
	Secrets           []byte
	SecretsProvided   bool
}

// ServiceDetails is the dashboard data for one managed service. Runtime
// fields are best-effort and may be unavailable when Docker is unreachable or
// the service has not created a container yet.
type ServiceDetails struct {
	Service              Service
	Logs                 string
	LogsAvailable        bool
	Environment          []EnvironmentVariable
	EnvironmentAvailable bool
	Backup               *BackupDetails
}

// ProxyDetails contains the runtime fields and routed domains for the
// managed core proxy. Runtime fields are best-effort and may be unavailable
// when Docker is unreachable or the proxy has not created a container yet.
type ProxyDetails struct {
	ContainerName string
	CreatedAt     time.Time
	Status        string
	ImageName     string
	Ports         string
	Logs          string
	LogsAvailable bool
	Domains       []ProxyDomain
}

// ProxyDomain is one request-path mapping currently represented in the
// managed proxy configuration.
type ProxyDomain struct {
	Name            string
	ApplicationName string
	RequestPath     string
	MappedService   string
	MappedPath      string
}

// BackupSchedule contains the persisted schedule and most recent backup
// status for one database service. BackupLocation is derived by the service
// layer from the configured backup root and is never user-editable.
type BackupSchedule struct {
	ServiceID        int64
	Enabled          bool
	ScheduleType     string
	Hour             int
	Minute           int
	Weekday          string
	RetentionDays    int
	BackupLocation   string
	LastBackupAt     time.Time
	LastBackupStatus string
	LastBackupSize   int64
}

// BackupScheduleInput contains the fields accepted by the schedule form.
type BackupScheduleInput struct {
	Enabled       bool
	ScheduleType  string
	Hour          int
	Minute        int
	Weekday       string
	RetentionDays int
}

// Backup is one completed database backup file.
type Backup struct {
	ID        int64
	ServiceID int64
	FileName  string
	CreatedAt time.Time
	SizeBytes int64
}

// BackupDetails is the backup dashboard data for one database service.
type BackupDetails struct {
	Schedule BackupSchedule
	Backups  []Backup
}

// IsDatabaseServiceType reports whether a service type supports database
// backups. PostgreSQL is the currently supported database implementation;
// the aliases keep metadata imported from older/manual Compose definitions
// compatible with the dashboard.
func IsDatabaseServiceType(serviceType string) bool {
	switch strings.ToLower(strings.TrimSpace(serviceType)) {
	case ServiceTypePostgreSQL, "postgres", "database", "db":
		return true
	default:
		return false
	}
}

// IsSensitiveEnvironmentKey reports whether an environment variable name
// commonly identifies a secret or credential. It is used to keep operational
// values out of rendered HTML by default.
func IsSensitiveEnvironmentKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	for _, marker := range []string{
		"password",
		"passwd",
		"secret",
		"token",
		"api_key",
		"apikey",
		"access_key",
		"private_key",
		"privatekey",
		"credential",
		"authorization",
		"database_url",
		"connection_string",
		"dsn",
	} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

// PostgreSQLServiceInput contains the user-supplied values needed to create a
// PostgreSQL service. The password is transient input and is never persisted
// in service metadata.
type PostgreSQLServiceInput struct {
	ServiceName      string
	PostgresVersion  string
	DatabaseName     string
	DatabaseUser     string
	DatabasePassword string
}

// RedisServiceInput contains the user-supplied values needed to create a
// Redis service. The password is transient input and is never persisted in
// service metadata.
type RedisServiceInput struct {
	ServiceName   string
	RedisVersion  string
	Port          string
	Password      string
	PersistToDisk bool
}

// ApplicationHealthcheck contains the optional healthcheck command and Compose
// timing settings for a custom application container.
type ApplicationHealthcheck struct {
	Command     string
	Interval    string
	Timeout     string
	Retries     string
	StartPeriod string
}

// ApplicationServiceDependency maps a custom application container to one of
// the application's existing services.
type ApplicationServiceDependency struct {
	ServiceName string
	Condition   string
}

// ApplicationPortMapping publishes one container port on the host.
type ApplicationPortMapping struct {
	HostPort      string
	ContainerPort string
	Protocol      string
}

// ApplicationVolumeMapping mounts a named volume or an application-relative
// host path at a container path.
type ApplicationVolumeMapping struct {
	Source  string
	Target  string
	Options string
}

// ApplicationServiceInput contains the user-supplied values needed to create
// a custom application container.
type ApplicationServiceInput struct {
	ServiceName    string
	ImageName      string
	AutoStart      bool
	Entrypoint     string
	Healthcheck    ApplicationHealthcheck
	DependsOn      []ApplicationServiceDependency
	RestartPolicy  string
	PortMappings   []ApplicationPortMapping
	VolumeMappings []ApplicationVolumeMapping
}

// ValidateName trims and validates a human-readable application name.
func ValidateName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", ErrNameRequired
	}
	if utf8.RuneCountInString(name) > MaxNameLength {
		return "", ErrNameTooLong
	}
	if containsControlCharacter(name) {
		return "", ErrNameInvalid
	}
	return name, nil
}

// ValidateEmail trims and validates one email address for the local
// authorization allowlist. Google returns email addresses without display
// names, so display-name forms are intentionally rejected here.
func ValidateEmail(value string) (string, error) {
	email := strings.TrimSpace(value)
	if email == "" {
		return "", ErrEmailRequired
	}
	if utf8.RuneCountInString(email) > MaxEmailLength {
		return "", ErrEmailTooLong
	}
	if containsControlCharacter(email) {
		return "", ErrEmailInvalid
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || parsed.Name != "" {
		return "", ErrEmailInvalid
	}
	return strings.ToLower(email), nil
}

// ValidateFolderName trims and validates one directory name. It deliberately
// accepts only a single path segment so callers can safely join it below the
// managed applications directory.
func ValidateFolderName(value string) (string, error) {
	folderName := strings.TrimSpace(value)
	if folderName == "" {
		return "", ErrFolderNameRequired
	}
	if utf8.RuneCountInString(folderName) > MaxFolderNameLength {
		return "", ErrFolderNameTooLong
	}
	if folderName == "." || folderName == ".." || containsControlCharacter(folderName) {
		return "", ErrFolderNameInvalid
	}
	if strings.ContainsAny(folderName, `/\\`) {
		return "", ErrFolderNameInvalid
	}
	return folderName, nil
}

// ValidateServiceName validates a Compose service name.
func ValidateServiceName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", ErrServiceNameRequired
	}
	if utf8.RuneCountInString(name) > MaxServiceNameLength {
		return "", ErrServiceNameTooLong
	}
	for index, character := range name {
		if index == 0 {
			if !isASCIIAlphaNumeric(character) {
				return "", ErrServiceNameInvalid
			}
			continue
		}
		if !isASCIIAlphaNumeric(character) && character != '_' && character != '-' && character != '.' {
			return "", ErrServiceNameInvalid
		}
	}
	return name, nil
}

// DefaultApplicationImageName returns the image reference used for a custom
// application container when the user does not provide one. The local
// registry is exposed by the server setup at localhost:5000.
func DefaultApplicationImageName(serviceName string) string {
	serviceName, err := ValidateServiceName(serviceName)
	if err != nil {
		serviceName = "app"
	}
	return LocalRegistryAddress + "/" + strings.ToLower(serviceName) + ":latest"
}

// ValidateImageName validates a Docker image reference before it is written to
// a Compose file. It accepts common registry, repository, tag, and digest
// forms while excluding syntax that could alter generated YAML.
func ValidateImageName(value string) (string, error) {
	image := strings.TrimSpace(value)
	if image == "" {
		return "", ErrImageNameRequired
	}
	if utf8.RuneCountInString(image) > MaxImageNameLength {
		return "", ErrImageNameTooLong
	}
	if containsControlCharacter(image) || strings.IndexFunc(image, unicode.IsSpace) >= 0 {
		return "", ErrImageNameInvalid
	}
	if strings.ContainsAny(image, "\"'\\$;{}#") || strings.Contains(image, "://") {
		return "", ErrImageNameInvalid
	}

	name := image
	if digestIndex := strings.IndexByte(image, '@'); digestIndex >= 0 {
		if strings.IndexByte(image[digestIndex+1:], '@') >= 0 || !validImageDigest(image[digestIndex+1:]) {
			return "", ErrImageNameInvalid
		}
		name = image[:digestIndex]
	}

	lastSlash := strings.LastIndexByte(name, '/')
	lastColon := strings.LastIndexByte(name, ':')
	if lastColon > lastSlash {
		tag := name[lastColon+1:]
		if !validImageTag(tag) {
			return "", ErrImageNameInvalid
		}
		name = name[:lastColon]
	}
	if !validImageRepositoryName(name) {
		return "", ErrImageNameInvalid
	}
	return image, nil
}

func validImageRepositoryName(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) == 0 {
		return false
	}
	for index, part := range parts {
		if index == 0 && len(parts) > 1 && looksLikeImageRegistry(part) {
			if !validImageRegistry(part) {
				return false
			}
			continue
		}
		if !validImagePathComponent(part) {
			return false
		}
	}
	return true
}

func looksLikeImageRegistry(value string) bool {
	return value == "localhost" || strings.ContainsAny(value, ".:")
}

func validImageRegistry(value string) bool {
	host := value
	if colon := strings.LastIndexByte(value, ':'); colon >= 0 {
		port := value[colon+1:]
		if !validImagePort(port) {
			return false
		}
		host = value[:colon]
	}
	if host == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !isASCIILowerAlphaNumeric(character) && character != '-' {
				return false
			}
		}
	}
	return true
}

func validImagePort(value string) bool {
	if value == "" || len(value) > 5 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	port, err := strconv.Atoi(value)
	return err == nil && port >= 1 && port <= 65535
}

func validImagePathComponent(value string) bool {
	if value == "" || !isASCIIAlphaNumeric(rune(value[0])) || !isASCIIAlphaNumeric(rune(value[len(value)-1])) {
		return false
	}
	for _, character := range value {
		if !isASCIILowerAlphaNumeric(character) && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func validImageTag(value string) bool {
	if value == "" || len(value) > 128 || !isASCIIAlphaNumeric(rune(value[0])) && value[0] != '_' {
		return false
	}
	for _, character := range value {
		if !isASCIIAlphaNumeric(character) && character != '_' && character != '.' && character != '-' {
			return false
		}
	}
	return true
}

func validImageDigest(value string) bool {
	algorithm, encoded, ok := strings.Cut(value, ":")
	if !ok || algorithm == "" || encoded == "" || len(encoded) < 32 {
		return false
	}
	for index, character := range algorithm {
		if index == 0 && !isASCIIAlpha(character) {
			return false
		}
		if !isASCIIAlphaNumeric(character) && character != '+' && character != '.' && character != '-' {
			return false
		}
	}
	for _, character := range encoded {
		if !isASCIIHex(character) {
			return false
		}
	}
	return true
}

// ValidateDomainName trims, normalizes, and validates a DNS-style domain
// name. The ASCII label rules keep the stored value safe to use in generated
// configuration while still accepting internationalized names in their
// standard punycode form.
func ValidateDomainName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", ErrDomainNameRequired
	}
	if len(name) > MaxDomainNameLength {
		return "", ErrDomainNameTooLong
	}
	if containsControlCharacter(name) {
		return "", ErrDomainNameInvalid
	}

	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrDomainNameInvalid
		}
		for _, character := range label {
			if !isASCIIAlphaNumeric(character) && character != '-' {
				return "", ErrDomainNameInvalid
			}
		}
	}
	return strings.ToLower(name), nil
}

// ValidateRedlaunchPublicAccess normalizes the optional public hostname and
// requires it whenever public access is enabled.
func ValidateRedlaunchPublicAccess(input RedlaunchPublicAccessInput) (RedlaunchPublicAccess, error) {
	domain := strings.TrimSpace(input.Domain)
	if domain == "" {
		if input.Enabled {
			return RedlaunchPublicAccess{}, ErrRedlaunchPublicDomainRequired
		}
		return RedlaunchPublicAccess{}, nil
	}

	normalized, err := ValidateDomainName(domain)
	if err != nil {
		switch {
		case errors.Is(err, ErrDomainNameTooLong):
			return RedlaunchPublicAccess{}, ErrRedlaunchPublicDomainTooLong
		default:
			return RedlaunchPublicAccess{}, ErrRedlaunchPublicDomainInvalid
		}
	}
	return RedlaunchPublicAccess{Enabled: input.Enabled, Domain: normalized}, nil
}

// ValidateRoutingSubdomain validates the optional labels prepended to an
// associated domain. Empty input means that the associated domain itself is
// the routing host.
func ValidateRoutingSubdomain(value string) (string, error) {
	subdomain := strings.TrimSpace(value)
	if subdomain == "" {
		return "", nil
	}
	if len(subdomain) > MaxRoutingSubdomainLength {
		return "", ErrRoutingSubdomainTooLong
	}
	if containsControlCharacter(subdomain) {
		return "", ErrRoutingSubdomainInvalid
	}
	for _, label := range strings.Split(subdomain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrRoutingSubdomainInvalid
		}
		for _, character := range label {
			if !isASCIIAlphaNumeric(character) && character != '-' {
				return "", ErrRoutingSubdomainInvalid
			}
		}
	}
	return strings.ToLower(subdomain), nil
}

// ValidateRoutingPath validates one Caddy path matcher or rewrite target.
// Paths are intentionally kept as plain text values, while excluding syntax
// that could escape the generated Caddyfile or make an HTTP path ambiguous.
func ValidateRoutingPath(value string) (string, error) {
	path := strings.TrimSpace(value)
	if path == "" {
		return "", ErrRoutingPathRequired
	}
	if len(path) > MaxRoutingPathLength {
		return "", ErrRoutingPathTooLong
	}
	if !strings.HasPrefix(path, "/") || containsControlCharacter(path) || strings.ContainsAny(path, "\\\"{}#") || strings.IndexFunc(path, unicode.IsSpace) >= 0 {
		return "", ErrRoutingPathInvalid
	}
	return path, nil
}

// ValidateRoutingPort validates the container port used by the reverse proxy.
// A zero value preserves the behavior of routing records created before the
// explicit port field was introduced.
func ValidateRoutingPort(value int) (int, error) {
	if value == 0 {
		return 80, nil
	}
	if value < 1 || value > 65535 {
		return 0, ErrRoutingPortInvalid
	}
	return value, nil
}

// ValidateEnvironmentVariableName validates a POSIX-style environment
// variable name.
func ValidateEnvironmentVariableName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", ErrEnvironmentVariableNameRequired
	}
	for index, character := range name {
		if index == 0 {
			if character != '_' && !isASCIIAlpha(character) {
				return "", ErrEnvironmentVariableNameInvalid
			}
			continue
		}
		if character != '_' && !isASCIIAlphaNumeric(character) {
			return "", ErrEnvironmentVariableNameInvalid
		}
	}
	return name, nil
}

// ValidateEnvironmentVariableValue rejects control characters that would
// make a single environment-file entry ambiguous or unsafe to write.
func ValidateEnvironmentVariableValue(value string) (string, error) {
	if containsControlCharacter(value) {
		return "", ErrEnvironmentVariableValueInvalid
	}
	return value, nil
}

// ValidatePostgresVersion validates the tag used for the official PostgreSQL
// image. Keeping it to Docker tag characters prevents YAML or image-reference
// injection through the form.
func ValidatePostgresVersion(value string) (string, error) {
	version := strings.TrimSpace(value)
	if version == "" {
		return "", ErrPostgresVersionRequired
	}
	if utf8.RuneCountInString(version) > MaxPostgresVersionLength {
		return "", ErrPostgresVersionTooLong
	}
	for index, character := range version {
		if index == 0 && !isASCIIAlphaNumeric(character) {
			return "", ErrPostgresVersionInvalid
		}
		if !isASCIIAlphaNumeric(character) && character != '_' && character != '-' && character != '.' {
			return "", ErrPostgresVersionInvalid
		}
	}
	return version, nil
}

// ValidateRedisVersion validates the tag used for the official Redis image.
// Keeping it to Docker tag characters prevents YAML or image-reference
// injection through the form.
func ValidateRedisVersion(value string) (string, error) {
	version := strings.TrimSpace(value)
	if version == "" {
		return "", ErrRedisVersionRequired
	}
	if utf8.RuneCountInString(version) > MaxRedisVersionLength {
		return "", ErrRedisVersionTooLong
	}
	for index, character := range version {
		if index == 0 && !isASCIIAlphaNumeric(character) {
			return "", ErrRedisVersionInvalid
		}
		if !isASCIIAlphaNumeric(character) && character != '_' && character != '-' && character != '.' {
			return "", ErrRedisVersionInvalid
		}
	}
	return version, nil
}

// ValidateRedisPort validates a TCP port and returns its canonical decimal
// representation for use in Compose configuration.
func ValidateRedisPort(value string) (string, error) {
	port := strings.TrimSpace(value)
	if port == "" {
		return "", ErrRedisPortRequired
	}
	if len(port) > MaxRedisPortLength {
		return "", ErrRedisPortTooLong
	}
	for _, character := range port {
		if character < '0' || character > '9' {
			return "", ErrRedisPortInvalid
		}
	}
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed < 1 || parsed > 65535 {
		return "", ErrRedisPortInvalid
	}
	return strconv.Itoa(parsed), nil
}

// ValidateRedisPassword validates an optional Redis password.
func ValidateRedisPassword(value string) (string, error) {
	if len(value) > MaxRedisPasswordLength {
		return "", ErrRedisPasswordTooLong
	}
	if containsControlCharacter(value) {
		return "", ErrRedisPasswordInvalid
	}
	return value, nil
}

// ValidateDatabaseName validates a PostgreSQL database name while allowing
// spaces so the application name can be used as the default. The restricted
// character set also keeps the value safe for the official image's database
// initialization SQL.
func ValidateDatabaseName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", ErrDatabaseNameRequired
	}
	if len(name) > MaxDatabaseNameLength {
		return "", ErrDatabaseNameTooLong
	}
	if containsControlCharacter(name) {
		return "", ErrDatabaseNameInvalid
	}
	for _, character := range name {
		if unicode.IsLetter(character) || unicode.IsNumber(character) || strings.ContainsRune(" _.-", character) {
			continue
		}
		return "", ErrDatabaseNameInvalid
	}
	return name, nil
}

// ValidateDatabaseUser validates the PostgreSQL role name used by the image.
func ValidateDatabaseUser(value string) (string, error) {
	user := strings.TrimSpace(value)
	if user == "" {
		return "", ErrDatabaseUserRequired
	}
	if len(user) > MaxDatabaseUserLength {
		return "", ErrDatabaseUserTooLong
	}
	for index, character := range user {
		if index == 0 {
			if !isASCIIAlpha(character) && character != '_' {
				return "", ErrDatabaseUserInvalid
			}
			continue
		}
		if !isASCIIAlphaNumeric(character) && character != '_' {
			return "", ErrDatabaseUserInvalid
		}
	}
	return user, nil
}

// ValidateDatabasePassword validates a supplied password. An empty password
// is valid and tells the service layer to generate one.
func ValidateDatabasePassword(value string) (string, error) {
	if len(value) > MaxDatabasePasswordLength {
		return "", ErrDatabasePasswordTooLong
	}
	if containsControlCharacter(value) {
		return "", ErrDatabasePasswordInvalid
	}
	return value, nil
}

// ValidateBackupScheduleInput validates a schedule independently from HTTP
// or systemd. Hourly schedules run at the selected minute of every hour;
// daily schedules run at hour and minute every day; weekly schedules also
// require a weekday.
func ValidateBackupScheduleInput(input BackupScheduleInput) (BackupScheduleInput, error) {
	scheduleType := strings.ToLower(strings.TrimSpace(input.ScheduleType))
	if scheduleType != BackupScheduleHourly && scheduleType != BackupScheduleDaily && scheduleType != BackupScheduleWeekly {
		return BackupScheduleInput{}, ErrBackupScheduleTypeInvalid
	}
	if input.Minute < 0 || input.Minute > 59 {
		return BackupScheduleInput{}, ErrBackupMinuteInvalid
	}
	if scheduleType != BackupScheduleHourly && (input.Hour < 0 || input.Hour > 23) {
		return BackupScheduleInput{}, ErrBackupHourInvalid
	}
	if input.RetentionDays < 1 || input.RetentionDays > MaxBackupRetentionDays {
		return BackupScheduleInput{}, ErrBackupRetentionInvalid
	}

	weekday := strings.ToLower(strings.TrimSpace(input.Weekday))
	if scheduleType == BackupScheduleWeekly {
		if !validBackupWeekday(weekday) {
			return BackupScheduleInput{}, ErrBackupWeekdayInvalid
		}
	} else {
		weekday = ""
	}
	if scheduleType == BackupScheduleHourly {
		input.Hour = 0
	}
	input.ScheduleType = scheduleType
	input.Weekday = weekday
	return input, nil
}

// ValidateBackupFileName restricts restore, download, and delete requests to
// files produced by the backup service. In particular, it rejects path
// separators and traversal.
func ValidateBackupFileName(value string) (string, error) {
	name := value
	if name == "" || utf8.RuneCountInString(name) > MaxBackupFileNameLength || strings.TrimSpace(name) != name || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) || !strings.HasPrefix(name, "backup-") || !strings.HasSuffix(name, ".sql") {
		return "", ErrBackupFileNameInvalid
	}
	for _, character := range name {
		if !isASCIIAlphaNumeric(character) && character != '-' && character != '.' {
			return "", ErrBackupFileNameInvalid
		}
	}
	return name, nil
}

func validBackupWeekday(value string) bool {
	switch value {
	case "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday":
		return true
	default:
		return false
	}
}

// BackupWeekdays returns the weekday values accepted by weekly schedules in
// their display order.
func BackupWeekdays() []string {
	return []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
}

func isASCIIAlpha(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func isASCIIAlphaNumeric(character rune) bool {
	return isASCIIAlpha(character) || character >= '0' && character <= '9'
}

func isASCIILowerAlphaNumeric(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

func isASCIIHex(character rune) bool {
	return character >= '0' && character <= '9' || character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F'
}

func containsControlCharacter(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
