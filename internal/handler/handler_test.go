package handler

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"redlaunch/internal/application"
	redlaunchauth "redlaunch/internal/auth"
	systemmetrics "redlaunch/internal/metrics"
)

type fakeSetupManager struct {
	needsSetup      bool
	setupCalls      int
	installProxy    bool
	installRegistry bool
	err             error
	done            chan struct{}
}

type fakeProxyService struct {
	details        application.ProxyDetails
	err            error
	fullLogs       string
	fullLogsErr    error
	fullLogsCalled bool
	proxyAction    string
	proxyActionErr error
}

type fakeDashboardMetricsService struct {
	snapshot systemmetrics.Snapshot
	err      error
	calls    int
}

type fakeGitHubActionsService struct {
	integration                  application.GitHubActionsIntegration
	configured                   application.GitHubActionsSetup
	configureErr                 error
	configureInput               application.GitHubActionsInput
	configureCalls               int
	configureStarted             chan struct{}
	configureRelease             chan struct{}
	configureWaitForCancellation bool
	revokeCalls                  int
	cleanupCalls                 int
	workflow                     string
}

func (s *fakeGitHubActionsService) Get(context.Context, int64) (application.GitHubActionsIntegration, error) {
	if s.integration.ApplicationID == 0 {
		return application.GitHubActionsIntegration{}, application.ErrGitHubActionsNotConfigured
	}
	return s.integration, nil
}

func (s *fakeGitHubActionsService) Configure(ctx context.Context, _ int64, input application.GitHubActionsInput) (application.GitHubActionsSetup, error) {
	s.configureInput = input
	s.configureCalls++
	if s.configureStarted != nil {
		select {
		case s.configureStarted <- struct{}{}:
		default:
		}
	}
	if s.configureRelease != nil {
		<-s.configureRelease
	}
	if s.configureWaitForCancellation {
		<-ctx.Done()
		return application.GitHubActionsSetup{}, ctx.Err()
	}
	if s.configureErr != nil {
		return application.GitHubActionsSetup{}, s.configureErr
	}
	if s.configured.Integration.ApplicationID != 0 {
		s.integration = s.configured.Integration
	}
	return s.configured, nil
}

func (s *fakeGitHubActionsService) RenderWorkflow(context.Context, int64) (string, error) {
	if s.workflow == "" {
		return "", application.ErrGitHubActionsNotConfigured
	}
	return s.workflow, nil
}

func (s *fakeGitHubActionsService) Revoke(context.Context, int64) error {
	s.revokeCalls++
	s.integration = application.GitHubActionsIntegration{}
	return nil
}

func (s *fakeGitHubActionsService) CleanupApplicationKey(context.Context, int64) error {
	s.cleanupCalls++
	return nil
}

type fakeAuthenticationService struct {
	enabled                  bool
	completeUser             redlaunchauth.User
	completeEmail            string
	completeErr              error
	completeCalls            int
	sessionValue             string
	sessionErr               error
	validateEmail            string
	validateUser             redlaunchauth.User
	validateValid            bool
	validateErr              error
	validateCalls            int
	authorizationURL         string
	authorizationRedirectURL string
	completeRedirectURL      string
}

func (s *fakeAuthenticationService) Enabled() bool {
	return s.enabled
}

func (s *fakeAuthenticationService) AuthorizationURL(state string) string {
	return s.authorizationURL + "?state=" + url.QueryEscape(state)
}

func (s *fakeAuthenticationService) AuthorizationURLForRedirect(state, redirectURL string) string {
	s.authorizationRedirectURL = redirectURL
	return s.AuthorizationURL(state)
}

func (s *fakeAuthenticationService) CompleteLogin(context.Context, string) (redlaunchauth.User, error) {
	s.completeCalls++
	if s.completeUser.Email != "" {
		return s.completeUser, s.completeErr
	}
	return redlaunchauth.User{Email: s.completeEmail}, s.completeErr
}

func (s *fakeAuthenticationService) CompleteLoginForRedirect(ctx context.Context, code, redirectURL string) (redlaunchauth.User, error) {
	s.completeRedirectURL = redirectURL
	return s.CompleteLogin(ctx, code)
}

func (s *fakeAuthenticationService) NewSession(redlaunchauth.User) (string, error) {
	return s.sessionValue, s.sessionErr
}

func (s *fakeAuthenticationService) ValidateSession(context.Context, string) (redlaunchauth.User, bool, error) {
	s.validateCalls++
	if s.validateUser.Email != "" {
		return s.validateUser, s.validateValid, s.validateErr
	}
	return redlaunchauth.User{Email: s.validateEmail}, s.validateValid, s.validateErr
}

func (s *fakeAuthenticationService) CookieSecure() bool {
	return false
}

func (s *fakeAuthenticationService) SessionDuration() time.Duration {
	return time.Hour
}

func (s *fakeDashboardMetricsService) Collect(context.Context) (systemmetrics.Snapshot, error) {
	s.calls++
	return s.snapshot, s.err
}

func (s *fakeProxyService) GetProxyDetails(context.Context) (application.ProxyDetails, error) {
	return s.details, s.err
}

func (s *fakeProxyService) GetProxyFullLogs(context.Context) (string, error) {
	s.fullLogsCalled = true
	return s.fullLogs, s.fullLogsErr
}

func (s *fakeProxyService) StartProxy(context.Context) error {
	s.proxyAction = "start"
	return s.proxyActionErr
}

func (s *fakeProxyService) StopProxy(context.Context) error {
	s.proxyAction = "stop"
	return s.proxyActionErr
}

func (s *fakeProxyService) RestartProxy(context.Context) error {
	s.proxyAction = "restart"
	return s.proxyActionErr
}

type fakeApplicationService struct {
	applications                  []application.Application
	services                      []application.Service
	domains                       []application.Domain
	routings                      []application.Routing
	created                       application.Application
	createErr                     error
	importID                      int64
	importContents                []byte
	importedServices              []application.Service
	importErr                     error
	environmentImportInput        application.EnvironmentFileImportInput
	environmentImportErr          error
	listErr                       error
	getErr                        error
	servicesErr                   error
	domainsErr                    error
	domainCreateID                int64
	domainCreateName              string
	domainCreateErr               error
	domainDeleteID                int64
	domainDeleteName              string
	domainDeleteErr               error
	routingCreateID               int64
	routingCreateDomainID         int64
	routingCreateInput            application.RoutingInput
	routingCreateErr              error
	routingUpdateID               int64
	routingUpdateDomainID         int64
	routingUpdateInput            application.RoutingInput
	routingUpdateErr              error
	routingDeleteID               int64
	routingDeleteDomainID         int64
	routingDeleteErr              error
	routingListErr                error
	publicAccess                  application.RedlaunchPublicAccess
	publicAccessInput             application.RedlaunchPublicAccessInput
	publicAccessGetErr            error
	publicAccessUpdateErr         error
	serviceDetails                application.ServiceDetails
	serviceDetailsErr             error
	fullServiceLogs               string
	fullServiceLogsErr            error
	fullServiceLogsCalled         bool
	environmentFiles              application.EnvironmentFiles
	environmentFilesErr           error
	variableUpdateID              int64
	variableOriginalName          string
	variableUpdateName            string
	variableUpdateValue           string
	variableUpdateErr             error
	variableAddID                 int64
	variableAddName               string
	variableAddValue              string
	variableAddErr                error
	variableDeleteID              int64
	variableDeleteName            string
	variableDeleteErr             error
	variableMoveID                int64
	variableMoveName              string
	variableMoveErr               error
	secretUpdateID                int64
	secretOriginalName            string
	secretUpdateName              string
	secretUpdateValue             string
	secretUpdateReplaceValue      bool
	secretUpdateErr               error
	secretAddID                   int64
	secretAddName                 string
	secretAddValue                string
	secretAddErr                  error
	secretDeleteID                int64
	secretDeleteName              string
	secretDeleteErr               error
	secretMoveID                  int64
	secretMoveName                string
	secretMoveErr                 error
	postgres                      application.Service
	postgresInput                 application.PostgreSQLServiceInput
	postgresErr                   error
	postgresDone                  chan struct{}
	postgresStarted               chan struct{}
	postgresRelease               chan struct{}
	postgresStages                []string
	redis                         application.Service
	redisInput                    application.RedisServiceInput
	redisErr                      error
	redisDone                     chan struct{}
	redisStarted                  chan struct{}
	redisRelease                  chan struct{}
	redisStages                   []string
	applicationContainer          application.Service
	applicationContainerInput     application.ApplicationServiceInput
	applicationContainerErr       error
	applicationContainerDone      chan struct{}
	applicationContainerStarted   chan struct{}
	applicationContainerRelease   chan struct{}
	applicationContainerStages    []string
	serviceAction                 string
	serviceActionID               int64
	serviceActionName             string
	serviceActionErr              error
	serviceDeleteID               int64
	serviceDeleteName             string
	serviceDeleteErr              error
	serviceDeleteDone             chan struct{}
	serviceDeleteStarted          chan struct{}
	serviceDeleteRelease          chan struct{}
	serviceDeleteStages           []string
	applicationDeleteID           int64
	applicationDeleteErr          error
	applicationDeleteRemoveApp    bool
	applicationDeleteDone         chan struct{}
	applicationDeleteStarted      chan struct{}
	applicationDeleteRelease      chan struct{}
	applicationDeleteStages       []string
	postgresStartOnce             sync.Once
	postgresDoneOnce              sync.Once
	redisStartOnce                sync.Once
	redisDoneOnce                 sync.Once
	applicationContainerStartOnce sync.Once
	applicationContainerDoneOnce  sync.Once
	serviceDeleteStartOnce        sync.Once
	serviceDeleteDoneOnce         sync.Once
	applicationDeleteStartOnce    sync.Once
	applicationDeleteDoneOnce     sync.Once
}

func (s *fakeApplicationService) List(context.Context) ([]application.Application, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.applications, nil
}

func (s *fakeApplicationService) Create(_ context.Context, name, folderName string) (application.Application, error) {
	if s.createErr != nil {
		return application.Application{}, s.createErr
	}
	s.created = application.Application{Name: name, FolderName: folderName}
	return s.created, nil
}

func (s *fakeApplicationService) ImportDockerComposeProject(_ context.Context, id int64, contents []byte) ([]application.Service, error) {
	s.importID = id
	s.importContents = append([]byte(nil), contents...)
	return s.importedServices, s.importErr
}

func (s *fakeApplicationService) ImportEnvironmentFiles(_ context.Context, id int64, input application.EnvironmentFileImportInput) error {
	s.environmentImportInput = application.EnvironmentFileImportInput{
		Variables:         append([]byte(nil), input.Variables...),
		VariablesProvided: input.VariablesProvided,
		Secrets:           append([]byte(nil), input.Secrets...),
		SecretsProvided:   input.SecretsProvided,
	}
	return s.environmentImportErr
}

func (s *fakeApplicationService) Get(_ context.Context, id int64) (application.Application, error) {
	if s.getErr != nil {
		return application.Application{}, s.getErr
	}
	for _, item := range s.applications {
		if item.ID == id {
			return item, nil
		}
	}
	return application.Application{}, application.ErrNotFound
}

func (s *fakeApplicationService) ListServices(_ context.Context, _ int64) ([]application.Service, error) {
	if s.servicesErr != nil {
		return nil, s.servicesErr
	}
	return s.services, nil
}

func (s *fakeApplicationService) ListDomains(_ context.Context, _ int64) ([]application.Domain, error) {
	if s.domainsErr != nil {
		return nil, s.domainsErr
	}
	return s.domains, nil
}

func (s *fakeApplicationService) CreateDomain(_ context.Context, id int64, name string) (application.Domain, error) {
	s.domainCreateID = id
	s.domainCreateName = name
	if s.domainCreateErr != nil {
		return application.Domain{}, s.domainCreateErr
	}
	return application.Domain{ApplicationID: id, Name: name}, nil
}

func (s *fakeApplicationService) DeleteDomain(_ context.Context, id int64, name string) error {
	s.domainDeleteID = id
	s.domainDeleteName = name
	return s.domainDeleteErr
}

func (s *fakeApplicationService) ListRoutings(_ context.Context, _ int64, domainID int64) ([]application.Routing, error) {
	if s.routingListErr != nil {
		return nil, s.routingListErr
	}
	var routings []application.Routing
	for _, item := range s.routings {
		if item.DomainID == domainID {
			routings = append(routings, item)
		}
	}
	return routings, nil
}

func (s *fakeApplicationService) CreateRouting(_ context.Context, applicationID, domainID int64, input application.RoutingInput) (application.Routing, error) {
	s.routingCreateID = applicationID
	s.routingCreateDomainID = domainID
	s.routingCreateInput = input
	if s.routingCreateErr != nil {
		return application.Routing{}, s.routingCreateErr
	}
	return application.Routing{ApplicationID: applicationID, DomainID: domainID, Subdomain: input.Subdomain, Path: input.Path, ServiceName: input.ServiceName, ServicePort: input.ServicePort, ServicePath: input.ServicePath}, nil
}

func (s *fakeApplicationService) UpdateRouting(_ context.Context, applicationID, domainID, routingID int64, input application.RoutingInput) error {
	s.routingUpdateID = routingID
	s.routingUpdateDomainID = domainID
	s.routingUpdateInput = input
	return s.routingUpdateErr
}

func (s *fakeApplicationService) DeleteRouting(_ context.Context, applicationID, domainID, routingID int64) error {
	s.routingDeleteID = routingID
	s.routingDeleteDomainID = domainID
	return s.routingDeleteErr
}

func (s *fakeApplicationService) GetRedlaunchPublicAccess(context.Context) (application.RedlaunchPublicAccess, error) {
	return s.publicAccess, s.publicAccessGetErr
}

func (s *fakeApplicationService) UpdateRedlaunchPublicAccess(_ context.Context, input application.RedlaunchPublicAccessInput) error {
	s.publicAccessInput = input
	return s.publicAccessUpdateErr
}

func (s *fakeApplicationService) GetEnvironmentFiles(_ context.Context, _ int64) (application.EnvironmentFiles, error) {
	if s.environmentFilesErr != nil {
		return application.EnvironmentFiles{}, s.environmentFilesErr
	}
	return s.environmentFiles, nil
}

func (s *fakeApplicationService) UpdateEnvironmentVariable(_ context.Context, id int64, originalName, name, value string) error {
	s.variableUpdateID = id
	s.variableOriginalName = originalName
	s.variableUpdateName = name
	s.variableUpdateValue = value
	return s.variableUpdateErr
}

func (s *fakeApplicationService) AddEnvironmentVariable(_ context.Context, id int64, name, value string) error {
	s.variableAddID = id
	s.variableAddName = name
	s.variableAddValue = value
	return s.variableAddErr
}

func (s *fakeApplicationService) DeleteEnvironmentVariable(_ context.Context, id int64, name string) error {
	s.variableDeleteID = id
	s.variableDeleteName = name
	return s.variableDeleteErr
}

func (s *fakeApplicationService) MoveEnvironmentVariableToSecrets(_ context.Context, id int64, name string) error {
	s.variableMoveID = id
	s.variableMoveName = name
	return s.variableMoveErr
}

func (s *fakeApplicationService) UpdateEnvironmentSecret(_ context.Context, id int64, originalName, name, value string) error {
	return s.updateEnvironmentSecret(id, originalName, name, value, true)
}

func (s *fakeApplicationService) UpdateEnvironmentSecretValue(_ context.Context, id int64, originalName, name, value string, replaceValue bool) error {
	return s.updateEnvironmentSecret(id, originalName, name, value, replaceValue)
}

func (s *fakeApplicationService) updateEnvironmentSecret(id int64, originalName, name, value string, replaceValue bool) error {
	s.secretUpdateID = id
	s.secretOriginalName = originalName
	s.secretUpdateName = name
	s.secretUpdateValue = value
	s.secretUpdateReplaceValue = replaceValue
	return s.secretUpdateErr
}

func (s *fakeApplicationService) AddEnvironmentSecret(_ context.Context, id int64, name, value string) error {
	s.secretAddID = id
	s.secretAddName = name
	s.secretAddValue = value
	return s.secretAddErr
}

func (s *fakeApplicationService) DeleteEnvironmentSecret(_ context.Context, id int64, name string) error {
	s.secretDeleteID = id
	s.secretDeleteName = name
	return s.secretDeleteErr
}

func (s *fakeApplicationService) MoveEnvironmentSecretToVariables(_ context.Context, id int64, name string) error {
	s.secretMoveID = id
	s.secretMoveName = name
	return s.secretMoveErr
}

func (s *fakeApplicationService) GetServiceDetails(_ context.Context, _ int64, _ string) (application.ServiceDetails, error) {
	if s.serviceDetailsErr != nil {
		return application.ServiceDetails{}, s.serviceDetailsErr
	}
	return s.serviceDetails, nil
}

func (s *fakeApplicationService) GetServiceFullLogs(context.Context, int64, string) (string, error) {
	s.fullServiceLogsCalled = true
	return s.fullServiceLogs, s.fullServiceLogsErr
}

func (s *fakeApplicationService) StartService(_ context.Context, id int64, serviceName string) error {
	s.serviceAction = "start"
	s.serviceActionID = id
	s.serviceActionName = serviceName
	return s.serviceActionErr
}

func (s *fakeApplicationService) StopService(_ context.Context, id int64, serviceName string) error {
	s.serviceAction = "stop"
	s.serviceActionID = id
	s.serviceActionName = serviceName
	return s.serviceActionErr
}

func (s *fakeApplicationService) RestartService(_ context.Context, id int64, serviceName string) error {
	s.serviceAction = "restart"
	s.serviceActionID = id
	s.serviceActionName = serviceName
	return s.serviceActionErr
}

func (s *fakeApplicationService) DeleteService(_ context.Context, id int64, serviceName string) error {
	s.serviceDeleteID = id
	s.serviceDeleteName = serviceName
	return s.serviceDeleteErr
}

func (s *fakeApplicationService) DeleteServiceWithProgress(ctx context.Context, id int64, serviceName string, progress func(stage, message string)) error {
	stages := s.serviceDeleteStages
	if len(stages) == 0 {
		stages = []string{"stop", "remove", "compose", "metadata"}
	}
	for _, stage := range stages {
		progress(stage, stage)
	}
	s.serviceDeleteID = id
	s.serviceDeleteName = serviceName
	if s.serviceDeleteStarted != nil {
		s.serviceDeleteStartOnce.Do(func() { close(s.serviceDeleteStarted) })
	}
	if s.serviceDeleteRelease != nil {
		<-s.serviceDeleteRelease
	}
	if s.serviceDeleteDone != nil {
		s.serviceDeleteDoneOnce.Do(func() { close(s.serviceDeleteDone) })
	}
	return s.serviceDeleteErr
}

func (s *fakeApplicationService) DeleteApplication(_ context.Context, id int64) error {
	s.applicationDeleteID = id
	return s.applicationDeleteErr
}

func (s *fakeApplicationService) DeleteApplicationWithProgress(_ context.Context, id int64, progress func(stage, message string)) error {
	stages := s.applicationDeleteStages
	if len(stages) == 0 {
		stages = []string{"resources", "metadata", "folder"}
	}
	for _, stage := range stages {
		progress(stage, stage)
	}
	s.applicationDeleteID = id
	if s.applicationDeleteStarted != nil {
		s.applicationDeleteStartOnce.Do(func() { close(s.applicationDeleteStarted) })
	}
	if s.applicationDeleteRelease != nil {
		<-s.applicationDeleteRelease
	}
	if s.applicationDeleteRemoveApp && s.applicationDeleteErr == nil {
		s.applications = nil
	}
	if s.applicationDeleteDone != nil {
		s.applicationDeleteDoneOnce.Do(func() { close(s.applicationDeleteDone) })
	}
	return s.applicationDeleteErr
}

