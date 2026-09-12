package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

const applicationCompose = `services: {}

networks:
  default:
    external: true
    name: redlaunch-common
`

// ApplicationRepository is the persistence capability required by the
// application use cases.
type ApplicationRepository interface {
	List(context.Context) ([]application.Application, error)
	Create(context.Context, application.Application) (application.Application, error)
}

type applicationDetailsRepository interface {
	Get(context.Context, int64) (application.Application, error)
	ListServices(context.Context, int64) ([]application.Service, error)
}

type applicationDomainRepository interface {
	ListDomains(context.Context, int64) ([]application.Domain, error)
	GetDomain(context.Context, int64, int64) (application.Domain, error)
	CreateDomain(context.Context, application.Domain) (application.Domain, error)
	DeleteDomain(context.Context, int64, string) error
}

type applicationRoutingRepository interface {
	ListRoutings(context.Context, int64, int64) ([]application.Routing, error)
	ListAllRoutings(context.Context) ([]application.Routing, error)
	GetRouting(context.Context, int64, int64, int64) (application.Routing, error)
	CreateRouting(context.Context, application.Routing) (application.Routing, error)
	UpdateRouting(context.Context, application.Routing) error
	DeleteRouting(context.Context, int64, int64, int64) error
}

type redlaunchSettingsRepository interface {
	ListRedlaunchDomains(context.Context) ([]application.RedlaunchDomain, error)
	CreateRedlaunchDomain(context.Context, application.RedlaunchDomain) (application.RedlaunchDomain, error)
	DeleteRedlaunchDomain(context.Context, string) error
}

type proxyReloader interface {
	ReloadProxy(context.Context, string) error
}

// GetEnvironmentFiles reads the two managed environment files belonging to an
// application without making filesystem access part of the HTTP layer.
func (s *Applications) GetEnvironmentFiles(ctx context.Context, applicationID int64) (application.EnvironmentFiles, error) {
	if s.detailsRepository == nil {
		return application.EnvironmentFiles{}, errors.New("application details repository is not configured")
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return application.EnvironmentFiles{}, err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return application.EnvironmentFiles{}, err
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return application.EnvironmentFiles{}, err
	}

	variables, variablesAvailable, err := readEnvironmentFile(filepath.Join(directory, varsEnvFile), false)
	if err != nil {
		return application.EnvironmentFiles{}, fmt.Errorf("read application variables file: %w", err)
	}
	secrets, secretsAvailable, err := readEnvironmentFile(filepath.Join(directory, secretsEnvFile), true)
	if err != nil {
		return application.EnvironmentFiles{}, fmt.Errorf("read application secrets file: %w", err)
	}
	return application.EnvironmentFiles{
		Variables:          variables,
		Secrets:            secrets,
		VariablesAvailable: variablesAvailable,
		SecretsAvailable:   secretsAvailable,
	}, nil
}

// UpdateEnvironmentVariable changes one entry in an application's vars.env
// file while preserving the rest of the file and its permissions.
func (s *Applications) UpdateEnvironmentVariable(ctx context.Context, applicationID int64, originalName, name, value string) error {
	originalName, err := application.ValidateEnvironmentVariableName(originalName)
	if err != nil {
		return err
	}
	name, err = application.ValidateEnvironmentVariableName(name)
	if err != nil {
		return err
	}
	value, err = application.ValidateEnvironmentVariableValue(value)
	if err != nil {
		return err
	}
	if s.detailsRepository == nil {
		return errors.New("application details repository is not configured")
	}
	return s.modifyApplicationEnvironmentFile(ctx, applicationID, varsEnvFile, "variables", func(contents string) (string, error) {
		return updateEnvironmentFile(contents, originalName, name, value)
	})
}

// AddEnvironmentVariable appends a new entry to an application's vars.env
// file while preserving the rest of the file and its permissions.
func (s *Applications) AddEnvironmentVariable(ctx context.Context, applicationID int64, name, value string) error {
	name, err := application.ValidateEnvironmentVariableName(name)
	if err != nil {
		return err
	}
	value, err = application.ValidateEnvironmentVariableValue(value)
	if err != nil {
		return err
	}
	if s.detailsRepository == nil {
		return errors.New("application details repository is not configured")
	}
	return s.modifyApplicationEnvironmentFile(ctx, applicationID, varsEnvFile, "variables", func(contents string) (string, error) {
		return appendEnvironmentVariable(contents, name, value)
	})
}

// DeleteEnvironmentVariable removes one entry from an application's vars.env
// file while preserving the rest of the file and its permissions.
func (s *Applications) DeleteEnvironmentVariable(ctx context.Context, applicationID int64, name string) error {
	name, err := application.ValidateEnvironmentVariableName(name)
	if err != nil {
		return err
	}
	if s.detailsRepository == nil {
		return errors.New("application details repository is not configured")
	}
	return s.modifyApplicationEnvironmentFile(ctx, applicationID, varsEnvFile, "variables", func(contents string) (string, error) {
		return deleteEnvironmentVariable(contents, name)
	})
}

// MoveEnvironmentVariableToSecrets moves one entry from an application's
// vars.env file to its secrets.env file while preserving both files' other
// contents and permissions.
func (s *Applications) MoveEnvironmentVariableToSecrets(ctx context.Context, applicationID int64, name string) error {
	return s.moveApplicationEnvironmentVariable(ctx, applicationID, name, varsEnvFile, "variables", secretsEnvFile, "secrets")
}

// MoveEnvironmentSecretToVariables moves one entry from an application's
// secrets.env file to its vars.env file while preserving both files' other
// contents and permissions.
func (s *Applications) MoveEnvironmentSecretToVariables(ctx context.Context, applicationID int64, name string) error {
	return s.moveApplicationEnvironmentVariable(ctx, applicationID, name, secretsEnvFile, "secrets", varsEnvFile, "variables")
}

func (s *Applications) moveApplicationEnvironmentVariable(ctx context.Context, applicationID int64, name, sourceFile, sourceLabel, destinationFile, destinationLabel string) error {
	name, err := application.ValidateEnvironmentVariableName(name)
	if err != nil {
		return err
	}
	if s.detailsRepository == nil {
		return errors.New("application details repository is not configured")
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return err
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return err
	}

	sourcePath := filepath.Join(directory, sourceFile)
	sourceSnapshot, err := snapshotManagedFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read application %s file: %w", sourceLabel, err)
	}
	if !sourceSnapshot.exists {
		return application.ErrEnvironmentFileNotFound
	}

	destinationPath := filepath.Join(directory, destinationFile)
	destinationSnapshot, err := snapshotManagedFile(destinationPath)
	if err != nil {
		return fmt.Errorf("read application %s file: %w", destinationLabel, err)
	}
	if !destinationSnapshot.exists {
		return application.ErrEnvironmentFileNotFound
	}

	entry, err := findEnvironmentEntry(string(sourceSnapshot.contents), name)
	if err != nil {
		return err
	}
	if _, err := application.ValidateEnvironmentVariableValue(entry.value); err != nil {
		return err
	}
	updatedSource, err := deleteEnvironmentVariable(string(sourceSnapshot.contents), name)
	if err != nil {
		return err
	}
	updatedDestination, err := appendRawEnvironmentVariable(string(destinationSnapshot.contents), name, entry.rawValueWithComment)
	if err != nil {
		return err
	}

	rollbackFiles := func() error {
		return errors.Join(restoreManagedFile(sourceSnapshot), restoreManagedFile(destinationSnapshot))
	}
	if err := writeManagedFile(sourcePath, updatedSource, sourceSnapshot.mode); err != nil {
		return errors.Join(fmt.Errorf("write application %s file: %w", sourceLabel, err), rollbackFiles())
	}
	if err := writeManagedFile(destinationPath, updatedDestination, destinationSnapshot.mode); err != nil {
		return errors.Join(fmt.Errorf("write application %s file: %w", destinationLabel, err), rollbackFiles())
	}
	return nil
}

// UpdateEnvironmentSecret changes one entry in an application's secrets.env
// file while preserving the rest of the file and its permissions. It is kept
// as the replace-value compatibility form for non-HTTP callers.
func (s *Applications) UpdateEnvironmentSecret(ctx context.Context, applicationID int64, originalName, name, value string) error {
	return s.UpdateEnvironmentSecretValue(ctx, applicationID, originalName, name, value, true)
}

// UpdateEnvironmentSecretValue renames a secret and optionally replaces its
// value. When replaceValue is false, the existing value token is preserved;
// this is the explicit unchanged-value path used by the web form.
func (s *Applications) UpdateEnvironmentSecretValue(ctx context.Context, applicationID int64, originalName, name, value string, replaceValue bool) error {
	originalName, err := application.ValidateEnvironmentVariableName(originalName)
	if err != nil {
		return err
	}
	name, err = application.ValidateEnvironmentVariableName(name)
	if err != nil {
		return err
	}
	if replaceValue {
		value, err = application.ValidateEnvironmentVariableValue(value)
		if err != nil {
			return err
		}
	}
	if s.detailsRepository == nil {
		return errors.New("application details repository is not configured")
	}
	return s.modifyApplicationEnvironmentFile(ctx, applicationID, secretsEnvFile, "secrets", func(contents string) (string, error) {
		if !replaceValue {
			return renameEnvironmentFile(contents, originalName, name)
		}
		return replaceEnvironmentFile(contents, originalName, name, value)
	})
}

