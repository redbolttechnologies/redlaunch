package application

import "testing"

func TestValidateManagedDatabaseEnableInputAcceptsPostgres(t *testing.T) {
	input, err := ValidateManagedDatabaseEnableInput(ManagedDatabaseEnableInput{
		Provider:    "postgres",
		Version:     "17",
		DefaultUser: "redlaunch",
		Password:    "",
	})
	if err != nil {
		t.Fatalf("ValidateManagedDatabaseEnableInput error = %v", err)
	}
	if input.Provider != ManagedDatabaseProviderPostgres || input.Version != "17" || input.DefaultUser != "redlaunch" {
		t.Fatalf("validated input = %#v", input)
	}
}

func TestValidateManagedDatabaseProviderRejectsUnknown(t *testing.T) {
	if _, err := ValidateManagedDatabaseProvider("mysql"); err != ErrManagedDatabaseProviderInvalid {
		t.Fatalf("provider error = %v, want provider invalid", err)
	}
}

func TestValidateManagedDatabaseUserInputRequiresPassword(t *testing.T) {
	if _, err := ValidateManagedDatabaseUserInput(ManagedDatabaseUserInput{Username: "app", Password: ""}); err == nil {
		t.Fatal("empty password error = nil, want an error")
	}
	if _, err := ValidateManagedDatabaseUserInput(ManagedDatabaseUserInput{Username: "app", Password: "secret", Databases: []string{"app", "app"}}); err != nil {
		t.Fatalf("duplicate databases error = %v", err)
	}
}

func TestValidateManagedDatabaseGrantsDeduplicates(t *testing.T) {
	grants, err := ValidateManagedDatabaseGrants([]string{"app", " app ", ""})
	if err != nil {
		t.Fatalf("grants error = %v", err)
	}
	if len(grants) != 1 || grants[0] != "app" {
		t.Fatalf("grants = %v, want [app]", grants)
	}
}

func TestManagedDatabaseUsernameAcceptsDatabaseNames(t *testing.T) {
	for _, name := range []string{"analytics", "app-production", "Status page", "42", "metrics.v2"} {
		if got, err := ValidateManagedDatabaseUsername(name); err != nil || got != name {
			t.Fatalf("valid database name %q rejected as a managed username", name)
		}
	}
	for _, name := range []string{"", "bad\nuser", "../evil", "user'", "user$NAME", "user\\name"} {
		if _, err := ValidateManagedDatabaseUsername(name); err == nil {
			t.Fatalf("unsafe username %q accepted", name)
		}
	}
}
