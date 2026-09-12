package store

import (
	"errors"
	"os"
	"testing"
	"time"

	"redlaunch/internal/application"
)

func TestPhaseZeroMigrationFixtureCoversLegacyMetadata(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	fixture, err := os.ReadFile("testdata/phase0_migration.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(t.Context(), string(fixture)); err != nil {
		t.Fatalf("load Phase 0 migration fixture: %v", err)
	}

	item, err := database.Get(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if item.FolderName != "proxy" {
		t.Fatalf("fixture application folder = %q, want colliding proxy name", item.FolderName)
	}
	services, err := database.ListServices(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 2 || services[0].Type != application.ServiceTypePostgreSQL || services[1].Type != application.ServiceTypePostgreSQL {
		t.Fatalf("fixture services = %#v, want two PostgreSQL services", services)
	}
	domains, err := database.ListDomains(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 1 {
		t.Fatalf("fixture domains = %#v, want one", domains)
	}
	routings, err := database.ListRoutings(t.Context(), item.ID, domains[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(routings) != 1 || routings[0].ServiceName != "db-primary" || routings[0].ServicePort != 80 {
		t.Fatalf("fixture routings = %#v, want db-primary route", routings)
	}
	schedule, err := database.GetBackupSchedule(t.Context(), services[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !schedule.Enabled {
		t.Fatal("fixture backup timer is disabled, want enabled")
	}
}

func TestStoreApplicationRoundTrip(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})

	created, err := database.Create(t.Context(), application.Application{
		Name:       "Status page",
		FolderName: "status-page",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID < 1 {
		t.Fatalf("created application ID = %d, want positive ID", created.ID)
	}
	if created.CreatedAt.IsZero() {
		t.Fatal("created application timestamp is zero")
	}

	got, err := database.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || got.Name != created.Name || got.FolderName != created.FolderName {
		t.Fatalf("Get() = %#v, want %#v", got, created)
	}

	applications, err := database.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(applications) != 1 || applications[0].ID != created.ID {
		t.Fatalf("List() = %#v, want one application with ID %d", applications, created.ID)
	}

	if _, err := database.Get(t.Context(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want %v", err, ErrNotFound)
	}
	if _, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "another-folder"}); !errors.Is(err, application.ErrAlreadyExists) {
		t.Fatalf("Create(duplicate name) error = %v, want %v", err, application.ErrAlreadyExists)
	}
	if _, err := database.Create(t.Context(), application.Application{Name: "Another page", FolderName: "status-page"}); !errors.Is(err, application.ErrAlreadyExists) {
		t.Fatalf("Create(duplicate folder) error = %v, want %v", err, application.ErrAlreadyExists)
	}

	services, err := database.ListServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if services != nil {
		t.Fatalf("ListServices() = %#v, want nil before service creation", services)
	}
}

func TestStoreAuthorizedEmailsRoundTrip(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if err := database.AddAuthorizedEmail(t.Context(), " Admin@Example.COM "); err != nil {
		t.Fatal(err)
	}
	if err := database.AddAuthorizedEmail(t.Context(), "admin@example.com"); !errors.Is(err, application.ErrAuthorizedEmailAlreadyExists) {
		t.Fatalf("AddAuthorizedEmail(duplicate) error = %v, want %v", err, application.ErrAuthorizedEmailAlreadyExists)
	}

	for _, email := range []string{"admin@example.com", "ADMIN@EXAMPLE.COM"} {
		authorized, err := database.IsAuthorizedEmail(t.Context(), email)
		if err != nil {
			t.Fatal(err)
		}
		if !authorized {
			t.Fatalf("IsAuthorizedEmail(%q) = false, want true", email)
		}
	}
	authorized, err := database.IsAuthorizedEmail(t.Context(), "other@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if authorized {
		t.Fatal("IsAuthorizedEmail(other@example.com) = true, want false")
	}

	emails, err := database.ListAuthorizedEmails(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 1 || emails[0] != "admin@example.com" {
		t.Fatalf("ListAuthorizedEmails() = %#v, want [admin@example.com]", emails)
	}
}

func TestStoreAuthorizedEmailsRejectInvalidAddress(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if err := database.AddAuthorizedEmail(t.Context(), "not-an-email"); !errors.Is(err, application.ErrEmailInvalid) {
		t.Fatalf("AddAuthorizedEmail(invalid) error = %v, want %v", err, application.ErrEmailInvalid)
	}
}

func TestStoreRedlaunchDomainsRoundTrip(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	domains, err := database.ListRedlaunchDomains(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 0 {
		t.Fatalf("initial Redlaunch domains = %#v, want none", domains)
	}

	first, err := database.CreateRedlaunchDomain(t.Context(), application.RedlaunchDomain{Name: "admin.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID < 1 || first.Name != "admin.example.com" {
		t.Fatalf("created Redlaunch domain = %#v, want persisted identity", first)
	}
	second, err := database.CreateRedlaunchDomain(t.Context(), application.RedlaunchDomain{Name: "redlaunch.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID < 1 || second.Name != "redlaunch.example.com" {
		t.Fatalf("created Redlaunch domain = %#v, want persisted identity", second)
	}

	domains, err = database.ListRedlaunchDomains(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 2 || domains[0] != first || domains[1] != second {
		t.Fatalf("ListRedlaunchDomains() = %#v, want [%#v %#v]", domains, first, second)
	}
	if _, err := database.CreateRedlaunchDomain(t.Context(), application.RedlaunchDomain{Name: "ADMIN.EXAMPLE.COM"}); !errors.Is(err, application.ErrDomainAlreadyExists) {
		t.Fatalf("CreateRedlaunchDomain(duplicate) error = %v, want %v", err, application.ErrDomainAlreadyExists)
	}

	if err := database.DeleteRedlaunchDomain(t.Context(), "admin.example.com"); err != nil {
		t.Fatal(err)
	}
	domains, err = database.ListRedlaunchDomains(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 1 || domains[0] != second {
		t.Fatalf("ListRedlaunchDomains() after deletion = %#v, want [%#v]", domains, second)
	}
	if err := database.DeleteRedlaunchDomain(t.Context(), "admin.example.com"); !errors.Is(err, application.ErrDomainNotFound) {
		t.Fatalf("DeleteRedlaunchDomain(missing) error = %v, want %v", err, application.ErrDomainNotFound)
	}
}

func TestStoreGitHubActionsRoundTrip(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	want := application.GitHubActionsIntegration{
		ApplicationID:  created.ID,
		Repository:     "acme/status-page",
		Branch:         "master",
		Dockerfile:     "Dockerfile",
		BuildContext:   ".",
		ServiceName:    "web",
		ImageName:      "status-page/web",
		ServerHost:     "203.0.113.10",
		ServerPort:     2222,
		SSHUsername:    "redlaunch-deploy",
		PublicKey:      "ssh-ed25519 AAAATEST",
		KeyFingerprint: "SHA256:fingerprint",
	}
	got, err := database.SaveGitHubActions(t.Context(), want)
	if err != nil {
		t.Fatal(err)
	}
	if got.ApplicationID != want.ApplicationID || got.Repository != want.Repository || got.PublicKey != want.PublicKey {
		t.Fatalf("saved integration = %#v, want %#v", got, want)
	}
	loaded, err := database.GetGitHubActions(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Repository != want.Repository || loaded.ImageName != want.ImageName || loaded.ServerPort != 2222 {
		t.Fatalf("loaded integration = %#v, want %#v", loaded, want)
	}
	if _, err := database.GetGitHubActions(t.Context(), 999); !errors.Is(err, application.ErrGitHubActionsNotConfigured) {
		t.Fatalf("missing integration error = %v, want %v", err, application.ErrGitHubActionsNotConfigured)
	}
	if err := database.DeleteGitHubActions(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetGitHubActions(t.Context(), created.ID); !errors.Is(err, application.ErrGitHubActionsNotConfigured) {
		t.Fatalf("deleted integration error = %v, want %v", err, application.ErrGitHubActionsNotConfigured)
	}
}

func TestStoreListsApplicationServiceMetadata(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := "2026-08-29T10:00:00Z"
	if _, err := database.db.ExecContext(t.Context(), `
		INSERT INTO services (application_id, name, created_at)
		VALUES (?, ?, ?)`, created.ID, "web", createdAt); err != nil {
		t.Fatal(err)
	}

	services, err := database.ListServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("ListServices() returned %d services, want 1", len(services))
	}
	if services[0].ApplicationID != created.ID || services[0].Name != "web" {
		t.Fatalf("ListServices() = %#v, want application %d web service", services, created.ID)
	}
	if services[0].CreatedAt.Format(time.RFC3339) != createdAt {
		t.Fatalf("service CreatedAt = %s, want %s", services[0].CreatedAt.Format(time.RFC3339), createdAt)
	}

	applications, err := database.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(applications) != 1 || applications[0].ServiceCount != 1 {
		t.Fatalf("List() = %#v, want one application with one service", applications)
	}
}

func TestStoreCreatesPostgreSQLServiceMetadataWithoutSecret(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}

	service, err := database.CreateService(t.Context(), application.Service{
		ApplicationID:   created.ID,
		Name:            "db",
		Type:            application.ServiceTypePostgreSQL,
		PostgresVersion: "17",
		DatabaseName:    "Status page",
		DatabaseUser:    "appuser",
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.ID < 1 {
		t.Fatalf("created service ID = %d, want positive ID", service.ID)
	}

	services, err := database.ListServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("ListServices() returned %d services, want 1", len(services))
	}
	got := services[0]
	if got.ID != service.ID || got.ApplicationID != created.ID || got.Name != "db" {
		t.Fatalf("service identity = %#v, want %#v", got, service)
	}
	if got.Type != application.ServiceTypePostgreSQL || got.PostgresVersion != "17" || got.DatabaseName != "Status page" || got.DatabaseUser != "appuser" {
		t.Fatalf("service metadata = %#v, want PostgreSQL metadata", got)
	}

	if _, err := database.CreateService(t.Context(), application.Service{ApplicationID: created.ID, Name: "db"}); !errors.Is(err, application.ErrServiceAlreadyExists) {
		t.Fatalf("CreateService(duplicate) error = %v, want %v", err, application.ErrServiceAlreadyExists)
	}
}

func TestStoreCreatesRedisServiceMetadataWithoutSecret(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}

	service, err := database.CreateService(t.Context(), application.Service{
		ApplicationID:      created.ID,
		Name:               "redis",
		Type:               application.ServiceTypeRedis,
		RedisVersion:       "7.2",
		RedisPort:          "6380",
		RedisPersistToDisk: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	services, err := database.ListServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("ListServices() returned %d services, want 1", len(services))
	}
	got := services[0]
	if got.ID != service.ID || got.ApplicationID != created.ID || got.Name != "redis" {
		t.Fatalf("service identity = %#v, want %#v", got, service)
	}
	if got.Type != application.ServiceTypeRedis || got.RedisVersion != "7.2" || got.RedisPort != "6380" || !got.RedisPersistToDisk {
		t.Fatalf("service metadata = %#v, want Redis metadata", got)
	}
}

func TestStoreCreateServicesIsAtomic(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	services, err := database.CreateServices(t.Context(), []application.Service{
		{ApplicationID: created.ID, Name: "web", Type: application.ServiceTypeApplication, ImageName: "nginx:1.27"},
		{ApplicationID: created.ID, Name: "worker", Type: application.ServiceTypeApplication, ImageName: "busybox:1.36"},
	})
	if err != nil || len(services) != 2 {
		t.Fatalf("CreateServices() = (%#v, %v), want two services", services, err)
	}

	if _, err := database.CreateServices(t.Context(), []application.Service{
		{ApplicationID: created.ID, Name: "transient", Type: application.ServiceTypeApplication},
		{ApplicationID: created.ID, Name: "web", Type: application.ServiceTypeApplication},
	}); !errors.Is(err, application.ErrServiceAlreadyExists) {
		t.Fatalf("CreateServices(duplicate) error = %v, want %v", err, application.ErrServiceAlreadyExists)
	}
	loaded, err := database.ListServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("services after atomic failure = %#v, want original two services", loaded)
	}
	for _, item := range loaded {
		if item.Name == "transient" {
			t.Fatal("atomic service transaction left the first insert behind")
		}
	}
}

func TestStoreCreatesApplicationContainerMetadata(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}

	wantImage := "localhost:5000/web:latest"
	service, err := database.CreateService(t.Context(), application.Service{
		ApplicationID: created.ID,
		Name:          "web",
		Type:          application.ServiceTypeApplication,
		ImageName:     wantImage,
	})
	if err != nil {
		t.Fatal(err)
	}

	services, err := database.ListServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("ListServices() returned %d services, want 1", len(services))
	}
	got := services[0]
	if got.ID != service.ID || got.Type != application.ServiceTypeApplication || got.Name != "web" || got.ImageName != wantImage {
		t.Fatalf("application service metadata = %#v, want image %q", got, wantImage)
	}
}

func TestStoreDeletesServiceMetadataByApplicationAndName(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateService(t.Context(), application.Service{ApplicationID: created.ID, Name: "db"}); err != nil {
		t.Fatal(err)
	}

	if err := database.DeleteService(t.Context(), created.ID, "db"); err != nil {
		t.Fatal(err)
	}
	services, err := database.ListServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 {
		t.Fatalf("ListServices() after deletion = %#v, want no services", services)
	}
	if err := database.DeleteService(t.Context(), created.ID, "db"); !errors.Is(err, application.ErrServiceNotFound) {
		t.Fatalf("DeleteService(missing) error = %v, want %v", err, application.ErrServiceNotFound)
	}
}

func TestStoreDeletesApplicationAndCascadesRelatedMetadata(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := database.CreateService(t.Context(), application.Service{ApplicationID: created.ID, Name: "db"})
	if err != nil {
		t.Fatal(err)
	}
	domain, err := database.CreateDomain(t.Context(), application.Domain{ApplicationID: created.ID, Name: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateRouting(t.Context(), application.Routing{
		ApplicationID: created.ID,
		DomainID:      domain.ID,
		Path:          "/",
		ServiceName:   "db",
		ServicePath:   "/",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveBackupSchedule(t.Context(), application.BackupSchedule{
		ServiceID:      service.ID,
		ScheduleType:   application.BackupScheduleDaily,
		RetentionDays:  14,
		BackupLocation: "/backups",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateBackup(t.Context(), application.Backup{
		ServiceID: service.ID,
		FileName:  "backup.sql",
	}); err != nil {
		t.Fatal(err)
	}

	if err := database.DeleteApplication(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Get(t.Context(), created.ID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("Get(after application deletion) error = %v, want %v", err, application.ErrNotFound)
	}
	services, err := database.ListServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 {
		t.Fatalf("ListServices(after application deletion) = %#v, want no services", services)
	}
	domains, err := database.ListDomains(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 0 {
		t.Fatalf("ListDomains(after application deletion) = %#v, want no domains", domains)
	}
	backups, err := database.ListBackups(t.Context(), service.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("ListBackups(after application deletion) = %#v, want no backups", backups)
	}
	if _, err := database.GetBackupSchedule(t.Context(), service.ID); !errors.Is(err, application.ErrBackupScheduleNotFound) {
		t.Fatalf("GetBackupSchedule(after application deletion) error = %v, want %v", err, application.ErrBackupScheduleNotFound)
	}
	if err := database.DeleteApplication(t.Context(), created.ID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("DeleteApplication(missing) error = %v, want %v", err, application.ErrNotFound)
	}
}

func TestStoreCreatesListsAndDeletesApplicationDomains(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}

	domain, err := database.CreateDomain(t.Context(), application.Domain{
		ApplicationID: created.ID,
		Name:          "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if domain.ID < 1 || domain.ApplicationID != created.ID || domain.Name != "example.com" {
		t.Fatalf("created domain = %#v, want persisted identity", domain)
	}

	domains, err := database.ListDomains(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 1 || domains[0] != domain {
		t.Fatalf("ListDomains() = %#v, want %#v", domains, []application.Domain{domain})
	}
	if _, err := database.CreateDomain(t.Context(), application.Domain{ApplicationID: created.ID, Name: "EXAMPLE.COM"}); !errors.Is(err, application.ErrDomainAlreadyExists) {
		t.Fatalf("CreateDomain(duplicate) error = %v, want %v", err, application.ErrDomainAlreadyExists)
	}

	if err := database.DeleteDomain(t.Context(), created.ID, "example.com"); err != nil {
		t.Fatal(err)
	}
	domains, err = database.ListDomains(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 0 {
		t.Fatalf("ListDomains() after deletion = %#v, want no domains", domains)
	}
	if err := database.DeleteDomain(t.Context(), created.ID, "example.com"); !errors.Is(err, application.ErrDomainNotFound) {
		t.Fatalf("DeleteDomain(missing) error = %v, want %v", err, application.ErrDomainNotFound)
	}
}

func TestStoreCreatesListsUpdatesAndDeletesApplicationRoutings(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.Create(t.Context(), application.Application{Name: "Status page", FolderName: "status-page"})
	if err != nil {
		t.Fatal(err)
	}
	domain, err := database.CreateDomain(t.Context(), application.Domain{ApplicationID: created.ID, Name: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	routing, err := database.CreateRouting(t.Context(), application.Routing{
		ApplicationID: created.ID,
		DomainID:      domain.ID,
		Subdomain:     "api",
		Path:          "/register",
		ServiceName:   "identity",
		ServicePort:   3000,
		ServicePath:   "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if routing.ID < 1 {
		t.Fatalf("created routing ID = %d, want positive ID", routing.ID)
	}

	routings, err := database.ListRoutings(t.Context(), created.ID, domain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(routings) != 1 || routings[0].ID != routing.ID || routings[0].DomainName != "example.com" || routings[0].Subdomain != "api" || routings[0].Path != "/register" || routings[0].ServiceName != "identity" || routings[0].ServicePort != 3000 || routings[0].ServicePath != "/" {
		t.Fatalf("ListRoutings() = %#v, want persisted routing with domain name", routings)
	}
	if _, err := database.CreateRouting(t.Context(), application.Routing{ApplicationID: created.ID, DomainID: domain.ID, Subdomain: "API", Path: "/register", ServiceName: "other", ServicePath: "/"}); !errors.Is(err, application.ErrRoutingAlreadyExists) {
		t.Fatalf("CreateRouting(duplicate) error = %v, want %v", err, application.ErrRoutingAlreadyExists)
	}

	routing.Subdomain = ""
	routing.Path = "/"
	routing.ServiceName = "frontend"
	routing.ServicePort = 8080
	routing.ServicePath = "/app"
	if err := database.UpdateRouting(t.Context(), routing); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetRouting(t.Context(), created.ID, domain.ID, routing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DomainName != "example.com" || got.Subdomain != "" || got.Path != "/" || got.ServiceName != "frontend" || got.ServicePort != 8080 || got.ServicePath != "/app" {
		t.Fatalf("GetRouting() = %#v, want updated routing", got)
	}

	all, err := database.ListAllRoutings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != routing.ID {
		t.Fatalf("ListAllRoutings() = %#v, want one routing", all)
	}
	if err := database.DeleteRouting(t.Context(), created.ID, domain.ID, routing.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteRouting(t.Context(), created.ID, domain.ID, routing.ID); !errors.Is(err, application.ErrRoutingNotFound) {
		t.Fatalf("DeleteRouting(missing) error = %v, want %v", err, application.ErrRoutingNotFound)
	}
}

func TestStoreMigrationsAreIdempotent(t *testing.T) {
	path := t.TempDir() + "/redlaunch.db"
	first, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })

	applications, err := second.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if applications != nil {
		t.Fatalf("List() = %#v, want nil for an empty database", applications)
	}
}
