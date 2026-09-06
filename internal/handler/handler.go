// Package handler contains the server-rendered HTTP interface.
package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"redlaunch/internal/application"
	redlaunchauth "redlaunch/internal/auth"
	systemmetrics "redlaunch/internal/metrics"
)

//go:embed templates/*.html static/*
var embeddedFiles embed.FS

const csrfCookieName = "redlaunch_csrf"

const (
	oauthStateCookieName    = "redlaunch_oauth_state"
	oauthRedirectCookieName = "redlaunch_oauth_redirect"
	sessionCookieName       = "redlaunch_session"
	oauthCallbackPath       = "/auth/google/callback"
)

type authenticatedUserContextKey struct{}

// Handler serves the application pages.
type Handler struct {
	templates                      *template.Template
	logger                         *slog.Logger
	server                         ServerInfo
	setupManager                   setupManager
	applicationManager             applicationService
	applicationImporter            applicationComposeImporter
	applicationEnvironmentImporter applicationEnvironmentFileImporter
	applicationDetails             applicationDetailsService
	applicationDomains             applicationDomainService
	applicationRoutings            applicationRoutingService
	redlaunchPublicAccess          redlaunchPublicAccessService
	applicationEnvironment         applicationEnvironmentService
	applicationEditor              applicationEnvironmentEditor
	serviceDetails                 serviceDetailsService
	serviceActions                 serviceActionService
	serviceDeletion                serviceDeletionService
	applicationDeletion            applicationDeletionService
	backupManager                  serviceBackupService
	postgresManager                postgresqlService
	redisManager                   redisService
	applicationContainerManager    applicationContainerService
	proxyManager                   proxyDetailsService
	proxyActions                   proxyActionService
	dashboardMetrics               dashboardMetricsService
	authentication                 authenticationService
	setupJobs                      *setupJobStore
	postgresJobs                   *postgresJobStore
	redisJobs                      *redisServiceJobStore
	applicationContainerJobs       *applicationContainerJobStore
	serviceDeleteJobs              *serviceDeleteJobStore
	applicationDeleteJobs          *applicationDeleteJobStore
	csrfToken                      string
}

type setupManager interface {
	NeedsSetup() (bool, error)
	Setup(ctx context.Context, installProxy, installRegistry bool) error
}

type authenticationService interface {
	Enabled() bool
	AuthorizationURL(string) string
	CompleteLogin(context.Context, string) (redlaunchauth.User, error)
	NewSession(redlaunchauth.User) (string, error)
	ValidateSession(context.Context, string) (redlaunchauth.User, bool, error)
	CookieSecure() bool
	SessionDuration() time.Duration
}

type authenticationRedirectService interface {
	AuthorizationURLForRedirect(string, string) string
	CompleteLoginForRedirect(context.Context, string, string) (redlaunchauth.User, error)
}

type applicationService interface {
	List(context.Context) ([]application.Application, error)
	Create(context.Context, string, string) (application.Application, error)
}

type applicationComposeImporter interface {
	ImportDockerComposeProject(context.Context, int64, []byte) ([]application.Service, error)
}

type applicationEnvironmentFileImporter interface {
	ImportEnvironmentFiles(context.Context, int64, application.EnvironmentFileImportInput) error
}

type applicationDeletionService interface {
	DeleteApplication(context.Context, int64) error
}

type applicationDeletionProgressService interface {
	DeleteApplicationWithProgress(context.Context, int64, func(stage, message string)) error
}

type applicationDetailsService interface {
	Get(context.Context, int64) (application.Application, error)
	ListServices(context.Context, int64) ([]application.Service, error)
}

type applicationDomainService interface {
	ListDomains(context.Context, int64) ([]application.Domain, error)
	CreateDomain(context.Context, int64, string) (application.Domain, error)
	DeleteDomain(context.Context, int64, string) error
}

type applicationRoutingService interface {
	ListRoutings(context.Context, int64, int64) ([]application.Routing, error)
	CreateRouting(context.Context, int64, int64, application.RoutingInput) (application.Routing, error)
	UpdateRouting(context.Context, int64, int64, int64, application.RoutingInput) error
	DeleteRouting(context.Context, int64, int64, int64) error
}

type redlaunchPublicAccessService interface {
	GetRedlaunchPublicAccess(context.Context) (application.RedlaunchPublicAccess, error)
	UpdateRedlaunchPublicAccess(context.Context, application.RedlaunchPublicAccessInput) error
}

type applicationEnvironmentService interface {
	GetEnvironmentFiles(context.Context, int64) (application.EnvironmentFiles, error)
}

type applicationEnvironmentEditor interface {
	AddEnvironmentVariable(context.Context, int64, string, string) error
	DeleteEnvironmentVariable(context.Context, int64, string) error
	UpdateEnvironmentVariable(context.Context, int64, string, string, string) error
	AddEnvironmentSecret(context.Context, int64, string, string) error
	DeleteEnvironmentSecret(context.Context, int64, string) error
	UpdateEnvironmentSecret(context.Context, int64, string, string, string) error
}

type serviceDetailsService interface {
	GetServiceDetails(context.Context, int64, string) (application.ServiceDetails, error)
}

type serviceFullLogsService interface {
	GetServiceFullLogs(context.Context, int64, string) (string, error)
}

type serviceActionService interface {
	StartService(context.Context, int64, string) error
	StopService(context.Context, int64, string) error
	RestartService(context.Context, int64, string) error
}

type serviceDeletionService interface {
	DeleteService(context.Context, int64, string) error
}

type serviceBackupService interface {
	GetBackupDetails(context.Context, int64, string) (application.BackupDetails, error)
	UpdateBackupSchedule(context.Context, int64, string, application.BackupScheduleInput) error
	RunBackupNow(context.Context, int64, string) (application.Backup, error)
	RestoreBackup(context.Context, int64, string, string) error
	DeleteBackup(context.Context, int64, string, string) error
	OpenBackup(context.Context, int64, string, string) (io.ReadCloser, application.Backup, error)
}

type postgresqlService interface {
	CreatePostgreSQLService(context.Context, int64, application.PostgreSQLServiceInput) (application.Service, error)
}

type postgresqlInputValidator interface {
	ValidatePostgreSQLServiceInput(application.PostgreSQLServiceInput) error
}

type redisService interface {
	CreateRedisService(context.Context, int64, application.RedisServiceInput) (application.Service, error)
}

type redisInputValidator interface {
	ValidateRedisServiceInput(application.RedisServiceInput) error
}

type applicationContainerService interface {
	CreateApplicationService(context.Context, int64, application.ApplicationServiceInput) (application.Service, error)
}

type proxyDetailsService interface {
	GetProxyDetails(context.Context) (application.ProxyDetails, error)
}

type proxyActionService interface {
	StartProxy(context.Context) error
	StopProxy(context.Context) error
	RestartProxy(context.Context) error
}

type dashboardMetricsService interface {
	Collect(context.Context) (systemmetrics.Snapshot, error)
}

type proxyFullLogsService interface {
	GetProxyFullLogs(context.Context) (string, error)
}

type applicationContainerInputValidator interface {
	ValidateApplicationServiceInput(application.ApplicationServiceInput) error
}

// ServerInfo contains the local machine identity shown in the application shell.
type ServerInfo struct {
	Hostname  string
	IPAddress string
}

// New constructs the HTTP handler. Additional dependencies may provide the
// setup manager and application service used by the corresponding pages.
func New(logger *slog.Logger, dependencies ...any) (*Handler, error) {
	if logger == nil {
		logger = slog.Default()
	}
	templates, err := template.New("redlaunch").Funcs(template.FuncMap{
		"serviceTypeClass":      serviceTypeClass,
		"serviceTypeLabel":      serviceTypeLabel,
		"serviceStatusClass":    serviceStatusClass,
		"serviceIsRunning":      serviceIsRunning,
		"serviceIsStopped":      serviceIsStopped,
		"serviceCreatedAt":      serviceCreatedAtText,
		"serviceCreatedAtISO":   serviceCreatedAtISO,
		"serviceCreatedAtTitle": serviceCreatedAtTitle,
		"proxyCreatedAt":        proxyCreatedAtText,
		"proxyCreatedAtISO":     proxyCreatedAtISO,
		"proxyCreatedAtTitle":   proxyCreatedAtTitle,
		"serviceDetailsPath":    serviceDetailsPath,
		"backupDownloadPath":    backupDownloadPath,
		"backupTime":            backupTimeText,
		"backupTimeISO":         backupTimeISO,
		"backupAt":              backupAtText,
		"backupAtISO":           backupAtISO,
		"backupSize":            backupSizeText,
		"backupStatusClass":     backupStatusClass,
		"backupStatusIcon":      backupStatusIcon,
		"backupStatusText":      backupStatusText,
		"backupScheduleType":    backupScheduleTypeText,
		"backupWeekday":         backupWeekdayText,
		"serviceLogLines":       serviceLogLines,
		"proxyLogLines":         proxyLogLines,
		"routingHost":           routingHost,
		"environmentSensitive":  environmentSensitive,
		"dashboardPercent":      dashboardPercent,
		"dashboardSize":         dashboardSize,
		"dashboardTime":         dashboardTime,
		"dashboardTimeISO":      dashboardTimeISO,
	}).ParseFS(embeddedFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	csrfToken, err := newCSRFToken()
	if err != nil {
		return nil, fmt.Errorf("create CSRF token: %w", err)
	}
	manager := setupManager(noSetupManager{})
	applications := applicationService(noApplicationService{})
	var applicationImporter applicationComposeImporter
	var applicationEnvironmentImporter applicationEnvironmentFileImporter
	details := applicationDetailsService(noApplicationService{})
	domains := applicationDomainService(noApplicationService{})
	routings := applicationRoutingService(noApplicationService{})
	publicAccess := redlaunchPublicAccessService(noApplicationService{})
	environment := applicationEnvironmentService(noApplicationService{})
	editor := applicationEnvironmentEditor(noApplicationService{})
	var serviceDetails serviceDetailsService
	serviceActions := serviceActionService(noApplicationService{})
	serviceDeletion := serviceDeletionService(noApplicationService{})
	applicationDeletion := applicationDeletionService(noApplicationService{})
	var backupManager serviceBackupService
	postgres := postgresqlService(noApplicationService{})
	redis := redisService(noApplicationService{})
	applicationContainer := applicationContainerService(noApplicationService{})
	proxy := proxyDetailsService(noProxyService{})
	proxyActions := proxyActionService(noProxyService{})
	dashboardMetrics := dashboardMetricsService(systemmetrics.New())
	var authentication authenticationService
	for _, dependency := range dependencies {
		switch dependency := dependency.(type) {
		case nil:
			continue
		case setupManager:
			if dependency != nil {
				manager = dependency
			}
		case applicationService:
			if dependency != nil {
				applications = dependency
				if importer, ok := dependency.(applicationComposeImporter); ok {
					applicationImporter = importer
				}
				if importer, ok := dependency.(applicationEnvironmentFileImporter); ok {
					applicationEnvironmentImporter = importer
				}
				if proxyDependency, ok := dependency.(proxyDetailsService); ok {
					proxy = proxyDependency
				}
				if proxyActionDependency, ok := dependency.(proxyActionService); ok {
					proxyActions = proxyActionDependency
				}
				if applicationDeletionDependency, ok := dependency.(applicationDeletionService); ok {
					applicationDeletion = applicationDeletionDependency
				}
				if applicationContainerDependency, ok := dependency.(applicationContainerService); ok {
					applicationContainer = applicationContainerDependency
				}
				if detailsDependency, ok := dependency.(applicationDetailsService); ok {
					details = detailsDependency
				}
				if serviceDetailsDependency, ok := dependency.(serviceDetailsService); ok {
					serviceDetails = serviceDetailsDependency
				}
				if postgresDependency, ok := dependency.(postgresqlService); ok {
					postgres = postgresDependency
				}
				if redisDependency, ok := dependency.(redisService); ok {
					redis = redisDependency
				}
				if serviceActionsDependency, ok := dependency.(serviceActionService); ok {
					serviceActions = serviceActionsDependency
				}
				if serviceDeletionDependency, ok := dependency.(serviceDeletionService); ok {
					serviceDeletion = serviceDeletionDependency
				}
				if backupDependency, ok := dependency.(serviceBackupService); ok {
					backupManager = backupDependency
				}
				if environmentDependency, ok := dependency.(applicationEnvironmentService); ok {
					environment = environmentDependency
				}
				if editorDependency, ok := dependency.(applicationEnvironmentEditor); ok {
					editor = editorDependency
				}
				if domainsDependency, ok := dependency.(applicationDomainService); ok {
					domains = domainsDependency
				}
				if routingsDependency, ok := dependency.(applicationRoutingService); ok {
					routings = routingsDependency
				}
				if publicAccessDependency, ok := dependency.(redlaunchPublicAccessService); ok {
					publicAccess = publicAccessDependency
				}
			}
		case applicationDetailsService:
			if dependency != nil {
				details = dependency
				if importer, ok := dependency.(applicationComposeImporter); ok {
					applicationImporter = importer
				}
				if importer, ok := dependency.(applicationEnvironmentFileImporter); ok {
					applicationEnvironmentImporter = importer
				}
				if applicationDeletionDependency, ok := dependency.(applicationDeletionService); ok {
					applicationDeletion = applicationDeletionDependency
				}
				if serviceDetailsDependency, ok := dependency.(serviceDetailsService); ok {
					serviceDetails = serviceDetailsDependency
				}
				if serviceDeletionDependency, ok := dependency.(serviceDeletionService); ok {
					serviceDeletion = serviceDeletionDependency
				}
				if backupDependency, ok := dependency.(serviceBackupService); ok {
					backupManager = backupDependency
				}
				if environmentDependency, ok := dependency.(applicationEnvironmentService); ok {
					environment = environmentDependency
				}
				if editorDependency, ok := dependency.(applicationEnvironmentEditor); ok {
					editor = editorDependency
				}
				if domainsDependency, ok := dependency.(applicationDomainService); ok {
					domains = domainsDependency
				}
				if routingsDependency, ok := dependency.(applicationRoutingService); ok {
					routings = routingsDependency
				}
			}
		case applicationDomainService:
			if dependency != nil {
				domains = dependency
			}
		case applicationRoutingService:
			if dependency != nil {
				routings = dependency
			}
		case redlaunchPublicAccessService:
			if dependency != nil {
				publicAccess = dependency
			}
		case applicationEnvironmentService:
			if dependency != nil {
				environment = dependency
				if importer, ok := dependency.(applicationEnvironmentFileImporter); ok {
					applicationEnvironmentImporter = importer
				}
				if editorDependency, ok := dependency.(applicationEnvironmentEditor); ok {
					editor = editorDependency
				}
			}
		case applicationEnvironmentEditor:
			if dependency != nil {
				editor = dependency
				if importer, ok := dependency.(applicationEnvironmentFileImporter); ok {
					applicationEnvironmentImporter = importer
				}
			}
		case serviceDetailsService:
			if dependency != nil {
				serviceDetails = dependency
				if serviceDeletionDependency, ok := dependency.(serviceDeletionService); ok {
					serviceDeletion = serviceDeletionDependency
				}
			}
		case serviceActionService:
			if dependency != nil {
				serviceActions = dependency
				if serviceDeletionDependency, ok := dependency.(serviceDeletionService); ok {
					serviceDeletion = serviceDeletionDependency
				}
			}
		case serviceDeletionService:
			if dependency != nil {
				serviceDeletion = dependency
			}
		case applicationDeletionService:
			if dependency != nil {
				applicationDeletion = dependency
			}
		case applicationComposeImporter:
			if dependency != nil {
				applicationImporter = dependency
				if importer, ok := dependency.(applicationEnvironmentFileImporter); ok {
					applicationEnvironmentImporter = importer
				}
			}
		case applicationEnvironmentFileImporter:
			if dependency != nil {
				applicationEnvironmentImporter = dependency
			}
		case serviceBackupService:
			if dependency != nil {
				backupManager = dependency
			}
		case postgresqlService:
			if dependency != nil {
				postgres = dependency
			}
		case redisService:
			if dependency != nil {
				redis = dependency
			}
		case applicationContainerService:
			if dependency != nil {
				applicationContainer = dependency
			}
		case proxyDetailsService:
			if dependency != nil {
				proxy = dependency
				if proxyActionDependency, ok := dependency.(proxyActionService); ok {
					proxyActions = proxyActionDependency
				}
			}
		case proxyActionService:
			if dependency != nil {
				proxyActions = dependency
			}
		case dashboardMetricsService:
			if dependency != nil {
				dashboardMetrics = dependency
			}
		case authenticationService:
			if dependency != nil {
				authentication = dependency
			}
		default:
			return nil, fmt.Errorf("unsupported handler dependency %T", dependency)
		}
	}
	return &Handler{
		templates:                      templates,
		logger:                         logger,
		server:                         discoverServerInfo(),
		setupManager:                   manager,
		applicationManager:             applications,
		applicationImporter:            applicationImporter,
		applicationEnvironmentImporter: applicationEnvironmentImporter,
		applicationDetails:             details,
		applicationDomains:             domains,
		applicationRoutings:            routings,
		redlaunchPublicAccess:          publicAccess,
		applicationEnvironment:         environment,
		applicationEditor:              editor,
		serviceDetails:                 serviceDetails,
		serviceActions:                 serviceActions,
		serviceDeletion:                serviceDeletion,
		applicationDeletion:            applicationDeletion,
		backupManager:                  backupManager,
		postgresManager:                postgres,
		redisManager:                   redis,
		applicationContainerManager:    applicationContainer,
		proxyManager:                   proxy,
		proxyActions:                   proxyActions,
		dashboardMetrics:               dashboardMetrics,
		authentication:                 authentication,
		setupJobs:                      newSetupJobStore(),
		postgresJobs:                   newPostgresJobStore(),
		redisJobs:                      newRedisServiceJobStore(),
		applicationContainerJobs:       newApplicationContainerJobStore(),
		serviceDeleteJobs:              newServiceDeleteJobStore(),
		applicationDeleteJobs:          newApplicationDeleteJobStore(),
		csrfToken:                      csrfToken,
	}, nil
}

// Routes returns the complete application HTTP handler.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.index)
	mux.HandleFunc("GET /login", h.login)
	mux.HandleFunc("GET /auth/google", h.googleLogin)
	mux.HandleFunc("GET /auth/google/callback", h.googleCallback)
	mux.HandleFunc("POST /logout", h.logout)
	mux.HandleFunc("POST /setup", h.setup)
	mux.HandleFunc("GET /setup/status", h.setupStatus)
	mux.HandleFunc("GET /applications", h.applications)
	mux.HandleFunc("GET /dashboard", h.dashboard)
	mux.HandleFunc("GET /dashboard/metrics", h.dashboardMetricsFragment)
	mux.HandleFunc("GET /proxy", h.proxyPage)
	mux.HandleFunc("POST /proxy/start", h.startProxy)
	mux.HandleFunc("POST /proxy/stop", h.stopProxy)
	mux.HandleFunc("POST /proxy/restart", h.restartProxy)
	mux.HandleFunc("GET /proxy/logs/download", h.downloadProxyLogs)
	mux.HandleFunc("GET /applications/{id}", h.applicationDetailsPage)
	mux.HandleFunc("POST /applications/{id}/import", h.importDockerComposeProject)
	mux.HandleFunc("POST /applications/{id}/environment/import", h.importApplicationEnvironmentFiles)
	mux.HandleFunc("POST /applications/{id}/delete", h.deleteApplication)
	mux.HandleFunc("GET /applications/{id}/delete/status", h.applicationDeleteStatus)
	mux.HandleFunc("POST /applications/{id}/variables", h.updateApplicationVariable)
	mux.HandleFunc("POST /applications/{id}/variables/delete", h.deleteApplicationVariable)
	mux.HandleFunc("POST /applications/{id}/secrets", h.updateApplicationSecret)
	mux.HandleFunc("POST /applications/{id}/secrets/delete", h.deleteApplicationSecret)
	mux.HandleFunc("POST /applications/{id}/domains", h.createApplicationDomain)
	mux.HandleFunc("POST /applications/{id}/domains/delete", h.deleteApplicationDomain)
	mux.HandleFunc("GET /applications/{id}/domains/{domainID}/routing", h.applicationRoutingPage)
	mux.HandleFunc("POST /applications/{id}/domains/{domainID}/routing", h.saveApplicationRouting)
	mux.HandleFunc("POST /applications/{id}/domains/{domainID}/routing/delete", h.deleteApplicationRouting)
	mux.HandleFunc("POST /applications/{id}/settings/public-access", h.updateRedlaunchPublicAccess)
	mux.HandleFunc("GET /applications/{id}/services/{service}", h.serviceDetailsPage)
	mux.HandleFunc("POST /applications/{id}/services/{service}/start", h.startService)
	mux.HandleFunc("POST /applications/{id}/services/{service}/stop", h.stopService)
	mux.HandleFunc("POST /applications/{id}/services/{service}/restart", h.restartService)
	mux.HandleFunc("POST /applications/{id}/services/{service}/delete", h.deleteService)
	mux.HandleFunc("GET /applications/{id}/services/{service}/delete/status", h.serviceDeleteStatus)
	mux.HandleFunc("GET /applications/{id}/services/{service}/logs/download", h.downloadServiceLogs)
	mux.HandleFunc("POST /applications/{id}/services/{service}/backups/schedule", h.updateBackupSchedule)
	mux.HandleFunc("POST /applications/{id}/services/{service}/backups/run", h.runBackupNow)
	mux.HandleFunc("POST /applications/{id}/services/{service}/backups/restore", h.restoreBackup)
	mux.HandleFunc("POST /applications/{id}/services/{service}/backups/delete", h.deleteBackup)
	mux.HandleFunc("GET /applications/{id}/services/{service}/backups/download", h.downloadBackup)
	mux.HandleFunc("GET /applications/{id}/services/postgresql/new", h.postgreSQLServicePage)
	mux.HandleFunc("GET /applications/{id}/services/postgresql/status", h.postgreSQLServiceStatus)
	mux.HandleFunc("POST /applications/{id}/services/postgresql/new", h.createPostgreSQLService)
	mux.HandleFunc("GET /applications/{id}/services/redis/new", h.redisServicePage)
	mux.HandleFunc("GET /applications/{id}/services/redis/status", h.redisServiceStatus)
	mux.HandleFunc("POST /applications/{id}/services/redis/new", h.createRedisService)
	mux.HandleFunc("GET /applications/{id}/services/application/new", h.applicationContainerPage)
	mux.HandleFunc("GET /applications/{id}/services/application/status", h.applicationContainerStatus)
	mux.HandleFunc("POST /applications/{id}/services/application/new", h.createApplicationContainer)
	mux.HandleFunc("POST /applications", h.createApplication)
	mux.HandleFunc("GET /healthz", h.health)
	static, err := fs.Sub(embeddedFiles, "static")
	if err == nil {
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	} else {
		h.logger.Error("mount static files", "error", err)
	}
	return h.withSecurityHeaders(h.withAuthentication(mux))
}

