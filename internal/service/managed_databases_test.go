package service

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
	"redlaunch/internal/store"
)

type managedDatabasesRunnerFake struct {
	running    bool
	execCalls  []string
	execErr    error
	execFunc   func(string) (string, error)
	upCalls    int
	networks   []string
	networkErr error
	upErr      error
	operations []string
}

func (r *managedDatabasesRunnerFake) EnsureNetwork(_ context.Context, name string) error {
	r.networks = append(r.networks, name)
	r.operations = append(r.operations, "network")
	return r.networkErr
}

func (r *managedDatabasesRunnerFake) Up(context.Context, string) error {
	r.upCalls++
	r.operations = append(r.operations, "up")
	return r.upErr
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

func TestManagedDatabaseConnectionsIncludeOnlyAccessibleUsers(t *testing.T) {
	runner := &managedDatabasesRunnerFake{}
	managed, _, _ := newManagedDatabasesForTest(t, runner)
	ctx := t.Context()
	if err := managed.repository.SaveManagedDatabaseCluster(ctx, application.ManagedDatabaseCluster{Enabled: true, DefaultUser: "admin"}); err != nil {
		t.Fatal(err)
	}
	for _, database := range []application.ManagedDatabase{{Name: "Status page", Owner: "owner"}, {Name: "other", Owner: "unrelated"}} {
		if _, err := managed.repository.CreateManagedDatabase(ctx, database); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []application.ManagedDatabaseUserDetail{
		{User: application.ManagedDatabaseUser{Username: "admin"}},
		{User: application.ManagedDatabaseUser{Username: "owner"}},
		{User: application.ManagedDatabaseUser{Username: "read user"}, Databases: []string{"Status page"}},
		{User: application.ManagedDatabaseUser{Username: "unrelated"}, Databases: []string{"other"}},
	} {
		if err := managed.repository.CreateManagedDatabaseUser(ctx, user.User.Username, user.Databases); err != nil {
			t.Fatal(err)
		}
	}
	connections, err := managed.GetDatabaseConnections(ctx, "Status page")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 3 {
		t.Fatalf("connection count = %d, want 3", len(connections))
	}
	for i, username := range []string{"admin", "owner", "read user"} {
		connection := connections[i]
		parsed, err := url.Parse(connection.URI)
		if err != nil {
			t.Fatal(err)
		}
		password, _ := parsed.User.Password()
		if connection.Username != username || parsed.User.Username() != username || password != "PASSWORD" || parsed.Scheme != "postgresql" || parsed.Host != managedDatabaseContainerName+":5432" || parsed.Path != "/Status page" {
			t.Fatal("connection template has incorrect user, placeholder or network address")
		}
		if !strings.Contains(connection.URI, "Status%20page") || (username == "read user" && !strings.Contains(connection.URI, "read%20user")) {
			t.Fatal("connection template did not URL-encode database or username")
		}
	}
	if len(runner.execCalls) != 0 || runner.upCalls != 0 {
		t.Fatal("reading connection templates executed a Docker operation")
	}
	if err := managed.repository.SetManagedDatabaseGrants(ctx, "read user", nil); err != nil {
		t.Fatal(err)
	}
	connections, err = managed.GetDatabaseConnections(ctx, "Status page")
	if err != nil || len(connections) != 2 {
		t.Fatal("revoking explicit access did not remove the connection option")
	}
	for _, name := range []string{"missing", "../outside", "bad\nname"} {
		if _, err := managed.GetDatabaseConnections(ctx, name); err == nil {
			t.Fatal("unknown or invalid database accepted for connection templates")
		}
	}
	if err := managed.repository.SaveManagedDatabaseCluster(ctx, application.ManagedDatabaseCluster{Enabled: false, DefaultUser: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := managed.GetDatabaseConnections(ctx, "Status page"); !errors.Is(err, application.ErrManagedDatabasesDisabled) {
		t.Fatal("disabled cluster returned connection templates")
	}
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
	if strings.Join(runner.networks, ",") != applicationNetworkName || strings.Join(runner.operations, ",") != "network,up" {
		t.Fatal("enable did not ensure the shared network before starting Compose")
	}
	if err := managed.EnableClusterWithProgress(t.Context(), application.ManagedDatabaseEnableInput{
		Provider: "postgres", Version: "17", DefaultUser: "redlaunch",
	}, nil); !errors.Is(err, application.ErrManagedDatabasesAlreadyEnabled) {
		t.Fatalf("second enable error = %v, want already enabled", err)
	}
}

func TestManagedDatabasesNetworkFailureLeavesEnableRetryable(t *testing.T) {
	runner := &managedDatabasesRunnerFake{networkErr: errors.New("shared network unavailable")}
	managed, _, _ := newManagedDatabasesForTest(t, runner)
	input := application.ManagedDatabaseEnableInput{Provider: "postgres", Version: "17", DefaultUser: "redlaunch", Password: "disposable-test-password"}
	if err := managed.EnableCluster(t.Context(), input); err == nil {
		t.Fatal("enable succeeded when the shared network could not be ensured")
	}
	cluster, err := managed.GetCluster(t.Context())
	if err != nil || cluster.Enabled || runner.upCalls != 0 {
		t.Fatal("network failure enabled the cluster or started Compose")
	}
	databases, err := managed.repository.ListManagedDatabases(t.Context())
	if err != nil || len(databases) != 0 {
		t.Fatal("network failure persisted database metadata")
	}
	users, err := managed.repository.ListManagedDatabaseUsers(t.Context())
	if err != nil || len(users) != 0 {
		t.Fatal("network failure persisted user metadata")
	}
	runner.networkErr = nil
	if err := managed.EnableCluster(t.Context(), input); err != nil {
		t.Fatalf("retry enable: %v", err)
	}
	if strings.Join(runner.operations, ",") != "network,network,up" {
		t.Fatal("enable retry did not ensure the network before starting Compose")
	}
}

func TestManagedDatabasesComposeConfiguration(t *testing.T) {
	if os.Getenv("REDLAUNCH_DOCKER_CONFIG_TEST") != "1" {
		t.Skip("set REDLAUNCH_DOCKER_CONFIG_TEST=1 to validate generated Compose configuration")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker CLI is unavailable")
	}
	directory := t.TempDir()
	if err := writeManagedFile(filepath.Join(directory, "compose.yml"), renderManagedDatabaseCompose("17"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{varsEnvFile, secretsEnvFile} {
		if err := writeManagedFile(filepath.Join(directory, name), "", envFileMode); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.CommandContext(t.Context(), binary, "compose", "--env-file", "/dev/null", "-f", filepath.Join(directory, "compose.yml"), "config", "--quiet")
	if err := command.Run(); err != nil {
		t.Fatalf("validate generated managed database Compose configuration: %v", err)
	}
}

func TestManagedDatabasesStartRecoversFailedFirstLaunch(t *testing.T) {
	for _, test := range []struct {
		name       string
		networkErr error
		upErr      error
	}{
		{"success", nil, nil},
		{"network refused", errors.New("network belongs to another application"), nil},
		{"compose failed", nil, errors.New("compose unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &managedDatabasesRunnerFake{upErr: errors.New("first launch failed")}
			managed, root, _ := newManagedDatabasesForTest(t, runner)
			input := application.ManagedDatabaseEnableInput{Provider: "postgres", Version: "17", DefaultUser: "redlaunch", Password: "disposable-test-password"}
			if err := managed.EnableCluster(t.Context(), input); err == nil {
				t.Fatal("initial launch unexpectedly succeeded")
			}
			cluster, err := managed.GetCluster(t.Context())
			if err != nil || !cluster.Enabled {
				t.Fatal("failed first launch did not retain enabled configuration for recovery")
			}
			files := make(map[string]string)
			for _, name := range []string{"compose.yml", varsEnvFile, secretsEnvFile} {
				path := filepath.Join(root, coreDir, managedDatabasesDir, name)
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				files[path] = string(contents)
			}
			runner.operations = nil
			runner.networks = nil
			runner.networkErr = test.networkErr
			runner.upErr = test.upErr
			err = managed.StartCluster(t.Context())
			if (err != nil) != (test.networkErr != nil || test.upErr != nil) {
				t.Fatal("Start returned an unexpected result")
			}
			wantOperations := "network,up"
			if test.networkErr != nil {
				wantOperations = "network"
			}
			if strings.Join(runner.networks, ",") != applicationNetworkName || strings.Join(runner.operations, ",") != wantOperations {
				t.Fatal("Start did not ensure the shared network before bringing up the project")
			}
			for path, before := range files {
				after, err := os.ReadFile(path)
				if err != nil || string(after) != before {
					t.Fatal("Start replaced existing database configuration or credentials")
				}
			}
		})
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
