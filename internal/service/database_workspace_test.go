package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

type workspaceRunnerFake struct {
	*managedDatabasesRunnerFake
	output          string
	err             error
	queries         []string
	owner, database string
	dump            []byte
	importErr       error
	queryFunc       func(database, owner, sql string) (string, error)
}

func (r *workspaceRunnerFake) ImportPostgresDatabase(_ context.Context, _, _, database, owner string, contents []byte) error {
	r.database, r.owner, r.dump = database, owner, contents
	return r.importErr
}

func TestDatabaseDumpImportService(t *testing.T) {
	s, runner := newWorkspaceService(t)
	if err := s.ImportDatabaseDump(t.Context(), "shop", strings.NewReader("CREATE TABLE imported (id integer);")); err != nil {
		t.Fatal(err)
	}
	if runner.database != "shop" || runner.owner != "shop owner" || string(runner.dump) != "CREATE TABLE imported (id integer);" {
		t.Fatal("dump import used incorrect target or contents")
	}
	runner.dump = nil
	for _, contents := range []string{"", strings.Repeat("x", application.DatabaseDumpMaxBytes+1)} {
		if err := s.ImportDatabaseDump(t.Context(), "shop", strings.NewReader(contents)); err == nil {
			t.Fatal("accepted invalid dump size")
		}
	}
	runner.execFunc = func(string) (string, error) { return "t", nil }
	if err := s.ImportDatabaseDump(t.Context(), "shop", strings.NewReader("SELECT 1;")); err == nil {
		t.Fatal("restored as privileged owner")
	}
	if runner.dump != nil {
		t.Fatal("invalid import reached runner")
	}
	runner.execFunc = func(string) (string, error) { return "f", nil }
	runner.importErr = errors.New("sensitive SQL diagnostic")
	if err := s.ImportDatabaseDump(t.Context(), "shop", strings.NewReader("SELECT 1;")); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("import diagnostic leaked")
	}
}

func (r *workspaceRunnerFake) QueryPostgresDatabase(_ context.Context, _, _, database, owner, sql string) (string, error) {
	r.queries = append(r.queries, sql)
	r.owner, r.database = owner, database
	if r.queryFunc != nil {
		return r.queryFunc(database, owner, sql)
	}
	return r.output, r.err
}

