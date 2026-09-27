package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func newRenameRequest(token string, values url.Values, htmx bool) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/applications/7/rename", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
	}
	if htmx {
		request.Header.Set("HX-Request", "true")
	}
	return request
}

func TestRenameApplicationSwapsTitleFragment(t *testing.T) {
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
		"csrf_token": {web.csrfToken},
		"name":       {"Shop front"},
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newRenameRequest(web.csrfToken, form, true))

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /applications/7/rename status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if applications.renameID != 7 || applications.renameName != "Shop front" {
		t.Fatalf("rename = (%d, %q), want (7, %q)", applications.renameID, applications.renameName, "Shop front")
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<h1 id="page-title">Shop front</h1>`,
		`id="application-title-block"`,
		`data-application-name="Shop front"`,
		`id="application-breadcrumb-name" hx-swap-oob="true">Shop front</span>`,
		`hx-post="/applications/7/rename"`,
		`hx-target="#application-title-block"`,
		`hx-swap="outerHTML"`,
		`data-application-name-edit`,
		`aria-label="Rename application"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("rename fragment did not render %q: %s", expected, body)
		}
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("rename fragment Content-Type = %q, want HTML", contentType)
	}
	if cache := recorder.Header().Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("rename fragment Cache-Control = %q, want no-store", cache)
	}
}

func TestRenameApplicationRedirectsWithoutHTMX(t *testing.T) {
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
		"csrf_token": {web.csrfToken},
		"name":       {"Shop front"},
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newRenameRequest(web.csrfToken, form, false))

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /applications/7/rename status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/applications/7" {
		t.Fatalf("POST /applications/7/rename Location = %q, want /applications/7", got)
	}
}

func TestRenameApplicationRequiresCSRF(t *testing.T) {
	applications := &fakeApplicationService{}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"name": {"Shop front"}}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newRenameRequest("", form, true))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST /applications/7/rename without CSRF status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if applications.renameID != 0 {
		t.Fatalf("rename without CSRF reached the service: (%d, %q)", applications.renameID, applications.renameName)
	}
}

func TestRenameApplicationRendersInlineValidationError(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Shop", FolderName: "shop"}},
		renameErr:    application.ErrAlreadyExists,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"name":       {"Blog"},
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newRenameRequest(web.csrfToken, form, true))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST duplicate rename status = %d, want %d", recorder.Code, http.StatusUnprocessableEntity)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`An application with that name already exists.`,
		`id="application-name-error"`,
		`role="alert"`,
		`aria-invalid="true"`,
		`value="Blog"`,
		`data-application-name="Shop"`,
		`id="application-breadcrumb-name" hx-swap-oob="true">Shop</span>`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("rename error fragment did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `data-application-name-form hidden`) {
		t.Fatalf("rename error fragment kept the editor closed: %s", body)
	}
}

func TestRenameApplicationEscapesFragmentName(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Shop", FolderName: "shop"}},
		renamed:      application.Application{ID: 7, Name: "<b>Bold</b>", FolderName: "shop"},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"name":       {"<b>Bold</b>"},
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newRenameRequest(web.csrfToken, form, true))

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /applications/7/rename status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `&lt;b&gt;Bold&lt;/b&gt;`) {
		t.Fatalf("rename fragment did not escape the name: %s", body)
	}
	if strings.Contains(body, "<b>Bold</b>") {
		t.Fatalf("rename fragment rendered raw HTML: %s", body)
	}
}

func TestRenameApplicationReturnsNotFoundForMissingApplication(t *testing.T) {
	applications := &fakeApplicationService{renameErr: application.ErrNotFound}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"name":       {"Shop front"},
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newRenameRequest(web.csrfToken, form, true))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("POST rename of missing application status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestRenameApplicationRejectsInvalidID(t *testing.T) {
	applications := &fakeApplicationService{}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/applications/0/rename", strings.NewReader(url.Values{"csrf_token": {web.csrfToken}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("POST /applications/0/rename status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestRenameApplicationFallbackRendersFullPageError(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Shop", FolderName: "shop"}},
		renameErr:    application.ErrNameTooLong,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token": {web.csrfToken},
		"name":       {strings.Repeat("n", application.MaxNameLength+1)},
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newRenameRequest(web.csrfToken, form, false))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST rename fallback status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`Application names must be 64 characters or fewer.`,
		`id="application-name-error"`,
		`data-application-name="Shop"`,
		`<h1 id="page-title">Shop</h1>`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("rename fallback page did not render %q: %s", expected, body)
		}
	}
}

func TestApplicationDetailsRendersInlineTitleEditor(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{
		ID:         7,
		Name:       "Shop",
		FolderName: "shop",
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
		`<h1 id="page-title">Shop</h1>`,
		`id="application-title-block"`,
		`data-application-name="Shop"`,
		`data-application-name-edit`,
		`hx-post="/applications/7/rename"`,
		`id="application-breadcrumb-name"`,
		`data-application-name-form hidden`,
		`/static/htmx.min.js`,
		`/static/application-name.js`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /applications/7 did not render title editor %q: %s", expected, body)
		}
	}
}
