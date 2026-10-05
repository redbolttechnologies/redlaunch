package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type fakeManagedDatabases struct {
	cluster          application.ManagedDatabaseCluster
	databases        []application.ManagedDatabase
	users            []application.ManagedDatabaseUserDetail
	backup           application.BackupDetails
	enableInput      application.ManagedDatabaseEnableInput
	enableCalls      int
	createdDB        string
	droppedDB        string
	createdUser      string
	deletedUser      string
	passwordUser     string
	permsUser        string
	status           *application.ManagedDatabaseStatus
	userErr          error
	backupErr        error
	backupDetailsErr error
	grants           []string
	backupAction     string
	backupDatabase   string
	backupFile       string
}

func (f *fakeManagedDatabases) GetCluster(context.Context) (application.ManagedDatabaseCluster, error) {
	return f.cluster, nil
}
func (f *fakeManagedDatabases) IsEnabled(context.Context) (bool, error) {
	return f.cluster.Enabled, nil
}
func (f *fakeManagedDatabases) EnableCluster(_ context.Context, input application.ManagedDatabaseEnableInput) error {
	f.enableCalls++
	f.enableInput = input
	f.cluster = application.ManagedDatabaseCluster{Enabled: true, Provider: input.Provider, Version: input.Version, DefaultUser: input.DefaultUser}
	return nil
}
func (f *fakeManagedDatabases) DisableCluster(context.Context) error {
	f.cluster.Enabled = false
	return nil
}
func (f *fakeManagedDatabases) StartCluster(context.Context) error   { return nil }
func (f *fakeManagedDatabases) StopCluster(context.Context) error    { return nil }
func (f *fakeManagedDatabases) RestartCluster(context.Context) error { return nil }
func (f *fakeManagedDatabases) GetStatus(context.Context) (application.ManagedDatabaseStatus, error) {
	if f.status != nil {
		return *f.status, nil
	}
	return application.ManagedDatabaseStatus{Running: true, Status: "running", ContainerName: "redbolt-databases", ImageName: "postgres:17"}, nil
}
func (f *fakeManagedDatabases) GetLogs(context.Context) (string, bool, error) {
	return "log", true, nil
}
func (f *fakeManagedDatabases) OpenLogs(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("log")), nil
}
func (f *fakeManagedDatabases) ListDatabases(context.Context) ([]application.ManagedDatabase, error) {
	return f.databases, nil
}
func (f *fakeManagedDatabases) CreateDatabase(_ context.Context, input application.ManagedDatabaseCreateInput) (application.ManagedDatabase, error) {
	name, err := application.ValidateManagedDatabaseName(input.Name)
	if err != nil {
		return application.ManagedDatabase{}, err
	}
	f.createdDB = name
	return application.ManagedDatabase{Name: name, Owner: "redlaunch"}, nil
}
func (f *fakeManagedDatabases) DropDatabase(_ context.Context, name string) error {
	f.droppedDB = name
	return nil
}
func (f *fakeManagedDatabases) ListUsers(context.Context) ([]application.ManagedDatabaseUserDetail, error) {
	return f.users, nil
}
func (f *fakeManagedDatabases) CreateUser(_ context.Context, input application.ManagedDatabaseUserInput) error {
	if f.userErr != nil {
		return f.userErr
	}
	validated, err := application.ValidateManagedDatabaseUserInput(input)
	if err != nil {
		return err
	}
	f.createdUser = validated.Username
	return nil
}
func (f *fakeManagedDatabases) UpdateUserPassword(_ context.Context, username, _ string) error {
	f.passwordUser = username
	return f.userErr
}
func (f *fakeManagedDatabases) SetUserPermissions(_ context.Context, username string, databases []string) error {
	f.permsUser = username
	f.grants = databases
	return f.userErr
}
func (f *fakeManagedDatabases) DeleteUser(_ context.Context, username string) error {
	f.deletedUser = username
	return f.userErr
}
func (f *fakeManagedDatabases) GetBackupDetails(_ context.Context, name string) (application.BackupDetails, error) {
	return f.backup, f.backupDetailsErr
}
func (f *fakeManagedDatabases) UpdateBackupSchedule(context.Context, string, application.BackupScheduleInput) error {
	return f.backupErr
}
func (f *fakeManagedDatabases) RunBackupNow(_ context.Context, name string) (application.Backup, error) {
	f.backupAction = "run"
	f.backupDatabase = name
	return application.Backup{FileName: "backup-20240102-030405.000000000Z.sql"}, f.backupErr
}
func (f *fakeManagedDatabases) RestoreBackup(_ context.Context, name, file string) error {
	f.backupAction = "restore"
	f.backupDatabase = name
	f.backupFile = file
	return f.backupErr
}
func (f *fakeManagedDatabases) DeleteBackup(_ context.Context, name, file string) error {
	f.backupAction = "delete"
	f.backupDatabase = name
	f.backupFile = file
	return f.backupErr
}
func (f *fakeManagedDatabases) OpenBackup(_ context.Context, _, file string) (io.ReadCloser, application.Backup, error) {
	return io.NopCloser(strings.NewReader("dump")), application.Backup{FileName: file}, nil
}

