package store

import (
	"errors"
	"testing"

	"redlaunch/internal/application"
)

func TestStoreUpdatesServiceDatabaseUser(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.Create(t.Context(), application.Application{Name: "Shop", FolderName: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := database.CreateService(t.Context(), application.Service{
		ApplicationID: created.ID,
		Name:          "db",
		Type:          application.ServiceTypePostgreSQL,
		DatabaseName:  "shop",
		DatabaseUser:  "shop",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := database.UpdateServiceDatabaseUser(t.Context(), created.ID, "db", "shopowner")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != service.ID || updated.DatabaseUser != "shopowner" || updated.DatabaseName != "shop" {
		t.Fatalf("updated service = %#v, want user shopowner with database kept", updated)
	}
	services, err := database.ListServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].DatabaseUser != "shopowner" {
		t.Fatalf("ListServices() after update = %#v, want updated user", services)
	}
	if _, err := database.UpdateServiceDatabaseUser(t.Context(), created.ID, "missing", "shopowner"); !errors.Is(err, application.ErrServiceNotFound) {
		t.Fatalf("UpdateServiceDatabaseUser(missing) error = %v, want %v", err, application.ErrServiceNotFound)
	}
}