func newWorkspaceService(t *testing.T) (*ManagedDatabases, *workspaceRunnerFake) {
	t.Helper()
	base := &managedDatabasesRunnerFake{execFunc: func(string) (string, error) { return "f", nil }}
	s, root, _ := newManagedDatabasesForTest(t, base)
	runner := &workspaceRunnerFake{managedDatabasesRunnerFake: base}
	s.runner = runner
	if err := os.MkdirAll(filepath.Join(root, "core", "databases"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.repository.SaveManagedDatabaseCluster(t.Context(), application.ManagedDatabaseCluster{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.repository.CreateManagedDatabase(t.Context(), application.ManagedDatabase{Name: "shop", Owner: "shop owner"}); err != nil {
		t.Fatal(err)
	}
	return s, runner
}

func TestDatabaseWorkspaceSchemasAndQuotedScripts(t *testing.T) {
	s, runner := newWorkspaceService(t)
	runner.output = "nspname,relname,attname\nempty,,\npublic,orders,id\npublic,orders,z_column\npublic,orders,a_column\npublic,\"odd\"\"; DROP TABLE orders;--\",\"quoted\"\"column\"\n"
	item, schemas, err := s.GetDatabaseWorkspace(t.Context(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "shop" || runner.owner != "shop owner" || runner.database != "shop" {
		t.Fatal("query did not target managed database owner")
	}
	if len(schemas) != 2 || len(schemas[0].Tables) != 0 || len(schemas[1].Tables) != 2 {
		t.Fatalf("unexpected schemas: %#v", schemas)
	}
	if got := schemas[1].Tables[1].SQL; got != `SELECT * FROM "public"."odd""; DROP TABLE orders;--" LIMIT 1000;` {
		t.Fatalf("unquoted identifier: %s", got)
	}
	if got := strings.Join(schemas[1].Tables[0].Columns, ","); got != "id,z_column,a_column" {
		t.Fatalf("columns not grouped in database order: %s", got)
	}
	if got := schemas[1].Tables[1].Columns[0]; got != `quoted"column` {
		t.Fatalf("quoted column metadata changed: %s", got)
	}
	if !strings.Contains(runner.queries[0], "a.attnum > 0 AND NOT a.attisdropped") || !strings.Contains(runner.queries[0], "ORDER BY n.nspname, c.relname, a.attnum") {
		t.Fatal("metadata query did not exclude system/dropped columns or preserve ordinal order")
	}
}

func TestDatabaseWorkspaceEmptyColumnsAndMalformedMetadata(t *testing.T) {
	s, runner := newWorkspaceService(t)
	runner.output = "schema,table,column\nempty,,\npublic,zero_columns,\n"
	_, schemas, err := s.GetDatabaseWorkspace(t.Context(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(schemas) != 2 || len(schemas[0].Tables) != 0 || len(schemas[1].Tables) != 1 || len(schemas[1].Tables[0].Columns) != 0 {
		t.Fatalf("empty schema/table metadata was lost: %#v", schemas)
	}
	for _, output := range []string{"", "schema,table\npublic,orders\n", "schema,table,column\npublic,orders\n"} {
		runner.output = output
		if _, _, err := s.GetDatabaseWorkspace(t.Context(), "shop"); err == nil {
			t.Fatal("accepted malformed column metadata")
		}
	}
}

func TestDatabaseWorkspaceQueries(t *testing.T) {
	s, runner := newWorkspaceService(t)
	runner.output = "id,value\n1,\"hello, world\"\n2,\"line one\nline two\"\n"
	result, err := s.RunDatabaseQuery(t.Context(), "shop", "SELECT * FROM example;")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Columns) != 2 || len(result.Rows) != 2 || result.Rows[1][1] != "line one\nline two" {
		t.Fatalf("unexpected result: %#v", result)
	}
	runner.output = ""
	if result, err := s.RunDatabaseQuery(t.Context(), "shop", "UPDATE example SET value = 'new';"); err != nil || len(result.Columns) != 0 {
		t.Fatalf("write failed: %#v, %v", result, err)
	}
	runner.output = "value\n" + strings.Repeat("1\n", 1001)
	if result, err := s.RunDatabaseQuery(t.Context(), "shop", "SELECT 1;"); err != nil || !result.Truncated || len(result.Rows) != 1000 {
		t.Fatalf("row limit failed: %#v, %v", result, err)
	}
	runner.err = errors.New("sensitive SQL error")
	if _, err := s.RunDatabaseQuery(t.Context(), "shop", "SELECT 1;"); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("query diagnostics leaked")
	}
}

func TestDatabaseWorkspaceRejectsUnsafeTargetsAndPSQL(t *testing.T) {
	s, runner := newWorkspaceService(t)
	for _, sql := range []string{"", `\! touch /tmp/unsafe`, " \n\\connect postgres", strings.Repeat("x", 65537), "SELECT\x00"} {
		if _, err := s.RunDatabaseQuery(t.Context(), "shop", sql); err == nil {
			t.Fatalf("accepted invalid SQL %q", sql[:min(len(sql), 30)])
		}
	}
	for _, name := range []string{"missing", "../outside"} {
		if _, err := s.RunDatabaseQuery(t.Context(), name, "SELECT 1;"); err == nil {
			t.Fatalf("accepted target %s", name)
		}
	}
	runner.execFunc = func(string) (string, error) { return "t", nil }
	if _, err := s.RunDatabaseQuery(t.Context(), "shop", "SELECT 1;"); err == nil {
		t.Fatal("accepted privileged owner")
	}
	runner.execFunc = func(string) (string, error) { return "", errors.New("unavailable") }
	if _, _, err := s.GetDatabaseWorkspace(t.Context(), "shop"); err == nil {
		t.Fatal("privilege check failed open")
	}
	if len(runner.queries) != 0 {
		t.Fatal("invalid input reached query execution")
	}
}
