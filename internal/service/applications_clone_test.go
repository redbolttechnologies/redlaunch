package service

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/store"
)

func seedCloneSource(t *testing.T, root string, repository *applicationRepositoryStub, source application.Application) {
	t.Helper()
	repository.applications = append(repository.applications, source)
	directory := filepath.Join(root, applicationsDir, source.FolderName)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	compose := `services:
  web:
    image: nginx:1.27
    container_name: redbolt-` + strconv.FormatInt(source.ID, 10) + `-web
    restart: unless-stopped
    env_file:
      - vars.env
      - secrets.env
    networks:
      - default
    labels:
      - "redlaunch.managed=true"
  db:
    image: postgres:17
    container_name: redbolt-` + strconv.FormatInt(source.ID, 10) + `-db
    restart: unless-stopped
    env_file:
      - vars.env
      - secrets.env
      - db.vars.env
      - db.secrets.env
    volumes:
      - db_data:/var/lib/postgresql/data
    networks:
      - default
    labels:
      - "redlaunch.managed=true"

networks:
  default:
    external: true
    name: redlaunch-common

volumes:
  db_data:
    name: redbolt-` + strconv.FormatInt(source.ID, 10) + `-db-data
`
	if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, varsEnvFile), []byte("# application settings\nAPP_NAME=Shop\nPORT=8080 # local port\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, secretsEnvFile), []byte("API_TOKEN=super-secret\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "db.vars.env"), []byte("POSTGRES_DB=shop\nPOSTGRES_USER=shop\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "db.secrets.env"), []byte("POSTGRES_PASSWORD=shop-secret\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
}

func TestRewriteClonedComposeIdentifiers(t *testing.T) {
	contents := "    container_name: redbolt-7-web\n    name: redbolt-7-web-data\n    image: nginx:1.27\n"
	rewritten := rewriteClonedComposeIdentifiers(contents, 7, 9)
	if !strings.Contains(rewritten, "redbolt-9-web") || strings.Contains(rewritten, "redbolt-7-") {
		t.Fatalf("rewritten Compose = %q, want redbolt-9 identifiers", rewritten)
	}
	if !strings.Contains(rewritten, "image: nginx:1.27") {
		t.Fatalf("rewritten Compose lost unrelated fields: %q", rewritten)
	}
	if got := rewriteClonedComposeIdentifiers(contents, 7, 7); got != contents {
		t.Fatalf("same-ID rewrite changed contents: %q", got)
	}
}

func TestCloneApplicationValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root, &serviceRuntimeRunner{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := applications.CloneApplication(t.Context(), 7, application.CloneInput{Name: "", FolderName: "shop-dev"}); !errors.Is(err, application.ErrNameRequired) {
		t.Fatalf("CloneApplication(empty name) error = %v, want %v", err, application.ErrNameRequired)
	}
	if _, err := applications.CloneApplication(t.Context(), 7, application.CloneInput{Name: "Shop dev", FolderName: "a/b"}); !errors.Is(err, application.ErrFolderNameInvalid) {
		t.Fatalf("CloneApplication(bad folder) error = %v, want %v", err, application.ErrFolderNameInvalid)
	}
	if _, err := applications.CloneApplication(t.Context(), 404, application.CloneInput{Name: "Shop dev", FolderName: "shop-dev"}); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("CloneApplication(missing source) error = %v, want %v", err, application.ErrNotFound)
	}
}

func TestCloneApplicationRejectsExistingFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root, &serviceRuntimeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	seedCloneSource(t, root, repository, application.Application{ID: 7, Name: "Shop", FolderName: "shop"})

	if _, err := applications.CloneApplication(t.Context(), 7, application.CloneInput{Name: "Shop dev", FolderName: "shop"}); !errors.Is(err, application.ErrAlreadyExists) {
		t.Fatalf("CloneApplication(existing folder) error = %v, want %v", err, application.ErrAlreadyExists)
	}
	if len(repository.applications) != 1 {
		t.Fatalf("applications after rejected clone = %#v, want only the source", repository.applications)
	}
}

