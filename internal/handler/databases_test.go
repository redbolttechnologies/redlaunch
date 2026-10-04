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
	cluster      application.ManagedDatabaseCluster
	databases    []application.ManagedDatabase
	users        []application.ManagedDatabaseUserDetail
	backup       application.BackupDetails
	enableInput  application.ManagedDatabaseEnableInput
	enableCalls  int
	createdDB    string
	droppedDB    string
	createdUser  string
	deletedUser  string
	passwordUser string
	permsUser    string
	status       *application.ManagedDatabaseStatus
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
	validated, err := application.ValidateManagedDatabaseUserInput(input)
	if err != nil {
		return err
	}
	f.createdUser = validated.Username
	return nil
}
func (f *fakeManagedDatabases) UpdateUserPassword(_ context.Context, username, _ string) error {
	f.passwordUser = username
	return nil
}
func (f *fakeManagedDatabases) SetUserPermissions(_ context.Context, username string, _ []string) error {
	f.permsUser = username
	return nil
}
func (f *fakeManagedDatabases) DeleteUser(_ context.Context, username string) error {
	f.deletedUser = username
	return nil
}
func (f *fakeManagedDatabases) GetBackupDetails(_ context.Context, name string) (application.BackupDetails, error) {
	return f.backup, nil
}
func (f *fakeManagedDatabases) UpdateBackupSchedule(context.Context, string, application.BackupScheduleInput) error {
	return nil
}
func (f *fakeManagedDatabases) RunBackupNow(_ context.Context, name string) (application.Backup, error) {
	return application.Backup{FileName: "backup-20240102-030405.000000000Z.sql"}, nil
}
func (f *fakeManagedDatabases) RestoreBackup(context.Context, string, string) error { return nil }
func (f *fakeManagedDatabases) DeleteBackup(context.Context, string, string) error  { return nil }
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
