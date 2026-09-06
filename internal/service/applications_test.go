package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

type applicationRepositoryStub struct {
	applications         []application.Application
	services             []application.Service
	domains              []application.Domain
	routings             []application.Routing
	created              application.Application
	service              application.Service
	domain               application.Domain
	routing              application.Routing
	publicAccess         application.RedlaunchPublicAccess
	err                  error
	serviceErr           error
	domainErr            error
	routingErr           error
	routingListErr       error
	routingUpdateErr     error
	routingDeleteErr     error
	publicAccessErr      error
	deleteErr            error
	applicationDeleteErr error
	deletedID            int64
	deletedName          string
	deletedApplicationID int64
	deletedDomainID      int64
	deletedDomainName    string
	deletedRoutingID     int64
}

func (s *applicationRepositoryStub) GetRedlaunchPublicAccess(context.Context) (application.RedlaunchPublicAccess, error) {
	return s.publicAccess, s.publicAccessErr
}

func (s *applicationRepositoryStub) UpdateRedlaunchPublicAccess(_ context.Context, settings application.RedlaunchPublicAccess) error {
	if s.publicAccessErr != nil {
		return s.publicAccessErr
	}
	s.publicAccess = settings
	return nil
}

type serviceRuntimeRunner struct {
	runtime        []compose.ServiceRuntime
	configured     []compose.ConfiguredService
	configErr      error
	configCalls    int
	projectDir     string
	action         string
	actions        []string
	service        string
	actionErr      error
	stopErr        error
	removeErr      error
	downErr        error
	logs           string
	logsTail       int
	logsErr        error
	allLogs        string
	allLogsErr     error
	allLogsCalled  bool
	environment    []compose.EnvironmentVariable
	environmentErr error
	reloadErr      error
	reloads        []string
}

func (r *serviceRuntimeRunner) Up(context.Context, string) error {
	return nil
}

func (r *serviceRuntimeRunner) ConfigServices(_ context.Context, projectDir string) ([]compose.ConfiguredService, error) {
	r.projectDir = projectDir
	r.configCalls++
	return r.configured, r.configErr
}

func (r *serviceRuntimeRunner) ReloadProxy(_ context.Context, projectDir string) error {
	r.reloads = append(r.reloads, projectDir)
	return r.reloadErr
}

func (r *serviceRuntimeRunner) ListServices(_ context.Context, projectDir string) ([]compose.ServiceRuntime, error) {
	r.projectDir = projectDir
	return r.runtime, nil
}

func (r *serviceRuntimeRunner) Logs(_ context.Context, projectDir, _ string, tail int) (string, error) {
	r.projectDir = projectDir
	r.logsTail = tail
	return r.logs, r.logsErr
}

func (r *serviceRuntimeRunner) AllLogs(_ context.Context, projectDir, _ string) (string, error) {
	r.projectDir = projectDir
	r.allLogsCalled = true
	return r.allLogs, r.allLogsErr
}

func (r *serviceRuntimeRunner) Environment(_ context.Context, projectDir, _ string) ([]compose.EnvironmentVariable, error) {
	r.projectDir = projectDir
	return r.environment, r.environmentErr
}

func (r *serviceRuntimeRunner) Start(_ context.Context, projectDir, serviceName string) error {
	r.action = "start"
	r.actions = append(r.actions, "start")
	r.projectDir = projectDir
	r.service = serviceName
	return r.actionErr
}

func (r *serviceRuntimeRunner) Stop(_ context.Context, projectDir, serviceName string) error {
	r.action = "stop"
	r.actions = append(r.actions, "stop")
	r.projectDir = projectDir
	r.service = serviceName
	if r.stopErr != nil {
		return r.stopErr
	}
	return r.actionErr
}

func (r *serviceRuntimeRunner) Restart(_ context.Context, projectDir, serviceName string) error {
	r.action = "restart"
	r.actions = append(r.actions, "restart")
	r.projectDir = projectDir
	r.service = serviceName
	return r.actionErr
}

func (r *serviceRuntimeRunner) Remove(_ context.Context, projectDir, serviceName string) error {
	r.action = "remove"
	r.actions = append(r.actions, "remove")
	r.projectDir = projectDir
	r.service = serviceName
	return r.removeErr
}

func (r *serviceRuntimeRunner) Down(_ context.Context, projectDir string) error {
	r.action = "down"
	r.actions = append(r.actions, "down")
	r.projectDir = projectDir
	return r.downErr
}

func (s *applicationRepositoryStub) List(context.Context) ([]application.Application, error) {
	return s.applications, nil
}

func (s *applicationRepositoryStub) Create(_ context.Context, item application.Application) (application.Application, error) {
	if s.err != nil {
		return application.Application{}, s.err
	}
	item.ID = 1
	s.created = item
	s.applications = append(s.applications, item)
	return item, nil
}

func (s *applicationRepositoryStub) Get(_ context.Context, id int64) (application.Application, error) {
	for _, item := range s.applications {
		if item.ID == id {
			return item, nil
		}
	}
	return application.Application{}, application.ErrNotFound
}

func (s *applicationRepositoryStub) ListServices(context.Context, int64) ([]application.Service, error) {
	return s.services, nil
}

func (s *applicationRepositoryStub) ListDomains(context.Context, int64) ([]application.Domain, error) {
	return s.domains, nil
}

func (s *applicationRepositoryStub) GetDomain(_ context.Context, applicationID, domainID int64) (application.Domain, error) {
	for _, item := range s.domains {
		if item.ApplicationID == applicationID && item.ID == domainID {
			return item, nil
		}
	}
	return application.Domain{}, application.ErrDomainNotFound
}

func (s *applicationRepositoryStub) CreateService(_ context.Context, item application.Service) (application.Service, error) {
	if s.serviceErr != nil {
		return application.Service{}, s.serviceErr
	}
	item.ID = int64(len(s.services) + 1)
	s.service = item
	s.services = append(s.services, item)
	return item, nil
}

func (s *applicationRepositoryStub) CreateDomain(_ context.Context, item application.Domain) (application.Domain, error) {
	if s.domainErr != nil {
		return application.Domain{}, s.domainErr
	}
	item.ID = int64(len(s.domains) + 1)
	s.domain = item
	s.domains = append(s.domains, item)
	return item, nil
}

func (s *applicationRepositoryStub) DeleteService(_ context.Context, applicationID int64, serviceName string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deletedID = applicationID
	s.deletedName = serviceName
	for index, item := range s.services {
		if item.ApplicationID == applicationID && item.Name == serviceName {
			s.services = append(s.services[:index], s.services[index+1:]...)
			return nil
		}
	}
	return application.ErrServiceNotFound
}

func (s *applicationRepositoryStub) DeleteApplication(_ context.Context, applicationID int64) error {
	if s.applicationDeleteErr != nil {
		return s.applicationDeleteErr
	}
	for index, item := range s.applications {
		if item.ID == applicationID {
			s.deletedApplicationID = applicationID
			s.applications = append(s.applications[:index], s.applications[index+1:]...)
			return nil
		}
	}
	return application.ErrNotFound
}

func (s *applicationRepositoryStub) DeleteDomain(_ context.Context, applicationID int64, domainName string) error {
	s.deletedDomainID = applicationID
	s.deletedDomainName = domainName
	if s.domainErr != nil {
		return s.domainErr
	}
	for index, item := range s.domains {
		if item.ApplicationID == applicationID && item.Name == domainName {
			s.domains = append(s.domains[:index], s.domains[index+1:]...)
			return nil
		}
	}
	return application.ErrDomainNotFound
}

func (s *applicationRepositoryStub) ListRoutings(_ context.Context, applicationID, domainID int64) ([]application.Routing, error) {
	if s.routingListErr != nil {
		return nil, s.routingListErr
	}
	var routings []application.Routing
	for _, item := range s.routings {
		if item.ApplicationID == applicationID && item.DomainID == domainID {
			routings = append(routings, item)
		}
	}
	return routings, nil
}

func (s *applicationRepositoryStub) ListAllRoutings(context.Context) ([]application.Routing, error) {
	if s.routingListErr != nil {
		return nil, s.routingListErr
	}
	return s.routings, nil
}

func (s *applicationRepositoryStub) GetRouting(_ context.Context, applicationID, domainID, routingID int64) (application.Routing, error) {
	for _, item := range s.routings {
		if item.ApplicationID == applicationID && item.DomainID == domainID && item.ID == routingID {
			return item, nil
		}
	}
	return application.Routing{}, application.ErrRoutingNotFound
}

func (s *applicationRepositoryStub) CreateRouting(_ context.Context, item application.Routing) (application.Routing, error) {
	if s.routingErr != nil {
		return application.Routing{}, s.routingErr
	}
	item.ID = int64(len(s.routings) + 1)
	s.routing = item
	s.routings = append(s.routings, item)
	return item, nil
}

func (s *applicationRepositoryStub) UpdateRouting(_ context.Context, item application.Routing) error {
	if s.routingUpdateErr != nil {
		return s.routingUpdateErr
	}
	for index := range s.routings {
		if s.routings[index].ID == item.ID && s.routings[index].ApplicationID == item.ApplicationID && s.routings[index].DomainID == item.DomainID {
			s.routings[index] = item
			s.routing = item
			return nil
		}
	}
	return application.ErrRoutingNotFound
}

func (s *applicationRepositoryStub) DeleteRouting(_ context.Context, applicationID, domainID, routingID int64) error {
	if s.routingDeleteErr != nil {
		return s.routingDeleteErr
	}
	s.deletedRoutingID = routingID
	for index, item := range s.routings {
		if item.ID == routingID && item.ApplicationID == applicationID && item.DomainID == domainID {
			s.routings = append(s.routings[:index], s.routings[index+1:]...)
			return nil
		}
	}
	return application.ErrRoutingNotFound
}

