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

func TestRegistryVersionImagesUseManifestPushTime(t *testing.T) {
	pushed := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	switched := pushed.Add(24 * time.Hour)
	registry, err := NewRegistryService(RegistryContainerName, &stubRegistryContentLister{contents: []compose.RegistryContent{
		{Repository: "web", Tag: "production", Digest: "sha256:aaaa", PushedAt: switched, ManifestPushedAt: pushed},
		{Repository: "web", Digest: "sha256:bbbb", PushedAt: pushed.Add(time.Hour), ManifestPushedAt: pushed.Add(time.Hour)},
		// Missing revision metadata must not present a tag move as a push.
		{Repository: "web", Tag: "legacy", Digest: "sha256:cccc", PushedAt: switched},
	}})
	if err != nil {
		t.Fatal(err)
	}
	images, err := registry.ListRegistryVersionImages(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 3 {
		t.Fatalf("version images = %#v, want 3", images)
	}
	for _, image := range images {
		if image.Tag == "legacy" {
			if !image.PushedAt.IsZero() {
				t.Fatalf("missing revision time = %v, want unknown", image.PushedAt)
			}
		} else {
			want := pushed
			if image.Tag == "" {
				want = pushed.Add(time.Hour)
			}
			if !image.PushedAt.Equal(want) {
				t.Fatalf("version push time = %v, want %v", image.PushedAt, want)
			}
		}
	}
	versions := application.GroupRegistryVersions(images, "web")
	if versions[0].Digest != "sha256:bbbb" || versions[1].Digest != "sha256:aaaa" {
		t.Fatalf("version order = %#v, want push order independent of the tag move", versions)
	}
	tags, err := registry.ListRegistryImages(t.Context())
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags = %#v, %v, want two tagged images", tags, err)
	}
	for _, tag := range tags {
		if !tag.PushedAt.Equal(switched) {
			t.Fatalf("retention timestamp = %v, want tag assignment time %v", tag.PushedAt, switched)
		}
	}
}
