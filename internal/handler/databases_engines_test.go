package handler

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

func multiEngineFixture() *fakeManagedDatabases {
	return &fakeManagedDatabases{
		cluster: application.ManagedDatabaseCluster{Enabled: true, Provider: "postgres", Version: "17", DefaultUser: "admin"},
		databases: []application.ManagedDatabase{
			{Name: "restock_admin", Owner: "restock_admin", Provider: "postgres"},
			{Name: "restock_events", Owner: "events_writer", Provider: "postgres"},
			{Name: "shop_catalog", Owner: "catalog_app", Provider: "mariadb"},
		},
		users: []application.ManagedDatabaseUserDetail{
			{User: application.ManagedDatabaseUser{Username: "admin", Provider: "postgres"}},
			{User: application.ManagedDatabaseUser{Username: "catalog_app", Provider: "mariadb"}, Databases: []string{"shop_catalog"}},
		},
		engines: []application.ManagedDatabaseEngine{
			{Provider: "postgres", Version: "17", Provisioned: true, Running: true, Status: "running"},
			{Provider: "mariadb", Version: "11.4", Provisioned: true, Running: true, Status: "running"},
			{Provider: "mysql", Version: "8.4"},
		},
	}
}

func TestDatabasesInventoryShowsEnginesAndFilters(t *testing.T) {
	h := newDatabasesTestHandler(t, multiEngineFixture())
	body := databasesRequest(t, h, "GET", "/databases", nil).Body.String()
	for _, want := range []string{
		`>All engines <span>3</span>`, `>Postgres <span>2</span>`, `>MariaDB <span>1</span>`, `>MySQL <span>0</span>`,
		`<th scope="col">Engine</th>`, `<span class="databases-engine-badge">MariaDB</span>`,
		`<h3>MySQL <code>8.4</code></h3><p>Starts on first use</p>`, `Available`,
		`<h3>MariaDB <code>11.4</code></h3><p>1 database</p>`, `3 databases`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("inventory missing %q", want)
		}
	}
	// Only Postgres databases have an SQL workspace.
	if !strings.Contains(body, `href="/databases/restock_admin"`) || strings.Contains(body, `href="/databases/shop_catalog"`) {
		t.Fatal("SQL workspace link is not limited to Postgres databases")
	}

	filtered := databasesRequest(t, h, "GET", "/databases?engine=mariadb", nil).Body.String()
	inventory := filtered[strings.Index(filtered, `id="managed-databases-panel"`):strings.Index(filtered, `id="managed-users-panel"`)]
	if !strings.Contains(inventory, "shop_catalog") || strings.Contains(inventory, "restock_admin") {
		t.Fatal("engine filter did not scope the inventory")
	}
	if !strings.Contains(filtered, `aria-current="true">MariaDB <span>1</span>`) || !strings.Contains(filtered, `name="engine" value="mariadb"`) {
		t.Fatal("engine filter is not marked active or kept by the search form")
	}
	if !strings.Contains(filtered, `1 database</p>`) {
		t.Fatal("filtered footer count is wrong")
	}

	empty := databasesRequest(t, h, "GET", "/databases?engine=mysql", nil).Body.String()
	for _, want := range []string{"No MySQL databases yet", "Create your first MySQL database to get started.", `href="/databases?create=1&amp;engine=mysql"`} {
		if !strings.Contains(empty, want) {
			t.Fatalf("empty engine view missing %q", want)
		}
	}

	// An unknown engine falls back to the unfiltered inventory.
	unknown := databasesRequest(t, h, "GET", "/databases?engine="+url.QueryEscape(`"><script>`), nil).Body.String()
	unknownInventory := unknown[strings.Index(unknown, `id="managed-databases-panel"`):strings.Index(unknown, `id="managed-users-panel"`)]
	if strings.Contains(unknown, "<script>") || !strings.Contains(unknownInventory, "shop_catalog") || !strings.Contains(unknownInventory, "restock_admin") {
		t.Fatal("unknown engine was not ignored safely")
	}
}

