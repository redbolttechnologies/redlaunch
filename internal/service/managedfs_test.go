package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func TestCheckManagedAncestorsRejectsSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	applicationsRoot := filepath.Join(root, applicationsDir)
	if err := os.MkdirAll(filepath.Join(applicationsRoot, "victim"), 0o755); err != nil {
		t.Fatalf("create victim directory: %v", err)
	}
	outside := t.TempDir()
	// Replace an intermediate ancestor with a symlink pointing outside the
	// managed tree. Lexical containment still passes, so the ancestor walk
	// must reject it.
	link := filepath.Join(root, "link")
	if err := os.RemoveAll(link); err != nil {
		t.Fatalf("clear link path: %v", err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("create ancestor symlink: %v", err)
	}
	candidate := filepath.Join(link, "victim")
	if err := checkManagedAncestors(root, candidate); err == nil {
		t.Fatal("checkManagedAncestors(symlinked ancestor) = nil, want rejection")
	}
}

func TestCheckManagedAncestorsAllowsNormalTree(t *testing.T) {
	root := t.TempDir()
	applicationsRoot := filepath.Join(root, applicationsDir)
	if err := os.MkdirAll(filepath.Join(applicationsRoot, "demo"), 0o755); err != nil {
		t.Fatalf("create application directory: %v", err)
	}
	if err := checkManagedAncestors(applicationsRoot, filepath.Join(applicationsRoot, "demo")); err != nil {
		t.Fatalf("checkManagedAncestors(normal) = %v, want nil", err)
	}
}

func TestManagedApplicationDirectoryRejectsAncestorReplacement(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "demo"), 0o755); err != nil {
		t.Fatalf("create outside application directory: %v", err)
	}
	// Replace the applications directory with a symlink pointing outside the
	// managed tree. The final directory itself is a real directory, so only
	// resolved-path containment can catch the escape.
	applicationsRoot := filepath.Join(root, applicationsDir)
	if err := os.Symlink(outside, applicationsRoot); err != nil {
		t.Fatalf("create ancestor symlink: %v", err)
	}
	// managedApplicationDirectory rejects a symlinked final directory before
	// containment, so exercise the resolver directly for the ancestor case.
	if err := checkResolvedDirectoryContainment(t.TempDir(), filepath.Join(outside, "demo")); err == nil {
		t.Fatal("checkResolvedDirectoryContainment(unrelated roots) = nil, want rejection")
	}
	service := &Applications{applicationsDir: applicationsRoot}
	if _, err := service.managedApplicationDirectory(application.Application{FolderName: "demo"}); err == nil {
		t.Fatal("managedApplicationDirectory(symlinked ancestor) = nil, want rejection")
	}
}

func TestValidateImportedVolumeSourceRejectsProjectRootBind(t *testing.T) {
	root := t.TempDir()
	for _, source := range []string{".", "./", ".:/data", "./:/data"} {
		if err := validateImportedVolumeSource(root, source); err == nil {
			t.Fatalf("validateImportedVolumeSource(%q) = nil, want project-root rejection", source)
		} else if !strings.Contains(err.Error(), "application directory itself") {
			t.Fatalf("validateImportedVolumeSource(%q) error = %v, want project-root message", source, err)
		}
	}
}

func TestValidateImportedVolumeSourceAllowsSubdirectoryBind(t *testing.T) {
	root := t.TempDir()
	if err := validateImportedVolumeSource(root, "./data:/data"); err != nil {
		t.Fatalf("validateImportedVolumeSource(subdirectory) = %v, want nil", err)
	}
}

func TestSupportedComposeFileNamesPrefersComposeYML(t *testing.T) {
	names := supportedComposeFileNames()
	if len(names) != 2 || names[0] != "compose.yml" {
		t.Fatalf("supportedComposeFileNames() = %v, want [compose.yml compose.yaml]", names)
	}
	if preferredComposeFileName != "compose.yml" {
		t.Fatalf("preferredComposeFileName = %q, want compose.yml", preferredComposeFileName)
	}
}
