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

func TestApplicationsCreateApplicationServiceWritesAdvancedComposeSettings(t *testing.T) {
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
	repository.services = []application.Service{{
		ApplicationID: created.ID,
		Name:          "db",
		Type:          application.ServiceTypePostgreSQL,
	}}

	_, err = applications.CreateApplicationService(t.Context(), created.ID, application.ApplicationServiceInput{
		ServiceName: "web",
		ImageName:   "ghcr.io/example/status-web:v1",
		Entrypoint:  "/usr/local/bin/start --serve",
		Healthcheck: application.ApplicationHealthcheck{
			Command:     "wget -q -O - http://localhost/health || exit 1",
			Interval:    "10s",
			Timeout:     "3s",
			Retries:     "5",
			StartPeriod: "20s",
		},
		DependsOn: []application.ApplicationServiceDependency{{
			ServiceName: "db",
			Condition:   application.ApplicationDependencyConditionHealthy,
		}},
		RestartPolicy: application.ApplicationRestartPolicyOnFailure,
		PortMappings: []application.ApplicationPortMapping{
			{HostPort: "8080", ContainerPort: "80", Protocol: "tcp"},
			{HostPort: "8443", ContainerPort: "443", Protocol: "udp"},
		},
		VolumeMappings: []application.ApplicationVolumeMapping{
			{Source: "app-data", Target: "/var/lib/app", Options: "ro"},
			{Source: "./cache", Target: "/cache", Options: "rw,z"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	composeContents := readServiceFile(t, filepath.Join(root, applicationsDir, "status-page", "compose.yml"))
	for _, expected := range []string{
		"entrypoint: \"/usr/local/bin/start --serve\"",
		"healthcheck:",
		"test: [\"CMD-SHELL\", \"wget -q -O - http://localhost/health || exit 1\"]",
		"interval: 10s",
		"timeout: 3s",
		"retries: 5",
		"start_period: 20s",
		"depends_on:",
		"db:\n        condition: service_healthy",
		"restart: on-failure",
		"- \"8080:80\"",
		"- \"8443:443/udp\"",
		"- \"app-data:/var/lib/app:ro\"",
		"- \"./cache:/cache:rw,z\"",
		"volumes:\n  app-data:",
	} {
		if !strings.Contains(composeContents, expected) {
			t.Errorf("advanced Compose output does not contain %q:\n%s", expected, composeContents)
		}
	}
}

func TestApplicationsCreateApplicationServiceAddsNamedVolumeToExistingComposeVolumes(t *testing.T) {
	contents := `services:
  db:
    image: postgres:16

volumes:
  db-data:
    name: redbolt-db-data

networks:
  default:
    external: true
    name: redlaunch-common
`

	got, err := addApplicationServiceWithOptions(contents, "web", "nginx:latest", "redbolt-1-web", application.ApplicationServiceInput{
		RestartPolicy: application.ApplicationRestartPolicyUnlessStopped,
		VolumeMappings: []application.ApplicationVolumeMapping{{
			Source:  "app-data",
			Target:  "/var/lib/app",
			Options: "rw",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "\nvolumes:\n") != 1 {
		t.Fatalf("Compose output has duplicate top-level volumes sections:\n%s", got)
	}
	if !strings.Contains(got, "  db-data:\n    name: redbolt-db-data\n") || !strings.Contains(got, "\n  app-data:\n") {
		t.Fatalf("Compose output did not preserve and extend the existing volumes block:\n%s", got)
	}
}

func TestApplicationsValidateApplicationServiceAdvancedSettings(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	applications, err := NewApplications(&applicationRepositoryStub{}, root)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		input application.ApplicationServiceInput
		want  error
	}{
		{
			name:  "invalid restart policy",
			input: application.ApplicationServiceInput{ServiceName: "web", RestartPolicy: "sometimes"},
			want:  application.ErrApplicationRestartPolicyInvalid,
		},
		{
			name: "invalid healthcheck interval",
			input: application.ApplicationServiceInput{
				ServiceName: "web",
				Healthcheck: application.ApplicationHealthcheck{Command: "true", Interval: "soon"},
			},
			want: application.ErrApplicationHealthcheckIntervalInvalid,
		},
		{
			name: "invalid port mapping",
			input: application.ApplicationServiceInput{
				ServiceName:  "web",
				PortMappings: []application.ApplicationPortMapping{{HostPort: "0", ContainerPort: "80"}},
			},
			want: application.ErrApplicationPortMappingInvalid,
		},
		{
			name: "volume path escapes project",
			input: application.ApplicationServiceInput{
				ServiceName:    "web",
				VolumeMappings: []application.ApplicationVolumeMapping{{Source: "../secrets", Target: "/run/secrets"}},
			},
			want: application.ErrApplicationVolumeSourceInvalid,
		},
		{
			name: "invalid dependency condition",
			input: application.ApplicationServiceInput{
				ServiceName: "web",
				DependsOn:   []application.ApplicationServiceDependency{{ServiceName: "db", Condition: "ready"}},
			},
			want: application.ErrApplicationDependencyConditionInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := applications.ValidateApplicationServiceInput(test.input); !errors.Is(err, test.want) {
				t.Fatalf("ValidateApplicationServiceInput() error = %v, want %v", err, test.want)
			}
		})
	}
}
