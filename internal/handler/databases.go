package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"redlaunch/internal/application"
)

var errManagedDatabasesNotConfigured = errors.New("managed databases are not configured")

type managedDatabasesService interface {
	GetCluster(context.Context) (application.ManagedDatabaseCluster, error)
	IsEnabled(context.Context) (bool, error)
	EnableCluster(context.Context, application.ManagedDatabaseEnableInput) error
	DisableCluster(context.Context) error
	StartCluster(ctx context.Context, provider string) error
	StopCluster(ctx context.Context, provider string) error
	RestartCluster(ctx context.Context, provider string) error
	GetEngines(context.Context) ([]application.ManagedDatabaseEngine, error)
	GetLogs(ctx context.Context, provider string) (string, bool, error)
	OpenLogs(ctx context.Context, provider string) (io.ReadCloser, error)
	ListDatabases(context.Context) ([]application.ManagedDatabase, error)
	CreateDatabase(context.Context, application.ManagedDatabaseCreateInput) (application.ManagedDatabaseCreation, error)
	CreateDatabaseWithProgress(context.Context, application.ManagedDatabaseCreateInput, func(stage, message string)) (application.ManagedDatabaseCreation, error)
	DropDatabase(context.Context, string) error
	ListUsers(context.Context) ([]application.ManagedDatabaseUserDetail, error)
	GetDatabaseConnections(context.Context, string) ([]application.ManagedDatabaseConnection, error)
	CreateUser(context.Context, application.ManagedDatabaseUserInput) error
	UpdateUserPassword(context.Context, string, string) error
	SetUserPermissions(context.Context, string, []string) error
	DeleteUser(context.Context, string) error
	GetBackupDetails(context.Context, string) (application.BackupDetails, error)
	UpdateBackupSchedule(context.Context, string, application.BackupScheduleInput) error
	RunBackupNow(context.Context, string) (application.Backup, error)
	RestoreBackup(context.Context, string, string) error
	DeleteBackup(context.Context, string, string) error
	OpenBackup(context.Context, string, string) (io.ReadCloser, application.Backup, error)
}

type managedDatabaseAccessChoice struct {
	Name     string
	Provider string
	Checked  bool
}

// databaseEngineOption is one entry of the create form's engine select. Note
// says what creating a database on the engine will do; the browser swaps it in
// when the selection changes.
type databaseEngineOption struct {
	Provider string
	Label    string
	Note     string
	Selected bool
}

// setCreateProvider selects the engine of the inline create form.
func (d *databasesPageData) setCreateProvider(provider string) {
	d.CreateProvider = provider
	d.CreateOptions = d.CreateOptions[:0]
	for _, engine := range d.Engines {
		option := databaseEngineOption{
			Provider: engine.Provider,
			Label:    engine.Name() + " " + engine.Version,
			Note:     managedEngineCreateNote(engine),
			Selected: engine.Provider == provider,
		}
		if option.Selected {
			d.CreateNote = option.Note
		}
		d.CreateOptions = append(d.CreateOptions, option)
	}
}

func managedEngineCreateNote(engine application.ManagedDatabaseEngine) string {
	name := engine.Name() + " " + engine.Version
	switch {
	case !engine.Provisioned:
		return "Redlaunch starts " + name + " when you create its first database."
	case engine.Running:
		return "Uses the running " + name + " container."
	default:
		return "The " + name + " container is not running. Start it from Settings first."
	}
}

// databaseEngineChip is one engine filter of the database inventory.
type databaseEngineChip struct {
	Provider string
	Label    string
	Count    int
	Active   bool
	Href     string
}

type databasesPageData struct {
	Cluster             application.ManagedDatabaseCluster
	Enabled             bool
	Engines             []application.ManagedDatabaseEngine
	EngineFilter        string
	EngineChips         []databaseEngineChip
	LogsEngine          string
	LogEngineChips      []databaseEngineChip
	DialogEngine        string
	Logs                string
	LogsAvailable       bool
	BackupRunning       bool
	Databases           []application.ManagedDatabase
	VisibleDatabases    []application.ManagedDatabase
	DatabaseOwners      []string
	DatabaseQuery       string
	DatabaseOwner       string
	CreateOpen          bool
	CreateProvider      string
	CreateOptions       []databaseEngineOption
	CreateNote          string
	CreateError         string
	DisableOpen         bool
	Users               []application.ManagedDatabaseUserDetail
	ActiveTab           string
	UserQuery           string
	VisibleUsers        []application.ManagedDatabaseUserDetail
	Dialog              string
	DialogError         string
	SelectedUser        *application.ManagedDatabaseUserDetail
	SelectedBackup      *application.Backup
	AccessChoices       []managedDatabaseAccessChoice
	CreateAccessChoices []managedDatabaseAccessChoice
	SelectedDatabase    string
	Backup              *application.BackupDetails
	BackupDatabaseName  string
	CSRFToken           string
	Error               string
	Notice              string
	EnableOpen          bool
	EnableError         string
	Provider            string
	Version             string
	DefaultUser         string
	CreateName          string
	CreateOwner         string
	CreatedCredentials  *managedDatabaseCredentialsData
	Connection          *managedDatabaseConnectionData
	UserName            string
	UserProvider        string
	UserDatabases       string
	Progress            *managedDatabasesProgressData
}

type managedDatabaseCredentialsData struct {
	Username    string
	Password    string
	DownloadURL template.URL
}

type managedDatabaseConnectionData struct {
	Database string
	Options  []application.ManagedDatabaseConnection
	Selected application.ManagedDatabaseConnection
}