func TestApplicationsCreateScaffoldsFilesAndPersistsMetadata(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	created, err := applications.Create(t.Context(), " Status page ", " status-page ")
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Status page" || created.FolderName != "status-page" {
		t.Fatalf("Create() = %#v, want normalized metadata", created)
	}
	if repository.created.Name != created.Name || repository.created.FolderName != created.FolderName {
		t.Fatalf("stored application = %#v, want %#v", repository.created, created)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	composePath := filepath.Join(directory, "compose.yml")
	if got := readServiceFile(t, composePath); !strings.Contains(got, "redlaunch-common") || !strings.Contains(got, "external: true") {
		t.Fatalf("Compose file does not declare the shared network: %s", got)
	}
	if got := serviceFilePermissions(t, composePath); got != 0o644 {
		t.Errorf("Compose permissions = %o, want %o", got, 0o644)
	}
	for _, name := range []string{varsEnvFile, secretsEnvFile} {
		path := filepath.Join(directory, name)
		if got := readServiceFile(t, path); got != "" {
			t.Errorf("%s = %q, want an empty file", name, got)
		}
		if got := serviceFilePermissions(t, path); got != envFileMode {
			t.Errorf("%s permissions = %o, want %o", name, got, envFileMode)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy .env stat error = %v, want not exist", err)
	}
}

func TestApplicationsImportDockerComposeProjectRegistersManagedServices(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &serviceRuntimeRunner{
		configured: []compose.ConfiguredService{
			{Name: "web", Image: "nginx:1.27"},
			{Name: "worker", Image: "busybox:1.36"},
		},
	}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	createdApplication, err := applications.Create(t.Context(), "Status", "status")
	if err != nil {
		t.Fatal(err)
	}

	contents := []byte("services:\n  web:\n    image: nginx:1.27\n    ports:\n      - \"8080:80\"\n  worker:\n    image: busybox:1.36\n    command: [\"sh\", \"-c\", \"echo ready\"]\n\nnetworks:\n  default:\n    external: true\n")
	services, err := applications.ImportDockerComposeProject(t.Context(), createdApplication.ID, contents)
	if err != nil {
		t.Fatal(err)
	}
	if runner.configCalls != 1 {
		t.Fatalf("ConfigServices() calls = %d, want 1", runner.configCalls)
	}
	if len(services) != 2 || services[0].Name != "web" || services[1].Name != "worker" {
		t.Fatalf("imported services = %#v, want web and worker in Compose order", services)
	}
	if services[0].Type != application.ServiceTypeApplication || services[0].ImageName != "nginx:1.27" || services[1].ImageName != "busybox:1.36" {
		t.Fatalf("imported service metadata = %#v, want application services with image references", services)
	}

	directory := filepath.Join(root, applicationsDir, "status")
	composeContents := readServiceFile(t, filepath.Join(directory, "compose.yml"))
	for _, serviceName := range []string{"web", "worker"} {
		if !strings.Contains(composeContents, "container_name: redbolt-1-"+serviceName) {
			t.Fatalf("Compose file has no managed container name for %s: %s", serviceName, composeContents)
		}
	}
	if strings.Count(composeContents, "- vars.env") != 2 || strings.Count(composeContents, "- secrets.env") != 2 {
		t.Fatalf("Compose env_file entries = %q, want both files for each service", composeContents)
	}
	if strings.Count(composeContents, "redlaunch.managed=true") != 2 {
		t.Fatalf("Compose managed labels = %q, want one label per service", composeContents)
	}
	if !strings.Contains(composeContents, "8080:80") || !strings.Contains(composeContents, "echo ready") {
		t.Fatalf("imported Compose configuration lost service fields: %s", composeContents)
	}
	for _, name := range []string{varsEnvFile, secretsEnvFile} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("%s was not created: %v", name, err)
		}
	}
}

func TestApplicationsImportDockerComposeProjectRollsBackOnValidationFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &serviceRuntimeRunner{configErr: errors.New("invalid Compose project")}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	createdApplication, err := applications.Create(t.Context(), "Status", "status")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, applicationsDir, "status")
	originalCompose := readServiceFile(t, filepath.Join(directory, "compose.yml"))

	_, err = applications.ImportDockerComposeProject(t.Context(), createdApplication.ID, []byte("services:\n  web:\n    image: nginx:1.27\n"))
	if err == nil {
		t.Fatal("ImportDockerComposeProject() error = nil, want validation failure")
	}
	if len(repository.services) != 0 {
		t.Fatalf("persisted services after failed import = %#v, want none", repository.services)
	}
	if got := readServiceFile(t, filepath.Join(directory, "compose.yml")); got != originalCompose {
		t.Fatalf("Compose file after failed import = %q, want original %q", got, originalCompose)
	}
	for _, name := range []string{varsEnvFile, secretsEnvFile} {
		if got := readServiceFile(t, filepath.Join(directory, name)); got != "" {
			t.Fatalf("%s after failed import = %q, want empty original file", name, got)
		}
	}
}

func TestApplicationsImportDockerComposeProjectRollsBackOnMetadataFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{serviceErr: errors.New("database unavailable")}
	runner := &serviceRuntimeRunner{configured: []compose.ConfiguredService{{Name: "web", Image: "nginx:1.27"}}}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	createdApplication, err := applications.Create(t.Context(), "Status", "status")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, applicationsDir, "status")
	originalCompose := readServiceFile(t, filepath.Join(directory, "compose.yml"))

	_, err = applications.ImportDockerComposeProject(t.Context(), createdApplication.ID, []byte("services:\n  web:\n    image: nginx:1.27\n"))
	if err == nil {
		t.Fatal("ImportDockerComposeProject() error = nil, want metadata failure")
	}
	if len(repository.services) != 0 {
		t.Fatalf("persisted services after failed metadata write = %#v, want none", repository.services)
	}
	if got := readServiceFile(t, filepath.Join(directory, "compose.yml")); got != originalCompose {
		t.Fatalf("Compose file after failed metadata write = %q, want original %q", got, originalCompose)
	}
}

func TestApplicationsImportDockerComposeProjectRejectsExistingServices(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &serviceRuntimeRunner{configured: []compose.ConfiguredService{{Name: "web", Image: "nginx:1.27"}}}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	createdApplication, err := applications.Create(t.Context(), "Status", "status")
	if err != nil {
		t.Fatal(err)
	}
	repository.services = []application.Service{{ID: 1, ApplicationID: createdApplication.ID, Name: "existing"}}

	_, err = applications.ImportDockerComposeProject(t.Context(), createdApplication.ID, []byte("services:\n  web:\n    image: nginx:1.27\n"))
	if !errors.Is(err, application.ErrComposeServicesAlreadyExist) {
		t.Fatalf("ImportDockerComposeProject() error = %v, want %v", err, application.ErrComposeServicesAlreadyExist)
	}
	if runner.configCalls != 0 {
		t.Fatalf("ConfigServices() calls = %d, want 0 for an application with services", runner.configCalls)
	}
}

func TestApplicationsCreateRejectsTraversalAndExistingDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := applications.Create(t.Context(), "Outside", "../outside"); !errors.Is(err, application.ErrFolderNameInvalid) {
		t.Fatalf("Create(traversal) error = %v, want %v", err, application.ErrFolderNameInvalid)
	}
	if len(repository.applications) != 0 {
		t.Fatalf("repository applications = %#v, want none after traversal", repository.applications)
	}

	existing := filepath.Join(root, applicationsDir, "existing")
	if err := os.Mkdir(existing, 0o750); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(existing, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := applications.Create(t.Context(), "Existing", "existing"); !errors.Is(err, application.ErrAlreadyExists) {
		t.Fatalf("Create(existing) error = %v, want %v", err, application.ErrAlreadyExists)
	}
	if got := readServiceFile(t, sentinel); got != "keep" {
		t.Fatalf("existing sentinel = %q, want keep", got)
	}
}

func TestApplicationsCreateCleansUpWhenPersistenceFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{err: errors.New("database unavailable")}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := applications.Create(t.Context(), "Status page", "status-page"); err == nil {
		t.Fatal("Create() error = nil, want persistence error")
	}
	if _, err := os.Stat(filepath.Join(root, applicationsDir, "status-page")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("application directory stat error = %v, want not exist", err)
	}
}

func TestApplicationsDetailsReturnsApplicationAndServices(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 1, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 1, ApplicationID: 1, Name: "web"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	gotApplication, err := applications.Get(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if gotApplication.Name != "Status page" || gotApplication.FolderName != "status-page" {
		t.Fatalf("Get() = %#v, want stored application", gotApplication)
	}

	gotServices, err := applications.ListServices(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotServices) != 1 || gotServices[0].Name != "web" {
		t.Fatalf("ListServices() = %#v, want web service", gotServices)
	}
}

func TestApplicationsCreateListAndDeleteDomains(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	created, err := applications.CreateDomain(t.Context(), 7, " Example.COM ")
	if err != nil {
		t.Fatal(err)
	}
	if created != (application.Domain{ID: 1, ApplicationID: 7, Name: "example.com"}) {
		t.Fatalf("CreateDomain() = %#v, want normalized domain", created)
	}
	if repository.domain != created {
		t.Fatalf("stored domain = %#v, want %#v", repository.domain, created)
	}

	domains, err := applications.ListDomains(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 1 || domains[0] != created {
		t.Fatalf("ListDomains() = %#v, want %#v", domains, []application.Domain{created})
	}

	if err := applications.DeleteDomain(t.Context(), 7, " EXAMPLE.COM "); err != nil {
		t.Fatal(err)
	}
	if repository.deletedDomainID != 7 || repository.deletedDomainName != "example.com" {
		t.Fatalf("deleted domain = (%d, %q), want (7, example.com)", repository.deletedDomainID, repository.deletedDomainName)
	}
}

func TestApplicationsCreateDomainRejectsInvalidName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := applications.CreateDomain(t.Context(), 7, "example.com/path"); !errors.Is(err, application.ErrDomainNameInvalid) {
		t.Fatalf("CreateDomain(invalid) error = %v, want %v", err, application.ErrDomainNameInvalid)
	}
	if repository.domain.ID != 0 {
		t.Fatalf("stored domain after rejected create = %#v, want none", repository.domain)
	}
}

func TestApplicationsCreateUpdateAndDeleteRoutingRefreshesCaddy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		domains:      []application.Domain{{ID: 1, ApplicationID: 7, Name: "example.com"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "frontend"}, {ID: 2, ApplicationID: 7, Name: "identity"}},
	}
	runner := &serviceRuntimeRunner{}
	prepareRoutingProxy(t, root)
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}

	created, err := applications.CreateRouting(t.Context(), 7, 1, application.RoutingInput{
		Subdomain:   "api",
		Path:        "/register",
		ServiceName: "identity",
		ServicePath: "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != 1 || created.DomainName != "example.com" || created.Subdomain != "api" {
		t.Fatalf("CreateRouting() = %#v, want persisted routing", created)
	}
	if len(runner.reloads) != 1 {
		t.Fatalf("Caddy reloads after create = %d, want 1", len(runner.reloads))
	}
	caddyPath := filepath.Join(root, coreDir, proxyDir, "Caddyfile")
	caddy := readServiceFile(t, caddyPath)
	for _, expected := range []string{
		"api.example.com {",
		"path /register",
		"rewrite * /",
		"reverse_proxy redbolt-7-identity",
	} {
		if !strings.Contains(caddy, expected) {
			t.Fatalf("Caddyfile does not contain %q:\n%s", expected, caddy)
		}
	}

	if err := applications.UpdateRouting(t.Context(), 7, 1, created.ID, application.RoutingInput{
		Path:        "/",
		ServiceName: "frontend",
		ServicePath: "/app",
	}); err != nil {
		t.Fatal(err)
	}
	if len(runner.reloads) != 2 {
		t.Fatalf("Caddy reloads after update = %d, want 2", len(runner.reloads))
	}
	caddy = readServiceFile(t, caddyPath)
	for _, expected := range []string{"example.com {", "path /", "rewrite * /app", "reverse_proxy redbolt-7-frontend"} {
		if !strings.Contains(caddy, expected) {
			t.Fatalf("updated Caddyfile does not contain %q:\n%s", expected, caddy)
		}
	}
	if strings.Contains(caddy, "api.example.com") || strings.Contains(caddy, "/register") {
		t.Fatalf("updated Caddyfile retained the previous route:\n%s", caddy)
	}

	if err := applications.DeleteRouting(t.Context(), 7, 1, created.ID); err != nil {
		t.Fatal(err)
	}
	if len(runner.reloads) != 3 {
		t.Fatalf("Caddy reloads after delete = %d, want 3", len(runner.reloads))
	}
	if caddy := readServiceFile(t, caddyPath); caddy != "# Routes managed by Redlaunch.\n" {
		t.Fatalf("Caddyfile after delete = %q, want managed header only", caddy)
	}
}

