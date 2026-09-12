package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"redlaunch/internal/application"
)

// TestCustomServiceVolumeSourceRejectsProjectRoot covers the R02 bypass: the
// custom-service workflow must not accept a writable .:/data mount that would
// expose sibling managed files to the workload.
func TestCustomServiceVolumeSourceRejectsProjectRoot(t *testing.T) {
	for _, source := range []string{".", "./"} {
		if err := validateApplicationVolumeSource(source); err == nil {
			t.Fatalf("validateApplicationVolumeSource(%q) = nil, want rejection", source)
		}
		input := application.ApplicationServiceInput{
			ServiceName:    "web",
			VolumeMappings: []application.ApplicationVolumeMapping{{Source: source, Target: "/data"}},
		}
		if _, err := normalizeApplicationServiceInput(input); err == nil {
			t.Fatalf("normalizeApplicationServiceInput(%q) = nil, want rejection", source)
		}
	}
	for _, source := range []string{"./data", "data-vol"} {
		if err := validateApplicationVolumeSource(source); err != nil {
			t.Fatalf("validateApplicationVolumeSource(%q) = %v, want nil", source, err)
		}
	}
}

// TestResolvedBindSourceEnforcesContainment verifies the shared resolved
// mount policy: manager-owned files and symlink escapes are rejected against
// the live filesystem.
func TestResolvedBindSourceEnforcesContainment(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"compose.yml", "vars.env", "secrets.env"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("managed"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	for _, source := range []string{".", "./", "./vars.env", "./secrets.env", "./compose.yml", "./escape", "./escape/sub"} {
		if err := validateResolvedBindSource(root, source); err == nil {
			t.Fatalf("validateResolvedBindSource(%q) = nil, want rejection", source)
		}
	}
	if err := validateResolvedBindSource(root, "./data"); err != nil {
		t.Fatalf("validateResolvedBindSource(./data) = %v, want nil", err)
	}
	if err := validateResolvedBindSource(root, "./not-yet-created"); err != nil {
		t.Fatalf("validateResolvedBindSource(missing subdir) = %v, want nil", err)
	}
}

// TestCreateApplicationServiceRejectsResolvedMountEscapes ensures the
// creation path enforces resolved containment before mutating any files.
func TestCreateApplicationServiceRejectsResolvedMountEscapes(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &serviceRuntimeRunner{}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(ctx, "Status page", "status-page")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, applicationsDir, "status-page")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(directory, "escape")); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(directory, "compose.yml")
	before := readServiceFile(t, composePath)

	for name, source := range map[string]string{
		"project root":   ".",
		"manager-owned":  "./vars.env",
		"symlink escape": "./escape",
		"nested escape":  "./escape/sub",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := applications.CreateApplicationService(ctx, created.ID, application.ApplicationServiceInput{
				ServiceName:    "web",
				VolumeMappings: []application.ApplicationVolumeMapping{{Source: source, Target: "/data"}},
			})
			if err == nil {
				t.Fatalf("CreateApplicationService(%q) = nil, want rejection", source)
			}
			if got := readServiceFile(t, composePath); got != before {
				t.Fatalf("Compose after rejected %s mount changed:\n%s", name, got)
			}
		})
	}
	if len(repository.services) != 0 {
		t.Fatalf("services after rejected mounts = %#v, want none", repository.services)
	}
}

// TestImportedVolumeSourceRejectsManagerOwnedFiles ensures the import path
// shares the same manager-owned file policy as custom-service creation.
func TestImportedVolumeSourceRejectsManagerOwnedFiles(t *testing.T) {
	root := t.TempDir()
	for _, source := range []string{"./vars.env:/data", "./secrets.env:/data", "./compose.yml:/data"} {
		if err := validateImportedVolumeSource(root, source); err == nil {
			t.Fatalf("validateImportedVolumeSource(%q) = nil, want rejection", source)
		}
	}
	if !isManagerOwnedBindSource(root, "./vars.env:/data") {
		t.Fatal("isManagerOwnedBindSource(./vars.env) = false, want true")
	}
	if isManagerOwnedBindSource(root, "./data:/data") {
		t.Fatal("isManagerOwnedBindSource(./data) = true, want false")
	}
	if isManagerOwnedBindSource(root, "data-vol:/data") {
		t.Fatal("isManagerOwnedBindSource(named volume) = true, want false")
	}
}