func (h *Handler) databasesPage(w http.ResponseWriter, r *http.Request) {
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
	if h.managedDatabases == nil {
		h.writeDatabasesPage(w, r, http.StatusInternalServerError, databasesPageData{Error: "Managed databases are not configured."})
		return
	}
	progress, ok := h.managedDatabasesProgress(r.URL.Query().Get("managed_job"))
	if !ok {
		http.Redirect(w, r, "/databases", http.StatusSeeOther)
		return
	}
	data, err := h.loadDatabasesPageData(r.Context(), r.URL.Query())
	if err != nil {
		h.logger.Error("load managed databases", "error", err)
		h.writeDatabasesPage(w, r, http.StatusInternalServerError, databasesPageData{Error: "The managed databases could not be read."})
		return
	}
	data.Progress = progress
	if jobID := r.URL.Query().Get("created"); jobID != "" && h.managedDatabasesJobs != nil {
		if job := h.managedDatabasesJobs.get(jobID); job != nil {
			if credentials := job.takeCredentials(); credentials != nil {
				data.CreatedCredentials = managedDatabaseCredentials(job.provider, credentials)
			}
		}
	}
	h.writeDatabasesPage(w, r, http.StatusOK, data)
}

func (h *Handler) loadDatabasesPageData(ctx context.Context, query url.Values) (databasesPageData, error) {
	data := databasesPageData{
		Provider:     application.ManagedDatabaseProviderPostgres,
		Version:      application.ManagedDatabaseDefaultVersion,
		UserProvider: application.ManagedDatabaseProviderPostgres,
	}
	cluster, err := h.managedDatabases.GetCluster(ctx)
	if err != nil {
		return data, err
	}
	data.Cluster = cluster
	data.Enabled = cluster.Enabled
	if cluster.Provider != "" {
		data.Provider = cluster.Provider
	}
	if cluster.Version != "" {
		data.Version = cluster.Version
	}
	if cluster.DefaultUser != "" {
		data.DefaultUser = cluster.DefaultUser
	}
	if !cluster.Enabled {
		return data, nil
	}
	// Engine state is best-effort at runtime; keep the page usable when Docker is down.
	engines, err := h.managedDatabases.GetEngines(ctx)
	if err != nil {
		return data, err
	}
	data.Engines = engines
	databases, err := h.managedDatabases.ListDatabases(ctx)
	if err != nil {
		return data, err
	}
	data.Databases = databases
	data.DatabaseQuery = strings.TrimSpace(query.Get("q"))
	data.DatabaseOwner = query.Get("owner")
	data.CreateOpen = query.Get("create") == "1"
	data.DisableOpen = query.Get("disable") == "1"
	if provider, err := application.ValidateManagedDatabaseEngine(query.Get("engine")); err == nil && query.Get("engine") != "" {
		data.EngineFilter = provider
	}
	createProvider := data.EngineFilter
	if createProvider == "" {
		createProvider = application.ManagedDatabaseProviderPostgres
	}
	data.setCreateProvider(createProvider)
	data.EngineChips = managedDatabaseEngineChips(engines, data.EngineFilter, len(databases), func(provider string) string {
		params := url.Values{}
		if provider != "" {
			params.Set("engine", provider)
		}
		if data.CreateOpen {
			params.Set("create", "1")
		}
		if data.DatabaseQuery != "" {
			params.Set("q", data.DatabaseQuery)
		}
		return databasesURL(params)
	})
	owners := make(map[string]bool)
	search := strings.ToLower(data.DatabaseQuery)
	for _, database := range databases {
		if data.EngineFilter != "" && database.Provider != data.EngineFilter {
			continue
		}
		owners[database.Owner] = true
		if strings.Contains(strings.ToLower(database.Name), search) &&
			(data.DatabaseOwner == "" || database.Owner == data.DatabaseOwner) {
			data.VisibleDatabases = append(data.VisibleDatabases, database)
		}
	}
	for owner := range owners {
		data.DatabaseOwners = append(data.DatabaseOwners, owner)
	}
	sort.Strings(data.DatabaseOwners)
	users, err := h.managedDatabases.ListUsers(ctx)
	if err != nil {
		return data, err
	}
	data.Users = users
	data.ActiveTab = managedDatabasesTab(query.Get("tab"))
	data.UserQuery = strings.TrimSpace(query.Get("user_q"))
	userSearch := strings.ToLower(data.UserQuery)
	for i := range users {
		if strings.Contains(strings.ToLower(users[i].User.Username), userSearch) {
			data.VisibleUsers = append(data.VisibleUsers, users[i])
		}
		if users[i].User.Username == query.Get("user") {
			data.SelectedUser = &users[i]
		}
	}
	data.LogsEngine = application.ManagedDatabaseProviderPostgres
	if provider, err := application.ValidateManagedDatabaseEngine(query.Get("log_engine")); err == nil {
		for _, engine := range engines {
			if engine.Provider == provider && engine.Provisioned {
				data.LogsEngine = provider
			}
		}
	}
	data.LogEngineChips = managedDatabaseLogChips(engines, data.LogsEngine)
	if logsProvider, ok := h.managedDatabases.(interface {
		GetLogs(context.Context, string) (string, bool, error)
	}); ok {
		if logs, available, err := logsProvider.GetLogs(ctx, data.LogsEngine); err == nil && available {
			data.Logs = logs
			data.LogsAvailable = true
		}
	}
	switch query.Get("dialog") {
	case "cluster-stop", "cluster-restart":
		data.Dialog = query.Get("dialog")
		data.DialogEngine = application.ManagedDatabaseProviderPostgres
		if provider, err := application.ValidateManagedDatabaseEngine(query.Get("engine")); err == nil {
			data.DialogEngine = provider
		}
		data.ActiveTab = "managed-settings"
	case "user-create":
		data.Dialog = "user-create"
		data.ActiveTab = "managed-users"
	case "user-password", "user-permissions", "user-delete":
		data.ActiveTab = "managed-users"
		if data.SelectedUser != nil {
			data.Dialog = query.Get("dialog")
		} else {
			data.Error = "The database user could not be found."
		}
	}
	for _, database := range databases {
		data.CreateAccessChoices = append(data.CreateAccessChoices, managedDatabaseAccessChoice{Name: database.Name, Provider: database.Provider})
		choice := managedDatabaseAccessChoice{Name: database.Name, Provider: database.Provider}
		if data.SelectedUser != nil {
			// A login only has access within its own engine.
			if database.Provider != data.SelectedUser.User.Provider {
				continue
			}
			for _, name := range data.SelectedUser.Databases {
				if name == database.Name {
					choice.Checked = true
				}
			}
		}
		data.AccessChoices = append(data.AccessChoices, choice)
	}
	selected := strings.TrimSpace(query.Get("database"))
	if selected == "" && len(databases) > 0 {
		selected = databases[0].Name
	}
	if selected != "" {
		data.SelectedDatabase = selected
		if details, err := h.managedDatabases.GetBackupDetails(ctx, selected); err == nil {
			backup := details
			data.Backup = &backup
			data.BackupDatabaseName = selected
		}
		for _, database := range databases {
			if database.Name != selected {
				continue
			}
			for _, engine := range engines {
				if engine.Provider == database.Provider {
					data.BackupRunning = engine.Running
				}
			}
		}
	}
	if query.Get("dialog") == "backup-restore" || query.Get("dialog") == "backup-delete" {
		data.ActiveTab = "managed-backups"
		if data.Backup != nil {
			for i := range data.Backup.Backups {
				if data.Backup.Backups[i].FileName == query.Get("backup_file") {
					data.SelectedBackup = &data.Backup.Backups[i]
					data.Dialog = query.Get("dialog")
					break
				}
			}
		}
		if data.SelectedBackup == nil {
			data.Error = "The backup could not be found."
		}
	}
	if query.Get("dialog") == "database-connection" {
		data.ActiveTab = "managed-databases"
		selected = strings.TrimSpace(query.Get("database"))
		var found bool
		for _, database := range databases {
			if database.Name == selected {
				found = true
				break
			}
		}
		if !found {
			data.Error = "The database could not be found."
		} else if options, err := h.managedDatabases.GetDatabaseConnections(ctx, selected); err != nil {
			data.Error = "The database connection details could not be read."
		} else {
			data.Dialog = "database-connection"
			connection := managedDatabaseConnectionData{Database: selected, Options: options}
			if len(options) > 0 {
				connection.Selected = options[0]
			}
			for _, option := range options {
				if option.Username == query.Get("user") {
					connection.Selected = option
					break
				}
			}
			data.Connection = &connection
		}
	}
	switch query.Get("notice") {
	case "user-created":
		data.Notice = "Database user created."
	case "password-updated":
		data.Notice = "Password updated."
	case "permissions-updated":
		data.Notice = "Database access updated."
	case "user-deleted":
		data.Notice = "Database user deleted."
	case "schedule-saved":
		data.Notice = "Backup schedule saved."
	case "backup-created":
		data.Notice = "Backup completed."
	case "backup-restored":
		data.Notice = "Backup restored."
	case "backup-deleted":
		data.Notice = "Backup deleted."
	case "start", "stop", "restart":
		data.Notice = "Cluster action completed."
	}
	return data, nil
}