func TestApplicationsUpdateRedlaunchPublicAccessRefreshesCaddy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &serviceRuntimeRunner{}
	prepareRoutingProxy(t, root)
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}

	if err := applications.UpdateRedlaunchPublicAccess(t.Context(), application.RedlaunchPublicAccessInput{
		Enabled: true,
		Domain:  " Admin.Example.COM ",
	}); err != nil {
		t.Fatal(err)
	}
	if repository.publicAccess != (application.RedlaunchPublicAccess{Enabled: true, Domain: "admin.example.com"}) {
		t.Fatalf("stored public access = %#v, want enabled normalized domain", repository.publicAccess)
	}
	if len(runner.reloads) != 1 {
		t.Fatalf("Caddy reloads after enabling public access = %d, want 1", len(runner.reloads))
	}
	caddyPath := filepath.Join(root, coreDir, proxyDir, "Caddyfile")
	caddy := readServiceFile(t, caddyPath)
	for _, expected := range []string{"admin.example.com {", "handle {", "reverse_proxy http://host.docker.internal:8080"} {
		if !strings.Contains(caddy, expected) {
			t.Fatalf("Caddyfile does not contain %q:\n%s", expected, caddy)
		}
	}

	if err := applications.UpdateRedlaunchPublicAccess(t.Context(), application.RedlaunchPublicAccessInput{
		Domain: "admin.example.com",
	}); err != nil {
		t.Fatal(err)
	}
	if repository.publicAccess.Enabled {
		t.Fatalf("stored public access after disabling = %#v, want disabled", repository.publicAccess)
	}
	if got := readServiceFile(t, caddyPath); got != "# Routes managed by Redlaunch.\n" {
		t.Fatalf("Caddyfile after disabling public access = %q, want managed header only", got)
	}
}

func TestApplicationsUpdateRedlaunchPublicAccessValidatesAndRollsBack(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &serviceRuntimeRunner{reloadErr: errors.New("Caddy unavailable")}
	prepareRoutingProxy(t, root)
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}

	if err := applications.UpdateRedlaunchPublicAccess(t.Context(), application.RedlaunchPublicAccessInput{Enabled: true}); !errors.Is(err, application.ErrRedlaunchPublicDomainRequired) {
		t.Fatalf("UpdateRedlaunchPublicAccess(missing domain) error = %v, want %v", err, application.ErrRedlaunchPublicDomainRequired)
	}
	if err := applications.UpdateRedlaunchPublicAccess(t.Context(), application.RedlaunchPublicAccessInput{Enabled: true, Domain: "bad/domain"}); !errors.Is(err, application.ErrRedlaunchPublicDomainInvalid) {
		t.Fatalf("UpdateRedlaunchPublicAccess(invalid domain) error = %v, want %v", err, application.ErrRedlaunchPublicDomainInvalid)
	}
	if err := applications.UpdateRedlaunchPublicAccess(t.Context(), application.RedlaunchPublicAccessInput{Enabled: true, Domain: "admin.example.com"}); err == nil {
		t.Fatal("UpdateRedlaunchPublicAccess(Caddy failure) error = nil, want error")
	}
	if repository.publicAccess != (application.RedlaunchPublicAccess{}) {
		t.Fatalf("public access after Caddy failure = %#v, want previous settings", repository.publicAccess)
	}
	if got := readServiceFile(t, filepath.Join(root, coreDir, proxyDir, "Caddyfile")); got != "# Routes managed by Redlaunch.\n" {
		t.Fatalf("Caddyfile after failed update = %q, want original contents", got)
	}
}

func TestApplicationsRoutingValidationAndCaddyFailureDoNotPersist(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		domains:      []application.Domain{{ID: 1, ApplicationID: 7, Name: "example.com"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "frontend"}},
	}
	runner := &serviceRuntimeRunner{reloadErr: errors.New("Caddy unavailable")}
	prepareRoutingProxy(t, root)
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := applications.CreateRouting(t.Context(), 7, 1, application.RoutingInput{Path: "register", ServiceName: "frontend", ServicePath: "/"}); !errors.Is(err, application.ErrRoutingPathInvalid) {
		t.Fatalf("CreateRouting(invalid path) error = %v, want %v", err, application.ErrRoutingPathInvalid)
	}
	if len(repository.routings) != 0 {
		t.Fatalf("routings after validation failure = %#v, want none", repository.routings)
	}

	if _, err := applications.CreateRouting(t.Context(), 7, 1, application.RoutingInput{Path: "/", ServiceName: "frontend", ServicePath: "/"}); err == nil {
		t.Fatal("CreateRouting(Caddy failure) error = nil, want error")
	}
	if len(repository.routings) != 0 {
		t.Fatalf("routings after Caddy failure = %#v, want none", repository.routings)
	}
	if got := readServiceFile(t, filepath.Join(root, coreDir, proxyDir, "Caddyfile")); got != "# Routes managed by Redlaunch.\n" {
		t.Fatalf("Caddyfile after failed create = %q, want original contents", got)
	}
}

func prepareRoutingProxy(t *testing.T, root string) {
	t.Helper()
	directory := filepath.Join(root, coreDir, proxyDir)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  proxy:\n    extra_hosts:\n      - \"host.docker.internal:host-gateway\"\n    volumes:\n      - caddy_data:/data\n      - caddy_config:/config\n      - ./Caddyfile:/etc/caddy/Caddyfile:ro\n"
	if err := os.WriteFile(filepath.Join(directory, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "Caddyfile"), []byte("# Routes managed by Redlaunch.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWriteManagedFileInPlacePreservesFileIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := writeManagedFileInPlace(path, "new\n", 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("writeManagedFileInPlace replaced the existing file")
	}
	if got := readServiceFile(t, path); got != "new\n" {
		t.Fatalf("file contents = %q, want new contents", got)
	}
}

func TestAddProxyHostGatewayUpgradesExistingCompose(t *testing.T) {
	compose := "services:\n  proxy:\n    image: caddy:2.11.4-alpine\n    ports:\n      - \"80:80\"\n"
	updated, changed, err := addProxyHostGateway(compose)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("addProxyHostGateway() changed = false, want true")
	}
	if !strings.Contains(updated, "  proxy:\n    extra_hosts:\n      - \"host.docker.internal:host-gateway\"\n    image:") {
		t.Fatalf("updated proxy Compose does not contain host gateway under proxy service:\n%s", updated)
	}

	unchanged, changed, err := addProxyHostGateway(updated)
	if err != nil {
		t.Fatal(err)
	}
	if changed || unchanged != updated {
		t.Fatalf("addProxyHostGateway(existing) = (%q, %t), want unchanged false", unchanged, changed)
	}
}

func TestApplicationsGetEnvironmentFilesReadsVariablesAndSecrets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, varsEnvFile), []byte("# application settings\nAPP_NAME=Status page\nexport PASSWORD_LIKE_VARIABLE=visible-value\nEMPTY=\nPORT=8080 # local port\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, secretsEnvFile), []byte("API_TOKEN=super-secret\nDATABASE_URL=\"postgres://user:password@db/app\"\n"), envFileMode); err != nil {
		t.Fatal(err)
	}

	got, err := applications.GetEnvironmentFiles(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !got.VariablesAvailable || !got.SecretsAvailable {
		t.Fatalf("environment file availability = (%v, %v), want both available", got.VariablesAvailable, got.SecretsAvailable)
	}
	if len(got.Variables) != 4 {
		t.Fatalf("variables = %#v, want four parsed entries", got.Variables)
	}
	if got.Variables[0] != (application.EnvironmentVariable{Key: "APP_NAME", Value: "Status page"}) {
		t.Fatalf("first variable = %#v, want APP_NAME with value", got.Variables[0])
	}
	if got.Variables[1] != (application.EnvironmentVariable{Key: "PASSWORD_LIKE_VARIABLE", Value: "visible-value"}) {
		t.Fatalf("password-like variable = %#v, want unmasked value", got.Variables[1])
	}
	if got.Variables[2] != (application.EnvironmentVariable{Key: "EMPTY", Value: ""}) || got.Variables[3] != (application.EnvironmentVariable{Key: "PORT", Value: "8080"}) {
		t.Fatalf("remaining variables = %#v, want empty and inline-comment values", got.Variables[2:])
	}
	if len(got.Secrets) != 2 {
		t.Fatalf("secrets = %#v, want two parsed entries", got.Secrets)
	}
	for _, secret := range got.Secrets {
		if !secret.Sensitive {
			t.Fatalf("secret = %#v, want Sensitive=true", secret)
		}
	}
	if got.Secrets[0].Value != "super-secret" || got.Secrets[1].Value != "postgres://user:password@db/app" {
		t.Fatalf("secret values = %#v, want parsed values", got.Secrets)
	}
}