func (s *fakeApplicationService) CreatePostgreSQLService(_ context.Context, _ int64, input application.PostgreSQLServiceInput) (application.Service, error) {
	if s.postgresStarted != nil {
		s.postgresStartOnce.Do(func() { close(s.postgresStarted) })
	}
	if s.postgresRelease != nil {
		<-s.postgresRelease
	}
	s.postgresInput = input
	if s.postgresDone != nil {
		s.postgresDoneOnce.Do(func() { close(s.postgresDone) })
	}
	if s.postgresErr != nil {
		return application.Service{}, s.postgresErr
	}
	return s.postgres, nil
}

func (s *fakeApplicationService) CreatePostgreSQLServiceWithProgress(ctx context.Context, id int64, input application.PostgreSQLServiceInput, progress func(stage, message string)) (application.Service, error) {
	stages := s.postgresStages
	if len(stages) == 0 {
		stages = []string{"configuration"}
	}
	for _, stage := range stages {
		progress(stage, stage)
	}
	return s.CreatePostgreSQLService(ctx, id, input)
}

func (s *fakeApplicationService) ValidatePostgreSQLServiceInput(input application.PostgreSQLServiceInput) error {
	if _, err := application.ValidateServiceName(input.ServiceName); err != nil {
		return err
	}
	if _, err := application.ValidatePostgresVersion(input.PostgresVersion); err != nil {
		return err
	}
	if _, err := application.ValidateDatabaseName(input.DatabaseName); err != nil {
		return err
	}
	if _, err := application.ValidateDatabaseUser(input.DatabaseUser); err != nil {
		return err
	}
	_, err := application.ValidateDatabasePassword(input.DatabasePassword)
	return err
}

func (s *fakeApplicationService) CreateRedisService(_ context.Context, _ int64, input application.RedisServiceInput) (application.Service, error) {
	if s.redisStarted != nil {
		s.redisStartOnce.Do(func() { close(s.redisStarted) })
	}
	if s.redisRelease != nil {
		<-s.redisRelease
	}
	s.redisInput = input
	if s.redisDone != nil {
		s.redisDoneOnce.Do(func() { close(s.redisDone) })
	}
	if s.redisErr != nil {
		return application.Service{}, s.redisErr
	}
	return s.redis, nil
}

func (s *fakeApplicationService) CreateRedisServiceWithProgress(ctx context.Context, id int64, input application.RedisServiceInput, progress func(stage, message string)) (application.Service, error) {
	stages := s.redisStages
	if len(stages) == 0 {
		stages = []string{"configuration"}
	}
	for _, stage := range stages {
		progress(stage, stage)
	}
	return s.CreateRedisService(ctx, id, input)
}

func (s *fakeApplicationService) ValidateRedisServiceInput(input application.RedisServiceInput) error {
	if _, err := application.ValidateServiceName(input.ServiceName); err != nil {
		return err
	}
	if _, err := application.ValidateRedisVersion(input.RedisVersion); err != nil {
		return err
	}
	if _, err := application.ValidateRedisPort(input.Port); err != nil {
		return err
	}
	_, err := application.ValidateRedisPassword(input.Password)
	return err
}

func (s *fakeApplicationService) CreateApplicationService(_ context.Context, _ int64, input application.ApplicationServiceInput) (application.Service, error) {
	if s.applicationContainerStarted != nil {
		s.applicationContainerStartOnce.Do(func() { close(s.applicationContainerStarted) })
	}
	if s.applicationContainerRelease != nil {
		<-s.applicationContainerRelease
	}
	s.applicationContainerInput = input
	if s.applicationContainerDone != nil {
		s.applicationContainerDoneOnce.Do(func() { close(s.applicationContainerDone) })
	}
	if s.applicationContainerErr != nil {
		return application.Service{}, s.applicationContainerErr
	}
	return s.applicationContainer, nil
}

func (s *fakeApplicationService) CreateApplicationServiceWithProgress(ctx context.Context, id int64, input application.ApplicationServiceInput, progress func(stage, message string)) (application.Service, error) {
	stages := s.applicationContainerStages
	if len(stages) == 0 {
		stages = []string{"configuration"}
	}
	for _, stage := range stages {
		progress(stage, stage)
	}
	return s.CreateApplicationService(ctx, id, input)
}

func (s *fakeApplicationService) ValidateApplicationServiceInput(input application.ApplicationServiceInput) error {
	if _, err := application.ValidateServiceName(input.ServiceName); err != nil {
		return err
	}
	imageName := input.ImageName
	if strings.TrimSpace(imageName) == "" {
		imageName = application.DefaultApplicationImageName(input.ServiceName)
	}
	_, err := application.ValidateImageName(imageName)
	return err
}

func (m *fakeSetupManager) NeedsSetup() (bool, error) {
	return m.needsSetup, nil
}

func (m *fakeSetupManager) Setup(_ context.Context, installProxy, installRegistry bool) error {
	m.setupCalls++
	m.installProxy = installProxy
	m.installRegistry = installRegistry
	if m.err == nil {
		m.needsSetup = false
	}
	if m.done != nil {
		close(m.done)
	}
	return m.err
}

func (m *fakeSetupManager) SetupWithProgress(ctx context.Context, installProxy, installRegistry bool, progress func(stage, message string)) error {
	progress("directories", "Preparing managed directories")
	if m.err != nil {
		progress("proxy-start", "Starting Caddy with Docker Compose")
	}
	return m.Setup(ctx, installProxy, installRegistry)
}

func TestIndexRendersEmptyMainPageWithApplicationsMenuItem(t *testing.T) {
	web, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `class="sidebar"`) {
		t.Fatalf("GET / did not render the sidebar: %s", body)
	}
	if !strings.Contains(body, `<img class="brand-logo" src="/static/redlaunch-logo.svg" alt="Redlaunch"`) {
		t.Fatalf("GET / did not render the Redlaunch logo in the shell header: %s", body)
	}
	if !strings.Contains(body, `href="/applications"`) || !strings.Contains(body, ">Applications</span>") {
		t.Fatalf("GET / did not render the Applications menu item: %s", body)
	}
	if !strings.Contains(body, `href="/proxy"`) || !strings.Contains(body, ">Proxy</span>") {
		t.Fatalf("GET / did not render the Proxy menu item: %s", body)
	}
	if !strings.Contains(body, `<header class="main-header">`) || !strings.Contains(body, `class="server-info"`) || !strings.Contains(body, ">Server</h2>") {
		t.Fatalf("GET / did not render the server info header: %s", body)
	}
	if !strings.Contains(body, ">Hostname</dt>") || !strings.Contains(body, ">IP address</dt>") {
		t.Fatalf("GET / did not render server info labels: %s", body)
	}
	if !strings.Contains(body, `class="server-info-value">`+web.server.Hostname+`</span>`) || !strings.Contains(body, `class="server-info-value">`+web.server.IPAddress+`</span>`) {
		t.Fatalf("GET / did not render the local server identity: %s", body)
	}
	for _, expected := range []string{
		`/static/copy.js`,
		`data-copy-value="` + web.server.Hostname + `"`,
		`aria-label="Copy hostname"`,
		`data-copy-value="` + web.server.IPAddress + `"`,
		`aria-label="Copy IP address"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET / did not render server copy control %q: %s", expected, body)
		}
	}
	mainHeaderStart := strings.Index(body, `<header class="main-header">`)
	mainHeaderEnd := strings.Index(body[mainHeaderStart:], `</header>`)
	serverInfoStart := strings.Index(body, `<section class="server-info"`)
	if mainHeaderStart < 0 || mainHeaderEnd < 0 || serverInfoStart < mainHeaderStart || serverInfoStart > mainHeaderStart+mainHeaderEnd {
		t.Fatalf("GET / rendered the server info outside the main header: %s", body)
	}
	if strings.Contains(body, "<h1") {
		t.Fatalf("GET / rendered page content even though the main page should be empty: %s", body)
	}
}

func TestDashboardRendersServerMetricsAndFirstNavigationItem(t *testing.T) {
	dashboard := &fakeDashboardMetricsService{snapshot: systemmetrics.Snapshot{
		CPUUsagePercent: 37.5,
		Memory: systemmetrics.Memory{
			AvailableBytes: 3 * 1024 * 1024 * 1024,
			UsedBytes:      5 * 1024 * 1024 * 1024,
			FreeBytes:      1 * 1024 * 1024 * 1024,
		},
		Disk: systemmetrics.Disk{
			UsedBytes:      12 * 1024 * 1024 * 1024,
			AvailableBytes: 8 * 1024 * 1024 * 1024,
		},
		TopCPU: []systemmetrics.Process{
			{PID: 101, Name: "worker<one>", CPUPercent: 82.4, MemoryBytes: 2 * 1024 * 1024},
			{PID: 102, Name: "worker-two", CPUPercent: 41.2, MemoryBytes: 1024 * 1024},
		},
		TopMemory: []systemmetrics.Process{
			{PID: 201, Name: "database", MemoryBytes: 4 * 1024 * 1024, MemoryPercent: 12.5},
		},
		CollectedAt: time.Date(2026, time.September, 2, 12, 34, 56, 0, time.UTC),
	}}
	web, err := New(nil, dashboard)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/dashboard", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /dashboard status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<title>Redlaunch · Dashboard</title>`,
		`<h1 id="page-title">Dashboard</h1>`,
		`>CPU usage</h2>`,
		`>Memory usage</h2>`,
		`>Disk usage</h2>`,
		`>Available</dt>`,
		`>Used</dt>`,
		`>Free</dt>`,
		`37.5%`,
		`3.0 GB`,
		`5.0 GB`,
		`1.0 GB`,
		`12.0 GB`,
		`8.0 GB`,
		`Top 10 most CPU-intensive processes`,
		`Top 10 most memory-consuming processes`,
		`worker&lt;one&gt;`,
		`>101</code>`,
		`12.5%`,
		`/static/htmx.min.js`,
		`hx-get="/dashboard/metrics"`,
		`hx-trigger="every 10s"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /dashboard did not render %q: %s", expected, body)
		}
	}
	dashboardIndex := strings.Index(body, `href="/dashboard"`)
	applicationsIndex := strings.Index(body, `href="/applications"`)
	if dashboardIndex < 0 || applicationsIndex < 0 || dashboardIndex > applicationsIndex {
		t.Fatalf("GET /dashboard did not put Dashboard before Applications: %s", body)
	}
	if !strings.Contains(body, `href="/dashboard" aria-current="page"`) {
		t.Fatalf("GET /dashboard did not mark Dashboard as active: %s", body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /dashboard Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
	if dashboard.calls != 1 {
		t.Fatalf("dashboard collector calls = %d, want 1", dashboard.calls)
	}
}

func TestDashboardMetricsFragmentRendersOnlyMetrics(t *testing.T) {
	dashboard := &fakeDashboardMetricsService{snapshot: systemmetrics.Snapshot{
		CPUUsagePercent: 11.2,
		Memory: systemmetrics.Memory{
			AvailableBytes: 256 * 1024 * 1024,
			UsedBytes:      768 * 1024 * 1024,
			FreeBytes:      128 * 1024 * 1024,
		},
		CollectedAt: time.Date(2026, time.September, 2, 13, 0, 0, 0, time.UTC),
	}}
	web, err := New(nil, dashboard)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/dashboard/metrics", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/metrics status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, `class="sidebar"`) {
		t.Fatalf("GET /dashboard/metrics rendered the full page: %s", body)
	}
	for _, expected := range []string{`11.2%`, `256.0 MB`, `768.0 MB`, `Top 10 most CPU-intensive processes`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /dashboard/metrics did not render %q: %s", expected, body)
		}
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /dashboard/metrics Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
	if dashboard.calls != 1 {
		t.Fatalf("dashboard collector calls = %d, want 1", dashboard.calls)
	}
}

func TestProxyRendersDashboardDetailsAndAssociatedDomains(t *testing.T) {
	proxy := &fakeProxyService{details: application.ProxyDetails{
		ContainerName: "redbolt-proxy",
		CreatedAt:     time.Date(2026, time.August, 30, 7, 25, 29, 0, time.UTC),
		Status:        "Up 9 minutes",
		ImageName:     "caddy:2.11.4-alpine",
		Ports:         "0.0.0.0:80->80/tcp",
		Logs:          "proxy started\nrequest served\n",
		LogsAvailable: true,
		Domains: []application.ProxyDomain{
			{Name: "example.com", ApplicationName: "Status page", RequestPath: "/", MappedService: "web", MappedPath: "/"},
			{Name: "<unsafe.example>", ApplicationName: "Admin <panel>", RequestPath: "/admin", MappedService: "dashboard", MappedPath: "/dashboard"},
		},
	}}
	web, err := New(nil, proxy)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/proxy", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /proxy status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<title>Redlaunch · Proxy</title>`,
		`<h1 id="page-title">Proxy</h1>`,
		`<code>redbolt-proxy</code>`,
		`action="/proxy/start"`,
		`action="/proxy/stop"`,
		`action="/proxy/restart"`,
		`>Start</span>`,
		`>Stop</span>`,
		`>Restart</span>`,
		`id="dashboard-tab"`,
		`>Dashboard</button>`,
		`id="logs-tab"`,
		`>Logs</button>`,
		`id="dashboard-panel"`,
		`<dt>Created</dt>`,
		`2026-08-30 07:25`,
		`<dt>Status</dt>`,
		`Up 9 minutes`,
		`<dt>Image</dt>`,
		`caddy:2.11.4-alpine`,
		`<h2 id="proxy-domains-title">Associated domains</h2>`,
		`>Application</th>`,
		`>Request path</th>`,
		`>Mapped service</th>`,
		`>Mapped path</th>`,
		`Most recent 1,000 lines from the proxy container.`,
		`href="/proxy/logs/download"`,
		`>Download full logs</span>`,
		`data-proxy-logs`,
		`proxy started`,
		`request served`,
		`example.com`,
		`Status page`,
		`/admin`,
		`dashboard`,
		`/dashboard`,
		`Admin &lt;panel&gt;`,
		`&lt;unsafe.example&gt;`,
		`/static/application-tabs.js`,
		`class="side-menu-item side-menu-item-active"`,
		`aria-labelledby="page-title"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /proxy did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "<unsafe.example>") {
		t.Fatalf("GET /proxy rendered an associated domain without escaping it: %s", body)
	}
	for _, unexpected := range []string{`<dt>Ports</dt>`, `0.0.0.0:80-&gt;80/tcp`} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("GET /proxy rendered removed port metadata %q: %s", unexpected, body)
		}
	}
	if !strings.Contains(body, `id="logs-panel" role="tabpanel" aria-labelledby="logs-tab" tabindex="0" hidden`) {
		t.Fatalf("GET /proxy did not render the hidden Logs panel: %s", body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /proxy Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
}

func TestProxyActionButtonsReflectContainerStatus(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		status        string
		startDisabled bool
		stopDisabled  bool
	}{
		{name: "running", status: "Up 9 minutes", startDisabled: true},
		{name: "stopped", status: "Exited (0)", stopDisabled: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			proxy := &fakeProxyService{details: application.ProxyDetails{Status: testCase.status}}
			web, err := New(nil, proxy)
			if err != nil {
				t.Fatal(err)
			}

			recorder := httptest.NewRecorder()
			web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/proxy", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("GET /proxy status = %d, want %d", recorder.Code, http.StatusOK)
			}

			body := recorder.Body.String()
			startButton := `class="button button-secondary service-details-action service-details-action-start" type="submit"`
			stopButton := `class="button button-secondary service-details-action service-details-action-stop" type="submit"`
			if got := strings.Contains(body, startButton+` disabled`); got != testCase.startDisabled {
				t.Fatalf("start button disabled = %t, want %t: %s", got, testCase.startDisabled, body)
			}
			if got := strings.Contains(body, stopButton+` disabled`); got != testCase.stopDisabled {
				t.Fatalf("stop button disabled = %t, want %t: %s", got, testCase.stopDisabled, body)
			}
			if strings.Contains(body, `service-details-action-restart" type="submit" disabled`) {
				t.Fatalf("restart button was disabled: %s", body)
			}
		})
	}
}

func TestProxyActionsRequireCSRFAndInvokeProxyService(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		path   string
		action string
	}{
		{name: "start", path: "/proxy/start", action: "start"},
		{name: "stop", path: "/proxy/stop", action: "stop"},
		{name: "restart", path: "/proxy/restart", action: "restart"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			proxy := &fakeProxyService{}
			web, err := New(nil, proxy)
			if err != nil {
				t.Fatal(err)
			}

			form := url.Values{"csrf_token": {web.csrfToken}}
			request := httptest.NewRequest(http.MethodPost, testCase.path, strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
			recorder := httptest.NewRecorder()
			web.Routes().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusSeeOther {
				t.Fatalf("POST %s status = %d, want %d", testCase.path, recorder.Code, http.StatusSeeOther)
			}
			if got := recorder.Header().Get("Location"); got != "/proxy" {
				t.Fatalf("POST %s Location = %q, want /proxy", testCase.path, got)
			}
			if proxy.proxyAction != testCase.action {
				t.Fatalf("proxy action = %q, want %q", proxy.proxyAction, testCase.action)
			}

			proxy.proxyAction = ""
			missingToken := httptest.NewRequest(http.MethodPost, testCase.path, nil)
			missingRecorder := httptest.NewRecorder()
			web.Routes().ServeHTTP(missingRecorder, missingToken)
			if missingRecorder.Code != http.StatusForbidden {
				t.Fatalf("POST %s without CSRF status = %d, want %d", testCase.path, missingRecorder.Code, http.StatusForbidden)
			}
			if proxy.proxyAction != "" {
				t.Fatalf("proxy action without CSRF = %q, want no action", proxy.proxyAction)
			}
		})
	}
}

func TestProxyLogsRenderOnlyTheMostRecentThousandLines(t *testing.T) {
	logs := "oldest line\n" + strings.Repeat("recent line\n", application.ProxyLogLineLimit)
	proxy := &fakeProxyService{details: application.ProxyDetails{Logs: logs, LogsAvailable: true}}
	web, err := New(nil, proxy)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/proxy?tab=logs", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /proxy?tab=logs status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "oldest line") {
		t.Fatalf("proxy log view rendered a line older than the most recent %d lines", application.ProxyLogLineLimit)
	}
	if got := strings.Count(body, `<li><code>recent line</code></li>`); got != application.ProxyLogLineLimit {
		t.Fatalf("proxy log view rendered %d lines, want %d", got, application.ProxyLogLineLimit)
	}
}

func TestProxyDownloadsFullLogs(t *testing.T) {
	content := "oldest line\n" + strings.Repeat("complete line\n", application.ProxyLogLineLimit)
	proxy := &fakeProxyService{fullLogs: content}
	web, err := New(nil, proxy)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/proxy/logs/download", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /proxy/logs/download status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.Body.String() != content {
		t.Fatalf("downloaded proxy logs = %q, want complete logs", recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("proxy log download Content-Type = %q, want text/plain", recorder.Header().Get("Content-Type"))
	}
	if recorder.Header().Get("Content-Disposition") != `attachment; filename="proxy-logs.txt"` {
		t.Fatalf("proxy log download disposition = %q, want attachment filename", recorder.Header().Get("Content-Disposition"))
	}
	if !proxy.fullLogsCalled {
		t.Fatal("proxy log download did not request the full log history")
	}
}