func TestDatabasesInlineCreateFormPreselectsActiveEngine(t *testing.T) {
	h := newDatabasesTestHandler(t, multiEngineFixture())
	body := databasesRequest(t, h, "GET", "/databases?create=1&engine=mysql", nil).Body.String()
	for _, want := range []string{
		`<option value="mysql" data-note="Redlaunch starts MySQL 8.4 when you create its first database." selected>MySQL 8.4</option>`,
		`<option value="mariadb" data-note="Uses the running MariaDB 11.4 container.">MariaDB 11.4</option>`,
		`<span data-managed-engine-note>Redlaunch starts MySQL 8.4 when you create its first database.</span>`,
		`aria-disabled="true"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("create form missing %q", want)
		}
	}
	if strings.Count(body, `id="managed-database-create-title"`) != 1 {
		t.Fatal("create form must have one heading")
	}
}

func TestDatabasesCreateOnRunningEngineForwardsProvider(t *testing.T) {
	fake := multiEngineFixture()
	h := newDatabasesTestHandler(t, fake)
	rr := databasesPostWithCSRF(h, "/databases/create", url.Values{"provider": {"mariadb"}, "name": {"billing"}, "owner": {"billing_app"}})
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/databases?database=billing&engine=mariadb" {
		t.Fatalf("status = %d location = %q", rr.Code, rr.Header().Get("Location"))
	}
	if fake.createdProvider != "mariadb" || fake.createdDB != "billing" || fake.progressCalls != 0 {
		t.Fatalf("provider = %q db = %q progress calls = %d", fake.createdProvider, fake.createdDB, fake.progressCalls)
	}
}

func TestDatabasesCreateRejectsInvalidInputInline(t *testing.T) {
	for _, test := range []struct {
		form    url.Values
		message string
	}{
		{url.Values{"provider": {"oracle"}, "name": {"billing"}}, "Choose Postgres, MariaDB, or MySQL."},
		{url.Values{"provider": {"mysql"}, "name": {"billing"}, "owner": {strings.Repeat("o", 33)}}, "MySQL usernames can be at most 32 characters."},
		{url.Values{"provider": {"mysql"}, "name": {"bad;name"}}, "The database name is invalid."},
	} {
		fake := multiEngineFixture()
		h := newDatabasesTestHandler(t, fake)
		rr := databasesPostWithCSRF(h, "/databases/create", test.form)
		body := rr.Body.String()
		if rr.Code != http.StatusBadRequest || !strings.Contains(body, test.message) || !strings.Contains(body, `class="service-widget databases-create"`) {
			t.Fatalf("form %v: status %d, message %q, inline form %v", test.form, rr.Code, test.message, strings.Contains(body, "databases-create"))
		}
		if fake.createdDB != "" || fake.progressCalls != 0 {
			t.Fatal("invalid request reached the service")
		}
		if name := test.form.Get("name"); name == "billing" && !strings.Contains(body, `value="billing"`) {
			t.Fatal("the submitted name was not kept")
		}
	}
}

func TestDatabasesFirstUseEngineCreatesDatabaseInAJob(t *testing.T) {
	fake := multiEngineFixture()
	fake.credentials = &application.ManagedDatabaseCredentials{Username: "billing", Password: "generated_password_value"}
	h := newDatabasesTestHandler(t, fake)
	rr := databasesPostWithCSRF(h, "/databases/create", url.Values{"provider": {"mysql"}, "name": {"billing"}})
	location := rr.Header().Get("Location")
	if rr.Code != http.StatusSeeOther || !strings.HasPrefix(location, "/databases?managed_job=") {
		t.Fatalf("status = %d location = %q, want a job redirect", rr.Code, location)
	}
	jobID := strings.TrimPrefix(location, "/databases?managed_job=")
	statusURL := "/databases/enable/status?id=" + jobID

	var status string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		status = databasesRequest(t, h, "GET", statusURL, nil).Body.String()
		if strings.Contains(status, `data-state="complete"`) {
			break
		}
	}
	for _, want := range []string{`data-state="complete"`, "MySQL database ready", "Wait for MySQL to accept connections", `data-close-url="/databases?created=` + jobID + `"`, ">Continue</button>"} {
		if !strings.Contains(status, want) {
			t.Fatalf("progress dialog missing %q:\n%s", want, status)
		}
	}
	if fake.progressCalls != 1 || fake.createdProvider != "mysql" || fake.createdDB != "billing" {
		t.Fatalf("progress calls = %d provider = %q db = %q", fake.progressCalls, fake.createdProvider, fake.createdDB)
	}
	if strings.Contains(status, "generated_password_value") || strings.Contains(databasesRequest(t, h, "GET", location, nil).Body.String(), "generated_password_value") {
		t.Fatal("credentials leaked into the progress view")
	}

	first := databasesRequest(t, h, "GET", "/databases?created="+jobID, nil).Body.String()
	encoded := base64.StdEncoding.EncodeToString([]byte("MYSQL_USER=\"billing\"\nMYSQL_PASSWORD=generated_password_value\n"))
	if !strings.Contains(first, `value="generated_password_value" readonly`) || !strings.Contains(first, encoded) {
		t.Fatal("credentials were not handed over after the job")
	}
	if again := databasesRequest(t, h, "GET", "/databases?created="+jobID, nil).Body.String(); strings.Contains(again, "generated_password_value") {
		t.Fatal("credentials were handed over twice")
	}
}

func TestDatabasesProgressFailureReturnsToCreateForm(t *testing.T) {
	fake := multiEngineFixture()
	h := newDatabasesTestHandler(t, fake)
	job, _, err := h.managedDatabasesJobs.createUnique(managedDatabasesJobKindCreate, "mariadb", "shop")
	if err != nil {
		t.Fatal(err)
	}
	job.update("start", "")
	job.fail(application.ErrManagedDatabaseEngineNotReady)
	body := databasesRequest(t, h, "GET", "/databases/enable/status?id="+job.id, nil).Body.String()
	for _, want := range []string{`data-state="failed"`, "Database creation stopped", "Start the MariaDB container", "did not become ready in time", `data-close-url="/databases?create=1&amp;engine=mariadb"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("failed progress missing %q:\n%s", want, body)
		}
	}
}