func TestApplicationsUpdateEnvironmentVariablePreservesFileStructure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	varsPath := filepath.Join(directory, varsEnvFile)
	original := "\ufeff# application settings\r\nexport APP_NAME=Status page\r\nPORT=8080 # local port\r\nEMPTY=\r\n"
	if err := os.WriteFile(varsPath, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := applications.UpdateEnvironmentVariable(t.Context(), 7, "APP_NAME", "APP_TITLE", "New title #1"); err != nil {
		t.Fatal(err)
	}

	want := "\ufeff# application settings\r\nexport APP_TITLE=\"New title #1\"\r\nPORT=8080 # local port\r\nEMPTY=\r\n"
	if got := readServiceFile(t, varsPath); got != want {
		t.Fatalf("updated vars.env = %q, want %q", got, want)
	}
	if got := serviceFilePermissions(t, varsPath); got != 0o640 {
		t.Fatalf("updated vars.env permissions = %o, want %o", got, 0o640)
	}
}

func TestApplicationsUpdateEnvironmentVariableRejectsUnsafeOrAmbiguousUpdates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	varsPath := filepath.Join(directory, varsEnvFile)
	original := "APP_NAME=Status page\nAPP_TITLE=Existing\n"
	if err := os.WriteFile(varsPath, []byte(original), envFileMode); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		original    string
		updatedName string
		value       string
		wantErr     error
	}{
		{name: "duplicate name", original: "APP_NAME", updatedName: "APP_TITLE", value: "new", wantErr: application.ErrEnvironmentVariableAlreadyExists},
		{name: "missing variable", original: "MISSING", updatedName: "APP_NEW", value: "new", wantErr: application.ErrEnvironmentVariableNotFound},
		{name: "invalid name", original: "APP_NAME", updatedName: "../outside", value: "new", wantErr: application.ErrEnvironmentVariableNameInvalid},
		{name: "control character", original: "APP_NAME", updatedName: "APP_NEW", value: "line\nvalue", wantErr: application.ErrEnvironmentVariableValueInvalid},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if err := applications.UpdateEnvironmentVariable(t.Context(), 7, testCase.original, testCase.updatedName, testCase.value); !errors.Is(err, testCase.wantErr) {
				t.Fatalf("UpdateEnvironmentVariable() error = %v, want %v", err, testCase.wantErr)
			}
			if got := readServiceFile(t, varsPath); got != original {
				t.Fatalf("vars.env after rejected update = %q, want %q", got, original)
			}
		})
	}
}

func TestApplicationsAddEnvironmentVariableAppendsToVariablesFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	varsPath := filepath.Join(directory, varsEnvFile)
	if err := os.WriteFile(varsPath, nil, envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := applications.AddEnvironmentVariable(t.Context(), 7, "APP_EMPTY", ""); err != nil {
		t.Fatal(err)
	}
	if got := readServiceFile(t, varsPath); got != "APP_EMPTY=\"\"\n" {
		t.Fatalf("vars.env after adding to empty file = %q, want %q", got, "APP_EMPTY=\"\"\n")
	}

	original := "\ufeff# application settings\r\nAPP_NAME=Status page"
	if err := os.Chmod(varsPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(varsPath, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	secretsPath := filepath.Join(directory, secretsEnvFile)
	secrets := "API_TOKEN=keep-me\n"
	if err := os.WriteFile(secretsPath, []byte(secrets), envFileMode); err != nil {
		t.Fatal(err)
	}

	if err := applications.AddEnvironmentVariable(t.Context(), 7, "APP_TITLE", "New title #1"); err != nil {
		t.Fatal(err)
	}

	want := "\ufeff# application settings\r\nAPP_NAME=Status page\r\nAPP_TITLE=\"New title #1\"\r\n"
	if got := readServiceFile(t, varsPath); got != want {
		t.Fatalf("vars.env after add = %q, want %q", got, want)
	}
	if got := serviceFilePermissions(t, varsPath); got != 0o640 {
		t.Fatalf("vars.env permissions after add = %o, want %o", got, 0o640)
	}
	if got := readServiceFile(t, secretsPath); got != secrets {
		t.Fatalf("secrets.env after add = %q, want unchanged contents", got)
	}
}

func TestApplicationsAddEnvironmentVariableRejectsDuplicateAndUnsafeValues(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	varsPath := filepath.Join(directory, varsEnvFile)
	original := "APP_NAME=Status page\n"
	if err := os.WriteFile(varsPath, []byte(original), envFileMode); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		value   string
		wantErr error
	}{
		{name: "duplicate", value: "new", wantErr: application.ErrEnvironmentVariableAlreadyExists},
		{name: "invalid name", value: "new", wantErr: application.ErrEnvironmentVariableNameInvalid},
		{name: "control character", value: "line\nvalue", wantErr: application.ErrEnvironmentVariableValueInvalid},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			name := "APP_NAME"
			if testCase.name == "invalid name" {
				name = "../outside"
			} else if testCase.name == "control character" {
				name = "APP_NEW"
			}
			if err := applications.AddEnvironmentVariable(t.Context(), 7, name, testCase.value); !errors.Is(err, testCase.wantErr) {
				t.Fatalf("AddEnvironmentVariable() error = %v, want %v", err, testCase.wantErr)
			}
			if got := readServiceFile(t, varsPath); got != original {
				t.Fatalf("vars.env after rejected add = %q, want %q", got, original)
			}
		})
	}
}

func TestApplicationsDeleteEnvironmentVariablePreservesFileStructure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	varsPath := filepath.Join(directory, varsEnvFile)
	original := "\ufeff# application settings\r\nexport APP_NAME=Status page\r\nPORT=8080 # local port\r\nEMPTY=\r\n"
	if err := os.WriteFile(varsPath, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	secretsPath := filepath.Join(directory, secretsEnvFile)
	secrets := "API_TOKEN=keep-me\n"
	if err := os.WriteFile(secretsPath, []byte(secrets), envFileMode); err != nil {
		t.Fatal(err)
	}

	if err := applications.DeleteEnvironmentVariable(t.Context(), 7, "APP_NAME"); err != nil {
		t.Fatal(err)
	}

	want := "\ufeff# application settings\r\nPORT=8080 # local port\r\nEMPTY=\r\n"
	if got := readServiceFile(t, varsPath); got != want {
		t.Fatalf("deleted vars.env = %q, want %q", got, want)
	}
	if got := serviceFilePermissions(t, varsPath); got != 0o640 {
		t.Fatalf("deleted vars.env permissions = %o, want %o", got, 0o640)
	}
	if got := readServiceFile(t, secretsPath); got != secrets {
		t.Fatalf("secrets.env after delete = %q, want unchanged contents", got)
	}
}

func TestApplicationsDeleteEnvironmentVariableRejectsUnsafeOrAmbiguousDeletes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	varsPath := filepath.Join(directory, varsEnvFile)

	tests := []struct {
		name     string
		contents string
		target   string
		wantErr  error
	}{
		{name: "missing variable", contents: "APP_NAME=Status page\n", target: "MISSING", wantErr: application.ErrEnvironmentVariableNotFound},
		{name: "invalid name", contents: "APP_NAME=Status page\n", target: "../outside", wantErr: application.ErrEnvironmentVariableNameInvalid},
		{name: "duplicate variable", contents: "APP_NAME=one\nAPP_NAME=two\n", target: "APP_NAME", wantErr: application.ErrEnvironmentVariableDuplicate},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if err := os.WriteFile(varsPath, []byte(testCase.contents), envFileMode); err != nil {
				t.Fatal(err)
			}
			if err := applications.DeleteEnvironmentVariable(t.Context(), 7, testCase.target); !errors.Is(err, testCase.wantErr) {
				t.Fatalf("DeleteEnvironmentVariable() error = %v, want %v", err, testCase.wantErr)
			}
			if got := readServiceFile(t, varsPath); got != testCase.contents {
				t.Fatalf("vars.env after rejected delete = %q, want %q", got, testCase.contents)
			}
		})
	}
}

func TestApplicationsUpdateEnvironmentSecretPreservesFileStructure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	secretsPath := filepath.Join(directory, secretsEnvFile)
	original := "\ufeff# application secrets\r\nexport API_TOKEN=old-value\r\nKEEP=unchanged # keep this comment\r\n"
	if err := os.WriteFile(secretsPath, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	varsPath := filepath.Join(directory, varsEnvFile)
	vars := "APP_NAME=Status page\n"
	if err := os.WriteFile(varsPath, []byte(vars), envFileMode); err != nil {
		t.Fatal(err)
	}

	if err := applications.UpdateEnvironmentSecret(t.Context(), 7, "API_TOKEN", "API_KEY", "new secret #1"); err != nil {
		t.Fatal(err)
	}

	want := "\ufeff# application secrets\r\nexport API_KEY=\"new secret #1\"\r\nKEEP=unchanged # keep this comment\r\n"
	if got := readServiceFile(t, secretsPath); got != want {
		t.Fatalf("updated secrets.env = %q, want %q", got, want)
	}
	if got := serviceFilePermissions(t, secretsPath); got != 0o640 {
		t.Fatalf("updated secrets.env permissions = %o, want %o", got, 0o640)
	}
	if got := readServiceFile(t, varsPath); got != vars {
		t.Fatalf("vars.env after secret update = %q, want unchanged contents %q", got, vars)
	}
}