func TestProxyRendersUnavailableContainerAndEmptyDomainsState(t *testing.T) {
	web, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/proxy", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /proxy status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`>Unknown</span>`,
		`No domains are associated with the proxy.`,
		`datetime=""`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /proxy unavailable state did not render %q: %s", expected, body)
		}
	}
}

func TestApplicationsRendersCreateControlsAndEmptyState(t *testing.T) {
	web, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /applications status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<h1 id="page-title">Applications</h1>`,
		`>Create application</span>`,
		`No applications yet`,
		`id="application-dialog"`,
		`name="name"`,
		`name="folder_name"`,
		`/static/applications.js`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /applications did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `open>`) {
		t.Fatalf("GET /applications opened the create dialog by default: %s", body)
	}
	if strings.Count(body, `<h1`) != 1 {
		t.Fatalf("GET /applications did not render the Applications heading: %s", body)
	}
	if !strings.Contains(body, `class="side-menu-item side-menu-item-active"`) || !strings.Contains(body, `aria-current="page"`) {
		t.Fatalf("GET /applications did not mark Applications as active: %s", body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /applications Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
}

func TestApplicationsRendersExistingApplicationCard(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{
		ID:           7,
		Name:         "Status <page>",
		FolderName:   "status-page",
		ServiceCount: 2,
	}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /applications status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{`class="application-card"`, `href="/applications/7"`, `Status &lt;page&gt;`, `>2 services</p>`, `<rect x="3.5" y="3.5" width="6.5" height="6.5" rx="1"></rect>`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /applications did not render %q: %s", expected, body)
		}
	}
	cardStart := strings.Index(body, `<a class="application-card"`)
	if cardStart < 0 {
		t.Fatalf("GET /applications did not render an application card: %s", body)
	}
	cardEnd := strings.Index(body[cardStart:], `</a>`)
	if cardEnd < 0 {
		t.Fatalf("GET /applications did not render a complete application card: %s", body)
	}
	card := body[cardStart : cardStart+cardEnd]
	for _, unexpected := range []string{"status-page", "applications/status-page", ">Ready</span>", "Folder"} {
		if strings.Contains(card, unexpected) {
			t.Fatalf("GET /applications rendered removed application card content %q: %s", unexpected, body)
		}
	}
	if strings.Contains(body, "No applications yet") {
		t.Fatalf("GET /applications rendered the empty state with an application: %s", body)
	}
}

func TestApplicationDetailsRendersEmptyServicesState(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{
		ID:         7,
		Name:       "Status <page>",
		FolderName: "status-page",
	}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /applications/7 status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<h1 id="page-title">Status &lt;page&gt;</h1>`,
		`href="/applications"`,
		`class="application-tabs"`,
		`role="tablist"`,
		`id="services-tab"`,
		`id="domains-tab"`,
		`id="domains-panel"`,
		`<h2 id="domains-title">Domains</h2>`,
		`id="deployment-tab"`,
		`id="deployment-panel"`,
		`<h2 id="github-actions-title">GitHub Actions deployment</h2>`,
		`href="/applications/7/deployments/github-actions"`,
		`id="settings-tab"`,
		`<h2 id="services-title">Services</h2>`,
		`id="settings-panel"`,
		`<h2>Settings</h2>`,
		`<h3 id="public-access-title">Public access</h3>`,
		`Enable access Redlaunch publicly (https)`,
		`name="domain"`,
		`action="/applications/7/settings/public-access?tab=settings"`,
		`/static/application-settings.js`,
		`Danger zone`,
		`data-application-delete-open`,
		`id="application-delete-dialog"`,
		`data-application-name="Status &lt;page&gt;"`,
		`action="/applications/7/delete"`,
		`Type <code>Status &lt;page&gt;</code> to confirm`,
		`/static/application-delete.js`,
		`No services yet`,
		`class="service-split-button"`,
		`>Create service</span>`,
		`Import Docker Compose project...`,
		`id="compose-import-dialog"`,
		`enctype="multipart/form-data"`,
		`/static/application-import.js`,
		`Import variables...`,
		`Import secrets...`,
		`id="variables-import-dialog"`,
		`id="secrets-import-dialog"`,
		`aria-controls="variables-import-dialog"`,
		`aria-controls="secrets-import-dialog"`,
		`name="variables_file"`,
		`name="secrets_file"`,
		`/static/application-environment-import.js`,
		`PostgreSQL database`,
		`Redis cache`,
		`href="/applications/7/services/application/new"`,
		`<span>Application</span>`,
		`/static/application-tabs.js`,
		`/static/service-menu.js`,
		`/static/application-environment.js`,
		`/static/application-domains.js`,
		`aria-expanded="false"`,
		`class="side-menu-item side-menu-item-active"`,
		`aria-labelledby="page-title"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /applications/7 did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "applications/status-page") {
		t.Fatalf("GET /applications/7 rendered the redundant application path: %s", body)
	}
	if !strings.Contains(body, `id="settings-panel"`) || !strings.Contains(body, `hidden`) {
		t.Fatalf("GET /applications/7 did not hide the Settings panel by default: %s", body)
	}
	deploymentPanelStart := strings.Index(body, `<section class="application-tab-panel" id="deployment-panel"`)
	settingsPanelStart := strings.Index(body, `<section class="application-tab-panel application-settings-panel" id="settings-panel"`)
	if deploymentPanelStart < 0 || settingsPanelStart < 0 || deploymentPanelStart >= settingsPanelStart {
		t.Fatalf("GET /applications/7 did not render Deployment before Settings: %s", body)
	}
	if deploymentPanel := body[deploymentPanelStart:settingsPanelStart]; !strings.Contains(deploymentPanel, `class="service-widget application-github-actions"`) {
		t.Fatalf("GET /applications/7 did not render GitHub Actions in Deployment: %s", body)
	}
	if settingsPanel := body[settingsPanelStart:]; strings.Contains(settingsPanel, `class="service-widget application-github-actions"`) {
		t.Fatalf("GET /applications/7 still rendered GitHub Actions in Settings: %s", body)
	}
	if strings.Contains(body, `service-split-button-main" type="button" disabled`) {
		t.Fatalf("GET /applications/7 rendered the Create service button disabled: %s", body)
	}
	if got := strings.Count(body, `class="service-split-option-icon"`); got != 3 {
		t.Fatalf("GET /applications/7 rendered %d service option icons, want 3: %s", got, body)
	}
	if strings.Contains(body, `class="services-list"`) {
		t.Fatalf("GET /applications/7 rendered a service list with no services: %s", body)
	}
	if strings.Count(body, `<h1`) != 1 {
		t.Fatalf("GET /applications/7 did not render one page heading: %s", body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /applications/7 Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
}

func TestApplicationDetailsRendersRedlaunchPublicAccessSettings(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		publicAccess: application.RedlaunchPublicAccess{Enabled: true, Domain: "admin.example.com"},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7?tab=settings", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /applications/7?tab=settings status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`id="redlaunch-public-access-enabled" name="enabled" type="checkbox" value="true" role="switch"`,
		`data-public-access-toggle checked`,
		`value="admin.example.com"`,
		`data-public-access-domain required`,
		`managed Caddy proxy`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET application settings did not render %q: %s", expected, body)
		}
	}
}

func TestUpdateRedlaunchPublicAccessRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	withoutCSRF := url.Values{"enabled": {"true"}, "domain": {"admin.example.com"}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/applications/7/settings/public-access?tab=settings", strings.NewReader(withoutCSRF.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST public access without CSRF status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if applications.publicAccessInput != (application.RedlaunchPublicAccessInput{}) {
		t.Fatalf("public access update without CSRF reached service: %#v", applications.publicAccessInput)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"enabled":    {"true"},
		"domain":     {" Admin.Example.COM "},
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/applications/7/settings/public-access?tab=settings", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST public access status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7?tab=settings" {
		t.Fatalf("POST public access Location = %q, want settings tab", got)
	}
	want := application.RedlaunchPublicAccessInput{Enabled: true, Domain: " Admin.Example.COM "}
	if applications.publicAccessInput != want {
		t.Fatalf("public access input = %#v, want %#v", applications.publicAccessInput, want)
	}
}

func TestUpdateRedlaunchPublicAccessRendersValidationError(t *testing.T) {
	applications := &fakeApplicationService{
		applications:          []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		publicAccessUpdateErr: application.ErrRedlaunchPublicDomainInvalid,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token": {web.csrfToken},
		"enabled":    {"true"},
		"domain":     {"bad/<domain>"},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/applications/7/settings/public-access?tab=settings", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid public access status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "Enter a valid domain name.") || !strings.Contains(body, `value="bad/&lt;domain&gt;"`) {
		t.Fatalf("POST invalid public access did not render safe field error: %s", body)
	}
}

func newComposeImportRequest(t *testing.T, token string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("csrf_token", token); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("compose_file", "compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("services:\n  web:\n    image: nginx:1.27\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func newEnvironmentImportRequest(t *testing.T, token string, variables []byte, provideVariables bool, secrets []byte, provideSecrets bool) *http.Request {
	return newEnvironmentImportRequestForKind(t, "", token, variables, provideVariables, secrets, provideSecrets)
}

func newEnvironmentImportRequestForKind(t *testing.T, kind, token string, variables []byte, provideVariables bool, secrets []byte, provideSecrets bool) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("csrf_token", token); err != nil {
		t.Fatal(err)
	}
	if kind != "" {
		if err := writer.WriteField("environment_import_kind", kind); err != nil {
			t.Fatal(err)
		}
	}
	if provideVariables {
		file, err := writer.CreateFormFile("variables_file", ".env")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(variables); err != nil {
			t.Fatal(err)
		}
	}
	if provideSecrets {
		file, err := writer.CreateFormFile("secrets_file", ".env")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(secrets); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/environment/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestImportDockerComposeProjectAcceptsComposeFile(t *testing.T) {
	applications := &fakeApplicationService{
		applications:     []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		importedServices: []application.Service{{ID: 1, ApplicationID: 7, Name: "web"}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	request := newComposeImportRequest(t, web.csrfToken)
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /applications/7/import status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7" {
		t.Fatalf("POST /applications/7/import Location = %q, want /applications/7", got)
	}
	if applications.importID != 7 || !strings.Contains(string(applications.importContents), "services:") {
		t.Fatalf("import request = (id %d, contents %q), want application 7 and Compose contents", applications.importID, applications.importContents)
	}
}

func TestImportDockerComposeProjectRequiresCSRF(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newComposeImportRequest(t, ""))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST /applications/7/import without CSRF status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if applications.importID != 0 {
		t.Fatalf("import request without CSRF reached the service: %#v", applications)
	}
}

func TestImportDockerComposeProjectRendersSafeValidationError(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		importErr:    application.ErrComposeFileInvalid,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	request := newComposeImportRequest(t, web.csrfToken)
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST /applications/7/import invalid file status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "The Docker Compose file is invalid or could not be validated.") {
		t.Fatalf("invalid import response did not render a safe validation message: %s", body)
	}
	if !strings.Contains(body, `data-compose-import-dialog-open`) || !strings.Contains(body, `role="alert"`) {
		t.Fatalf("invalid import response did not reopen the import dialog: %s", body)
	}
}

func TestImportApplicationEnvironmentFilesAcceptsVariablesAndSecrets(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	variables := []byte("APP_NAME=Status page\n")
	secrets := []byte("API_TOKEN=super-secret\n")
	request := newEnvironmentImportRequest(t, web.csrfToken, variables, true, secrets, true)
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /applications/7/environment/import status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7?tab=variables" {
		t.Fatalf("POST /applications/7/environment/import Location = %q, want variables tab", got)
	}
	input := applications.environmentImportInput
	if !input.VariablesProvided || string(input.Variables) != string(variables) {
		t.Fatalf("imported variables = (%v, %q), want uploaded variables", input.VariablesProvided, input.Variables)
	}
	if !input.SecretsProvided || string(input.Secrets) != string(secrets) {
		t.Fatalf("imported secrets = (%v, %q), want uploaded secrets", input.SecretsProvided, input.Secrets)
	}
}

func TestImportApplicationEnvironmentFilesRequiresCSRF(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newEnvironmentImportRequest(t, "", []byte("APP_NAME=value\n"), true, nil, false))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST /applications/7/environment/import without CSRF status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if applications.environmentImportInput.VariablesProvided || applications.environmentImportInput.SecretsProvided {
		t.Fatalf("environment import without CSRF reached the service: %#v", applications.environmentImportInput)
	}
}

func TestImportApplicationEnvironmentFilesRendersSafeValidationError(t *testing.T) {
	applications := &fakeApplicationService{
		applications:         []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		environmentImportErr: application.ErrEnvironmentImportInvalid,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	request := newEnvironmentImportRequestForKind(t, "secrets", web.csrfToken, []byte("APP_NAME=value\n"), true, []byte("API_TOKEN=super-secret\n"), true)
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST /applications/7/environment/import invalid file status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "The uploaded environment file is invalid.") {
		t.Fatalf("invalid environment import response did not render a safe validation message: %s", body)
	}
	if strings.Contains(body, "super-secret") {
		t.Fatalf("invalid environment import response exposed secret contents: %s", body)
	}
	if !strings.Contains(body, `data-environment-import-dialog-open`) || !strings.Contains(body, `role="alert"`) {
		t.Fatalf("invalid environment import response did not reopen the import dialog: %s", body)
	}
	if !strings.Contains(body, `id="secrets-import-dialog" data-environment-import-dialog data-environment-import-dialog-open`) {
		t.Fatalf("invalid secrets import response did not reopen the secrets dialog: %s", body)
	}
	if strings.Contains(body, `id="variables-import-dialog" data-environment-import-dialog data-environment-import-dialog-open`) {
		t.Fatalf("invalid secrets import response reopened the variables dialog: %s", body)
	}
}

func TestApplicationDetailsRendersDomains(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{
			ID:         7,
			Name:       "Status page",
			FolderName: "status-page",
		}},
		domains: []application.Domain{
			{ID: 1, ApplicationID: 7, Name: "example.com"},
			{ID: 2, ApplicationID: 7, Name: "www.example.com"},
		},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7?tab=domains", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /applications/7 status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`id="domains-tab"`,
		`id="domains-panel"`,
		`class="service-widget application-domains-widget"`,
		`<h2 id="domains-title">Domains</h2>`,
		`Domains associated with this application.`,
		`data-domain-add`,
		`<th scope="col">Domain</th>`,
		`<code>example.com</code>`,
		`<code>www.example.com</code>`,
		`data-domain-name="example.com"`,
		`href="/applications/7/domains/1/routing"`,
		`>Manage routing</span>`,
		`aria-label="Delete domain example.com"`,
		`id="domain-edit-dialog"`,
		`action="/applications/7/domains"`,
		`id="domain-delete-dialog"`,
		`action="/applications/7/domains/delete"`,
		`/static/application-domains.js`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /applications/7 did not render %q: %s", expected, body)
		}
	}
	if got := strings.Count(body, `data-domain-delete `); got != len(applications.domains) {
		t.Fatalf("GET /applications/7 rendered %d domain delete buttons, want %d: %s", got, len(applications.domains), body)
	}
	if domainsTab := strings.Index(body, `id="domains-tab"`); domainsTab < 0 {
		t.Fatalf("GET /applications/7 did not render the Domains tab: %s", body)
	} else if deploymentTab := strings.Index(body, `id="deployment-tab"`); deploymentTab < 0 || domainsTab > deploymentTab {
		t.Fatalf("GET /applications/7 rendered Domains after Deployment: %s", body)
	} else if settingsTab := strings.Index(body, `id="settings-tab"`); settingsTab < 0 || deploymentTab > settingsTab {
		t.Fatalf("GET /applications/7 rendered Deployment after Settings: %s", body)
	}
}

func TestApplicationRoutingPageRendersDomainRoutingsAndServiceSelector(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		domains:      []application.Domain{{ID: 2, ApplicationID: 7, Name: "example.com"}},
		services: []application.Service{
			{ID: 1, ApplicationID: 7, Name: "frontend"},
			{ID: 2, ApplicationID: 7, Name: "identity"},
		},
		routings: []application.Routing{{
			ID:            11,
			ApplicationID: 7,
			DomainID:      2,
			Subdomain:     "api",
			Path:          "/register",
			ServiceName:   "identity",
			ServicePort:   3000,
			ServicePath:   "/",
		}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/domains/2/routing", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET routing page status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<h1 id="page-title">Routing</h1>`,
		`Status page`,
		`<h2 id="routings-title">Routings</h2>`,
		`api.example.com`,
		`/register`,
		`<code>identity</code>`,
		`data-routing-add`,
		`>Add routing</span>`,
		`data-routing-edit`,
		`data-routing-service="identity"`,
		`data-routing-service-port="3000"`,
		`data-routing-delete`,
		`id="routing-edit-dialog"`,
		`name="subdomain"`,
		`name="service"`,
		`<option value="frontend">frontend</option>`,
		`<option value="identity">identity</option>`,
		`name="service_path"`,
		`name="service_port"`,
		`id="routing-edit-path" name="path" type="text" value="/"`,
		`id="routing-edit-service-path" name="service_path" type="text" value="/"`,
		`id="routing-delete-dialog"`,
		`/static/application-routings.js`,
		`href="/applications/7?tab=domains"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET routing page did not render %q: %s", expected, body)
		}
	}
	if strings.Count(body, `data-routing-edit `) != 1 || strings.Count(body, `data-routing-delete `) != 1 {
		t.Fatalf("GET routing page rendered unexpected routing action counts: %s", body)
	}
	if strings.Contains(body, `data-routing-edit-open`) || strings.Contains(body, `data-routing-delete-open`) {
		t.Fatalf("GET routing page opened a dialog by default: %s", body)
	}
}

func TestApplicationRoutingSaveRequiresCSRFAndSupportsCreateAndEdit(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		domains:      []application.Domain{{ID: 2, ApplicationID: 7, Name: "example.com"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "frontend"}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{"operation": {"add"}, "path": {"/"}, "service": {"frontend"}, "service_port": {"3000"}, "service_path": {"/"}}
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/applications/7/domains/2/routing", strings.NewReader(form.Encode()))
	missingTokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingToken := httptest.NewRecorder()
	handler.ServeHTTP(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST routing without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.routingCreateID != 0 {
		t.Fatalf("routing create without CSRF = %#v, want no create", applications)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/domains/2/routing", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST create routing status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7/domains/2/routing" {
		t.Fatalf("POST create routing Location = %q, want routing page", got)
	}
	if applications.routingCreateID != 7 || applications.routingCreateDomainID != 2 || applications.routingCreateInput.ServiceName != "frontend" {
		t.Fatalf("routing create = %#v, want application/domain/service", applications)
	}
	if applications.routingCreateInput.ServicePort != 3000 {
		t.Fatalf("routing create service port = %d, want 3000", applications.routingCreateInput.ServicePort)
	}

	editForm := url.Values{
		"csrf_token":   {web.csrfToken},
		"operation":    {"edit"},
		"routing_id":   {"11"},
		"subdomain":    {"api"},
		"path":         {"/register"},
		"service":      {"frontend"},
		"service_port": {"8080"},
		"service_path": {"/app"},
	}
	editRequest := httptest.NewRequest(http.MethodPost, "/applications/7/domains/2/routing", strings.NewReader(editForm.Encode()))
	editRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	editRequest.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	editRecorder := httptest.NewRecorder()
	handler.ServeHTTP(editRecorder, editRequest)
	if editRecorder.Code != http.StatusSeeOther {
		t.Fatalf("POST edit routing status = %d, want %d", editRecorder.Code, http.StatusSeeOther)
	}
	if applications.routingUpdateID != 11 || applications.routingUpdateDomainID != 2 || applications.routingUpdateInput.Subdomain != "api" || applications.routingUpdateInput.ServicePath != "/app" {
		t.Fatalf("routing update = %#v, want routing ID/domain/input", applications)
	}
	if applications.routingUpdateInput.ServicePort != 8080 {
		t.Fatalf("routing update service port = %d, want 8080", applications.routingUpdateInput.ServicePort)
	}
}

func TestApplicationRoutingSaveRendersValidationErrorInDialog(t *testing.T) {
	applications := &fakeApplicationService{
		applications:     []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		domains:          []application.Domain{{ID: 2, ApplicationID: 7, Name: "example.com"}},
		services:         []application.Service{{ID: 1, ApplicationID: 7, Name: "frontend"}},
		routingCreateErr: application.ErrRoutingPathInvalid,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":   {web.csrfToken},
		"operation":    {"add"},
		"path":         {"register"},
		"service":      {"frontend"},
		"service_path": {"/"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/domains/2/routing", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid routing status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{`data-routing-edit-open`, `<h2 id="routing-edit-dialog-title" data-routing-edit-title>Add routing</h2>`, `role="alert">Paths must start with / and contain no spaces or control characters.</div>`, `id="routing-edit-path" name="path" type="text" value="register"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("POST invalid routing did not render %q: %s", expected, body)
		}
	}
}

func TestApplicationRoutingDeleteRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		domains:      []application.Domain{{ID: 2, ApplicationID: 7, Name: "example.com"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "frontend"}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{"routing_id": {"11"}, "host": {"api.example.com"}}
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/applications/7/domains/2/routing/delete", strings.NewReader(form.Encode()))
	missingTokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingToken := httptest.NewRecorder()
	handler.ServeHTTP(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST delete routing without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.routingDeleteID != 0 {
		t.Fatalf("routing delete without CSRF = %#v, want no delete", applications)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/domains/2/routing/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST delete routing status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7/domains/2/routing" {
		t.Fatalf("POST delete routing Location = %q, want routing page", got)
	}
	if applications.routingDeleteID != 11 || applications.routingDeleteDomainID != 2 {
		t.Fatalf("routing delete = %#v, want routing ID/domain", applications)
	}
}

func TestApplicationDetailsRendersServicesTable(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{
			ID:         7,
			Name:       "Status page",
			FolderName: "status-page",
		}},
		services: []application.Service{{
			ID:                 1,
			ApplicationID:      7,
			Name:               "db",
			Type:               application.ServiceTypePostgreSQL,
			ContainerName:      "redbolt-7-db",
			ContainerCreatedAt: time.Date(2026, time.August, 30, 7, 25, 29, 0, time.UTC),
			Status:             "Up 9 minutes",
			Ports:              "5432/tcp",
		}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /applications/7 status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<table class="services-table">`,
		`<th scope="col">Type</th>`,
		`<th scope="col">Service</th>`,
		`<th scope="col">Container</th>`,
		`<th scope="col">Created</th>`,
		`<th scope="col">Status</th>`,
		`<th scope="col">Ports</th>`,
		`service-type-database`,
		`aria-label="Database"`,
		`<th scope="row" class="service-table-name"><a class="service-table-name-link" href="/applications/7/services/db"><code>db</code></a></th>`,
		`redbolt-7-db`,
		`2026-08-30 07:25`,
		`Up 9 minutes`,
		`5432/tcp`,
		`class="service-table-actions"`,
		`class="service-actions-menu"`,
		`aria-label="Actions for db"`,
		`class="service-actions-options"`,
		`action="/applications/7/services/db/start"`,
		`action="/applications/7/services/db/stop"`,
		`action="/applications/7/services/db/restart"`,
		`name="csrf_token"`,
		`>Start service</span>`,
		`>Stop service</span>`,
		`>Restart service</span>`,
		`class="service-actions-item service-actions-item-start"`,
		`class="service-actions-item service-actions-item-stop"`,
		`class="service-actions-item service-actions-item-restart"`,
		`data-service-delete-open`,
		`id="service-delete-dialog-1"`,
		`name="confirmation"`,
		`action="/applications/7/services/db/delete"`,
		`Type <code>db</code> to confirm`,
		`>Edit</span>`,
		`>Delete</span>`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /applications/7 did not render %q: %s", expected, body)
		}
	}
	if got := strings.Count(body, `class="service-actions-menu"`); got != len(applications.services) {
		t.Fatalf("GET /applications/7 rendered %d service action menus, want %d: %s", got, len(applications.services), body)
	}
	if got := strings.Count(body, `class="service-actions-form"`); got != len(applications.services)*3 {
		t.Fatalf("GET /applications/7 rendered %d service action forms, want %d: %s", got, len(applications.services)*3, body)
	}
	if strings.Contains(body, `class="services-list"`) || strings.Contains(body, "applications/status-page") {
		t.Fatalf("GET /applications/7 rendered an obsolete details pattern: %s", body)
	}
}

func TestApplicationDetailsRendersVariablesAndMasksSecrets(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{
			ID:         7,
			Name:       "Status page",
			FolderName: "status-page",
		}},
		environmentFiles: application.EnvironmentFiles{
			Variables: []application.EnvironmentVariable{
				{Key: "APP_NAME", Value: "Status <page>"},
				{Key: "PASSWORD_LIKE_VARIABLE", Value: "visible-variable-value", Sensitive: true},
			},
			Secrets: []application.EnvironmentVariable{
				{Key: "API_TOKEN", Value: "super-secret"},
			},
			VariablesAvailable: true,
			SecretsAvailable:   true,
		},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /applications/7 status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`id="variables-tab"`,
		`id="variables-panel"`,
		`id="variables-title">Variables</h2>`,
		`APP_NAME`,
		`Status &lt;page&gt;`,
		`PASSWORD_LIKE_VARIABLE`,
		`visible-variable-value`,
		`id="secrets-tab"`,
		`id="secrets-panel"`,
		`id="secrets-title">Secrets</h2>`,
		`API_TOKEN`,
		`Secret value masked`,
		`data-variable-edit`,
		`aria-label="Edit variable APP_NAME"`,
		`aria-label="Edit variable PASSWORD_LIKE_VARIABLE"`,
		`data-variable-delete`,
		`aria-label="Delete variable APP_NAME"`,
		`aria-label="Delete variable PASSWORD_LIKE_VARIABLE"`,
		`data-variable-move`,
		`aria-label="Move variable APP_NAME to secrets"`,
		`aria-label="Move variable PASSWORD_LIKE_VARIABLE to secrets"`,
		`data-variable-add`,
		`>Add variable</span>`,
		`data-secret-edit`,
		`aria-label="Edit secret API_TOKEN"`,
		`data-secret-move`,
		`aria-label="Move secret API_TOKEN to variables"`,
		`<path d="M7 11V7a5 5 0 0 1 9.9-1"></path>`,
		`data-secret-delete`,
		`aria-label="Delete secret API_TOKEN"`,
		`data-secret-add`,
		`>Add secret</span>`,
		`id="variable-edit-dialog"`,
		`id="variable-delete-dialog"`,
		`id="secret-edit-dialog"`,
		`id="secret-delete-dialog"`,
		`id="secret-edit-value" name="value" type="password"`,
		`data-secret-toggle`,
		`data-secret-show-icon`,
		`data-secret-hide-icon aria-hidden="true" hidden`,
		`data-secret-generate`,
		`<path d="M21 12a9 9 0 0 0-9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"></path>`,
		`>Generate secret</span>`,
		`name="original_name"`,
		`id="variable-edit-name"`,
		`id="variable-edit-value"`,
		`>Cancel</button>`,
		`>Save</button>`,
		`>Delete variable</button>`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /applications/7 did not render %q: %s", expected, body)
		}
	}
	if got := strings.Count(body, ` data-variable-edit `); got != len(applications.environmentFiles.Variables) {
		t.Fatalf("GET /applications/7 rendered %d variable edit buttons, want %d: %s", got, len(applications.environmentFiles.Variables), body)
	}
	if got := strings.Count(body, ` data-variable-delete `); got != len(applications.environmentFiles.Variables) {
		t.Fatalf("GET /applications/7 rendered %d variable delete buttons, want %d: %s", got, len(applications.environmentFiles.Variables), body)
	}
	if got := strings.Count(body, ` data-variable-move `); got != len(applications.environmentFiles.Variables) {
		t.Fatalf("GET /applications/7 rendered %d variable move buttons, want %d: %s", got, len(applications.environmentFiles.Variables), body)
	}
	if got := strings.Count(body, `data-variable-add`); got != 1 {
		t.Fatalf("GET /applications/7 rendered %d add variable buttons, want 1: %s", got, body)
	}
	if got := strings.Count(body, ` data-secret-edit `); got != len(applications.environmentFiles.Secrets) {
		t.Fatalf("GET /applications/7 rendered %d secret edit buttons, want %d: %s", got, len(applications.environmentFiles.Secrets), body)
	}
	if got := strings.Count(body, ` data-secret-delete `); got != len(applications.environmentFiles.Secrets) {
		t.Fatalf("GET /applications/7 rendered %d secret delete buttons, want %d: %s", got, len(applications.environmentFiles.Secrets), body)
	}
	if got := strings.Count(body, ` data-secret-move `); got != len(applications.environmentFiles.Secrets) {
		t.Fatalf("GET /applications/7 rendered %d secret move buttons, want %d: %s", got, len(applications.environmentFiles.Secrets), body)
	}
	if got := strings.Count(body, `data-secret-add`); got != 1 {
		t.Fatalf("GET /applications/7 rendered %d add secret buttons, want 1: %s", got, body)
	}
	if strings.Count(body, `class="application-tab"`) != 6 {
		t.Fatalf("GET /applications/7 rendered %d tabs, want 6: %s", strings.Count(body, `class="application-tab"`), body)
	}
	if strings.Count(body, `class="service-environment-masked"`) != 1 {
		t.Fatalf("GET /applications/7 rendered %d masked values, want 1: %s", strings.Count(body, `class="service-environment-masked"`), body)
	}
	if strings.Contains(body, "super-secret") || strings.Contains(body, "data-secret-value") {
		t.Fatal("GET /applications/7 included a secret value in the response")
	}
}

func TestApplicationVariableUpdateRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{
		"original_name": {"APP_NAME"},
		"name":          {"APP_TITLE"},
		"value":         {"New title"},
	}
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/applications/7/variables", strings.NewReader(form.Encode()))
	missingTokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingToken := httptest.NewRecorder()
	handler.ServeHTTP(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST variables without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.variableUpdateID != 0 {
		t.Fatalf("variable update without CSRF = %#v, want no update", applications)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/variables", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST variables status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7?tab=variables" {
		t.Fatalf("POST variables Location = %q, want variables tab", got)
	}
	if applications.variableUpdateID != 7 || applications.variableOriginalName != "APP_NAME" || applications.variableUpdateName != "APP_TITLE" || applications.variableUpdateValue != "New title" {
		t.Fatalf("variable update = (%d, %q, %q, %q), want (7, APP_NAME, APP_TITLE, New title)", applications.variableUpdateID, applications.variableOriginalName, applications.variableUpdateName, applications.variableUpdateValue)
	}
}

func TestApplicationVariableDeleteRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{"name": {"APP_NAME"}}
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/applications/7/variables/delete", strings.NewReader(form.Encode()))
	missingTokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingToken := httptest.NewRecorder()
	handler.ServeHTTP(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST delete variable without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.variableDeleteID != 0 {
		t.Fatalf("variable delete without CSRF = %#v, want no delete", applications)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/variables/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST delete variable status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7?tab=variables" {
		t.Fatalf("POST delete variable Location = %q, want variables tab", got)
	}
	if applications.variableDeleteID != 7 || applications.variableDeleteName != "APP_NAME" {
		t.Fatalf("variable delete = (%d, %q), want (7, APP_NAME)", applications.variableDeleteID, applications.variableDeleteName)
	}
}

func TestApplicationVariableDeleteRendersErrorInConfirmationDialog(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		environmentFiles: application.EnvironmentFiles{
			Variables:          []application.EnvironmentVariable{{Key: "APP_NAME", Value: "Status page"}},
			VariablesAvailable: true,
		},
		variableDeleteErr: application.ErrEnvironmentVariableNotFound,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"name":       {"APP_NAME"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/variables/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid delete variable status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-variable-delete-open`,
		`<h2 id="variable-delete-dialog-title">Delete variable</h2>`,
		`<code data-variable-delete-name-display>APP_NAME</code>`,
		`name="name" value="APP_NAME" data-variable-delete-name-input`,
		`role="alert">The variable could not be found. Refresh the page and try again.</div>`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("POST invalid delete variable did not render %q: %s", expected, body)
		}
	}
}

func TestApplicationVariableMoveToSecretsRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{"name": {"APP_NAME"}}
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/applications/7/variables/move-to-secrets", strings.NewReader(form.Encode()))
	missingTokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingToken := httptest.NewRecorder()
	handler.ServeHTTP(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST move variable without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.variableMoveID != 0 {
		t.Fatalf("variable move without CSRF = %#v, want no move", applications)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/variables/move-to-secrets", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST move variable status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7?tab=variables" {
		t.Fatalf("POST move variable Location = %q, want variables tab", got)
	}
	if applications.variableMoveID != 7 || applications.variableMoveName != "APP_NAME" {
		t.Fatalf("variable move = (%d, %q), want (7, APP_NAME)", applications.variableMoveID, applications.variableMoveName)
	}
}

func TestApplicationVariableMoveToSecretsRendersConflictError(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		environmentFiles: application.EnvironmentFiles{
			Variables:          []application.EnvironmentVariable{{Key: "APP_NAME", Value: "Status page"}},
			VariablesAvailable: true,
			SecretsAvailable:   true,
		},
		variableMoveErr: application.ErrEnvironmentVariableAlreadyExists,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"name":       {"APP_NAME"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/variables/move-to-secrets", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST conflicting move variable status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`class="application-alert application-environment-alert" role="alert">A secret with that name already exists.</div>`,
		`APP_NAME`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("POST conflicting move variable did not render %q: %s", expected, body)
		}
	}
}

func TestApplicationSecretMoveToVariablesRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{"name": {"API_TOKEN"}}
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/applications/7/secrets/move-to-variables", strings.NewReader(form.Encode()))
	missingTokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingToken := httptest.NewRecorder()
	handler.ServeHTTP(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST move secret without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.secretMoveID != 0 {
		t.Fatalf("secret move without CSRF = %#v, want no move", applications)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/secrets/move-to-variables", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST move secret status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7?tab=secrets" {
		t.Fatalf("POST move secret Location = %q, want secrets tab", got)
	}
	if applications.secretMoveID != 7 || applications.secretMoveName != "API_TOKEN" {
		t.Fatalf("secret move = (%d, %q), want (7, API_TOKEN)", applications.secretMoveID, applications.secretMoveName)
	}
}

func TestApplicationSecretMoveToVariablesRendersConflictError(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		environmentFiles: application.EnvironmentFiles{
			Variables:          []application.EnvironmentVariable{{Key: "APP_NAME", Value: "Status page"}},
			Secrets:            []application.EnvironmentVariable{{Key: "API_TOKEN", Value: "secret-value"}},
			VariablesAvailable: true,
			SecretsAvailable:   true,
		},
		secretMoveErr: application.ErrEnvironmentVariableAlreadyExists,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"name":       {"API_TOKEN"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/secrets/move-to-variables", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST conflicting move secret status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`class="application-alert application-environment-alert" role="alert">A variable with that name already exists.</div>`,
		`API_TOKEN`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("POST conflicting move secret did not render %q: %s", expected, body)
		}
	}
}

func TestApplicationVariableAddRequiresNameAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"operation":  {"add"},
		"name":       {"APP_TITLE"},
		"value":      {"New title"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/variables", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST add variable status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7?tab=variables" {
		t.Fatalf("POST add variable Location = %q, want variables tab", got)
	}
	if applications.variableAddID != 7 || applications.variableAddName != "APP_TITLE" || applications.variableAddValue != "New title" {
		t.Fatalf("variable add = (%d, %q, %q), want (7, APP_TITLE, New title)", applications.variableAddID, applications.variableAddName, applications.variableAddValue)
	}
	if applications.variableUpdateID != 0 {
		t.Fatalf("POST add variable called update = %#v, want add only", applications)
	}
}

