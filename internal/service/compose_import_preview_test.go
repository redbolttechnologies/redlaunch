package service

import (
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

func TestPreviewImportedComposeListsResourcesWithSettings(t *testing.T) {
	contents := `services:
  web:
    image: nginx:1.27
    ports:
      - "8080:80"
    volumes:
      - data:/data
    networks:
      - frontend
    environment:
      - APP_ENV=production
  db:
    image: postgres:17
    volumes:
      - db_data:/var/lib/postgresql/data

volumes:
  data:
  db_data:
    name: custom-db-data

networks:
  frontend:
    driver: bridge
`
	preview, err := PreviewImportedCompose(contents)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Services) != 2 {
		t.Fatalf("services = %d, want 2", len(preview.Services))
	}
	web := preview.Services[0]
	if web.Name != "web" || web.Image != "nginx:1.27" {
		t.Fatalf("web preview = %#v, want web/nginx:1.27", web)
	}
	if web.DetectedType != application.ServiceTypeApplication {
		t.Fatalf("web detected type = %q, want application", web.DetectedType)
	}
	if len(web.Ports) != 1 || web.Ports[0] != `"8080:80"` {
		t.Fatalf("web ports = %#v, want 8080:80", web.Ports)
	}
	db := preview.Services[1]
	if db.DetectedType != application.ServiceTypePostgreSQL {
		t.Fatalf("db detected type = %q, want postgresql", db.DetectedType)
	}
	if db.DetectionReason == "" {
		t.Fatal("db detection reason is empty, want image-based explanation")
	}
	if len(preview.Volumes) != 2 {
		t.Fatalf("volumes = %#v, want 2", preview.Volumes)
	}
	if preview.Volumes[1].Name != "db_data" || len(preview.Volumes[1].Config) != 1 {
		t.Fatalf("db_data preview = %#v, want custom name config", preview.Volumes[1])
	}
	if len(preview.Volumes[0].UsedBy) != 1 || preview.Volumes[0].UsedBy[0] != "web" {
		t.Fatalf("data usedBy = %#v, want [web]", preview.Volumes[0].UsedBy)
	}
	if len(preview.Networks) != 1 || preview.Networks[0].Name != "frontend" {
		t.Fatalf("networks = %#v, want [frontend]", preview.Networks)
	}
	if len(preview.Networks[0].UsedBy) != 1 || preview.Networks[0].UsedBy[0] != "web" {
		t.Fatalf("frontend usedBy = %#v, want [web]", preview.Networks[0].UsedBy)
	}
}

func TestPreviewImportedComposeDetectsManagedServices(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		contents  string
		wantType  string
		wantEmpty bool
	}{
		{
			name:      "postgres image",
			contents:  "services:\n  db:\n    image: postgres:17\n",
			wantType:  application.ServiceTypePostgreSQL,
			wantEmpty: false,
		},
		{
			name:      "postgres registry image",
			contents:  "services:\n  db:\n    image: docker.io/library/postgres:16-alpine\n",
			wantType:  application.ServiceTypePostgreSQL,
			wantEmpty: false,
		},
		{
			name:      "redis image",
			contents:  "services:\n  cache:\n    image: redis:7.2-alpine\n",
			wantType:  application.ServiceTypeRedis,
			wantEmpty: false,
		},
		{
			name:      "redis command signal",
			contents:  "services:\n  cache:\n    image: custom-registry/internal-cache:1\n    command: [\"redis-server\", \"--appendonly\", \"yes\"]\n",
			wantType:  application.ServiceTypeRedis,
			wantEmpty: false,
		},
		{
			name:      "postgres volume signal",
			contents:  "services:\n  db:\n    image: custom-registry/internal-db:1\n    volumes:\n      - db_data:/var/lib/postgresql/data\nvolumes:\n  db_data:\n",
			wantType:  application.ServiceTypePostgreSQL,
			wantEmpty: false,
		},
		{
			name:      "application image",
			contents:  "services:\n  web:\n    image: nginx:1.27\n",
			wantType:  application.ServiceTypeApplication,
			wantEmpty: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			preview, err := PreviewImportedCompose(testCase.contents)
			if err != nil {
				t.Fatal(err)
			}
			if len(preview.Services) != 1 {
				t.Fatalf("services = %d, want 1", len(preview.Services))
			}
			if preview.Services[0].DetectedType != testCase.wantType {
				t.Fatalf("detected type = %q, want %q", preview.Services[0].DetectedType, testCase.wantType)
			}
			if testCase.wantEmpty && preview.Services[0].DetectionReason != "" {
				t.Fatalf("detection reason = %q, want empty for plain application", preview.Services[0].DetectionReason)
			}
			if !testCase.wantEmpty && preview.Services[0].DetectionReason == "" {
				t.Fatal("detection reason is empty, want explanation")
			}
		})
	}
}