func (h *Handler) withAuthentication(next http.Handler) http.Handler {
	if !h.authenticationEnabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicAuthenticationPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			h.redirectToLogin(w, r, r.URL.RequestURI())
			return
		}
		user, valid, err := h.authentication.ValidateSession(r.Context(), cookie.Value)
		if err != nil {
			h.logger.Error("validate authentication session", "error", err)
			http.Error(w, "The authentication state could not be checked.", http.StatusInternalServerError)
			return
		}
		if !valid {
			h.expireSessionCookie(w, r)
			h.redirectToLogin(w, r, r.URL.RequestURI())
			return
		}
		requestContext := context.WithValue(r.Context(), authenticatedUserContextKey{}, user)
		next.ServeHTTP(w, r.WithContext(requestContext))
	})
}

func isPublicAuthenticationPath(path string) bool {
	return path == "/login" ||
		path == "/auth/google" ||
		path == "/auth/google/callback" ||
		path == "/healthz" ||
		strings.HasPrefix(path, "/static/")
}

func (h *Handler) authenticationEnabled() bool {
	return h.authentication != nil && h.authentication.Enabled()
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	progress, ok := h.setupProgress(r)
	if !ok && r.URL.Query().Get("setup_job") != "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if needsSetup {
		h.writeSetupPage(w, r, http.StatusOK, setupPageData{}, progress)
		return
	}
	page := h.shellPageData(r)
	page.SetupProgress = progress
	h.writeTemplate(w, "index.html", page)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.authenticationEnabled() {
		http.NotFound(w, r)
		return
	}
	if _, valid, err := h.currentSessionUser(r); err != nil {
		h.logger.Error("check existing authentication session", "error", err)
		http.Error(w, "The authentication state could not be checked.", http.StatusInternalServerError)
		return
	} else if valid {
		http.Redirect(w, r, safeRedirectTarget(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}

	errorMessage := loginErrorMessage(r.URL.Query().Get("error"))
	target := safeRedirectTarget(r.URL.Query().Get("next"))
	h.writeLoginPage(w, http.StatusOK, loginPageData{
		Error:          errorMessage,
		GoogleLoginURL: "/auth/google?next=" + url.QueryEscape(target),
	})
}

func (h *Handler) googleLogin(w http.ResponseWriter, r *http.Request) {
	if !h.authenticationEnabled() {
		http.NotFound(w, r)
		return
	}
	redirectURL, err := h.oauthRedirectURL(r)
	if err != nil {
		h.logger.Error("resolve Google OAuth redirect URL", "error", err)
		http.Error(w, "Google sign-in could not be started.", http.StatusInternalServerError)
		return
	}
	state, err := newCSRFToken()
	if err != nil {
		h.logger.Error("create Google OAuth state", "error", err)
		http.Error(w, "Google sign-in could not be started.", http.StatusInternalServerError)
		return
	}
	target := safeRedirectTarget(r.URL.Query().Get("next"))
	secure := h.secureCookie(r)
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   10 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     oauthRedirectCookieName,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(target)),
		Path:     "/",
		MaxAge:   10 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
	http.Redirect(w, r, h.authorizationURL(state, redirectURL), http.StatusFound)
}

func (h *Handler) googleCallback(w http.ResponseWriter, r *http.Request) {
	if !h.authenticationEnabled() {
		http.NotFound(w, r)
		return
	}
	target := "/"
	if cookie, err := r.Cookie(oauthRedirectCookieName); err == nil {
		if decoded, decodeErr := base64.RawURLEncoding.DecodeString(cookie.Value); decodeErr == nil {
			target = safeRedirectTarget(string(decoded))
		}
	}
	h.expireOAuthCookies(w, r)

	stateCookie, err := r.Cookie(oauthStateCookieName)
	if err != nil || !validCSRFToken(r.URL.Query().Get("state"), stateCookie.Value) {
		h.writeLoginPage(w, http.StatusBadRequest, loginPageData{
			Error:          "The Google sign-in session expired. Start again.",
			GoogleLoginURL: "/auth/google?next=" + url.QueryEscape(target),
		})
		return
	}
	if r.URL.Query().Get("error") != "" {
		h.redirectToLoginWithError(w, r, target, "cancelled")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		h.writeLoginPage(w, http.StatusBadRequest, loginPageData{
			Error:          "Google did not return an authorization code. Start again.",
			GoogleLoginURL: "/auth/google?next=" + url.QueryEscape(target),
		})
		return
	}

	redirectURL, err := h.oauthRedirectURL(r)
	if err != nil {
		h.logger.Error("resolve Google OAuth redirect URL", "error", err)
		http.Error(w, "Google sign-in could not be completed.", http.StatusInternalServerError)
		return
	}
	user, err := h.completeLogin(r.Context(), code, redirectURL)
	if errors.Is(err, redlaunchauth.ErrNotAuthorized) || errors.Is(err, redlaunchauth.ErrEmailNotVerified) || errors.Is(err, application.ErrEmailInvalid) {
		h.redirectToLoginWithError(w, r, target, "unauthorized")
		return
	}
	if err != nil {
		h.logger.Error("complete Google login", "error", err)
		h.writeLoginPage(w, http.StatusBadGateway, loginPageData{
			Error:          "Google sign-in could not be completed. Try again.",
			GoogleLoginURL: "/auth/google?next=" + url.QueryEscape(target),
		})
		return
	}
	session, err := h.authentication.NewSession(user)
	if err != nil {
		h.logger.Error("create authentication session", "error", err)
		h.writeLoginPage(w, http.StatusInternalServerError, loginPageData{
			Error:          "Your sign-in could not be saved. Try again.",
			GoogleLoginURL: "/auth/google?next=" + url.QueryEscape(target),
		})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session,
		Path:     "/",
		MaxAge:   int(h.authentication.SessionDuration().Seconds()),
		Expires:  time.Now().Add(h.authentication.SessionDuration()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.secureCookie(r),
	})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (h *Handler) currentSessionUser(r *http.Request) (redlaunchauth.User, bool, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return redlaunchauth.User{}, false, nil
	}
	return h.authentication.ValidateSession(r.Context(), cookie.Value)
}

func (h *Handler) authorizationURL(state, redirectURL string) string {
	if authentication, ok := h.authentication.(authenticationRedirectService); ok {
		return authentication.AuthorizationURLForRedirect(state, redirectURL)
	}
	return h.authentication.AuthorizationURL(state)
}

func (h *Handler) completeLogin(ctx context.Context, code, redirectURL string) (redlaunchauth.User, error) {
	if authentication, ok := h.authentication.(authenticationRedirectService); ok {
		return authentication.CompleteLoginForRedirect(ctx, code, redirectURL)
	}
	return h.authentication.CompleteLogin(ctx, code)
}

func (h *Handler) oauthRedirectURL(r *http.Request) (string, error) {
	if _, ok := h.authentication.(authenticationRedirectService); !ok {
		return "", nil
	}
	publicAccess, err := h.redlaunchPublicAccess.GetRedlaunchPublicAccess(r.Context())
	if err != nil {
		return "", fmt.Errorf("read Redlaunch public access settings: %w", err)
	}
	if !publicAccess.Enabled {
		return "", nil
	}
	domain, err := application.ValidateDomainName(publicAccess.Domain)
	if err != nil {
		return "", fmt.Errorf("validate Redlaunch public access domain: %w", err)
	}
	if !requestUsesPublicHost(r, domain) {
		return "", nil
	}
	return (&url.URL{Scheme: "https", Host: domain, Path: oauthCallbackPath}).String(), nil
}

func requestUsesPublicHost(r *http.Request, expectedDomain string) bool {
	host := strings.TrimSpace(r.Host)
	if host == "" && r.URL != nil {
		host = strings.TrimSpace(r.URL.Host)
	}
	if host == "" {
		return false
	}
	parsed, err := url.Parse("//" + host)
	if err != nil || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return false
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	expectedDomain = strings.TrimSuffix(strings.ToLower(expectedDomain), ".")
	return hostname != "" && hostname == expectedDomain
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if !h.authenticationEnabled() {
		http.NotFound(w, r)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The logout request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This logout request expired. Refresh the page and try again.", http.StatusForbidden)
		return
	}

	h.expireSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *Handler) redirectToLogin(w http.ResponseWriter, r *http.Request, target string) {
	h.redirectToLoginWithError(w, r, target, "")
}