func TestApplicationVariableAddRendersRequiredNameError(t *testing.T) {
	applications := &fakeApplicationService{
		applications:   []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		variableAddErr: application.ErrEnvironmentVariableNameRequired,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"operation":  {"add"},
		"name":       {""},
		"value":      {"New title"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/variables", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST add variable without name status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-variable-edit-open`,
		`<h2 id="variable-edit-dialog-title" data-variable-edit-title>Add variable</h2>`,
		`role="alert">Enter a variable name.</div>`,
		`name="operation" value="add"`,
		`id="variable-edit-name" name="name" type="text" value=""`,
		`id="variable-edit-value" name="value" type="text" value="New title"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("POST add variable without name did not render %q: %s", expected, body)
		}
	}
}

func TestApplicationVariableUpdateRendersValidationErrorInDialog(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		environmentFiles: application.EnvironmentFiles{
			Variables:          []application.EnvironmentVariable{{Key: "APP_NAME", Value: "Status page"}},
			VariablesAvailable: true,
		},
		variableUpdateErr: application.ErrEnvironmentVariableAlreadyExists,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token":    {web.csrfToken},
		"original_name": {"APP_NAME"},
		"name":          {"APP_TITLE"},
		"value":         {"New title"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/variables", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid variables status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-variable-edit-open`,
		`role="alert">A variable with that name already exists.</div>`,
		`name="original_name" value="APP_NAME"`,
		`name="name" type="text" value="APP_TITLE"`,
		`name="value" type="text" value="New title"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("POST invalid variables did not render %q: %s", expected, body)
		}
	}
}

func TestApplicationSecretUpdateRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{
		"original_name": {"API_TOKEN"},
		"name":          {"API_KEY"},
		"value":         {"new-secret"},
	}
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/applications/7/secrets", strings.NewReader(form.Encode()))
	missingTokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingToken := httptest.NewRecorder()
	handler.ServeHTTP(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST secrets without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.secretUpdateID != 0 {
		t.Fatalf("secret update without CSRF = %#v, want no update", applications)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/secrets", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST secrets status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7?tab=secrets" {
		t.Fatalf("POST secrets Location = %q, want secrets tab", got)
	}
	if applications.secretUpdateID != 7 || applications.secretOriginalName != "API_TOKEN" || applications.secretUpdateName != "API_KEY" || applications.secretUpdateValue != "new-secret" {
		t.Fatalf("secret update = (%d, %q, %q, %q), want (7, API_TOKEN, API_KEY, new-secret)", applications.secretUpdateID, applications.secretOriginalName, applications.secretUpdateName, applications.secretUpdateValue)
	}
}

func TestApplicationSecretUpdateLeavesBlankValueUnchanged(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":    {web.csrfToken},
		"original_name": {"API_TOKEN"},
		"name":          {"API_KEY"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/secrets", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST blank secret value status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if applications.secretUpdateValue != "" || applications.secretUpdateReplaceValue {
		t.Fatalf("blank secret update = value %q replace=%v, want unchanged", applications.secretUpdateValue, applications.secretUpdateReplaceValue)
	}
}

func TestApplicationSecretUpdateRequiresExplicitClear(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":    {web.csrfToken},
		"original_name": {"API_TOKEN"},
		"name":          {"API_TOKEN"},
		"clear_value":   {"on"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/secrets", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST clear secret status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if applications.secretUpdateValue != "" || !applications.secretUpdateReplaceValue {
		t.Fatalf("clear secret update = value %q replace=%v, want explicit empty replacement", applications.secretUpdateValue, applications.secretUpdateReplaceValue)
	}
}

func TestApplicationSecretAddRendersPasswordDialogOnError(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		secretAddErr: application.ErrEnvironmentVariableNameRequired,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"operation":  {"add"},
		"name":       {""},
		"value":      {"new-secret"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/secrets", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST add secret status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-secret-edit-open`,
		`<h2 id="secret-edit-dialog-title" data-secret-edit-title>Add secret</h2>`,
		`role="alert">Enter a secret name.</div>`,
		`name="operation" value="add"`,
		`id="secret-edit-value" name="value" type="password" value=""`,
		`data-secret-toggle`,
		`data-secret-generate`,
		`Generate secret`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("POST add secret did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "new-secret") {
		t.Fatal("POST add secret validation response reflected the submitted secret")
	}
}

func TestApplicationSecretDeleteRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{"name": {"API_TOKEN"}}
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/applications/7/secrets/delete", strings.NewReader(form.Encode()))
	missingTokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingToken := httptest.NewRecorder()
	handler.ServeHTTP(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST delete secret without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.secretDeleteID != 0 {
		t.Fatalf("secret delete without CSRF = %#v, want no delete", applications)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/secrets/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST delete secret status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7?tab=secrets" {
		t.Fatalf("POST delete secret Location = %q, want secrets tab", got)
	}
	if applications.secretDeleteID != 7 || applications.secretDeleteName != "API_TOKEN" {
		t.Fatalf("secret delete = (%d, %q), want (7, API_TOKEN)", applications.secretDeleteID, applications.secretDeleteName)
	}
}

func TestApplicationDomainCreateRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{"name": {"example.com"}}
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/applications/7/domains", strings.NewReader(form.Encode()))
	missingTokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingToken := httptest.NewRecorder()
	handler.ServeHTTP(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST domains without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.domainCreateID != 0 {
		t.Fatalf("domain create without CSRF = %#v, want no create", applications)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/domains", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST domains status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7?tab=domains" {
		t.Fatalf("POST domains Location = %q, want domains tab", got)
	}
	if applications.domainCreateID != 7 || applications.domainCreateName != "example.com" {
		t.Fatalf("domain create = (%d, %q), want (7, example.com)", applications.domainCreateID, applications.domainCreateName)
	}
}

func TestApplicationDomainCreateRendersValidationErrorInDialog(t *testing.T) {
	applications := &fakeApplicationService{
		applications:    []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		domainCreateErr: application.ErrDomainNameRequired,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"name":       {""},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/domains", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST domains without name status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-domain-edit-open`,
		`<h2 id="domain-edit-dialog-title">Add domain</h2>`,
		`role="alert">Enter a domain name.</div>`,
		`id="domain-edit-name" name="name" type="text" value=""`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("POST domains without name did not render %q: %s", expected, body)
		}
	}
}

func TestApplicationDomainDeleteRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{"name": {"example.com"}}
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/applications/7/domains/delete", strings.NewReader(form.Encode()))
	missingTokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingToken := httptest.NewRecorder()
	handler.ServeHTTP(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST delete domain without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.domainDeleteID != 0 {
		t.Fatalf("domain delete without CSRF = %#v, want no delete", applications)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/domains/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST delete domain status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7?tab=domains" {
		t.Fatalf("POST delete domain Location = %q, want domains tab", got)
	}
	if applications.domainDeleteID != 7 || applications.domainDeleteName != "example.com" {
		t.Fatalf("domain delete = (%d, %q), want (7, example.com)", applications.domainDeleteID, applications.domainDeleteName)
	}
}

func TestApplicationDomainDeleteRendersErrorInConfirmationDialog(t *testing.T) {
	applications := &fakeApplicationService{
		applications:    []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		domains:         []application.Domain{{ID: 1, ApplicationID: 7, Name: "example.com"}},
		domainDeleteErr: application.ErrDomainNotFound,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"name":       {"example.com"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/domains/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid delete domain status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-domain-delete-open`,
		`<h2 id="domain-delete-dialog-title">Delete domain</h2>`,
		`<code data-domain-delete-name-display>example.com</code>`,
		`name="name" value="example.com" data-domain-delete-name-input`,
		`role="alert">The domain could not be found. Refresh the page and try again.</div>`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("POST invalid delete domain did not render %q: %s", expected, body)
		}
	}
}

func TestServiceDetailsRendersHeaderAndOmitsEnvironmentVariables(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{
			ID:         7,
			Name:       "Status <page>",
			FolderName: "status-page",
		}},
		services: []application.Service{{ID: 1, ApplicationID: 7, Name: "db"}},
		serviceDetails: application.ServiceDetails{
			Service: application.Service{
				ID:                 1,
				ApplicationID:      7,
				Name:               "db",
				Type:               application.ServiceTypePostgreSQL,
				ImageName:          "postgres:17",
				ContainerName:      "redbolt-7-db",
				ContainerCreatedAt: time.Date(2026, time.August, 30, 7, 25, 29, 0, time.UTC),
				Status:             "Up 9 minutes",
				Ports:              "5432/tcp",
			},
			Logs:          "server ready <healthy>\nconnection accepted\n",
			LogsAvailable: true,
			Environment: []application.EnvironmentVariable{
				{Key: "POSTGRES_DB", Value: "status"},
				{Key: "POSTGRES_PASSWORD", Value: "super-secret"},
			},
			EnvironmentAvailable: true,
		},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/db", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /applications/7/services/db status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<title>Redlaunch · db</title>`,
		`<h1 id="page-title">db</h1>`,
		`class="service-details-title"`,
		`class="service-type-icon service-details-type-icon service-type-database"`,
		`aria-label="Database"`,
		`href="/applications/7">Status &lt;page&gt;</a>`,
		`role="group" aria-label="Service actions"`,
		`action="/applications/7/services/db/start"`,
		`action="/applications/7/services/db/stop"`,
		`action="/applications/7/services/db/restart"`,
		`>Start</span>`,
		`>Stop</span>`,
		`>Restart</span>`,
		`General information`,
		`Service Name`,
		`redbolt-7-db`,
		`2026-08-30 07:25`,
		`Up 9 minutes`,
		`5432/tcp`,
		`service-status-badge service-status-running`,
		`Logs`,
		`Most recent 1,000 lines from this service.`,
		`data-service-logs`,
		`data-log-filter-toggle`,
		`Filter`,
		`Download`,
		`Copy`,
		`Download full logs`,
		`href="/applications/7/services/db/logs/download"`,
		`id="backups-tab"`,
		`id="backups-panel"`,
		`server ready &lt;healthy&gt;`,
		`class="service-dashboard"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /applications/7/services/db did not render %q: %s", expected, body)
		}
	}
	if got := strings.Count(body, `<input type="hidden" name="csrf_token"`); got != 3 {
		t.Fatalf("GET /applications/7/services/db rendered %d service action CSRF inputs, want 3", got)
	}
	if got := strings.Count(body, `<input type="hidden" name="return_to" value="service-details">`); got != 3 {
		t.Fatalf("GET /applications/7/services/db rendered %d service action return targets, want 3", got)
	}
	for _, unexpected := range []string{
		"Environment variables",
		"POSTGRES_DB",
		"<code>status</code>",
		"POSTGRES_PASSWORD",
		"super-secret",
		"Sensitive values are masked.",
	} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("GET /applications/7/services/db rendered removed environment content %q: %s", unexpected, body)
		}
	}
	if strings.Count(body, `<h1`) != 1 {
		t.Fatalf("GET /applications/7/services/db rendered %d page headings, want 1", strings.Count(body, `<h1`))
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /applications/7/services/db Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
}

func TestServiceDetailsDownloadsFullLogs(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		serviceDetails: application.ServiceDetails{Service: application.Service{
			ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL,
		}},
		fullServiceLogs: "old line\nnew line\n",
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/db/logs/download", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET service logs download status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.Body.String() != applications.fullServiceLogs {
		t.Fatalf("downloaded service logs = %q, want %q", recorder.Body.String(), applications.fullServiceLogs)
	}
	if recorder.Header().Get("Content-Disposition") != `attachment; filename="service-logs.txt"` {
		t.Fatalf("service log download disposition = %q, want attachment filename", recorder.Header().Get("Content-Disposition"))
	}
	if !applications.fullServiceLogsCalled {
		t.Fatal("service log download did not request the full log history")
	}
}

func TestServiceDetailsRendersOnlyTheMostRecentThousandLogLines(t *testing.T) {
	logs := "oldest line\n" + strings.Repeat("recent line\n", application.ServiceLogLineLimit)
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		serviceDetails: application.ServiceDetails{
			Service:       application.Service{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL},
			Logs:          logs,
			LogsAvailable: true,
		},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/db?tab=logs", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET service logs status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "oldest line") {
		t.Fatalf("service log view rendered a line older than the most recent %d lines", application.ServiceLogLineLimit)
	}
	if got := strings.Count(body, `<li><code>recent line</code></li>`); got != application.ServiceLogLineLimit {
		t.Fatalf("service log view rendered %d lines, want %d", got, application.ServiceLogLineLimit)
	}
}

func TestServiceDetailsRendersServiceTypeIcons(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		serviceType string
		class       string
		label       string
		icon        string
	}{
		{
			name:        "database",
			serviceType: application.ServiceTypePostgreSQL,
			class:       "database",
			label:       "Database",
			icon:        `<ellipse cx="12" cy="5" rx="9" ry="3">`,
		},
		{
			name:        "cache",
			serviceType: "redis",
			class:       "cache",
			label:       "Cache",
			icon:        `<path d="m4 7 8-4 8 4-8 4-8-4Z">`,
		},
		{
			name:        "app",
			serviceType: "worker",
			class:       "app",
			label:       "App",
			icon:        `<rect x="3.5" y="3.5" width="6.5" height="6.5" rx="1">`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			applications := &fakeApplicationService{
				applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
				serviceDetails: application.ServiceDetails{
					Service: application.Service{
						ApplicationID: 7,
						Name:          "worker",
						Type:          testCase.serviceType,
					},
				},
			}
			web, err := New(nil, applications)
			if err != nil {
				t.Fatal(err)
			}

			recorder := httptest.NewRecorder()
			web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/worker", nil))

			if recorder.Code != http.StatusOK {
				t.Fatalf("GET service details status = %d, want %d", recorder.Code, http.StatusOK)
			}
			body := recorder.Body.String()
			for _, expected := range []string{
				`class="service-type-icon service-details-type-icon service-type-` + testCase.class + `"`,
				`aria-label="` + testCase.label + `"`,
				testCase.icon,
			} {
				if !strings.Contains(body, expected) {
					t.Fatalf("GET service details did not render %q: %s", expected, body)
				}
			}
		})
	}
}

func TestServiceDetailsRejectsUnknownService(t *testing.T) {
	applications := &fakeApplicationService{
		applications:      []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		serviceDetailsErr: application.ErrServiceNotFound,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/missing", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET unknown service status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestServiceActionsRequireCSRFAndRedirect(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		path   string
		action string
	}{
		{name: "start", path: "/applications/7/services/db/start", action: "start"},
		{name: "stop", path: "/applications/7/services/db/stop", action: "stop"},
		{name: "restart", path: "/applications/7/services/db/restart", action: "restart"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
			web, err := New(nil, applications)
			if err != nil {
				t.Fatal(err)
			}
			handler := web.Routes()

			missingToken := httptest.NewRecorder()
			handler.ServeHTTP(missingToken, httptest.NewRequest(http.MethodPost, testCase.path, nil))
			if missingToken.Code != http.StatusForbidden {
				t.Fatalf("POST %s without CSRF status = %d, want %d", testCase.path, missingToken.Code, http.StatusForbidden)
			}
			if applications.serviceAction != "" {
				t.Fatalf("service action without CSRF = %q, want no action", applications.serviceAction)
			}

			form := url.Values{"csrf_token": {web.csrfToken}}
			request := httptest.NewRequest(http.MethodPost, testCase.path, strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusSeeOther {
				t.Fatalf("POST %s status = %d, want %d", testCase.path, recorder.Code, http.StatusSeeOther)
			}
			if got := recorder.Header().Get("Location"); got != "/applications/7" {
				t.Fatalf("POST %s Location = %q, want /applications/7", testCase.path, got)
			}
			if applications.serviceAction != testCase.action || applications.serviceActionID != 7 || applications.serviceActionName != "db" {
				t.Fatalf("service action = (%q, %d, %q), want (%q, 7, db)", applications.serviceAction, applications.serviceActionID, applications.serviceActionName, testCase.action)
			}
		})
	}
}

func TestServiceActionsCanReturnToServiceDetails(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		path   string
		action string
	}{
		{name: "start", path: "/applications/7/services/db/start", action: "start"},
		{name: "stop", path: "/applications/7/services/db/stop", action: "stop"},
		{name: "restart", path: "/applications/7/services/db/restart", action: "restart"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
			web, err := New(nil, applications)
			if err != nil {
				t.Fatal(err)
			}

			form := url.Values{"csrf_token": {web.csrfToken}, "return_to": {"service-details"}}
			request := httptest.NewRequest(http.MethodPost, testCase.path, strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
			recorder := httptest.NewRecorder()
			web.Routes().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusSeeOther {
				t.Fatalf("POST %s status = %d, want %d", testCase.path, recorder.Code, http.StatusSeeOther)
			}
			if got := recorder.Header().Get("Location"); got != "/applications/7/services/db" {
				t.Fatalf("POST %s Location = %q, want /applications/7/services/db", testCase.path, got)
			}
			if applications.serviceAction != testCase.action || applications.serviceActionID != 7 || applications.serviceActionName != "db" {
				t.Fatalf("service action = (%q, %d, %q), want (%q, 7, db)", applications.serviceAction, applications.serviceActionID, applications.serviceActionName, testCase.action)
			}
		})
	}
}

func TestDeleteServiceRequiresCSRFAndExactConfirmation(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(closeRelease)
	applications := &fakeApplicationService{
		applications:         []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:             []application.Service{{ID: 1, ApplicationID: 7, Name: "db"}},
		serviceDeleteStarted: make(chan struct{}),
		serviceDeleteDone:    make(chan struct{}),
		serviceDeleteRelease: release,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{"confirmation": {"db"}}
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/applications/7/services/db/delete", strings.NewReader(form.Encode()))
	missingTokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingToken := httptest.NewRecorder()
	handler.ServeHTTP(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST delete without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.serviceDeleteName != "" {
		t.Fatalf("service deletion without CSRF = %q, want no deletion", applications.serviceDeleteName)
	}

	wrongConfirmation := url.Values{"csrf_token": {web.csrfToken}, "confirmation": {"DB"}}
	wrongRequest := httptest.NewRequest(http.MethodPost, "/applications/7/services/db/delete", strings.NewReader(wrongConfirmation.Encode()))
	wrongRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wrongRequest.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	wrongRecorder := httptest.NewRecorder()
	handler.ServeHTTP(wrongRecorder, wrongRequest)
	if wrongRecorder.Code != http.StatusBadRequest {
		t.Fatalf("POST delete with wrong confirmation status = %d, want %d", wrongRecorder.Code, http.StatusBadRequest)
	}
	if applications.serviceDeleteName != "" {
		t.Fatalf("service deletion with wrong confirmation = %q, want no deletion", applications.serviceDeleteName)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/db/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST delete status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	jobID := location.Query().Get("service_delete_job")
	if location.Path != "/applications/7" || jobID == "" {
		t.Fatalf("POST delete Location = %q, want application with service deletion job", location.String())
	}
	select {
	case <-applications.serviceDeleteStarted:
	case <-time.After(time.Second):
		t.Fatal("service deletion job did not start")
	}
	if applications.serviceDeleteID != 7 || applications.serviceDeleteName != "db" {
		t.Fatalf("service deletion target = (%d, %q), want (7, db)", applications.serviceDeleteID, applications.serviceDeleteName)
	}

	pageRecorder := httptest.NewRecorder()
	handler.ServeHTTP(pageRecorder, httptest.NewRequest(http.MethodGet, location.String(), nil))
	if pageRecorder.Code != http.StatusOK {
		t.Fatalf("GET service deletion progress page status = %d, want %d", pageRecorder.Code, http.StatusOK)
	}
	for _, expected := range []string{
		`data-service-delete-progress`,
		`data-state="running"`,
		`aria-busy="true"`,
		"Deleting service",
		"Stop service container",
		"Remove service container",
		"Remove service from Compose file",
		"Delete service metadata",
		`/static/setup.js`,
	} {
		if !strings.Contains(pageRecorder.Body.String(), expected) {
			t.Fatalf("running service deletion page did not render %q: %s", expected, pageRecorder.Body.String())
		}
	}

	closeRelease()
	select {
	case <-applications.serviceDeleteDone:
	case <-time.After(time.Second):
		t.Fatal("service deletion job did not finish")
	}
	statusRecorder := httptest.NewRecorder()
	handler.ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/db/delete/status?id="+url.QueryEscape(jobID), nil))
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), "Service deleted") {
		t.Fatalf("completed service deletion status = (%d, %s), want completed progress dialog", statusRecorder.Code, statusRecorder.Body.String())
	}
}

func TestDeleteServiceShowsFailedProgressStage(t *testing.T) {
	applications := &fakeApplicationService{
		applications:        []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:            []application.Service{{ID: 1, ApplicationID: 7, Name: "db"}},
		serviceDeleteErr:    errors.New("remove service container: docker failed"),
		serviceDeleteDone:   make(chan struct{}),
		serviceDeleteStages: []string{"stop", "remove"},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {web.csrfToken}, "confirmation": {"db"}}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/db/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST failed delete status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-applications.serviceDeleteDone:
	case <-time.After(time.Second):
		t.Fatal("failed service deletion job did not finish")
	}
	statusRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/db/delete/status?id="+url.QueryEscape(location.Query().Get("service_delete_job")), nil))
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("GET failed service deletion status = %d, want %d", statusRecorder.Code, http.StatusOK)
	}
	body := statusRecorder.Body.String()
	for _, expected := range []string{"Service deletion stopped", "Remove service container", "could not be removed"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("failed service deletion response did not render %q: %s", expected, body)
		}
	}
}

func TestDeleteApplicationRequiresCSRFAndExactConfirmation(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(closeRelease)
	applications := &fakeApplicationService{
		applications:               []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		applicationDeleteRemoveApp: true,
		applicationDeleteStarted:   make(chan struct{}),
		applicationDeleteDone:      make(chan struct{}),
		applicationDeleteRelease:   release,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{"confirmation": {"Status page"}}
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/applications/7/delete", strings.NewReader(form.Encode()))
	missingTokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingToken := httptest.NewRecorder()
	handler.ServeHTTP(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST application delete without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.applicationDeleteID != 0 {
		t.Fatalf("application deletion without CSRF = %d, want no deletion", applications.applicationDeleteID)
	}

	wrongConfirmation := url.Values{"csrf_token": {web.csrfToken}, "confirmation": {"status page"}}
	wrongRequest := httptest.NewRequest(http.MethodPost, "/applications/7/delete", strings.NewReader(wrongConfirmation.Encode()))
	wrongRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wrongRequest.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	wrongRecorder := httptest.NewRecorder()
	handler.ServeHTTP(wrongRecorder, wrongRequest)
	if wrongRecorder.Code != http.StatusBadRequest {
		t.Fatalf("POST application delete with wrong confirmation status = %d, want %d", wrongRecorder.Code, http.StatusBadRequest)
	}
	if applications.applicationDeleteID != 0 {
		t.Fatalf("application deletion with wrong confirmation = %d, want no deletion", applications.applicationDeleteID)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST application delete status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	jobID := location.Query().Get("application_delete_job")
	if location.Path != "/applications/7" || jobID == "" {
		t.Fatalf("POST application delete Location = %q, want application with deletion job", location.String())
	}
	select {
	case <-applications.applicationDeleteStarted:
	case <-time.After(time.Second):
		t.Fatal("application deletion job did not start")
	}
	if applications.applicationDeleteID != 7 {
		t.Fatalf("application deletion target = %d, want 7", applications.applicationDeleteID)
	}

	pageRecorder := httptest.NewRecorder()
	handler.ServeHTTP(pageRecorder, httptest.NewRequest(http.MethodGet, location.String(), nil))
	if pageRecorder.Code != http.StatusOK {
		t.Fatalf("GET application deletion progress page status = %d, want %d", pageRecorder.Code, http.StatusOK)
	}
	for _, expected := range []string{
		`data-application-delete-progress`,
		`data-state="running"`,
		`aria-busy="true"`,
		"Deleting application",
		"Remove application Docker resources",
		"Delete application metadata",
		"Delete application folder",
		`/static/setup.js`,
	} {
		if !strings.Contains(pageRecorder.Body.String(), expected) {
			t.Fatalf("running application deletion page did not render %q: %s", expected, pageRecorder.Body.String())
		}
	}

	closeRelease()
	select {
	case <-applications.applicationDeleteDone:
	case <-time.After(time.Second):
		t.Fatal("application deletion job did not finish")
	}
	statusRecorder := httptest.NewRecorder()
	handler.ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/delete/status?id="+url.QueryEscape(jobID), nil))
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), "Application deleted") {
		t.Fatalf("completed application deletion status = (%d, %s), want completed progress dialog", statusRecorder.Code, statusRecorder.Body.String())
	}
	deletedPageRecorder := httptest.NewRecorder()
	handler.ServeHTTP(deletedPageRecorder, httptest.NewRequest(http.MethodGet, location.String(), nil))
	if deletedPageRecorder.Code != http.StatusOK || !strings.Contains(deletedPageRecorder.Body.String(), "Application deleted") {
		t.Fatalf("deleted application progress page = (%d, %s), want completed progress dialog", deletedPageRecorder.Code, deletedPageRecorder.Body.String())
	}
}

func TestDeleteApplicationShowsFailedProgressStage(t *testing.T) {
	applications := &fakeApplicationService{
		applications:            []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		applicationDeleteErr:    errors.New("remove application resources: docker failed"),
		applicationDeleteDone:   make(chan struct{}),
		applicationDeleteStages: []string{"resources"},
	}
	githubActions := &fakeGitHubActionsService{integration: application.GitHubActionsIntegration{ApplicationID: 7}}
	web, err := New(nil, applications, githubActions)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {web.csrfToken}, "confirmation": {"Status page"}}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST failed application delete status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-applications.applicationDeleteDone:
	case <-time.After(time.Second):
		t.Fatal("failed application deletion job did not finish")
	}
	statusRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/delete/status?id="+url.QueryEscape(location.Query().Get("application_delete_job")), nil))
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("GET failed application deletion status = %d, want %d", statusRecorder.Code, http.StatusOK)
	}
	body := statusRecorder.Body.String()
	for _, expected := range []string{"Application deletion stopped", "Remove application Docker resources", "Docker resources could not be removed"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("failed application deletion response did not render %q: %s", expected, body)
		}
	}
	if githubActions.revokeCalls != 0 {
		t.Fatalf("GitHub Actions revoke calls after failed deletion = %d, want zero", githubActions.revokeCalls)
	}
	if githubActions.cleanupCalls != 1 {
		t.Fatalf("GitHub Actions cleanup calls after failed deletion = %d, want one idempotent check", githubActions.cleanupCalls)
	}
}

func TestDeleteApplicationCleansUpGitHubActionsAccessAfterDeletion(t *testing.T) {
	applications := &fakeApplicationService{
		applications:               []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		applicationDeleteRemoveApp: true,
		applicationDeleteDone:      make(chan struct{}),
	}
	githubActions := &fakeGitHubActionsService{
		integration: application.GitHubActionsIntegration{ApplicationID: 7, Repository: "acme/status-page"},
	}
	web, err := New(nil, applications, githubActions)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {web.csrfToken}, "confirmation": {"Status page"}}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST application delete status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	jobID := location.Query().Get("application_delete_job")
	if jobID == "" {
		t.Fatalf("POST application delete Location = %q, want deletion job", location.String())
	}
	select {
	case <-applications.applicationDeleteDone:
	case <-time.After(time.Second):
		t.Fatal("application deletion job did not finish")
	}
	deadline := time.Now().Add(time.Second)
	var completed bool
	for time.Now().Before(deadline) {
		job := web.applicationDeleteJobs.get(7, jobID)
		if job != nil && job.snapshot().State != applicationDeleteJobStateRunning {
			completed = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !completed {
		t.Fatal("application deletion job did not reach a terminal state")
	}
	if githubActions.revokeCalls != 0 {
		t.Fatalf("GitHub Actions revoke calls = %d, want zero before deletion", githubActions.revokeCalls)
	}
	if githubActions.cleanupCalls != 1 {
		t.Fatalf("GitHub Actions cleanup calls = %d, want one", githubActions.cleanupCalls)
	}
}

func TestServiceStatusClassDistinguishesStartedAndStopped(t *testing.T) {
	if got := serviceStatusClass("Up 9 minutes"); got != "service-status-running" {
		t.Fatalf("serviceStatusClass(Up) = %q, want service-status-running", got)
	}
	if got := serviceStatusClass("Stopped"); got != "service-status-failed" {
		t.Fatalf("serviceStatusClass(Stopped) = %q, want service-status-failed", got)
	}
}

func TestServiceLogLinesTrimsOnlyOuterLineBreaks(t *testing.T) {
	got := serviceLogLines("\nfirst\n\nthird\n")
	want := []string{"first", "", "third"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serviceLogLines() = %#v, want %#v", got, want)
	}
}

func TestPostgreSQLServicePageRendersDefaults(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{
		ID:         7,
		Name:       "Status <page>",
		FolderName: "status-page",
	}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/postgresql/new", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET PostgreSQL service page status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<h1 id="page-title">Create PostgreSQL database</h1>`,
		`name="service_name"`,
		`value="db"`,
		`name="postgres_version"`,
		`value="17"`,
		`name="database_name"`,
		`value="Status &lt;page&gt;"`,
		`name="database_user"`,
		`value="appuser"`,
		`name="database_password"`,
		`action="/applications/7/services/postgresql/new"`,
		`name="csrf_token"`,
		`Leave empty and Redlaunch will generate a secure random password.`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET PostgreSQL service page did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `value="Status <page>"`) {
		t.Fatalf("GET PostgreSQL service page rendered unescaped application data: %s", body)
	}
	if strings.Count(body, `<h1`) != 1 {
		t.Fatalf("GET PostgreSQL service page rendered %d page headings, want 1", strings.Count(body, `<h1`))
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET PostgreSQL service page Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
}

func TestCreatePostgreSQLServiceRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{
		ID:         7,
		Name:       "Status page",
		FolderName: "status-page",
	}}, postgresDone: make(chan struct{})}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()
	form := url.Values{
		"service_name":      {"database"},
		"postgres_version":  {"17.1"},
		"database_name":     {"status"},
		"database_user":     {"status_user"},
		"database_password": {"submitted-password"},
	}

	missingToken := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodPost, "/applications/7/services/postgresql/new", strings.NewReader(form.Encode()))
	missingRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(missingToken, missingRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST PostgreSQL service without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.postgresInput.ServiceName != "" {
		t.Fatalf("PostgreSQL service was created without CSRF: %#v", applications.postgresInput)
	}
	if strings.Contains(missingToken.Body.String(), form.Get("database_password")) {
		t.Fatalf("CSRF error response exposed the submitted password: %s", missingToken.Body.String())
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/postgresql/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST PostgreSQL service status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Path != "/applications/7" || location.Query().Get("postgres_job") == "" {
		t.Fatalf("POST PostgreSQL service Location = %q, want application with postgres job", location.String())
	}
	select {
	case <-applications.postgresDone:
	case <-time.After(time.Second):
		t.Fatal("PostgreSQL service job did not finish")
	}
	statusRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/postgresql/status?id="+url.QueryEscape(location.Query().Get("postgres_job")), nil))
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), "PostgreSQL database ready") {
		t.Fatalf("completed PostgreSQL service status = (%d, %s), want completed progress dialog", statusRecorder.Code, statusRecorder.Body.String())
	}
	if applications.postgresInput.ServiceName != "database" || applications.postgresInput.PostgresVersion != "17.1" || applications.postgresInput.DatabaseName != "status" || applications.postgresInput.DatabaseUser != "status_user" || applications.postgresInput.DatabasePassword != "submitted-password" {
		t.Fatalf("PostgreSQL service input = %#v, want submitted fields", applications.postgresInput)
	}
}

