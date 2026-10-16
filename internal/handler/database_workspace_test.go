package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

type fakeWorkspaceDatabases struct {
	*fakeManagedDatabases
	name, sql  string
	schemas    []application.DatabaseSchema
	result     application.DatabaseQueryResult
	err        error
	queryErr   error
	imported   string
	importName string
	importErr  error
}

func (f *fakeWorkspaceDatabases) ImportDatabaseDump(_ context.Context, name string, source io.Reader) error {
	contents, err := io.ReadAll(source)
	if err != nil {
		return err
	}
	f.imported, f.importName = string(contents), name
	return f.importErr
}

func (f *fakeWorkspaceDatabases) GetDatabaseWorkspace(_ context.Context, name string) (application.ManagedDatabase, []application.DatabaseSchema, error) {
	return application.ManagedDatabase{Name: name, Owner: "shop"}, f.schemas, f.err
}
func (f *fakeWorkspaceDatabases) RunDatabaseQuery(_ context.Context, name, sql string) (application.DatabaseQueryResult, error) {
	f.name, f.sql = name, sql
	return f.result, f.queryErr
}

func newWorkspaceHandler(t *testing.T) (*Handler, *fakeWorkspaceDatabases) {
	t.Helper()
	f := &fakeWorkspaceDatabases{
		fakeManagedDatabases: &fakeManagedDatabases{cluster: application.ManagedDatabaseCluster{Enabled: true}, databases: []application.ManagedDatabase{{Name: "shop", Owner: "shop"}}},
		schemas:              []application.DatabaseSchema{{Name: "public", Tables: []application.DatabaseTable{{Name: "orders", SQL: `SELECT * FROM "public"."orders" LIMIT 1000;`, Columns: []string{"id", "name", "created_at"}}}}},
		result:               application.DatabaseQueryResult{Columns: []string{"value"}, Rows: [][]string{{"<script>alert(1)</script>"}}},
	}
	h, err := NewForTests(nil, fakeSetupManagerForDatabases(), f)
	if err != nil {
		t.Fatal(err)
	}
	return h, f
}

func TestDatabaseWorkspaceTableColumns(t *testing.T) {
	h, f := newWorkspaceHandler(t)
	f.schemas[0].Tables[0].Columns = []string{"id", "z_column", "<script>alert(1)</script>", "a_column"}
	w := databasesRequest(t, h, "GET", "/databases/shop", nil)
	body := w.Body.String()
	for _, value := range []string{`data-table-columns`, `aria-label="Columns for table orders"`, `class="database-column-list"`, `aria-label="Columns of orders"`, `data-table-actions`, `aria-label="Actions for table orders"`} {
		if !strings.Contains(body, value) {
			t.Fatalf("missing expandable table markup: %s", value)
		}
	}
	if strings.Contains(body, "<script>alert(1)</script>") || !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("column name was not escaped")
	}
	if strings.Index(body, ">z_column<") > strings.Index(body, ">a_column<") {
		t.Fatal("column order changed")
	}
	if f.sql != "" {
		t.Fatal("loading columns executed user SQL")
	}
	f.schemas[0].Tables[0].Columns = nil
	w = databasesRequest(t, h, "GET", "/databases/shop", nil)
	if !strings.Contains(w.Body.String(), "No columns.") {
		t.Fatal("missing empty column state")
	}
}

func workspacePost(t *testing.T, h *Handler, form url.Values, partial bool) *httptest.ResponseRecorder {
	t.Helper()
	form.Set("csrf_token", h.csrfToken)
	r := httptest.NewRequest("POST", "/databases/shop/query", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: h.csrfToken})
	if partial {
		r.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)
	return w
}