func (h *Handler) redirectToLoginWithError(w http.ResponseWriter, r *http.Request, target, errorCode string) {
	values := url.Values{"next": {safeRedirectTarget(target)}}
	if errorCode != "" {
		values.Set("error", errorCode)
	}
	http.Redirect(w, r, "/login?"+values.Encode(), http.StatusSeeOther)
}

func loginErrorMessage(code string) string {
	switch code {
	case "cancelled":
		return "Google sign-in was cancelled."
	case "unauthorized":
		return "This Google account is not authorized to use Redlaunch."
	case "oauth":
		return "Google sign-in could not be completed. Try again."
	default:
		return ""
	}
}

func safeRedirectTarget(value string) string {
	if strings.TrimSpace(value) == "" {
		return "/"
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, "\\") {
		return "/"
	}
	return parsed.RequestURI()
}

func (h *Handler) secureCookie(r *http.Request) bool {
	return r.TLS != nil || h.authentication.CookieSecure()
}

func (h *Handler) expireOAuthCookies(w http.ResponseWriter, r *http.Request) {
	secure := h.secureCookie(r)
	for _, name := range []string{oauthStateCookieName, oauthRedirectCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Expires:  time.Unix(1, 0),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   secure,
		})
	}
}

func (h *Handler) expireSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.secureCookie(r),
	})
}

func (h *Handler) setup(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if !needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The setup request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		h.writeSetupPage(w, r, http.StatusForbidden, setupPageData{
			Error:           "This setup page expired. Submit the refreshed form to continue.",
			InstallProxy:    r.Form.Get("proxy") == "on",
			InstallRegistry: r.Form.Get("registry") == "on",
		})
		return
	}

	setupData := setupPageData{
		InstallProxy:    r.Form.Get("proxy") == "on",
		InstallRegistry: r.Form.Get("registry") == "on",
	}
	job, err := h.setupJobs.create(setupData.InstallProxy, setupData.InstallRegistry)
	if err != nil {
		h.logger.Error("create setup job", "error", err)
		http.Error(w, "The setup job could not be created.", http.StatusInternalServerError)
		return
	}
	go h.runSetupJob(job, setupData.InstallProxy, setupData.InstallRegistry)

	location := "/?setup_job=" + url.QueryEscape(job.id)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) setupStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "The setup job ID is required.", http.StatusBadRequest)
		return
	}
	job := h.setupJobs.get(jobID)
	if job == nil {
		http.Error(w, "The setup job was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	progress := job.snapshot()
	h.writeTemplateStatus(w, "setup-progress.html", pageData{SetupProgress: &progress}, http.StatusOK)
}

func (h *Handler) applications(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		h.writeSetupPage(w, r, http.StatusOK, setupPageData{})
		return
	}
	items, err := h.applicationManager.List(r.Context())
	if err != nil {
		h.logger.Error("list applications", "error", err)
		http.Error(w, "The applications could not be read.", http.StatusInternalServerError)
		return
	}
	h.writeApplicationPage(w, r, http.StatusOK, applicationPageData{Applications: items})
}

func (h *Handler) applicationDetailsPage(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		h.writeSetupPage(w, r, http.StatusOK, setupPageData{})
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	postgresProgress, ok := h.postgreSQLProgress(id, r.URL.Query().Get("postgres_job"))
	if !ok {
		http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
		return
	}
	redisProgress, ok := h.redisProgress(id, r.URL.Query().Get("redis_job"))
	if !ok {
		http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
		return
	}
	applicationProgress, ok := h.applicationContainerProgress(id, r.URL.Query().Get("application_job"))
	if !ok {
		http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
		return
	}
	serviceDeleteProgress, ok := h.serviceDeleteProgress(id, r.URL.Query().Get("service_delete_job"))
	if !ok {
		http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
		return
	}
	applicationDeleteProgress, ok := h.applicationDeleteProgress(id, r.URL.Query().Get("application_delete_job"))
	if !ok {
		http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
		return
	}

	data, err := h.loadApplicationDetailsPageData(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		if applicationDeleteProgress != nil {
			h.writeApplicationDeleteProgressPage(w, r, http.StatusOK, applicationDeleteProgress)
			return
		}
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("get application details", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	data.ServiceDeleteProgress = serviceDeleteProgress
	data.ApplicationDeleteProgress = applicationDeleteProgress
	h.writeApplicationDetailsPage(w, r, http.StatusOK, data, postgresProgress, redisProgress, applicationProgress)
}

const maxComposeImportBody = application.MaxComposeFileSize + 256*1024

func (h *Handler) importDockerComposeProject(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	if h.applicationImporter == nil {
		h.logger.Error("import Docker Compose project without an importer", "application_id", id)
		http.Error(w, "The Docker Compose importer is not configured.", http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxComposeImportBody)
	parseErr := r.ParseMultipartForm(application.MaxComposeFileSize)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if parseErr != nil {
		h.renderApplicationImportError(w, r, id, "Choose a Docker Compose file no larger than 4 MB.", http.StatusBadRequest)
		return
	}

	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This application page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	file, _, err := r.FormFile("compose_file")
	if err != nil {
		h.renderApplicationImportError(w, r, id, "Choose a Docker Compose file.", http.StatusBadRequest)
		return
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, application.MaxComposeFileSize+1))
	_ = file.Close()
	if readErr != nil {
		h.renderApplicationImportError(w, r, id, "The Docker Compose file could not be read.", http.StatusBadRequest)
		return
	}
	if len(contents) > application.MaxComposeFileSize {
		h.renderApplicationImportError(w, r, id, "Compose files must be 4 MB or smaller.", http.StatusBadRequest)
		return
	}

	_, err = h.applicationImporter.ImportDockerComposeProject(r.Context(), id, contents)
	if err != nil {
		status := http.StatusInternalServerError
		if composeImportUserError(err) {
			status = http.StatusBadRequest
		}
		h.renderApplicationImportError(w, r, id, composeImportUserMessage(err), status)
		return
	}
	http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

const maxEnvironmentImportBody = 2*application.MaxEnvironmentFileSize + 256*1024

func (h *Handler) importApplicationEnvironmentFiles(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	if h.applicationEnvironmentImporter == nil {
		h.logger.Error("import application environment files without an importer", "application_id", id)
		http.Error(w, "The environment file importer is not configured.", http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxEnvironmentImportBody)
	parseErr := r.ParseMultipartForm(application.MaxEnvironmentFileSize)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if parseErr != nil {
		h.renderApplicationEnvironmentImportError(w, r, id, "Choose an environment file no larger than 1 MB.", environmentImportKind(r.Form.Get("environment_import_kind")), http.StatusBadRequest)
		return
	}

	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This application page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	importKind := environmentImportKind(r.Form.Get("environment_import_kind"))

	variables, variablesProvided, readErr := readUploadedEnvironmentFile(r, "variables_file")
	if readErr != nil {
		h.renderApplicationEnvironmentImportError(w, r, id, environmentImportMessage(readErr), importKind, http.StatusBadRequest)
		return
	}
	secrets, secretsProvided, readErr := readUploadedEnvironmentFile(r, "secrets_file")
	if readErr != nil {
		h.renderApplicationEnvironmentImportError(w, r, id, environmentImportMessage(readErr), importKind, http.StatusBadRequest)
		return
	}
	if !variablesProvided && !secretsProvided {
		h.renderApplicationEnvironmentImportError(w, r, id, environmentImportMessage(application.ErrEnvironmentImportFileRequired), importKind, http.StatusBadRequest)
		return
	}

	input := application.EnvironmentFileImportInput{
		Variables:         variables,
		VariablesProvided: variablesProvided,
		Secrets:           secrets,
		SecretsProvided:   secretsProvided,
	}
	if err := h.applicationEnvironmentImporter.ImportEnvironmentFiles(r.Context(), id, input); err != nil {
		status := http.StatusInternalServerError
		if environmentImportUserError(err) {
			status = http.StatusBadRequest
		} else {
			h.logger.Error("import application environment files", "application_id", id, "error", err)
		}
		h.renderApplicationEnvironmentImportError(w, r, id, environmentImportMessage(err), importKind, status)
		return
	}

	tab := "variables"
	if !variablesProvided {
		tab = "secrets"
	}
	http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10)+"?tab="+tab, http.StatusSeeOther)
}

func readUploadedEnvironmentFile(r *http.Request, fieldName string) ([]byte, bool, error) {
	file, _, err := r.FormFile(fieldName)
	if errors.Is(err, http.ErrMissingFile) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	contents, err := io.ReadAll(io.LimitReader(file, application.MaxEnvironmentFileSize+1))
	if err != nil {
		return nil, true, err
	}
	if len(contents) > application.MaxEnvironmentFileSize {
		return nil, true, application.ErrEnvironmentImportFileTooLarge
	}
	return contents, true, nil
}

func (h *Handler) loadApplicationDetailsPageData(ctx context.Context, id int64) (applicationDetailsPageData, error) {
	item, err := h.applicationDetails.Get(ctx, id)
	if err != nil {
		return applicationDetailsPageData{}, err
	}
	services, err := h.applicationDetails.ListServices(ctx, id)
	if err != nil {
		return applicationDetailsPageData{}, fmt.Errorf("list application services: %w", err)
	}

	var domains []application.Domain
	if h.applicationDomains != nil {
		domains, err = h.applicationDomains.ListDomains(ctx, id)
		if err != nil {
			return applicationDetailsPageData{}, fmt.Errorf("list application domains: %w", err)
		}
	}

	environmentFiles := application.EnvironmentFiles{}
	if h.applicationEnvironment != nil {
		environmentFiles, err = h.applicationEnvironment.GetEnvironmentFiles(ctx, id)
		if err != nil {
			h.logger.Error("read application environment files", "application_id", id, "error", err)
			environmentFiles = application.EnvironmentFiles{}
		}
	}
	publicAccess, err := h.redlaunchPublicAccess.GetRedlaunchPublicAccess(ctx)
	if err != nil {
		return applicationDetailsPageData{}, fmt.Errorf("read Redlaunch public access settings: %w", err)
	}
	return applicationDetailsPageData{
		Application:  item,
		Services:     services,
		Domains:      domains,
		PublicAccess: publicAccess,
		Variables: environmentFilePageData{
			ID:              "variables",
			Title:           "Variables",
			Description:     "Non-secret values from vars.env.",
			ApplicationName: item.Name,
			Entries:         environmentFiles.Variables,
			Available:       environmentFiles.VariablesAvailable,
			Editable:        true,
		},
		Secrets: environmentFilePageData{
			ID:              "secrets",
			Title:           "Secrets",
			Description:     "Sensitive values from secrets.env are masked.",
			ApplicationName: item.Name,
			Entries:         environmentFiles.Secrets,
			Available:       environmentFiles.SecretsAvailable,
			Editable:        true,
			MaskValues:      true,
		},
	}, nil
}

func (h *Handler) updateRedlaunchPublicAccess(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	if _, err := h.applicationDetails.Get(r.Context(), id); errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		h.logger.Error("get application before public access update", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The public access request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This settings page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	input := application.RedlaunchPublicAccessInput{
		Enabled: r.Form.Has("enabled"),
		Domain:  r.Form.Get("domain"),
	}
	if err := h.redlaunchPublicAccess.UpdateRedlaunchPublicAccess(r.Context(), input); err != nil {
		status := http.StatusInternalServerError
		message := "The public access settings could not be saved right now."
		if redlaunchPublicAccessUserError(err) {
			status = http.StatusBadRequest
			message = redlaunchPublicAccessMessage(err)
		} else {
			h.logger.Error("update Redlaunch public access", "application_id", id, "error", err)
		}
		h.renderRedlaunchPublicAccessError(w, r, id, input, message, status)
		return
	}
	http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10)+"?tab=settings", http.StatusSeeOther)
}

func (h *Handler) renderRedlaunchPublicAccessError(w http.ResponseWriter, r *http.Request, id int64, input application.RedlaunchPublicAccessInput, message string, status int) {
	data, err := h.loadApplicationDetailsPageData(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application details after public access failure", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	data.PublicAccess = application.RedlaunchPublicAccess{Enabled: input.Enabled, Domain: strings.TrimSpace(input.Domain)}
	data.PublicAccessError = message
	h.writeApplicationDetailsPage(w, r, status, data, nil, nil)
}

func redlaunchPublicAccessUserError(err error) bool {
	return errors.Is(err, application.ErrRedlaunchPublicDomainRequired) ||
		errors.Is(err, application.ErrRedlaunchPublicDomainTooLong) ||
		errors.Is(err, application.ErrRedlaunchPublicDomainInvalid)
}

func redlaunchPublicAccessMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrRedlaunchPublicDomainRequired):
		return "Enter a domain before enabling public access."
	case errors.Is(err, application.ErrRedlaunchPublicDomainTooLong):
		return "The domain name is too long."
	case errors.Is(err, application.ErrRedlaunchPublicDomainInvalid):
		return "Enter a valid domain name."
	default:
		return "The public access settings could not be saved."
	}
}

func (h *Handler) updateApplicationVariable(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The variable save request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This variables page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	edit := &variableEditPageData{
		Open:         true,
		Add:          r.Form.Get("operation") == "add",
		OriginalName: r.Form.Get("original_name"),
		Name:         r.Form.Get("name"),
		Value:        r.Form.Get("value"),
	}
	if h.applicationEditor == nil {
		h.logger.Error("update application variable without an editor", "application_id", id)
		edit.Error = "The variable could not be saved right now."
		h.renderApplicationVariableEditError(w, r, id, edit, http.StatusInternalServerError)
		return
	}
	var updateErr error
	if edit.Add {
		updateErr = h.applicationEditor.AddEnvironmentVariable(r.Context(), id, edit.Name, edit.Value)
	} else {
		updateErr = h.applicationEditor.UpdateEnvironmentVariable(r.Context(), id, edit.OriginalName, edit.Name, edit.Value)
	}
	if updateErr != nil {
		if environmentVariableUpdateUserError(updateErr) {
			edit.Error = environmentVariableUpdateMessage(updateErr)
			h.renderApplicationVariableEditError(w, r, id, edit, http.StatusBadRequest)
			return
		}
		h.logger.Error("save application variable", "application_id", id, "error", updateErr)
		edit.Error = "The variable could not be saved right now."
		h.renderApplicationVariableEditError(w, r, id, edit, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10)+"?tab=variables", http.StatusSeeOther)
}

func (h *Handler) deleteApplicationVariable(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The variable delete request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This variables page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	name := r.Form.Get("name")
	if h.applicationEditor == nil {
		h.logger.Error("delete application variable without an editor", "application_id", id)
		h.renderApplicationVariableDeleteError(w, r, id, &variableDeletePageData{
			Open:  true,
			Name:  name,
			Error: "The variable could not be deleted right now.",
		}, http.StatusInternalServerError)
		return
	}
	if err := h.applicationEditor.DeleteEnvironmentVariable(r.Context(), id, name); err != nil {
		if environmentVariableUpdateUserError(err) {
			h.renderApplicationVariableDeleteError(w, r, id, &variableDeletePageData{
				Open:  true,
				Name:  name,
				Error: environmentVariableDeleteMessage(err),
			}, http.StatusBadRequest)
			return
		}
		h.logger.Error("delete application variable", "application_id", id, "error", err)
		h.renderApplicationVariableDeleteError(w, r, id, &variableDeletePageData{
			Open:  true,
			Name:  name,
			Error: "The variable could not be deleted right now.",
		}, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10)+"?tab=variables", http.StatusSeeOther)
}