func TestDatabasesCredentialsDownloadUsesProviderVariables(t *testing.T) {
	data := managedDatabaseCredentials("mariadb", &application.ManagedDatabaseCredentials{Username: "shop", Password: "pw"})
	if !strings.HasSuffix(string(data.DownloadURL), base64.StdEncoding.EncodeToString([]byte("MARIADB_USER=\"shop\"\nMARIADB_PASSWORD=pw\n"))) {
		t.Fatalf("download = %s", data.DownloadURL)
	}
}

func TestDatabasesClusterActionsTargetTheSelectedEngine(t *testing.T) {
	fake := multiEngineFixture()
	h := newDatabasesTestHandler(t, fake)
	for _, test := range []struct{ path, engine, want string }{
		{"/databases/start", "mariadb", "start:mariadb"},
		{"/databases/stop", "mysql", "stop:mysql"},
		{"/databases/restart", "", "restart:postgres"},
	} {
		form := url.Values{}
		if test.engine != "" {
			form.Set("engine", test.engine)
		}
		if rr := databasesPostWithCSRF(h, test.path, form); rr.Code != http.StatusSeeOther {
			t.Fatalf("%s status = %d", test.path, rr.Code)
		}
		if got := fake.engineActions[len(fake.engineActions)-1]; got != test.want {
			t.Fatalf("%s called %q, want %q", test.path, got, test.want)
		}
	}
	before := len(fake.engineActions)
	if rr := databasesPostWithCSRF(h, "/databases/stop", url.Values{"engine": {"oracle"}}); rr.Code != http.StatusBadRequest || len(fake.engineActions) != before {
		t.Fatalf("invalid engine status = %d, actions = %v", rr.Code, fake.engineActions)
	}
}

func TestDatabasesSettingsListsOnlyStartedEngines(t *testing.T) {
	h := newDatabasesTestHandler(t, multiEngineFixture())
	body := databasesRequest(t, h, "GET", "/databases?tab=managed-settings", nil).Body.String()
	for _, want := range []string{"Postgres container", "MariaDB container", `name="engine" value="mariadb"`, `dialog=cluster-stop&amp;engine=mariadb`} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings missing %q", want)
		}
	}
	if strings.Contains(body, "MySQL container") {
		t.Fatal("settings lists an engine that was never started")
	}
	dialog := databasesRequest(t, h, "GET", "/databases?tab=managed-settings&dialog=cluster-stop&engine=mariadb", nil).Body.String()
	if !strings.Contains(dialog, "Stop MariaDB?") || !strings.Contains(dialog, `name="engine" value="mariadb"`) {
		t.Fatal("stop dialog does not target the selected engine")
	}
}

