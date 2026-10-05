package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
	"redlaunch/internal/store"
)

type managedDatabasesRunnerFake struct {
	running   bool
	execCalls []string
	execErr   error
	execFunc  func(string) (string, error)
	upCalls   int
}

func (r *managedDatabasesRunnerFake) Up(context.Context, string) error {
	r.upCalls++
	return nil
}

func (r *managedDatabasesRunnerFake) ConfigServices(context.Context, string) ([]compose.ConfiguredService, error) {
	return nil, nil
}

func (r *managedDatabasesRunnerFake) IsServiceRunning(context.Context, string, string) (bool, error) {
	return r.running, nil
}

func (r *managedDatabasesRunnerFake) BackupPostgreSQL(_ context.Context, _, _ string, destination string) error {
	return writeTestBackupFile(destination)
}

func (r *managedDatabasesRunnerFake) RestorePostgreSQL(context.Context, string, string, string) error {
	return nil
}

func (r *managedDatabasesRunnerFake) ExecPostgresSQL(_ context.Context, _, _ string, sql string) (string, error) {
	r.execCalls = append(r.execCalls, sql)
	if r.execFunc != nil {
		return r.execFunc(sql)
	}
	if r.execErr != nil {
		return "", r.execErr
	}
	if sql == "SELECT 1 FROM pg_roles WHERE rolname = 'redlaunch'" {
		return "1", nil
	}
	return "", nil
}

func (r *managedDatabasesRunnerFake) Start(context.Context, string, string) error { return nil }
func (r *managedDatabasesRunnerFake) Stop(context.Context, string, string) error  { return nil }
func (r *managedDatabasesRunnerFake) Restart(context.Context, string, string) error {
	return nil
}
func (r *managedDatabasesRunnerFake) Logs(context.Context, string, string, int) (string, error) {
	return "log line", nil
}
func (r *managedDatabasesRunnerFake) OpenLogs(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("log line")), nil
}
func (r *managedDatabasesRunnerFake) ListServices(context.Context, string) ([]compose.ServiceRuntime, error) {
	return []compose.ServiceRuntime{{
		ServiceName:   managedDatabaseServiceName,
		ContainerName: managedDatabaseContainerName,
		Status:        "running",
		Image:         "postgres:17",
	}}, nil
}

func writeTestBackupFile(destination string) error {
	return writeManagedFile(destination, "-- test dump\n", 0o600)
}

