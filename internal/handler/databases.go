package handler

import (
	"context"
	"errors"
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
	StartCluster(context.Context) error
	StopCluster(context.Context) error
	RestartCluster(context.Context) error
	GetStatus(context.Context) (application.ManagedDatabaseStatus, error)
	GetLogs(context.Context) (string, bool, error)
	OpenLogs(context.Context) (io.ReadCloser, error)
	ListDatabases(context.Context) ([]application.ManagedDatabase, error)
	CreateDatabase(context.Context, application.ManagedDatabaseCreateInput) (application.ManagedDatabase, error)
	DropDatabase(context.Context, string) error
	ListUsers(context.Context) ([]application.ManagedDatabaseUserDetail, error)
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
	Name    string
	Checked bool
}

type databasesPageData struct {
	Cluster             application.ManagedDatabaseCluster
	Enabled             bool
	Status              application.ManagedDatabaseStatus
	Logs                string
	LogsAvailable       bool
	Databases           []application.ManagedDatabase
	VisibleDatabases    []application.ManagedDatabase
	DatabaseOwners      []string
	DatabaseQuery       string
	DatabaseOwner       string
	CreateOpen          bool
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
	UserName            string
	UserDatabases       string
	Progress            *managedDatabasesProgressData
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
	h.writeDatabasesPage(w, r, http.StatusOK, data)
}

func (h *Handler) loadDatabasesPageData(ctx context.Context, query url.Values) (databasesPageData, error) {
	data := databasesPageData{
		Provider: application.ManagedDatabaseProviderPostgres,
		Version:  application.ManagedDatabaseDefaultVersion,
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
	if statusProvider, ok := h.managedDatabases.(interface {
		GetStatus(context.Context) (application.ManagedDatabaseStatus, error)
	}); ok {
		if status, err := statusProvider.GetStatus(ctx); err == nil {
			// Status is best-effort; keep the page usable when Docker is down.
			data.Status = status
		}
	}
	if logsProvider, ok := h.managedDatabases.(interface {
		GetLogs(context.Context) (string, bool, error)
	}); ok {
		if logs, available, err := logsProvider.GetLogs(ctx); err == nil && available {
			data.Logs = logs
			data.LogsAvailable = true
		}
	}
	databases, err := h.managedDatabases.ListDatabases(ctx)
	if err != nil {
		return data, err
	}
	data.Databases = databases
	data.DatabaseQuery = strings.TrimSpace(query.Get("q"))
	data.DatabaseOwner = query.Get("owner")
	data.CreateOpen = query.Get("create") == "1"
	data.DisableOpen = query.Get("disable") == "1"
	owners := make(map[string]bool)
	search := strings.ToLower(data.DatabaseQuery)
	for _, database := range databases {
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
	switch query.Get("dialog") {
	case "cluster-stop", "cluster-restart":
		data.Dialog = query.Get("dialog")
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
		choice := managedDatabaseAccessChoice{Name: database.Name}
		if data.SelectedUser != nil {
			for _, name := range data.SelectedUser.Databases {
				if name == database.Name {
					choice.Checked = true
				}
			}
		}
		data.AccessChoices = append(data.AccessChoices, choice)
		data.CreateAccessChoices = append(data.CreateAccessChoices, managedDatabaseAccessChoice{Name: database.Name})
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
	job, created, err := h.managedDatabasesJobs.createUnique()
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
		var err error
		switch action {
		case "start":
			err = h.managedDatabases.StartCluster(r.Context())
		case "stop":
			err = h.managedDatabases.StopCluster(r.Context())
		case "restart":
			err = h.managedDatabases.RestartCluster(r.Context())
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
		Name:  r.Form.Get("name"),
		Owner: r.Form.Get("owner"),
	}
	if _, err := h.managedDatabases.CreateDatabase(r.Context(), input); err != nil {
		h.logger.Error("create managed database", "error", err)
		http.Error(w, managedDatabasesUserMessage(err), managedDatabasesStatus(err))
		return
	}
	http.Redirect(w, r, "/databases?database="+url.QueryEscape(strings.TrimSpace(input.Name)), http.StatusSeeOther)
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
	stream, err := h.managedDatabases.OpenLogs(r.Context())
	if err != nil {
		h.logger.Error("open managed database logs", "error", err)
		http.Error(w, "The database logs could not be read.", http.StatusInternalServerError)
		return
	}
	defer stream.Close()
	h.writeLogDownload(w, stream, "managed-databases-logs.txt", "managed-databases")
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
		errors.Is(err, application.ErrBackupOperationInProgress),
		errors.Is(err, application.ErrBackupScheduleDisabled),
		errors.Is(err, application.ErrManagedDatabaseDefaultUserInUse):
		return http.StatusBadRequest
	case errors.Is(err, application.ErrBackupSchedulerUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, application.ErrBackupServiceNotRunning),
		errors.Is(err, application.ErrDatabaseServiceNotRunning):
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
		return "Only Postgres is supported for now."
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
		return "The database user is invalid. Use letters, numbers, and underscores, starting with a letter or underscore."
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