func TestDatabasesLogsSelectTheEngine(t *testing.T) {
	h := newDatabasesTestHandler(t, multiEngineFixture())
	body := databasesRequest(t, h, "GET", "/databases?tab=managed-logs&log_engine=mariadb", nil).Body.String()
	for _, want := range []string{"<code>log mariadb</code>", `href="/databases/logs/download?engine=mariadb"`, `aria-current="true">MariaDB</a>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("logs missing %q", want)
		}
	}
	// A never-started engine falls back to Postgres.
	body = databasesRequest(t, h, "GET", "/databases?tab=managed-logs&log_engine=mysql", nil).Body.String()
	if !strings.Contains(body, "<code>log postgres</code>") {
		t.Fatal("logs for an unstarted engine were not replaced by Postgres logs")
	}
	download := databasesRequest(t, h, "GET", "/databases/logs/download?engine=mariadb", nil)
	if download.Code != http.StatusOK || download.Body.String() != "log mariadb" || !strings.Contains(download.Header().Get("Content-Disposition"), "managed-databases-mariadb-logs.txt") {
		t.Fatalf("download = %d %q %q", download.Code, download.Body.String(), download.Header().Get("Content-Disposition"))
	}
	if rr := databasesRequest(t, h, "GET", "/databases/logs/download?engine=oracle", nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid engine download status = %d", rr.Code)
	}
}

func TestDatabasesUsersShowEngineAndFilterAccessChoices(t *testing.T) {
	fake := multiEngineFixture()
	h := newDatabasesTestHandler(t, fake)
	body := databasesRequest(t, h, "GET", "/databases?tab=managed-users", nil).Body.String()
	for _, want := range []string{`<th scope="col">Engine</th>`, `<span class="databases-engine-badge">MariaDB</span>`, `data-managed-access-provider="mariadb"`, `data-managed-access-provider="postgres"`, `<option value="mariadb">MariaDB</option>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("users tab missing %q", want)
		}
	}
	if strings.Contains(body, `<option value="mysql">`) {
		t.Fatal("create user offers an engine that was never started")
	}
	// The default Postgres user is protected; a MariaDB login with the same
	// name as the default user is an ordinary user.
	if strings.Contains(body, `aria-label="Delete user admin"`) || !strings.Contains(body, `aria-label="Delete user catalog_app"`) {
		t.Fatal("delete actions do not follow the default user rule")
	}

	rr := databasesPostWithCSRF(h, "/databases/users/create", url.Values{"provider": {"mariadb"}, "username": {"reader"}, "password": {"pw"}, "databases": {"shop_catalog"}})
	if rr.Code != http.StatusSeeOther || fake.createdUser != "reader" {
		t.Fatalf("status = %d created = %q", rr.Code, fake.createdUser)
	}
}

func TestDatabasesPermissionDialogOffersOnlyDatabasesOfTheUsersEngine(t *testing.T) {
	h := newDatabasesTestHandler(t, multiEngineFixture())
	data, err := h.loadDatabasesPageData(t.Context(), url.Values{"tab": {"managed-users"}, "user": {"catalog_app"}, "dialog": {"user-permissions"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.AccessChoices) != 1 || data.AccessChoices[0].Name != "shop_catalog" || !data.AccessChoices[0].Checked {
		t.Fatalf("access choices = %#v", data.AccessChoices)
	}
	if len(data.CreateAccessChoices) != 3 {
		t.Fatalf("create choices = %#v", data.CreateAccessChoices)
	}
}

func TestDatabasesBackupRunDependsOnTheDatabaseEngine(t *testing.T) {
	fake := multiEngineFixture()
	fake.engines[1].Running = false
	fake.engines[1].Status = "exited"
	h := newDatabasesTestHandler(t, fake)
	body := databasesRequest(t, h, "GET", "/databases?tab=managed-backups&database=shop_catalog", nil).Body.String()
	if !regexp.MustCompile(`class="button button-primary databases-enable-button" type="submit" disabled`).MatchString(body) {
		t.Fatal("backup button is enabled while the database's engine is stopped")
	}
	body = databasesRequest(t, h, "GET", "/databases?tab=managed-backups&database=restock_admin", nil).Body.String()
	if regexp.MustCompile(`class="button button-primary databases-enable-button" type="submit" disabled`).MatchString(body) {
		t.Fatal("backup button is disabled although the Postgres container runs")
	}
}

func TestManagedDatabasesMessagesForEngineErrors(t *testing.T) {
	for err, status := range map[error]int{
		application.ErrManagedDatabaseProviderMismatch:     http.StatusBadRequest,
		application.ErrManagedDatabaseUserTooLong:          http.StatusBadRequest,
		application.ErrManagedDatabaseUserOwnsDatabase:     http.StatusBadRequest,
		application.ErrManagedDatabaseWorkspaceUnsupported: http.StatusBadRequest,
		application.ErrManagedDatabaseEngineNotProvisioned: http.StatusConflict,
	} {
		if got := managedDatabasesStatus(err); got != status {
			t.Errorf("status(%v) = %d, want %d", err, got, status)
		}
		if managedDatabasesUserMessage(err) == "The managed database operation failed." {
			t.Errorf("no user message for %v", err)
		}
	}
}
