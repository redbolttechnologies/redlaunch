package store

import (
	"testing"

	"redlaunch/internal/application"
)

func openRetentionTestStore(t *testing.T) *Store {
	t.Helper()
	database, err := Open(t.Context(), t.TempDir()+"/retention.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestStoreRegistryDefaultKeepDefaultsToFive(t *testing.T) {
	database := openRetentionTestStore(t)
	got, err := database.GetRegistryDefaultKeep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != application.DefaultRegistryKeepCount {
		t.Fatalf("GetRegistryDefaultKeep() = %d, want %d", got, application.DefaultRegistryKeepCount)
	}
}

func TestStoreRegistryDefaultKeepRoundTrip(t *testing.T) {
	database := openRetentionTestStore(t)
	if err := database.SetRegistryDefaultKeep(t.Context(), 3); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetRegistryDefaultKeep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Fatalf("GetRegistryDefaultKeep() = %d, want 3", got)
	}
	if err := database.SetRegistryDefaultKeep(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	got, err = database.GetRegistryDefaultKeep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("GetRegistryDefaultKeep() = %d, want 0 unlimited", got)
	}
	if err := database.SetRegistryDefaultKeep(t.Context(), 101); err == nil {
		t.Fatal("SetRegistryDefaultKeep(101) error = nil, want invalid")
	}
}

func TestStoreRegistryRetentionPolicies(t *testing.T) {
	database := openRetentionTestStore(t)
	if err := database.SetRegistryRetentionPolicy(t.Context(), "acme-app", 3); err != nil {
		t.Fatal(err)
	}
	keep, ok, err := database.GetRegistryRetentionPolicy(t.Context(), "acme-app")
	if err != nil || !ok || keep != 3 {
		t.Fatalf("GetRegistryRetentionPolicy() = (%d, %v, %v), want (3, true, nil)", keep, ok, err)
	}
	if _, ok, err := database.GetRegistryRetentionPolicy(t.Context(), "other"); err != nil || ok {
		t.Fatalf("GetRegistryRetentionPolicy(missing) = (%v, %v), want (false, nil)", ok, err)
	}
	policies, err := database.ListRegistryRetentionPolicies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || policies[0].Repository != "acme-app" || policies[0].KeepCount != 3 {
		t.Fatalf("ListRegistryRetentionPolicies() = %#v, want one acme-app policy", policies)
	}
	if err := database.DeleteRegistryRetentionPolicy(t.Context(), "acme-app"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := database.GetRegistryRetentionPolicy(t.Context(), "acme-app"); err != nil || ok {
		t.Fatalf("GetRegistryRetentionPolicy(after delete) = (%v, %v), want (false, nil)", ok, err)
	}
	if err := database.SetRegistryRetentionPolicy(t.Context(), "../escape", 3); err == nil {
		t.Fatal("SetRegistryRetentionPolicy(traversal) error = nil, want invalid")
	}
}
