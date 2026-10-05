package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func enabledManagedDatabasesForCreation(t *testing.T, runner *managedDatabasesRunnerFake) *ManagedDatabases {
	t.Helper()
	managed, _, _ := newManagedDatabasesForTest(t, runner)
	if err := managed.EnableCluster(t.Context(), application.ManagedDatabaseEnableInput{Provider: "postgres", Version: "17", DefaultUser: "redlaunch", Password: "disposable-test-password"}); err != nil {
		t.Fatal(err)
	}
	runner.execCalls = nil
	return managed
}

func TestManagedDatabaseAutomaticallyCreatesScopedUser(t *testing.T) {
	for _, owner := range []string{"", "reporter"} {
		t.Run("owner="+owner, func(t *testing.T) {
			runner := &managedDatabasesRunnerFake{running: true}
			managed := enabledManagedDatabasesForCreation(t, runner)
			result, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: "analytics", Owner: owner})
			if err != nil {
				t.Fatal(err)
			}
			username := owner
			if username == "" {
				username = "analytics"
			}
			if result.Owner != username || result.Credentials == nil || result.Credentials.Username != username {
				t.Fatal("missing generated user credentials")
			}
			secret, err := base64.RawURLEncoding.DecodeString(result.Credentials.Password)
			if err != nil || len(secret) != 32 {
				t.Fatal("generated password must contain 256 bits of randomness")
			}
			user, err := managed.repository.GetManagedDatabaseUser(t.Context(), username)
			if err != nil {
				t.Fatal(err)
			}
			if len(user.Databases) != 1 || user.Databases[0] != "analytics" {
				t.Fatal("automatic user has access outside its database")
			}
			statements := strings.Join(runner.execCalls, "\n")
			for _, required := range []string{"NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS", `CREATE DATABASE "analytics" OWNER "` + username + `"`, `REVOKE CONNECT, TEMPORARY ON DATABASE "analytics" FROM PUBLIC`, "aclexplode", "WHERE NOT rolsuper"} {
				if !strings.Contains(statements, required) {
					t.Fatalf("missing required permission statement %q", required)
				}
			}
		})
	}
}

func TestManagedDatabasePreservesExistingRole(t *testing.T) {
	for _, owner := range []string{"redlaunch", "external_user", ""} {
		t.Run("owner="+owner, func(t *testing.T) {
			runner := &managedDatabasesRunnerFake{running: true, execFunc: func(sql string) (string, error) {
				if strings.HasPrefix(sql, "SELECT 1 FROM pg_roles") {
					return "1", nil
				}
				return "", nil
			}}
			managed := enabledManagedDatabasesForCreation(t, runner)
			result, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: "analytics", Owner: owner})
			if err != nil {
				t.Fatal(err)
			}
			if result.Credentials != nil {
				t.Fatal("existing role was given replacement credentials")
			}
			for _, statement := range runner.execCalls {
				if strings.Contains(statement, "CREATE ROLE") || strings.Contains(statement, "ALTER ROLE") || strings.Contains(statement, "GRANT ") {
					t.Fatal("existing role was modified")
				}
			}
			if owner == "redlaunch" {
				user, err := managed.repository.GetManagedDatabaseUser(t.Context(), owner)
				if err != nil || len(user.Databases) != 1 || user.Databases[0] != "redlaunch" {
					t.Fatal("existing role permissions changed")
				}
			}
		})
	}
}

func TestManagedDatabaseCreationCleansUpFailures(t *testing.T) {
	for _, failAt := range []string{"CREATE DATABASE", "REVOKE CONNECT, TEMPORARY ON DATABASE"} {
		t.Run(failAt, func(t *testing.T) {
			runner := &managedDatabasesRunnerFake{running: true, execFunc: func(sql string) (string, error) {
				if strings.HasPrefix(sql, failAt) {
					return "", errors.New("disposable test failure")
				}
				return "", nil
			}}
			managed := enabledManagedDatabasesForCreation(t, runner)
			result, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: "analytics"})
			if err == nil || result.Credentials != nil {
				t.Fatal("failure returned credentials or succeeded")
			}
			statements := strings.Join(runner.execCalls, "\n")
			if !strings.Contains(statements, `DROP ROLE "analytics"`) {
				t.Fatal("new role was not removed")
			}
			if failAt != "CREATE DATABASE" && !strings.Contains(statements, `DROP DATABASE "analytics"`) {
				t.Fatal("new database was not removed")
			}
			if _, err := managed.repository.GetManagedDatabaseUser(t.Context(), "analytics"); !errors.Is(err, application.ErrManagedDatabaseUserNotFound) {
				t.Fatal("failed creation persisted user metadata")
			}
		})
	}
}