func databasesURL(params url.Values) string {
	if len(params) == 0 {
		return "/databases"
	}
	return "/databases?" + params.Encode()
}

// managedDatabaseEngineChips builds the inventory engine filter: every engine
// with its database count, preceded by the unfiltered view.
func managedDatabaseEngineChips(engines []application.ManagedDatabaseEngine, active string, total int, href func(provider string) string) []databaseEngineChip {
	chips := []databaseEngineChip{{Label: "All engines", Count: total, Active: active == "", Href: href("")}}
	for _, engine := range engines {
		chips = append(chips, databaseEngineChip{
			Provider: engine.Provider, Label: engine.Name(), Count: engine.Databases,
			Active: active == engine.Provider, Href: href(engine.Provider),
		})
	}
	return chips
}

// managedDatabaseLogChips lists the engines whose logs can be shown.
func managedDatabaseLogChips(engines []application.ManagedDatabaseEngine, active string) []databaseEngineChip {
	var chips []databaseEngineChip
	for _, engine := range engines {
		if !engine.Provisioned {
			continue
		}
		chips = append(chips, databaseEngineChip{
			Provider: engine.Provider, Label: engine.Name(), Active: active == engine.Provider,
			Href: "/databases?tab=managed-logs&log_engine=" + url.QueryEscape(engine.Provider),
		})
	}
	return chips
}

func (h *Handler) writeDatabasesPage(w http.ResponseWriter, r *http.Request, status int, data databasesPageData) {
	csrfToken := h.setCSRFCookie(w, r)
	data.CSRFToken = csrfToken
	if data.ActiveTab == "" {
		data.ActiveTab = managedDatabasesTab(r.URL.Query().Get("tab"))
	}
	if data.Provider == "" {
		data.Provider = application.ManagedDatabaseProviderPostgres
	}
	if data.Version == "" {
		data.Version = application.ManagedDatabaseDefaultVersion
	}
	w.Header().Set("Cache-Control", "no-store")
	page := h.shellPageData(r)
	page.ActivePage = "databases"
	page.DatabasesPage = &data
	h.writeTemplateStatus(w, "databases.html", page, status)
}

