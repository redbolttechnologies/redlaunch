package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

func TestListServicesReturnsDatabaseSnapshotWhileWriteHoldsLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services: []application.Service{{
			ID:            1,
			ApplicationID: 7,
			Name:          "db",
			Type:          application.ServiceTypePostgreSQL,
		}},
	}
	runner := &serviceRuntimeRunner{runtime: []compose.ServiceRuntime{{
		ServiceName:   "db",
		ContainerName: "redbolt-7-db",
		Status:        "Up 9 minutes",
	}}}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, applicationsDir, "status-page"), 0o750); err != nil {
		t.Fatal(err)
	}

	held, err := applications.projectLocks.acquire(context.Background(), applicationProjectLockKey(7))
	if err != nil {
		t.Fatal(err)
	}
	defer held.release()

	start := time.Now()
	services, err := applications.ListServices(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ListServices() blocked for %v while write held the lock", elapsed)
	}
	if len(services) != 1 || services[0].Name != "db" {
		t.Fatalf("ListServices() = %#v, want database snapshot", services)
	}
	if services[0].Status == "Up 9 minutes" {
		t.Fatalf("ListServices() enriched Docker status while lock was held; want database snapshot")
	}
}
