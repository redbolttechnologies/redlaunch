package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestServiceDeletionVolumesAreImmutableAndSurviveReopen(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.BeginServiceDeletion(ctx, 7, "db", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := database.SetServiceDeletionVolumes(ctx, 7, "db", []string{"data"}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetServiceDeletionVolumes(ctx, 7, "db", []string{"other"}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	intent, err := database.GetServiceDeletion(ctx, 7, "db")
	if err != nil || !reflect.DeepEqual(intent.Volumes, []string{"data"}) {
		t.Fatalf("intent = %#v, %v", intent, err)
	}
}

func TestServiceDeletionVolumeMigrationPreservesLegacyIntent(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.BeginServiceDeletion(ctx, 7, "db", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateServiceDeletion(ctx, 7, "db", "remove", "failed", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `ALTER TABLE service_deletion_intents DROP COLUMN volumes; DELETE FROM schema_migrations WHERE version = 27`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	intent, err := database.GetServiceDeletion(ctx, 7, "db")
	if err != nil || intent.Stage != "remove" || intent.Volumes != nil {
		t.Fatalf("legacy intent = %#v, %v", intent, err)
	}
	if err := database.SetServiceDeletionVolumes(ctx, 7, "db", []string{"new"}); err != nil {
		t.Fatal(err)
	}
	intent, err = database.GetServiceDeletion(ctx, 7, "db")
	if err != nil || intent.Volumes != nil {
		t.Fatalf("legacy selection changed: %#v, %v", intent, err)
	}
}