func (h *Handler) managedDatabasesProgress(jobID string) (*managedDatabasesProgressData, bool) {
	if jobID == "" {
		return nil, true
	}
	if h.managedDatabasesJobs == nil {
		return nil, false
	}
	job := h.managedDatabasesJobs.get(jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	progress.StatusURL = "/databases/enable/status?id=" + url.QueryEscape(jobID)
	progress.CloseURL = "/databases"
	if progress.Kind == managedDatabasesJobKindCreate {
		switch {
		case progress.State == managedDatabasesJobStateComplete && job.hasCredentials():
			// The credentials are handed over once, on the next page load.
			progress.CloseURL = "/databases?created=" + url.QueryEscape(jobID)
		case progress.State == managedDatabasesJobStateComplete:
			progress.CloseURL = "/databases?engine=" + url.QueryEscape(progress.Provider)
		case progress.State == managedDatabasesJobStateFailed:
			progress.CloseURL = "/databases?create=1&engine=" + url.QueryEscape(progress.Provider)
		}
	}
	return &progress, true
}

func (h *Handler) enableManagedDatabases(w http.ResponseWriter, r *http.Request) {
	if h.requireDatabasesSetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		h.renderDatabasesEnableError(w, r, "The enable request was invalid.", http.StatusBadRequest, application.ManagedDatabaseEnableInput{})
		return
	}
	if !h.validRequestCSRF(r) {
		h.renderDatabasesEnableError(w, r, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden, application.ManagedDatabaseEnableInput{})
		return
	}
	input := application.ManagedDatabaseEnableInput{
		Provider:    r.Form.Get("provider"),
		Version:     r.Form.Get("version"),
		DefaultUser: r.Form.Get("default_user"),
		Password:    r.Form.Get("password"),
	}
	if _, err := application.ValidateManagedDatabaseEnableInput(input); err != nil {
		h.renderDatabasesEnableError(w, r, managedDatabasesUserMessage(err), http.StatusBadRequest, input)
		return
	}
	job, created, err := h.managedDatabasesJobs.createUnique(managedDatabasesJobKindEnable, application.ManagedDatabaseProviderPostgres, "")
	if err != nil {
		h.logger.Error("create managed databases job", "error", err)
		h.renderDatabasesEnableError(w, r, "The enable operation could not be started.", http.StatusInternalServerError, input)
		return
	}
	if created {
		if err := h.startTrackedJob("managed-databases-enable", func(ctx context.Context) {
			h.runManagedDatabasesJob(ctx, job, input)
		}); err != nil {
			job.fail(err)
			h.logger.Error("admit managed databases job", "error", err)
			h.renderDatabasesEnableError(w, r, "The operation system is busy. Try again shortly.", http.StatusServiceUnavailable, input)
			return
		}
	}
	http.Redirect(w, r, "/databases?managed_job="+url.QueryEscape(job.id), http.StatusSeeOther)
}

func (h *Handler) renderDatabasesEnableError(w http.ResponseWriter, r *http.Request, message string, status int, input application.ManagedDatabaseEnableInput) {
	data, err := h.loadDatabasesPageData(r.Context(), url.Values{})
	if err != nil {
		h.writeDatabasesPage(w, r, status, databasesPageData{Error: message})
		return
	}
	data.EnableOpen = true
	data.EnableError = message
	data.Provider = input.Provider
	if data.Provider == "" {
		data.Provider = application.ManagedDatabaseProviderPostgres
	}
	data.Version = input.Version
	data.DefaultUser = input.DefaultUser
	h.writeDatabasesPage(w, r, status, data)
}

func (h *Handler) managedDatabasesEnableStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "The enable job ID is required.", http.StatusBadRequest)
		return
	}
	progress, ok := h.managedDatabasesProgress(jobID)
	if !ok {
		http.Error(w, "The enable job was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "managed-databases-progress.html", pageData{ManagedDatabasesProgress: progress}, http.StatusOK)
}

func (h *Handler) disableManagedDatabases(w http.ResponseWriter, r *http.Request) {
	if h.requireDatabasesSetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The disable request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	if err := h.managedDatabases.DisableCluster(r.Context()); err != nil {
		h.renderManagedDatabasesActionError(w, r, err, "managed-settings", "")
		return
	}
	http.Redirect(w, r, "/databases", http.StatusSeeOther)
}

func (h *Handler) managedDatabasesClusterAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.requireDatabasesSetup(w, r) {
			return
		}
		if err := parseBoundedForm(w, r); err != nil {
			http.Error(w, "The cluster action request was invalid.", http.StatusBadRequest)
			return
		}
		if !h.validRequestCSRF(r) {
			http.Error(w, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden)
			return
		}
		provider, err := application.ValidateManagedDatabaseEngine(r.Form.Get("engine"))
		if err != nil {
			h.renderManagedDatabasesActionError(w, r, err, "managed-settings", "")
			return
		}
		switch action {
		case "start":
			err = h.managedDatabases.StartCluster(r.Context(), provider)
		case "stop":
			err = h.managedDatabases.StopCluster(r.Context(), provider)
		case "restart":
			err = h.managedDatabases.RestartCluster(r.Context(), provider)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.logger.Error("managed databases cluster action", "action", action, "error", err)
			h.renderManagedDatabasesActionError(w, r, err, "managed-settings", "")
			return
		}
		managedDatabasesRedirect(w, r, "managed-settings", action, "")
	}
}