func TestCreatePostgreSQLServiceShowsProgressModalWhileRunning(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseJob := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseJob()
	applications := &fakeApplicationService{
		applications:    []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		postgresStarted: make(chan struct{}),
		postgresRelease: release,
		postgresDone:    make(chan struct{}),
		postgresStages:  []string{"configuration", "files", "metadata", "start"},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token":       {web.csrfToken},
		"service_name":     {"db"},
		"postgres_version": {"17"},
		"database_name":    {"status"},
		"database_user":    {"appuser"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/postgresql/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	postRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(postRecorder, request)
	if postRecorder.Code != http.StatusSeeOther {
		t.Fatalf("POST PostgreSQL service status = %d, want %d", postRecorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(postRecorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-applications.postgresStarted:
	case <-time.After(time.Second):
		t.Fatal("PostgreSQL service job did not start")
	}

	pageRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(pageRecorder, httptest.NewRequest(http.MethodGet, location.String(), nil))
	if pageRecorder.Code != http.StatusOK {
		t.Fatalf("GET PostgreSQL progress page status = %d, want %d", pageRecorder.Code, http.StatusOK)
	}
	body := pageRecorder.Body.String()
	for _, expected := range []string{
		`data-postgresql-progress`,
		`data-state="running"`,
		`aria-busy="true"`,
		"Creating PostgreSQL database",
		"Start PostgreSQL container",
		`/static/setup.js`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("running PostgreSQL progress page did not render %q: %s", expected, body)
		}
	}

	releaseJob()
	select {
	case <-applications.postgresDone:
	case <-time.After(time.Second):
		t.Fatal("PostgreSQL service job did not finish")
	}
}

func TestCreatePostgreSQLServiceRendersValidationErrorWithoutPassword(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		postgresErr:  application.ErrServiceNameInvalid,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":        {web.csrfToken},
		"service_name":      {"bad/name"},
		"postgres_version":  {"17"},
		"database_name":     {"status"},
		"database_user":     {"appuser"},
		"database_password": {"submitted-password"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/postgresql/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid PostgreSQL service status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{"Service names may contain only letters", `role="alert"`, `value="bad/name"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("invalid PostgreSQL service response did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, form.Get("database_password")) {
		t.Fatalf("invalid PostgreSQL service response exposed the password: %s", body)
	}
}

func TestCreatePostgreSQLServiceRendersStartupContextAndRedactsSecrets(t *testing.T) {
	applications := &fakeApplicationService{
		applications:   []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		postgresErr:    errors.New("start PostgreSQL service: run compose project: exit status 1: Bind for 0.0.0.0:5432 failed: port is already allocated\nPOSTGRES_PASSWORD=super-secret"),
		postgresStages: []string{"start"},
		postgresDone:   make(chan struct{}),
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":       {web.csrfToken},
		"service_name":     {"db"},
		"postgres_version": {"17"},
		"database_name":    {"status"},
		"database_user":    {"appuser"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/postgresql/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST failed PostgreSQL service status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	jobID := location.Query().Get("postgres_job")
	if jobID == "" {
		t.Fatalf("POST failed PostgreSQL service Location = %q, want postgres job", location.String())
	}
	select {
	case <-applications.postgresDone:
	case <-time.After(time.Second):
		t.Fatal("failed PostgreSQL service job did not finish")
	}
	statusRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/postgresql/status?id="+url.QueryEscape(jobID), nil))
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("GET failed PostgreSQL service status = %d, want %d", statusRecorder.Code, http.StatusOK)
	}
	body := statusRecorder.Body.String()
	for _, expected := range []string{
		"Database creation stopped",
		"Start PostgreSQL container",
		"the container could not be started",
		"a required port is already in use",
		"port is already allocated",
	} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(expected)) {
			t.Fatalf("failed PostgreSQL service response did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "super-secret") {
		t.Fatalf("failed PostgreSQL service response exposed a password: %s", body)
	}
}

func TestRedisServicePageRendersDefaults(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{
		ID:         7,
		Name:       "Status <page>",
		FolderName: "status-page",
	}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/redis/new", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET Redis service page status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<h1 id="page-title">Create Redis service</h1>`,
		`name="service_name"`,
		`value="redis"`,
		`name="redis_version"`,
		`value="7"`,
		`name="port"`,
		`value="6379"`,
		`name="password"`,
		`name="persist_to_disk"`,
		`action="/applications/7/services/redis/new"`,
		`name="csrf_token"`,
		`Persist to disk`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET Redis service page did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `value="Status <page>"`) {
		t.Fatalf("GET Redis service page rendered unescaped application data: %s", body)
	}
	if strings.Count(body, `<h1`) != 1 {
		t.Fatalf("GET Redis service page rendered %d page headings, want 1", strings.Count(body, `<h1`))
	}
}

func TestCreateRedisServiceRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{
		ID:         7,
		Name:       "Status page",
		FolderName: "status-page",
	}}, redisDone: make(chan struct{})}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()
	form := url.Values{
		"service_name":    {"cache"},
		"redis_version":   {"7.2"},
		"port":            {"6380"},
		"password":        {"submitted-password"},
		"persist_to_disk": {"on"},
	}

	missingToken := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodPost, "/applications/7/services/redis/new", strings.NewReader(form.Encode()))
	missingRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(missingToken, missingRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST Redis service without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.redisInput.ServiceName != "" {
		t.Fatalf("Redis service was created without CSRF: %#v", applications.redisInput)
	}
	if strings.Contains(missingToken.Body.String(), form.Get("password")) {
		t.Fatalf("CSRF error response exposed the submitted password: %s", missingToken.Body.String())
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/redis/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST Redis service status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Path != "/applications/7" || location.Query().Get("redis_job") == "" {
		t.Fatalf("POST Redis service Location = %q, want application with Redis job", location.String())
	}
	select {
	case <-applications.redisDone:
	case <-time.After(time.Second):
		t.Fatal("Redis service job did not finish")
	}
	statusRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/redis/status?id="+url.QueryEscape(location.Query().Get("redis_job")), nil))
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), "Redis service ready") {
		t.Fatalf("completed Redis service status = (%d, %s), want completed progress dialog", statusRecorder.Code, statusRecorder.Body.String())
	}
	if applications.redisInput.ServiceName != "cache" || applications.redisInput.RedisVersion != "7.2" || applications.redisInput.Port != "6380" || applications.redisInput.Password != "submitted-password" || !applications.redisInput.PersistToDisk {
		t.Fatalf("Redis service input = %#v, want submitted fields", applications.redisInput)
	}
}