func TestManagedDatabaseRoleCreationErrorRedactsPassword(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true, execFunc: func(sql string) (string, error) {
		if strings.Contains(sql, "CREATE ROLE") {
			return "", fmt.Errorf("runner echoed statement: %s", sql)
		}
		return "", nil
	}}
	managed := enabledManagedDatabasesForCreation(t, runner)
	_, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: "analytics"})
	if err == nil || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatal("password was not redacted from diagnostic")
	}
	statement := runner.execCalls[len(runner.execCalls)-1]
	password := strings.Split(strings.Split(statement, "PASSWORD '")[1], "'")[0]
	if strings.Contains(err.Error(), password) {
		t.Fatal("generated password leaked in error")
	}
}

func TestManagedDatabaseHandlesRoleCreationRace(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true, execFunc: func(sql string) (string, error) {
		if strings.Contains(sql, "CREATE ROLE") {
			return "", errors.New("role already exists")
		}
		return "", nil
	}}
	managed := enabledManagedDatabasesForCreation(t, runner)
	result, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: "analytics"})
	if err != nil || result.Credentials != nil {
		t.Fatal("concurrently created role must be reused without credentials")
	}
}

type failingDatabaseCreationRepository struct{ ManagedDatabaseRepository }

func (r failingDatabaseCreationRepository) CreateManagedDatabaseWithUser(context.Context, application.ManagedDatabase, string) (application.ManagedDatabase, error) {
	return application.ManagedDatabase{}, errors.New("disposable metadata failure")
}

func TestManagedDatabaseMetadataFailureCleansUp(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	managed.repository = failingDatabaseCreationRepository{managed.repository}
	if _, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: "analytics"}); err == nil {
		t.Fatal("metadata failure was ignored")
	}
	statements := strings.Join(runner.execCalls, "\n")
	if !strings.Contains(statements, `DROP DATABASE "analytics"`) || !strings.Contains(statements, `DROP ROLE "analytics"`) {
		t.Fatal("metadata failure left new resources behind")
	}
}

func TestManagedDatabaseRejectsInvalidUsernameBeforeDocker(t *testing.T) {
	runner := &managedDatabasesRunnerFake{running: true}
	managed := enabledManagedDatabasesForCreation(t, runner)
	for _, input := range []application.ManagedDatabaseCreateInput{{Name: "analytics", Owner: "bad\nuser"}, {Name: "analytics", Owner: "../bad"}, {Name: "analytics", Owner: "bad';--"}} {
		if _, err := managed.CreateDatabase(t.Context(), input); err == nil {
			t.Fatal("invalid username was accepted")
		}
	}
	if len(runner.execCalls) != 0 {
		t.Fatal("invalid input reached Docker")
	}
}

func TestManagedDatabaseEmptyUsernameUsesExactDatabaseName(t *testing.T) {
	for _, name := range []string{"app-production", "Status page", "42", "metrics.v2"} {
		t.Run(name, func(t *testing.T) {
			runner := &managedDatabasesRunnerFake{running: true}
			managed := enabledManagedDatabasesForCreation(t, runner)
			result, err := managed.CreateDatabase(t.Context(), application.ManagedDatabaseCreateInput{Name: name})
			if err != nil {
				t.Fatal(err)
			}
			if result.Owner != name || result.Credentials == nil || result.Credentials.Username != name {
				t.Fatal("automatic username differs from the database name")
			}
			if _, err := managed.repository.GetManagedDatabaseUser(t.Context(), name); err != nil {
				t.Fatal("generated user name is not usable by the repository")
			}
		})
	}
}
