package service

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func TestApplicationsRenameApplication(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{
			{ID: 7, Name: "Shop", FolderName: "shop"},
			{ID: 9, Name: "Blog", FolderName: "blog"},
		},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	renamed, err := applications.RenameApplication(t.Context(), 7, "  Shop front  ")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != 7 || renamed.Name != "Shop front" || renamed.FolderName != "shop" {
		t.Fatalf("RenameApplication() = %#v, want trimmed name with unchanged folder", renamed)
	}
	if repository.applications[0].Name != "Shop front" {
		t.Fatalf("stored application name = %q, want %q", repository.applications[0].Name, "Shop front")
	}
}

func TestApplicationsRenameApplicationRejectsInvalidNames(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Shop", FolderName: "shop"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	for name, wantErr := range map[string]error{
		"":    application.ErrNameRequired,
		"   ": application.ErrNameRequired,
		strings.Repeat("n", application.MaxNameLength+1): application.ErrNameTooLong,
		"bad\x00name": application.ErrNameInvalid,
	} {
		if _, err := applications.RenameApplication(t.Context(), 7, name); !errors.Is(err, wantErr) {
			t.Fatalf("RenameApplication(%q) error = %v, want %v", name, err, wantErr)
		}
	}
	if repository.applications[0].Name != "Shop" {
		t.Fatalf("stored application name = %q, want unchanged %q", repository.applications[0].Name, "Shop")
	}
}

func TestApplicationsRenameApplicationRejectsDuplicateAndMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{
			{ID: 7, Name: "Shop", FolderName: "shop"},
			{ID: 9, Name: "Blog", FolderName: "blog"},
		},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := applications.RenameApplication(t.Context(), 7, "Blog"); !errors.Is(err, application.ErrAlreadyExists) {
		t.Fatalf("RenameApplication(duplicate) error = %v, want %v", err, application.ErrAlreadyExists)
	}
	if _, err := applications.RenameApplication(t.Context(), 999, "Missing"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("RenameApplication(missing) error = %v, want %v", err, application.ErrNotFound)
	}
}