func TestDatabaseWorkspacePageAndLink(t *testing.T) {
	h, _ := newWorkspaceHandler(t)
	w := databasesRequest(t, h, "GET", "/databases/shop", nil)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	for _, value := range []string{"Schemas &amp; Tables", "orders", "SQL Editor", "Results", "Show data (max. 1000 rows)", `data-sql="SELECT * FROM`, `name="schema" value="public"`, `/static/database-workspace.js`, `/static/htmx.min.js`, "Import dump file…", "Search tables…", "Run Query", "data-sql-highlight", "Active"} {
		if !strings.Contains(w.Body.String(), value) {
			t.Fatalf("missing %s", value)
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("workspace can be cached")
	}
	w = databasesRequest(t, h, "GET", "/databases?tab=managed-databases", nil)
	if !strings.Contains(w.Body.String(), `href="/databases/shop"`) {
		t.Fatal("database name is not linked")
	}
}

func TestDatabaseDumpImport(t *testing.T) {
	for _, test := range []struct {
		name, token, confirm, filename, content string
		fail                                    bool
		status                                  int
	}{
		{name: "SQL", confirm: "1", filename: "dump.sql", content: "CREATE TABLE sample (id integer);", status: 303},
		{name: "custom", confirm: "1", filename: "dump.backup", content: "PGDMPfixture", status: 303},
		{name: "hostile filename", confirm: "1", filename: "../../outside.sql", content: "SELECT 1;", status: 303},
		{name: "confirmation required", filename: "dump.sql", content: "SELECT 1;", status: 422},
		{name: "invalid token", token: "wrong", confirm: "1", filename: "dump.sql", content: "SELECT 1;", status: 403},
		{name: "empty file", confirm: "1", filename: "dump.sql", status: 422},
		{name: "import failure", confirm: "1", filename: "dump.sql", content: "bad SQL", fail: true, status: 422},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, fake := newWorkspaceHandler(t)
			if test.fail {
				fake.importErr = errors.New("sensitive dump data")
			}
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			token := h.csrfToken
			if test.token != "" {
				token = test.token
			}
			if err := writer.WriteField("csrf_token", token); err != nil {
				t.Fatal(err)
			}
			if err := writer.WriteField("confirm", test.confirm); err != nil {
				t.Fatal(err)
			}
			file, err := writer.CreateFormFile("dump", test.filename)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(file, test.content); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/databases/shop/import", &body)
			r.Header.Set("Content-Type", writer.FormDataContentType())
			r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: h.csrfToken})
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status %d, expected %d: %s", w.Code, test.status, w.Body.String())
			}
			if test.status == 303 && (fake.importName != "shop" || fake.imported != test.content || w.Header().Get("Location") != "/databases/shop?notice=dump-imported") {
				t.Fatal("dump did not import into selected database")
			}
			if test.status == 403 || (test.status == 422 && !test.fail) {
				if fake.imported != "" {
					t.Fatal("invalid form imported dump")
				}
			}
			if strings.Contains(w.Body.String(), "sensitive dump data") {
				t.Fatal("dump diagnostics leaked")
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("import response can be cached")
			}
		})
	}
}

func TestDatabaseWorkspaceExecutionAndPartial(t *testing.T) {
	for _, partial := range []bool{false, true} {
		h, f := newWorkspaceHandler(t)
		w := workspacePost(t, h, url.Values{"sql": {"SELECT '<script>';"}}, partial)
		if w.Code != 200 || f.name != "shop" || f.sql != "SELECT '<script>';" {
			t.Fatalf("SQL not executed: %d, %q", w.Code, f.sql)
		}
		if strings.Contains(w.Body.String(), "<script>alert(1)</script>") || !strings.Contains(w.Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Fatal("result was not HTML escaped")
		}
		if partial && (strings.Contains(w.Body.String(), "<!doctype") || strings.Contains(w.Body.String(), "SQL editor")) {
			t.Fatal("partial included full page")
		}
	}
	h, f := newWorkspaceHandler(t)
	w := workspacePost(t, h, url.Values{"action": {"show-data"}, "schema": {"public"}, "table": {"orders"}, "sql": {"DROP DATABASE shop;"}}, false)
	if w.Code != 200 || f.sql != `SELECT * FROM "public"."orders" LIMIT 1000;` {
		t.Fatal("context action did not use trusted prepared SQL")
	}
	f.queryErr = errors.New("sensitive diagnostic")
	w = workspacePost(t, h, url.Values{"sql": {"bad SQL"}}, true)
	if !strings.Contains(w.Body.String(), "SQL execution failed") || strings.Contains(w.Body.String(), "sensitive diagnostic") {
		t.Fatal("query error was not safely displayed")
	}
}

func TestDatabaseWorkspaceCSRFAndMissingDatabase(t *testing.T) {
	h, f := newWorkspaceHandler(t)
	w := databasesRequest(t, h, "POST", "/databases/shop/query", url.Values{"sql": {"SELECT 1"}})
	if w.Code != http.StatusForbidden || f.sql != "" {
		t.Fatal("query executed without CSRF")
	}
	r := httptest.NewRequest("POST", "/databases/shop/query", strings.NewReader("csrf_token=wrong&sql=SELECT+1"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: h.csrfToken})
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || f.sql != "" {
		t.Fatal("query executed with invalid CSRF")
	}
	f.err = application.ErrManagedDatabaseNotFound
	w = databasesRequest(t, h, "GET", "/databases/missing", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing database status = %d", w.Code)
	}
}

func TestManagedDatabaseWorkspacePath(t *testing.T) {
	for name, want := range map[string]string{"shop": "/databases/shop", "Status page": "/databases/Status%20page", ".": "/databases/%2E", "..": "/databases/%2E%2E"} {
		if got := managedDatabaseWorkspacePath(name); got != want {
			t.Fatalf("path = %s, want %s", got, want)
		}
	}
}