func (h *Handler) updateApplicationSecret(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The secret save request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This secrets page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	edit := &secretEditPageData{
		Open:         true,
		Add:          r.Form.Get("operation") == "add",
		OriginalName: r.Form.Get("original_name"),
		Name:         r.Form.Get("name"),
		Value:        r.Form.Get("value"),
	}
	if h.applicationEditor == nil {
		h.logger.Error("update application secret without an editor", "application_id", id)
		edit.Error = "The secret could not be saved right now."
		h.renderApplicationSecretEditError(w, r, id, edit, http.StatusInternalServerError)
		return
	}
	var updateErr error
	if edit.Add {
		updateErr = h.applicationEditor.AddEnvironmentSecret(r.Context(), id, edit.Name, edit.Value)
	} else {
		updateErr = h.applicationEditor.UpdateEnvironmentSecret(r.Context(), id, edit.OriginalName, edit.Name, edit.Value)
	}
	if updateErr != nil {
		if environmentVariableUpdateUserError(updateErr) {
			edit.Error = environmentSecretUpdateMessage(updateErr)
			h.renderApplicationSecretEditError(w, r, id, edit, http.StatusBadRequest)
			return
		}
		h.logger.Error("save application secret", "application_id", id, "error", updateErr)
		edit.Error = "The secret could not be saved right now."
		h.renderApplicationSecretEditError(w, r, id, edit, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10)+"?tab=secrets", http.StatusSeeOther)
}

func (h *Handler) deleteApplicationSecret(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The secret delete request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This secrets page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	name := r.Form.Get("name")
	if h.applicationEditor == nil {
		h.logger.Error("delete application secret without an editor", "application_id", id)
		h.renderApplicationSecretDeleteError(w, r, id, &secretDeletePageData{
			Open:  true,
			Name:  name,
			Error: "The secret could not be deleted right now.",
		}, http.StatusInternalServerError)
		return
	}
	if err := h.applicationEditor.DeleteEnvironmentSecret(r.Context(), id, name); err != nil {
		if environmentVariableUpdateUserError(err) {
			h.renderApplicationSecretDeleteError(w, r, id, &secretDeletePageData{
				Open:  true,
				Name:  name,
				Error: environmentSecretDeleteMessage(err),
			}, http.StatusBadRequest)
			return
		}
		h.logger.Error("delete application secret", "application_id", id, "error", err)
		h.renderApplicationSecretDeleteError(w, r, id, &secretDeletePageData{
			Open:  true,
			Name:  name,
			Error: "The secret could not be deleted right now.",
		}, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10)+"?tab=secrets", http.StatusSeeOther)
}

func (h *Handler) createApplicationDomain(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The domain save request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This domains page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	edit := &domainEditPageData{
		Open: true,
		Name: r.Form.Get("name"),
	}
	if h.applicationDomains == nil {
		h.logger.Error("create application domain without a domain service", "application_id", id)
		edit.Error = "The domain could not be saved right now."
		h.renderApplicationDomainEditError(w, r, id, edit, http.StatusInternalServerError)
		return
	}
	if _, err := h.applicationDomains.CreateDomain(r.Context(), id, edit.Name); err != nil {
		if domainCreateUserError(err) {
			edit.Error = domainCreateMessage(err)
			h.renderApplicationDomainEditError(w, r, id, edit, http.StatusBadRequest)
			return
		}
		h.logger.Error("create application domain", "application_id", id, "error", err)
		edit.Error = "The domain could not be saved right now."
		h.renderApplicationDomainEditError(w, r, id, edit, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10)+"?tab=domains", http.StatusSeeOther)
}

func (h *Handler) deleteApplicationDomain(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The domain delete request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This domains page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	name := r.Form.Get("name")
	if h.applicationDomains == nil {
		h.logger.Error("delete application domain without a domain service", "application_id", id)
		h.renderApplicationDomainDeleteError(w, r, id, &domainDeletePageData{
			Open:  true,
			Name:  name,
			Error: "The domain could not be deleted right now.",
		}, http.StatusInternalServerError)
		return
	}
	if err := h.applicationDomains.DeleteDomain(r.Context(), id, name); err != nil {
		if domainDeleteUserError(err) {
			h.renderApplicationDomainDeleteError(w, r, id, &domainDeletePageData{
				Open:  true,
				Name:  name,
				Error: domainDeleteMessage(err),
			}, http.StatusBadRequest)
			return
		}
		h.logger.Error("delete application domain", "application_id", id, "error", err)
		h.renderApplicationDomainDeleteError(w, r, id, &domainDeletePageData{
			Open:  true,
			Name:  name,
			Error: "The domain could not be deleted right now.",
		}, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10)+"?tab=domains", http.StatusSeeOther)
}

func (h *Handler) renderApplicationDomainEditError(w http.ResponseWriter, r *http.Request, id int64, edit *domainEditPageData, status int) {
	data, err := h.loadApplicationDetailsPageData(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application details after domain create failure", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	data.DomainEdit = edit
	h.writeApplicationDetailsPage(w, r, status, data, nil, nil)
}

func (h *Handler) renderApplicationImportError(w http.ResponseWriter, r *http.Request, id int64, message string, status int) {
	data, err := h.loadApplicationDetailsPageData(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application details after Compose import failure", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	data.ImportError = message
	h.writeApplicationDetailsPage(w, r, status, data, nil, nil)
}

func (h *Handler) renderApplicationEnvironmentImportError(w http.ResponseWriter, r *http.Request, id int64, message, importKind string, status int) {
	data, err := h.loadApplicationDetailsPageData(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application details after environment file import failure", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	data.EnvironmentImportError = message
	data.EnvironmentImportKind = importKind
	h.writeApplicationDetailsPage(w, r, status, data, nil, nil)
}

func (h *Handler) renderApplicationDomainDeleteError(w http.ResponseWriter, r *http.Request, id int64, deleteData *domainDeletePageData, status int) {
	data, err := h.loadApplicationDetailsPageData(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application details after domain delete failure", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	data.DomainDelete = deleteData
	h.writeApplicationDetailsPage(w, r, status, data, nil, nil)
}

func domainCreateUserError(err error) bool {
	return errors.Is(err, application.ErrDomainNameRequired) ||
		errors.Is(err, application.ErrDomainNameTooLong) ||
		errors.Is(err, application.ErrDomainNameInvalid) ||
		errors.Is(err, application.ErrDomainAlreadyExists)
}

func domainCreateMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrDomainNameRequired):
		return "Enter a domain name."
	case errors.Is(err, application.ErrDomainNameTooLong):
		return "The domain name is too long."
	case errors.Is(err, application.ErrDomainNameInvalid):
		return "Enter a valid domain name."
	case errors.Is(err, application.ErrDomainAlreadyExists):
		return "This domain is already associated with the application."
	default:
		return "The domain could not be saved."
	}
}

func domainDeleteUserError(err error) bool {
	return errors.Is(err, application.ErrDomainNameRequired) ||
		errors.Is(err, application.ErrDomainNameTooLong) ||
		errors.Is(err, application.ErrDomainNameInvalid) ||
		errors.Is(err, application.ErrDomainNotFound)
}

func domainDeleteMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrDomainNameRequired):
		return "The domain name is required."
	case errors.Is(err, application.ErrDomainNameTooLong), errors.Is(err, application.ErrDomainNameInvalid):
		return "The domain name is invalid."
	case errors.Is(err, application.ErrDomainNotFound):
		return "The domain could not be found. Refresh the page and try again."
	default:
		return "The domain could not be deleted."
	}
}

func (h *Handler) renderApplicationVariableEditError(w http.ResponseWriter, r *http.Request, id int64, edit *variableEditPageData, status int) {
	data, err := h.loadApplicationDetailsPageData(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application details after variable update failure", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	data.VariableEdit = edit
	h.writeApplicationDetailsPage(w, r, status, data, nil, nil)
}

func (h *Handler) renderApplicationSecretEditError(w http.ResponseWriter, r *http.Request, id int64, edit *secretEditPageData, status int) {
	data, err := h.loadApplicationDetailsPageData(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application details after secret update failure", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	data.SecretEdit = edit
	h.writeApplicationDetailsPage(w, r, status, data, nil, nil)
}

func (h *Handler) renderApplicationVariableDeleteError(w http.ResponseWriter, r *http.Request, id int64, deleteData *variableDeletePageData, status int) {
	data, err := h.loadApplicationDetailsPageData(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application details after variable delete failure", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	data.VariableDelete = deleteData
	h.writeApplicationDetailsPage(w, r, status, data, nil, nil)
}

func (h *Handler) renderApplicationSecretDeleteError(w http.ResponseWriter, r *http.Request, id int64, deleteData *secretDeletePageData, status int) {
	data, err := h.loadApplicationDetailsPageData(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application details after secret delete failure", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	data.SecretDelete = deleteData
	h.writeApplicationDetailsPage(w, r, status, data, nil, nil)
}

func environmentVariableUpdateUserError(err error) bool {
	return errors.Is(err, application.ErrEnvironmentVariableNameRequired) ||
		errors.Is(err, application.ErrEnvironmentVariableNameInvalid) ||
		errors.Is(err, application.ErrEnvironmentVariableValueInvalid) ||
		errors.Is(err, application.ErrEnvironmentVariableNotFound) ||
		errors.Is(err, application.ErrEnvironmentVariableAlreadyExists) ||
		errors.Is(err, application.ErrEnvironmentVariableDuplicate) ||
		errors.Is(err, application.ErrEnvironmentFileNotFound)
}

func environmentVariableUpdateMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrEnvironmentVariableNameRequired):
		return "Enter a variable name."
	case errors.Is(err, application.ErrEnvironmentVariableNameInvalid):
		return "Variable names may contain only letters, numbers, and underscores, and must start with a letter or underscore."
	case errors.Is(err, application.ErrEnvironmentVariableValueInvalid):
		return "Variable values cannot contain control characters."
	case errors.Is(err, application.ErrEnvironmentVariableAlreadyExists):
		return "A variable with that name already exists."
	case errors.Is(err, application.ErrEnvironmentVariableDuplicate):
		return "The variable appears more than once in vars.env."
	case errors.Is(err, application.ErrEnvironmentFileNotFound):
		return "The vars.env file is unavailable."
	case errors.Is(err, application.ErrEnvironmentVariableNotFound):
		return "The variable could not be found. Refresh the page and try again."
	default:
		return "The variable could not be saved."
	}
}

func environmentVariableDeleteMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrEnvironmentVariableNameRequired):
		return "The variable name is required."
	case errors.Is(err, application.ErrEnvironmentVariableNameInvalid):
		return "The variable name is invalid."
	case errors.Is(err, application.ErrEnvironmentVariableNotFound):
		return "The variable could not be found. Refresh the page and try again."
	case errors.Is(err, application.ErrEnvironmentFileNotFound):
		return "The vars.env file is unavailable."
	case errors.Is(err, application.ErrEnvironmentVariableDuplicate):
		return "The variable appears more than once in vars.env."
	default:
		return "The variable could not be deleted."
	}
}

func environmentSecretUpdateMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrEnvironmentVariableNameRequired):
		return "Enter a secret name."
	case errors.Is(err, application.ErrEnvironmentVariableNameInvalid):
		return "Secret names may contain only letters, numbers, and underscores, and must start with a letter or underscore."
	case errors.Is(err, application.ErrEnvironmentVariableValueInvalid):
		return "Secret values cannot contain control characters."
	case errors.Is(err, application.ErrEnvironmentVariableAlreadyExists):
		return "A secret with that name already exists."
	case errors.Is(err, application.ErrEnvironmentVariableDuplicate):
		return "The secret appears more than once in secrets.env."
	case errors.Is(err, application.ErrEnvironmentFileNotFound):
		return "The secrets.env file is unavailable."
	case errors.Is(err, application.ErrEnvironmentVariableNotFound):
		return "The secret could not be found. Refresh the page and try again."
	default:
		return "The secret could not be saved."
	}
}

func environmentSecretDeleteMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrEnvironmentVariableNameRequired):
		return "The secret name is required."
	case errors.Is(err, application.ErrEnvironmentVariableNameInvalid):
		return "The secret name is invalid."
	case errors.Is(err, application.ErrEnvironmentVariableNotFound):
		return "The secret could not be found. Refresh the page and try again."
	case errors.Is(err, application.ErrEnvironmentFileNotFound):
		return "The secrets.env file is unavailable."
	case errors.Is(err, application.ErrEnvironmentVariableDuplicate):
		return "The secret appears more than once in secrets.env."
	default:
		return "The secret could not be deleted."
	}
}

func (h *Handler) serviceDetailsPage(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		h.writeSetupPage(w, r, http.StatusOK, setupPageData{})
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	serviceName := r.PathValue("service")
	validatedServiceName, err := application.ValidateServiceName(serviceName)
	if err != nil || validatedServiceName != serviceName {
		http.NotFound(w, r)
		return
	}

	item, err := h.applicationDetails.Get(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("get application for service details", "id", id, "error", err)
		http.Error(w, "The application could not be read.", http.StatusInternalServerError)
		return
	}

	details, err := h.getServiceDetails(r.Context(), id, serviceName)
	if errors.Is(err, application.ErrNotFound) || errors.Is(err, application.ErrServiceNotFound) || errors.Is(err, application.ErrServiceNameRequired) || errors.Is(err, application.ErrServiceNameTooLong) || errors.Is(err, application.ErrServiceNameInvalid) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("get service details", "application_id", id, "service", serviceName, "error", err)
		http.Error(w, "The service details could not be read.", http.StatusInternalServerError)
		return
	}
	if h.backupManager != nil && application.IsDatabaseServiceType(details.Service.Type) {
		backupDetails, backupErr := h.backupManager.GetBackupDetails(r.Context(), id, serviceName)
		if backupErr != nil {
			h.logger.Error("get service backup details", "application_id", id, "service", serviceName, "error", backupErr)
		} else {
			details.Backup = &backupDetails
		}
	}

	h.writeServiceDetailsPage(w, r, http.StatusOK, serviceDetailsPageData{
		Application: item,
		Details:     details,
	})
}