func TestCreateRedisServiceShowsProgressModalWhileRunning(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseJob := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseJob()
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		redisStarted: make(chan struct{}),
		redisRelease: release,
		redisDone:    make(chan struct{}),
		redisStages:  []string{"configuration", "files", "metadata", "start"},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token":      {web.csrfToken},
		"service_name":    {"redis"},
		"redis_version":   {"7"},
		"port":            {"6379"},
		"persist_to_disk": {"on"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/redis/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	postRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(postRecorder, request)
	if postRecorder.Code != http.StatusSeeOther {
		t.Fatalf("POST Redis service status = %d, want %d", postRecorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(postRecorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-applications.redisStarted:
	case <-time.After(time.Second):
		t.Fatal("Redis service job did not start")
	}

	pageRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(pageRecorder, httptest.NewRequest(http.MethodGet, location.String(), nil))
	if pageRecorder.Code != http.StatusOK {
		t.Fatalf("GET Redis progress page status = %d, want %d", pageRecorder.Code, http.StatusOK)
	}
	body := pageRecorder.Body.String()
	for _, expected := range []string{
		`data-redis-progress`,
		`data-state="running"`,
		`aria-busy="true"`,
		"Creating Redis service",
		"Start Redis container",
		`/static/setup.js`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("running Redis progress page did not render %q: %s", expected, body)
		}
	}

	releaseJob()
	select {
	case <-applications.redisDone:
	case <-time.After(time.Second):
		t.Fatal("Redis service job did not finish")
	}
}

func TestCreateRedisServiceRendersValidationErrorWithoutPassword(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":    {web.csrfToken},
		"service_name":  {"bad/name"},
		"redis_version": {"7"},
		"port":          {"6379"},
		"password":      {"submitted-password"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/redis/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid Redis service status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{"Service names may contain only letters", `role="alert"`, `value="bad/name"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("invalid Redis service response did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, form.Get("password")) {
		t.Fatalf("invalid Redis service response exposed the password: %s", body)
	}
}

func TestApplicationContainerPageRendersLocalRegistryDefault(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{
		ID:         7,
		Name:       "Status <page>",
		FolderName: "status-page",
	}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/application/new", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET application container page status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<h1 id="page-title">Create application container</h1>`,
		`name="service_name"`,
		`value="app"`,
		`name="image_name"`,
		`value="app:latest"`,
		`name="use_docker_registry"`,
		`Use Docker Registry`,
		`name="auto_start"`,
		`role="switch"`,
		`Automatically start container`,
		`action="/applications/7/services/application/new"`,
		`local registry at <code>localhost:5000</code>`,
		`Status &lt;page&gt;`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET application container page did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `value="Status <page>"`) {
		t.Fatalf("GET application container page rendered unescaped application data: %s", body)
	}
	if strings.Contains(body, `name="use_docker_registry" type="checkbox" role="switch" value="on" checked`) {
		t.Fatalf("GET application container page checked the Docker Registry option by default: %s", body)
	}
	if strings.Contains(body, `name="auto_start" type="checkbox" role="switch" value="on" checked`) {
		t.Fatalf("GET application container page enabled automatic startup by default: %s", body)
	}
	if strings.Contains(body, `value="localhost:5000/app:latest"`) {
		t.Fatalf("GET application container page rendered the local registry in the image field: %s", body)
	}
	if strings.Count(body, `<h1`) != 1 {
		t.Fatalf("GET application container page rendered %d page headings, want 1", strings.Count(body, `<h1`))
	}
}

func TestApplicationContainerPageRendersAdvancedSettingsAndDependencyOptions(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services: []application.Service{
			{Name: "db", Type: application.ServiceTypePostgreSQL},
			{Name: "cache", Type: application.ServiceTypeRedis},
		},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/application/new", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET advanced application container page status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"Advanced settings",
		"Custom entrypoint command",
		`name="healthcheck_command"`,
		`name="healthcheck_interval"`,
		`name="restart_policy"`,
		`name="depends_on_service"`,
		`option value="db">db</option>`,
		`option value="cache">cache</option>`,
		`option value="service_started" selected>Service started (default)</option>`,
		`data-repeatable-add="application-container-dependencies"`,
		`data-repeatable-add="application-container-ports"`,
		`data-repeatable-add="application-container-volumes"`,
		`name="volume_options"`,
		`/static/application-container.js`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET advanced application container page did not render %q: %s", expected, body)
		}
	}
	previous := -1
	for _, legend := range []string{
		"<legend>Volumes</legend>",
		"<legend>Port mappings</legend>",
		"<legend>Dependencies</legend>",
		"<legend>Process</legend>",
		"<legend>Healthcheck</legend>",
	} {
		position := strings.Index(body, legend)
		if position <= previous {
			t.Fatalf("GET advanced application container groups are out of order at %q", legend)
		}
		previous = position
	}
}

func TestCreateApplicationContainerPassesAdvancedSettings(t *testing.T) {
	applications := &fakeApplicationService{
		applications:             []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		applicationContainerDone: make(chan struct{}),
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":               {web.csrfToken},
		"service_name":             {"web"},
		"image_name":               {"ghcr.io/example/web:v1"},
		"use_docker_registry":      {"on"},
		"entrypoint":               {"/usr/local/bin/start --serve"},
		"healthcheck_command":      {"curl --fail http://localhost/health"},
		"healthcheck_interval":     {"10s"},
		"healthcheck_timeout":      {"3s"},
		"healthcheck_retries":      {"5"},
		"healthcheck_start_period": {"20s"},
		"depends_on_service":       {"db", "cache"},
		"depends_on_condition":     {"service_healthy", "service_started"},
		"restart_policy":           {"on-failure"},
		"port_host":                {"8080", "8443"},
		"port_container":           {"80", "443"},
		"port_protocol":            {"tcp", "udp"},
		"volume_source":            {"app-data", "./cache"},
		"volume_target":            {"/var/lib/app", "/cache"},
		"volume_options":           {"ro", "rw,z"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/application/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST advanced application container status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	select {
	case <-applications.applicationContainerDone:
	case <-time.After(time.Second):
		t.Fatal("advanced application container job did not finish")
	}

	want := application.ApplicationServiceInput{
		ServiceName: "web",
		ImageName:   "ghcr.io/example/web:v1",
		Entrypoint:  "/usr/local/bin/start --serve",
		Healthcheck: application.ApplicationHealthcheck{
			Command:     "curl --fail http://localhost/health",
			Interval:    "10s",
			Timeout:     "3s",
			Retries:     "5",
			StartPeriod: "20s",
		},
		DependsOn: []application.ApplicationServiceDependency{
			{ServiceName: "db", Condition: "service_healthy"},
			{ServiceName: "cache", Condition: "service_started"},
		},
		RestartPolicy: "on-failure",
		PortMappings: []application.ApplicationPortMapping{
			{HostPort: "8080", ContainerPort: "80", Protocol: "tcp"},
			{HostPort: "8443", ContainerPort: "443", Protocol: "udp"},
		},
		VolumeMappings: []application.ApplicationVolumeMapping{
			{Source: "app-data", Target: "/var/lib/app", Options: "ro"},
			{Source: "./cache", Target: "/cache", Options: "rw,z"},
		},
	}
	if !reflect.DeepEqual(applications.applicationContainerInput, want) {
		t.Fatalf("application container input = %#v, want %#v", applications.applicationContainerInput, want)
	}
}

func TestCreateApplicationContainerRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{
		applications:             []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		applicationContainer:     application.Service{Type: application.ServiceTypeApplication, ImageName: "ghcr.io/example/web:v1"},
		applicationContainerDone: make(chan struct{}),
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()
	form := url.Values{
		"service_name":        {"web"},
		"image_name":          {"ghcr.io/example/web:v1"},
		"use_docker_registry": {"on"},
		"auto_start":          {"on"},
	}

	missingToken := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodPost, "/applications/7/services/application/new", strings.NewReader(form.Encode()))
	missingRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(missingToken, missingRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST application container without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.applicationContainerInput.ServiceName != "" {
		t.Fatalf("application container was created without CSRF: %#v", applications.applicationContainerInput)
	}

	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/application/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST application container status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	jobID := location.Query().Get("application_job")
	if location.Path != "/applications/7" || jobID == "" {
		t.Fatalf("POST application container Location = %q, want application with job", location.String())
	}
	select {
	case <-applications.applicationContainerDone:
	case <-time.After(time.Second):
		t.Fatal("application container job did not finish")
	}
	statusRecorder := httptest.NewRecorder()
	handler.ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/application/status?id="+url.QueryEscape(jobID), nil))
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), "Application container ready") {
		t.Fatalf("completed application container status = (%d, %s), want completed progress dialog", statusRecorder.Code, statusRecorder.Body.String())
	}
	if applications.applicationContainerInput.ServiceName != "web" || applications.applicationContainerInput.ImageName != "ghcr.io/example/web:v1" {
		t.Fatalf("application container input = %#v, want submitted fields", applications.applicationContainerInput)
	}
	if !applications.applicationContainerInput.AutoStart {
		t.Fatalf("application container AutoStart = false, want true")
	}
}

func TestCreateApplicationContainerUsesLocalRegistryWhenCheckboxIsUnchecked(t *testing.T) {
	applications := &fakeApplicationService{
		applications:             []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		applicationContainerDone: make(chan struct{}),
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":   {web.csrfToken},
		"service_name": {"worker"},
		"image_name":   {"worker:latest"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/application/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST local registry application container status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	select {
	case <-applications.applicationContainerDone:
	case <-time.After(time.Second):
		t.Fatal("local registry application container job did not finish")
	}
	if applications.applicationContainerInput.ImageName != "localhost:5000/worker:latest" || applications.applicationContainerInput.AutoStart {
		t.Fatalf("application container input = %#v, want local image with automatic startup disabled", applications.applicationContainerInput)
	}
}

func TestCreateApplicationContainerUsesDefaultImageWhenImageIsEmpty(t *testing.T) {
	applications := &fakeApplicationService{
		applications:             []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		applicationContainerDone: make(chan struct{}),
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":   {web.csrfToken},
		"service_name": {"worker"},
		"image_name":   {""},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/application/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST default application container status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	select {
	case <-applications.applicationContainerDone:
	case <-time.After(time.Second):
		t.Fatal("default application container job did not finish")
	}
	if applications.applicationContainerInput.ServiceName != "worker" || applications.applicationContainerInput.ImageName != "localhost:5000/worker:latest" || applications.applicationContainerInput.AutoStart {
		t.Fatalf("default application container input = %#v, want local registry default", applications.applicationContainerInput)
	}
}

func TestCreateApplicationContainerRendersImageValidationError(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":   {web.csrfToken},
		"service_name": {"web"},
		"image_name":   {"bad/name with spaces"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/application/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid application container status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{"Enter a valid Docker image reference.", `role="alert"`, `value="bad/name with spaces"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("invalid application container response did not render %q: %s", expected, body)
		}
	}
}

func TestApplicationDetailsRejectsInvalidAndMissingApplications(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/applications/not-an-id", "/applications/999"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("GET %s status = %d, want %d", path, recorder.Code, http.StatusNotFound)
			}
		})
	}
}

func TestCreateApplicationRequiresCSRFAndPersistsFormFields(t *testing.T) {
	applications := &fakeApplicationService{}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	missingToken := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodPost, "/applications", strings.NewReader("name=app&folder_name=app"))
	missingRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(missingToken, missingRequest)
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("POST /applications without CSRF status = %d, want %d", missingToken.Code, http.StatusForbidden)
	}
	if applications.created.Name != "" {
		t.Fatalf("application was created without CSRF: %#v", applications.created)
	}

	form := url.Values{
		"name":        {"Status page"},
		"folder_name": {"status-page"},
		"csrf_token":  {web.csrfToken},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /applications status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications" {
		t.Fatalf("POST /applications Location = %q, want /applications", got)
	}
	if applications.created.Name != "Status page" || applications.created.FolderName != "status-page" {
		t.Fatalf("created application = %#v, want submitted fields", applications.created)
	}
}

func TestCreateApplicationRendersValidationErrorInOpenModal(t *testing.T) {
	applications := &fakeApplicationService{createErr: application.ErrFolderNameInvalid}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"name":        {"Status page"},
		"folder_name": {"../outside"},
		"csrf_token":  {web.csrfToken},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST /applications invalid status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{`open>`, "single, valid directory name", "../outside", `role="alert"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("invalid application response did not render %q: %s", expected, body)
		}
	}
}

func TestProjectsRoutesAreRemoved(t *testing.T) {
	web, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/projects", "/projects/1/actions/up"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("GET %s status = %d, want %d", path, recorder.Code, http.StatusNotFound)
			}
		})
	}
}

func TestHealth(t *testing.T) {
	web, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok\n" {
		t.Fatalf("GET /healthz = (%d, %q), want (200, %q)", recorder.Code, recorder.Body.String(), "ok\n")
	}
}

func TestAuthenticationRedirectsToLoginAndRendersGoogleButton(t *testing.T) {
	authentication := &fakeAuthenticationService{
		enabled:          true,
		authorizationURL: "https://accounts.example.test/authorize",
	}
	web, err := New(nil, authentication)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("GET / status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/login?next=%2F" {
		t.Fatalf("GET / Location = %q, want /login?next=%%2F", got)
	}

	loginRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodGet, "/login?next=https://evil.example", nil))
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("GET /login status = %d, want %d", loginRecorder.Code, http.StatusOK)
	}
	body := loginRecorder.Body.String()
	if strings.Count(body, "Login with Google") != 1 || !strings.Contains(body, `class="google-logo"`) {
		t.Fatalf("login page did not render one Google button and logo: %s", body)
	}
	if !strings.Contains(body, `<img class="brand-logo" src="/static/redlaunch-logo.svg" alt="Redlaunch"`) {
		t.Fatalf("login page did not render the Redlaunch logo: %s", body)
	}
	if strings.Contains(body, `class="sidebar"`) || !strings.Contains(body, `href="/auth/google?next=%2F"`) {
		t.Fatalf("login page rendered the wrong shell or redirect target: %s", body)
	}

	staticRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(staticRecorder, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if staticRecorder.Code != http.StatusOK {
		t.Fatalf("GET /static/app.css status = %d, want %d", staticRecorder.Code, http.StatusOK)
	}
}

func TestAuthenticationUsesConfiguredPublicHostForOAuthRedirect(t *testing.T) {
	const wantRedirectURL = "https://redlaunch.example.com/auth/google/callback"
	authentication := &fakeAuthenticationService{
		enabled:          true,
		authorizationURL: "https://accounts.example.test/authorize",
	}
	applications := &fakeApplicationService{
		publicAccess: application.RedlaunchPublicAccess{
			Enabled: true,
			Domain:  "Redlaunch.Example.COM",
		},
	}
	web, err := New(nil, applications, authentication)
	if err != nil {
		t.Fatal(err)
	}

	publicRequest := httptest.NewRequest(http.MethodGet, "https://redlaunch.example.com/auth/google", nil)
	publicRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(publicRecorder, publicRequest)
	if publicRecorder.Code != http.StatusFound {
		t.Fatalf("public GET /auth/google status = %d, want %d", publicRecorder.Code, http.StatusFound)
	}
	if authentication.authorizationRedirectURL != wantRedirectURL {
		t.Fatalf("public OAuth redirect URL = %q, want %q", authentication.authorizationRedirectURL, wantRedirectURL)
	}

	localRequest := httptest.NewRequest(http.MethodGet, "http://localhost:8080/auth/google", nil)
	localRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(localRecorder, localRequest)
	if localRecorder.Code != http.StatusFound {
		t.Fatalf("local GET /auth/google status = %d, want %d", localRecorder.Code, http.StatusFound)
	}
	if authentication.authorizationRedirectURL != "" {
		t.Fatalf("local OAuth redirect URL = %q, want configured redirect URL", authentication.authorizationRedirectURL)
	}
}

func TestAuthenticationUsesPublicRedirectForOAuthCallback(t *testing.T) {
	const wantRedirectURL = "https://redlaunch.example.com/auth/google/callback"
	authentication := &fakeAuthenticationService{
		enabled:          true,
		authorizationURL: "https://accounts.example.test/authorize",
		completeEmail:    "admin@example.com",
		sessionValue:     "signed-session",
	}
	applications := &fakeApplicationService{
		publicAccess: application.RedlaunchPublicAccess{
			Enabled: true,
			Domain:  "redlaunch.example.com",
		},
	}
	web, err := New(nil, applications, authentication)
	if err != nil {
		t.Fatal(err)
	}

	startRequest := httptest.NewRequest(http.MethodGet, "https://redlaunch.example.com/auth/google", nil)
	startRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(startRecorder, startRequest)
	startLocation, err := url.Parse(startRecorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var stateCookie, redirectCookie *http.Cookie
	for _, cookie := range startRecorder.Result().Cookies() {
		switch cookie.Name {
		case oauthStateCookieName:
			stateCookie = cookie
		case oauthRedirectCookieName:
			redirectCookie = cookie
		}
	}

	callbackRequest := httptest.NewRequest(http.MethodGet, "https://redlaunch.example.com/auth/google/callback?code=code&state="+url.QueryEscape(startLocation.Query().Get("state")), nil)
	callbackRequest.AddCookie(stateCookie)
	callbackRequest.AddCookie(redirectCookie)
	callbackRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(callbackRecorder, callbackRequest)
	if callbackRecorder.Code != http.StatusSeeOther {
		t.Fatalf("public GET /auth/google/callback status = %d, want %d", callbackRecorder.Code, http.StatusSeeOther)
	}
	if authentication.completeRedirectURL != wantRedirectURL {
		t.Fatalf("callback OAuth redirect URL = %q, want %q", authentication.completeRedirectURL, wantRedirectURL)
	}
}

func TestAuthenticationOAuthCallbackCreatesSessionAndPreservesTarget(t *testing.T) {
	authentication := &fakeAuthenticationService{
		enabled:          true,
		authorizationURL: "https://accounts.example.test/authorize",
		completeEmail:    "admin@example.com",
		sessionValue:     "signed-session",
	}
	web, err := New(nil, authentication)
	if err != nil {
		t.Fatal(err)
	}

	startRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(startRecorder, httptest.NewRequest(http.MethodGet, "/auth/google?next=%2Fapplications%3Ftab%3Ddomains", nil))
	if startRecorder.Code != http.StatusFound {
		t.Fatalf("GET /auth/google status = %d, want %d", startRecorder.Code, http.StatusFound)
	}
	startLocation, err := url.Parse(startRecorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := startLocation.Query().Get("state")
	if state == "" {
		t.Fatal("GET /auth/google did not include OAuth state")
	}
	var stateCookie, redirectCookie *http.Cookie
	for _, cookie := range startRecorder.Result().Cookies() {
		switch cookie.Name {
		case oauthStateCookieName:
			stateCookie = cookie
		case oauthRedirectCookieName:
			redirectCookie = cookie
		}
	}
	if stateCookie == nil || redirectCookie == nil {
		t.Fatalf("GET /auth/google cookies = %v, want state and redirect cookies", startRecorder.Result().Cookies())
	}

	callbackRequest := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=code&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(stateCookie)
	callbackRequest.AddCookie(redirectCookie)
	callbackRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(callbackRecorder, callbackRequest)
	if callbackRecorder.Code != http.StatusSeeOther {
		t.Fatalf("GET /auth/google/callback status = %d, want %d", callbackRecorder.Code, http.StatusSeeOther)
	}
	if got := callbackRecorder.Header().Get("Location"); got != "/applications?tab=domains" {
		t.Fatalf("callback Location = %q, want /applications?tab=domains", got)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range callbackRecorder.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || sessionCookie.Value != "signed-session" {
		t.Fatalf("callback session cookie = %v, want signed session", sessionCookie)
	}
	if authentication.completeCalls != 1 {
		t.Fatalf("CompleteLogin call count = %d, want 1", authentication.completeCalls)
	}

	authentication.validateValid = true
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(sessionCookie)
	protectedRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(protectedRecorder, request)
	if protectedRecorder.Code != http.StatusOK {
		t.Fatalf("authenticated GET / status = %d, want %d", protectedRecorder.Code, http.StatusOK)
	}
}

func TestAuthenticatedSidebarRendersProfileImageOrInitials(t *testing.T) {
	tests := []struct {
		name    string
		user    redlaunchauth.User
		want    []string
		notWant []string
	}{
		{
			name: "profile image",
			user: redlaunchauth.User{
				Email:      "ada@example.com",
				Name:       "Ada Lovelace",
				PictureURL: "https://lh3.googleusercontent.com/a/avatar",
			},
			want: []string{
				`class="sidebar-user-widget"`,
				`src="https://lh3.googleusercontent.com/a/avatar"`,
				`>Ada Lovelace</strong>`,
				`>ada@example.com</span>`,
				`aria-label="Log out"`,
				`action="/logout"`,
			},
			notWant: []string{`sidebar-user-avatar-initials`},
		},
		{
			name: "initials fallback",
			user: redlaunchauth.User{
				Email: "grace@example.com",
				Name:  "Grace Hopper",
			},
			want: []string{
				`class="sidebar-user-avatar sidebar-user-avatar-initials"`,
				">GH</span>",
				">Grace Hopper</strong>",
				">grace@example.com</span>",
			},
			notWant: []string{`<img class="sidebar-user-avatar"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authentication := &fakeAuthenticationService{
				enabled:       true,
				validateUser:  tt.user,
				validateValid: true,
			}
			web, err := New(nil, authentication)
			if err != nil {
				t.Fatal(err)
			}

			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "signed-session"})
			recorder := httptest.NewRecorder()
			web.Routes().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("authenticated GET / status = %d, want %d", recorder.Code, http.StatusOK)
			}
			body := recorder.Body.String()
			for _, expected := range tt.want {
				if !strings.Contains(body, expected) {
					t.Fatalf("authenticated sidebar did not render %q: %s", expected, body)
				}
			}
			for _, unexpected := range tt.notWant {
				if strings.Contains(body, unexpected) {
					t.Fatalf("authenticated sidebar unexpectedly rendered %q: %s", unexpected, body)
				}
			}
		})
	}
}

func TestLogoutExpiresSessionCookieAndRedirectsToLogin(t *testing.T) {
	authentication := &fakeAuthenticationService{
		enabled: true,
		validateUser: redlaunchauth.User{
			Email: "admin@example.com",
			Name:  "Admin User",
		},
		validateValid: true,
	}
	web, err := New(nil, authentication)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"csrf_token": {web.csrfToken}}
	request := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "signed-session"})
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /logout status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/login" {
		t.Fatalf("POST /logout Location = %q, want /login", got)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || sessionCookie.Value != "" || sessionCookie.MaxAge != -1 {
		t.Fatalf("POST /logout session cookie = %v, want an expired empty cookie", sessionCookie)
	}
}

func TestLogoutRejectsInvalidCSRFToken(t *testing.T) {
	authentication := &fakeAuthenticationService{
		enabled: true,
		validateUser: redlaunchauth.User{
			Email: "admin@example.com",
			Name:  "Admin User",
		},
		validateValid: true,
	}
	web, err := New(nil, authentication)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader("csrf_token=invalid"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "signed-session"})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST /logout with invalid CSRF status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			t.Fatalf("POST /logout with invalid CSRF expired session cookie: %v", cookie)
		}
	}
}

func TestAuthenticationRejectsOAuthStateMismatch(t *testing.T) {
	authentication := &fakeAuthenticationService{
		enabled:          true,
		authorizationURL: "https://accounts.example.test/authorize",
	}
	web, err := New(nil, authentication)
	if err != nil {
		t.Fatal(err)
	}
	startRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(startRecorder, httptest.NewRequest(http.MethodGet, "/auth/google", nil))
	var stateCookie *http.Cookie
	for _, cookie := range startRecorder.Result().Cookies() {
		if cookie.Name == oauthStateCookieName {
			stateCookie = cookie
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=code&state=wrong", nil)
	request.AddCookie(stateCookie)
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("state mismatch status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if authentication.completeCalls != 0 || !strings.Contains(recorder.Body.String(), "sign-in session expired") {
		t.Fatalf("state mismatch response = %s, complete calls = %d", recorder.Body.String(), authentication.completeCalls)
	}
}

func TestIndexRendersSetupScreenWhenSetupIsRequired(t *testing.T) {
	manager := &fakeSetupManager{needsSetup: true}
	web, err := New(nil, manager)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<h1 id="setup-title">Set up your server</h1>`,
		`name="proxy"`,
		`name="registry"`,
		`action="/setup"`,
		`name="csrf_token"`,
		`>Complete setup</button>`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET / did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `<p class="setup-kicker">`) {
		t.Fatalf("GET / rendered a duplicate setup heading: %s", body)
	}
}

func TestApplicationsRendersSetupScreenWhenSetupIsRequired(t *testing.T) {
	manager := &fakeSetupManager{needsSetup: true}
	web, err := New(nil, manager)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /applications status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), `<h1 id="setup-title">Set up your server</h1>`) {
		t.Fatalf("GET /applications did not render the setup screen: %s", recorder.Body.String())
	}
}

func TestSetupAcceptsSelectedCoreServices(t *testing.T) {
	manager := &fakeSetupManager{needsSetup: true, done: make(chan struct{})}
	web, err := New(nil, manager)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"proxy":      {"on"},
		"registry":   {"on"},
	}
	request := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /setup status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	select {
	case <-manager.done:
	case <-time.After(time.Second):
		t.Fatal("setup job did not finish")
	}
	if manager.setupCalls != 1 || !manager.installProxy || !manager.installRegistry {
		t.Fatalf("setup call = (%d, %t, %t), want (1, true, true)", manager.setupCalls, manager.installProxy, manager.installRegistry)
	}
}

func TestSetupStatusReportsFailureStageAndDetails(t *testing.T) {
	manager := &fakeSetupManager{
		needsSetup: true,
		err:        errors.New("docker compose failed: port 80 is already allocated"),
		done:       make(chan struct{}),
	}
	web, err := New(nil, manager)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"csrf_token": {web.csrfToken}, "proxy": {"on"}}
	request := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	postRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(postRecorder, request)
	if postRecorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /setup status = %d, want %d", postRecorder.Code, http.StatusSeeOther)
	}
	select {
	case <-manager.done:
	case <-time.After(time.Second):
		t.Fatal("setup job did not finish")
	}

	location, err := url.Parse(postRecorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	statusRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "/setup/status?id="+location.Query().Get("setup_job"), nil))

	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("GET /setup/status status = %d, want %d", statusRecorder.Code, http.StatusOK)
	}
	statusBody := statusRecorder.Body.String()
	for _, expected := range []string{"Setup stopped", "Start Caddy", "port 80 is already allocated", "Failed", `aria-label="Failed"`, `data-setup-close`, ">Close</button>"} {
		if !strings.Contains(statusBody, expected) {
			t.Fatalf("GET /setup/status did not render %q: %s", expected, statusBody)
		}
	}
	for _, unexpected := range []string{"Creating managed application and core directories", "Starting Caddy with Docker Compose", "setup-progress-step-detail", "setup-progress-step-state", "Open Redlaunch", "Reload setup status", "Return to setup", `<p class="setup-kicker">`} {
		if strings.Contains(statusBody, unexpected) {
			t.Fatalf("GET /setup/status rendered unexpected %q: %s", unexpected, statusBody)
		}
	}
}

