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

type fakeRetentionRegistryService struct {
	images       []application.RegistryImage
	err          error
	defaultKeep  int
	policies     map[string]int
	defaultCalls int
	setDefault   int
	setRepo      string
	setKeep      int
	resetRepo    string
	purged       []application.RegistryImage
	purgeErr     error
}

func newFakeRetentionRegistry(images []application.RegistryImage) *fakeRetentionRegistryService {
	return &fakeRetentionRegistryService{
		images:      images,
		defaultKeep: application.DefaultRegistryKeepCount,
		policies:    map[string]int{},
	}
}

func (s *fakeRetentionRegistryService) ListRegistryImages(context.Context) ([]application.RegistryImage, error) {
	return s.images, s.err
}

func (s *fakeRetentionRegistryService) GetDefaultKeep(context.Context) (int, error) {
	return s.defaultKeep, nil
}

func (s *fakeRetentionRegistryService) SetDefaultKeep(_ context.Context, keep int) error {
	if _, err := application.ValidateRegistryKeepCount(keep); err != nil {
		return err
	}
	s.defaultKeep = keep
	s.setDefault = keep
	s.defaultCalls++
	return nil
}

func (s *fakeRetentionRegistryService) ListRetentionPolicies(context.Context) ([]application.RegistryRetentionPolicy, error) {
	policies := make([]application.RegistryRetentionPolicy, 0, len(s.policies))
	for repository, keep := range s.policies {
		policies = append(policies, application.RegistryRetentionPolicy{Repository: repository, KeepCount: keep})
	}
	return policies, nil
}

func (s *fakeRetentionRegistryService) SetRepositoryKeep(_ context.Context, repository string, keep int) error {
	if _, err := application.ValidateRegistryRepository(repository); err != nil {
		return err
	}
	if _, err := application.ValidateRegistryKeepCount(keep); err != nil {
		return err
	}
	s.policies[repository] = keep
	s.setRepo = repository
	s.setKeep = keep
	return nil
}

func (s *fakeRetentionRegistryService) DeleteRepositoryKeep(_ context.Context, repository string) error {
	delete(s.policies, repository)
	s.resetRepo = repository
	return nil
}

func (s *fakeRetentionRegistryService) PlanRepositoryPurge(_ context.Context, repository string, protected map[string]bool) ([]application.RegistryImage, []application.RegistryImage, int, error) {
	filtered := make([]application.RegistryImage, 0)
	for _, image := range s.images {
		if image.Repository == repository {
			filtered = append(filtered, image)
		}
	}
	keep := s.defaultKeep
	if override, ok := s.policies[repository]; ok {
		keep = override
	}
	kept, purge, err := application.PlanRegistryPurge(filtered, keep, protected)
	return kept, purge, keep, err
}

func (s *fakeRetentionRegistryService) PurgeRepository(_ context.Context, repository string, protected map[string]bool) ([]application.RegistryImage, error) {
	if s.purgeErr != nil {
		return nil, s.purgeErr
	}
	_, purge, _, err := s.PlanRepositoryPurge(context.Background(), repository, protected)
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

func TestRegistryRetentionGroupsAndPurgePreview(t *testing.T) {
	now := time.Now()
	registry := newFakeRetentionRegistry([]application.RegistryImage{
		{Repository: "acme-app", Tag: "oldest", Digest: "sha256:aaaa", PushedAt: now.Add(-3 * time.Hour)},
		{Repository: "acme-app", Tag: "middle", Digest: "sha256:bbbb", PushedAt: now.Add(-2 * time.Hour)},
		{Repository: "acme-app", Tag: "newest", Digest: "sha256:cccc", PushedAt: now.Add(-1 * time.Hour)},
	})
	registry.defaultKeep = 1
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
		`Retention`,
		`Default keep count`,
		`acme-app`,
		`Purge 2 older images`,
		`middle`,
		`oldest`,
		`Created`,
		`Last pushed`,
		`registry-repository-group`,
		`registry-repository-summary`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /registry did not render %q", expected)
		}
	}
	if strings.Contains(body, "<details class=\"registry-repository-group\" open") || strings.Contains(body, "<details open") {
		t.Fatalf("GET /registry repository rows should be initially closed")
	}
}

func TestRegistrySetDefaultKeep(t *testing.T) {
	registry := newFakeRetentionRegistry(nil)
	web, err := New(nil, &fakeSetupManager{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := registryPost(t, web, "/registry/retention/default", url.Values{"keep_count": {"3"}})
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/registry" {
		t.Fatalf("POST default keep = (%d, %q), want redirect to /registry", recorder.Code, recorder.Header().Get("Location"))
	}
	if registry.defaultKeep != 3 {
		t.Fatalf("default keep = %d, want 3", registry.defaultKeep)
	}
	recorder = registryPost(t, web, "/registry/retention/default", url.Values{"keep_count": {"101"}})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid keep status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestRegistryPurgeProtectsDeployedTag(t *testing.T) {
	now := time.Now()
	registry := newFakeRetentionRegistry([]application.RegistryImage{
		{Repository: "acme-app", Tag: "oldest", Digest: "sha256:aaaa", PushedAt: now.Add(-3 * time.Hour)},
		{Repository: "acme-app", Tag: "middle", Digest: "sha256:bbbb", PushedAt: now.Add(-2 * time.Hour)},
		{Repository: "acme-app", Tag: "newest", Digest: "sha256:cccc", PushedAt: now.Add(-1 * time.Hour)},
	})
	registry.defaultKeep = 1
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Acme", FolderName: "acme-app"}},
		services:     []application.Service{{ID: 11, ApplicationID: 7, Name: "web", ImageName: "localhost:5000/acme-app:oldest"}},
	}
	web, err := New(nil, &fakeSetupManager{}, applications, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := registryPost(t, web, "/registry/purge", url.Values{"repository": {"acme-app"}, "confirm": {"purge"}})
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

func TestRegistryPurgeRequiresConfirmation(t *testing.T) {
	registry := newFakeRetentionRegistry(nil)
	web, err := New(nil, &fakeSetupManager{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := registryPost(t, web, "/registry/purge", url.Values{"repository": {"acme-app"}})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST purge without confirm status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
