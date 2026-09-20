package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type fakePurgeRegistryService struct {
	images   []application.RegistryImage
	err      error
	keep     int
	purged   []application.RegistryImage
	purgeErr error
}

func newFakePurgeRegistry(images []application.RegistryImage) *fakePurgeRegistryService {
	return &fakePurgeRegistryService{images: images}
}

func (s *fakePurgeRegistryService) ListRegistryImages(context.Context) ([]application.RegistryImage, error) {
	return s.images, s.err
}

func (s *fakePurgeRegistryService) PurgeRepository(_ context.Context, repository string, keep int, protected map[string]bool) ([]application.RegistryImage, error) {
	if s.purgeErr != nil {
		return nil, s.purgeErr
	}
	if _, err := application.ValidateRegistryRepository(repository); err != nil {
		return nil, err
	}
	if _, err := application.ValidateRegistryKeepCount(keep); err != nil {
		return nil, err
	}
	s.keep = keep
	filtered := make([]application.RegistryImage, 0)
	for _, image := range s.images {
		if image.Repository == repository {
			filtered = append(filtered, image)
		}
	}
	_, purge, err := application.PlanRegistryPurge(filtered, keep, protected)
	if err != nil {
		return nil, err
	}
	s.purged = append([]application.RegistryImage(nil), purge...)
	remaining := make([]application.RegistryImage, 0, len(s.images))
	purgedSet := make(map[string]bool, len(purge))
	for _, image := range purge {
		purgedSet[application.RegistryImageKey(image.Repository, image.Tag)] = true
	}
	for _, image := range s.images {
		if !purgedSet[application.RegistryImageKey(image.Repository, image.Tag)] {
			remaining = append(remaining, image)
		}
	}
	s.images = remaining
	return purge, nil
}

func registryPost(t *testing.T, web *Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	form.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	return recorder
}

func TestRegistryGroupsWithBadgesBannerAndDialog(t *testing.T) {
	now := time.Now()
	registry := newFakePurgeRegistry([]application.RegistryImage{
		{Repository: "acme-app", Tag: "oldest", Digest: "sha256:aaaa", PushedAt: now.Add(-3 * time.Hour)},
		{Repository: "acme-app", Tag: "middle", Digest: "sha256:bbbb", PushedAt: now.Add(-2 * time.Hour)},
		{Repository: "acme-app", Tag: "newest", Digest: "sha256:cccc", PushedAt: now.Add(-1 * time.Hour)},
	})
	web, err := New(nil, &fakeSetupManager{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/registry", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /registry status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`acme-app`,
		`3 tags`,
		`Keep 5`,
		`registry-meta-badge`,
		`Nothing to purge. The newest images and deployed tags are kept.`,
		`registry-info-banner`,
		`registry-purge-dialog-0`,
		`Keep the N recent images`,
		`name="keep_count"`,
		`middle`,
		`oldest`,
		`Created`,
		`Last pushed`,
		`registry-repository-group`,
		`registry-repository-summary`,
		`registry-summary-badges`,
		`registry-digest-cell`,
		`Pushed images`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /registry did not render %q", expected)
		}
	}
	for _, unexpected := range []string{
		`Retention`,
		`Default keep count`,
		`Keep for this repository`,
		`Use default`,
		`/registry/retention/`,
		`registry-repository-actions`,
	} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("GET /registry should not render retention setting %q", unexpected)
		}
	}
	if strings.Contains(body, "<details class=\"registry-repository-group\" open") || strings.Contains(body, "<details open") {
		t.Fatalf("GET /registry repository rows should be initially closed")
	}
}