func TestCloneApplicationCopiesFilesServicesAndDomains(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &serviceRuntimeRunner{}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	seedCloneSource(t, root, repository, application.Application{ID: 7, Name: "Shop", FolderName: "shop"})
	repository.services = []application.Service{
		{ID: 11, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication, ImageName: "nginx:1.27"},
		{ID: 12, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL, ImageName: "postgres:17", PostgresVersion: "17", DatabaseName: "shop", DatabaseUser: "shop"},
	}
	repository.domains = []application.Domain{
		{ID: 21, ApplicationID: 7, Name: "shop.example.com"},
	}
	repository.routings = []application.Routing{
		{ID: 31, ApplicationID: 7, DomainID: 21, DomainName: "shop.example.com", Path: "/", ServiceName: "web", ServicePort: 80},
	}

	cloned, err := applications.CloneApplication(t.Context(), 7, application.CloneInput{Name: "Shop dev", FolderName: "shop-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if cloned.Name != "Shop dev" || cloned.FolderName != "shop-dev" || cloned.ID < 1 {
		t.Fatalf("CloneApplication() = %#v, want new identity", cloned)
	}

	destination := filepath.Join(root, applicationsDir, "shop-dev")
	composeContents := readServiceFile(t, filepath.Join(destination, "compose.yml"))
	for _, expected := range []string{
		"container_name: redbolt-1-web",
		"container_name: redbolt-1-db",
		"name: redbolt-1-db-data",
		"redlaunch.managed=true",
		"name: redlaunch-common",
	} {
		if !strings.Contains(composeContents, expected) {
			t.Fatalf("cloned Compose is missing %q:\n%s", expected, composeContents)
		}
	}
	if strings.Contains(composeContents, "redbolt-7-") {
		t.Fatalf("cloned Compose still references the source application:\n%s", composeContents)
	}

	if got := readServiceFile(t, filepath.Join(destination, varsEnvFile)); got != "# application settings\nAPP_NAME=Shop\nPORT=8080 # local port\n" {
		t.Fatalf("cloned vars.env = %q, want verbatim copy", got)
	}
	if got := readServiceFile(t, filepath.Join(destination, secretsEnvFile)); got != "API_TOKEN=super-secret\n" {
		t.Fatalf("cloned secrets.env = %q, want verbatim copy", got)
	}
	if got := readServiceFile(t, filepath.Join(destination, "db.vars.env")); !strings.Contains(got, "POSTGRES_DB=shop") {
		t.Fatalf("cloned db.vars.env = %q, want scoped copy", got)
	}
	if got := readServiceFile(t, filepath.Join(destination, "db.secrets.env")); !strings.Contains(got, "POSTGRES_PASSWORD=shop-secret") {
		t.Fatalf("cloned db.secrets.env = %q, want scoped copy", got)
	}

	var clonedServices []application.Service
	for _, service := range repository.services {
		if service.ApplicationID == cloned.ID {
			clonedServices = append(clonedServices, service)
		}
	}
	if len(clonedServices) != 2 {
		t.Fatalf("cloned services = %#v, want web and db", repository.services)
	}
	for _, service := range clonedServices {
		if service.ID == 11 || service.ID == 12 {
			t.Fatalf("cloned service reused source ID: %#v", service)
		}
	}
	for _, service := range clonedServices {
		if service.Name == "db" && (service.DatabaseName != "shop" || service.DatabaseUser != "shop" || service.PostgresVersion != "17") {
			t.Fatalf("cloned db metadata lost fields: %#v", service)
		}
	}

	var clonedDomains []application.Domain
	for _, domain := range repository.domains {
		if domain.ApplicationID == cloned.ID {
			clonedDomains = append(clonedDomains, domain)
		}
	}
	if len(clonedDomains) != 1 || clonedDomains[0].Name != "shop.example.com" {
		t.Fatalf("cloned domains = %#v, want the source hostname", repository.domains)
	}
	for _, routing := range repository.routings {
		if routing.ApplicationID == cloned.ID {
			t.Fatalf("clone copied routings: %#v", routing)
		}
	}

	if len(runner.actions) != 0 {
		t.Fatalf("clone started containers: %v", runner.actions)
	}
	if runner.configCalls != 1 {
		t.Fatalf("ConfigServices() calls = %d, want 1 staged validation", runner.configCalls)
	}
}

func TestCloneApplicationExtraFilesOptIn(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root, &serviceRuntimeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	seedCloneSource(t, root, repository, application.Application{ID: 7, Name: "Shop", FolderName: "shop"})
	sourceDirectory := filepath.Join(root, applicationsDir, "shop")
	if err := os.WriteFile(filepath.Join(sourceDirectory, "app.conf"), []byte("upstream web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourceDirectory, "config"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDirectory, "config", "seed.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cloned, err := applications.CloneApplication(t.Context(), 7, application.CloneInput{Name: "Shop dev", FolderName: "shop-dev"})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, applicationsDir, cloned.FolderName)
	if _, err := os.Stat(filepath.Join(destination, "app.conf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default clone copied extra files: %v", err)
	}

	clonedWithExtras, err := applications.CloneApplication(t.Context(), 7, application.CloneInput{Name: "Shop stage", FolderName: "shop-stage", CopyExtraFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	extrasDestination := filepath.Join(root, applicationsDir, clonedWithExtras.FolderName)
	if got := readServiceFile(t, filepath.Join(extrasDestination, "app.conf")); got != "upstream web\n" {
		t.Fatalf("cloned app.conf = %q, want verbatim copy", got)
	}
	if got := readServiceFile(t, filepath.Join(extrasDestination, "config", "seed.json")); got != "{}\n" {
		t.Fatalf("cloned config/seed.json = %q, want verbatim copy", got)
	}
}

func TestCloneApplicationRefusesSymlinkExtraFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root, &serviceRuntimeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	seedCloneSource(t, root, repository, application.Application{ID: 7, Name: "Shop", FolderName: "shop"})
	sourceDirectory := filepath.Join(root, applicationsDir, "shop")
	if err := os.Symlink("/etc/hostname", filepath.Join(sourceDirectory, "linked.conf")); err != nil {
		t.Fatal(err)
	}

	if _, err := applications.CloneApplication(t.Context(), 7, application.CloneInput{Name: "Shop dev", FolderName: "shop-dev", CopyExtraFiles: true}); err == nil {
		t.Fatal("CloneApplication(symlink extra) succeeded, want refusal")
	}
	if _, err := os.Stat(filepath.Join(root, applicationsDir, "shop-dev")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed clone left its destination directory behind")
	}
	if len(repository.applications) != 1 {
		t.Fatalf("applications after failed clone = %#v, want only the source", repository.applications)
	}
}

func TestCloneApplicationRollsBackOnMetadataFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{serviceErr: errors.New("boom")}
	applications, err := NewApplications(repository, root, &serviceRuntimeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	seedCloneSource(t, root, repository, application.Application{ID: 7, Name: "Shop", FolderName: "shop"})
	repository.services = []application.Service{
		{ID: 11, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication, ImageName: "nginx:1.27"},
	}

	if _, err := applications.CloneApplication(t.Context(), 7, application.CloneInput{Name: "Shop dev", FolderName: "shop-dev"}); err == nil {
		t.Fatal("CloneApplication(metadata failure) succeeded, want an error")
	}
	if _, err := os.Stat(filepath.Join(root, applicationsDir, "shop-dev")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed clone left its destination directory behind")
	}
	for _, item := range repository.applications {
		if item.FolderName == "shop-dev" {
			t.Fatalf("failed clone left its application row behind: %#v", item)
		}
	}
}

func TestCloneApplicationAgainstSQLiteStore(t *testing.T) {
	ctx := t.Context()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "redlaunch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	projectsRoot := filepath.Join(t.TempDir(), "projects")
	runner := &serviceRuntimeRunner{}
	applications, err := NewApplications(database, projectsRoot, runner)
	if err != nil {
		t.Fatal(err)
	}
	source, err := applications.Create(ctx, "Shop", "shop")
	if err != nil {
		t.Fatal(err)
	}
	sourceDirectory := filepath.Join(projectsRoot, applicationsDir, source.FolderName)
	compose := `services:
  web:
    image: nginx:1.27
    container_name: redbolt-` + strconv.FormatInt(source.ID, 10) + `-web
    restart: unless-stopped
    env_file:
      - vars.env
      - secrets.env
    networks:
      - default
    labels:
      - "redlaunch.managed=true"

networks:
  default:
    external: true
    name: redlaunch-common
`
	if err := os.WriteFile(filepath.Join(sourceDirectory, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDirectory, varsEnvFile), []byte("APP_NAME=Shop\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDirectory, secretsEnvFile), []byte("API_TOKEN=super-secret\n"), envFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateService(ctx, application.Service{ApplicationID: source.ID, Name: "web", Type: application.ServiceTypeApplication, ImageName: "nginx:1.27"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateDomain(ctx, application.Domain{ApplicationID: source.ID, Name: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}

	cloned, err := applications.CloneApplication(ctx, source.ID, application.CloneInput{Name: "Shop dev", FolderName: "shop-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if cloned.ID == source.ID {
		t.Fatalf("clone reused source ID %d", source.ID)
	}

	clonedCompose, err := os.ReadFile(filepath.Join(projectsRoot, applicationsDir, cloned.FolderName, "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(clonedCompose), "container_name: redbolt-"+strconv.FormatInt(cloned.ID, 10)+"-web") {
		t.Fatalf("cloned Compose was not re-scoped:\n%s", clonedCompose)
	}
	clonedServices, err := database.ListServices(ctx, cloned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(clonedServices) != 1 || clonedServices[0].Name != "web" || clonedServices[0].ImageName != "nginx:1.27" {
		t.Fatalf("cloned services = %#v, want the copied web service", clonedServices)
	}
	clonedDomains, err := database.ListDomains(ctx, cloned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(clonedDomains) != 1 || clonedDomains[0].Name != "shop.example.com" {
		t.Fatalf("cloned domains = %#v, want the copied hostname", clonedDomains)
	}
	// Routings are never copied; the clone starts without proxy mappings.
	sourceDomains, err := database.ListDomains(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	routings, err := database.ListRoutings(ctx, cloned.ID, sourceDomains[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(routings) != 0 {
		t.Fatalf("cloned routings = %#v, want none", routings)
	}
	if len(runner.actions) != 0 {
		t.Fatalf("clone started containers: %v", runner.actions)
	}

	// A source with an incomplete deletion intent cannot be cloned, and a
	// reserved folder name stays reserved for clones too.
	if _, err := database.BeginApplicationDeletion(ctx, source, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := applications.CloneApplication(ctx, source.ID, application.CloneInput{Name: "Shop blocked", FolderName: "shop-dev-2"}); !errors.Is(err, application.ErrApplicationDeletionInProgress) {
		t.Fatalf("CloneApplication(deleting source) error = %v, want %v", err, application.ErrApplicationDeletionInProgress)
	}
	if _, err := applications.CloneApplication(ctx, cloned.ID, application.CloneInput{Name: "Shop blocked", FolderName: "shop"}); !errors.Is(err, application.ErrApplicationDeletionInProgress) {
		t.Fatalf("CloneApplication(reserved folder) error = %v, want %v", err, application.ErrApplicationDeletionInProgress)
	}
}

func TestCloneApplicationRejectsHalfPresentScopedPair(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root, &serviceRuntimeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	seedCloneSource(t, root, repository, application.Application{ID: 7, Name: "Shop", FolderName: "shop"})
	sourceDirectory := filepath.Join(root, applicationsDir, "shop")
	if err := os.WriteFile(filepath.Join(sourceDirectory, "orphan.vars.env"), []byte("FOO=bar\n"), envFileMode); err != nil {
		t.Fatal(err)
	}

	if _, err := applications.CloneApplication(t.Context(), 7, application.CloneInput{Name: "Shop dev", FolderName: "shop-dev"}); !errors.Is(err, application.ErrDatabaseCredentialsAmbiguous) {
		t.Fatalf("CloneApplication(half scoped pair) error = %v, want %v", err, application.ErrDatabaseCredentialsAmbiguous)
	}
}