func TestApplicationsAddAndDeleteEnvironmentSecret(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	secretsPath := filepath.Join(directory, secretsEnvFile)
	if err := os.WriteFile(secretsPath, []byte("# managed secrets\nEXISTING=keep\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := applications.AddEnvironmentSecret(t.Context(), 7, "API_TOKEN", "generated-value"); err != nil {
		t.Fatal(err)
	}
	if got := readServiceFile(t, secretsPath); got != "# managed secrets\nEXISTING=keep\nAPI_TOKEN=generated-value\n" {
		t.Fatalf("secrets.env after add = %q, want appended secret", got)
	}
	if got := serviceFilePermissions(t, secretsPath); got != 0o640 {
		t.Fatalf("secrets.env permissions after add = %o, want %o", got, 0o640)
	}

	if err := applications.DeleteEnvironmentSecret(t.Context(), 7, "EXISTING"); err != nil {
		t.Fatal(err)
	}
	if got := readServiceFile(t, secretsPath); got != "# managed secrets\nAPI_TOKEN=generated-value\n" {
		t.Fatalf("secrets.env after delete = %q, want remaining secret", got)
	}
}

func TestApplicationsEnvironmentSecretRejectsUnsafeUpdates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	secretsPath := filepath.Join(directory, secretsEnvFile)
	original := "API_TOKEN=keep\n"
	if err := os.WriteFile(secretsPath, []byte(original), envFileMode); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		original    string
		updatedName string
		value       string
		wantErr     error
	}{
		{name: "invalid original name", original: "../outside", updatedName: "API_KEY", value: "new", wantErr: application.ErrEnvironmentVariableNameInvalid},
		{name: "invalid updated name", original: "API_TOKEN", updatedName: "../outside", value: "new", wantErr: application.ErrEnvironmentVariableNameInvalid},
		{name: "control character", original: "API_TOKEN", updatedName: "API_KEY", value: "line\nvalue", wantErr: application.ErrEnvironmentVariableValueInvalid},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if err := applications.UpdateEnvironmentSecret(t.Context(), 7, testCase.original, testCase.updatedName, testCase.value); !errors.Is(err, testCase.wantErr) {
				t.Fatalf("UpdateEnvironmentSecret() error = %v, want %v", err, testCase.wantErr)
			}
			if got := readServiceFile(t, secretsPath); got != original {
				t.Fatalf("secrets.env after rejected update = %q, want %q", got, original)
			}
		})
	}
}

func TestApplicationsImportEnvironmentFilesReplacesSelectedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	varsPath := filepath.Join(directory, varsEnvFile)
	secretsPath := filepath.Join(directory, secretsEnvFile)
	if err := os.WriteFile(varsPath, []byte("OLD=value\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretsPath, []byte("OLD_SECRET=old\n"), envFileMode); err != nil {
		t.Fatal(err)
	}

	variables := "# imported variables\r\nexport APP_NAME=Status page\r\nPORT=8080 # local port\r\n"
	secrets := "# imported secrets\nAPI_TOKEN=super-secret\n"
	if err := applications.ImportEnvironmentFiles(t.Context(), 7, application.EnvironmentFileImportInput{
		Variables:         []byte(variables),
		VariablesProvided: true,
		Secrets:           []byte(secrets),
		SecretsProvided:   true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := readServiceFile(t, varsPath); got != variables {
		t.Fatalf("vars.env after import = %q, want uploaded contents", got)
	}
	if got := readServiceFile(t, secretsPath); got != secrets {
		t.Fatalf("secrets.env after import = %q, want uploaded contents", got)
	}
	if got := serviceFilePermissions(t, varsPath); got != 0o640 {
		t.Fatalf("vars.env permissions after import = %o, want existing permissions", got)
	}
	if got := serviceFilePermissions(t, secretsPath); got != envFileMode {
		t.Fatalf("secrets.env permissions after import = %o, want existing permissions", got)
	}

	if err := applications.ImportEnvironmentFiles(t.Context(), 7, application.EnvironmentFileImportInput{
		Variables:         []byte("APP_NAME=updated\n"),
		VariablesProvided: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := readServiceFile(t, varsPath); got != "APP_NAME=updated\n" {
		t.Fatalf("vars.env after variables-only import = %q, want updated contents", got)
	}
	if got := readServiceFile(t, secretsPath); got != secrets {
		t.Fatalf("secrets.env after variables-only import = %q, want unchanged contents", got)
	}
}

func TestApplicationsImportEnvironmentFilesCreatesBothFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := applications.ImportEnvironmentFiles(t.Context(), 7, application.EnvironmentFileImportInput{
		Secrets:         []byte("API_TOKEN=secret\n"),
		SecretsProvided: true,
	}); err != nil {
		t.Fatal(err)
	}

	varsPath := filepath.Join(directory, varsEnvFile)
	secretsPath := filepath.Join(directory, secretsEnvFile)
	if got := readServiceFile(t, varsPath); got != "" {
		t.Fatalf("created vars.env = %q, want empty file", got)
	}
	if got := readServiceFile(t, secretsPath); got != "API_TOKEN=secret\n" {
		t.Fatalf("created secrets.env = %q, want uploaded contents", got)
	}
	for _, path := range []string{varsPath, secretsPath} {
		if got := serviceFilePermissions(t, path); got != envFileMode {
			t.Fatalf("created environment file %s permissions = %o, want %o", path, got, envFileMode)
		}
	}
}

func TestApplicationsImportEnvironmentFilesRejectsInvalidInputWithoutChangingFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	varsPath := filepath.Join(directory, varsEnvFile)
	secretsPath := filepath.Join(directory, secretsEnvFile)
	originalVars := "KEEP=variable\n"
	originalSecrets := "KEEP_SECRET=secret\n"
	if err := os.WriteFile(varsPath, []byte(originalVars), envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretsPath, []byte(originalSecrets), envFileMode); err != nil {
		t.Fatal(err)
	}

	for _, contents := range [][]byte{
		[]byte("BROKEN=\"unterminated\n"),
		[]byte("DUPLICATE=one\nDUPLICATE=two\n"),
	} {
		err := applications.ImportEnvironmentFiles(t.Context(), 7, application.EnvironmentFileImportInput{
			Variables:         []byte("NEW=value\n"),
			VariablesProvided: true,
			Secrets:           contents,
			SecretsProvided:   true,
		})
		if !errors.Is(err, application.ErrEnvironmentImportInvalid) {
			t.Fatalf("ImportEnvironmentFiles() error = %v, want invalid environment file", err)
		}
		if got := readServiceFile(t, varsPath); got != originalVars {
			t.Fatalf("vars.env after rejected import = %q, want unchanged contents", got)
		}
		if got := readServiceFile(t, secretsPath); got != originalSecrets {
			t.Fatalf("secrets.env after rejected import = %q, want unchanged contents", got)
		}
	}

	if err := applications.ImportEnvironmentFiles(t.Context(), 7, application.EnvironmentFileImportInput{}); !errors.Is(err, application.ErrEnvironmentImportFileRequired) {
		t.Fatalf("empty ImportEnvironmentFiles() error = %v, want required-file error", err)
	}
	tooLarge := strings.Repeat("A", application.MaxEnvironmentFileSize+1)
	if err := applications.ImportEnvironmentFiles(t.Context(), 7, application.EnvironmentFileImportInput{
		Variables:         []byte(tooLarge),
		VariablesProvided: true,
	}); !errors.Is(err, application.ErrEnvironmentImportFileTooLarge) {
		t.Fatalf("oversized ImportEnvironmentFiles() error = %v, want too-large error", err)
	}
}

func TestApplicationsGetEnvironmentFilesRejectsSymlinkedEnvironmentFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, secretsEnvFile+".outside"), []byte("API_TOKEN=super-secret\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretsEnvFile+".outside", filepath.Join(directory, secretsEnvFile)); err != nil {
		t.Fatal(err)
	}

	if _, err := applications.GetEnvironmentFiles(t.Context(), 7); err == nil {
		t.Fatal("GetEnvironmentFiles() error = nil, want symlink rejection")
	}
}

func TestApplicationsListServicesEnrichesDockerRuntimeDetails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services: []application.Service{{
			ID:            1,
			ApplicationID: 7,
			Name:          "db",
			Type:          application.ServiceTypePostgreSQL,
			CreatedAt:     time.Date(2026, time.August, 30, 7, 20, 0, 0, time.UTC),
		}},
	}
	runner := &serviceRuntimeRunner{runtime: []compose.ServiceRuntime{{
		ServiceName:   "db",
		ContainerName: "redbolt-7-db",
		CreatedAt:     time.Date(2026, time.August, 30, 7, 25, 29, 0, time.UTC),
		Status:        "Up 9 minutes",
		Ports:         "5432/tcp",
	}}}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, applicationsDir, "status-page"), 0o750); err != nil {
		t.Fatal(err)
	}

	services, err := applications.ListServices(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("ListServices() returned %d services, want 1", len(services))
	}
	got := services[0]
	if got.ContainerName != "redbolt-7-db" || !got.ContainerCreatedAt.Equal(runner.runtime[0].CreatedAt) || got.Status != "Up 9 minutes" || got.Ports != "5432/tcp" {
		t.Fatalf("ListServices() runtime = %#v, want Docker runtime details", got)
	}
	wantDirectory := filepath.Join(root, applicationsDir, "status-page")
	if runner.projectDir != wantDirectory {
		t.Fatalf("runtime inspection directory = %q, want %q", runner.projectDir, wantDirectory)
	}
}

