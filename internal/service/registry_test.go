package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

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

type stubRegistryDeleter struct {
	deleted [][2]string
	err     error
}

func (s *stubRegistryDeleter) DeleteRegistryTag(_ context.Context, _, repository, tag string) error {
	if s.err != nil {
		return s.err
	}
	s.deleted = append(s.deleted, [2]string{repository, tag})
	return nil
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

func TestRegistryServicePurgeKeepsNewestAndProtectsDeployed(t *testing.T) {
	now := time.Now()
	runner := &stubRegistryContentLister{contents: []compose.RegistryContent{
		{Repository: "acme-app", Tag: "oldest", Digest: "sha256:aaaa", PushedAt: now.Add(-3 * time.Hour)},
		{Repository: "acme-app", Tag: "middle", Digest: "sha256:bbbb", PushedAt: now.Add(-2 * time.Hour)},
		{Repository: "acme-app", Tag: "newest", Digest: "sha256:cccc", PushedAt: now.Add(-1 * time.Hour)},
	}}
	registry, err := NewRegistryService(RegistryContainerName, runner)
	if err != nil {
		t.Fatal(err)
	}
	deleter := &stubRegistryDeleter{}
	registry.SetTagDeleter(deleter)

	protected := map[string]bool{application.RegistryImageKey("acme-app", "oldest"): true}
	purged, err := registry.PurgeRepository(t.Context(), "acme-app", 1, protected)
	if err != nil {
		t.Fatal(err)
	}
	if len(purged) != 1 || purged[0].Tag != "middle" {
		t.Fatalf("PurgeRepository() = %#v, want middle only", purged)
	}
	if len(deleter.deleted) != 1 || deleter.deleted[0][1] != "middle" {
		t.Fatalf("deleted = %#v, want middle only", deleter.deleted)
	}
}

func TestRegistryServicePurgeUsesExplicitKeepCount(t *testing.T) {
	now := time.Now()
	runner := &stubRegistryContentLister{contents: []compose.RegistryContent{
		{Repository: "acme-app", Tag: "a", Digest: "sha256:aaaa", PushedAt: now.Add(-2 * time.Hour)},
		{Repository: "acme-app", Tag: "b", Digest: "sha256:bbbb", PushedAt: now.Add(-1 * time.Hour)},
	}}
	registry, err := NewRegistryService(RegistryContainerName, runner)
	if err != nil {
		t.Fatal(err)
	}
	deleter := &stubRegistryDeleter{}
	registry.SetTagDeleter(deleter)
	purged, err := registry.PurgeRepository(t.Context(), "acme-app", 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(purged) != 0 {
		t.Fatalf("PurgeRepository() = %#v, want no purge with keep=3", purged)
	}
}

func TestRegistryServicePurgeRejectsInvalidKeepCount(t *testing.T) {
	runner := &stubRegistryContentLister{contents: []compose.RegistryContent{}}
	registry, err := NewRegistryService(RegistryContainerName, runner)
	if err != nil {
		t.Fatal(err)
	}
	registry.SetTagDeleter(&stubRegistryDeleter{})
	if _, err := registry.PurgeRepository(t.Context(), "acme-app", 101, nil); !errors.Is(err, application.ErrRegistryKeepInvalid) {
		t.Fatalf("PurgeRepository() error = %v, want %v", err, application.ErrRegistryKeepInvalid)
	}
}

func TestRegistryServicePurgeRequiresDeleter(t *testing.T) {
	runner := &stubRegistryContentLister{contents: []compose.RegistryContent{}}
	registry, err := NewRegistryService(RegistryContainerName, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PurgeRepository(t.Context(), "acme-app", 5, nil); err == nil {
		t.Fatal("PurgeRepository() error = nil, want missing deleter error")
	}
}