func (h *Handler) createManagedDatabase(w http.ResponseWriter, r *http.Request) {
	if h.requireDatabasesSetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The create database request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	input := application.ManagedDatabaseCreateInput{
		Provider: r.Form.Get("provider"),
		Name:     r.Form.Get("name"),
		Owner:    r.Form.Get("owner"),
	}
	validated, err := application.ValidateManagedDatabaseCreateInput(input)
	if err != nil {
		h.renderCreateDatabaseError(w, r, input, err)
		return
	}
	// The first database on MariaDB or MySQL starts that engine's container,
	// which outlasts a request, so it runs as a job with progress.
	if h.managedDatabasesJobs != nil && h.managedDatabaseNeedsProvisioning(r.Context(), validated.Provider) {
		h.startManagedDatabaseCreateJob(w, r, input, validated.Provider)
		return
	}
	created, err := h.managedDatabases.CreateDatabase(r.Context(), input)
	if err != nil {
		h.logger.Error("create managed database", "error", err)
		h.renderCreateDatabaseError(w, r, input, err)
		return
	}
	if created.Credentials != nil {
		// Return the one-time credentials directly, never in a URL or persisted flash.
		data, err := h.loadDatabasesPageData(r.Context(), url.Values{"database": {created.Name}})
		if err != nil {
			// A dashboard read failure must not lose newly generated credentials.
			data = databasesPageData{Enabled: true, Error: "The database was created, but the overview could not be refreshed."}
		}
		data.CreatedCredentials = managedDatabaseCredentials(created.Provider, created.Credentials)
		h.writeDatabasesPage(w, r, http.StatusOK, data)
		return
	}
	http.Redirect(w, r, "/databases?"+url.Values{"database": {created.Name}, "engine": {created.Provider}}.Encode(), http.StatusSeeOther)
}

func (h *Handler) managedDatabaseNeedsProvisioning(ctx context.Context, provider string) bool {
	engines, err := h.managedDatabases.GetEngines(ctx)
	if err != nil {
		return false
	}
	for _, engine := range engines {
		if engine.Provider == provider {
			return !engine.Provisioned
		}
	}
	return false
}

func (h *Handler) startManagedDatabaseCreateJob(w http.ResponseWriter, r *http.Request, input application.ManagedDatabaseCreateInput, provider string) {
	job, created, err := h.managedDatabasesJobs.createUnique(managedDatabasesJobKindCreate, provider, input.Name)
	if err != nil {
		h.logger.Error("create managed databases job", "error", err)
		h.renderCreateDatabaseError(w, r, input, errors.New("job unavailable"))
		return
	}
	if created {
		if err := h.startTrackedJob("managed-databases-create", func(ctx context.Context) {
			h.runManagedDatabaseCreateJob(ctx, job, input)
		}); err != nil {
			job.fail(err)
			h.logger.Error("admit managed databases job", "error", err)
			http.Error(w, "The operation system is busy. Try again shortly.", http.StatusServiceUnavailable)
			return
		}
	}
	http.Redirect(w, r, "/databases?managed_job="+url.QueryEscape(job.id), http.StatusSeeOther)
}

// renderCreateDatabaseError shows the inline create form again with the
// submitted values and the failure, so nothing has to be retyped.
func (h *Handler) renderCreateDatabaseError(w http.ResponseWriter, r *http.Request, input application.ManagedDatabaseCreateInput, createErr error) {
	provider, err := application.ValidateManagedDatabaseEngine(input.Provider)
	if err != nil {
		provider = application.ManagedDatabaseProviderPostgres
	}
	data, loadErr := h.loadDatabasesPageData(r.Context(), url.Values{"engine": {provider}, "create": {"1"}})
	if loadErr != nil {
		h.writeDatabasesPage(w, r, managedDatabasesStatus(createErr), databasesPageData{Error: managedDatabasesUserMessage(createErr)})
		return
	}
	data.CreateOpen = true
	data.setCreateProvider(provider)
	data.CreateName = input.Name
	data.CreateOwner = input.Owner
	data.CreateError = managedDatabasesUserMessage(createErr)
	h.writeDatabasesPage(w, r, managedDatabasesStatus(createErr), data)
}

// managedDatabaseCredentials prepares the one-time credentials dialog. The
// download uses the environment variables of the provider's official image.
func managedDatabaseCredentials(provider string, credentials *application.ManagedDatabaseCredentials) *managedDatabaseCredentialsData {
	prefix := "POSTGRES"
	switch provider {
	case application.ManagedDatabaseProviderMariaDB:
		prefix = "MARIADB"
	case application.ManagedDatabaseProviderMySQL:
		prefix = "MYSQL"
	}
	// Names can contain spaces; validated names cannot contain quotes,
	// backslashes or interpolation characters. The generated password is URL-safe.
	env := prefix + "_USER=" + strconv.Quote(credentials.Username) + "\n" + prefix + "_PASSWORD=" + credentials.Password + "\n"
	return &managedDatabaseCredentialsData{
		Username: credentials.Username, Password: credentials.Password,
		// The fixed data URL prefix and base64 encoding cannot contain active content.
		DownloadURL: template.URL("data:text/plain;charset=utf-8;base64," + base64.StdEncoding.EncodeToString([]byte(env))),
	}
}

func (h *Handler) dropManagedDatabase(w http.ResponseWriter, r *http.Request) {
	if h.requireDatabasesSetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The drop database request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	name := r.Form.Get("name")
	if err := h.managedDatabases.DropDatabase(r.Context(), name); err != nil {
		h.logger.Error("drop managed database", "error", err)
		http.Error(w, managedDatabasesUserMessage(err), managedDatabasesStatus(err))
		return
	}
	http.Redirect(w, r, "/databases", http.StatusSeeOther)
}

func (h *Handler) createManagedDatabaseUser(w http.ResponseWriter, r *http.Request) {
	if h.requireDatabasesSetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The create user request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	input := application.ManagedDatabaseUserInput{
		Provider:  r.Form.Get("provider"),
		Username:  r.Form.Get("username"),
		Password:  r.Form.Get("password"),
		Databases: r.Form["databases"],
	}
	if err := h.managedDatabases.CreateUser(r.Context(), input); err != nil {
		h.logger.Error("create managed database user", "error", err)
		h.renderManagedDatabasesActionError(w, r, err, "managed-users", "user-create")
		return
	}
	managedDatabasesRedirect(w, r, "managed-users", "user-created", "")
}