// AddEnvironmentSecret appends a new entry to an application's secrets.env
// file while preserving the rest of the file and its permissions.
func (s *Applications) AddEnvironmentSecret(ctx context.Context, applicationID int64, name, value string) error {
	name, err := application.ValidateEnvironmentVariableName(name)
	if err != nil {
		return err
	}
	value, err = application.ValidateEnvironmentVariableValue(value)
	if err != nil {
		return err
	}
	if s.detailsRepository == nil {
		return errors.New("application details repository is not configured")
	}
	return s.modifyApplicationEnvironmentFile(ctx, applicationID, secretsEnvFile, "secrets", func(contents string) (string, error) {
		return appendEnvironmentVariable(contents, name, value)
	})
}

// DeleteEnvironmentSecret removes one entry from an application's secrets.env
// file while preserving the rest of the file and its permissions.
func (s *Applications) DeleteEnvironmentSecret(ctx context.Context, applicationID int64, name string) error {
	name, err := application.ValidateEnvironmentVariableName(name)
	if err != nil {
		return err
	}
	if s.detailsRepository == nil {
		return errors.New("application details repository is not configured")
	}
	return s.modifyApplicationEnvironmentFile(ctx, applicationID, secretsEnvFile, "secrets", func(contents string) (string, error) {
		return deleteEnvironmentVariable(contents, name)
	})
}

func (s *Applications) modifyApplicationEnvironmentFile(ctx context.Context, applicationID int64, fileName, fileLabel string, modify func(string) (string, error)) error {
	if fileName != varsEnvFile && fileName != secretsEnvFile {
		return errors.New("unsupported application environment file")
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return err
	}
	defer lease.release()
	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return err
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return err
	}

	path := filepath.Join(directory, fileName)
	snapshot, err := snapshotManagedFile(path)
	if err != nil {
		return fmt.Errorf("read application %s file: %w", fileLabel, err)
	}
	if !snapshot.exists {
		return application.ErrEnvironmentFileNotFound
	}
	updated, err := modify(string(snapshot.contents))
	if err != nil {
		return err
	}
	if err := writeManagedFile(path, updated, snapshot.mode); err != nil {
		return fmt.Errorf("write application %s file: %w", fileLabel, err)
	}
	return nil
}

type applicationServiceRepository interface {
	CreateService(context.Context, application.Service) (application.Service, error)
}

type applicationServiceBatchRepository interface {
	CreateServices(context.Context, []application.Service) ([]application.Service, error)
}

type applicationServiceDeletionRepository interface {
	DeleteService(context.Context, int64, string) error
}

type applicationDeletionRepository interface {
	DeleteApplication(context.Context, int64) error
}

type applicationDeletionIntentRepository interface {
	BeginApplicationDeletion(context.Context, application.Application, time.Time) (application.ApplicationDeletionIntent, error)
	GetApplicationDeletion(context.Context, int64) (application.ApplicationDeletionIntent, error)
	UpdateApplicationDeletion(context.Context, int64, string, string, string, time.Time) error
}

type applicationFolderDeletionReservation interface {
	IsApplicationFolderDeletionActive(context.Context, string) (bool, error)
}

type applicationDeletionStateChecker interface {
	IsApplicationDeletionActive(context.Context, int64) (bool, error)
}

type applicationBackupLeaseRepository interface {
	AcquireBackupLease(context.Context, int64, string, string, time.Time, time.Time) error
	ReleaseBackupLease(context.Context, int64, string) error
}

type applicationDeletionScheduleDisabler interface {
	DisableApplicationSchedules(context.Context, int64) error
}

// applicationServiceScheduleDisabler disables a single service's backup timer.
// BackupService implements it; the narrow application-level capability above
// stays for application deletion.
type applicationServiceScheduleDisabler interface {
	DisableServiceBackupSchedule(context.Context, int64, int64) error
}

type serviceDeletionIntentRepository interface {
	BeginServiceDeletion(context.Context, int64, string, time.Time) (application.ServiceDeletionIntent, error)
	GetServiceDeletion(context.Context, int64, string) (application.ServiceDeletionIntent, error)
	UpdateServiceDeletion(context.Context, int64, string, string, string, string, time.Time) error
}

type applicationDeletionCleanup interface {
	CleanupApplicationKey(context.Context, int64) error
}

type composeServiceInspector interface {
	ListServices(context.Context, string) ([]compose.ServiceRuntime, error)
}

type composeServiceLogsInspector interface {
	Logs(context.Context, string, string, int) (string, error)
}

type composeServiceFullLogsInspector interface {
	AllLogs(context.Context, string, string) (string, error)
}

type composeServiceLogStreamer interface {
	OpenLogs(context.Context, string, string) (io.ReadCloser, error)
}

type composeServiceController interface {
	Start(context.Context, string, string) error
	Stop(context.Context, string, string) error
	Restart(context.Context, string, string) error
}

type composeServiceStarter interface {
	UpService(context.Context, string, string) error
}

type composeServiceRemover interface {
	Remove(context.Context, string, string) error
}

type composeProjectRemover interface {
	Down(context.Context, string) error
}

// ApplicationsOptions contains deployment values that affect generated
// configuration. The HTTP address is used only to derive the port Caddy uses
// when proxying the management interface through the host gateway.
type ApplicationsOptions struct {
	ManagementHTTPAddr string
}

// Applications coordinates application metadata and its managed files.
type Applications struct {
	repository             ApplicationRepository
	detailsRepository      applicationDetailsRepository
	serviceRepository      applicationServiceRepository
	domainRepository       applicationDomainRepository
	routingRepository      applicationRoutingRepository
	settingsRepository     redlaunchSettingsRepository
	deletionIntents        applicationDeletionIntentRepository
	serviceDeletionIntents serviceDeletionIntentRepository
	backupLeases           applicationBackupLeaseRepository
	scheduleDisabler       applicationDeletionScheduleDisabler
	keyCleanup             applicationDeletionCleanup
	applicationsDir        string
	proxyDirectory         string
	managementPort         int
	runner                 composeRunner
	projectLocks           *projectLockManager
	mu                     sync.RWMutex
}

func (s *Applications) acquireApplicationProject(ctx context.Context, applicationID int64) (*projectLockLease, error) {
	if applicationID < 1 {
		return nil, application.ErrNotFound
	}
	s.mu.Lock()
	if s.projectLocks == nil {
		s.projectLocks = newProjectLockManager()
	}
	locks := s.projectLocks
	s.mu.Unlock()
	return locks.acquire(ctx, applicationProjectLockKey(applicationID))
}

func (s *Applications) acquireProjectKey(ctx context.Context, key string) (*projectLockLease, error) {
	s.mu.Lock()
	if s.projectLocks == nil {
		s.projectLocks = newProjectLockManager()
	}
	locks := s.projectLocks
	s.mu.Unlock()
	return locks.acquire(ctx, key)
}

func (s *Applications) acquireProxyProject(ctx context.Context) (*projectLockLease, error) {
	return s.acquireProjectKey(ctx, proxyProjectLockKey)
}

// SetApplicationDeletionDependencies supplies the optional infrastructure
// operations that must agree with a durable application deletion intent.
// Keeping them as narrow capabilities avoids coupling this service to the
// backup and GitHub Actions implementations.
func (s *Applications) SetApplicationDeletionDependencies(scheduleDisabler applicationDeletionScheduleDisabler, keyCleanup applicationDeletionCleanup) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduleDisabler = scheduleDisabler
	s.keyCleanup = keyCleanup
}

// ApplicationDeletionHandlesKeyCleanup reports whether the durable deletion
// workflow owns deployment-key cleanup. The HTTP compatibility path uses this
// to avoid running a second cleanup after the workflow has already completed.
func (s *Applications) ApplicationDeletionHandlesKeyCleanup() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.keyCleanup != nil
}

func (s *Applications) applicationDeletionDependencies() (applicationDeletionScheduleDisabler, applicationDeletionCleanup) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scheduleDisabler, s.keyCleanup
}

// GetApplicationDeletion exposes only the non-secret tombstone needed by the
// HTTP layer to offer a retry after metadata has already been removed.
func (s *Applications) GetApplicationDeletion(ctx context.Context, applicationID int64) (application.ApplicationDeletionIntent, error) {
	if s.deletionIntents == nil {
		return application.ApplicationDeletionIntent{}, application.ErrNotFound
	}
	return s.deletionIntents.GetApplicationDeletion(ctx, applicationID)
}

// NewApplications constructs an application service rooted below the
// configured projects directory.
func NewApplications(repository ApplicationRepository, projectsRoot string, runners ...composeRunner) (*Applications, error) {
	return NewApplicationsWithOptions(repository, projectsRoot, ApplicationsOptions{}, runners...)
}