func (h *Handler) downloadServiceLogs(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	logsService, ok := h.serviceDetails.(serviceFullLogsService)
	if !ok {
		http.Error(w, "Service log downloads are not configured.", http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	serviceName := r.PathValue("service")
	validatedServiceName, err := application.ValidateServiceName(serviceName)
	if err != nil || validatedServiceName != serviceName {
		http.NotFound(w, r)
		return
	}

	logs, err := logsService.GetServiceFullLogs(r.Context(), id, serviceName)
	if err != nil {
		if errors.Is(err, application.ErrNotFound) || errors.Is(err, application.ErrServiceNotFound) {
			http.NotFound(w, r)
			return
		}
		h.logger.Error("get full service logs", "application_id", id, "service", serviceName, "error", err)
		http.Error(w, "The service logs could not be read.", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="service-logs.txt"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(logs)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.WriteString(w, logs); err != nil {
		h.logger.Error("download full service logs", "application_id", id, "service", serviceName, "error", err)
	}
}

func (h *Handler) getServiceDetails(ctx context.Context, applicationID int64, serviceName string) (application.ServiceDetails, error) {
	if h.serviceDetails != nil {
		return h.serviceDetails.GetServiceDetails(ctx, applicationID, serviceName)
	}

	services, err := h.applicationDetails.ListServices(ctx, applicationID)
	if err != nil {
		return application.ServiceDetails{}, err
	}
	for _, service := range services {
		if service.Name == serviceName {
			return application.ServiceDetails{Service: service}, nil
		}
	}
	return application.ServiceDetails{}, application.ErrServiceNotFound
}

func (h *Handler) updateBackupSchedule(w http.ResponseWriter, r *http.Request) {
	if h.backupManager == nil {
		http.Error(w, "Scheduled backups are not configured.", http.StatusInternalServerError)
		return
	}
	id, serviceName, ok := h.prepareBackupRequest(w, r)
	if !ok {
		return
	}
	input := application.BackupScheduleInput{
		Enabled:       formValueOn(r.Form, "enabled"),
		ScheduleType:  r.Form.Get("schedule_type"),
		Hour:          backupFormInt(r.Form.Get("hour")),
		Minute:        backupFormInt(r.Form.Get("minute")),
		Weekday:       r.Form.Get("weekday"),
		RetentionDays: backupFormInt(r.Form.Get("retention_days")),
	}
	if err := h.backupManager.UpdateBackupSchedule(r.Context(), id, serviceName, input); err != nil {
		h.writeBackupError(w, r, "update scheduled backups", err)
		return
	}
	http.Redirect(w, r, serviceDetailsPath(id, serviceName)+"?tab=backups", http.StatusSeeOther)
}

func (h *Handler) runBackupNow(w http.ResponseWriter, r *http.Request) {
	if h.backupManager == nil {
		http.Error(w, "Backups are not configured.", http.StatusInternalServerError)
		return
	}
	id, serviceName, ok := h.prepareBackupRequest(w, r)
	if !ok {
		return
	}
	if _, err := h.backupManager.RunBackupNow(r.Context(), id, serviceName); err != nil {
		h.writeBackupError(w, r, "run backup", err)
		return
	}
	http.Redirect(w, r, serviceDetailsPath(id, serviceName)+"?tab=backups", http.StatusSeeOther)
}

func (h *Handler) restoreBackup(w http.ResponseWriter, r *http.Request) {
	if h.backupManager == nil {
		http.Error(w, "Backups are not configured.", http.StatusInternalServerError)
		return
	}
	id, serviceName, ok := h.prepareBackupRequest(w, r)
	if !ok {
		return
	}
	if err := h.backupManager.RestoreBackup(r.Context(), id, serviceName, r.Form.Get("backup_file")); err != nil {
		h.writeBackupError(w, r, "restore backup", err)
		return
	}
	http.Redirect(w, r, serviceDetailsPath(id, serviceName), http.StatusSeeOther)
}

func (h *Handler) deleteBackup(w http.ResponseWriter, r *http.Request) {
	if h.backupManager == nil {
		http.Error(w, "Backups are not configured.", http.StatusInternalServerError)
		return
	}
	id, serviceName, ok := h.prepareBackupRequest(w, r)
	if !ok {
		return
	}
	if err := h.backupManager.DeleteBackup(r.Context(), id, serviceName, r.Form.Get("backup_file")); err != nil {
		h.writeBackupError(w, r, "delete backup", err)
		return
	}
	http.Redirect(w, r, serviceDetailsPath(id, serviceName), http.StatusSeeOther)
}

func (h *Handler) downloadBackup(w http.ResponseWriter, r *http.Request) {
	if h.backupManager == nil {
		http.Error(w, "Backups are not configured.", http.StatusInternalServerError)
		return
	}
	id, serviceName, ok := h.prepareBackupDownloadRequest(w, r)
	if !ok {
		return
	}
	reader, backup, err := h.backupManager.OpenBackup(r.Context(), id, serviceName, r.URL.Query().Get("backup_file"))
	if err != nil {
		h.writeBackupError(w, r, "download backup", err)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/sql")
	w.Header().Set("Content-Disposition", `attachment; filename="`+backup.FileName+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(backup.SizeBytes, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, reader); err != nil {
		h.logger.Error("download backup", "application_id", id, "service", serviceName, "backup_file", backup.FileName, "error", err)
	}
}

func (h *Handler) prepareBackupRequest(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return 0, "", false
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return 0, "", false
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return 0, "", false
	}
	serviceName := r.PathValue("service")
	validatedServiceName, err := application.ValidateServiceName(serviceName)
	if err != nil || validatedServiceName != serviceName {
		http.Error(w, "The service name is invalid.", http.StatusBadRequest)
		return 0, "", false
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The backup request was invalid.", http.StatusBadRequest)
		return 0, "", false
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This backup page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return 0, "", false
	}
	return id, serviceName, true
}

func (h *Handler) prepareBackupDownloadRequest(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return 0, "", false
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return 0, "", false
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return 0, "", false
	}
	serviceName := r.PathValue("service")
	validatedServiceName, err := application.ValidateServiceName(serviceName)
	if err != nil || validatedServiceName != serviceName {
		http.Error(w, "The service name is invalid.", http.StatusBadRequest)
		return 0, "", false
	}
	return id, serviceName, true
}

func (h *Handler) writeBackupError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	switch {
	case errors.Is(err, application.ErrServiceNotFound), errors.Is(err, application.ErrNotFound), errors.Is(err, application.ErrBackupNotFound), errors.Is(err, application.ErrBackupUnsupported):
		http.NotFound(w, r)
	case errors.Is(err, application.ErrBackupServiceNotRunning):
		http.Error(w, "The database service must be running before a manual backup can be created.", http.StatusConflict)
	case errors.Is(err, application.ErrBackupScheduleTypeInvalid), errors.Is(err, application.ErrBackupHourInvalid), errors.Is(err, application.ErrBackupMinuteInvalid), errors.Is(err, application.ErrBackupWeekdayInvalid), errors.Is(err, application.ErrBackupRetentionInvalid), errors.Is(err, application.ErrBackupFileNameInvalid):
		http.Error(w, backupValidationMessage(err), http.StatusBadRequest)
	default:
		h.logger.Error(operation, "application_id", r.PathValue("id"), "service", r.PathValue("service"), "error", err)
		http.Error(w, "The "+operation+" could not be completed.", http.StatusInternalServerError)
	}
}

func backupFormInt(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return -1
	}
	return parsed
}

func formValueOn(values url.Values, key string) bool {
	for _, value := range values[key] {
		if value == "on" || value == "true" || value == "1" {
			return true
		}
	}
	return false
}

func (h *Handler) startService(w http.ResponseWriter, r *http.Request) {
	h.serviceAction(w, r, "start")
}

func (h *Handler) stopService(w http.ResponseWriter, r *http.Request) {
	h.serviceAction(w, r, "stop")
}

func (h *Handler) restartService(w http.ResponseWriter, r *http.Request) {
	h.serviceAction(w, r, "restart")
}

func (h *Handler) deleteApplication(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The application deletion request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This application deletion page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	item, err := h.applicationDetails.Get(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("get application for deletion", "application_id", id, "error", err)
		http.Error(w, "The application could not be deleted right now.", http.StatusInternalServerError)
		return
	}
	if firstFormValue(r.Form, "confirmation", "application_name_confirmation", "confirm_application_name") != item.Name {
		http.Error(w, "Type the application name exactly to confirm deletion.", http.StatusBadRequest)
		return
	}

	job, err := h.applicationDeleteJobs.create(id, item.Name)
	if err != nil {
		h.logger.Error("create application deletion job", "application_id", id, "error", err)
		http.Error(w, "The application deletion job could not be created.", http.StatusInternalServerError)
		return
	}
	go h.runApplicationDeleteJob(job)
	location := "/applications/" + strconv.FormatInt(id, 10) + "?application_delete_job=" + url.QueryEscape(job.id)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) deleteService(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	serviceName := r.PathValue("service")
	validatedServiceName, err := application.ValidateServiceName(serviceName)
	if err != nil || validatedServiceName != serviceName {
		http.Error(w, "The service name is invalid.", http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The service deletion request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This service deletion page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	if firstFormValue(r.Form, "confirmation", "service_name_confirmation", "confirm_service_name") != serviceName {
		http.Error(w, "Type the service name exactly to confirm deletion.", http.StatusBadRequest)
		return
	}

	job, err := h.serviceDeleteJobs.create(id, serviceName)
	if err != nil {
		h.logger.Error("create service deletion job", "application_id", id, "service", serviceName, "error", err)
		http.Error(w, "The service deletion job could not be created.", http.StatusInternalServerError)
		return
	}
	go h.runServiceDeleteJob(job)
	location := "/applications/" + strconv.FormatInt(id, 10) + "?service_delete_job=" + url.QueryEscape(job.id)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) serviceAction(w http.ResponseWriter, r *http.Request, action string) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	serviceName := r.PathValue("service")
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The service action request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This service action page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	var actionErr error
	switch action {
	case "start":
		actionErr = h.serviceActions.StartService(r.Context(), id, serviceName)
	case "stop":
		actionErr = h.serviceActions.StopService(r.Context(), id, serviceName)
	case "restart":
		actionErr = h.serviceActions.RestartService(r.Context(), id, serviceName)
	default:
		http.NotFound(w, r)
		return
	}
	if actionErr != nil {
		if errors.Is(actionErr, application.ErrNotFound) || errors.Is(actionErr, application.ErrServiceNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(actionErr, application.ErrServiceNameRequired) || errors.Is(actionErr, application.ErrServiceNameTooLong) || errors.Is(actionErr, application.ErrServiceNameInvalid) {
			http.Error(w, "The service name is invalid.", http.StatusBadRequest)
			return
		}
		h.logger.Error("run service action", "application_id", id, "service", serviceName, "action", action, "error", actionErr)
		http.Error(w, "The service could not be "+serviceActionPastTense(action)+".", http.StatusInternalServerError)
		return
	}
	location := "/applications/" + strconv.FormatInt(id, 10)
	if r.Form.Get("return_to") == "service-details" {
		location = serviceDetailsPath(id, serviceName)
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func serviceActionPastTense(action string) string {
	switch action {
	case "start":
		return "started"
	case "stop":
		return "stopped"
	case "restart":
		return "restarted"
	default:
		return "changed"
	}
}

func (h *Handler) postgreSQLServiceStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "The PostgreSQL service job ID is required.", http.StatusBadRequest)
		return
	}
	progress, ok := h.postgreSQLProgress(id, jobID)
	if !ok {
		http.Error(w, "The PostgreSQL service job was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "postgresql-progress.html", pageData{PostgreSQLProgress: progress}, http.StatusOK)
}

func (h *Handler) redisServiceStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "The Redis service job ID is required.", http.StatusBadRequest)
		return
	}
	progress, ok := h.redisProgress(id, jobID)
	if !ok {
		http.Error(w, "The Redis service job was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "redis-progress.html", pageData{RedisProgress: progress}, http.StatusOK)
}

func (h *Handler) applicationContainerStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "The application container job ID is required.", http.StatusBadRequest)
		return
	}
	progress, ok := h.applicationContainerProgress(id, jobID)
	if !ok {
		http.Error(w, "The application container job was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "application-container-progress.html", pageData{ApplicationContainerProgress: progress}, http.StatusOK)
}

func (h *Handler) serviceDeleteStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	serviceName := r.PathValue("service")
	validatedServiceName, err := application.ValidateServiceName(serviceName)
	if err != nil || validatedServiceName != serviceName {
		http.NotFound(w, r)
		return
	}
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "The service deletion job ID is required.", http.StatusBadRequest)
		return
	}
	progress, ok := h.serviceDeleteProgress(id, jobID)
	if !ok || progress.ServiceName != serviceName {
		http.Error(w, "The service deletion job was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "service-delete-progress.html", pageData{ServiceDeleteProgress: progress}, http.StatusOK)
}

func (h *Handler) applicationDeleteStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "The application deletion job ID is required.", http.StatusBadRequest)
		return
	}
	progress, ok := h.applicationDeleteProgress(id, jobID)
	if !ok {
		http.Error(w, "The application deletion job was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "application-delete-progress.html", pageData{ApplicationDeleteProgress: progress}, http.StatusOK)
}

func (h *Handler) postgreSQLProgress(applicationID int64, jobID string) (*postgresProgressData, bool) {
	if jobID == "" {
		return nil, true
	}
	job := h.postgresJobs.get(applicationID, jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	progress.StatusURL = "/applications/" + strconv.FormatInt(applicationID, 10) + "/services/postgresql/status?id=" + url.QueryEscape(jobID)
	progress.CloseURL = "/applications/" + strconv.FormatInt(applicationID, 10)
	return &progress, true
}

func (h *Handler) redisProgress(applicationID int64, jobID string) (*redisProgressData, bool) {
	if jobID == "" {
		return nil, true
	}
	job := h.redisJobs.get(applicationID, jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	progress.StatusURL = "/applications/" + strconv.FormatInt(applicationID, 10) + "/services/redis/status?id=" + url.QueryEscape(jobID)
	progress.CloseURL = "/applications/" + strconv.FormatInt(applicationID, 10)
	return &progress, true
}

func (h *Handler) applicationContainerProgress(applicationID int64, jobID string) (*applicationContainerProgressData, bool) {
	if jobID == "" {
		return nil, true
	}
	job := h.applicationContainerJobs.get(applicationID, jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	progress.StatusURL = "/applications/" + strconv.FormatInt(applicationID, 10) + "/services/application/status?id=" + url.QueryEscape(jobID)
	progress.CloseURL = "/applications/" + strconv.FormatInt(applicationID, 10)
	return &progress, true
}

func (h *Handler) serviceDeleteProgress(applicationID int64, jobID string) (*serviceDeleteProgressData, bool) {
	if jobID == "" {
		return nil, true
	}
	job := h.serviceDeleteJobs.get(applicationID, jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	progress.StatusURL = "/applications/" + strconv.FormatInt(applicationID, 10) + "/services/" + url.PathEscape(progress.ServiceName) + "/delete/status?id=" + url.QueryEscape(jobID)
	progress.CloseURL = "/applications/" + strconv.FormatInt(applicationID, 10)
	return &progress, true
}

func (h *Handler) applicationDeleteProgress(applicationID int64, jobID string) (*applicationDeleteProgressData, bool) {
	if jobID == "" {
		return nil, true
	}
	job := h.applicationDeleteJobs.get(applicationID, jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	progress.StatusURL = "/applications/" + strconv.FormatInt(applicationID, 10) + "/delete/status?id=" + url.QueryEscape(jobID)
	progress.CloseURL = "/applications"
	return &progress, true
}

func (h *Handler) postgreSQLServicePage(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		h.writeSetupPage(w, r, http.StatusOK, setupPageData{})
		return
	}

	item, ok := h.applicationForServicePage(w, r)
	if !ok {
		return
	}
	h.writePostgreSQLServicePage(w, r, http.StatusOK, newPostgreSQLServicePageData(item))
}

func (h *Handler) redisServicePage(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		h.writeSetupPage(w, r, http.StatusOK, setupPageData{})
		return
	}

	item, ok := h.applicationForServicePage(w, r)
	if !ok {
		return
	}
	h.writeRedisServicePage(w, r, http.StatusOK, newRedisServicePageData(item))
}

func (h *Handler) applicationContainerPage(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		h.writeSetupPage(w, r, http.StatusOK, setupPageData{})
		return
	}

	item, ok := h.applicationForServicePage(w, r)
	if !ok {
		return
	}
	h.writeApplicationContainerPage(w, r, http.StatusOK, newApplicationContainerPageData(item))
}

func (h *Handler) createPostgreSQLService(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	item, err := h.applicationDetails.Get(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("get application for PostgreSQL service", "id", id, "error", err)
		http.Error(w, "The application could not be read.", http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The PostgreSQL service request was invalid.", http.StatusBadRequest)
		return
	}
	data := postgresqlServicePageData{
		Application:     item,
		ServiceName:     firstFormValue(r.Form, "service_name", "name"),
		PostgresVersion: firstFormValue(r.Form, "postgres_version", "version"),
		DatabaseName:    firstFormValue(r.Form, "database_name", "db_name"),
		DatabaseUser:    firstFormValue(r.Form, "database_user", "username", "user"),
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		data.Error = "This database page expired. Submit the refreshed form to continue."
		h.writePostgreSQLServicePage(w, r, http.StatusForbidden, data)
		return
	}

	input := application.PostgreSQLServiceInput{
		ServiceName:      data.ServiceName,
		PostgresVersion:  data.PostgresVersion,
		DatabaseName:     data.DatabaseName,
		DatabaseUser:     data.DatabaseUser,
		DatabasePassword: firstFormValue(r.Form, "database_password", "password"),
	}
	if validator, ok := h.postgresManager.(postgresqlInputValidator); ok {
		if err := validator.ValidatePostgreSQLServiceInput(input); err != nil {
			message := postgresUserMessage(err)
			h.logger.Info("create PostgreSQL service rejected", "application_id", id, "reason", message)
			data.Error = message
			h.writePostgreSQLServicePage(w, r, http.StatusBadRequest, data)
			return
		}
	}
	job, err := h.postgresJobs.create(id)
	if err != nil {
		h.logger.Error("create PostgreSQL service job", "application_id", id, "error", err)
		h.writePostgreSQLServicePage(w, r, http.StatusInternalServerError, data)
		return
	}
	go h.runPostgresJob(job, input)
	location := "/applications/" + strconv.FormatInt(id, 10) + "?postgres_job=" + url.QueryEscape(job.id)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) createRedisService(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	item, err := h.applicationDetails.Get(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("get application for Redis service", "id", id, "error", err)
		http.Error(w, "The application could not be read.", http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The Redis service request was invalid.", http.StatusBadRequest)
		return
	}
	data := redisServicePageData{
		Application:   item,
		ServiceName:   firstFormValue(r.Form, "service_name", "name"),
		RedisVersion:  firstFormValue(r.Form, "redis_version", "version"),
		Port:          firstFormValue(r.Form, "port"),
		PersistToDisk: r.Form.Get("persist_to_disk") == "on",
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		data.Error = "This Redis page expired. Submit the refreshed form to continue."
		h.writeRedisServicePage(w, r, http.StatusForbidden, data)
		return
	}

	input := application.RedisServiceInput{
		ServiceName:   data.ServiceName,
		RedisVersion:  data.RedisVersion,
		Port:          data.Port,
		Password:      firstFormValue(r.Form, "password", "redis_password"),
		PersistToDisk: data.PersistToDisk,
	}
	if validator, ok := h.redisManager.(redisInputValidator); ok {
		if err := validator.ValidateRedisServiceInput(input); err != nil {
			message := redisUserMessage(err)
			h.logger.Info("create Redis service rejected", "application_id", id, "reason", message)
			data.Error = message
			h.writeRedisServicePage(w, r, http.StatusBadRequest, data)
			return
		}
	}
	job, err := h.redisJobs.create(id)
	if err != nil {
		h.logger.Error("create Redis service job", "application_id", id, "error", err)
		h.writeRedisServicePage(w, r, http.StatusInternalServerError, data)
		return
	}
	go h.runRedisJob(job, input)
	location := "/applications/" + strconv.FormatInt(id, 10) + "?redis_job=" + url.QueryEscape(job.id)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) createApplicationContainer(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	item, err := h.applicationDetails.Get(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("get application for application container", "id", id, "error", err)
		http.Error(w, "The application could not be read.", http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The application container request was invalid.", http.StatusBadRequest)
		return
	}
	data := applicationContainerPageData{
		Application:       item,
		ServiceName:       firstFormValue(r.Form, "service_name", "name"),
		ImageName:         firstFormValue(r.Form, "image_name", "image"),
		UseDockerRegistry: r.Form.Get("use_docker_registry") == "on",
		AutoStart:         r.Form.Get("auto_start") == "on",
	}
	if strings.TrimSpace(data.ServiceName) == "" {
		data.ServiceName = "app"
	}
	if strings.TrimSpace(data.ImageName) == "" {
		data.ImageName = defaultApplicationContainerImageName(data.ServiceName)
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		data.Error = "This application container page expired. Submit the refreshed form to continue."
		h.writeApplicationContainerPage(w, r, http.StatusForbidden, data)
		return
	}

	input := application.ApplicationServiceInput{
		ServiceName: data.ServiceName,
		ImageName:   applicationContainerImageName(data.ImageName, data.UseDockerRegistry),
		AutoStart:   data.AutoStart,
	}
	if validator, ok := h.applicationContainerManager.(applicationContainerInputValidator); ok {
		if err := validator.ValidateApplicationServiceInput(input); err != nil {
			message := applicationContainerUserMessage(err)
			h.logger.Info("create application container rejected", "application_id", id, "reason", message)
			data.Error = message
			h.writeApplicationContainerPage(w, r, http.StatusBadRequest, data)
			return
		}
	}
	job, err := h.applicationContainerJobs.create(id, input.AutoStart)
	if err != nil {
		h.logger.Error("create application container job", "application_id", id, "error", err)
		h.writeApplicationContainerPage(w, r, http.StatusInternalServerError, data)
		return
	}
	go h.runApplicationContainerJob(job, input)
	location := "/applications/" + strconv.FormatInt(id, 10) + "?application_job=" + url.QueryEscape(job.id)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) applicationForServicePage(w http.ResponseWriter, r *http.Request) (application.Application, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return application.Application{}, false
	}
	item, err := h.applicationDetails.Get(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return application.Application{}, false
	}
	if err != nil {
		h.logger.Error("get application for PostgreSQL service", "id", id, "error", err)
		http.Error(w, "The application could not be read.", http.StatusInternalServerError)
		return application.Application{}, false
	}
	return item, true
}

func newPostgreSQLServicePageData(item application.Application) postgresqlServicePageData {
	return postgresqlServicePageData{
		Application:     item,
		ServiceName:     "db",
		PostgresVersion: "17",
		DatabaseName:    item.Name,
		DatabaseUser:    "appuser",
	}
}

func (h *Handler) createApplication(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The application request was invalid.", http.StatusBadRequest)
		return
	}
	data := applicationPageData{
		Name:       firstFormValue(r.Form, "name", "application_name"),
		FolderName: firstFormValue(r.Form, "folder_name", "folder"),
		ModalOpen:  true,
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		data.Error = "This applications page expired. Submit the refreshed form to continue."
		data.Applications = h.currentApplications(r.Context())
		h.writeApplicationPage(w, r, http.StatusForbidden, data)
		return
	}

	if _, err := h.applicationManager.Create(r.Context(), data.Name, data.FolderName); err != nil {
		h.logger.Info("create application rejected", "reason", userMessage(err))
		data.Error = userMessage(err)
		data.Applications = h.currentApplications(r.Context())
		h.writeApplicationPage(w, r, http.StatusBadRequest, data)
		return
	}
	http.Redirect(w, r, "/applications", http.StatusSeeOther)
}

func (h *Handler) currentApplications(ctx context.Context) []application.Application {
	items, err := h.applicationManager.List(ctx)
	if err != nil {
		h.logger.Error("list applications after create failure", "error", err)
		return nil
	}
	return items
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (h *Handler) writeTemplate(w http.ResponseWriter, name string, data pageData) {
	h.writeTemplateStatus(w, name, data, http.StatusOK)
}

func (h *Handler) writeTemplateStatus(w http.ResponseWriter, name string, data pageData, status int) {
	var body bytes.Buffer
	if err := h.templates.ExecuteTemplate(&body, name, data); err != nil {
		h.logger.Error("render template", "template", name, "error", err)
		http.Error(w, "The page could not be rendered.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

func (h *Handler) writeLoginPage(w http.ResponseWriter, status int, data loginPageData) {
	var body bytes.Buffer
	if err := h.templates.ExecuteTemplate(&body, "login.html", data); err != nil {
		h.logger.Error("render login template", "error", err)
		http.Error(w, "The page could not be rendered.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

func (h *Handler) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https://googleusercontent.com https://*.googleusercontent.com; connect-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func userMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrNameRequired):
		return "Enter an application name."
	case errors.Is(err, application.ErrNameTooLong):
		return "Application names must be 64 characters or fewer."
	case errors.Is(err, application.ErrNameInvalid):
		return "Application names cannot contain control characters."
	case errors.Is(err, application.ErrFolderNameRequired):
		return "Enter a folder name."
	case errors.Is(err, application.ErrFolderNameTooLong):
		return "Folder names must be 64 characters or fewer."
	case errors.Is(err, application.ErrFolderNameInvalid):
		return "Folder names must be a single, valid directory name."
	case errors.Is(err, application.ErrAlreadyExists):
		return "An application with that name or folder already exists."
	default:
		return "The application could not be created."
	}
}

func backupValidationMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrBackupScheduleTypeInvalid):
		return "Choose hourly, daily, or weekly backups."
	case errors.Is(err, application.ErrBackupHourInvalid):
		return "The backup hour must be between 00 and 23."
	case errors.Is(err, application.ErrBackupMinuteInvalid):
		return "The backup minute must be between 00 and 59."
	case errors.Is(err, application.ErrBackupWeekdayInvalid):
		return "Choose a valid weekday for weekly backups."
	case errors.Is(err, application.ErrBackupRetentionInvalid):
		return "Retention must be between 1 and 3650 days."
	case errors.Is(err, application.ErrBackupFileNameInvalid):
		return "The selected backup file is invalid."
	default:
		return "The backup request is invalid."
	}
}

func serviceTypeClass(serviceType string) string {
	switch strings.ToLower(strings.TrimSpace(serviceType)) {
	case application.ServiceTypePostgreSQL, "postgres", "database", "db":
		return "database"
	case "redis", "cache":
		return "cache"
	default:
		return "app"
	}
}

func serviceDetailsPath(applicationID int64, serviceName string) string {
	return "/applications/" + strconv.FormatInt(applicationID, 10) + "/services/" + url.PathEscape(serviceName)
}

func backupDownloadPath(applicationID int64, serviceName, fileName string) string {
	return serviceDetailsPath(applicationID, serviceName) + "/backups/download?backup_file=" + url.QueryEscape(fileName)
}

func applicationRoutingPath(applicationID, domainID int64) string {
	return "/applications/" + strconv.FormatInt(applicationID, 10) + "/domains/" + strconv.FormatInt(domainID, 10) + "/routing"
}

func routingHost(domainName, subdomain string) string {
	if subdomain == "" {
		return domainName
	}
	return subdomain + "." + domainName
}

func environmentSensitive(variable application.EnvironmentVariable) bool {
	return variable.Sensitive || application.IsSensitiveEnvironmentKey(variable.Key)
}

func serviceTypeLabel(serviceType string) string {
	if strings.EqualFold(strings.TrimSpace(serviceType), application.ServiceTypeApplication) {
		return "Application"
	}
	switch serviceTypeClass(serviceType) {
	case "database":
		return "Database"
	case "cache":
		return "Cache"
	default:
		return "App"
	}
}

func serviceStatusClass(status string) string {
	lower := strings.ToLower(strings.TrimSpace(status))
	switch {
	case serviceIsRunning(lower):
		return "service-status-running"
	case strings.Contains(lower, "restarting"), strings.Contains(lower, "paused"), strings.HasPrefix(lower, "created"):
		return "service-status-warning"
	case strings.HasPrefix(lower, "stopped"), strings.Contains(lower, "exited"), strings.Contains(lower, "dead"), strings.Contains(lower, "removing"):
		return "service-status-failed"
	default:
		return "service-status-neutral"
	}
}

func serviceIsRunning(status string) bool {
	lower := strings.ToLower(strings.TrimSpace(status))
	return strings.HasPrefix(lower, "up") || strings.HasPrefix(lower, "running") || strings.HasPrefix(lower, "started")
}

func serviceIsStopped(status string) bool {
	lower := strings.ToLower(strings.TrimSpace(status))
	return strings.HasPrefix(lower, "stopped") || strings.HasPrefix(lower, "exited") || strings.HasPrefix(lower, "dead")
}

func backupTimeText(backup application.Backup) string {
	if backup.CreatedAt.IsZero() {
		return "—"
	}
	return backup.CreatedAt.Local().Format("2006-01-02 15:04")
}

func backupTimeISO(backup application.Backup) string {
	if backup.CreatedAt.IsZero() {
		return ""
	}
	return backup.CreatedAt.Format(time.RFC3339)
}

func backupAtText(at time.Time) string {
	if at.IsZero() {
		return "—"
	}
	return at.Local().Format("2006-01-02 15:04")
}

func backupAtISO(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format(time.RFC3339)
}

func backupSizeText(size int64) string {
	if size < 0 {
		return "—"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(size)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return strconv.FormatInt(size, 10) + " " + units[unit]
	}
	if value >= 10 {
		return fmt.Sprintf("%.0f %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func backupStatusClass(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "successful", "success":
		return "backup-status-success"
	case "failed", "failure":
		return "backup-status-failed"
	default:
		return "backup-status-neutral"
	}
}

func backupStatusText(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "successful", "success":
		return "Successful"
	case "failed", "failure":
		return "Failed"
	default:
		return "Not run"
	}
}

func backupScheduleTypeText(scheduleType string) string {
	switch strings.ToLower(strings.TrimSpace(scheduleType)) {
	case application.BackupScheduleHourly:
		return "Hourly"
	case application.BackupScheduleDaily:
		return "Daily"
	case application.BackupScheduleWeekly:
		return "Weekly"
	default:
		return "Unknown"
	}
}

func backupWeekdayText(weekday string) string {
	switch strings.ToLower(strings.TrimSpace(weekday)) {
	case "monday":
		return "Monday"
	case "tuesday":
		return "Tuesday"
	case "wednesday":
		return "Wednesday"
	case "thursday":
		return "Thursday"
	case "friday":
		return "Friday"
	case "saturday":
		return "Saturday"
	case "sunday":
		return "Sunday"
	default:
		return "Unknown"
	}
}

func backupStatusIcon(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "successful", "success":
		return "✓"
	case "failed", "failure":
		return "!"
	default:
		return "·"
	}
}

func serviceLogLines(logs string) []string {
	logs = strings.Trim(logs, "\r\n")
	if logs == "" {
		return nil
	}
	return strings.Split(logs, "\n")
}

func proxyLogLines(logs string) []string {
	lines := serviceLogLines(logs)
	if len(lines) <= application.ProxyLogLineLimit {
		return lines
	}
	return lines[len(lines)-application.ProxyLogLineLimit:]
}

func serviceCreatedAt(service application.Service) time.Time {
	if !service.ContainerCreatedAt.IsZero() {
		return service.ContainerCreatedAt
	}
	return service.CreatedAt
}

func serviceCreatedAtText(service application.Service) string {
	createdAt := serviceCreatedAt(service)
	if createdAt.IsZero() {
		return "—"
	}
	return createdAt.Format("2006-01-02 15:04")
}

func serviceCreatedAtISO(service application.Service) string {
	createdAt := serviceCreatedAt(service)
	if createdAt.IsZero() {
		return ""
	}
	return createdAt.Format(time.RFC3339)
}

func serviceCreatedAtTitle(service application.Service) string {
	createdAt := serviceCreatedAt(service)
	if createdAt.IsZero() {
		return "Creation date unavailable"
	}
	return createdAt.Format("2006-01-02 15:04:05 MST")
}

func postgresUserMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrServiceNameRequired):
		return "Enter a service name."
	case errors.Is(err, application.ErrServiceNameTooLong):
		return "Service names must be 64 characters or fewer."
	case errors.Is(err, application.ErrServiceNameInvalid):
		return "Service names may contain only letters, numbers, dots, hyphens, and underscores."
	case errors.Is(err, application.ErrPostgresVersionRequired):
		return "Enter a PostgreSQL version."
	case errors.Is(err, application.ErrPostgresVersionTooLong):
		return "PostgreSQL versions must be 64 characters or fewer."
	case errors.Is(err, application.ErrPostgresVersionInvalid):
		return "PostgreSQL versions may contain only Docker image tag characters."
	case errors.Is(err, application.ErrDatabaseNameRequired):
		return "Enter a database name."
	case errors.Is(err, application.ErrDatabaseNameTooLong):
		return "Database names must be 63 characters or fewer."
	case errors.Is(err, application.ErrDatabaseNameInvalid):
		return "Database names may contain only letters, numbers, spaces, dots, hyphens, and underscores."
	case errors.Is(err, application.ErrDatabaseUserRequired):
		return "Enter a database user."
	case errors.Is(err, application.ErrDatabaseUserTooLong):
		return "Database users must be 63 characters or fewer."
	case errors.Is(err, application.ErrDatabaseUserInvalid):
		return "Database users may contain only letters, numbers, and underscores."
	case errors.Is(err, application.ErrDatabasePasswordTooLong):
		return "Database passwords must be 256 characters or fewer."
	case errors.Is(err, application.ErrDatabasePasswordInvalid):
		return "Database passwords cannot contain control characters."
	case errors.Is(err, application.ErrServiceAlreadyExists):
		return "A service with that name already exists."
	default:
		return postgresOperationalUserMessage(err)
	}
}

func redisUserMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrServiceNameRequired):
		return "Enter a service name."
	case errors.Is(err, application.ErrServiceNameTooLong):
		return "Service names must be 64 characters or fewer."
	case errors.Is(err, application.ErrServiceNameInvalid):
		return "Service names may contain only letters, numbers, dots, hyphens, and underscores."
	case errors.Is(err, application.ErrRedisVersionRequired):
		return "Enter a Redis version."
	case errors.Is(err, application.ErrRedisVersionTooLong):
		return "Redis versions must be 64 characters or fewer."
	case errors.Is(err, application.ErrRedisVersionInvalid):
		return "Redis versions may contain only Docker image tag characters."
	case errors.Is(err, application.ErrRedisPortRequired):
		return "Enter a Redis port."
	case errors.Is(err, application.ErrRedisPortTooLong), errors.Is(err, application.ErrRedisPortInvalid):
		return "Redis ports must be numbers from 1 to 65535."
	case errors.Is(err, application.ErrRedisPasswordTooLong):
		return "Redis passwords must be 256 characters or fewer."
	case errors.Is(err, application.ErrRedisPasswordInvalid):
		return "Redis passwords cannot contain control characters."
	case errors.Is(err, application.ErrServiceAlreadyExists):
		return "A service with that name already exists."
	default:
		return redisOperationalUserMessage(err)
	}
}

func applicationContainerUserMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrServiceNameRequired):
		return "Enter a service name."
	case errors.Is(err, application.ErrServiceNameTooLong):
		return "Service names must be 64 characters or fewer."
	case errors.Is(err, application.ErrServiceNameInvalid):
		return "Service names may contain only letters, numbers, dots, hyphens, and underscores."
	case errors.Is(err, application.ErrImageNameRequired):
		return "Enter an image name or leave it empty to use the local registry default."
	case errors.Is(err, application.ErrImageNameTooLong):
		return "Image names must be 255 characters or fewer."
	case errors.Is(err, application.ErrImageNameInvalid):
		return "Enter a valid Docker image reference."
	case errors.Is(err, application.ErrServiceAlreadyExists):
		return "A service with that name already exists."
	default:
		return applicationContainerOperationalUserMessage(err)
	}
}

func composeImportUserError(err error) bool {
	return errors.Is(err, application.ErrComposeFileRequired) ||
		errors.Is(err, application.ErrComposeFileTooLarge) ||
		errors.Is(err, application.ErrComposeFileInvalid) ||
		errors.Is(err, application.ErrComposeProjectHasNoServices) ||
		errors.Is(err, application.ErrComposeServicesAlreadyExist)
}

func environmentImportUserError(err error) bool {
	return errors.Is(err, application.ErrEnvironmentImportFileRequired) ||
		errors.Is(err, application.ErrEnvironmentImportFileTooLarge) ||
		errors.Is(err, application.ErrEnvironmentImportInvalid)
}

