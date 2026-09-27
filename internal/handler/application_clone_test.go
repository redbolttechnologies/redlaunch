package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func newCloneRequest(token string, values url.Values) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/applications/7/clone", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
	}
	return request
}

func TestCloneApplicationRedirectsToNewApplication(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{
		ID:         7,
		Name:       "Shop",
		FolderName: "shop",
	}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token":       {web.csrfToken},
		"name":             {"Shop dev"},
		"folder_name":      {"shop-dev"},
		"copy_extra_files": {"on"},
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newCloneRequest(web.csrfToken, form))

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /applications/7/clone status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/9?cloned_from=7" {
		t.Fatalf("POST /applications/7/clone Location = %q, want /applications/9?cloned_from=7", got)
	}
	if applications.cloneID != 7 {
		t.Fatalf("clone source ID = %d, want 7", applications.cloneID)
	}
	if applications.cloneInput.Name != "Shop dev" || applications.cloneInput.FolderName != "shop-dev" || !applications.cloneInput.CopyExtraFiles {
		t.Fatalf("clone input = %#v, want dev copy with extra files", applications.cloneInput)
	}
}

func TestCloneApplicationRequiresCSRF(t *testing.T) {
	applications := &fakeApplicationService{}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"name": {"Shop dev"}, "folder_name": {"shop-dev"}}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newCloneRequest("", form))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST /applications/7/clone without CSRF status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if applications.cloneID != 0 {
		t.Fatalf("clone without CSRF reached the service: %#v", applications.cloneInput)
	}
}

func TestCloneApplicationRendersSafeValidationError(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Shop", FolderName: "shop"}},
		cloneErr:     application.ErrAlreadyExists,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token":  {web.csrfToken},
		"name":        {"Shop <dev>"},
		"folder_name": {"shop-dev"},
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newCloneRequest(web.csrfToken, form))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST duplicate clone status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"An application with that name or folder already exists.",
		`data-application-clone-open`,
		`id="application-clone-dialog" data-application-clone-dialog data-application-clone-open`,
		`value="Shop &lt;dev&gt;"`,
		`value="shop-dev"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("duplicate clone response did not render %q: %s", expected, body)
		}
	}
}

func TestCloneApplicationReturnsNotFoundForMissingSource(t *testing.T) {
	applications := &fakeApplicationService{cloneErr: application.ErrNotFound}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token":  {web.csrfToken},
		"name":        {"Shop dev"},
		"folder_name": {"shop-dev"},
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newCloneRequest(web.csrfToken, form))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("POST clone of missing application status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestApplicationDetailsRendersCloneSection(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{
		ID:         7,
		Name:       "Status <page>",
		FolderName: "status-page",
	}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /applications/7 status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`id="clone-zone-title"`,
		`action="/applications/7/clone"`,
		`id="application-clone-dialog" data-application-clone-dialog`,
		`value="Status &lt;page&gt; (dev)"`,
		`value="status-page-dev"`,
		`Secrets are cloned as-is and need careful review`,
		`/static/application-clone.js`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /applications/7 did not render clone UI %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `id="application-clone-dialog" data-application-clone-dialog data-application-clone-open`) {
		t.Fatalf("GET /applications/7 opened the clone dialog by default: %s", body)
	}
	if strings.Contains(body, "Cloned with secrets copied as-is") {
		t.Fatalf("GET /applications/7 rendered the clone banner without cloned_from: %s", body)
	}
	cloneIndex := strings.Index(body, `id="clone-zone-title"`)
	dangerIndex := strings.Index(body, `id="danger-zone-title"`)
	if cloneIndex < 0 || dangerIndex < 0 || cloneIndex > dangerIndex {
		t.Fatalf("clone section must render above the danger zone: %s", body)
	}
}

func TestApplicationDetailsRendersClonedBanner(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{
		ID:         9,
		Name:       "Shop dev",
		FolderName: "shop-dev",
	}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/9?cloned_from=7", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /applications/9?cloned_from=7 status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"Cloned with secrets copied as-is.",
		"All services are stopped",
		`role="status"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("cloned application page did not render banner %q: %s", expected, body)
		}
	}
}
