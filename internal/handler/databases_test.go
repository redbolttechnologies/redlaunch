package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