// NewApplicationsWithOptions constructs the application service with explicit
// deployment settings while retaining the small legacy constructor for tests
// and callers that use the default management port.
func NewApplicationsWithOptions(repository ApplicationRepository, projectsRoot string, options ApplicationsOptions, runners ...composeRunner) (*Applications, error) {
	if repository == nil {
		return nil, errors.New("application repository is required")
	}
	root, err := filepath.Abs(projectsRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve projects root: %w", err)
	}
	root = filepath.Clean(root)
	if root == string(filepath.Separator) {
		return nil, errors.New("projects root must not be the filesystem root")
	}
	if err := ensureDirectory(root); err != nil {
		return nil, fmt.Errorf("ensure projects root: %w", err)
	}
	applicationsDirectory := filepath.Join(root, applicationsDir)
	if err := ensureDirectory(applicationsDirectory); err != nil {
		return nil, fmt.Errorf("ensure applications directory: %w", err)
	}
	detailsRepository, _ := repository.(applicationDetailsRepository)
	serviceRepository, _ := repository.(applicationServiceRepository)
	domainRepository, _ := repository.(applicationDomainRepository)
	routingRepository, _ := repository.(applicationRoutingRepository)
	settingsRepository, _ := repository.(redlaunchSettingsRepository)
	deletionIntents, _ := repository.(applicationDeletionIntentRepository)
	serviceDeletionIntents, _ := repository.(serviceDeletionIntentRepository)
	backupLeases, _ := repository.(applicationBackupLeaseRepository)
	runner := composeRunner(compose.CommandRunner{})
	if len(runners) > 0 && runners[0] != nil {
		runner = runners[0]
	}
	return &Applications{
		repository:             repository,
		detailsRepository:      detailsRepository,
		serviceRepository:      serviceRepository,
		domainRepository:       domainRepository,
		routingRepository:      routingRepository,
		settingsRepository:     settingsRepository,
		deletionIntents:        deletionIntents,
		serviceDeletionIntents: serviceDeletionIntents,
		backupLeases:           backupLeases,
		applicationsDir:        applicationsDirectory,
		proxyDirectory:         filepath.Join(root, coreDir, proxyDir),
		managementPort:         managementPortFromHTTPAddr(options.ManagementHTTPAddr),
		runner:                 runner,
		projectLocks:           newProjectLockManager(),
	}, nil
}

func managementPortFromHTTPAddr(address string) int {
	address = strings.TrimSpace(address)
	if address == "" {
		return 8080
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return 8080
	}
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed < 1 || parsed > 65535 {
		return 8080
	}
	return parsed
}

// startManagedService starts only the requested managed service when the
// Compose runner supports service-scoped startup. The project-wide fallback
// keeps the runner interface compatible with existing integrations.
func (s *Applications) startManagedService(ctx context.Context, directory, serviceName string) error {
	if starter, ok := s.runner.(composeServiceStarter); ok {
		return starter.UpService(ctx, directory, serviceName)
	}
	return s.runner.Up(ctx, directory)
}

// List returns all applications in creation order.
func (s *Applications) List(ctx context.Context) ([]application.Application, error) {
	return s.repository.List(ctx)
}

// Get returns one application by its database ID.
func (s *Applications) Get(ctx context.Context, id int64) (application.Application, error) {
	if s.detailsRepository == nil {
		return application.Application{}, errors.New("application details repository is not configured")
	}
	return s.detailsRepository.Get(ctx, id)
}

// ListDomains returns an application's associated domains in creation order.
func (s *Applications) ListDomains(ctx context.Context, applicationID int64) ([]application.Domain, error) {
	if s.domainRepository == nil {
		return nil, errors.New("application domain repository is not configured")
	}
	return s.domainRepository.ListDomains(ctx, applicationID)
}

// CreateDomain validates and persists one domain associated with an
// application.
func (s *Applications) CreateDomain(ctx context.Context, applicationID int64, name string) (application.Domain, error) {
	if s.detailsRepository == nil {
		return application.Domain{}, errors.New("application details repository is not configured")
	}
	if s.domainRepository == nil {
		return application.Domain{}, errors.New("application domain repository is not configured")
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return application.Domain{}, err
	}
	defer lease.release()
	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return application.Domain{}, err
	}
	name, err = application.ValidateDomainName(name)
	if err != nil {
		return application.Domain{}, err
	}
	return s.domainRepository.CreateDomain(ctx, application.Domain{
		ApplicationID: item.ID,
		Name:          name,
	})
}

// DeleteDomain removes one domain associated with an application.
func (s *Applications) DeleteDomain(ctx context.Context, applicationID int64, name string) error {
	if s.domainRepository == nil {
		return errors.New("application domain repository is not configured")
	}
	name, err := application.ValidateDomainName(name)
	if err != nil {
		return err
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return err
	}
	defer lease.release()
	if s.routingRepository == nil {
		return s.domainRepository.DeleteDomain(ctx, applicationID, name)
	}

	domains, err := s.domainRepository.ListDomains(ctx, applicationID)
	if err != nil {
		return err
	}
	var domain application.Domain
	for _, candidate := range domains {
		if candidate.Name == name {
			domain = candidate
			break
		}
	}
	if domain.ID == 0 {
		return application.ErrDomainNotFound
	}

	routings, err := s.routingRepository.ListRoutings(ctx, applicationID, domain.ID)
	if err != nil {
		return fmt.Errorf("list domain routings before deletion: %w", err)
	}
	if err := s.domainRepository.DeleteDomain(ctx, applicationID, name); err != nil {
		return err
	}
	if len(routings) == 0 {
		return nil
	}
	if err := s.refreshProxyConfiguration(ctx); err != nil {
		if rollbackErr := s.restoreDeletedDomain(ctx, domain, routings); rollbackErr != nil {
			return fmt.Errorf("apply Caddy domain configuration: %w (rollback domain: %v)", err, rollbackErr)
		}
		return fmt.Errorf("apply Caddy domain configuration: %w", err)
	}
	return nil
}

