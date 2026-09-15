package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

type stubRegistryContentLister struct {
	contents  []compose.RegistryContent
	err       error
	container string
	calls     int
}

func (s *stubRegistryContentLister) ListRegistryContents(_ context.Context, container string) ([]compose.RegistryContent, error) {
	s.container = container
	s.calls++
	return s.contents, s.err
}

func TestNewRegistryServiceRequiresContainer(t *testing.T) {
	if _, err := NewRegistryService("  ", nil); err == nil {
		t.Fatal("NewRegistryService() error = nil, want container name error")
	}
}

func TestRegistryServiceListsPushedImagesSorted(t *testing.T) {
	runner := &stubRegistryContentLister{contents: []compose.RegistryContent{
		{Repository: "status-page/web", Tag: "def456", Digest: "sha256:bbbb"},
		{Repository: "admin", Tag: "latest", Digest: "sha256:cccc"},
		{Repository: "status-page/web", Tag: "abc123", Digest: "sha256:aaaa"},
	}}
	registry, err := NewRegistryService(RegistryContainerName, runner)
	if err != nil {
		t.Fatal(err)
	}

	got, err := registry.ListRegistryImages(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []application.RegistryImage{
		{Repository: "admin", Tag: "latest", Digest: "sha256:cccc"},
		{Repository: "status-page/web", Tag: "abc123", Digest: "sha256:aaaa"},
		{Repository: "status-page/web", Tag: "def456", Digest: "sha256:bbbb"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListRegistryImages() = %#v, want %#v", got, want)
	}
	if runner.container != RegistryContainerName || runner.calls != 1 {
		t.Fatalf("registry inspection = (%q, %d calls), want (%q, 1)", runner.container, runner.calls, RegistryContainerName)
	}
}

func TestRegistryServiceMapsUnavailableRegistry(t *testing.T) {
	runner := &stubRegistryContentLister{err: compose.ErrRegistryUnavailable}
	registry, err := NewRegistryService(RegistryContainerName, runner)
	if err != nil {
		t.Fatal(err)
	}

	_, err = registry.ListRegistryImages(t.Context())
	if !errors.Is(err, application.ErrRegistryUnavailable) {
		t.Fatalf("ListRegistryImages() error = %v, want %v", err, application.ErrRegistryUnavailable)
	}
}

func TestRegistryServiceKeepsInspectionErrors(t *testing.T) {
	runner := &stubRegistryContentLister{err: errors.New("docker unavailable")}
	registry, err := NewRegistryService(RegistryContainerName, runner)
	if err != nil {
		t.Fatal(err)
	}

	_, err = registry.ListRegistryImages(t.Context())
	if err == nil || errors.Is(err, application.ErrRegistryUnavailable) {
		t.Fatalf("ListRegistryImages() error = %v, want inspection failure", err)
	}
}