func newManagedDatabasesForTest(t *testing.T, runner *managedDatabasesRunnerFake) (*ManagedDatabases, string, string) {
	t.Helper()
	projectsRoot := t.TempDir() + "/projects"
	backupRoot := t.TempDir() + "/backups"
	database, err := store.Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	managed, err := NewManagedDatabases(database, ManagedDatabaseConfig{
		ProjectsRoot: projectsRoot,
		BackupRoot:   backupRoot,
		Runner:       runner,
		Clock:        func() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return managed, projectsRoot, backupRoot
}

func TestManagedDatabasesEnableWritesComposeAndStarts(t *testing.T) {
	runner := &managedDatabasesRunnerFake{}
	managed, _, _ := newManagedDatabasesForTest(t, runner)
	if err := managed.EnableClusterWithProgress(t.Context(), application.ManagedDatabaseEnableInput{
		Provider:    "postgres",
		Version:     "17",
		DefaultUser: "redlaunch",
		Password:    "secret-password",
	}, nil); err != nil {
		t.Fatalf("enable error = %v", err)
	}
	cluster, err := managed.GetCluster(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !cluster.Enabled || cluster.Version != "17" {
		t.Fatalf("cluster = %#v", cluster)
	}
	if runner.upCalls != 1 {
		t.Fatalf("up calls = %d, want 1", runner.upCalls)
	}
	if err := managed.EnableClusterWithProgress(t.Context(), application.ManagedDatabaseEnableInput{
		Provider: "postgres", Version: "17", DefaultUser: "redlaunch",
	}, nil); !errors.Is(err, application.ErrManagedDatabasesAlreadyEnabled) {
		t.Fatalf("second enable error = %v, want already enabled", err)
	}
}

func TestManagedDatabasesCreateAndDrop(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed, _, _ := newManagedDatabasesForTest(t, runner)
	if err := managed.EnableClusterWithProgress(t.Context(), application.ManagedDatabaseEnableInput{
		Provider: "postgres", Version: "17", DefaultUser: "redlaunch", Password: "x",
	}, nil); err != nil {
		t.Fatal(err)
	}
	created, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: "analytics", Owner: ""})
	if err != nil {
		t.Fatalf("create error = %v", err)
	}
	if created.Owner != "analytics" {
		t.Fatalf("owner = %q, want analytics", created.Owner)
	}
	if err := managed.DropDatabase(t.Context(), "analytics"); err != nil {
		t.Fatalf("drop error = %v", err)
	}
}

func TestManagedDatabasesUsersAndPermissions(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed, _, _ := newManagedDatabasesForTest(t, runner)
	if err := managed.EnableClusterWithProgress(t.Context(), application.ManagedDatabaseEnableInput{
		Provider: "postgres", Version: "17", DefaultUser: "redlaunch", Password: "x",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: "app2"}); err != nil {
		t.Fatal(err)
	}
	if err := managed.CreateUser(t.Context(), application.ManagedDatabaseUserInput{
		Username: "app", Password: "secret", Databases: []string{"app2"},
	}); err != nil {
		t.Fatalf("create user error = %v", err)
	}
	if err := managed.UpdateUserPassword(t.Context(), "app", "rotated"); err != nil {
		t.Fatalf("password error = %v", err)
	}
	if err := managed.SetUserPermissions(t.Context(), "app", []string{}); err != nil {
		t.Fatalf("permissions error = %v", err)
	}
	if err := managed.DeleteUser(t.Context(), "app"); err != nil {
		t.Fatalf("delete error = %v", err)
	}
	if err := managed.DeleteUser(t.Context(), "redlaunch"); !errors.Is(err, application.ErrManagedDatabaseDefaultUserInUse) {
		t.Fatalf("delete default error = %v, want default in use", err)
	}
}

func TestManagedDatabasesBackupRunAndSchedule(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed, _, _ := newManagedDatabasesForTest(t, runner)
	if err := managed.EnableClusterWithProgress(t.Context(), application.ManagedDatabaseEnableInput{
		Provider: "postgres", Version: "17", DefaultUser: "redlaunch", Password: "x",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := managed.UpdateBackupSchedule(t.Context(), "redlaunch", application.BackupScheduleInput{
		Enabled: true, ScheduleType: application.BackupScheduleDaily, Hour: 3, Minute: 0, RetentionDays: 14,
	}); err != nil {
		t.Fatalf("schedule error = %v", err)
	}
	backup, err := managed.RunBackupNow(t.Context(), "redlaunch")
	if err != nil {
		t.Fatalf("backup error = %v", err)
	}
	if backup.FileName == "" {
		t.Fatal("backup file name is empty")
	}
	details, err := managed.GetBackupDetails(t.Context(), "redlaunch")
	if err != nil {
		t.Fatal(err)
	}
	if len(details.Backups) != 1 {
		t.Fatalf("backups = %#v", details.Backups)
	}
	if err := managed.DeleteBackup(t.Context(), "redlaunch", backup.FileName); err != nil {
		t.Fatalf("delete backup error = %v", err)
	}
}

func TestManagedDatabasesRejectsTraversalNames(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed, _, _ := newManagedDatabasesForTest(t, runner)
	if err := managed.EnableClusterWithProgress(t.Context(), application.ManagedDatabaseEnableInput{
		Provider: "postgres", Version: "17", DefaultUser: "redlaunch", Password: "x",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: "../evil"}); err == nil {
		t.Fatal("traversal database error = nil, want an error")
	}
	if err := managed.CreateUser(t.Context(), application.ManagedDatabaseUserInput{Username: "bad;user", Password: "secret"}); err == nil {
		t.Fatal("injection user error = nil, want an error")
	}
}
