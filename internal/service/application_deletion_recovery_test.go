package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/store"
)

func r06TestContext(t *testing.T) context.Context {
	t.Helper()
	return t.Context()
}

// TestApplicationDeletionRetryAfterFolderRemovedIsIdempotent covers the R06
// crash window: a crash after os.RemoveAll and before the final checkpoint
// must let a retry complete instead of returning ErrNotFound.
func TestApplicationDeletionRetryAfterFolderRemovedIsIdempotent(t *testing.T) {
	ctx := r06TestContext(t)
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "redlaunch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	item, err := database.Create(ctx, application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	applications, err := NewApplications(database, projectsRoot, &serviceRuntimeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(projectsRoot, applicationsDir, item.FolderName)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := database.BeginApplicationDeletion(ctx, item, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateApplicationDeletion(ctx, item.ID, applicationDeletionStageFolder, applicationDeletionStateRunning, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteApplication(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}

	if err := applications.DeleteApplicationWithProgress(ctx, item.ID, nil); err != nil {
		t.Fatalf("retry after folder removal: %v", err)
	}
	intent, err := database.GetApplicationDeletion(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.State != "complete" || intent.Stage != applicationDeletionStageComplete {
		t.Fatalf("intent after retry = %#v, want complete", intent)
	}
}

// TestApplicationCreateRejectsReservedFolderName ensures a folder referenced
// by an incomplete deletion tombstone cannot be reused until deletion is
// durably complete.
func TestApplicationCreateRejectsReservedFolderName(t *testing.T) {
	ctx := r06TestContext(t)
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "redlaunch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	item, err := database.Create(ctx, application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	applications, err := NewApplications(database, projectsRoot, &serviceRuntimeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(projectsRoot, applicationsDir, item.FolderName)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := database.BeginApplicationDeletion(ctx, item, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateApplicationDeletion(ctx, item.ID, applicationDeletionStageFolder, applicationDeletionStateRunning, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteApplication(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}

	if _, err := applications.Create(ctx, "Replacement", "status-page"); !errors.Is(err, application.ErrApplicationDeletionInProgress) {
		t.Fatalf("Create(reserved folder) error = %v, want %v", err, application.ErrApplicationDeletionInProgress)
	}

	// After the retry completes the tombstone, the folder name is reusable.
	if err := applications.DeleteApplicationWithProgress(ctx, item.ID, nil); err != nil {
		t.Fatalf("retry to release folder reservation: %v", err)
	}
	replacement, err := applications.Create(ctx, "Replacement", "status-page")
	if err != nil {
		t.Fatalf("Create(after completed deletion) error = %v", err)
	}
	if replacement.FolderName != "status-page" {
		t.Fatalf("replacement folder = %q, want status-page", replacement.FolderName)
	}
}

// TestApplicationDeletionRetryDoesNotRemoveReplacementFolder is the R06
// defense-in-depth check: even if a replacement folder exists (for example,
// created before the reservation guard), retrying the old deletion must not
// delete the replacement folder.
func TestApplicationDeletionRetryDoesNotRemoveReplacementFolder(t *testing.T) {
	ctx := r06TestContext(t)
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "redlaunch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	item, err := database.Create(ctx, application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	applications, err := NewApplications(database, projectsRoot, &serviceRuntimeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(projectsRoot, applicationsDir, item.FolderName)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := database.BeginApplicationDeletion(ctx, item, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateApplicationDeletion(ctx, item.ID, applicationDeletionStageFolder, applicationDeletionStateRunning, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteApplication(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	// Simulate a replacement created outside the reservation guard: a new
	// metadata row reusing the folder name with its own folder contents.
	replacement, err := database.Create(ctx, application.Application{Name: "Replacement", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(directory, "replacement-sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("replacement data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := applications.DeleteApplicationWithProgress(ctx, item.ID, nil); err == nil {
		t.Fatal("retry over replacement folder succeeded, want refusal")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("replacement sentinel stat error = %v, want replacement data preserved", err)
	}
	if _, err := database.Get(ctx, replacement.ID); err != nil {
		t.Fatalf("replacement metadata after refused retry: %v", err)
	}
}

// TestApplicationMutationRejectsActiveDeletionIntent ensures service-level
// mutations fail while a deletion intent is active.
func TestApplicationMutationRejectsActiveDeletionIntent(t *testing.T) {
	ctx := r06TestContext(t)
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "redlaunch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	item, err := database.Create(ctx, application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	applications, err := NewApplications(database, projectsRoot, &serviceRuntimeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(projectsRoot, applicationsDir, item.FolderName)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := database.BeginApplicationDeletion(ctx, item, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	if err := applications.DeleteServiceWithProgress(ctx, item.ID, "web", nil); !errors.Is(err, application.ErrApplicationDeletionInProgress) {
		t.Fatalf("DeleteService during deletion error = %v, want %v", err, application.ErrApplicationDeletionInProgress)
	}
}
