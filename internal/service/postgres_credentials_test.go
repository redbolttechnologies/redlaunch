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

type postgresCredentialsRunner struct {
	running    bool
	runningErr error
	execErr    error
	execCalls  []string
	upCalls    []string
	upErr      error
}

func (r *postgresCredentialsRunner) Up(_ context.Context, _ string) error {
	return nil
}

func (r *postgresCredentialsRunner) UpService(_ context.Context, _ string, serviceName string) error {
	r.upCalls = append(r.upCalls, serviceName)
	return r.upErr
}

func (r *postgresCredentialsRunner) IsServiceRunning(_ context.Context, _, _ string) (bool, error) {
	return r.running, r.runningErr
}

func (r *postgresCredentialsRunner) ExecPostgresSQL(_ context.Context, _, _ string, sql string) (string, error) {
	r.execCalls = append(r.execCalls, sql)
	return "", r.execErr
}

func (s *applicationRepositoryStub) UpdateServiceDatabaseUser(_ context.Context, applicationID int64, serviceName, databaseUser string) (application.Service, error) {
	if s.serviceErr != nil {
		return application.Service{}, s.serviceErr
	}
	for index := range s.services {
		if s.services[index].ApplicationID == applicationID && s.services[index].Name == serviceName {
			s.services[index].DatabaseUser = databaseUser
			s.service = s.services[index]
			return s.services[index], nil
		}
	}
	return application.Service{}, application.ErrServiceNotFound
}

func setupPostgresCredentialsProject(t *testing.T, user, password string) (string, *applicationRepositoryStub, *postgresCredentialsRunner) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Shop", FolderName: "shop"}},
		services: []application.Service{{
			ID: 1, ApplicationID: 7, Name: "db",
			Type:         application.ServiceTypePostgreSQL,
			DatabaseName: "shop",
			DatabaseUser: "shop",
		}},
	}
	runner := &postgresCredentialsRunner{running: true}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, applicationsDir, "shop")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte("services:\n  db:\n    image: postgres:17\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, varsEnvFile), []byte("POSTGRES_DB=shop\nPOSTGRES_USER=shop\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, secretsEnvFile), []byte("POSTGRES_PASSWORD=old-secret\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "db.vars.env"), []byte("POSTGRES_DB=shop\nPOSTGRES_USER="+user+"\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "db.secrets.env"), []byte("POSTGRES_PASSWORD="+password+"\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
	_ = applications
	return root, repository, runner
}