func (s *Applications) restoreDeletedDomain(ctx context.Context, domain application.Domain, routings []application.Routing) error {
	restored, err := s.domainRepository.CreateDomain(ctx, domain)
	if err != nil {
		return err
	}
	for _, item := range routings {
		item.DomainID = restored.ID
		item.DomainName = restored.Name
		if _, err := s.routingRepository.CreateRouting(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

// ListServices returns an application's service metadata in creation order and
// enriches it with best-effort Docker runtime details when available.
func (s *Applications) ListServices(ctx context.Context, applicationID int64) ([]application.Service, error) {
	if s.detailsRepository == nil {
		return nil, errors.New("application details repository is not configured")
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	defer lease.release()
	return s.listServicesLocked(ctx, applicationID)
}

func (s *Applications) listServicesLocked(ctx context.Context, applicationID int64) ([]application.Service, error) {
	services, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil || len(services) == 0 {
		return services, err
	}

	inspector, ok := s.runner.(composeServiceInspector)
	if !ok {
		return services, nil
	}
	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return services, nil
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return services, nil
	}
	runtimeServices, err := inspector.ListServices(ctx, directory)
	if err != nil {
		return services, nil
	}

	runtimeByName := make(map[string]compose.ServiceRuntime, len(runtimeServices))
	for _, runtimeService := range runtimeServices {
		if runtimeService.ServiceName != "" {
			runtimeByName[runtimeService.ServiceName] = runtimeService
		}
	}
	for index := range services {
		runtimeService, ok := runtimeByName[services[index].Name]
		if !ok {
			continue
		}
		services[index].ContainerName = runtimeService.ContainerName
		services[index].ContainerCreatedAt = runtimeService.CreatedAt
		services[index].Status = runtimeService.Status
		services[index].Ports = runtimeService.Ports
	}
	return services, nil
}

// GetProxyDetails returns best-effort Docker runtime information for the
// managed proxy and the request mappings represented by its persisted routing
// entries. Docker inspection failures leave the runtime fields unavailable so
// the dashboard can still show the configured domains.
func (s *Applications) GetProxyDetails(ctx context.Context) (application.ProxyDetails, error) {
	lease, err := s.acquireProxyProject(ctx)
	if err != nil {
		return application.ProxyDetails{}, err
	}
	defer lease.release()

	var details application.ProxyDetails

	if s.routingRepository != nil {
		routings, err := s.routingRepository.ListAllRoutings(ctx)
		if err != nil {
			return application.ProxyDetails{}, fmt.Errorf("list proxy domains: %w", err)
		}
		applications := make([]application.Application, 0)
		if len(routings) > 0 {
			applications, err = s.repository.List(ctx)
			if err != nil {
				return application.ProxyDetails{}, fmt.Errorf("list proxy applications: %w", err)
			}
		}
		applicationNames := make(map[int64]string, len(applications))
		for _, item := range applications {
			applicationNames[item.ID] = item.Name
		}
		details.Domains = proxyDomainsFromRoutings(routings, applicationNames)
	}

	inspector, ok := s.runner.(composeServiceInspector)
	if ok {
		runtimeServices, err := inspector.ListServices(ctx, s.proxyDirectory)
		if err == nil {
			for _, runtimeService := range runtimeServices {
				if runtimeService.ServiceName != proxyDir {
					continue
				}
				details.ContainerName = runtimeService.ContainerName
				details.CreatedAt = runtimeService.CreatedAt
				details.Status = runtimeService.Status
				details.ImageName = runtimeService.Image
				details.Ports = runtimeService.Ports
				break
			}
		}
	}
	if logsInspector, ok := s.runner.(composeServiceLogsInspector); ok {
		if logs, err := logsInspector.Logs(ctx, s.proxyDirectory, proxyDir, application.ProxyLogLineLimit); err == nil {
			details.Logs = logs
			details.LogsAvailable = true
		}
	}
	return details, nil
}

func proxyDomainsFromRoutings(routings []application.Routing, applicationNames map[int64]string) []application.ProxyDomain {
	domains := make([]application.ProxyDomain, 0, len(routings))
	for _, routing := range routings {
		host := strings.TrimSpace(routingHost(routing.DomainName, routing.Subdomain))
		if host == "" {
			continue
		}
		domains = append(domains, application.ProxyDomain{
			Name:            host,
			ApplicationName: applicationNames[routing.ApplicationID],
			RequestPath:     routing.Path,
			MappedService:   routing.ServiceName,
			MappedPath:      routing.ServicePath,
		})
	}
	return domains
}

// GetProxyFullLogs returns the managed proxy log history up to the Compose
// adapter's configured download limit. HTTP handlers use OpenProxyLogs
// directly so the response remains backpressured.
func (s *Applications) GetProxyFullLogs(ctx context.Context) (string, error) {
	stream, err := s.OpenProxyLogs(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	logs, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("read proxy logs: %w", err)
	}
	return string(logs), nil
}

// OpenProxyLogs opens a bounded, cancellable stream for the managed proxy.
// The legacy string inspector remains a compatibility fallback for small
// adapters; the production CommandRunner implements the streaming capability.
func (s *Applications) OpenProxyLogs(ctx context.Context) (io.ReadCloser, error) {
	if streamer, ok := s.runner.(composeServiceLogStreamer); ok {
		return streamer.OpenLogs(ctx, s.proxyDirectory, proxyDir)
	}
	if inspector, ok := s.runner.(composeServiceFullLogsInspector); ok {
		logs, err := inspector.AllLogs(ctx, s.proxyDirectory, proxyDir)
		if err != nil {
			return nil, fmt.Errorf("read proxy logs: %w", err)
		}
		return io.NopCloser(strings.NewReader(logs)), nil
	}
	return nil, errors.New("proxy log inspection is not configured")
}

// GetServiceDetails returns the registered service metadata together with
// best-effort Docker runtime and recent log information. Environment values
// are loaded only by the dedicated environment page, so sensitive Compose
// resolution is not part of this request.
func (s *Applications) GetServiceDetails(ctx context.Context, applicationID int64, serviceName string) (application.ServiceDetails, error) {
	if s.detailsRepository == nil {
		return application.ServiceDetails{}, errors.New("application details repository is not configured")
	}
	serviceName, err := application.ValidateServiceName(serviceName)
	if err != nil {
		return application.ServiceDetails{}, err
	}

	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return application.ServiceDetails{}, err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return application.ServiceDetails{}, err
	}
	services, err := s.listServicesLocked(ctx, applicationID)
	if err != nil {
		return application.ServiceDetails{}, err
	}

	var details application.ServiceDetails
	for _, service := range services {
		if service.Name == serviceName {
			details.Service = service
			break
		}
	}
	if details.Service.Name == "" {
		return application.ServiceDetails{}, application.ErrServiceNotFound
	}

	logsInspector, hasLogs := s.runner.(composeServiceLogsInspector)
	if !hasLogs {
		return details, nil
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return details, nil
	}

	if hasLogs {
		if logs, err := logsInspector.Logs(ctx, directory, serviceName, serviceLogTail); err == nil {
			details.Logs = logs
			details.LogsAvailable = true
		}
	}
	return details, nil
}

// GetServiceFullLogs returns the log history for one managed service up to the
// Compose adapter's configured download limit. HTTP handlers use
// OpenServiceLogs directly so the response remains backpressured.
func (s *Applications) GetServiceFullLogs(ctx context.Context, applicationID int64, serviceName string) (string, error) {
	stream, err := s.OpenServiceLogs(ctx, applicationID, serviceName)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	logs, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("read service logs: %w", err)
	}
	return string(logs), nil
}

// OpenServiceLogs validates ownership and opens a bounded, cancellable stream
// for one registered service.
func (s *Applications) OpenServiceLogs(ctx context.Context, applicationID int64, serviceName string) (io.ReadCloser, error) {
	if s.detailsRepository == nil {
		return nil, errors.New("application details repository is not configured")
	}
	serviceName, err := application.ValidateServiceName(serviceName)
	if err != nil {
		return nil, err
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	services, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	for _, service := range services {
		if service.Name != serviceName {
			continue
		}
		directory, err := s.managedApplicationDirectory(item)
		if err != nil {
			return nil, err
		}
		if streamer, ok := s.runner.(composeServiceLogStreamer); ok {
			return streamer.OpenLogs(ctx, directory, serviceName)
		}
		if inspector, ok := s.runner.(composeServiceFullLogsInspector); ok {
			logs, err := inspector.AllLogs(ctx, directory, serviceName)
			if err != nil {
				return nil, fmt.Errorf("read service logs: %w", err)
			}
			return io.NopCloser(strings.NewReader(logs)), nil
		}
		return nil, errors.New("service log inspection is not configured")
	}
	return nil, application.ErrServiceNotFound
}

const serviceLogTail = application.ServiceLogLineLimit

// StartProxy starts the managed proxy container.
func (s *Applications) StartProxy(ctx context.Context) error {
	return s.runProxyAction(ctx, "start", func(controller composeServiceController, directory, serviceName string) error {
		return controller.Start(ctx, directory, serviceName)
	})
}

// StopProxy stops the managed proxy container.
func (s *Applications) StopProxy(ctx context.Context) error {
	return s.runProxyAction(ctx, "stop", func(controller composeServiceController, directory, serviceName string) error {
		return controller.Stop(ctx, directory, serviceName)
	})
}

// RestartProxy restarts the managed proxy container.
func (s *Applications) RestartProxy(ctx context.Context) error {
	return s.runProxyAction(ctx, "restart", func(controller composeServiceController, directory, serviceName string) error {
		return controller.Restart(ctx, directory, serviceName)
	})
}

// StartService starts one registered service in an application.
func (s *Applications) StartService(ctx context.Context, applicationID int64, serviceName string) error {
	return s.runServiceAction(ctx, applicationID, serviceName, "start", func(controller composeServiceController, directory, name string) error {
		return controller.Start(ctx, directory, name)
	})
}

// StopService stops one registered service in an application.
func (s *Applications) StopService(ctx context.Context, applicationID int64, serviceName string) error {
	return s.runServiceAction(ctx, applicationID, serviceName, "stop", func(controller composeServiceController, directory, name string) error {
		return controller.Stop(ctx, directory, name)
	})
}

// RestartService restarts one registered service in an application.
func (s *Applications) RestartService(ctx context.Context, applicationID int64, serviceName string) error {
	return s.runServiceAction(ctx, applicationID, serviceName, "restart", func(controller composeServiceController, directory, name string) error {
		return controller.Restart(ctx, directory, name)
	})
}

// DeleteService stops and removes one registered service container, removes
// its Compose definition, and then deletes the service metadata that backs
// the application dashboard.
func (s *Applications) DeleteService(ctx context.Context, applicationID int64, serviceName string) error {
	return s.DeleteServiceWithProgress(ctx, applicationID, serviceName, nil)
}

// DeleteServiceWithProgress performs coordinated service deletion and reports
// the active workflow stage before each destructive operation. The callback is
// synchronous and may be nil.
//
// Deletion is durable when the repository records service deletion intents:
// the timer is disabled first so no new backup can start, the backup lease is
// held across the remaining stages so a running backup cannot be interrupted,
// and routing rows are removed with a Caddy reload. Backup files are retained
// as operator-managed artifacts while schedule and history records cascade
// with the service metadata.
func (s *Applications) DeleteServiceWithProgress(ctx context.Context, applicationID int64, serviceName string, progress func(stage, message string)) error {
	if s.detailsRepository == nil {
		return errors.New("application details repository is not configured")
	}
	serviceName, err := application.ValidateServiceName(serviceName)
	if err != nil {
		return err
	}
	if s.serviceRepository == nil {
		return errors.New("application service repository is not configured")
	}
	deleter, ok := s.serviceRepository.(applicationServiceDeletionRepository)
	if !ok {
		return errors.New("application service deletion repository is not configured")
	}
	controller, ok := s.runner.(composeServiceController)
	if !ok {
		return errors.New("compose service controller is not configured")
	}
	remover, ok := s.runner.(composeServiceRemover)
	if !ok {
		return errors.New("compose service remover is not configured")
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return fmt.Errorf("get application for service deletion: %w", err)
	}
	if err := s.ensureApplicationNotDeleting(ctx, applicationID); err != nil {
		return err
	}
	services, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return fmt.Errorf("list services for service deletion: %w", err)
	}
	var target application.Service
	registered := false
	for _, service := range services {
		if service.Name == serviceName {
			target = service
			registered = true
			break
		}
	}
	if s.serviceDeletionIntents == nil {
		if !registered {
			return application.ErrServiceNotFound
		}
		return s.deleteServiceWithoutIntent(ctx, item, serviceName, deleter, controller, remover, progress)
	}

	var intent application.ServiceDeletionIntent
	if !registered {
		intent, err = s.serviceDeletionIntents.GetServiceDeletion(ctx, applicationID, serviceName)
		if err != nil {
			return application.ErrServiceNotFound
		}
	} else {
		intent, err = s.serviceDeletionIntents.BeginServiceDeletion(ctx, applicationID, serviceName, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("record service deletion intent: %w", err)
		}
	}
	if intent.State == "complete" {
		return nil
	}

	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return fmt.Errorf("resolve application directory for service deletion: %w", err)
	}

	leases := &applicationBackupLeaseSet{repository: s.backupLeases}
	if registered && application.IsDatabaseServiceType(target.Type) && s.backupLeases != nil && target.ID >= 1 {
		token, err := newBackupLeaseToken()
		if err != nil {
			return s.failServiceDeletion(applicationID, serviceName, serviceDeletionStageSchedules, fmt.Errorf("create service deletion lease token: %w", err))
		}
		now := time.Now().UTC()
		if err := s.backupLeases.AcquireBackupLease(ctx, target.ID, "service-deletion", token, now, now.Add(backupLeaseDuration)); err != nil {
			return s.failServiceDeletion(applicationID, serviceName, serviceDeletionStageSchedules, err)
		}
		leases.leases = append(leases.leases, applicationBackupLease{serviceID: target.ID, token: token})
	}
	deletionErr := s.resumeServiceDeletion(ctx, item, target, registered, intent, directory, deleter, controller, remover, progress)
	if releaseErr := leases.release(); releaseErr != nil {
		leaseErr := fmt.Errorf("release service deletion leases: %w", releaseErr)
		if deletionErr != nil {
			return errors.Join(deletionErr, leaseErr)
		}
		return leaseErr
	}
	return deletionErr
}

