package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/service"
)

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

func registryPageRequest(t *testing.T, web *Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func TestRegistryRendersUnifiedRetentionAndOnePurgeAction(t *testing.T) {
	now := time.Now()
	registry := &fakeRegistryService{
		images: []application.RegistryImage{
			{Repository: "acme-app", Tag: "newest", Digest: "sha256:cccc", PushedAt: now.Add(-1 * time.Hour)},
			{Repository: "acme-app", Tag: "middle", Digest: "sha256:bbbb", PushedAt: now.Add(-2 * time.Hour)},
			{Repository: "acme-app", Tag: "oldest", Digest: "sha256:aaaa", PushedAt: now.Add(-3 * time.Hour)},
		},
		defaultKeep:    1,
		defaultKeepSet: true,
	}
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Acme", FolderName: "acme-app"}},
		services: []application.Service{{
			ID: 11, ApplicationID: 7, Name: "web", ImageName: "localhost:5000/acme-app:oldest",
		}},
	}
	web, err := New(nil, &fakeSetupManager{}, applications, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := registryPageRequest(t, web, "/registry")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /registry status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"id=\"registry-retention-title\">Retention</h2>",
		"Applies to automatic retention",
		"name=\"keep_count\"",
		">Save default</button>",
		">Purge images</button>",
		">Run garbage collection</button>",
		"Keep for this repository",
		"Up to 1 older version would be removed",
		"data-registry-purge-dialog",
		"data-registry-gc-dialog",
		"The registry will be read-only",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /registry did not render %q", expected)
		}
	}
	if strings.Count(body, "data-registry-purge-open ") != 1 {
		t.Fatalf("GET /registry rendered more than one Purge images trigger")
	}
	if strings.Contains(body, "registry-purge-button") {
		t.Fatalf("GET /registry still rendered a per-repository purge button")
	}
}

func TestRegistryPurgeStartsRetentionJob(t *testing.T) {
	now := time.Now()
	registry := &fakeRegistryService{
		images: []application.RegistryImage{
			{Repository: "acme-app", Tag: "v1", Digest: "sha256:bbbb", PushedAt: now.Add(-1 * time.Hour)},
			{Repository: "acme-app", Tag: "v2", Digest: "sha256:aaaa", PushedAt: now.Add(-2 * time.Hour)},
		},
		defaultKeep:    1,
		defaultKeepSet: true,
	}
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Acme", FolderName: "acme-app"}},
		registryRetentionResult: service.RegistryMaintenanceResult{
			DeletedVersions:    1,
			RetentionCompleted: true,
		},
	}
	web, err := New(nil, &fakeSetupManager{}, applications, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := registryPost(t, web, "/registry/purge", url.Values{"confirmation": {"purge-all"}})
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST purge status = %d: %s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	jobID := location.Query().Get("registry_maintenance_job")
	if jobID == "" {
		t.Fatalf("redirect missing maintenance job: %s", location)
	}
	waitRegistryMaintenanceJob(t, web, jobID)
	if applications.registryRetentionCalls != 1 {
		t.Fatalf("retention calls = %d, want 1", applications.registryRetentionCalls)
	}
	status := registryPageRequest(t, web, "/registry/maintenance/status?id="+url.QueryEscape(jobID))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), "Removed 1 older image version.") {
		t.Fatalf("status = %d: %s", status.Code, status.Body.String())
	}
	page := registryPageRequest(t, web, "/registry?registry_maintenance_job="+url.QueryEscape(jobID))
	if !strings.Contains(page.Body.String(), "Purge completed") {
		t.Fatalf("page did not render completed maintenance toast")
	}
}

func TestRegistryGarbageCollectionStartsJobAndShowsReadOnlyWarning(t *testing.T) {
	registry := &fakeRegistryService{}
	applications := &fakeApplicationService{
		registryGCResult: service.RegistryMaintenanceResult{
			GarbageCollectionCompleted: true,
			ReclaimedBytes:             2048,
			ReclaimedBytesKnown:        true,
		},
	}
	web, err := New(nil, &fakeSetupManager{}, applications, registry)
	if err != nil {
		t.Fatal(err)
	}
	page := registryPageRequest(t, web, "/registry")
	if !strings.Contains(page.Body.String(), "restart in read-only mode") {
		t.Fatalf("GC dialog missing read-only warning")
	}
	recorder := registryPost(t, web, "/registry/garbage-collect", url.Values{"confirmation": {"garbage-collect"}})
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST garbage collect status = %d: %s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	jobID := location.Query().Get("registry_maintenance_job")
	waitRegistryMaintenanceJob(t, web, jobID)
	if applications.registryGCCalls != 1 {
		t.Fatalf("GC calls = %d, want 1", applications.registryGCCalls)
	}
	status := registryPageRequest(t, web, "/registry/maintenance/status?id="+url.QueryEscape(jobID))
	body := status.Body.String()
	if !strings.Contains(body, "Garbage collection completed") || !strings.Contains(body, "Reclaimed") {
		t.Fatalf("status missing GC result: %s", body)
	}
}