func (h *Handler) updateManagedDatabaseUserPassword(w http.ResponseWriter, r *http.Request) {
	if h.requireDatabasesSetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The password update request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	username := r.Form.Get("username")
	password := r.Form.Get("password")
	if err := h.managedDatabases.UpdateUserPassword(r.Context(), username, password); err != nil {
		h.logger.Error("update managed database password", "error", err)
		h.renderManagedDatabasesActionError(w, r, err, "managed-users", "user-password")
		return
	}
	managedDatabasesRedirect(w, r, "managed-users", "password-updated", "")
}

func (h *Handler) setManagedDatabaseUserPermissions(w http.ResponseWriter, r *http.Request) {
	if h.requireDatabasesSetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The permissions request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	username := r.Form.Get("username")
	if err := h.managedDatabases.SetUserPermissions(r.Context(), username, r.Form["databases"]); err != nil {
		h.logger.Error("set managed database permissions", "error", err)
		h.renderManagedDatabasesActionError(w, r, err, "managed-users", "user-permissions")
		return
	}
	managedDatabasesRedirect(w, r, "managed-users", "permissions-updated", "")
}

func (h *Handler) deleteManagedDatabaseUser(w http.ResponseWriter, r *http.Request) {
	if h.requireDatabasesSetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The delete user request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	username := r.Form.Get("username")
	if err := h.managedDatabases.DeleteUser(r.Context(), username); err != nil {
		h.logger.Error("delete managed database user", "error", err)
		h.renderManagedDatabasesActionError(w, r, err, "managed-users", "user-delete")
		return
	}
	managedDatabasesRedirect(w, r, "managed-users", "user-deleted", "")
}

func (h *Handler) updateManagedBackupSchedule(w http.ResponseWriter, r *http.Request) {
	if h.requireDatabasesSetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The backup schedule request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	database := r.Form.Get("database")
	input, err := managedBackupScheduleInput(r)
	if err != nil {
		h.renderManagedDatabasesActionError(w, r, err, "managed-backups", "")
		return
	}
	if err := h.managedDatabases.UpdateBackupSchedule(r.Context(), database, input); err != nil {
		h.logger.Error("update managed backup schedule", "error", err)
		h.renderManagedDatabasesActionError(w, r, err, "managed-backups", "")
		return
	}
	managedDatabasesRedirect(w, r, "managed-backups", "schedule-saved", database)
}

func (h *Handler) runManagedBackupNow(w http.ResponseWriter, r *http.Request) {
	if h.requireDatabasesSetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The backup request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	database := r.Form.Get("database")
	if _, err := h.managedDatabases.RunBackupNow(r.Context(), database); err != nil {
		h.logger.Error("run managed backup", "error", err)
		h.renderManagedDatabasesActionError(w, r, err, "managed-backups", "")
		return
	}
	managedDatabasesRedirect(w, r, "managed-backups", "backup-created", database)
}

func (h *Handler) restoreManagedBackup(w http.ResponseWriter, r *http.Request) {
	if h.requireDatabasesSetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The restore request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	database := r.Form.Get("database")
	file := r.Form.Get("backup_file")
	if err := h.managedDatabases.RestoreBackup(r.Context(), database, file); err != nil {
		h.logger.Error("restore managed backup", "error", err)
		h.renderManagedDatabasesActionError(w, r, err, "managed-backups", "backup-restore")
		return
	}
	managedDatabasesRedirect(w, r, "managed-backups", "backup-restored", database)
}

func (h *Handler) deleteManagedBackup(w http.ResponseWriter, r *http.Request) {
	if h.requireDatabasesSetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The delete backup request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This databases page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	database := r.Form.Get("database")
	file := r.Form.Get("backup_file")
	if err := h.managedDatabases.DeleteBackup(r.Context(), database, file); err != nil {
		h.logger.Error("delete managed backup", "error", err)
		h.renderManagedDatabasesActionError(w, r, err, "managed-backups", "backup-delete")
		return
	}
	managedDatabasesRedirect(w, r, "managed-backups", "backup-deleted", database)
}

func (h *Handler) downloadManagedBackup(w http.ResponseWriter, r *http.Request) {
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
	if h.managedDatabases == nil {
		http.Error(w, "Managed databases are not configured.", http.StatusInternalServerError)
		return
	}
	database := strings.TrimSpace(r.URL.Query().Get("database"))
	file := strings.TrimSpace(r.URL.Query().Get("file"))
	if database == "" || file == "" {
		http.Error(w, "The backup download request was invalid.", http.StatusBadRequest)
		return
	}
	release, err := h.acquireLogDownload(r.Context())
	if err != nil {
		http.Error(w, "Too many downloads are active. Try again shortly.", http.StatusTooManyRequests)
		return
	}
	defer release()
	stream, backup, err := h.managedDatabases.OpenBackup(r.Context(), database, file)
	if err != nil {
		h.logger.Error("open managed backup", "error", err)
		http.Error(w, managedDatabasesUserMessage(err), managedDatabasesStatus(err))
		return
	}
	defer stream.Close()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/sql")
	w.Header().Set("Content-Disposition", `attachment; filename="`+backup.FileName+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, stream); err != nil {
		h.logger.Error("download managed backup", "error", err)
	}
}

func (h *Handler) managedBackupStatus(w http.ResponseWriter, r *http.Request) {
	database := strings.TrimSpace(r.URL.Query().Get("database"))
	if database == "" {
		http.Error(w, "The database name is required.", http.StatusBadRequest)
		return
	}
	details, err := h.managedDatabases.GetBackupDetails(r.Context(), database)
	if err != nil {
		http.Error(w, managedDatabasesUserMessage(err), managedDatabasesStatus(err))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "managed-databases-backup-status.html", pageData{
		ManagedDatabasesBackup: &managedDatabasesBackupFragment{
			Database: database,
			Details:  details,
		},
	}, http.StatusOK)
}