func environmentImportKind(value string) string {
	if value == "secrets" {
		return "secrets"
	}
	return "variables"
}

func environmentImportMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrEnvironmentImportFileRequired):
		return "Choose a vars.env or secrets.env file."
	case errors.Is(err, application.ErrEnvironmentImportFileTooLarge):
		return "Environment files must be 1 MB or smaller."
	case errors.Is(err, application.ErrEnvironmentImportInvalid):
		return "The uploaded environment file is invalid. Use KEY=value entries and try again."
	default:
		return "The environment file could not be imported right now."
	}
}

func composeImportUserMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrComposeFileRequired):
		return "Choose a Docker Compose file."
	case errors.Is(err, application.ErrComposeFileTooLarge):
		return "Compose files must be 4 MB or smaller."
	case errors.Is(err, application.ErrComposeProjectHasNoServices):
		return "The Docker Compose file must define at least one service."
	case errors.Is(err, application.ErrComposeServicesAlreadyExist):
		return "Import is available only before services have been added to this application."
	case errors.Is(err, application.ErrComposeFileInvalid):
		return "The Docker Compose file is invalid or could not be validated. Check the file and try again."
	default:
		return "The Docker Compose project could not be imported right now."
	}
}

const maxApplicationContainerDiagnosticLength = 2048

func applicationContainerOperationalUserMessage(err error) string {
	raw := ""
	if err != nil {
		raw = strings.TrimSpace(err.Error())
	}
	lower := strings.ToLower(raw)
	detail := applicationContainerErrorDetail(err)
	portConflict := strings.Contains(lower, "port is already allocated") ||
		strings.Contains(lower, "address already in use") ||
		strings.Contains(lower, "failed to bind") ||
		strings.Contains(lower, "bind for ")

	switch {
	case strings.HasPrefix(lower, "start application service:") && portConflict:
		return "The application container was configured, but it could not be started because a required port is already in use. Docker Compose reported: " + detail
	case strings.HasPrefix(lower, "start application service:"):
		return "The application container was configured, but it could not be started: " + detail
	case portConflict:
		return "The application container could not be created because a required port is already in use. Docker Compose reported: " + detail
	case strings.HasPrefix(lower, "persist application service metadata:"):
		return "The application container could not be created because its metadata could not be saved: " + detail
	case strings.HasPrefix(lower, "add application service to compose file:"):
		return "The application container could not be created because the Compose file could not be updated: " + detail
	case strings.HasPrefix(lower, "write application compose file:"):
		return "The application container could not be created because the Compose file could not be written: " + detail
	case strings.HasPrefix(lower, "write application variables file:") || strings.HasPrefix(lower, "write application secrets file:"):
		return "The application container could not be created because its environment files could not be written: " + detail
	default:
		return "The application container could not be created: " + detail
	}
}

func applicationContainerErrorDetail(err error) string {
	if err == nil {
		return "no additional details were provided"
	}

	detail := strings.TrimSpace(err.Error())
	prefixes := []string{
		"start application service: ",
		"persist application service metadata: ",
		"add application service to Compose file: ",
		"write application Compose file: ",
		"write application variables file: ",
		"write application secrets file: ",
		"read application Compose file: ",
		"read application variables file: ",
		"read application secrets file: ",
		"run compose project: ",
	}
	for {
		previous := detail
		for _, prefix := range prefixes {
			if strings.HasPrefix(detail, prefix) {
				detail = strings.TrimSpace(strings.TrimPrefix(detail, prefix))
				break
			}
		}
		if detail == previous {
			break
		}
	}
	if detail == "" {
		return "no additional details were provided"
	}
	runes := []rune(detail)
	if len(runes) > maxApplicationContainerDiagnosticLength {
		return string(runes[:maxApplicationContainerDiagnosticLength]) + "…"
	}
	return detail
}

const maxPostgreSQLDiagnosticLength = 2048

func postgresOperationalUserMessage(err error) string {
	raw := ""
	if err != nil {
		raw = strings.TrimSpace(err.Error())
	}
	lower := strings.ToLower(raw)
	detail := postgresErrorDetail(err)
	portConflict := strings.Contains(lower, "port is already allocated") ||
		strings.Contains(lower, "address already in use") ||
		strings.Contains(lower, "failed to bind") ||
		strings.Contains(lower, "bind for ")

	switch {
	case strings.HasPrefix(lower, "start postgresql service:") && portConflict:
		return "The PostgreSQL database was configured, but the container could not be started because a required port is already in use. Docker Compose reported: " + detail
	case strings.HasPrefix(lower, "start postgresql service:"):
		return "The PostgreSQL database was configured, but the container could not be started: " + detail
	case portConflict:
		return "The PostgreSQL database could not be created because a required port is already in use. Docker Compose reported: " + detail
	case strings.HasPrefix(lower, "persist postgresql service metadata:"):
		return "The PostgreSQL database could not be created because its metadata could not be saved: " + detail
	case strings.HasPrefix(lower, "add postgresql service to compose file:"):
		return "The PostgreSQL database could not be created because the Compose file could not be updated: " + detail
	case strings.HasPrefix(lower, "write application compose file:"):
		return "The PostgreSQL database could not be created because the Compose file could not be written: " + detail
	case strings.HasPrefix(lower, "write application environment file:"):
		return "The PostgreSQL database could not be created because its environment file could not be written: " + detail
	case strings.HasPrefix(lower, "generate postgresql password:"):
		return "The PostgreSQL database could not be created because a secure password could not be generated: " + detail
	default:
		return "The PostgreSQL database could not be created: " + detail
	}
}

const maxRedisDiagnosticLength = 2048

func redisOperationalUserMessage(err error) string {
	raw := ""
	if err != nil {
		raw = strings.TrimSpace(err.Error())
	}
	lower := strings.ToLower(raw)
	detail := redisErrorDetail(err)
	portConflict := strings.Contains(lower, "port is already allocated") ||
		strings.Contains(lower, "address already in use") ||
		strings.Contains(lower, "failed to bind") ||
		strings.Contains(lower, "bind for ")

	switch {
	case strings.HasPrefix(lower, "start redis service:") && portConflict:
		return "The Redis service was configured, but the container could not be started because a required port is already in use. Docker Compose reported: " + detail
	case strings.HasPrefix(lower, "start redis service:"):
		return "The Redis service was configured, but the container could not be started: " + detail
	case portConflict:
		return "The Redis service could not be created because a required port is already in use. Docker Compose reported: " + detail
	case strings.HasPrefix(lower, "persist redis service metadata:"):
		return "The Redis service could not be created because its metadata could not be saved: " + detail
	case strings.HasPrefix(lower, "add redis service to compose file:"):
		return "The Redis service could not be created because the Compose file could not be updated: " + detail
	case strings.HasPrefix(lower, "write application compose file:"):
		return "The Redis service could not be created because the Compose file could not be written: " + detail
	case strings.HasPrefix(lower, "write application environment file:"):
		return "The Redis service could not be created because its environment file could not be written: " + detail
	default:
		return "The Redis service could not be created: " + detail
	}
}

func redisErrorDetail(err error) string {
	if err == nil {
		return "no additional details were provided"
	}

	detail := strings.TrimSpace(err.Error())
	prefixes := []string{
		"start Redis service: ",
		"persist Redis service metadata: ",
		"add Redis service to Compose file: ",
		"write application Compose file: ",
		"write application environment file: ",
		"read application Compose file: ",
		"read application environment file: ",
		"run compose project: ",
	}
	for {
		previous := detail
		for _, prefix := range prefixes {
			if strings.HasPrefix(detail, prefix) {
				detail = strings.TrimSpace(strings.TrimPrefix(detail, prefix))
				break
			}
		}
		if detail == previous {
			break
		}
	}

	detail = redactRedisDiagnostics(detail)
	if detail == "" {
		return "no additional details were provided"
	}
	runes := []rune(detail)
	if len(runes) > maxRedisDiagnosticLength {
		return string(runes[:maxRedisDiagnosticLength]) + "…"
	}
	return detail
}

func redactRedisDiagnostics(detail string) string {
	lines := strings.Split(detail, "\n")
	for index, line := range lines {
		lower := strings.ToLower(line)
		markerIndex := -1
		for _, marker := range []string{"redis_password", "password=", "password:"} {
			if candidate := strings.Index(lower, marker); candidate >= 0 && (markerIndex < 0 || candidate < markerIndex) {
				markerIndex = candidate
			}
		}
		if markerIndex >= 0 {
			lines[index] = line[:markerIndex] + "[Redis password details redacted]"
		}
	}
	return strings.Join(lines, "\n")
}

func postgresErrorDetail(err error) string {
	if err == nil {
		return "no additional details were provided"
	}

	detail := strings.TrimSpace(err.Error())
	prefixes := []string{
		"start PostgreSQL service: ",
		"persist PostgreSQL service metadata: ",
		"add PostgreSQL service to Compose file: ",
		"write application Compose file: ",
		"write application environment file: ",
		"read application Compose file: ",
		"read application environment file: ",
		"generate PostgreSQL password: ",
		"run compose project: ",
	}
	for {
		previous := detail
		for _, prefix := range prefixes {
			if strings.HasPrefix(detail, prefix) {
				detail = strings.TrimSpace(strings.TrimPrefix(detail, prefix))
				break
			}
		}
		if detail == previous {
			break
		}
	}

	detail = redactPostgreSQLDiagnostics(detail)
	if detail == "" {
		return "no additional details were provided"
	}
	runes := []rune(detail)
	if len(runes) > maxPostgreSQLDiagnosticLength {
		return string(runes[:maxPostgreSQLDiagnosticLength]) + "…"
	}
	return detail
}

func redactPostgreSQLDiagnostics(detail string) string {
	lines := strings.Split(detail, "\n")
	for index, line := range lines {
		lower := strings.ToLower(line)
		markerIndex := -1
		for _, marker := range []string{"postgres_password", "password=", "password:"} {
			if candidate := strings.Index(lower, marker); candidate >= 0 && (markerIndex < 0 || candidate < markerIndex) {
				markerIndex = candidate
			}
		}
		if markerIndex >= 0 {
			lines[index] = line[:markerIndex] + "[PostgreSQL password details redacted]"
		}
	}
	return strings.Join(lines, "\n")
}

func firstFormValue(values url.Values, names ...string) string {
	for _, name := range names {
		if value := values.Get(name); value != "" {
			return value
		}
	}
	return ""
}

func defaultApplicationContainerImageName(serviceName string) string {
	return strings.TrimPrefix(application.DefaultApplicationImageName(serviceName), application.LocalRegistryAddress+"/")
}

func applicationContainerImageName(imageName string, useDockerRegistry bool) string {
	imageName = strings.TrimSpace(imageName)
	if useDockerRegistry || imageName == "" {
		return imageName
	}

	localRegistryPrefix := application.LocalRegistryAddress + "/"
	if strings.HasPrefix(imageName, localRegistryPrefix) {
		return imageName
	}
	return localRegistryPrefix + imageName
}

type pageData struct {
	ActivePage                   string
	Server                       ServerInfo
	CSRFToken                    string
	User                         *sidebarUserData
	Setup                        *setupPageData
	SetupProgress                *setupProgressData
	PostgreSQLProgress           *postgresProgressData
	RedisProgress                *redisProgressData
	ApplicationContainerProgress *applicationContainerProgressData
	ServiceDeleteProgress        *serviceDeleteProgressData
	ApplicationDeleteProgress    *applicationDeleteProgressData
	ApplicationsPage             *applicationPageData
	ApplicationDetailsPage       *applicationDetailsPageData
	ApplicationRoutingPage       *applicationRoutingPageData
	ServiceDetailsPage           *serviceDetailsPageData
	ProxyPage                    *proxyPageData
	PostgreSQLServicePage        *postgresqlServicePageData
	RedisServicePage             *redisServicePageData
	ApplicationContainerPage     *applicationContainerPageData
	DashboardPage                *dashboardPageData
}

