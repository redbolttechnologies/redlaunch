package handler

import (
	"context"
	"testing"
)

type stubSetupManager struct{}

func (stubSetupManager) NeedsSetup() (bool, error) { return false, nil }

func (stubSetupManager) Setup(context.Context, bool, bool) error { return nil }

type stubApplicationService struct{ noApplicationService }

type stubAuthenticationService struct{ fakeAuthenticationService }

func TestNewWithDependenciesRequiresSetup(t *testing.T) {
	_, err := NewWithDependencies(nil, Dependencies{
		Applications:                 stubApplicationService{},
		AllowUnauthenticatedForTests: true,
	})
	if err == nil {
		t.Fatal("NewWithDependencies() error = nil, want missing setup service error")
	}
}

func TestNewWithDependenciesRequiresApplications(t *testing.T) {
	_, err := NewWithDependencies(nil, Dependencies{
		Setup:                        stubSetupManager{},
		AllowUnauthenticatedForTests: true,
	})
	if err == nil {
		t.Fatal("NewWithDependencies() error = nil, want missing application service error")
	}
}

func TestNewWithDependenciesRequiresAuthenticationForProduction(t *testing.T) {
	_, err := NewWithDependencies(nil, Dependencies{
		Setup:        stubSetupManager{},
		Applications: stubApplicationService{},
	})
	if err == nil {
		t.Fatal("NewWithDependencies() error = nil, want production authentication error")
	}
}

func TestNewWithDependenciesRejectsAccessMode(t *testing.T) {
	auth := &fakeAuthenticationService{enabled: true}
	_, err := NewWithDependencies(nil, Dependencies{
		Setup:                        stubSetupManager{},
		Applications:                 stubApplicationService{},
		Authentication:               auth,
		Security:                     SecurityConfig{AccessMode: "public"},
		AllowUnauthenticatedForTests: true,
	})
	if err == nil {
		t.Fatal("NewWithDependencies() error = nil, want access mode error")
	}
}

func TestNewWithDependenciesAllowsTestUnauthenticated(t *testing.T) {
	handler, err := NewWithDependencies(nil, Dependencies{
		Setup:                        stubSetupManager{},
		Applications:                 stubApplicationService{},
		AllowUnauthenticatedForTests: true,
	})
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}
	if handler == nil {
		t.Fatal("NewWithDependencies() handler = nil")
	}
}

func TestNewWithDependenciesProductionAuth(t *testing.T) {
	auth := &fakeAuthenticationService{enabled: true}
	handler, err := NewWithDependencies(nil, Dependencies{
		Setup:          stubSetupManager{},
		Applications:   stubApplicationService{},
		Authentication: auth,
		Security:       SecurityConfig{AccessMode: "ssh-only"},
	})
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}
	if !handler.authenticationEnabled() {
		t.Fatal("authenticationEnabled() = false, want production authentication")
	}
}