func (h *Handler) downloadManagedLogs(w http.ResponseWriter, r *http.Request) {
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
	if h.managedDatabases == nil {
		http.Error(w, "Managed databases are not configured.", http.StatusInternalServerError)
		return
	}
	release, err := h.acquireLogDownload(r.Context())
	if err != nil {
		http.Error(w, "Too many log downloads are active. Try again shortly.", http.StatusTooManyRequests)
		return
	}
	defer release()
	provider, err := application.ValidateManagedDatabaseEngine(r.URL.Query().Get("engine"))
	if err != nil {
		http.Error(w, managedDatabasesUserMessage(err), managedDatabasesStatus(err))
		return
	}
	stream, err := h.managedDatabases.OpenLogs(r.Context(), provider)
	if err != nil {
		h.logger.Error("open managed database logs", "error", err)
		http.Error(w, "The database logs could not be read.", http.StatusInternalServerError)
		return
	}
	defer stream.Close()
	h.writeLogDownload(w, stream, "managed-databases-"+provider+"-logs.txt", "managed-databases")
}

func (h *Handler) requireDatabasesSetup(w http.ResponseWriter, r *http.Request) bool {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return true
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return true
	}
	if h.managedDatabases == nil {
		http.Error(w, "Managed databases are not configured.", http.StatusInternalServerError)
		return true
	}
	return false
}

func managedBackupScheduleInput(r *http.Request) (application.BackupScheduleInput, error) {
	enabled := r.Form.Get("enabled") == "on"
	scheduleType := strings.TrimSpace(r.Form.Get("schedule_type"))
	hour, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get("hour")))
	minute, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get("minute")))
	retention, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get("retention_days")))
	input := application.BackupScheduleInput{
		Enabled:       enabled,
		ScheduleType:  scheduleType,
		Hour:          hour,
		Minute:        minute,
		Weekday:       strings.TrimSpace(r.Form.Get("weekday")),
		RetentionDays: retention,
	}
	if !enabled {
		return input, nil
	}
	return application.ValidateBackupScheduleInput(input)
}

func managedDatabasesStatus(err error) int {
	switch {
	case errors.Is(err, application.ErrManagedDatabasesDisabled),
		errors.Is(err, application.ErrManagedDatabaseNotFound),
		errors.Is(err, application.ErrManagedDatabaseUserNotFound),
		errors.Is(err, application.ErrBackupNotFound),
		errors.Is(err, application.ErrBackupScheduleNotFound):
		return http.StatusNotFound
	case errors.Is(err, application.ErrManagedDatabasesAlreadyEnabled),
		errors.Is(err, application.ErrManagedDatabaseAlreadyExists),
		errors.Is(err, application.ErrManagedDatabaseUserAlreadyExists),
		errors.Is(err, application.ErrManagedDatabaseProviderInvalid),
		errors.Is(err, application.ErrManagedDatabaseProviderMismatch),
		errors.Is(err, application.ErrManagedDatabaseUserTooLong),
		errors.Is(err, application.ErrManagedDatabaseUserOwnsDatabase),
		errors.Is(err, application.ErrManagedDatabaseWorkspaceUnsupported),
		errors.Is(err, application.ErrBackupOperationInProgress),
		errors.Is(err, application.ErrBackupScheduleDisabled),
		errors.Is(err, application.ErrManagedDatabaseDefaultUserInUse):
		return http.StatusBadRequest
	case errors.Is(err, application.ErrBackupSchedulerUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, application.ErrBackupServiceNotRunning),
		errors.Is(err, application.ErrDatabaseServiceNotRunning),
		errors.Is(err, application.ErrManagedDatabaseEngineNotProvisioned):
		return http.StatusConflict
	default:
		if userErr := managedDatabasesUserMessage(err); userErr != "The managed database operation failed." {
			return http.StatusBadRequest
		}
		return http.StatusInternalServerError
	}
}