func TestSetupJobUsesFixedWorkflowSteps(t *testing.T) {
	store := newSetupJobStore()
	job, err := store.create(true, true)
	if err != nil {
		t.Fatal(err)
	}

	progress := job.snapshot()
	wantLabels := []string{
		"Prepare directories",
		"Configure Caddy",
		"Start Caddy",
		"Configure Docker Registry",
		"Start Docker Registry",
		"Finalize setup",
	}
	if len(progress.Steps) != len(wantLabels) {
		t.Fatalf("setup steps = %d, want %d", len(progress.Steps), len(wantLabels))
	}
	for index, want := range wantLabels {
		if progress.Steps[index].Label != want {
			t.Errorf("setup step %d = %q, want %q", index, progress.Steps[index].Label, want)
		}
		if progress.Steps[index].State != setupStepRemaining {
			t.Errorf("setup step %d state = %q, want %q", index, progress.Steps[index].State, setupStepRemaining)
		}
	}
}

func TestSetupRejectsMissingCSRFToken(t *testing.T) {
	manager := &fakeSetupManager{needsSetup: true}
	web, err := New(nil, manager)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/setup", nil))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST /setup status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if manager.setupCalls != 0 {
		t.Fatalf("setup call count = %d, want 0", manager.setupCalls)
	}
}

func TestSetupRendersFreshFormForExpiredCSRFToken(t *testing.T) {
	manager := &fakeSetupManager{needsSetup: true}
	web, err := New(nil, manager)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"csrf_token": {"expired-token"}, "proxy": {"on"}}
	request := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST /setup status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	for _, expected := range []string{"This setup page expired", `<h1 id="setup-title">Set up your server</h1>`, `name="csrf_token"`} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("expired setup response did not render %q: %s", expected, recorder.Body.String())
		}
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if manager.setupCalls != 0 {
		t.Fatalf("setup call count = %d, want 0", manager.setupCalls)
	}
}

func TestSetupAcceptsCSRFTokenFromCookieAfterHandlerRestart(t *testing.T) {
	firstManager := &fakeSetupManager{needsSetup: true}
	first, err := New(nil, firstManager)
	if err != nil {
		t.Fatal(err)
	}
	getRecorder := httptest.NewRecorder()
	first.Routes().ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/", nil))
	cookies := getRecorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != csrfCookieName {
		t.Fatalf("setup cookies = %v, want one %q cookie", cookies, csrfCookieName)
	}

	secondManager := &fakeSetupManager{needsSetup: true, done: make(chan struct{})}
	second, err := New(nil, secondManager)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {cookies[0].Value}}
	request := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookies[0])
	recorder := httptest.NewRecorder()
	second.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /setup status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	select {
	case <-secondManager.done:
	case <-time.After(time.Second):
		t.Fatal("setup job did not finish")
	}
}

func TestManagedHTTPSUsesSecureCSRFCookieBehindConfiguredTLSProxy(t *testing.T) {
	manager := &fakeSetupManager{needsSetup: true}
	web, err := New(nil, manager, SecurityConfig{AccessMode: accessModeManagedHTTPS})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://manager.example/", nil)
	web.Routes().ServeHTTP(recorder, request)

	var csrfCookie *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == csrfCookieName {
			csrfCookie = cookie
		}
	}
	if csrfCookie == nil || !csrfCookie.Secure || csrfCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("managed HTTPS CSRF cookie = %#v, want Secure and SameSite=Strict", csrfCookie)
	}
}

func TestStateChangingRequestRejectsCrossOriginRequest(t *testing.T) {
	manager := &fakeSetupManager{needsSetup: true}
	web, err := New(nil, manager)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {web.csrfToken}}
	request := httptest.NewRequest(http.MethodPost, "http://manager.example/setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://evil.example")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if manager.setupCalls != 0 {
		t.Fatalf("cross-origin POST setup calls = %d, want 0", manager.setupCalls)
	}
}

func TestStateChangingRequestRejectsMalformedReferer(t *testing.T) {
	manager := &fakeSetupManager{needsSetup: true}
	web, err := New(nil, manager)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {web.csrfToken}}
	request := httptest.NewRequest(http.MethodPost, "http://manager.example/setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Referer", "not-an-origin")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("malformed Referer POST status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if manager.setupCalls != 0 {
		t.Fatalf("malformed Referer setup calls = %d, want 0", manager.setupCalls)
	}
}

func TestGitHubActionsPageShowsSetupForm(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 9, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication}},
	}
	githubActions := &fakeGitHubActionsService{}
	web, err := New(nil, applications, githubActions)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/deployments/github-actions", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET GitHub Actions page status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{"GitHub Actions deployment", "owner/repository", "Create SSH tunnel and workflow"} {
		if !strings.Contains(body, expected) {
			t.Errorf("GitHub Actions page does not contain %q: %s", expected, body)
		}
	}
}

func TestConfigureGitHubActionsPageReturnsAsyncHandoff(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 9, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication}},
	}
	githubActions := &fakeGitHubActionsService{
		configured: application.GitHubActionsSetup{
			Integration: application.GitHubActionsIntegration{ApplicationID: 7, Repository: "acme/status-page", Branch: "master", ServerHost: "203.0.113.10", ServerPort: 2222, SSHUsername: "redlaunch-deploy"},
			PrivateKey:  "private-key",
			KnownHosts:  "[203.0.113.10]:2222 ssh-ed25519 host-key",
			HostKey:     "ssh-ed25519 host-key",
			Workflow:    "name: generated",
		},
	}
	web, err := New(nil, applications, githubActions)
	if err != nil {
		t.Fatal(err)
	}
	getRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/deployments/github-actions", nil))
	csrfCookie := getRecorder.Result().Cookies()[0]
	form := url.Values{
		"csrf_token":    {csrfCookie.Value},
		"repository":    {"acme/status-page"},
		"branch":        {"master"},
		"dockerfile":    {"Dockerfile"},
		"build_context": {"."},
		"service_name":  {"web"},
		"image_name":    {"status-page/web"},
		"server_host":   {"203.0.113.10"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/deployments/github-actions", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(csrfCookie)
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST GitHub Actions status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	jobID := location.Query().Get("github_actions_job")
	if jobID == "" {
		t.Fatalf("POST GitHub Actions Location = %q, want setup job", location.String())
	}
	deadline := time.Now().Add(time.Second)
	var completed bool
	for time.Now().Before(deadline) {
		job := web.githubActionsJobs.get(7, jobID)
		if job != nil && job.snapshot().State != githubActionsJobStateRunning {
			completed = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !completed {
		t.Fatal("GitHub Actions setup job did not finish")
	}
	handoffLocation := location.Path + "?github_actions_job=" + url.QueryEscape(jobID) + "&github_actions_handoff=1"
	handoffRecorder := httptest.NewRecorder()
	handoffRequest := httptest.NewRequest(http.MethodGet, handoffLocation, nil)
	web.Routes().ServeHTTP(handoffRecorder, handoffRequest)
	if handoffRecorder.Code != http.StatusOK {
		t.Fatalf("GET GitHub Actions handoff status = %d, want %d", handoffRecorder.Code, http.StatusOK)
	}
	if !strings.Contains(handoffRecorder.Body.String(), "private-key") || !strings.Contains(handoffRecorder.Body.String(), "Finish setup in GitHub") {
		t.Fatalf("handoff page did not include expected async handoff content: %s", handoffRecorder.Body.String())
	}
	if githubActions.configureInput.Repository != "acme/status-page" {
		t.Fatalf("configure input = %#v", githubActions.configureInput)
	}
	if got := handoffRecorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestConfigureGitHubActionsDuplicateSubmissionUsesExistingJob(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 9, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication}},
	}
	githubActions := &fakeGitHubActionsService{
		configured:       application.GitHubActionsSetup{Integration: application.GitHubActionsIntegration{ApplicationID: 7}},
		configureStarted: make(chan struct{}, 1),
		configureRelease: make(chan struct{}),
	}
	web, err := New(nil, applications, githubActions)
	if err != nil {
		t.Fatal(err)
	}
	getRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/deployments/github-actions", nil))
	csrfCookie := getRecorder.Result().Cookies()[0]
	form := url.Values{
		"csrf_token": {csrfCookie.Value}, "repository": {"acme/status-page"}, "branch": {"master"},
		"dockerfile": {"Dockerfile"}, "build_context": {"."}, "service_name": {"web"},
		"image_name": {"status-page/web"}, "server_host": {"203.0.113.10"},
	}
	post := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/applications/7/deployments/github-actions", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(csrfCookie)
		recorder := httptest.NewRecorder()
		web.Routes().ServeHTTP(recorder, request)
		return recorder
	}
	first := post()
	select {
	case <-githubActions.configureStarted:
	case <-time.After(time.Second):
		t.Fatal("first GitHub Actions job did not start")
	}
	second := post()
	firstLocation, err := url.Parse(first.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	secondLocation, err := url.Parse(second.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Code != http.StatusSeeOther || second.Code != http.StatusSeeOther || firstLocation.Query().Get("github_actions_job") != secondLocation.Query().Get("github_actions_job") {
		t.Fatalf("duplicate setup redirects = (%d, %q) and (%d, %q), want the same job", first.Code, firstLocation.String(), second.Code, secondLocation.String())
	}
	progressRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(progressRecorder, httptest.NewRequest(http.MethodGet, firstLocation.String(), nil))
	if progressRecorder.Code != http.StatusOK {
		t.Fatalf("GET GitHub Actions progress status = %d, want %d", progressRecorder.Code, http.StatusOK)
	}
	compactProgressBody := strings.Join(strings.Fields(progressRecorder.Body.String()), " ")
	if !strings.Contains(compactProgressBody, "data-progress-close>Close</button> </div> </section> </div> </main>") {
		t.Fatalf("GitHub Actions progress dialog is not a sibling of the inert page content: %s", progressRecorder.Body.String())
	}
	close(githubActions.configureRelease)
	jobID := firstLocation.Query().Get("github_actions_job")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if job := web.githubActionsJobs.get(7, jobID); job != nil && job.snapshot().State != githubActionsJobStateRunning {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if githubActions.configureCalls != 1 {
		t.Fatalf("configure calls = %d, want one", githubActions.configureCalls)
	}
}

func TestHandlerShutdownCancelsGitHubActionsJobs(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 9, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication}},
	}
	githubActions := &fakeGitHubActionsService{
		configureStarted:             make(chan struct{}, 1),
		configureWaitForCancellation: true,
	}
	web, err := New(nil, applications, githubActions)
	if err != nil {
		t.Fatal(err)
	}
	getRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/deployments/github-actions", nil))
	csrfCookie := getRecorder.Result().Cookies()[0]
	form := url.Values{
		"csrf_token": {csrfCookie.Value}, "repository": {"acme/status-page"}, "branch": {"master"},
		"dockerfile": {"Dockerfile"}, "build_context": {"."}, "service_name": {"web"},
		"image_name": {"status-page/web"}, "server_host": {"203.0.113.10"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/deployments/github-actions", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(csrfCookie)
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	select {
	case <-githubActions.configureStarted:
	case <-time.After(time.Second):
		t.Fatal("GitHub Actions job did not start")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := web.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown handler: %v", err)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	job := web.githubActionsJobs.get(7, location.Query().Get("github_actions_job"))
	if job == nil || job.snapshot().State != githubActionsJobStateFailed {
		t.Fatalf("job after shutdown = %#v, want failed terminal state", job)
	}
}

func TestDownloadGitHubActionsWorkflow(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	githubActions := &fakeGitHubActionsService{workflow: "name: generated\n"}
	web, err := New(nil, applications, githubActions)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/deployments/github-actions/workflow", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "name: generated\n" {
		t.Fatalf("workflow response = (%d, %q)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Header().Get("Content-Disposition"), "redlaunch-push-image.yml") {
		t.Fatalf("Content-Disposition = %q", recorder.Header().Get("Content-Disposition"))
	}
}

func TestGitHubActionsConfiguredPageDoesNotRedisplayPrivateKey(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 9, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication}},
	}
	githubActions := &fakeGitHubActionsService{
		integration: application.GitHubActionsIntegration{
			ApplicationID: 7, Repository: "acme/status-page", Branch: "master", Dockerfile: "Dockerfile", BuildContext: ".", ServiceName: "web", ImageName: "status-page/web", ServerHost: "203.0.113.10", KeyFingerprint: "SHA256:public",
		},
	}
	web, err := New(nil, applications, githubActions)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/deployments/github-actions", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("configured GitHub Actions page status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{"Deployment configured", "Rotate or update configuration", "Revoke repository key", "SHA256:public"} {
		if !strings.Contains(body, expected) {
			t.Errorf("configured GitHub Actions page does not contain %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "private-key") {
		t.Fatal("configured GitHub Actions page redisplayed a private key")
	}
}

func TestRevokeGitHubActionsRequiresCSRFAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}}}
	githubActions := &fakeGitHubActionsService{integration: application.GitHubActionsIntegration{ApplicationID: 7, Repository: "acme/status-page"}}
	web, err := New(nil, applications, githubActions)
	if err != nil {
		t.Fatal(err)
	}
	getRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/deployments/github-actions", nil))
	cookies := getRecorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("GET GitHub Actions cookies = %d, want one", len(cookies))
	}
	form := url.Values{"csrf_token": {cookies[0].Value}}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/deployments/github-actions/revoke", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookies[0])
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("revoke response = (%d, %q), want redirect", recorder.Code, recorder.Header().Get("Location"))
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	jobID := location.Query().Get("github_actions_job")
	if location.Path != "/applications/7/deployments/github-actions" || jobID == "" {
		t.Fatalf("revoke Location = %q, want revoke progress job", location.String())
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if job := web.githubActionsJobs.get(7, jobID); job != nil && job.snapshot().State != githubActionsJobStateRunning {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if githubActions.revokeCalls != 1 {
		t.Fatalf("revoke calls = %d, want one", githubActions.revokeCalls)
	}
	statusRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/deployments/github-actions/status?id="+url.QueryEscape(jobID), nil))
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), "Repository key revoked") {
		t.Fatalf("revoke progress response = (%d, %s), want completed revocation", statusRecorder.Code, statusRecorder.Body.String())
	}
	if strings.Contains(statusRecorder.Body.String(), "Open GitHub handoff") {
		t.Fatal("completed revocation incorrectly offered a private-key handoff")
	}
}