const (
	serviceDeletionStageSchedules = "schedules"
	serviceDeletionStageStop      = "stop"
	serviceDeletionStageRemove    = "remove"
	serviceDeletionStageCompose   = "compose"
	serviceDeletionStageRouting   = "routing"
	serviceDeletionStageMetadata  = "metadata"
	serviceDeletionStageComplete  = "complete"
	serviceDeletionStateRunning   = "running"
	serviceDeletionStateFailed    = "failed"
)

// deleteServiceWithoutIntent performs service deletion without a durable
// checkpoint. It still disables nothing silently: callers without backup
// infrastructure skip timer/lease coordination, while routing rows are always
// removed with a Caddy reload and backup files are retained.
func (s *Applications) deleteServiceWithoutIntent(ctx context.Context, item application.Application, serviceName string, deleter applicationServiceDeletionRepository, controller composeServiceController, remover composeServiceRemover, progress func(stage, message string)) error {
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return fmt.Errorf("resolve application directory for service deletion: %w", err)
	}
	composePath, composeSnapshot, composeContents, composeChanged, err := s.stageServiceRemoval(directory, serviceName)
	if err != nil {
		return err
	}
	if composeChanged {
		if err := s.validateStagedCompose(ctx, directory); err != nil {
			return errors.Join(err, restoreManagedFile(composeSnapshot))
		}
		// Docker must still see the service definition while it stops and
		// removes the container. The validated edit is committed after those
		// service-scoped operations complete.
		if err := restoreManagedFile(composeSnapshot); err != nil {
			return fmt.Errorf("restore Compose file before service removal: %w", err)
		}
	}

	reportServiceDeletionProgress(progress, "stop", "Stopping the service container with Docker Compose")
	if err := controller.Stop(ctx, directory, serviceName); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}

	reportServiceDeletionProgress(progress, "remove", "Removing the service container with Docker Compose")
	if err := remover.Remove(ctx, directory, serviceName); err != nil {
		return fmt.Errorf("remove service container: %w", err)
	}

	reportServiceDeletionProgress(progress, "compose", "Removing the service from the Docker Compose file")
	if composeChanged {
		if err := writeManagedFile(composePath, composeContents, composeSnapshot.mode); err != nil {
			return fmt.Errorf("remove service from Compose file: %w", err)
		}
	}

	if err := s.removeServiceRoutings(ctx, item.ID, serviceName, progress); err != nil {
		return err
	}

	reportServiceDeletionProgress(progress, "metadata", "Deleting service metadata")
	if err := deleter.DeleteService(ctx, item.ID, serviceName); err != nil {
		if composeChanged {
			if restoreErr := restoreManagedFile(composeSnapshot); restoreErr != nil {
				return fmt.Errorf("delete service metadata: %w (restore Compose file: %v)", err, restoreErr)
			}
		}
		return fmt.Errorf("delete service metadata: %w", err)
	}
	return nil
}

// stageServiceRemoval reads the Compose file and returns the staged removal
// contents without mutating anything.
func (s *Applications) stageServiceRemoval(directory, serviceName string) (string, managedFileSnapshot, string, bool, error) {
	composePath, err := findApplicationComposeFile(directory)
	if err != nil {
		return "", managedFileSnapshot{}, "", false, fmt.Errorf("find application Compose file: %w", err)
	}
	var composeSnapshot managedFileSnapshot
	composeContents := ""
	composeChanged := false
	if composePath != "" {
		composeSnapshot, err = snapshotManagedFile(composePath)
		if err != nil {
			return "", managedFileSnapshot{}, "", false, fmt.Errorf("read application Compose file: %w", err)
		}
		composeContents, err = removeServiceFromCompose(string(composeSnapshot.contents), serviceName)
		if err != nil {
			return "", managedFileSnapshot{}, "", false, fmt.Errorf("remove service from Compose file: %w", err)
		}
		composeChanged = composeContents != string(composeSnapshot.contents)
	}
	return composePath, composeSnapshot, composeContents, composeChanged, nil
}

// resumeServiceDeletion runs the durable staged cleanup. Stages completed
// before a crash are skipped; the current stage may safely run again.
func (s *Applications) resumeServiceDeletion(ctx context.Context, item application.Application, target application.Service, registered bool, intent application.ServiceDeletionIntent, directory string, deleter applicationServiceDeletionRepository, controller composeServiceController, remover composeServiceRemover, progress func(stage, message string)) error {
	applicationID := item.ID
	serviceName := intent.ServiceName
	if serviceName == "" {
		serviceName = target.Name
	}
	stage := intent.Stage
	if stage == "" {
		stage = serviceDeletionStageSchedules
	}
	if stage == serviceDeletionStageComplete || intent.State == "complete" {
		return nil
	}

	if stage == serviceDeletionStageSchedules {
		reportServiceDeletionProgress(progress, serviceDeletionStageSchedules, "Disabling scheduled backups for the service")
		if registered && application.IsDatabaseServiceType(target.Type) {
			if err := s.disableServiceBackupTimer(ctx, applicationID, target); err != nil {
				return s.failServiceDeletion(applicationID, serviceName, serviceDeletionStageSchedules, err)
			}
		}
		if err := s.checkpointServiceDeletion(ctx, applicationID, serviceName, serviceDeletionStageStop, serviceDeletionStateRunning, ""); err != nil {
			return err
		}
		stage = serviceDeletionStageStop
	}

	composePath, composeSnapshot, composeContents, composeChanged, err := s.stageServiceRemoval(directory, serviceName)
	if err != nil {
		// A missing Compose definition after metadata removal still lets the
		// remaining stages (routing, metadata) finish; container operations
		// below fail closed and checkpoint their stage for retry.
		if stage == serviceDeletionStageRouting || stage == serviceDeletionStageMetadata {
			composeChanged = false
		} else {
			return s.failServiceDeletion(applicationID, serviceName, stage, err)
		}
	}
	if composeChanged && (stage == serviceDeletionStageStop || stage == serviceDeletionStageRemove || stage == serviceDeletionStageCompose) {
		if err := writeManagedFile(composePath, composeContents, composeSnapshot.mode); err != nil {
			return s.failServiceDeletion(applicationID, serviceName, stage, fmt.Errorf("stage service removal in Compose file: %w", err))
		}
		if err := s.validateStagedCompose(ctx, directory); err != nil {
			restoreErr := restoreManagedFile(composeSnapshot)
			return s.failServiceDeletion(applicationID, serviceName, stage, errors.Join(err, restoreErr))
		}
		// Docker must still see the service definition while it stops and
		// removes the container. The validated edit is committed after those
		// service-scoped operations complete.
		if err := restoreManagedFile(composeSnapshot); err != nil {
			return s.failServiceDeletion(applicationID, serviceName, stage, fmt.Errorf("restore Compose file before service removal: %w", err))
		}
	}

	if stage == serviceDeletionStageStop {
		reportServiceDeletionProgress(progress, serviceDeletionStageStop, "Stopping the service container with Docker Compose")
		if err := s.checkpointServiceDeletion(ctx, applicationID, serviceName, serviceDeletionStageStop, serviceDeletionStateRunning, ""); err != nil {
			return err
		}
		if err := controller.Stop(ctx, directory, serviceName); err != nil {
			return s.failServiceDeletion(applicationID, serviceName, serviceDeletionStageStop, fmt.Errorf("stop service: %w", err))
		}
		if err := s.checkpointServiceDeletion(ctx, applicationID, serviceName, serviceDeletionStageRemove, serviceDeletionStateRunning, ""); err != nil {
			return err
		}
		stage = serviceDeletionStageRemove
	}

	if stage == serviceDeletionStageRemove {
		reportServiceDeletionProgress(progress, serviceDeletionStageRemove, "Removing the service container with Docker Compose")
		if err := remover.Remove(ctx, directory, serviceName); err != nil {
			return s.failServiceDeletion(applicationID, serviceName, serviceDeletionStageRemove, fmt.Errorf("remove service container: %w", err))
		}
		if err := s.checkpointServiceDeletion(ctx, applicationID, serviceName, serviceDeletionStageCompose, serviceDeletionStateRunning, ""); err != nil {
			return err
		}
		stage = serviceDeletionStageCompose
	}

	if stage == serviceDeletionStageCompose {
		reportServiceDeletionProgress(progress, serviceDeletionStageCompose, "Removing the service from the Docker Compose file")
		if composeChanged {
			if err := writeManagedFile(composePath, composeContents, composeSnapshot.mode); err != nil {
				return s.failServiceDeletion(applicationID, serviceName, serviceDeletionStageCompose, fmt.Errorf("remove service from Compose file: %w", err))
			}
		}
		if err := s.checkpointServiceDeletion(ctx, applicationID, serviceName, serviceDeletionStageRouting, serviceDeletionStateRunning, ""); err != nil {
			return err
		}
		stage = serviceDeletionStageRouting
	}

	if stage == serviceDeletionStageRouting {
		if err := s.removeServiceRoutings(ctx, applicationID, serviceName, progress); err != nil {
			return s.failServiceDeletion(applicationID, serviceName, serviceDeletionStageRouting, err)
		}
		if err := s.checkpointServiceDeletion(ctx, applicationID, serviceName, serviceDeletionStageMetadata, serviceDeletionStateRunning, ""); err != nil {
			return err
		}
		stage = serviceDeletionStageMetadata
	}

	if stage == serviceDeletionStageMetadata {
		reportServiceDeletionProgress(progress, serviceDeletionStageMetadata, "Deleting service metadata")
		if err := deleter.DeleteService(ctx, applicationID, serviceName); err != nil && !errors.Is(err, application.ErrServiceNotFound) && !errors.Is(err, application.ErrNotFound) {
			if composeChanged {
				restoreErr := restoreManagedFile(composeSnapshot)
				if restoreErr != nil {
					return s.failServiceDeletion(applicationID, serviceName, serviceDeletionStageMetadata, fmt.Errorf("delete service metadata: %w (restore Compose file: %v)", err, restoreErr))
				}
			}
			return s.failServiceDeletion(applicationID, serviceName, serviceDeletionStageMetadata, fmt.Errorf("delete service metadata: %w", err))
		}
		if err := s.checkpointServiceDeletion(ctx, applicationID, serviceName, serviceDeletionStageComplete, "complete", ""); err != nil {
			return err
		}
	}
	return nil
}