func TestRegistryGroupsWithPurgeCandidatesShowInlinePurgeButton(t *testing.T) {
	now := time.Now()
	images := make([]application.RegistryImage, 0, 7)
	for i := 0; i < 7; i++ {
		images = append(images, application.RegistryImage{
			Repository: "acme-app",
			Tag:        "v" + string(rune('a'+i)),
			Digest:     "sha256:aaaa",
			PushedAt:   now.Add(time.Duration(i-7) * time.Hour),
		})
	}
	registry := newFakePurgeRegistry(images)
	web, err := New(nil, &fakeSetupManager{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/registry", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /registry status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`7 tags`,
		`Keep 5`,
		`2 older images would be purged`,
		`Purge 2 older images`,
		`data-registry-purge-open`,
		`registry-purge-actions`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /registry did not render %q", expected)
		}
	}
	if strings.Contains(body, "Nothing to purge") {
		t.Fatalf("GET /registry should not render the empty purge banner when candidates exist: %s", body)
	}
}

func TestRegistryPurgeUsesKeepCountFromDialog(t *testing.T) {
	now := time.Now()
	registry := newFakePurgeRegistry([]application.RegistryImage{
		{Repository: "acme-app", Tag: "oldest", Digest: "sha256:aaaa", PushedAt: now.Add(-3 * time.Hour)},
		{Repository: "acme-app", Tag: "middle", Digest: "sha256:bbbb", PushedAt: now.Add(-2 * time.Hour)},
		{Repository: "acme-app", Tag: "newest", Digest: "sha256:cccc", PushedAt: now.Add(-1 * time.Hour)},
	})
	web, err := New(nil, &fakeSetupManager{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := registryPost(t, web, "/registry/purge", url.Values{"repository": {"acme-app"}, "keep_count": {"1"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST purge status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if registry.keep != 1 {
		t.Fatalf("purge keep = %d, want 1", registry.keep)
	}
	if len(registry.purged) != 2 {
		t.Fatalf("purged = %#v, want 2 older images", registry.purged)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "Purged 2 images") {
		t.Fatalf("purge response missing confirmation: %s", body)
	}
}

func TestRegistryPurgeProtectsDeployedTag(t *testing.T) {
	now := time.Now()
	registry := newFakePurgeRegistry([]application.RegistryImage{
		{Repository: "acme-app", Tag: "oldest", Digest: "sha256:aaaa", PushedAt: now.Add(-3 * time.Hour)},
		{Repository: "acme-app", Tag: "middle", Digest: "sha256:bbbb", PushedAt: now.Add(-2 * time.Hour)},
		{Repository: "acme-app", Tag: "newest", Digest: "sha256:cccc", PushedAt: now.Add(-1 * time.Hour)},
	})
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Acme", FolderName: "acme-app"}},
		services:     []application.Service{{ID: 11, ApplicationID: 7, Name: "web", ImageName: "localhost:5000/acme-app:oldest"}},
	}
	web, err := New(nil, &fakeSetupManager{}, applications, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := registryPost(t, web, "/registry/purge", url.Values{"repository": {"acme-app"}, "keep_count": {"1"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST purge status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if len(registry.purged) != 1 || registry.purged[0].Tag != "middle" {
		t.Fatalf("purged = %#v, want middle only (oldest deployed is protected)", registry.purged)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "Purged 1 image") {
		t.Fatalf("purge response missing confirmation: %s", body)
	}
}

func TestRegistryPurgeRequiresKeepCount(t *testing.T) {
	now := time.Now()
	registry := newFakePurgeRegistry([]application.RegistryImage{
		{Repository: "acme-app", Tag: "v1", Digest: "sha256:aaaa", PushedAt: now},
	})
	web, err := New(nil, &fakeSetupManager{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := registryPost(t, web, "/registry/purge", url.Values{"repository": {"acme-app"}})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST purge without keep count status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "data-registry-purge-open-dialog") {
		t.Fatalf("invalid keep count should reopen the purge dialog: %s", body)
	}
	recorder = registryPost(t, web, "/registry/purge", url.Values{"repository": {"acme-app"}, "keep_count": {"101"}})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST purge with invalid keep status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