func TestRegistryMaintenanceRequiresConfirmation(t *testing.T) {
	registry := &fakeRegistryService{}
	applications := &fakeApplicationService{}
	web, err := New(nil, &fakeSetupManager{}, applications, registry)
	if err != nil {
		t.Fatal(err)
	}
	for path := range map[string]string{"/registry/purge": "purge-all", "/registry/garbage-collect": "garbage-collect"} {
		recorder := registryPost(t, web, path, url.Values{})
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("POST %s without confirmation status = %d, want 400", path, recorder.Code)
		}
	}
	if applications.registryRetentionCalls != 0 || applications.registryGCCalls != 0 {
		t.Fatal("maintenance ran without confirmation")
	}
}

func TestRegistryMaintenanceFailedJobShowsMessage(t *testing.T) {
	registry := &fakeRegistryService{}
	applications := &fakeApplicationService{registryGCErr: application.ErrRegistryBusy}
	web, err := New(nil, &fakeSetupManager{}, applications, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := registryPost(t, web, "/registry/garbage-collect", url.Values{"confirmation": {"garbage-collect"}})
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST status = %d", recorder.Code)
	}
	location, _ := url.Parse(recorder.Header().Get("Location"))
	jobID := location.Query().Get("registry_maintenance_job")
	waitRegistryMaintenanceJob(t, web, jobID)
	status := registryPageRequest(t, web, "/registry/maintenance/status?id="+url.QueryEscape(jobID))
	if !strings.Contains(status.Body.String(), "Another registry maintenance operation is already running.") {
		t.Fatalf("failure message missing: %s", status.Body.String())
	}
}

func TestRegistryRetentionSettingsSaveAndReset(t *testing.T) {
	registry := &fakeRegistryService{}
	applications := &fakeApplicationService{}
	web, err := New(nil, &fakeSetupManager{}, applications, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := registryPost(t, web, "/registry/retention/default", url.Values{"keep_count": {"3"}})
	if recorder.Code != http.StatusSeeOther || registry.setKeep != 3 {
		t.Fatalf("default save = %d, keep=%d", recorder.Code, registry.setKeep)
	}
	recorder = registryPost(t, web, "/registry/retention/default", url.Values{"keep_count": {"101"}})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid default status = %d, want 400", recorder.Code)
	}
	recorder = registryPost(t, web, "/registry/retention/repository", url.Values{"repository": {"acme-app"}, "keep_count": {"4"}})
	if recorder.Code != http.StatusSeeOther || registry.setRepository != "acme-app" || registry.setRepoKeep != 4 {
		t.Fatalf("repository save = %d, repository=%q keep=%d", recorder.Code, registry.setRepository, registry.setRepoKeep)
	}
	recorder = registryPost(t, web, "/registry/retention/repository", url.Values{"repository": {"acme-app"}, "reset": {"1"}})
	if recorder.Code != http.StatusSeeOther || registry.resetRepository != "acme-app" {
		t.Fatalf("repository reset = %d, repository=%q", recorder.Code, registry.resetRepository)
	}
}

func TestRegistryMaintenanceBlockedDisablesActions(t *testing.T) {
	registry := &fakeRegistryService{maintenanceErr: errors.New("blocked")}
	applications := &fakeApplicationService{}
	web, err := New(nil, &fakeSetupManager{}, applications, registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := registryPageRequest(t, web, "/registry")
	body := recorder.Body.String()
	if !strings.Contains(body, "Registry maintenance is blocked") {
		t.Fatalf("blocked maintenance message missing")
	}
	if !strings.Contains(body, "data-registry-purge-open aria-controls=\"registry-purge-dialog\" aria-haspopup=\"dialog\" aria-expanded=\"false\" disabled") {
		t.Fatalf("purge button not disabled")
	}
}

func waitRegistryMaintenanceJob(t *testing.T, web *Handler, jobID string) {
	t.Helper()
	if jobID == "" {
		t.Fatal("maintenance job ID is empty")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job := web.registryMaintenanceJobs.get(jobID)
		if job == nil {
			t.Fatal("maintenance job not found")
		}
		if snapshot := job.snapshot(); snapshot.State != registryMaintenanceJobStateRunning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("maintenance job did not finish")
}
