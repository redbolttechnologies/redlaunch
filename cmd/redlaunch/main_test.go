package main

import (
	"context"
	"strings"
	"testing"

	"redlaunch/internal/store"
)

func TestRunFailsClosedWithoutGoogleAuthentication(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("GOOGLE_CLIENT_SECRET", "")
	t.Setenv("AUTH_COOKIE_SECURE", "false")

	if err := run(context.Background()); err == nil || !strings.Contains(err.Error(), "Google authentication must be configured") {
		t.Fatalf("run() error = %v, want missing Google authentication configuration error", err)
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