func managedDatabasesUserMessage(err error) string {
	switch {
	case errors.Is(err, errManagedDatabasesNotConfigured):
		return "Managed databases are not configured."
	case errors.Is(err, application.ErrManagedDatabasesDisabled):
		return "Managed databases are not enabled."
	case errors.Is(err, application.ErrManagedDatabasesAlreadyEnabled):
		return "Managed databases are already enabled."
	case errors.Is(err, application.ErrManagedDatabaseProviderInvalid):
		return "Choose Postgres, MariaDB, or MySQL."
	case errors.Is(err, application.ErrManagedDatabaseProviderMismatch):
		return "This user and database belong to different database engines."
	case errors.Is(err, application.ErrManagedDatabaseUserTooLong):
		return "MySQL usernames can be at most 32 characters."
	case errors.Is(err, application.ErrManagedDatabaseUserOwnsDatabase):
		return "This user owns a database. Drop the database first."
	case errors.Is(err, application.ErrManagedDatabaseEngineNotProvisioned):
		return "This database engine has not been started yet."
	case errors.Is(err, application.ErrManagedDatabaseEngineNotReady):
		return "The database engine did not become ready in time. Check its logs and try again."
	case errors.Is(err, application.ErrManagedDatabaseWorkspaceUnsupported):
		return "The SQL workspace is only available for Postgres databases."
	case errors.Is(err, application.ErrManagedDatabaseAlreadyExists):
		return "A database with this name already exists."
	case errors.Is(err, application.ErrManagedDatabaseNotFound):
		return "The database was not found."
	case errors.Is(err, application.ErrManagedDatabaseUserAlreadyExists):
		return "A user with this name already exists."
	case errors.Is(err, application.ErrManagedDatabaseUserNotFound):
		return "The database user was not found."
	case errors.Is(err, application.ErrManagedDatabaseDefaultUserInUse):
		return "The default database user cannot be deleted."
	case errors.Is(err, application.ErrDatabaseServiceNotRunning):
		return "The managed database container is not running."
	case errors.Is(err, application.ErrBackupServiceNotRunning):
		return "The managed database container is not running."
	case errors.Is(err, application.ErrBackupOperationInProgress):
		return "Another backup operation is already running. Try again shortly."
	case errors.Is(err, application.ErrBackupSchedulerUnavailable):
		return "Scheduled backups are unavailable because the systemd controller is not configured."
	case errors.Is(err, application.ErrBackupScheduleDisabled):
		return "Scheduled backups are disabled for this database."
	case errors.Is(err, application.ErrBackupNotFound):
		return "The backup was not found."
	case errors.Is(err, application.ErrBackupFileNameInvalid):
		return "The backup file name is invalid."
	case errors.Is(err, application.ErrBackupFormatUnsupported):
		return "Only plain SQL backups can be restored."
	case errors.Is(err, application.ErrPostgresVersionRequired),
		errors.Is(err, application.ErrPostgresVersionTooLong),
		errors.Is(err, application.ErrPostgresVersionInvalid):
		return "The Postgres version is invalid. Use an official image tag such as 17."
	case errors.Is(err, application.ErrDatabaseNameRequired),
		errors.Is(err, application.ErrDatabaseNameTooLong),
		errors.Is(err, application.ErrDatabaseNameInvalid):
		return "The database name is invalid."
	case errors.Is(err, application.ErrDatabaseUserRequired),
		errors.Is(err, application.ErrDatabaseUserTooLong),
		errors.Is(err, application.ErrDatabaseUserInvalid):
		return "The database user is invalid. Use letters, numbers, spaces, dots, hyphens, and underscores."
	case errors.Is(err, application.ErrDatabasePasswordTooLong),
		errors.Is(err, application.ErrDatabasePasswordInvalid):
		return "The database password is invalid."
	case errors.Is(err, application.ErrBackupScheduleTypeInvalid),
		errors.Is(err, application.ErrBackupHourInvalid),
		errors.Is(err, application.ErrBackupMinuteInvalid),
		errors.Is(err, application.ErrBackupWeekdayInvalid),
		errors.Is(err, application.ErrBackupRetentionInvalid):
		return "The backup schedule is invalid."
	default:
		return "The managed database operation failed."
	}
}

func managedDatabasesErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type managedDatabasesBackupFragment struct {
	Database string
	Details  application.BackupDetails
}

// managedDatabasesBackupDownloadPath returns the download URL for a backup.
func managedDatabasesBackupDownloadPath(database, file string) string {
	return "/databases/backups/download?database=" + url.QueryEscape(database) + "&file=" + url.QueryEscape(file)
}

var _ = time.Now

func managedDatabasesTab(tab string) string {
	switch tab {
	case "managed-users", "managed-backups", "managed-logs", "managed-settings":
		return tab
	default:
		return "managed-databases"
	}
}

func managedDatabasesRedirect(w http.ResponseWriter, r *http.Request, tab, notice, database string) {
	query := url.Values{"tab": {tab}, "notice": {notice}}
	if database != "" {
		query.Set("database", database)
	}
	http.Redirect(w, r, "/databases?"+query.Encode(), http.StatusSeeOther)
}

func (h *Handler) renderManagedDatabasesActionError(w http.ResponseWriter, r *http.Request, actionErr error, tab, dialog string) {
	query := url.Values{"tab": {tab}, "dialog": {dialog}, "user": {r.Form.Get("username")}, "database": {r.Form.Get("database")}, "backup_file": {r.Form.Get("backup_file")}}
	data, err := h.loadDatabasesPageData(r.Context(), query)
	if err != nil {
		h.writeDatabasesPage(w, r, managedDatabasesStatus(actionErr), databasesPageData{ActiveTab: tab, Error: managedDatabasesUserMessage(actionErr)})
		return
	}
	if dialog == "user-create" {
		data.UserName = r.Form.Get("username")
		if provider, err := application.ValidateManagedDatabaseEngine(r.Form.Get("provider")); err == nil {
			data.UserProvider = provider
		}
	}
	choices := data.AccessChoices
	if dialog == "user-create" {
		choices = data.CreateAccessChoices
	}
	if dialog == "user-create" || dialog == "user-permissions" {
		for i := range choices {
			choices[i].Checked = false
			for _, name := range r.Form["databases"] {
				if name == choices[i].Name {
					choices[i].Checked = true
				}
			}
		}
	}
	if tab == "managed-backups" && r.URL.Path == "/databases/backups/schedule" && data.Backup != nil && r.Form.Get("enabled") == "on" {
		schedule := &data.Backup.Schedule
		schedule.Enabled = r.Form.Get("enabled") == "on"
		schedule.ScheduleType = r.Form.Get("schedule_type")
		schedule.Hour, _ = strconv.Atoi(r.Form.Get("hour"))
		schedule.Minute, _ = strconv.Atoi(r.Form.Get("minute"))
		schedule.Weekday = r.Form.Get("weekday")
		schedule.RetentionDays, _ = strconv.Atoi(r.Form.Get("retention_days"))
	}
	if data.Dialog != "" {
		data.DialogError = managedDatabasesUserMessage(actionErr)
	} else {
		data.Error = managedDatabasesUserMessage(actionErr)
	}
	// Passwords are intentionally never copied into the page model.
	h.writeDatabasesPage(w, r, managedDatabasesStatus(actionErr), data)
}