func newDatabasesTestHandler(t *testing.T, fake *fakeManagedDatabases) *Handler {
	t.Helper()
	h, err := NewForTests(nil, fakeSetupManagerForDatabases(), fake)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func fakeSetupManagerForDatabases() setupManager {
	return &fakeSetup{ready: true}
}

type fakeSetup struct{ ready bool }

func (f *fakeSetup) NeedsSetup() (bool, error)               { return !f.ready, nil }
func (f *fakeSetup) Setup(context.Context, bool, bool) error { return nil }

func databasesRequest(t *testing.T, h *Handler, method, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rr := httptest.NewRecorder()
	// Bypass auth/CSRF wrappers by calling the handler method through Routes
	// would require login; instead call the method via the mux with a
	// test-only handler that skips auth by using NewForTests defaults.
	// NewForTests leaves authentication nil, and withAuthentication allows
	// unauthenticated access in tests.
	h.Routes().ServeHTTP(rr, req)
	return rr
}

func TestDatabasesDisabledShowsEnableSwitch(t *testing.T) {
	fake := &fakeManagedDatabases{}
	h := newDatabasesTestHandler(t, fake)
	rr := databasesRequest(t, h, "GET", "/databases", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Enable managed databases") {
		t.Fatalf("disabled page missing switch, body excerpt: %.200s", body)
	}
	if !strings.Contains(body, `href="/databases"`) {
		t.Fatalf("nav missing databases link")
	}
	if !strings.Contains(body, `<img src="/static/managed-databases.svg" alt="" width="320" height="300">`) {
		t.Fatal("disabled page missing the decorative database illustration")
	}
}

func TestDatabasesVisualAssetsServed(t *testing.T) {
	h := newDatabasesTestHandler(t, &fakeManagedDatabases{})
	for _, asset := range []struct {
		path   string
		marker string
	}{
		{"/static/managed-databases.svg", `viewBox="0 0 320 300"`},
		{"/static/InterVariable.woff2", "wOF2"},
		{"/static/Inter-LICENSE.txt", "SIL OPEN FONT LICENSE"},
	} {
		t.Run(asset.path, func(t *testing.T) {
			rr := databasesRequest(t, h, http.MethodGet, asset.path, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("asset status = %d, want 200", rr.Code)
			}
			if !strings.Contains(rr.Body.String(), asset.marker) {
				t.Fatal("asset response missing expected format marker")
			}
		})
	}
}

func TestDatabasesEnabledShowsDashboard(t *testing.T) {
	fake := &fakeManagedDatabases{
		cluster:   application.ManagedDatabaseCluster{Enabled: true, Provider: "postgres", Version: "17", DefaultUser: "redlaunch"},
		databases: []application.ManagedDatabase{{Name: "app", Owner: "redlaunch"}},
		users: []application.ManagedDatabaseUserDetail{{
			User:      application.ManagedDatabaseUser{Username: "app"},
			Databases: []string{"app"},
		}},
	}
	h := newDatabasesTestHandler(t, fake)
	rr := databasesRequest(t, h, "GET", "/databases", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"Databases", "Users", "Backups", "Logs", "Settings"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard missing %q", want)
		}
	}
}

