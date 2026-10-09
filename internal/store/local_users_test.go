package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"redlaunch/internal/application"
)

func TestLocalUsersMigrationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	database, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := database.HasLocalUsers(t.Context()); err != nil || exists {
		t.Fatal("new database has users", err)
	}
	if err := database.CreateLocalUser(t.Context(), "ADMIN@EXAMPLE.COM", "test hash"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateLocalUser(t.Context(), "admin@example.com", "replacement hash"); !errors.Is(err, application.ErrLocalUserExists) {
		t.Fatal("duplicate was accepted", err)
	}
	if authorized, err := database.IsAuthorizedEmail(t.Context(), "admin@example.com"); err != nil || !authorized {
		t.Fatal("password user not authorized for Google", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("unsafe database permissions", err)
		}
	}
	user, err := database.GetLocalUser(t.Context(), "admin@example.com")
	if err != nil || user.PasswordHash != "test hash" || user.CredentialVersion != 1 {
		t.Fatal("credential changed on reopen", err)
	}
	if err := database.ResetLocalPassword(t.Context(), "Admin@Example.com", "new test hash"); err != nil {
		t.Fatal(err)
	}
	user, err = database.GetLocalUser(t.Context(), "admin@example.com")
	if err != nil || user.PasswordHash != "new test hash" || user.CredentialVersion != 2 {
		t.Fatal("reset not atomic", err)
	}
	if err := database.ResetLocalPassword(t.Context(), "missing", "hash"); !errors.Is(err, application.ErrLocalUserNotFound) {
		t.Fatal("reset created account", err)
	}
	if _, err := database.GetLocalUser(t.Context(), "' OR 1=1 --"); !errors.Is(err, application.ErrLocalUserNotFound) {
		t.Fatal("unsafe lookup", err)
	}
	if authorized, err := database.IsAuthorizedEmail(t.Context(), "admin@example.com"); err != nil || !authorized {
		t.Fatal("Google allowlist changed", err)
	}
}

func TestUnifiedUsersUpgradePreservesLegacyAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	database, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`DROP TABLE users; DELETE FROM schema_migrations WHERE version=24;
 INSERT INTO local_users(username,password_hash,credential_version,created_at,updated_at) VALUES('admin','legacy hash',3,'2026-01-01','2026-01-01');
 INSERT INTO authorized_emails(email,created_at) VALUES('google@example.com','2026-01-01');`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	database, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	legacy, err := database.GetLocalUser(t.Context(), "admin")
	if err != nil || legacy.PasswordHash != "legacy hash" || legacy.CredentialVersion != 3 || legacy.Email != "" {
		t.Fatal("legacy credential changed", err)
	}
	if err := database.AssignUserEmail(t.Context(), legacy.ID, "ADMIN@EXAMPLE.COM"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetLocalUser(t.Context(), "admin"); !errors.Is(err, application.ErrUserNotFound) {
		t.Fatal("old username still enabled", err)
	}
	migrated, err := database.GetLocalUser(t.Context(), "admin@example.com")
	if err != nil || migrated.PasswordHash != "legacy hash" || migrated.CredentialVersion != 4 {
		t.Fatal("email conversion changed password", err)
	}
	for _, email := range []string{"admin@example.com", "google@example.com"} {
		authorized, err := database.IsAuthorizedEmail(t.Context(), email)
		if err != nil || !authorized {
			t.Fatal("shared Google membership lost", err)
		}
	}
	if err := database.DeleteUser(t.Context(), legacy.ID); err != nil {
		t.Fatal(err)
	}
	database.Close()
	database, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	users, err := database.ListUsers(t.Context())
	if err != nil || len(users) != 1 || users[0].Email != "google@example.com" {
		t.Fatal("migration resurrected deleted users", err)
	}
}

func TestOpenProtectsExistingSQLiteFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	database, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(file, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("existing SQLite file not protected", err)
		}
	}
}
