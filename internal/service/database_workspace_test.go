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
}

func (r *workspaceRunnerFake) QueryPostgresDatabase(_ context.Context, _, _, database, owner, sql string) (string, error) {
	r.queries = append(r.queries, sql)
	r.owner, r.database = owner, database
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
	runner.output = "nspname,relname\nempty,\npublic,orders\npublic,\"odd\"\"; DROP TABLE orders;--\"\n"
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