func TestApplicationsGetProxyDetailsIncludesRuntimeAndRoutedDomainMappings(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{
			{ID: 7, Name: "Status page"},
			{ID: 8, Name: "Admin"},
		},
		routings: []application.Routing{
			{ID: 1, ApplicationID: 7, DomainName: "example.com", Path: "/", ServiceName: "web", ServicePath: "/"},
			{ID: 2, ApplicationID: 7, DomainName: "example.com", Subdomain: "api", Path: "/v1", ServiceName: "api", ServicePath: "/api"},
			{ID: 3, ApplicationID: 8, DomainName: "admin.example.com", Path: "/admin", ServiceName: "dashboard", ServicePath: "/"},
		},
	}
	runner := &serviceRuntimeRunner{runtime: []compose.ServiceRuntime{
		{
			ServiceName:   "proxy",
			ContainerName: "redbolt-proxy",
			CreatedAt:     time.Date(2026, time.August, 30, 7, 25, 29, 0, time.UTC),
			Status:        "Up 9 minutes",
			Image:         "caddy:2.11.4-alpine",
			Ports:         "0.0.0.0:80->80/tcp, [::]:443->443/tcp",
		},
	}, logs: "proxy started\n"}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}

	details, err := applications.GetProxyDetails(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if details.ContainerName != "redbolt-proxy" || details.ImageName != "caddy:2.11.4-alpine" || details.Status != "Up 9 minutes" || details.Ports == "" {
		t.Fatalf("GetProxyDetails() runtime = %#v, want proxy runtime details", details)
	}
	wantCreatedAt := time.Date(2026, time.August, 30, 7, 25, 29, 0, time.UTC)
	if !details.CreatedAt.Equal(wantCreatedAt) {
		t.Fatalf("GetProxyDetails() CreatedAt = %s, want %s", details.CreatedAt, wantCreatedAt)
	}
	if !details.LogsAvailable || details.Logs != "proxy started\n" || runner.logsTail != application.ProxyLogLineLimit {
		t.Fatalf("GetProxyDetails() logs = (%v, %q), tail = %d, want bounded proxy logs", details.LogsAvailable, details.Logs, runner.logsTail)
	}
	wantDomains := []application.ProxyDomain{
		{Name: "example.com", ApplicationName: "Status page", RequestPath: "/", MappedService: "web", MappedPath: "/"},
		{Name: "api.example.com", ApplicationName: "Status page", RequestPath: "/v1", MappedService: "api", MappedPath: "/api"},
		{Name: "admin.example.com", ApplicationName: "Admin", RequestPath: "/admin", MappedService: "dashboard", MappedPath: "/"},
	}
	if !reflect.DeepEqual(details.Domains, wantDomains) {
		t.Fatalf("GetProxyDetails() domains = %#v, want %#v", details.Domains, wantDomains)
	}
	wantDirectory := filepath.Join(root, coreDir, proxyDir)
	if runner.projectDir != wantDirectory {
		t.Fatalf("proxy runtime inspection directory = %q, want %q", runner.projectDir, wantDirectory)
	}
}

func TestApplicationsProxyActionsUseTheManagedProxyService(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	runner := &serviceRuntimeRunner{}
	applications, err := NewApplications(&applicationRepositoryStub{}, root, runner)
	if err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name   string
		action func(context.Context) error
		want   string
	}{
		{name: "start", action: applications.StartProxy, want: "start"},
		{name: "stop", action: applications.StopProxy, want: "stop"},
		{name: "restart", action: applications.RestartProxy, want: "restart"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.action(t.Context()); err != nil {
				t.Fatal(err)
			}
			if runner.action != testCase.want || runner.service != proxyDir {
				t.Fatalf("proxy action = (%q, %q), want (%q, %q)", runner.action, runner.service, testCase.want, proxyDir)
			}
			wantDirectory := filepath.Join(root, coreDir, proxyDir)
			if runner.projectDir != wantDirectory {
				t.Fatalf("proxy action directory = %q, want %q", runner.projectDir, wantDirectory)
			}
		})
	}
}

func TestApplicationsGetProxyFullLogsReturnsUnboundedLogs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	runner := &serviceRuntimeRunner{allLogs: "old line\nnew line\n"}
	applications, err := NewApplications(&applicationRepositoryStub{}, root, runner)
	if err != nil {
		t.Fatal(err)
	}

	logs, err := applications.GetProxyFullLogs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if logs != "old line\nnew line\n" || !runner.allLogsCalled {
		t.Fatalf("GetProxyFullLogs() = (%q, called=%t), want all runner logs", logs, runner.allLogsCalled)
	}
	wantDirectory := filepath.Join(root, coreDir, proxyDir)
	if runner.projectDir != wantDirectory {
		t.Fatalf("full proxy logs directory = %q, want %q", runner.projectDir, wantDirectory)
	}
}

func TestApplicationsGetServiceDetailsIncludesRuntimeLogsAndEnvironment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "db", CreatedAt: time.Date(2026, time.August, 30, 7, 20, 0, 0, time.UTC)}},
	}
	runner := &serviceRuntimeRunner{
		runtime: []compose.ServiceRuntime{{
			ServiceName:   "db",
			ContainerName: "redbolt-7-db",
			CreatedAt:     time.Date(2026, time.August, 30, 7, 25, 29, 0, time.UTC),
			Status:        "Up 9 minutes",
			Ports:         "5432/tcp",
		}},
		logs: "database system is ready\n",
		environment: []compose.EnvironmentVariable{
			{Key: "POSTGRES_PASSWORD", Value: "server-secret"},
			{Key: "POSTGRES_DB", Value: "status"},
		},
	}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, applicationsDir, "status-page"), 0o750); err != nil {
		t.Fatal(err)
	}

	details, err := applications.GetServiceDetails(t.Context(), 7, "db")
	if err != nil {
		t.Fatal(err)
	}
	if details.Service.ContainerName != "redbolt-7-db" || details.Service.Status != "Up 9 minutes" || details.Service.Ports != "5432/tcp" {
		t.Fatalf("service details runtime = %#v, want Docker runtime details", details.Service)
	}
	if !details.LogsAvailable || details.Logs != "database system is ready\n" || runner.logsTail != application.ServiceLogLineLimit {
		t.Fatalf("service details logs = (%v, %q), tail = %d, want available logs and tail %d", details.LogsAvailable, details.Logs, runner.logsTail, application.ServiceLogLineLimit)
	}
	if !details.EnvironmentAvailable || len(details.Environment) != 2 {
		t.Fatalf("service details environment = (%v, %#v), want two variables", details.EnvironmentAvailable, details.Environment)
	}
	if details.Environment[0].Key != "POSTGRES_DB" || details.Environment[0].Sensitive {
		t.Fatalf("database environment variable = %#v, want non-sensitive POSTGRES_DB", details.Environment[0])
	}
	if details.Environment[1].Key != "POSTGRES_PASSWORD" || !details.Environment[1].Sensitive {
		t.Fatalf("password environment variable = %#v, want sensitive POSTGRES_PASSWORD", details.Environment[1])
	}
}

func TestApplicationsGetServiceFullLogsReturnsCompleteHistory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "db"}},
	}
	runner := &serviceRuntimeRunner{allLogs: "oldest line\nnewest line\n"}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, applicationsDir, "status-page"), 0o750); err != nil {
		t.Fatal(err)
	}

	logs, err := applications.GetServiceFullLogs(t.Context(), 7, "db")
	if err != nil {
		t.Fatal(err)
	}
	if logs != runner.allLogs || !runner.allLogsCalled {
		t.Fatalf("GetServiceFullLogs() = (%q, called=%t), want complete runner logs", logs, runner.allLogsCalled)
	}
	wantDirectory := filepath.Join(root, applicationsDir, "status-page")
	if runner.projectDir != wantDirectory {
		t.Fatalf("full service logs directory = %q, want %q", runner.projectDir, wantDirectory)
	}
}

func TestApplicationsGetServiceDetailsRejectsUnknownService(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "db"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := applications.GetServiceDetails(t.Context(), 7, "web"); !errors.Is(err, application.ErrServiceNotFound) {
		t.Fatalf("GetServiceDetails(unknown) error = %v, want %v", err, application.ErrServiceNotFound)
	}
	if _, err := applications.GetServiceDetails(t.Context(), 7, "../outside"); !errors.Is(err, application.ErrServiceNameInvalid) {
		t.Fatalf("GetServiceDetails(traversal) error = %v, want %v", err, application.ErrServiceNameInvalid)
	}
}

func TestApplicationsControlsRegisteredServices(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		action string
		run    func(*Applications) error
	}{
		{name: "start", action: "start", run: func(applications *Applications) error {
			return applications.StartService(context.Background(), 7, "db")
		}},
		{name: "stop", action: "stop", run: func(applications *Applications) error {
			return applications.StopService(context.Background(), 7, "db")
		}},
		{name: "restart", action: "restart", run: func(applications *Applications) error {
			return applications.RestartService(context.Background(), 7, "db")
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "projects")
			repository := &applicationRepositoryStub{
				applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
				services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "db"}},
			}
			runner := &serviceRuntimeRunner{}
			applications, err := NewApplications(repository, root, runner)
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(root, applicationsDir, "status-page")
			if err := os.Mkdir(directory, 0o750); err != nil {
				t.Fatal(err)
			}

			if err := testCase.run(applications); err != nil {
				t.Fatal(err)
			}
			if runner.action != testCase.action || runner.service != "db" || runner.projectDir != directory {
				t.Fatalf("service action = (%q, %q, %q), want (%q, db, %q)", runner.action, runner.service, runner.projectDir, testCase.action, directory)
			}
		})
	}
}

func TestApplicationsRejectsUnregisteredServiceActions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "db"}},
	}
	runner := &serviceRuntimeRunner{}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, applicationsDir, "status-page"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := applications.StartService(context.Background(), 7, "web"); !errors.Is(err, application.ErrServiceNotFound) {
		t.Fatalf("StartService(unregistered) error = %v, want %v", err, application.ErrServiceNotFound)
	}
	if err := applications.DeleteService(context.Background(), 7, "web"); !errors.Is(err, application.ErrServiceNotFound) {
		t.Fatalf("DeleteService(unregistered) error = %v, want %v", err, application.ErrServiceNotFound)
	}
	if runner.action != "" {
		t.Fatalf("unregistered service action = %q, want no action", runner.action)
	}
	if err := applications.StartService(context.Background(), 7, "../outside"); !errors.Is(err, application.ErrServiceNameInvalid) {
		t.Fatalf("StartService(traversal) error = %v, want %v", err, application.ErrServiceNameInvalid)
	}
}

func TestApplicationsDeletesRegisteredServiceInOrder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "db"}},
	}
	runner := &serviceRuntimeRunner{}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte("services:\n  db:\n    image: postgres:17\n    volumes:\n      - db_data:/var/lib/postgresql/data\n  keep:\n    image: nginx:1.27\n    volumes:\n      - shared_data:/data\n\nnetworks:\n  default:\n    external: true\n\nvolumes:\n  db_data:\n    name: redbolt-7-db-data\n  shared_data:\n    name: redbolt-7-shared-data\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := applications.DeleteService(context.Background(), 7, "db"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(runner.actions, "\x00") != "stop\x00remove" {
		t.Fatalf("service deletion actions = %v, want stop then remove", runner.actions)
	}
	if runner.projectDir != directory || runner.service != "db" {
		t.Fatalf("service deletion target = (%q, %q), want (%q, db)", runner.projectDir, runner.service, directory)
	}
	if repository.deletedID != 7 || repository.deletedName != "db" || len(repository.services) != 0 {
		t.Fatalf("deleted service metadata = (%d, %q), remaining = %#v; want application 7/db and none", repository.deletedID, repository.deletedName, repository.services)
	}
	composeContents := readServiceFile(t, filepath.Join(directory, "compose.yml"))
	if strings.Contains(composeContents, "\n  db:\n") {
		t.Fatalf("deleted service remains in Compose file:\n%s", composeContents)
	}
	if strings.Contains(composeContents, "db_data") {
		t.Fatalf("deleted service volume remains in Compose file:\n%s", composeContents)
	}
	if !strings.Contains(composeContents, "\n  keep:\n") || !strings.Contains(composeContents, "shared_data") || !strings.Contains(composeContents, "networks:\n") {
		t.Fatalf("Compose file lost unrelated definitions:\n%s", composeContents)
	}
}