// disableServiceBackupTimer stops one database service's timer through the
// narrow service-scoped capability. Non-database services have no timer.
// Callers without backup infrastructure (nil disabler and leases) skip this
// step; production wiring always provides BackupService, which implements the
// service-scoped method.
func (s *Applications) disableServiceBackupTimer(ctx context.Context, applicationID int64, target application.Service) error {
	if !application.IsDatabaseServiceType(target.Type) || target.ID < 1 {
		return nil
	}
	s.mu.RLock()
	disabler := s.scheduleDisabler
	s.mu.RUnlock()
	if scoped, ok := disabler.(applicationServiceScheduleDisabler); ok && scoped != nil {
		return scoped.DisableServiceBackupSchedule(ctx, applicationID, target.ID)
	}
	if disabler == nil && s.backupLeases == nil {
		return nil
	}
	return errors.New("service backup scheduler is not configured")
}

// removeServiceRoutings deletes every routing row pointing at the service and
// reloads Caddy when anything was removed. Backup files are intentionally
// retained: only routing metadata is coordinated here.
func (s *Applications) removeServiceRoutings(ctx context.Context, applicationID int64, serviceName string, progress func(stage, message string)) error {
	if s.routingRepository == nil {
		return nil
	}
	routings, err := s.routingRepository.ListAllRoutings(ctx)
	if err != nil {
		return fmt.Errorf("list routings for service deletion: %w", err)
	}
	removed := false
	for _, routing := range routings {
		if routing.ApplicationID != applicationID || routing.ServiceName != serviceName {
			continue
		}
		if err := s.routingRepository.DeleteRouting(ctx, routing.ApplicationID, routing.DomainID, routing.ID); err != nil && !errors.Is(err, application.ErrRoutingNotFound) {
			return fmt.Errorf("delete service routing: %w", err)
		}
		removed = true
	}
	if !removed {
		return nil
	}
	reportServiceDeletionProgress(progress, serviceDeletionStageRouting, "Removing service routing and reloading the proxy")
	if err := s.refreshProxyAfterApplicationDeletion(ctx); err != nil {
		return fmt.Errorf("reload proxy after service deletion: %w", err)
	}
	return nil
}

func (s *Applications) checkpointServiceDeletion(ctx context.Context, applicationID int64, serviceName, stage, state, detail string) error {
	if s.serviceDeletionIntents == nil {
		return nil
	}
	checkpointContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.serviceDeletionIntents.UpdateServiceDeletion(checkpointContext, applicationID, serviceName, stage, state, detail, time.Now().UTC()); err != nil {
		return fmt.Errorf("checkpoint service deletion: %w", err)
	}
	return nil
}

func (s *Applications) failServiceDeletion(applicationID int64, serviceName, stage string, operationErr error) error {
	checkpointErr := s.checkpointServiceDeletion(context.Background(), applicationID, serviceName, stage, serviceDeletionStateFailed, serviceDeletionFailureDetail(stage))
	if checkpointErr != nil {
		return errors.Join(operationErr, checkpointErr)
	}
	return operationErr
}

func serviceDeletionFailureDetail(stage string) string {
	switch stage {
	case serviceDeletionStageSchedules:
		return "Disabling scheduled backups failed or another backup operation is using this service. Retry the deletion to continue."
	case serviceDeletionStageStop:
		return "Stopping the service container failed. Retry the deletion to continue."
	case serviceDeletionStageRemove:
		return "Removing the service container failed. Retry the deletion to continue."
	case serviceDeletionStageCompose:
		return "Removing the service from the Compose file failed. Retry the deletion to continue."
	case serviceDeletionStageRouting:
		return "Removing service routing failed. Retry the deletion to continue."
	case serviceDeletionStageMetadata:
		return "Removing service metadata failed. Retry the deletion to continue."
	default:
		return "Service deletion failed. Retry the deletion to continue."
	}
}

// DeleteApplication removes all containers and Compose-managed resources for
// an application, deletes its database metadata, and finally removes the
// validated application directory.
func (s *Applications) DeleteApplication(ctx context.Context, applicationID int64) error {
	return s.DeleteApplicationWithProgress(ctx, applicationID, nil)
}

// DeleteApplicationWithProgress performs application deletion and reports the
// active workflow stage before each destructive operation. The callback is
// synchronous and may be nil.
func (s *Applications) DeleteApplicationWithProgress(ctx context.Context, applicationID int64, progress func(stage, message string)) error {
	if s.detailsRepository == nil {
		return errors.New("application details repository is not configured")
	}
	deleter, ok := s.repository.(applicationDeletionRepository)
	if !ok {
		return errors.New("application deletion repository is not configured")
	}
	remover, ok := s.runner.(composeProjectRemover)
	if !ok {
		return errors.New("compose project remover is not configured")
	}

	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	var intent application.ApplicationDeletionIntent
	if err != nil {
		if s.deletionIntents == nil || !errors.Is(err, application.ErrNotFound) {
			return fmt.Errorf("get application for deletion: %w", err)
		}
		intent, err = s.deletionIntents.GetApplicationDeletion(ctx, applicationID)
		if err != nil {
			return fmt.Errorf("get application deletion intent: %w", err)
		}
		item = application.Application{
			ID:         intent.ApplicationID,
			Name:       intent.Name,
			FolderName: intent.FolderName,
		}
	}
	if s.deletionIntents != nil && intent.ApplicationID == 0 {
		intent, err = s.deletionIntents.BeginApplicationDeletion(ctx, item, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("record application deletion intent: %w", err)
		}
	}
	if intent.State == "complete" {
		return nil
	}
	folderName, err := application.ValidateFolderName(item.FolderName)
	if err != nil {
		return fmt.Errorf("validate stored application folder for deletion: %w", err)
	}
	folderLease, err := s.acquireProjectKey(ctx, applicationFolderLockKey(folderName))
	if err != nil {
		return err
	}
	defer folderLease.release()

	directory, err := s.managedApplicationDirectory(item)
	directoryMissing := false
	if err != nil {
		if s.deletionIntents == nil || !errors.Is(err, application.ErrNotFound) {
			return fmt.Errorf("resolve application directory for deletion: %w", err)
		}
		// A crash after folder removal and before the final checkpoint leaves
		// an intent without a directory. Resume with the expected path and let
		// the staged cleanup treat the absent folder as already complete
		// instead of returning ErrNotFound.
		directory = filepath.Join(s.applicationsDir, folderName)
		if relative, relErr := filepath.Rel(s.applicationsDir, directory); relErr != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("resolve application directory for deletion: %w", err)
		}
		directoryMissing = true
	}
	leases, err := s.acquireApplicationBackupLeases(ctx, applicationID)
	if err != nil {
		if s.deletionIntents != nil {
			stage := intent.Stage
			if stage == "" {
				stage = applicationDeletionStageSchedules
			}
			return s.failApplicationDeletion(applicationID, stage, err)
		}
		return err
	}
	var deletionErr error
	if s.deletionIntents == nil {
		deletionErr = s.deleteApplicationWithoutIntent(ctx, applicationID, folderName, directory, remover, deleter, progress)
	} else {
		deletionErr = s.resumeApplicationDeletion(ctx, applicationID, intent, directory, directoryMissing, remover, deleter, progress)
	}
	if releaseErr := leases.release(); releaseErr != nil {
		leaseErr := fmt.Errorf("release application deletion leases: %w", releaseErr)
		if deletionErr != nil {
			return errors.Join(deletionErr, leaseErr)
		}
		return leaseErr
	}
	return deletionErr
}

