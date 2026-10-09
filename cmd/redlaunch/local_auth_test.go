package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
	"redlaunch/internal/auth"
	"redlaunch/internal/config"
	"redlaunch/internal/store"
)

func TestLocalUserCLIProvisionAndReset(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("GOOGLE_CLIENT_SECRET", "")
	t.Setenv("AUTH_SESSION_SECRET", "")
	path := filepath.Join(t.TempDir(), "auth.db")
	args := []string{"--db-path", path, "--email", "ADMIN@EXAMPLE.COM", "--password-stdin"}
	if err := runLocalUser(t.Context(), args, strings.NewReader("short\n"), false); !errors.Is(err, auth.ErrPasswordInvalid) {
		t.Fatal("weak password accepted", err)
	}
	if err := runLocalUser(t.Context(), args, strings.NewReader(" a '$#\\ passphrase \n"), false); err != nil {
		t.Fatal(err)
	}
	if err := runLocalUser(t.Context(), args, strings.NewReader("replacement password\n"), false); !errors.Is(err, application.ErrLocalUserExists) {
		t.Fatal("provision overwrote user", err)
	}
	database, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service, err := auth.New(auth.Config{SessionSecret: strings.Repeat("s", 32)}, database)
	if err != nil {
		t.Fatal(err)
	}
	user, err := service.AuthenticatePassword(t.Context(), "admin@example.com", " a '$#\\ passphrase ", "ip")
	if err != nil {
		t.Fatal("password special characters not preserved", err)
	}
	session, err := service.NewSession(user)
	if err != nil {
		t.Fatal(err)
	}
	if err := runLocalUser(t.Context(), args, strings.NewReader("replacement password\n"), true); err != nil {
		t.Fatal(err)
	}
	if _, valid, _ := service.ValidateSession(t.Context(), session); valid {
		t.Fatal("reset left session valid")
	}
	if _, err := service.AuthenticatePassword(t.Context(), "admin@example.com", "replacement password", "ip"); err != nil {
		t.Fatal("reset password failed", err)
	}
	if err := runLocalUser(t.Context(), []string{"--password", "secret"}, strings.NewReader("password"), false); err == nil {
		t.Fatal("password flag accepted")
	}
}

func TestAuthenticationReadiness(t *testing.T) {
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg := config.Config{AuthSessionSecret: strings.Repeat("s", 32)}
	if err := checkAuthenticationReady(t.Context(), cfg, database); err == nil {
		t.Fatal("empty auth accepted")
	}
	cfg.GoogleClientID = "id"
	cfg.GoogleClientSecret = "secret"
	if err := checkAuthenticationReady(t.Context(), cfg, database); err == nil {
		t.Fatal("Google without allowlist accepted")
	}
	if err := database.AddAuthorizedEmail(t.Context(), "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := checkAuthenticationReady(t.Context(), cfg, database); err != nil {
		t.Fatal("Google-only upgrade rejected", err)
	}
	cfg.GoogleClientID = ""
	cfg.GoogleClientSecret = ""
	if err := database.CreateLocalUser(t.Context(), "local@example.com", "test hash"); err != nil {
		t.Fatal(err)
	}
	if err := checkAuthenticationReady(t.Context(), cfg, database); err != nil {
		t.Fatal("local-only auth rejected", err)
	}
	cfg.AuthSessionSecret = ""
	if err := checkAuthenticationReady(t.Context(), cfg, database); err == nil {
		t.Fatal("missing session key accepted")
	}
}

func TestPasswordStdinLimitsAndUnicode(t *testing.T) {
	for _, value := range []string{strings.Repeat("界", 128) + "\n", "        \n", "password"} {
		if _, err := readLocalPassword(strings.NewReader(value)); err != nil {
			t.Fatal("valid stdin rejected", err)
		}
	}
	for _, value := range []string{strings.Repeat("界", 129), strings.Repeat("x", 1024), "password\nsecond-line", "password\n\n"} {
		if _, err := readLocalPassword(strings.NewReader(value)); err == nil {
			t.Fatal("invalid stdin accepted")
		}
	}
}

func TestSetupCredentialValidation(t *testing.T) {
	for _, email := range []string{"", "admin", "bad@", "admin@example.com"} {
		err := runValidatePassword([]string{"--email", email, "--password-stdin"}, strings.NewReader("password\n"))
		if (err == nil) != (email == "admin@example.com") {
			t.Fatalf("email validation %q: %v", email, err)
		}
	}
	if err := runValidatePassword([]string{"--email", "admin@example.com", "--password-stdin"}, strings.NewReader("short\n")); !errors.Is(err, auth.ErrPasswordInvalid) {
		t.Fatal(err)
	}
}