func TestPreviewImportedComposeRejectsFilesWithoutServices(t *testing.T) {
	if _, err := PreviewImportedCompose(""); err == nil {
		t.Fatal("PreviewImportedCompose(empty) = nil, want error")
	}
	if _, err := PreviewImportedCompose("services:\n  web:\n    image: nginx:1.27\nvolumes:\n  data: {name: foreign}\n"); err == nil {
		// Flow mappings for top-level resources cannot carry explicit names
		// through the line parser, so the preview fails closed like policy.
		t.Fatal("PreviewImportedCompose(flow volume) = nil, want error")
	}
}

func TestFilterImportedComposeSelectionKeepsSelectedResources(t *testing.T) {
	contents := `services:
  web:
    image: nginx:1.27
    volumes:
      - data:/data
    networks:
      - frontend
  worker:
    image: busybox:1.36
  db:
    image: postgres:17
    volumes:
      - db_data:/var/lib/postgresql/data

volumes:
  data:
  db_data:

networks:
  frontend:
  backend:
`
	filtered, err := filterImportedComposeSelection(contents, application.ComposeImportSelection{
		Services:  []string{"web"},
		Volumes:   []string{"data"},
		Networks:  []string{"frontend"},
		Selective: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(filtered, "worker:") || strings.Contains(filtered, "postgres:17") {
		t.Fatalf("filtered Compose still contains deselected services:\n%s", filtered)
	}
	if !strings.Contains(filtered, "web:") || !strings.Contains(filtered, "nginx:1.27") {
		t.Fatalf("filtered Compose lost the selected service:\n%s", filtered)
	}
	if strings.Contains(filtered, "db_data:") || strings.Contains(filtered, "backend:") {
		t.Fatalf("filtered Compose still contains deselected resources:\n%s", filtered)
	}
	if !strings.Contains(filtered, "data:") || !strings.Contains(filtered, "frontend:") {
		t.Fatalf("filtered Compose lost selected resources:\n%s", filtered)
	}
	if _, err := parseImportedComposeServices(filtered); err != nil {
		t.Fatalf("parse filtered services = %v, want nil", err)
	}
}

func TestFilterImportedComposeSelectionRejectsDanglingReferences(t *testing.T) {
	contents := `services:
  web:
    image: nginx:1.27
    volumes:
      - data:/data

volumes:
  data:
  orphan:
`
	if _, err := filterImportedComposeSelection(contents, application.ComposeImportSelection{
		Services:  []string{"web"},
		Volumes:   []string{"orphan"},
		Selective: true,
	}); err == nil {
		t.Fatal("filter with deselected referenced volume = nil, want error")
	}
	if _, err := filterImportedComposeSelection(contents, application.ComposeImportSelection{
		Services:  []string{},
		Volumes:   []string{"data"},
		Selective: true,
	}); err == nil {
		t.Fatal("filter without services = nil, want error")
	}
	if _, err := filterImportedComposeSelection(contents, application.ComposeImportSelection{
		Services:  []string{"missing"},
		Selective: true,
	}); err == nil {
		t.Fatal("filter with unknown service = nil, want error")
	}
}

func TestApplicationsImportWithSelectionRegistersOnlySelectedServices(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &serviceRuntimeRunner{
		configured: []compose.ConfiguredService{{Name: "web", Image: "nginx:1.27"}},
	}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	createdApplication, err := applications.Create(t.Context(), "Status", "status")
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte("services:\n  web:\n    image: nginx:1.27\n  worker:\n    image: busybox:1.36\n")
	services, err := applications.ImportDockerComposeProjectWithSelection(t.Context(), createdApplication.ID, contents, application.ComposeImportSelection{
		Services:  []string{"web"},
		Selective: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].Name != "web" {
		t.Fatalf("imported services = %#v, want only web", services)
	}
	directory := filepath.Join(root, applicationsDir, "status")
	composeContents := readServiceFile(t, filepath.Join(directory, "compose.yml"))
	if strings.Contains(composeContents, "worker:") || strings.Contains(composeContents, "busybox") {
		t.Fatalf("Compose file contains deselected service:\n%s", composeContents)
	}
}

func TestApplicationsPreviewReturnsPolicyErrorWithPreview(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &serviceRuntimeRunner{}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	createdApplication, err := applications.Create(t.Context(), "Status", "status")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := applications.PreviewDockerComposeProject(t.Context(), createdApplication.ID, []byte("services:\n  web:\n    image: nginx:1.27\n    privileged: true\n"))
	if err == nil {
		t.Fatal("PreviewDockerComposeProject(privileged) = nil, want policy error")
	}
	if preview == nil || len(preview.Services) != 1 || preview.Services[0].Name != "web" {
		t.Fatalf("preview with policy error = %#v, want web service summary", preview)
	}
}