func TestDatabasesNavBelowApplications(t *testing.T) {
	fake := &fakeManagedDatabases{}
	h := newDatabasesTestHandler(t, fake)
	rr := databasesRequest(t, h, "GET", "/databases", nil)
	body := rr.Body.String()
	apps := strings.Index(body, `href="/applications"`)
	dbs := strings.Index(body, `href="/databases"`)
	proxy := strings.Index(body, `href="/proxy"`)
	if apps < 0 || dbs < 0 || proxy < 0 {
		t.Fatal("nav links missing")
	}
	if !(apps < dbs && dbs < proxy) {
		t.Fatalf("databases nav order wrong: applications=%d databases=%d proxy=%d", apps, dbs, proxy)
	}
}

func TestDatabasesOverviewFiltersKeepTotals(t *testing.T) {
	fake := &fakeManagedDatabases{
		cluster: application.ManagedDatabaseCluster{Enabled: true, Version: "17"},
		databases: []application.ManagedDatabase{
			{Name: "app_production", Owner: "admin", CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)},
			{Name: "app_staging", Owner: "developer"},
			{Name: "analytics", Owner: "admin"},
		},
	}
	h := newDatabasesTestHandler(t, fake)
	for _, test := range []struct {
		query string
		names []string
	}{
		{"", []string{"app_production", "app_staging", "analytics"}},
		{"?q=APP", []string{"app_production", "app_staging"}},
		{"?owner=admin", []string{"app_production", "analytics"}},
		{"?q=app&owner=admin", []string{"app_production"}},
		{"?q=missing", nil},
	} {
		t.Run(test.query, func(t *testing.T) {
			data, err := h.loadDatabasesPageData(context.Background(), mustParseDatabaseQuery(t, test.query))
			if err != nil {
				t.Fatal(err)
			}
			if len(data.Databases) != 3 {
				t.Fatal("filter changed overview total")
			}
			if len(data.VisibleDatabases) != len(test.names) {
				t.Fatalf("filtered databases = %v", data.VisibleDatabases)
			}
			for i, name := range test.names {
				if data.VisibleDatabases[i].Name != name {
					t.Fatalf("filtered database = %q, want %q", data.VisibleDatabases[i].Name, name)
				}
			}
			if strings.Join(data.DatabaseOwners, ",") != "admin,developer" {
				t.Fatalf("owners = %v", data.DatabaseOwners)
			}
		})
	}
	body := databasesRequest(t, h, "GET", "/databases?q=missing", nil).Body.String()
	if !strings.Contains(body, "No databases match your filters.") {
		t.Fatal("missing filtered empty state")
	}
	body = databasesRequest(t, h, "GET", "/databases", nil).Body.String()
	for _, want := range []string{"Users &amp; access", "Postgres 17", "Oct 01, 2026", `datetime="2026-10-01T12:00:00Z"`, `href="/databases?tab=managed-backups&amp;database=app_production"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("overview missing %q", want)
		}
	}
	if strings.Contains(body, "Connection details") || strings.Contains(body, "Healthy") {
		t.Fatal("overview contains unsupported connection or health widget")
	}
}

func mustParseDatabaseQuery(t *testing.T, query string) url.Values {
	t.Helper()
	values, err := url.ParseQuery(strings.TrimPrefix(query, "?"))
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func TestDatabasesOverviewEmptyAndUnavailable(t *testing.T) {
	fake := &fakeManagedDatabases{
		cluster: application.ManagedDatabaseCluster{Enabled: true, Version: "17"},
		status:  &application.ManagedDatabaseStatus{},
	}
	body := databasesRequest(t, newDatabasesTestHandler(t, fake), "GET", "/databases", nil).Body.String()
	for _, want := range []string{"No managed databases yet.", "No managed users yet.", "Unavailable", "Check cluster settings and logs"} {
		if !strings.Contains(body, want) {
			t.Fatalf("empty overview missing %q", want)
		}
	}
	if strings.Contains(body, "running and ready to use") {
		t.Fatal("unavailable cluster advertised as ready")
	}
}

func TestDatabasesOverviewEscapesFiltersAndResourceNames(t *testing.T) {
	value := `<script>alert("xss")</script>`
	fake := &fakeManagedDatabases{
		cluster:   application.ManagedDatabaseCluster{Enabled: true, Version: "17"},
		databases: []application.ManagedDatabase{{Name: value, Owner: value}},
		users:     []application.ManagedDatabaseUserDetail{{User: application.ManagedDatabaseUser{Username: value}}},
	}
	h := newDatabasesTestHandler(t, fake)
	for _, target := range []string{"/databases", "/databases?q=" + url.QueryEscape(value)} {
		body := databasesRequest(t, h, "GET", target, nil).Body.String()
		if strings.Contains(body, value) {
			t.Fatal("unescaped user input in database overview")
		}
		if !strings.Contains(body, "&lt;script&gt;") {
			t.Fatal("missing escaped input")
		}
	}
}

func TestDatabasesOverviewDialogsHaveHTTPFallbacks(t *testing.T) {
	h := newDatabasesTestHandler(t, &fakeManagedDatabases{cluster: application.ManagedDatabaseCluster{Enabled: true}})
	for _, test := range []struct{ query, id, action string }{
		{"create", "managed-database-create-dialog", "/databases/create"},
		{"disable", "managed-database-disable-dialog", "/databases/disable"},
	} {
		body := databasesRequest(t, h, "GET", "/databases?"+test.query+"=1", nil).Body.String()
		start := strings.Index(body, `<dialog class="application-dialog" id="`+test.id+`"`)
		if start < 0 {
			t.Fatal("missing dialog")
		}
		end := strings.Index(body[start:], "</dialog>")
		dialog := body[start : start+end]
		if !strings.Contains(dialog, " open>") || !strings.Contains(dialog, `method="post" action="`+test.action+`"`) || !strings.Contains(dialog, `name="csrf_token"`) || !strings.Contains(dialog, `data-managed-dialog-close`) {
			t.Fatalf("dialog %s missing fallback, POST form, CSRF or cancel", test.id)
		}
	}
}

func TestDatabasesOverviewRecognizesStoppedContainer(t *testing.T) {
	for _, status := range []string{"stopped", "exited", "Exited (0) 2 minutes ago"} {
		t.Run(status, func(t *testing.T) {
			fake := &fakeManagedDatabases{
				cluster: application.ManagedDatabaseCluster{Enabled: true},
				status:  &application.ManagedDatabaseStatus{Status: status},
			}
			body := databasesRequest(t, newDatabasesTestHandler(t, fake), "GET", "/databases", nil).Body.String()
			if !strings.Contains(body, `<p class="databases-stat-value">Stopped</p>`) {
				t.Fatal("stopped container missing accurate overview status")
			}
		})
	}
}

func newDatabasesTabsFixture() *fakeManagedDatabases {
	return &fakeManagedDatabases{
		cluster:   application.ManagedDatabaseCluster{Enabled: true, Provider: "postgres", Version: "17", DefaultUser: "admin"},
		databases: []application.ManagedDatabase{{Name: "production", Owner: "admin"}, {Name: "analytics", Owner: "reporter"}},
		users:     []application.ManagedDatabaseUserDetail{{User: application.ManagedDatabaseUser{Username: "admin"}}, {User: application.ManagedDatabaseUser{Username: "reporter"}, Databases: []string{"analytics"}}},
		backup:    application.BackupDetails{Schedule: application.BackupSchedule{ScheduleType: "daily", Hour: 2, RetentionDays: 7}, Backups: []application.Backup{{FileName: "backup-20261004-020000.sql", CreatedAt: time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC), SizeBytes: 4096}}},
	}
}

func databasesPostWithCSRF(h *Handler, target string, form url.Values) *httptest.ResponseRecorder {
	form.Set("csrf_token", h.csrfToken)
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: h.csrfToken})
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)
	return rr
}

func TestDatabasesTabsRenderSelectionWithoutJavaScript(t *testing.T) {
	h := newDatabasesTestHandler(t, newDatabasesTabsFixture())
	for _, tab := range []string{"managed-users", "managed-backups", "managed-settings"} {
		t.Run(tab, func(t *testing.T) {
			body := databasesRequest(t, h, "GET", "/databases?tab="+tab, nil).Body.String()
			marker := `id="` + tab + `-panel" role="tabpanel" aria-labelledby="` + tab + `-tab" tabindex="0">`
			if !strings.Contains(body, marker) {
				t.Fatal("requested tab is not visible in server HTML")
			}
			if !strings.Contains(body, `data-application-initial-tab="`+tab+`"`) {
				t.Fatal("missing initial selection for tab script")
			}
		})
	}
	body := databasesRequest(t, h, "GET", "/databases?tab=unknown", nil).Body.String()
	if !strings.Contains(body, `data-application-initial-tab="managed-databases"`) {
		t.Fatal("unknown tab did not fall back to overview")
	}
}

func TestDatabasesUserSearchAndAccessSelection(t *testing.T) {
	h := newDatabasesTestHandler(t, newDatabasesTabsFixture())
	data, err := h.loadDatabasesPageData(context.Background(), url.Values{"tab": {"managed-users"}, "user_q": {"REPORT"}, "user": {"reporter"}, "dialog": {"user-permissions"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.VisibleUsers) != 1 || data.VisibleUsers[0].User.Username != "reporter" || len(data.Users) != 2 {
		t.Fatal("user search changed totals or missed case-insensitive match")
	}
	if data.AccessChoices[0].Checked || !data.AccessChoices[1].Checked {
		t.Fatal("access dialog does not reflect user's database grants")
	}
	for _, choice := range data.CreateAccessChoices {
		if choice.Checked {
			t.Fatal("new user inherited another user's grants")
		}
	}
	body := databasesRequest(t, h, "GET", "/databases?tab=managed-users", nil).Body.String()
	if strings.Contains(body, `aria-label="Delete user admin"`) {
		t.Fatal("default user has a delete action")
	}
	if !strings.Contains(body, `aria-label="Delete user reporter"`) {
		t.Fatal("ordinary user missing delete action")
	}
}

func TestDatabasesActionDialogsOnlySelectKnownResources(t *testing.T) {
	fake := newDatabasesTabsFixture()
	h := newDatabasesTestHandler(t, fake)
	for _, test := range []struct{ query, id, post string }{
		{"dialog=user-password&user=reporter", "managed-user-action-dialog", "/databases/users/password"},
		{"dialog=user-permissions&user=reporter", "managed-user-action-dialog", "/databases/users/permissions"},
		{"dialog=user-delete&user=reporter", "managed-user-action-dialog", "/databases/users/delete"},
		{"dialog=backup-restore&database=production&backup_file=backup-20261004-020000.sql", "managed-backup-action-dialog", "/databases/backups/restore"},
		{"dialog=backup-delete&database=production&backup_file=backup-20261004-020000.sql", "managed-backup-action-dialog", "/databases/backups/delete"},
		{"dialog=cluster-stop", "managed-cluster-action-dialog", "/databases/stop"},
		{"dialog=cluster-restart", "managed-cluster-action-dialog", "/databases/restart"},
	} {
		t.Run(test.query, func(t *testing.T) {
			body := databasesRequest(t, h, "GET", "/databases?"+test.query, nil).Body.String()
			start := strings.Index(body, `<dialog class="application-dialog" id="`+test.id+`"`)
			if start < 0 {
				t.Fatal("confirmation dialog missing")
			}
			end := strings.Index(body[start:], "</dialog>")
			dialog := body[start : start+end]
			if !strings.Contains(dialog, " open>") || !strings.Contains(dialog, `action="`+test.post+`"`) || !strings.Contains(dialog, `name="csrf_token"`) {
				t.Fatal("missing native dialog, confirmed POST action or CSRF token")
			}
		})
	}
	for _, query := range []string{"dialog=user-delete&user=missing", "dialog=backup-delete&backup_file=missing.sql", "dialog=unsupported"} {
		body := databasesRequest(t, h, "GET", "/databases?"+query, nil).Body.String()
		if strings.Contains(body, `id="managed-user-action-dialog"`) || strings.Contains(body, `id="managed-backup-action-dialog"`) {
			t.Fatal("action dialog selected an unknown resource")
		}
	}
	if fake.deletedUser != "" || fake.passwordUser != "" || fake.backupAction != "" {
		t.Fatal("GET confirmation mutated a resource")
	}
}

func TestDatabasesActionsStayOnOwningTab(t *testing.T) {
	for _, test := range []struct {
		target, tab, notice string
		form                url.Values
	}{
		{"/databases/users/create", "managed-users", "user-created", url.Values{"username": {"new_user"}, "password": {"disposable-test-password"}, "databases": {"production", "analytics"}}},
		{"/databases/users/password", "managed-users", "password-updated", url.Values{"username": {"reporter"}, "password": {"disposable-test-password"}}},
		{"/databases/users/permissions", "managed-users", "permissions-updated", url.Values{"username": {"reporter"}, "databases": {"production", "analytics"}}},
		{"/databases/users/delete", "managed-users", "user-deleted", url.Values{"username": {"reporter"}}},
		{"/databases/backups/schedule", "managed-backups", "schedule-saved", url.Values{"database": {"production"}, "enabled": {"on"}, "schedule_type": {"weekly"}, "weekday": {"monday"}, "hour": {"2"}, "minute": {"15"}, "retention_days": {"7"}}},
		{"/databases/backups/run", "managed-backups", "backup-created", url.Values{"database": {"production"}}},
		{"/databases/backups/restore", "managed-backups", "backup-restored", url.Values{"database": {"production"}, "backup_file": {"backup-20261004-020000.sql"}}},
		{"/databases/backups/delete", "managed-backups", "backup-deleted", url.Values{"database": {"production"}, "backup_file": {"backup-20261004-020000.sql"}}},
		{"/databases/start", "managed-settings", "start", url.Values{}},
		{"/databases/stop", "managed-settings", "stop", url.Values{}},
		{"/databases/restart", "managed-settings", "restart", url.Values{}},
	} {
		t.Run(test.target, func(t *testing.T) {
			fake := newDatabasesTabsFixture()
			h := newDatabasesTestHandler(t, fake)
			rr := databasesPostWithCSRF(h, test.target, test.form)
			if rr.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", rr.Code)
			}
			location, err := url.Parse(rr.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			if location.Query().Get("tab") != test.tab || location.Query().Get("notice") != test.notice || location.Query().Get("database") != test.form.Get("database") {
				t.Fatalf("incorrect redirect: %s", location)
			}
			if test.target == "/databases/users/permissions" && strings.Join(fake.grants, ",") != "production,analytics" {
				t.Fatal("checkbox values lost during access update")
			}
			if test.target == "/databases/backups/restore" && (fake.backupFile != test.form.Get("backup_file") || fake.backupDatabase != "production") {
				t.Fatal("restore targeted the wrong backup")
			}
		})
	}
}

func TestDatabasesUserErrorKeepsDialogWithoutPassword(t *testing.T) {
	fake := newDatabasesTabsFixture()
	fake.userErr = application.ErrManagedDatabaseUserAlreadyExists
	h := newDatabasesTestHandler(t, fake)
	password := "never-echo-this-test-password"
	rr := databasesPostWithCSRF(h, "/databases/users/create", url.Values{"username": {"reporter"}, "password": {password}, "databases": {"analytics"}})
	body := rr.Body.String()
	if rr.Code != http.StatusBadRequest || !strings.Contains(body, "A user with this name already exists.") || !strings.Contains(body, `data-application-initial-tab="managed-users"`) || !strings.Contains(body, `data-managed-dialog open`) {
		t.Fatal("validation error lost the user dialog")
	}
	if strings.Contains(body, password) {
		t.Fatal("password echoed after validation error")
	}
	if !strings.Contains(body, `name="databases" value="analytics" checked`) {
		t.Fatal("selected access lost after creation error")
	}
}

func TestDatabasesBackupErrorKeepsScheduleAndUnavailableState(t *testing.T) {
	fake := newDatabasesTabsFixture()
	h := newDatabasesTestHandler(t, fake)
	rr := databasesPostWithCSRF(h, "/databases/backups/schedule", url.Values{"database": {"production"}, "enabled": {"on"}, "schedule_type": {"weekly"}, "weekday": {"monday"}, "hour": {"4"}, "minute": {"15"}, "retention_days": {"0"}})
	body := rr.Body.String()
	if rr.Code != http.StatusBadRequest || !strings.Contains(body, "The backup schedule is invalid.") || !strings.Contains(body, `name="hour" type="number" min="0" max="23" value="4"`) || !strings.Contains(body, `value="monday" selected`) {
		t.Fatal("invalid schedule lost entered values or error")
	}
	fake.backupDetailsErr = application.ErrBackupScheduleNotFound
	body = databasesRequest(t, h, "GET", "/databases?tab=managed-backups&database=production", nil).Body.String()
	if !strings.Contains(body, "Backup details are unavailable") {
		t.Fatal("missing unavailable backup state")
	}
}

func TestDatabasesNewFormsRejectMissingCSRF(t *testing.T) {
	fake := newDatabasesTabsFixture()
	h := newDatabasesTestHandler(t, fake)
	for _, target := range []string{"/databases/users/create", "/databases/users/permissions", "/databases/users/password", "/databases/backups/restore", "/databases/backups/delete", "/databases/stop"} {
		rr := databasesRequest(t, h, "POST", target, url.Values{"username": {"reporter"}, "database": {"production"}, "backup_file": {"backup-20261004-020000.sql"}})
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403", target, rr.Code)
		}
	}
	if fake.createdUser != "" || fake.permsUser != "" || fake.passwordUser != "" || fake.backupAction != "" {
		t.Fatal("unprotected form mutated resources")
	}
}

func TestDatabasesPasswordAndBackupFailuresKeepSafeDialogContext(t *testing.T) {
	fake := newDatabasesTabsFixture()
	fake.userErr = application.ErrDatabaseServiceNotRunning
	fake.backupErr = application.ErrBackupServiceNotRunning
	h := newDatabasesTestHandler(t, fake)
	rr := databasesPostWithCSRF(h, "/databases/users/password", url.Values{"username": {"reporter"}, "password": {"never-echo-password"}})
	body := rr.Body.String()
	if rr.Code != http.StatusConflict || strings.Contains(body, "never-echo-password") || !strings.Contains(body, `id="managed-user-action-dialog"`) || !strings.Contains(body, `name="username" value="reporter"`) {
		t.Fatal("password error lost safe dialog context")
	}
	rr = databasesPostWithCSRF(h, "/databases/backups/restore", url.Values{"database": {"production"}, "backup_file": {"backup-20261004-020000.sql"}})
	body = rr.Body.String()
	if rr.Code != http.StatusConflict || !strings.Contains(body, `id="managed-backup-action-dialog"`) || !strings.Contains(body, `name="backup_file" value="backup-20261004-020000.sql"`) {
		t.Fatal("backup failure lost selected restore dialog")
	}
}

func TestDatabasesDialogEscapesUserAndBackupNames(t *testing.T) {
	unsafe := `<img src=x onerror=alert(1)>`
	fake := newDatabasesTabsFixture()
	fake.users[1].User.Username = unsafe
	fake.backup.Backups[0].FileName = unsafe
	h := newDatabasesTestHandler(t, fake)
	for _, query := range []url.Values{
		{"dialog": {"user-password"}, "user": {unsafe}},
		{"dialog": {"backup-delete"}, "database": {"production"}, "backup_file": {unsafe}},
	} {
		body := databasesRequest(t, h, "GET", "/databases?"+query.Encode(), nil).Body.String()
		if strings.Contains(body, unsafe) || !strings.Contains(body, "&lt;img") {
			t.Fatal("unescaped name in action dialog")
		}
	}
}
