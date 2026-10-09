package service

import (
	"errors"
	"path/filepath"
	"redlaunch/internal/application"
	"redlaunch/internal/auth"
	"redlaunch/internal/store"
	"strings"
	"testing"
)

func TestUserManagementAndSharedAuthentication(t *testing.T) {
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	users, err := NewUserService(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := users.Create(t.Context(), " ADMIN@EXAMPLE.COM ", "test password"); err != nil {
		t.Fatal(err)
	}
	if err := users.Create(t.Context(), "admin@example.com", "test password"); !errors.Is(err, application.ErrUserExists) {
		t.Fatal("duplicate accepted", err)
	}
	list, err := users.List(t.Context())
	if err != nil || len(list) != 1 || list[0].Email != "admin@example.com" || !list[0].HasPassword {
		t.Fatal("unsafe/incomplete list", err)
	}
	if err := users.Delete(t.Context(), list[0].ID); !errors.Is(err, application.ErrLastUser) {
		t.Fatal("deleted last user", err)
	}
	if err := users.Create(t.Context(), "second@example.com", "second password"); err != nil {
		t.Fatal(err)
	}
	if err := users.Create(t.Context(), "not-an-email", "test password"); err == nil {
		t.Fatal("invalid email accepted")
	}
	if err := users.Create(t.Context(), "bad@example.com", "short"); !errors.Is(err, auth.ErrPasswordInvalid) {
		t.Fatal("invalid password accepted", err)
	}
	authentication, err := auth.New(auth.Config{SessionSecret: strings.Repeat("s", 32), ClientID: "id", ClientSecret: "secret", RedirectURL: "https://example.com/auth/google/callback"}, database)
	if err != nil {
		t.Fatal(err)
	}
	user, err := authentication.AuthenticatePassword(t.Context(), "SECOND@EXAMPLE.COM", "second password", "ip")
	if err != nil || user.Email != "second@example.com" || user.AccountID < 1 {
		t.Fatal("email login failed", err)
	}
	session, err := authentication.NewSession(user)
	if err != nil {
		t.Fatal(err)
	}
	if authorized, err := database.IsAuthorizedEmail(t.Context(), user.Email); err != nil || !authorized {
		t.Fatal("Google not authorized automatically", err)
	}
	if err := users.ChangePassword(t.Context(), user.AccountID, "new second password"); err != nil {
		t.Fatal(err)
	}
	if _, valid, _ := authentication.ValidateSession(t.Context(), session); valid {
		t.Fatal("password reset did not revoke session")
	}
	if _, err := authentication.AuthenticatePassword(t.Context(), user.Email, "new second password", "ip"); err != nil {
		t.Fatal("new password failed", err)
	}
	if err := users.Delete(t.Context(), user.AccountID); err != nil {
		t.Fatal(err)
	}
	if authorized, _ := database.IsAuthorizedEmail(t.Context(), user.Email); authorized {
		t.Fatal("deleted user still allowed Google access")
	}
	if err := users.Create(t.Context(), user.Email, "second password"); err != nil {
		t.Fatal(err)
	}
	if _, valid, _ := authentication.ValidateSession(t.Context(), session); valid {
		t.Fatal("recreated account revived session")
	}
	if err := users.ChangePassword(t.Context(), 999, "test password"); !errors.Is(err, application.ErrUserNotFound) {
		t.Fatal("changed missing user", err)
	}
}
