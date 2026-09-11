package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	GetRedlaunchPublicAccess(context.Context) (application.RedlaunchPublicAccess, error)
	UpdateRedlaunchPublicAccess(context.Context, application.RedlaunchPublicAccess) error
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

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return application.EnvironmentFiles{}, err
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return application.EnvironmentFiles{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return err
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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

	value, rawValue, err := findEnvironmentVariableToken(string(sourceSnapshot.contents), name)
	if err != nil {
		return err
	}
	if _, err := application.ValidateEnvironmentVariableValue(value); err != nil {
		return err
	}
	updatedSource, err := deleteEnvironmentVariable(string(sourceSnapshot.contents), name)
	if err != nil {
		return err
	}
	updatedDestination, err := appendRawEnvironmentVariable(string(destinationSnapshot.contents), name, rawValue)
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
// file while preserving the rest of the file and its permissions.
func (s *Applications) UpdateEnvironmentSecret(ctx context.Context, applicationID int64, originalName, name, value string) error {
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
	return s.modifyApplicationEnvironmentFile(ctx, applicationID, secretsEnvFile, "secrets", func(contents string) (string, error) {
		return updateEnvironmentFile(contents, originalName, name, value)
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
	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return err
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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

type applicationServiceDeletionRepository interface {
	DeleteService(context.Context, int64, string) error
}

type applicationDeletionRepository interface {
	DeleteApplication(context.Context, int64) error
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

type composeServiceEnvironmentInspector interface {
	Environment(context.Context, string, string) ([]compose.EnvironmentVariable, error)
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

// Applications coordinates application metadata and its managed files.
type Applications struct {
	repository         ApplicationRepository
	detailsRepository  applicationDetailsRepository
	serviceRepository  applicationServiceRepository
	domainRepository   applicationDomainRepository
	routingRepository  applicationRoutingRepository
	settingsRepository redlaunchSettingsRepository
	applicationsDir    string
	proxyDirectory     string
	runner             composeRunner
	mu                 sync.Mutex
}

// NewApplications constructs an application service rooted below the
// configured projects directory.
func NewApplications(repository ApplicationRepository, projectsRoot string, runners ...composeRunner) (*Applications, error) {
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
	runner := composeRunner(compose.CommandRunner{})
	if len(runners) > 0 && runners[0] != nil {
		runner = runners[0]
	}
	return &Applications{
		repository:         repository,
		detailsRepository:  detailsRepository,
		serviceRepository:  serviceRepository,
		domainRepository:   domainRepository,
		routingRepository:  routingRepository,
		settingsRepository: settingsRepository,
		applicationsDir:    applicationsDirectory,
		proxyDirectory:     filepath.Join(root, coreDir, proxyDir),
		runner:             runner,
	}, nil
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

	s.mu.Lock()
	defer s.mu.Unlock()

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

// GetProxyFullLogs returns the complete log history for the managed proxy.
// Unlike the dashboard data, this method deliberately does not apply a line
// limit so the HTTP layer can return it as a download response.
func (s *Applications) GetProxyFullLogs(ctx context.Context) (string, error) {
	inspector, ok := s.runner.(composeServiceFullLogsInspector)
	if !ok {
		return "", errors.New("proxy log inspection is not configured")
	}
	logs, err := inspector.AllLogs(ctx, s.proxyDirectory, proxyDir)
	if err != nil {
		return "", fmt.Errorf("read proxy logs: %w", err)
	}
	return logs, nil
}

// GetServiceDetails returns the registered service metadata together with
// best-effort Docker logs and resolved environment values for the dashboard.
// Docker inspection failures leave the corresponding widget unavailable while
// preserving the service metadata and any runtime fields that were found.
func (s *Applications) GetServiceDetails(ctx context.Context, applicationID int64, serviceName string) (application.ServiceDetails, error) {
	if s.detailsRepository == nil {
		return application.ServiceDetails{}, errors.New("application details repository is not configured")
	}
	serviceName, err := application.ValidateServiceName(serviceName)
	if err != nil {
		return application.ServiceDetails{}, err
	}

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return application.ServiceDetails{}, err
	}
	services, err := s.ListServices(ctx, applicationID)
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
	environmentInspector, hasEnvironment := s.runner.(composeServiceEnvironmentInspector)
	if !hasLogs && !hasEnvironment {
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
	if hasEnvironment {
		if environment, err := environmentInspector.Environment(ctx, directory, serviceName); err == nil {
			details.Environment = make([]application.EnvironmentVariable, 0, len(environment))
			for _, variable := range environment {
				details.Environment = append(details.Environment, application.EnvironmentVariable{
					Key:       variable.Key,
					Value:     variable.Value,
					Sensitive: application.IsSensitiveEnvironmentKey(variable.Key),
				})
			}
			sort.SliceStable(details.Environment, func(left, right int) bool {
				return details.Environment[left].Key < details.Environment[right].Key
			})
			details.EnvironmentAvailable = true
		}
	}
	return details, nil
}

// GetServiceFullLogs returns the complete log history for one managed service.
// Unlike the service details data, this method deliberately does not apply a
// line limit so the HTTP layer can return it as a download response.
func (s *Applications) GetServiceFullLogs(ctx context.Context, applicationID int64, serviceName string) (string, error) {
	if s.detailsRepository == nil {
		return "", errors.New("application details repository is not configured")
	}
	serviceName, err := application.ValidateServiceName(serviceName)
	if err != nil {
		return "", err
	}

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return "", err
	}
	services, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return "", err
	}
	registered := false
	for _, service := range services {
		if service.Name == serviceName {
			registered = true
			break
		}
	}
	if !registered {
		return "", application.ErrServiceNotFound
	}

	inspector, ok := s.runner.(composeServiceFullLogsInspector)
	if !ok {
		return "", errors.New("service log inspection is not configured")
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return "", err
	}
	logs, err := inspector.AllLogs(ctx, directory, serviceName)
	if err != nil {
		return "", fmt.Errorf("read service logs: %w", err)
	}
	return logs, nil
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

// DeleteServiceWithProgress performs service deletion and reports the active
// workflow stage before each destructive operation. The callback is
// synchronous and may be nil.
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

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return fmt.Errorf("get application for service deletion: %w", err)
	}
	services, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return fmt.Errorf("list services for service deletion: %w", err)
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

	s.mu.Lock()
	defer s.mu.Unlock()

	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return fmt.Errorf("resolve application directory for service deletion: %w", err)
	}
	composePath, err := findApplicationComposeFile(directory)
	if err != nil {
		return fmt.Errorf("find application Compose file: %w", err)
	}
	var composeSnapshot managedFileSnapshot
	composeContents := ""
	composeChanged := false
	if composePath != "" {
		composeSnapshot, err = snapshotManagedFile(composePath)
		if err != nil {
			return fmt.Errorf("read application Compose file: %w", err)
		}
		composeContents, err = removeServiceFromCompose(string(composeSnapshot.contents), serviceName)
		if err != nil {
			return fmt.Errorf("remove service from Compose file: %w", err)
		}
		composeChanged = composeContents != string(composeSnapshot.contents)
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

	reportServiceDeletionProgress(progress, "metadata", "Deleting service metadata")
	if err := deleter.DeleteService(ctx, applicationID, serviceName); err != nil {
		if composeChanged {
			if restoreErr := restoreManagedFile(composeSnapshot); restoreErr != nil {
				return fmt.Errorf("delete service metadata: %w (restore Compose file: %v)", err, restoreErr)
			}
		}
		return fmt.Errorf("delete service metadata: %w", err)
	}
	return nil
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

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return fmt.Errorf("get application for deletion: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return fmt.Errorf("resolve application directory for deletion: %w", err)
	}

	reportApplicationDeletionProgress(progress, "resources", "Removing application containers and Docker resources")
	if err := remover.Down(ctx, directory); err != nil {
		return fmt.Errorf("remove application resources: %w", err)
	}

	reportApplicationDeletionProgress(progress, "metadata", "Deleting application metadata")
	if err := deleter.DeleteApplication(ctx, applicationID); err != nil {
		return fmt.Errorf("delete application metadata: %w", err)
	}

	reportApplicationDeletionProgress(progress, "folder", "Deleting the application folder")
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("delete application folder: %w", err)
	}
	return nil
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

	s.mu.Lock()
	defer s.mu.Unlock()

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
