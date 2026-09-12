package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/store"
)

func TestRunComposeProjectNameSeparatesManagedResourceKinds(t *testing.T) {
	root := t.TempDir()
	var applicationName bytes.Buffer
	if err := runComposeProjectName([]string{"--directory", filepath.Join(root, "applications", "proxy")}, &applicationName); err != nil {
		t.Fatal(err)
	}
	var coreName bytes.Buffer
	if err := runComposeProjectName([]string{"--directory", filepath.Join(root, "core", "proxy")}, &coreName); err != nil {
		t.Fatal(err)
	}
	if applicationName.String() == coreName.String() {
		t.Fatalf("application and core Compose project names both equal %q", strings.TrimSpace(applicationName.String()))
	}
}

func TestRunFailsClosedWithoutGoogleAuthentication(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("GOOGLE_CLIENT_SECRET", "")
	t.Setenv("AUTH_COOKIE_SECURE", "false")

	if err := run(context.Background()); err == nil || !strings.Contains(err.Error(), "Google authentication must be configured") {
		t.Fatalf("run() error = %v, want missing Google authentication configuration error", err)
	}
}

func TestRunSelfUpdateRequiresDirectory(t *testing.T) {
	if err := runSelfUpdate(context.Background(), nil); err == nil {
		t.Fatal("runSelfUpdate() without --directory returned nil error")
	}
}

func TestRunSelfUpdatePullsThenRebuildsSynchronously(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "docker-compose.yml"), []byte("services:\n  app:\n    image: redlaunch:local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	callsDirectory := t.TempDir()
	t.Setenv("SELF_UPDATE_CALLS_DIR", callsDirectory)
	binDirectory := t.TempDir()
	for _, name := range []string{"git", "docker"} {
		script := "#!/bin/sh\necho \"$0 $@\" >> \"$SELF_UPDATE_CALLS_DIR/" + name + ".calls\"\nexit 0\n"
		if err := os.WriteFile(filepath.Join(binDirectory, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := runSelfUpdate(context.Background(), []string{"--directory", directory}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git", "docker"} {
		contents, err := os.ReadFile(filepath.Join(callsDirectory, name+".calls"))
		if err != nil {
			t.Fatalf("read %s calls: %v", name, err)
		}
		if strings.TrimSpace(string(contents)) == "" {
			t.Fatalf("%s was not invoked by selfupdate-run", name)
		}
	}
}

func TestRunAddAuthorizedEmailPersistsNormalizedAddress(t *testing.T) {
	databasePath := t.TempDir() + "/redlaunch.db"
	t.Setenv("DB_PATH", databasePath)
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("GOOGLE_CLIENT_SECRET", "")
	t.Setenv("GOOGLE_REDIRECT_URL", "")
	t.Setenv("AUTH_SESSION_SECRET", "")
	t.Setenv("AUTH_COOKIE_SECURE", "false")

	if err := runAddAuthorizedEmail(context.Background(), []string{"--email", "Admin@Example.COM"}); err != nil {
		t.Fatal(err)
	}

	database, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	emails, err := database.ListAuthorizedEmails(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 1 || emails[0] != "admin@example.com" {
		t.Fatalf("authorized emails = %#v, want [admin@example.com]", emails)
	}
}