func TestApplicationsStopsDeletionWhenAStageFails(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		runner     func(*serviceRuntimeRunner)
		repository func(*applicationRepositoryStub)
		wantStages string
	}{
		{name: "stop", runner: func(runner *serviceRuntimeRunner) { runner.stopErr = errors.New("stop failed") }, wantStages: "stop"},
		{name: "remove", runner: func(runner *serviceRuntimeRunner) { runner.removeErr = errors.New("remove failed") }, wantStages: "stop\x00remove"},
		{name: "metadata", repository: func(repository *applicationRepositoryStub) { repository.deleteErr = errors.New("database unavailable") }, wantStages: "stop\x00remove"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "projects")
			repository := &applicationRepositoryStub{
				applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
				services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "db"}},
			}
			if testCase.repository != nil {
				testCase.repository(repository)
			}
			runner := &serviceRuntimeRunner{}
			if testCase.runner != nil {
				testCase.runner(runner)
			}
			applications, err := NewApplications(repository, root, runner)
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(root, applicationsDir, "status-page")
			if err := os.Mkdir(directory, 0o750); err != nil {
				t.Fatal(err)
			}
			composePath := filepath.Join(directory, "compose.yml")
			originalCompose := "services:\n  db:\n    image: postgres:17\n"
			if err := os.WriteFile(composePath, []byte(originalCompose), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := applications.DeleteService(context.Background(), 7, "db"); err == nil {
				t.Fatal("DeleteService() error = nil, want failure")
			}
			if strings.Join(runner.actions, "\x00") != testCase.wantStages {
				t.Fatalf("service deletion actions = %v, want %q", runner.actions, testCase.wantStages)
			}
			if repository.deletedName != "" {
				t.Fatalf("service metadata deleted after failed %s stage: %q", testCase.name, repository.deletedName)
			}
			if got := readServiceFile(t, composePath); got != originalCompose {
				t.Fatalf("Compose file after failed %s stage = %q, want %q", testCase.name, got, originalCompose)
			}
		})
	}
}

func TestApplicationsDeletesApplicationResourcesMetadataAndFolderInOrder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "db"}},
	}
	runner := &serviceRuntimeRunner{}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "keep-me.txt"), []byte("removed with the application"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stages []string
	if err := applications.DeleteApplicationWithProgress(context.Background(), 7, func(stage, _ string) {
		stages = append(stages, stage)
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(runner.actions, "\x00") != "down" {
		t.Fatalf("application deletion actions = %v, want down", runner.actions)
	}
	if runner.projectDir != directory {
		t.Fatalf("application deletion target = %q, want %q", runner.projectDir, directory)
	}
	if strings.Join(stages, "\x00") != "resources\x00metadata\x00folder" {
		t.Fatalf("application deletion stages = %v, want resources, metadata, folder", stages)
	}
	if repository.deletedApplicationID != 7 || len(repository.applications) != 0 {
		t.Fatalf("deleted application = %d, remaining = %#v; want application 7 and none", repository.deletedApplicationID, repository.applications)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("application directory stat error = %v, want not exist", err)
	}
}

func TestApplicationsStopsApplicationDeletionBeforeMetadataWhenAStageFails(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		runnerErr     error
		repositoryErr error
		wantStages    string
		wantAction    string
	}{
		{name: "resources", runnerErr: errors.New("Docker unavailable"), wantStages: "resources", wantAction: "down"},
		{name: "metadata", repositoryErr: errors.New("database unavailable"), wantStages: "resources\x00metadata", wantAction: "down"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "projects")
			repository := &applicationRepositoryStub{
				applications:         []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
				applicationDeleteErr: testCase.repositoryErr,
			}
			runner := &serviceRuntimeRunner{downErr: testCase.runnerErr}
			applications, err := NewApplications(repository, root, runner)
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(root, applicationsDir, "status-page")
			if err := os.Mkdir(directory, 0o750); err != nil {
				t.Fatal(err)
			}
			composePath := filepath.Join(directory, "compose.yml")
			if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			var stages []string
			if err := applications.DeleteApplicationWithProgress(context.Background(), 7, func(stage, _ string) {
				stages = append(stages, stage)
			}); err == nil {
				t.Fatal("DeleteApplicationWithProgress() error = nil, want failure")
			}
			if strings.Join(runner.actions, "\x00") != testCase.wantAction {
				t.Fatalf("application deletion actions = %v, want %q", runner.actions, testCase.wantAction)
			}
			if strings.Join(stages, "\x00") != testCase.wantStages {
				t.Fatalf("application deletion stages = %v, want %q", stages, testCase.wantStages)
			}
			if repository.deletedApplicationID != 0 {
				t.Fatalf("application metadata deleted after failed %s stage: %d", testCase.name, repository.deletedApplicationID)
			}
			if _, err := os.Stat(directory); err != nil {
				t.Fatalf("application directory stat error = %v, want directory to remain", err)
			}
		})
	}
}

func TestRemoveServiceFromComposePreservesOtherDefinitionsAndCleansOwnedVolume(t *testing.T) {
	contents := "# managed application\nservices:\n  web:\n    image: nginx:1.27\n    labels:\n      - keep=this\n    volumes:\n      - shared_data:/data\n\n  db:\n    image: postgres:17\n    volumes:\n      - db_data:/var/lib/postgresql/data\n\nnetworks:\n  default:\n    external: true\n\nvolumes:\n  db_data:\n    name: redbolt-7-db-data\n  shared_data:\n    name: redbolt-7-shared-data\n"

	got, err := removeServiceFromCompose(contents, "db")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "\n  db:\n") || strings.Contains(got, "image: postgres:17") || strings.Contains(got, "db_data") {
		t.Fatalf("removed service remains in Compose file:\n%s", got)
	}
	for _, expected := range []string{"# managed application", "\n  web:\n", "image: nginx:1.27", "keep=this", "shared_data", "networks:\n", "volumes:\n"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("Compose file after service removal does not contain %q:\n%s", expected, got)
		}
	}
}

func TestRemoveServiceFromComposeLeavesEmptyServicesMapping(t *testing.T) {
	contents := "services:\n  db:\n    image: postgres:17\n    volumes:\n      - db_data:/var/lib/postgresql/data\n\nnetworks:\n  default:\n    external: true\n\nvolumes:\n  db_data:\n    name: redbolt-7-db-data\n"

	got, err := removeServiceFromCompose(contents, "db")
	if err != nil {
		t.Fatal(err)
	}
	if got != "services: {}\n\nnetworks:\n  default:\n    external: true\n" {
		t.Fatalf("Compose file after removing last service and volume = %q, want cleaned Compose file", got)
	}
}