func (s *Applications) deleteApplicationWithoutIntent(ctx context.Context, applicationID int64, folderName, directory string, remover composeProjectRemover, deleter applicationDeletionRepository, progress func(stage, message string)) error {

	reportApplicationDeletionProgress(progress, "resources", "Removing application containers and Docker resources")
	if err := remover.Down(ctx, directory); err != nil {
		return fmt.Errorf("remove application resources: %w", err)
	}

	reportApplicationDeletionProgress(progress, "metadata", "Deleting application metadata")
	if err := deleter.DeleteApplication(ctx, applicationID); err != nil {
		return fmt.Errorf("delete application metadata: %w", err)
	}

	reportApplicationDeletionProgress(progress, "folder", "Deleting the application folder")
	owned, err := s.verifyApplicationFolderOwnership(ctx, applicationID, folderName, directory)
	if err != nil {
		return fmt.Errorf("verify application folder ownership: %w", err)
	}
	if owned {
		if err := os.RemoveAll(directory); err != nil {
			return fmt.Errorf("delete application folder: %w", err)
		}
	}
	return nil
}

const (
	applicationDeletionStageResources = "resources"
	applicationDeletionStageMetadata  = "metadata"
	applicationDeletionStageSchedules = "schedules"
	applicationDeletionStageRouting   = "routing"
	applicationDeletionStageKeys      = "keys"
	applicationDeletionStageFolder    = "folder"
	applicationDeletionStageComplete  = "complete"
	applicationDeletionStateRunning   = "running"
	applicationDeletionStateFailed    = "failed"
)

func (s *Applications) resumeApplicationDeletion(ctx context.Context, applicationID int64, intent application.ApplicationDeletionIntent, directory string, directoryMissing bool, remover composeProjectRemover, deleter applicationDeletionRepository, progress func(stage, message string)) error {
	scheduleDisabler, keyCleanup := s.applicationDeletionDependencies()
	folderName := intent.FolderName
	if folderName == "" {
		folderName = filepath.Base(directory)
	}
	if _, err := application.ValidateFolderName(folderName); err != nil {
		return fmt.Errorf("validate stored application folder for deletion: %w", err)
	}
	stage := intent.Stage
	if stage == "" {
		stage = applicationDeletionStageSchedules
	}
	if stage == applicationDeletionStageComplete || intent.State == "complete" {
		return nil
	}

	if stage == applicationDeletionStageSchedules {
		if scheduleDisabler != nil {
			if err := scheduleDisabler.DisableApplicationSchedules(ctx, applicationID); err != nil {
				return s.failApplicationDeletion(applicationID, applicationDeletionStageSchedules, err)
			}
		}
		if err := s.checkpointApplicationDeletion(ctx, applicationID, applicationDeletionStageResources, applicationDeletionStateRunning, ""); err != nil {
			return err
		}
		stage = applicationDeletionStageResources
	}

	if stage == applicationDeletionStageResources {
		reportApplicationDeletionProgress(progress, applicationDeletionStageResources, "Removing application containers and Docker resources")
		if err := s.checkpointApplicationDeletion(ctx, applicationID, applicationDeletionStageResources, applicationDeletionStateRunning, ""); err != nil {
			return err
		}
		if !directoryMissing {
			if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
				directoryMissing = true
			} else if err != nil {
				return s.failApplicationDeletion(applicationID, applicationDeletionStageResources, err)
			}
		}
		if !directoryMissing {
			if err := remover.Down(ctx, directory); err != nil {
				return s.failApplicationDeletion(applicationID, applicationDeletionStageResources, err)
			}
		}
		if err := s.checkpointApplicationDeletion(ctx, applicationID, applicationDeletionStageMetadata, applicationDeletionStateRunning, ""); err != nil {
			return err
		}
		stage = applicationDeletionStageMetadata
	}

	if stage == applicationDeletionStageMetadata {
		reportApplicationDeletionProgress(progress, applicationDeletionStageMetadata, "Deleting application metadata")
		if err := deleter.DeleteApplication(ctx, applicationID); err != nil && !errors.Is(err, application.ErrNotFound) {
			return s.failApplicationDeletion(applicationID, applicationDeletionStageMetadata, err)
		}
		if err := s.checkpointApplicationDeletion(ctx, applicationID, applicationDeletionStageRouting, applicationDeletionStateRunning, ""); err != nil {
			return err
		}
		stage = applicationDeletionStageRouting
	}

	if stage == applicationDeletionStageRouting {
		if err := s.refreshProxyAfterApplicationDeletion(ctx); err != nil {
			return s.failApplicationDeletion(applicationID, applicationDeletionStageRouting, err)
		}
		if err := s.checkpointApplicationDeletion(ctx, applicationID, applicationDeletionStageKeys, applicationDeletionStateRunning, ""); err != nil {
			return err
		}
		stage = applicationDeletionStageKeys
	}

	if stage == applicationDeletionStageKeys {
		if keyCleanup != nil {
			if err := keyCleanup.CleanupApplicationKey(ctx, applicationID); err != nil {
				return s.failApplicationDeletion(applicationID, applicationDeletionStageKeys, err)
			}
		}
		if err := s.checkpointApplicationDeletion(ctx, applicationID, applicationDeletionStageFolder, applicationDeletionStateRunning, ""); err != nil {
			return err
		}
		stage = applicationDeletionStageFolder
	}

	if stage == applicationDeletionStageFolder {
		reportApplicationDeletionProgress(progress, applicationDeletionStageFolder, "Deleting the application folder")
		owned, err := s.verifyApplicationFolderOwnership(ctx, applicationID, folderName, directory)
		if err != nil {
			return s.failApplicationDeletion(applicationID, applicationDeletionStageFolder, err)
		}
		if owned {
			if err := os.RemoveAll(directory); err != nil {
				return s.failApplicationDeletion(applicationID, applicationDeletionStageFolder, err)
			}
			if err := syncDirectory(s.applicationsDir); err != nil {
				return s.failApplicationDeletion(applicationID, applicationDeletionStageFolder, err)
			}
		}
		if err := s.checkpointApplicationDeletion(ctx, applicationID, applicationDeletionStageComplete, "complete", ""); err != nil {
			return err
		}
	}
	return nil
}

func (s *Applications) refreshProxyAfterApplicationDeletion(ctx context.Context) error {
	if s.routingRepository == nil {
		return nil
	}
	if _, err := os.Lstat(s.proxyDirectory); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect Caddy proxy for application deletion: %w", err)
	}
	if err := s.refreshProxyConfiguration(ctx); err != nil {
		return fmt.Errorf("reload Caddy after application deletion: %w", err)
	}
	return nil
}

func (s *Applications) checkpointApplicationDeletion(ctx context.Context, applicationID int64, stage, state, detail string) error {
	if s.deletionIntents == nil {
		return nil
	}
	checkpointContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.deletionIntents.UpdateApplicationDeletion(checkpointContext, applicationID, stage, state, detail, time.Now().UTC()); err != nil {
		return fmt.Errorf("checkpoint application deletion: %w", err)
	}
	return nil
}

func (s *Applications) failApplicationDeletion(applicationID int64, stage string, operationErr error) error {
	checkpointErr := s.checkpointApplicationDeletion(context.Background(), applicationID, stage, applicationDeletionStateFailed, applicationDeletionFailureDetail(stage))
	if checkpointErr != nil {
		return errors.Join(operationErr, checkpointErr)
	}
	return operationErr
}

func applicationDeletionFailureDetail(stage string) string {
	switch stage {
	case applicationDeletionStageSchedules:
		return "Disabling scheduled backups failed. Retry the deletion to continue."
	case applicationDeletionStageResources:
		return "Removing Docker resources failed. Retry the deletion to continue."
	case applicationDeletionStageMetadata:
		return "Removing application metadata failed. Retry the deletion to continue."
	case applicationDeletionStageRouting:
		return "Refreshing application routing failed. Retry the deletion to continue."
	case applicationDeletionStageKeys:
		return "Removing deployment keys failed. Retry the deletion to continue."
	case applicationDeletionStageFolder:
		return "Removing the application folder failed. Retry the deletion to continue."
	default:
		return "Application deletion failed. Retry the deletion to continue."
	}
}