func TestUpdatePostgreSQLCredentialsRotatesPassword(t *testing.T) {
	root, repository, runner := setupPostgresCredentialsProject(t, "shop", "old-secret")
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := applications.UpdatePostgreSQLCredentials(t.Context(), 7, "db", application.PostgreSQLCredentialsInput{
		DatabaseUser:     "shop",
		DatabasePassword: "brand-new-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "db" {
		t.Fatalf("updated service = %q, want db", updated.Name)
	}
	if len(runner.execCalls) != 2 {
		t.Fatalf("postgres statements = %v, want ALTER ROLE plus verification", runner.execCalls)
	}
	if !strings.Contains(runner.execCalls[0], `ALTER ROLE "shop" WITH PASSWORD 'brand-new-secret'`) {
		t.Fatalf("postgres statement = %q, want password rotation", runner.execCalls[0])
	}
	if runner.execCalls[1] != "SELECT 1" {
		t.Fatalf("verification statement = %q, want SELECT 1", runner.execCalls[1])
	}
	if got := readServiceFile(t, filepath.Join(root, applicationsDir, "shop", "db.secrets.env")); !strings.Contains(got, "POSTGRES_PASSWORD=brand-new-secret") {
		t.Fatalf("scoped secrets = %q, want new password", got)
	}
	if got := readServiceFile(t, filepath.Join(root, applicationsDir, "shop", secretsEnvFile)); !strings.Contains(got, "POSTGRES_PASSWORD=brand-new-secret") {
		t.Fatalf("project secrets = %q, want mirrored password", got)
	}
	if len(runner.upCalls) != 1 || runner.upCalls[0] != "db" {
		t.Fatalf("recreated services = %v, want [db]", runner.upCalls)
	}
}

func TestUpdatePostgreSQLCredentialsRenamesRole(t *testing.T) {
	root, repository, runner := setupPostgresCredentialsProject(t, "shop", "old-secret")
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := applications.UpdatePostgreSQLCredentials(t.Context(), 7, "db", application.PostgreSQLCredentialsInput{
		DatabaseUser:     "shopowner",
		DatabasePassword: "rotated-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DatabaseUser != "shopowner" {
		t.Fatalf("updated database user = %q, want shopowner", updated.DatabaseUser)
	}
	if len(runner.execCalls) != 3 {
		t.Fatalf("postgres statements = %v, want rename plus password plus verification", runner.execCalls)
	}
	if !strings.Contains(runner.execCalls[0], `ALTER ROLE "shop" RENAME TO "shopowner"`) {
		t.Fatalf("first statement = %q, want rename", runner.execCalls[0])
	}
	if got := readServiceFile(t, filepath.Join(root, applicationsDir, "shop", "db.vars.env")); !strings.Contains(got, "POSTGRES_USER=shopowner") {
		t.Fatalf("scoped vars = %q, want new user", got)
	}
	if got := readServiceFile(t, filepath.Join(root, applicationsDir, "shop", varsEnvFile)); !strings.Contains(got, "POSTGRES_USER=shopowner") {
		t.Fatalf("project vars = %q, want mirrored user", got)
	}
	for _, service := range repository.services {
		if service.Name == "db" && service.DatabaseUser != "shopowner" {
			t.Fatalf("stored database user = %q, want shopowner", service.DatabaseUser)
		}
	}
}

func TestUpdatePostgreSQLCredentialsRejectsUnchanged(t *testing.T) {
	root, repository, runner := setupPostgresCredentialsProject(t, "shop", "old-secret")
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applications.UpdatePostgreSQLCredentials(t.Context(), 7, "db", application.PostgreSQLCredentialsInput{
		DatabaseUser: "shop",
	}); !errors.Is(err, application.ErrDatabaseCredentialsUnchanged) {
		t.Fatalf("unchanged error = %v, want %v", err, application.ErrDatabaseCredentialsUnchanged)
	}
	if len(runner.execCalls) != 0 {
		t.Fatalf("postgres statements = %v, want none", runner.execCalls)
	}
}

func TestUpdatePostgreSQLCredentialsRequiresRunning(t *testing.T) {
	root, repository, runner := setupPostgresCredentialsProject(t, "shop", "old-secret")
	runner.running = false
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applications.UpdatePostgreSQLCredentials(t.Context(), 7, "db", application.PostgreSQLCredentialsInput{
		DatabaseUser:     "shop",
		DatabasePassword: "new-secret",
	}); !errors.Is(err, application.ErrDatabaseServiceNotRunning) {
		t.Fatalf("stopped error = %v, want %v", err, application.ErrDatabaseServiceNotRunning)
	}
}

func TestUpdatePostgreSQLCredentialsEscapesSQL(t *testing.T) {
	root, repository, runner := setupPostgresCredentialsProject(t, "shop", "old-secret")
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applications.UpdatePostgreSQLCredentials(t.Context(), 7, "db", application.PostgreSQLCredentialsInput{
		DatabaseUser:     "shop",
		DatabasePassword: "o'brien",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.execCalls[0], `'o''brien'`) {
		t.Fatalf("postgres statement = %q, want escaped literal", runner.execCalls[0])
	}
	if strings.Contains(runner.execCalls[0], "o'brien") && !strings.Contains(runner.execCalls[0], "o''brien") {
		t.Fatalf("postgres statement = %q, want doubled quote", runner.execCalls[0])
	}
}

func TestStartServiceRefusesPostgresDrift(t *testing.T) {
	root, repository, runner := setupPostgresCredentialsProject(t, "mall", "old-secret")
	runner.running = false
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := applications.StartService(t.Context(), 7, "db"); !errors.Is(err, application.ErrDatabaseCredentialsDrift) {
		t.Fatalf("StartService(drift) error = %v, want %v", err, application.ErrDatabaseCredentialsDrift)
	}
	if len(runner.upCalls) != 0 {
		t.Fatalf("recreated services = %v, want none", runner.upCalls)
	}
}

func TestPostgresQuoteHelpers(t *testing.T) {
	if got := postgresQuoteIdentifier(`we"ird`); got != `"we""ird"` {
		t.Fatalf("identifier = %q, want quoted", got)
	}
	if got := postgresQuoteLiteral("o'brien"); got != `'o''brien'` {
		t.Fatalf("literal = %q, want escaped", got)
	}
}