func TestApplicationsCreatePostgreSQLServiceWritesFilesPersistsMetadataAndStarts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &recordingRunner{}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(t.Context(), "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}

	service, err := applications.CreatePostgreSQLService(t.Context(), created.ID, application.PostgreSQLServiceInput{
		ServiceName:      "db",
		PostgresVersion:  "17.2",
		DatabaseName:     "Status page",
		DatabaseUser:     "appuser",
		DatabasePassword: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.Type != application.ServiceTypePostgreSQL || service.PostgresVersion != "17.2" || service.DatabaseName != "Status page" || service.DatabaseUser != "appuser" {
		t.Fatalf("created service = %#v, want PostgreSQL metadata", service)
	}
	if repository.service.Type != application.ServiceTypePostgreSQL || repository.service.Name != "db" {
		t.Fatalf("persisted service = %#v, want PostgreSQL metadata", repository.service)
	}
	if len(runner.directories) != 1 || runner.directories[0] != filepath.Join(root, applicationsDir, "status-page") {
		t.Fatalf("started Compose directories = %v, want application directory", runner.directories)
	}
	if len(runner.services) != 1 || runner.services[0] != "db" {
		t.Fatalf("started Compose services = %v, want db", runner.services)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	composeContents := readServiceFile(t, filepath.Join(directory, "compose.yml"))
	for _, expected := range []string{
		"services:\n  db:",
		"image: postgres:17.2",
		"container_name: redbolt-1-db",
		"env_file:",
		"- vars.env",
		"- secrets.env",
		`test: ["CMD-SHELL", "pg_isready -U \"$$POSTGRES_USER\" -d \"$$POSTGRES_DB\""]`,
		"interval: 10s",
		"timeout: 5s",
		"retries: 5",
		"start_period: 30s",
		"db_data:/var/lib/postgresql/data",
		"redlaunch.managed=true",
		"name: redbolt-1-db-data",
	} {
		if !strings.Contains(composeContents, expected) {
			t.Errorf("Compose file does not contain %q:\n%s", expected, composeContents)
		}
	}
	varsContents := readServiceFile(t, filepath.Join(directory, varsEnvFile))
	for _, expected := range []string{"POSTGRES_DB=\"Status page\"", "POSTGRES_USER=appuser"} {
		if !strings.Contains(varsContents, expected) {
			t.Errorf("variables file does not contain %q: %s", expected, varsContents)
		}
	}
	if strings.Contains(varsContents, postgresEnvironmentPassword+"=") {
		t.Fatalf("variables file contains PostgreSQL password: %s", varsContents)
	}
	secretsContents := readServiceFile(t, filepath.Join(directory, secretsEnvFile))
	if !strings.Contains(secretsContents, "POSTGRES_PASSWORD=") {
		t.Fatalf("secrets file does not contain PostgreSQL password: %s", secretsContents)
	}
	passwordLine := ""
	for _, line := range strings.Split(secretsContents, "\n") {
		if strings.HasPrefix(line, "POSTGRES_PASSWORD=") {
			passwordLine = line
			break
		}
	}
	if passwordLine == "POSTGRES_PASSWORD=" || passwordLine == "POSTGRES_PASSWORD=\"\"" {
		t.Fatalf("generated password line = %q, want a non-empty secret", passwordLine)
	}
	if strings.Contains(varsContents, "Status page\n") {
		t.Fatalf("variables file contains an unquoted database name: %s", varsContents)
	}
	for _, name := range []string{varsEnvFile, secretsEnvFile} {
		if got := serviceFilePermissions(t, filepath.Join(directory, name)); got != envFileMode {
			t.Errorf("%s permissions = %o, want %o", name, got, envFileMode)
		}
	}
}

func TestApplicationsCreatePostgreSQLServiceStartsOnlyTheRequestedService(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &recordingRunner{}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(t.Context(), "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := applications.CreateApplicationService(t.Context(), created.ID, application.ApplicationServiceInput{
		ServiceName: "app",
		ImageName:   "localhost:5000/app:latest",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := applications.CreatePostgreSQLService(t.Context(), created.ID, application.PostgreSQLServiceInput{
		ServiceName:      "db",
		PostgresVersion:  "17",
		DatabaseName:     "status",
		DatabaseUser:     "appuser",
		DatabasePassword: "secret",
	}); err != nil {
		t.Fatal(err)
	}

	if len(runner.services) != 1 || runner.services[0] != "db" {
		t.Fatalf("started Compose services = %v, want only db", runner.services)
	}
	if len(runner.directories) != 1 {
		t.Fatalf("started Compose directories = %v, want one service start", runner.directories)
	}
	composeContents := readServiceFile(t, filepath.Join(root, applicationsDir, "status-page", "compose.yml"))
	for _, expected := range []string{"image: localhost:5000/app:latest", "image: postgres:17"} {
		if !strings.Contains(composeContents, expected) {
			t.Errorf("Compose file does not contain %q:\n%s", expected, composeContents)
		}
	}
}

func TestApplicationsCreatePostgreSQLServiceReportsProgressStages(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root, &recordingRunner{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(t.Context(), "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}

	var stages []string
	_, err = applications.CreatePostgreSQLServiceWithProgress(t.Context(), created.ID, application.PostgreSQLServiceInput{
		ServiceName:      "db",
		PostgresVersion:  "17",
		DatabaseName:     "status",
		DatabaseUser:     "appuser",
		DatabasePassword: "secret",
	}, func(stage, _ string) {
		stages = append(stages, stage)
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"configuration", "files", "metadata", "start"}
	if len(stages) != len(want) {
		t.Fatalf("PostgreSQL progress stages = %v, want %v", stages, want)
	}
	for index := range want {
		if stages[index] != want[index] {
			t.Errorf("PostgreSQL progress stage %d = %q, want %q", index, stages[index], want[index])
		}
	}
}

func TestApplicationsCreatePostgreSQLServicePreservesExistingEnvironmentAndPassword(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root, &recordingRunner{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(t.Context(), "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}
	varsPath := filepath.Join(root, applicationsDir, "status-page", varsEnvFile)
	secretsPath := filepath.Join(root, applicationsDir, "status-page", secretsEnvFile)
	if err := os.WriteFile(varsPath, []byte("# keep this setting\nAPP_ENV=production\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretsPath, []byte("# keep this secret\nEXISTING_SECRET=old\n"), envFileMode); err != nil {
		t.Fatal(err)
	}

	password := `pa$$ word "quoted"`
	if _, err := applications.CreatePostgreSQLService(t.Context(), created.ID, application.PostgreSQLServiceInput{
		ServiceName:      "db",
		PostgresVersion:  "17",
		DatabaseName:     "status",
		DatabaseUser:     "appuser",
		DatabasePassword: password,
	}); err != nil {
		t.Fatal(err)
	}

	varsContents := readServiceFile(t, varsPath)
	if !strings.Contains(varsContents, "# keep this setting") || !strings.Contains(varsContents, "APP_ENV=production") {
		t.Fatalf("existing variables were not preserved: %s", varsContents)
	}
	secretsContents := readServiceFile(t, secretsPath)
	if !strings.Contains(secretsContents, "# keep this secret") || !strings.Contains(secretsContents, "EXISTING_SECRET=old") {
		t.Fatalf("existing secrets were not preserved: %s", secretsContents)
	}
	if !strings.Contains(secretsContents, `POSTGRES_PASSWORD="pa$$$$ word \"quoted\""`) {
		t.Fatalf("supplied password was not safely encoded: %s", secretsContents)
	}
}

func TestApplicationsCreatePostgreSQLServiceLeavesFilesWhenMetadataPersistenceFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(t.Context(), "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(root, applicationsDir, "status-page", "compose.yml")
	varsPath := filepath.Join(root, applicationsDir, "status-page", varsEnvFile)
	secretsPath := filepath.Join(root, applicationsDir, "status-page", secretsEnvFile)
	originalCompose := readServiceFile(t, composePath)
	originalVars := readServiceFile(t, varsPath)
	originalSecrets := readServiceFile(t, secretsPath)
	repository.serviceErr = errors.New("database unavailable")

	if _, err := applications.CreatePostgreSQLService(t.Context(), created.ID, application.PostgreSQLServiceInput{
		ServiceName:     "db",
		PostgresVersion: "17",
		DatabaseName:    "status",
		DatabaseUser:    "appuser",
	}); err == nil {
		t.Fatal("CreatePostgreSQLService() error = nil, want persistence error")
	}
	if got := readServiceFile(t, composePath); got != originalCompose {
		t.Fatalf("Compose file after persistence failure = %q, want original %q", got, originalCompose)
	}
	if got := readServiceFile(t, varsPath); got != originalVars {
		t.Fatalf("variables file after persistence failure = %q, want original %q", got, originalVars)
	}
	if got := readServiceFile(t, secretsPath); got != originalSecrets {
		t.Fatalf("secrets file after persistence failure = %q, want original %q", got, originalSecrets)
	}
}

func TestApplicationsCreateRedisServiceWritesProductionComposeAndPersistsSettings(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &recordingRunner{}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(t.Context(), "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}

	service, err := applications.CreateRedisService(t.Context(), created.ID, application.RedisServiceInput{
		ServiceName:   "cache",
		RedisVersion:  "7.2-alpine",
		Port:          "6380",
		Password:      `pa$$ word "quoted"`,
		PersistToDisk: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.Type != application.ServiceTypeRedis || service.RedisVersion != "7.2-alpine" || service.RedisPort != "6380" || !service.RedisPersistToDisk {
		t.Fatalf("created service = %#v, want Redis metadata", service)
	}
	if repository.service.Type != application.ServiceTypeRedis || repository.service.Name != "cache" {
		t.Fatalf("persisted service = %#v, want Redis metadata", repository.service)
	}
	if len(runner.services) != 1 || runner.services[0] != "cache" {
		t.Fatalf("started Compose services = %v, want cache", runner.services)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	composeContents := readServiceFile(t, filepath.Join(directory, "compose.yml"))
	for _, expected := range []string{
		"services:\n  cache:",
		"image: redis:7.2-alpine",
		"container_name: redbolt-1-cache",
		"- \"127.0.0.1:6380:6379\"",
		"env_file:",
		"- vars.env",
		"- secrets.env",
		`test: ["CMD-SHELL", "if [ -n \"$$REDIS_PASSWORD\" ]; then REDISCLI_AUTH=\"$$REDIS_PASSWORD\" redis-cli --no-auth-warning ping; else redis-cli ping; fi"]`,
		"interval: 10s",
		"timeout: 5s",
		"retries: 5",
		"start_period: 30s",
		"--appendonly yes --save 1 1",
		"cache_data:/data",
		"redlaunch.managed=true",
		"name: redbolt-1-cache-data",
	} {
		if !strings.Contains(composeContents, expected) {
			t.Errorf("Compose file does not contain %q:\n%s", expected, composeContents)
		}
	}
	if strings.Contains(composeContents, "pa$$") || strings.Contains(composeContents, "quoted") {
		t.Fatalf("Compose file contains Redis password: %s", composeContents)
	}
	secretsContents := readServiceFile(t, filepath.Join(directory, secretsEnvFile))
	if !strings.Contains(secretsContents, `REDIS_PASSWORD="pa$$$$ word \"quoted\""`) {
		t.Fatalf("Redis password was not safely encoded: %s", secretsContents)
	}
	if got := serviceFilePermissions(t, filepath.Join(directory, secretsEnvFile)); got != envFileMode {
		t.Errorf("secrets.env permissions = %o, want %o", got, envFileMode)
	}
}

func TestApplicationsCreateRedisServiceWithoutPersistenceDoesNotCreateVolume(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root, &recordingRunner{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(t.Context(), "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := applications.CreateRedisService(t.Context(), created.ID, application.RedisServiceInput{
		ServiceName:  "cache",
		RedisVersion: "7",
		Port:         "6379",
	}); err != nil {
		t.Fatal(err)
	}
	composeContents := readServiceFile(t, filepath.Join(root, applicationsDir, "status-page", "compose.yml"))
	if !strings.Contains(composeContents, `"--protected-mode","no"`) {
		t.Fatalf("passwordless Redis Compose does not disable protected mode for the application network:\n%s", composeContents)
	}
	if strings.Contains(composeContents, "appendonly") || strings.Contains(composeContents, "save 1 1") || strings.Contains(composeContents, "/data") || strings.Contains(composeContents, "volumes:") {
		t.Fatalf("non-persistent Redis Compose contains persistence settings:\n%s", composeContents)
	}
}

func TestApplicationsCreateRedisServiceReportsProgressStages(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root, &recordingRunner{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(t.Context(), "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}

	var stages []string
	_, err = applications.CreateRedisServiceWithProgress(t.Context(), created.ID, application.RedisServiceInput{
		ServiceName:  "redis",
		RedisVersion: "7",
		Port:         "6379",
	}, func(stage, _ string) {
		stages = append(stages, stage)
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"configuration", "files", "metadata", "start"}
	if strings.Join(stages, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Redis progress stages = %v, want %v", stages, want)
	}
}

func readServiceFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func serviceFilePermissions(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}