// ensureFolderNotReserved rejects folder reuse while an incomplete deletion
// tombstone still owns the name. The folder lock serializes concurrent
// creators, but only this durable check survives a crash between folder
// removal and the final deletion checkpoint.
func (s *Applications) ensureFolderNotReserved(ctx context.Context, folderName string) error {
	if s.deletionIntents == nil {
		return nil
	}
	checker, ok := s.deletionIntents.(applicationFolderDeletionReservation)
	if !ok {
		return nil
	}
	reserved, err := checker.IsApplicationFolderDeletionActive(ctx, folderName)
	if err != nil {
		return fmt.Errorf("check application folder reservation: %w", err)
	}
	if reserved {
		return application.ErrApplicationDeletionInProgress
	}
	return nil
}

// ensureApplicationNotDeleting rejects mutations on an application with an
// incomplete deletion intent, including retries after metadata removal.
func (s *Applications) ensureApplicationNotDeleting(ctx context.Context, applicationID int64) error {
	if s.deletionIntents == nil {
		return nil
	}
	checker, ok := s.deletionIntents.(applicationDeletionStateChecker)
	if !ok {
		return nil
	}
	active, err := checker.IsApplicationDeletionActive(ctx, applicationID)
	if err != nil {
		return fmt.Errorf("check application deletion state: %w", err)
	}
	if active {
		return application.ErrApplicationDeletionInProgress
	}
	return nil
}

// verifyApplicationFolderOwnership ensures a deletion removes only the folder
// it owns. It rejects symlinks, escapes from the managed applications
// directory, and folders currently claimed by another application record.
// A missing directory is idempotent success and returns false.
func (s *Applications) verifyApplicationFolderOwnership(ctx context.Context, applicationID int64, folderName, directory string) (bool, error) {
	if _, err := application.ValidateFolderName(folderName); err != nil {
		return false, fmt.Errorf("validate stored application folder for deletion: %w", err)
	}
	expected := filepath.Join(s.applicationsDir, folderName)
	if filepath.Clean(directory) != filepath.Clean(expected) {
		return false, errors.New("application folder is outside the managed applications directory")
	}
	if err := checkManagedAncestors(s.applicationsDir, directory); err != nil {
		return false, err
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, errors.New("application path is not a directory")
	}
	if err := checkResolvedDirectoryContainment(s.applicationsDir, directory); err != nil {
		return false, err
	}
	if s.repository != nil {
		remaining, err := s.repository.List(ctx)
		if err != nil {
			return false, fmt.Errorf("list applications for folder ownership: %w", err)
		}
		for _, item := range remaining {
			if item.FolderName == folderName && item.ID != applicationID {
				return false, application.ErrApplicationDeletionInProgress
			}
		}
	}
	return true, nil
}

type applicationBackupLeaseSet struct {
	repository applicationBackupLeaseRepository
	leases     []applicationBackupLease
}

type applicationBackupLease struct {
	serviceID int64
	token     string
}

func (s *Applications) acquireApplicationBackupLeases(ctx context.Context, applicationID int64) (*applicationBackupLeaseSet, error) {
	set := &applicationBackupLeaseSet{repository: s.backupLeases}
	if s.backupLeases == nil || s.detailsRepository == nil {
		return set, nil
	}
	services, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		if errors.Is(err, application.ErrNotFound) {
			return set, nil
		}
		return nil, fmt.Errorf("list services for application deletion leases: %w", err)
	}
	sort.Slice(services, func(left, right int) bool { return services[left].ID < services[right].ID })
	now := time.Now().UTC()
	for _, service := range services {
		if !application.IsDatabaseServiceType(service.Type) || service.ID < 1 {
			continue
		}
		token, err := newBackupLeaseToken()
		if err != nil {
			if releaseErr := set.release(); releaseErr != nil {
				err = errors.Join(err, fmt.Errorf("release application deletion leases: %w", releaseErr))
			}
			return nil, fmt.Errorf("create application deletion lease token: %w", err)
		}
		if err := s.backupLeases.AcquireBackupLease(ctx, service.ID, "deletion", token, now, now.Add(backupLeaseDuration)); err != nil {
			acquireErr := err
			if releaseErr := set.release(); releaseErr != nil {
				acquireErr = errors.Join(acquireErr, fmt.Errorf("release application deletion leases: %w", releaseErr))
			}
			if errors.Is(err, application.ErrBackupOperationInProgress) {
				return nil, acquireErr
			}
			return nil, fmt.Errorf("acquire application deletion lease: %w", acquireErr)
		}
		set.leases = append(set.leases, applicationBackupLease{serviceID: service.ID, token: token})
	}
	return set, nil
}

func (s *applicationBackupLeaseSet) release() error {
	if s == nil || s.repository == nil {
		return nil
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var releaseErr error
	for _, lease := range s.leases {
		if err := s.repository.ReleaseBackupLease(cleanupContext, lease.serviceID, lease.token); err != nil {
			releaseErr = errors.Join(releaseErr, err)
		}
	}
	return releaseErr
}

func reportApplicationDeletionProgress(progress func(stage, message string), stage, message string) {
	if progress != nil {
		progress(stage, message)
	}
}

func reportServiceDeletionProgress(progress func(stage, message string), stage, message string) {
	if progress != nil {
		progress(stage, message)
	}
}

func (s *Applications) runServiceAction(ctx context.Context, applicationID int64, serviceName, action string, run func(composeServiceController, string, string) error) error {
	if s.detailsRepository == nil {
		return errors.New("application details repository is not configured")
	}
	serviceName, err := application.ValidateServiceName(serviceName)
	if err != nil {
		return err
	}
	controller, ok := s.runner.(composeServiceController)
	if !ok {
		return errors.New("compose service controller is not configured")
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return fmt.Errorf("get application for service action: %w", err)
	}
	services, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return fmt.Errorf("list services for service action: %w", err)
	}
	registered := false
	for _, service := range services {
		if service.Name == serviceName {
			registered = true
			break
		}
	}
	if !registered {
		return application.ErrServiceNotFound
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return fmt.Errorf("resolve application directory for service action: %w", err)
	}
	if err := run(controller, directory, serviceName); err != nil {
		return fmt.Errorf("%s service: %w", action, err)
	}
	return nil
}

func (s *Applications) runProxyAction(ctx context.Context, action string, run func(composeServiceController, string, string) error) error {
	controller, ok := s.runner.(composeServiceController)
	if !ok {
		return errors.New("compose service controller is not configured")
	}
	lease, err := s.acquireProxyProject(ctx)
	if err != nil {
		return err
	}
	defer lease.release()
	if err := run(controller, s.proxyDirectory, proxyDir); err != nil {
		return fmt.Errorf("%s proxy: %w", action, err)
	}
	return nil
}

// Create validates the fields, creates the application files, and persists its
// metadata. A failed metadata write removes only the files created by this
// operation.
func (s *Applications) Create(ctx context.Context, name, folderName string) (application.Application, error) {
	name, err := application.ValidateName(name)
	if err != nil {
		return application.Application{}, err
	}
	folderName, err = application.ValidateFolderName(folderName)
	if err != nil {
		return application.Application{}, err
	}

	lease, err := s.acquireProjectKey(ctx, applicationFolderLockKey(folderName))
	if err != nil {
		return application.Application{}, err
	}
	defer lease.release()

	if err := s.ensureFolderNotReserved(ctx, folderName); err != nil {
		return application.Application{}, err
	}

	if err := ensureDirectory(s.applicationsDir); err != nil {
		return application.Application{}, fmt.Errorf("ensure applications directory: %w", err)
	}
	applicationDirectory := filepath.Join(s.applicationsDir, folderName)
	if _, err := os.Lstat(applicationDirectory); err == nil {
		return application.Application{}, application.ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return application.Application{}, fmt.Errorf("inspect application directory: %w", err)
	}
	if err := os.Mkdir(applicationDirectory, 0o750); err != nil {
		if errors.Is(err, os.ErrExist) {
			return application.Application{}, application.ErrAlreadyExists
		}
		return application.Application{}, fmt.Errorf("create application directory: %w", err)
	}

	composePath := filepath.Join(applicationDirectory, "compose.yml")
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = os.Remove(composePath)
		_ = os.Remove(filepath.Join(applicationDirectory, varsEnvFile))
		_ = os.Remove(filepath.Join(applicationDirectory, secretsEnvFile))
		_ = os.Remove(applicationDirectory)
	}()

	if err := writeManagedFile(composePath, applicationCompose, 0o644); err != nil {
		return application.Application{}, fmt.Errorf("write application Compose file: %w", err)
	}
	if err := writeEmptyEnvironmentFiles(applicationDirectory); err != nil {
		return application.Application{}, fmt.Errorf("write application environment files: %w", err)
	}

	created, err := s.repository.Create(ctx, application.Application{
		Name:       name,
		FolderName: folderName,
		CreatedAt:  time.Now().UTC(),
	})
	if err != nil {
		return application.Application{}, fmt.Errorf("persist application metadata: %w", err)
	}
	committed = true
	return created, nil
}
