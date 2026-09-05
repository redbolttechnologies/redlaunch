package service

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func TestApplicationsCreateApplicationServiceUsesLocalRegistryByDefault(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &recordingRunner{}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(t.Context(), "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}

	defaultService, err := applications.CreateApplicationService(t.Context(), created.ID, application.ApplicationServiceInput{ServiceName: "web", AutoStart: true})
	if err != nil {
		t.Fatal(err)
	}
	customService, err := applications.CreateApplicationService(t.Context(), created.ID, application.ApplicationServiceInput{
		ServiceName: "worker",
		ImageName:   "ghcr.io/example/status-worker:v1.2",
		AutoStart:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if defaultService.Type != application.ServiceTypeApplication || defaultService.ImageName != "localhost:5000/web:latest" {
		t.Fatalf("default application service = %#v, want local registry image metadata", defaultService)
	}
	if customService.Type != application.ServiceTypeApplication || customService.ImageName != "ghcr.io/example/status-worker:v1.2" {
		t.Fatalf("custom application service = %#v, want supplied image metadata", customService)
	}
	if len(repository.services) != 2 || repository.services[0].ImageName != "localhost:5000/web:latest" || repository.services[1].ImageName != "ghcr.io/example/status-worker:v1.2" {
		t.Fatalf("persisted application services = %#v, want both image references", repository.services)
	}
	if len(runner.directories) != 2 {
		t.Fatalf("started Compose directories = %v, want two starts", runner.directories)
	}
	if strings.Join(runner.services, "\x00") != "web\x00worker" {
		t.Fatalf("started Compose services = %v, want web and worker", runner.services)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	composeContents := readServiceFile(t, filepath.Join(directory, "compose.yml"))
	for _, expected := range []string{
		"services:\n  web:",
		"image: localhost:5000/web:latest",
		"container_name: redbolt-1-web",
		"  worker:\n    image: ghcr.io/example/status-worker:v1.2",
		"container_name: redbolt-1-worker",
		"env_file:",
		"- vars.env",
		"- secrets.env",
		"networks:",
		"redlaunch.managed=true",
	} {
		if !strings.Contains(composeContents, expected) {
			t.Errorf("Compose file does not contain %q:\n%s", expected, composeContents)
		}
	}
	for _, name := range []string{varsEnvFile, secretsEnvFile} {
		if got := serviceFilePermissions(t, filepath.Join(directory, name)); got != envFileMode {
			t.Errorf("%s permissions = %o, want %o", name, got, envFileMode)
		}
	}
}

func TestApplicationsCreateApplicationServiceDoesNotStartWhenAutoStartIsDisabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &recordingRunner{}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(t.Context(), "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}

	service, err := applications.CreateApplicationService(t.Context(), created.ID, application.ApplicationServiceInput{
		ServiceName: "worker",
		ImageName:   "localhost:5000/worker:latest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.ImageName != "localhost:5000/worker:latest" {
		t.Fatalf("created application service image = %q, want localhost:5000/worker:latest", service.ImageName)
	}
	if len(runner.directories) != 0 {
		t.Fatalf("Compose projects started = %v, want none when AutoStart is disabled", runner.directories)
	}
	composeContents := readServiceFile(t, filepath.Join(root, applicationsDir, "status-page", "compose.yml"))
	if !strings.Contains(composeContents, "image: localhost:5000/worker:latest") {
		t.Fatalf("Compose file does not contain the saved application service image:\n%s", composeContents)
	}
}

func TestApplicationsCreateApplicationServiceRestoresFilesWhenMetadataPersistenceFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	applications, err := NewApplications(repository, root, &recordingRunner{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(t.Context(), "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, applicationsDir, "status-page")
	composePath := filepath.Join(directory, "compose.yml")
	varsPath := filepath.Join(directory, varsEnvFile)
	secretsPath := filepath.Join(directory, secretsEnvFile)
	originalCompose := readServiceFile(t, composePath)
	originalVars := readServiceFile(t, varsPath)
	originalSecrets := readServiceFile(t, secretsPath)
	repository.serviceErr = errors.New("database unavailable")

	if _, err := applications.CreateApplicationService(t.Context(), created.ID, application.ApplicationServiceInput{
		ServiceName: "web",
		ImageName:   "localhost:5000/web:latest",
	}); err == nil {
		t.Fatal("CreateApplicationService() error = nil, want persistence error")
	}
	if got := readServiceFile(t, composePath); got != originalCompose {
		t.Fatalf("Compose file after persistence failure = %q, want original %q", got, originalCompose)
	}
	if got := readServiceFile(t, varsPath); got != originalVars {
		t.Fatalf("variables file after persistence failure = %q, want original %q", got, originalVars)
	}
	if got := readServiceFile(t, secretsPath); got != originalSecrets {
		t.Fatalf("secrets file after persistence failure = %q, want original %q", got, originalSecrets)
	}
}

func TestApplicationsValidateApplicationServiceInput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	applications, err := NewApplications(&applicationRepositoryStub{}, root)
	if err != nil {
		t.Fatal(err)
	}

	if err := applications.ValidateApplicationServiceInput(application.ApplicationServiceInput{ServiceName: "worker", ImageName: "bad/name with spaces"}); !errors.Is(err, application.ErrImageNameInvalid) {
		t.Fatalf("ValidateApplicationServiceInput() error = %v, want %v", err, application.ErrImageNameInvalid)
	}
	if err := applications.ValidateApplicationServiceInput(application.ApplicationServiceInput{ServiceName: "worker"}); err != nil {
		t.Fatalf("ValidateApplicationServiceInput(default image) error = %v, want nil", err)
	}
}