type sidebarUserData struct {
	Name       string
	Email      string
	PictureURL string
	Initials   string
}

func (h *Handler) shellPageData(r *http.Request) pageData {
	data := pageData{
		Server:    h.server,
		CSRFToken: h.csrfToken,
	}
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		data.CSRFToken = cookie.Value
	}
	if user, ok := r.Context().Value(authenticatedUserContextKey{}).(redlaunchauth.User); ok && user.Email != "" {
		name := strings.TrimSpace(user.Name)
		if name == "" {
			name = user.Email
		}
		data.User = &sidebarUserData{
			Name:       name,
			Email:      user.Email,
			PictureURL: safeProfileImageURL(user.PictureURL),
			Initials:   userInitials(name, user.Email),
		}
	}
	return data
}

func safeProfileImageURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Port() != "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "googleusercontent.com" && !strings.HasSuffix(host, ".googleusercontent.com") {
		return ""
	}
	return parsed.String()
}

func userInitials(name, email string) string {
	value := strings.TrimSpace(name)
	if value == "" {
		value = strings.TrimSpace(email)
	}
	words := strings.Fields(value)
	if len(words) == 0 {
		return "?"
	}

	initials := []rune(strings.ToUpper(string([]rune(words[0])[0])))
	if len(words) > 1 {
		last := []rune(words[len(words)-1])
		initials = append(initials, []rune(strings.ToUpper(string(last[0])))...)
	} else if runes := []rune(words[0]); len(runes) > 1 {
		initials = append(initials, []rune(strings.ToUpper(string(runes[1])))...)
	}
	return string(initials)
}

type loginPageData struct {
	Error          string
	GoogleLoginURL string
}

type setupPageData struct {
	Error           string
	CSRFToken       string
	InstallProxy    bool
	InstallRegistry bool
}

const maxFormBody = 1 << 20

type applicationPageData struct {
	Applications []application.Application
	CSRFToken    string
	Error        string
	Name         string
	FolderName   string
	ModalOpen    bool
}

type applicationDetailsPageData struct {
	Application               application.Application
	Services                  []application.Service
	ImportError               string
	EnvironmentImportError    string
	EnvironmentImportKind     string
	Domains                   []application.Domain
	PublicAccess              application.RedlaunchPublicAccess
	PublicAccessError         string
	Variables                 environmentFilePageData
	Secrets                   environmentFilePageData
	CSRFToken                 string
	DomainEdit                *domainEditPageData
	DomainDelete              *domainDeletePageData
	VariableEdit              *variableEditPageData
	VariableDelete            *variableDeletePageData
	SecretEdit                *secretEditPageData
	SecretDelete              *secretDeletePageData
	ServiceDeleteProgress     *serviceDeleteProgressData
	ApplicationDeleteProgress *applicationDeleteProgressData
}

type applicationRoutingPageData struct {
	Application   application.Application
	Domain        application.Domain
	Routings      []application.Routing
	Services      []application.Service
	CSRFToken     string
	RoutingEdit   *routingEditPageData
	RoutingDelete *routingDeletePageData
}

type environmentFilePageData struct {
	ID              string
	Title           string
	Description     string
	ApplicationName string
	Entries         []application.EnvironmentVariable
	Available       bool
	Editable        bool
	MaskValues      bool
}

type variableEditPageData struct {
	Open         bool
	Add          bool
	Error        string
	OriginalName string
	Name         string
	Value        string
}

type domainEditPageData struct {
	Open  bool
	Error string
	Name  string
}

type domainDeletePageData struct {
	Open  bool
	Name  string
	Error string
}

type routingEditPageData struct {
	Open        bool
	Add         bool
	Error       string
	ID          int64
	Subdomain   string
	Path        string
	ServiceName string
	ServicePath string
}

type routingDeletePageData struct {
	Open  bool
	ID    int64
	Host  string
	Error string
}

type variableDeletePageData struct {
	Open  bool
	Name  string
	Error string
}

type secretEditPageData struct {
	Open         bool
	Add          bool
	Error        string
	OriginalName string
	Name         string
	Value        string
}

type secretDeletePageData struct {
	Open  bool
	Name  string
	Error string
}

type serviceDetailsPageData struct {
	Application application.Application
	Details     application.ServiceDetails
	CSRFToken   string
}

type proxyPageData struct {
	Details   application.ProxyDetails
	CSRFToken string
}

type postgresqlServicePageData struct {
	Application     application.Application
	CSRFToken       string
	Error           string
	ServiceName     string
	PostgresVersion string
	DatabaseName    string
	DatabaseUser    string
}

type redisServicePageData struct {
	Application   application.Application
	CSRFToken     string
	Error         string
	ServiceName   string
	RedisVersion  string
	Port          string
	PersistToDisk bool
}

type applicationContainerPageData struct {
	Application       application.Application
	CSRFToken         string
	Error             string
	ServiceName       string
	ImageName         string
	UseDockerRegistry bool
	AutoStart         bool
}

type noSetupManager struct{}

func (noSetupManager) NeedsSetup() (bool, error) {
	return false, nil
}

func (noSetupManager) Setup(context.Context, bool, bool) error {
	return errors.New("setup service is not configured")
}

type noProxyService struct{}

func (noProxyService) GetProxyDetails(context.Context) (application.ProxyDetails, error) {
	return application.ProxyDetails{}, nil
}

func (noProxyService) StartProxy(context.Context) error {
	return errors.New("proxy action service is not configured")
}

func (noProxyService) StopProxy(context.Context) error {
	return errors.New("proxy action service is not configured")
}

func (noProxyService) RestartProxy(context.Context) error {
	return errors.New("proxy action service is not configured")
}

func (noProxyService) GetProxyFullLogs(context.Context) (string, error) {
	return "", errors.New("proxy log service is not configured")
}

type noApplicationService struct{}

func (noApplicationService) List(context.Context) ([]application.Application, error) {
	return nil, nil
}

func (noApplicationService) Create(context.Context, string, string) (application.Application, error) {
	return application.Application{}, errors.New("application service is not configured")
}

func (noApplicationService) Get(context.Context, int64) (application.Application, error) {
	return application.Application{}, application.ErrNotFound
}

func (noApplicationService) ListServices(context.Context, int64) ([]application.Service, error) {
	return nil, errors.New("application service is not configured")
}

func (noApplicationService) ListDomains(context.Context, int64) ([]application.Domain, error) {
	return nil, nil
}

func (noApplicationService) CreateDomain(context.Context, int64, string) (application.Domain, error) {
	return application.Domain{}, errors.New("application service is not configured")
}

func (noApplicationService) DeleteDomain(context.Context, int64, string) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) ListRoutings(context.Context, int64, int64) ([]application.Routing, error) {
	return nil, errors.New("application service is not configured")
}

func (noApplicationService) CreateRouting(context.Context, int64, int64, application.RoutingInput) (application.Routing, error) {
	return application.Routing{}, errors.New("application service is not configured")
}

func (noApplicationService) UpdateRouting(context.Context, int64, int64, int64, application.RoutingInput) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) DeleteRouting(context.Context, int64, int64, int64) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) GetRedlaunchPublicAccess(context.Context) (application.RedlaunchPublicAccess, error) {
	return application.RedlaunchPublicAccess{}, nil
}

func (noApplicationService) UpdateRedlaunchPublicAccess(context.Context, application.RedlaunchPublicAccessInput) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) GetEnvironmentFiles(context.Context, int64) (application.EnvironmentFiles, error) {
	return application.EnvironmentFiles{}, nil
}

func (noApplicationService) UpdateEnvironmentVariable(context.Context, int64, string, string, string) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) AddEnvironmentVariable(context.Context, int64, string, string) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) DeleteEnvironmentVariable(context.Context, int64, string) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) UpdateEnvironmentSecret(context.Context, int64, string, string, string) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) AddEnvironmentSecret(context.Context, int64, string, string) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) DeleteEnvironmentSecret(context.Context, int64, string) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) StartService(context.Context, int64, string) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) StopService(context.Context, int64, string) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) RestartService(context.Context, int64, string) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) DeleteService(context.Context, int64, string) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) DeleteServiceWithProgress(context.Context, int64, string, func(stage, message string)) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) DeleteApplication(context.Context, int64) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) DeleteApplicationWithProgress(context.Context, int64, func(stage, message string)) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) CreatePostgreSQLService(context.Context, int64, application.PostgreSQLServiceInput) (application.Service, error) {
	return application.Service{}, errors.New("application service is not configured")
}

func (noApplicationService) ValidatePostgreSQLServiceInput(application.PostgreSQLServiceInput) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) CreateRedisService(context.Context, int64, application.RedisServiceInput) (application.Service, error) {
	return application.Service{}, errors.New("application service is not configured")
}

func (noApplicationService) ValidateRedisServiceInput(application.RedisServiceInput) error {
	return errors.New("application service is not configured")
}

func (noApplicationService) CreateApplicationService(context.Context, int64, application.ApplicationServiceInput) (application.Service, error) {
	return application.Service{}, errors.New("application service is not configured")
}

func (noApplicationService) ValidateApplicationServiceInput(application.ApplicationServiceInput) error {
	return errors.New("application service is not configured")
}

func (h *Handler) writeSetupPage(w http.ResponseWriter, r *http.Request, status int, data setupPageData, progress ...*setupProgressData) {
	csrfToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		csrfToken = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	w.Header().Set("Cache-Control", "no-store")
	data.CSRFToken = csrfToken
	page := h.shellPageData(r)
	page.Setup = &data
	if len(progress) > 0 {
		page.SetupProgress = progress[0]
	}
	h.writeTemplateStatus(w, "index.html", page, status)
}

func (h *Handler) writeApplicationPage(w http.ResponseWriter, r *http.Request, status int, data applicationPageData) {
	csrfToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		csrfToken = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	w.Header().Set("Cache-Control", "no-store")
	data.CSRFToken = csrfToken
	page := h.shellPageData(r)
	page.ActivePage = "applications"
	page.ApplicationsPage = &data
	h.writeTemplateStatus(w, "applications.html", page, status)
}

func (h *Handler) writeApplicationDetailsPage(w http.ResponseWriter, r *http.Request, status int, data applicationDetailsPageData, postgresProgress *postgresProgressData, redisProgress *redisProgressData, applicationProgress ...*applicationContainerProgressData) {
	csrfToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		csrfToken = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	w.Header().Set("Cache-Control", "no-store")
	data.CSRFToken = csrfToken
	page := h.shellPageData(r)
	page.ActivePage = "applications"
	page.ApplicationDetailsPage = &data
	page.PostgreSQLProgress = postgresProgress
	page.RedisProgress = redisProgress
	if len(applicationProgress) > 0 {
		page.ApplicationContainerProgress = applicationProgress[0]
	}
	page.ServiceDeleteProgress = data.ServiceDeleteProgress
	page.ApplicationDeleteProgress = data.ApplicationDeleteProgress
	h.writeTemplateStatus(w, "application-details.html", page, status)
}

func (h *Handler) writeApplicationDeleteProgressPage(w http.ResponseWriter, r *http.Request, status int, progress *applicationDeleteProgressData) {
	csrfToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		csrfToken = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	w.Header().Set("Cache-Control", "no-store")
	page := h.shellPageData(r)
	page.ApplicationDeleteProgress = progress
	h.writeTemplateStatus(w, "application-details.html", page, status)
}

func (h *Handler) writeServiceDetailsPage(w http.ResponseWriter, r *http.Request, status int, data serviceDetailsPageData) {
	csrfToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		csrfToken = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	w.Header().Set("Cache-Control", "no-store")
	data.CSRFToken = csrfToken
	page := h.shellPageData(r)
	page.ActivePage = "applications"
	page.ServiceDetailsPage = &data
	h.writeTemplateStatus(w, "service-details.html", page, status)
}

func (h *Handler) writePostgreSQLServicePage(w http.ResponseWriter, r *http.Request, status int, data postgresqlServicePageData) {
	csrfToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		csrfToken = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	w.Header().Set("Cache-Control", "no-store")
	data.CSRFToken = csrfToken
	page := h.shellPageData(r)
	page.ActivePage = "applications"
	page.PostgreSQLServicePage = &data
	h.writeTemplateStatus(w, "postgresql-service.html", page, status)
}

func (h *Handler) writeRedisServicePage(w http.ResponseWriter, r *http.Request, status int, data redisServicePageData) {
	csrfToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		csrfToken = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	w.Header().Set("Cache-Control", "no-store")
	data.CSRFToken = csrfToken
	page := h.shellPageData(r)
	page.ActivePage = "applications"
	page.RedisServicePage = &data
	h.writeTemplateStatus(w, "redis-service.html", page, status)
}

func (h *Handler) writeApplicationContainerPage(w http.ResponseWriter, r *http.Request, status int, data applicationContainerPageData) {
	csrfToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		csrfToken = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	w.Header().Set("Cache-Control", "no-store")
	data.CSRFToken = csrfToken
	page := h.shellPageData(r)
	page.ActivePage = "applications"
	page.ApplicationContainerPage = &data
	h.writeTemplateStatus(w, "application-container.html", page, status)
}

func newRedisServicePageData(item application.Application) redisServicePageData {
	return redisServicePageData{
		Application:  item,
		ServiceName:  "redis",
		RedisVersion: "7",
		Port:         "6379",
	}
}

func newApplicationContainerPageData(item application.Application) applicationContainerPageData {
	return applicationContainerPageData{
		Application:       item,
		ServiceName:       "app",
		ImageName:         defaultApplicationContainerImageName("app"),
		UseDockerRegistry: false,
		AutoStart:         false,
	}
}

func (h *Handler) setupProgress(r *http.Request) (*setupProgressData, bool) {
	jobID := r.URL.Query().Get("setup_job")
	if jobID == "" {
		return nil, true
	}
	job := h.setupJobs.get(jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	return &progress, true
}

func newCSRFToken() (string, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	return hex.EncodeToString(token), nil
}

func validCSRFToken(got, want string) bool {
	if !validCSRFTokenFormat(got) || !validCSRFTokenFormat(want) || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func validCSRFTokenFormat(token string) bool {
	if token == "" || len(token) != 64 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

func discoverServerInfo() ServerInfo {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "Unavailable"
	}

	ipAddress := discoverIPAddress()
	if ipAddress == "" {
		ipAddress = "Unavailable"
	}

	return ServerInfo{
		Hostname:  hostname,
		IPAddress: ipAddress,
	}
}

func discoverIPAddress() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	var ipv4Addresses []string
	var ipv6Addresses []string
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}

		interfaceAddresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range interfaceAddresses {
			ip := ipFromAddr(address)
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			if ipv4 := ip.To4(); ipv4 != nil {
				ipv4Addresses = append(ipv4Addresses, ipv4.String())
			} else if ipv6 := ip.To16(); ipv6 != nil {
				ipv6Addresses = append(ipv6Addresses, ipv6.String())
			}
		}
	}

	if len(ipv4Addresses) > 0 {
		sort.Strings(ipv4Addresses)
		return ipv4Addresses[0]
	}

	if len(ipv6Addresses) > 0 {
		sort.Strings(ipv6Addresses)
		return ipv6Addresses[0]
	}

	return ""
}

func ipFromAddr(address net.Addr) net.IP {
	switch address := address.(type) {
	case *net.IPNet:
		return address.IP
	case *net.IPAddr:
		return address.IP
	default:
		return nil
	}
}
