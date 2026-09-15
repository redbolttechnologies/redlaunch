package service

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func TestApplicationsGetApplicationServiceConfigRoundTrip(t *testing.T) {
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
		ImageName:   "ghcr.io/example/web:v1",
		Entrypoint:  "/usr/local/bin/start",
		Healthcheck: application.ApplicationHealthcheck{
			Command:  "curl --fail http://localhost/health",
			Interval: "10s",
			Timeout:  "3s",
			Retries:  "5",
		},
		DependsOn: []application.ApplicationServiceDependency{{
			ServiceName: "db",
			Condition:   application.ApplicationDependencyConditionHealthy,
		}},
		RestartPolicy:  application.ApplicationRestartPolicyOnFailure,
		PortMappings:   []application.ApplicationPortMapping{{HostPort: "8080", ContainerPort: "80", Protocol: "tcp"}},
		VolumeMappings: []application.ApplicationVolumeMapping{{Source: "app-data", Target: "/data", Options: "rw"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	config, err := applications.GetApplicationServiceConfig(t.Context(), created.ID, "web")
	if err != nil {
		t.Fatal(err)
	}
	if config.ServiceName != "web" || config.ImageName != "ghcr.io/example/web:v1" {
		t.Fatalf("config service/image = %q/%q, want web/ghcr.io/example/web:v1", config.ServiceName, config.ImageName)
	}
	if config.Entrypoint != "/usr/local/bin/start" {
		t.Fatalf("config entrypoint = %q, want /usr/local/bin/start", config.Entrypoint)
	}
	if config.Healthcheck.Command != "curl --fail http://localhost/health" || config.Healthcheck.Interval != "10s" {
		t.Fatalf("config healthcheck = %#v, want command and interval", config.Healthcheck)
	}
	if len(config.DependsOn) != 1 || config.DependsOn[0].ServiceName != "db" {
		t.Fatalf("config dependencies = %#v, want one db dependency", config.DependsOn)
	}
	if config.RestartPolicy != application.ApplicationRestartPolicyOnFailure {
		t.Fatalf("config restart = %q, want on-failure", config.RestartPolicy)
	}
	if len(config.PortMappings) != 1 || config.PortMappings[0].HostPort != "8080" {
		t.Fatalf("config ports = %#v, want 8080:80", config.PortMappings)
	}
	if len(config.VolumeMappings) != 1 || config.VolumeMappings[0].Source != "app-data" {
		t.Fatalf("config volumes = %#v, want app-data", config.VolumeMappings)
	}
}

func TestApplicationsUpdateApplicationServiceRewritesComposeAndRestarts(t *testing.T) {
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
	if _, err := applications.CreateApplicationService(t.Context(), created.ID, application.ApplicationServiceInput{
		ServiceName:    "web",
		ImageName:      "localhost:5000/web:latest",
		VolumeMappings: []application.ApplicationVolumeMapping{{Source: "app-data", Target: "/data", Options: "rw"}},
	}); err != nil {
		t.Fatal(err)
	}
	runner.directories = nil
	runner.services = nil

	updated, err := applications.UpdateApplicationService(t.Context(), created.ID, "web", application.ApplicationServiceInput{
		ServiceName:    "web",
		ImageName:      "ghcr.io/example/web:v2",
		RestartPolicy:  application.ApplicationRestartPolicyAlways,
		PortMappings:   []application.ApplicationPortMapping{{HostPort: "8080", ContainerPort: "80", Protocol: "tcp"}},
		VolumeMappings: []application.ApplicationVolumeMapping{{Source: "./cache", Target: "/cache", Options: "rw"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ImageName != "ghcr.io/example/web:v2" {
		t.Fatalf("updated image = %q, want ghcr.io/example/web:v2", updated.ImageName)
	}
	if len(runner.services) != 1 || runner.services[0] != "web" {
		t.Fatalf("restarted services = %v, want [web]", runner.services)
	}

	composeContents := readServiceFile(t, filepath.Join(root, applicationsDir, "status-page", "compose.yml"))
	for _, expected := range []string{
		"image: ghcr.io/example/web:v2",
		"restart: always",
		"- \"8080:80\"",
		"- \"./cache:/cache:rw\"",
	} {
		if !strings.Contains(composeContents, expected) {
			t.Errorf("updated Compose does not contain %q:\n%s", expected, composeContents)
		}
	}
	if strings.Contains(composeContents, "app-data:/data") {
		t.Errorf("updated Compose still references the removed volume:\n%s", composeContents)
	}
	if strings.Contains(composeContents, "\n  app-data:\n") {
		t.Errorf("updated Compose still declares the unused named volume:\n%s", composeContents)
	}
}

func TestApplicationsUpdateApplicationServiceRejectsNonApplicationType(t *testing.T) {
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
		ImageName:     "postgres:17",
	}}

	if _, err := applications.UpdateApplicationService(t.Context(), created.ID, "db", application.ApplicationServiceInput{
		ServiceName: "db",
		ImageName:   "postgres:16",
	}); !errors.Is(err, application.ErrServiceNotFound) {
		t.Fatalf("UpdateApplicationService(database) error = %v, want %v", err, application.ErrServiceNotFound)
	}
	if _, err := applications.GetApplicationServiceConfig(t.Context(), created.ID, "db"); !errors.Is(err, application.ErrServiceNotFound) {
		t.Fatalf("GetApplicationServiceConfig(database) error = %v, want %v", err, application.ErrServiceNotFound)
	}
}

func TestApplicationsUpdateApplicationServiceValidatesInput(t *testing.T) {
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
	if _, err := applications.CreateApplicationService(t.Context(), created.ID, application.ApplicationServiceInput{ServiceName: "web"}); err != nil {
		t.Fatal(err)
	}

	if _, err := applications.UpdateApplicationService(t.Context(), created.ID, "web", application.ApplicationServiceInput{
		ServiceName:  "web",
		PortMappings: []application.ApplicationPortMapping{{HostPort: "0", ContainerPort: "80"}},
	}); !errors.Is(err, application.ErrApplicationPortMappingInvalid) {
		t.Fatalf("UpdateApplicationService(bad port) error = %v, want %v", err, application.ErrApplicationPortMappingInvalid)
	}
}
