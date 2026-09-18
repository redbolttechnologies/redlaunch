package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func newSettingsHandlerWithAPITokens(t *testing.T, applications *fakeApplicationService, tokens *fakeAPITokenService) *Handler {
	t.Helper()
	dependencies := []any{applications}
	if tokens != nil {
		dependencies = append(dependencies, tokens)
	}
	web, err := New(nil, dependencies...)
	if err != nil {
		t.Fatal(err)
	}
	return web
}

func TestSettingsPageRendersAPITokensTab(t *testing.T) {
	tokens := newFakeAPITokenService()
	tokens.tokens = []application.APIToken{{ID: 1, DisplayName: "talenthunt migrate via GHA", Prefix: "rlr_01234567", ApplicationID: 7, Scope: application.APITokenScopeRun}}
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Talent hunt", FolderName: "talenthunt"}},
	}
	web := newSettingsHandlerWithAPITokens(t, applications, tokens)

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /settings status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`id="api-tokens-tab"`,
		`id="api-tokens-panel"`,
		`data-api-token-add`,
		`id="api-token-create-dialog"`,
		`data-api-token-create-dialog`,
		`action="/settings/api-tokens"`,
		`action="/settings/api-tokens/delete"`,
		`talenthunt migrate via GHA`,
		`rlr_01234567`,
		`Run services in Talent hunt`,
		`/static/api-tokens.js`,
		`name="display_name"`,
		`name="application_id"`,
		`name="expires"`,
		`service-widget-body settings-api-tokens-body`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /settings did not render %q", expected)
		}
	}
	if strings.Contains(body, "rlr_testtokenplaintext") {
		t.Fatalf("GET /settings rendered token plaintext without creation")
	}
}

func TestSettingsPageRendersAPITokenApplicationPicker(t *testing.T) {
	tokens := newFakeAPITokenService()
	applications := &fakeApplicationService{
		applications: []application.Application{
			{ID: 7, Name: "Talent hunt", FolderName: "talenthunt"},
			{ID: 9, Name: "Status page", FolderName: "status-page"},
		},
	}
	web := newSettingsHandlerWithAPITokens(t, applications, tokens)

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /settings status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`value="7"`,
		`value="9"`,
		`>Talent hunt</option>`,
		`>Status page</option>`,
		`Never expires`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /settings did not render application picker %q", expected)
		}
	}
}

func TestCreateAPITokenShowsPlaintextOnce(t *testing.T) {
	tokens := newFakeAPITokenService()
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Talent hunt", FolderName: "talenthunt"}},
	}
	web := newSettingsHandlerWithAPITokens(t, applications, tokens)

	form := url.Values{"csrf_token": {web.csrfToken}, "display_name": {"talenthunt migrate via GHA"}, "application_id": {"7"}, "expires": {"90"}}
	request := httptest.NewRequest(http.MethodPost, "/settings/api-tokens", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST /settings/api-tokens status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"will not be shown again",
		"rlr_testtokenplaintext",
		`id="api-token-setup-dialog"`,
		`data-api-token-setup-dialog`,
		`data-api-token-setup-open`,
		`id="api-token-plaintext-value"`,
		`data-copy-target="api-token-plaintext-value"`,
		`talenthunt migrate via GHA`,
		`id="api-tokens-panel"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("POST /settings/api-tokens did not render one-time handoff %q", expected)
		}
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("API token handoff Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}

	followUp := httptest.NewRecorder()
	web.Routes().ServeHTTP(followUp, httptest.NewRequest(http.MethodGet, "/settings?tab=api-tokens", nil))
	if followUp.Code != http.StatusOK {
		t.Fatalf("GET /settings after creation status = %d, want %d", followUp.Code, http.StatusOK)
	}
	if strings.Contains(followUp.Body.String(), "rlr_testtokenplaintext") {
		t.Fatalf("token plaintext persisted beyond the creation response")
	}
}

func TestCreateAPITokenRendersValidationError(t *testing.T) {
	tokens := newFakeAPITokenService()
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Talent hunt", FolderName: "talenthunt"}},
	}
	web := newSettingsHandlerWithAPITokens(t, applications, tokens)

	form := url.Values{"csrf_token": {web.csrfToken}, "display_name": {""}, "application_id": {"7"}, "expires": {"90"}}
	request := httptest.NewRequest(http.MethodPost, "/settings/api-tokens", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST /settings/api-tokens status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-api-token-create-open`,
		`Enter a display name for the API token`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("POST /settings/api-tokens did not render validation error %q", expected)
		}
	}
}

func TestCreateAPITokenRejectsUnknownApplication(t *testing.T) {
	tokens := newFakeAPITokenService()
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Talent hunt", FolderName: "talenthunt"}},
	}
	web := newSettingsHandlerWithAPITokens(t, applications, tokens)

	for _, form := range []url.Values{
		{"csrf_token": {web.csrfToken}, "display_name": {"job"}, "application_id": {""}, "expires": {"90"}},
		{"csrf_token": {web.csrfToken}, "display_name": {"job"}, "application_id": {"abc"}, "expires": {"90"}},
		{"csrf_token": {web.csrfToken}, "display_name": {"job"}, "application_id": {"7"}, "expires": {"13"}},
	} {
		request := httptest.NewRequest(http.MethodPost, "/settings/api-tokens", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
		recorder := httptest.NewRecorder()
		web.Routes().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("POST /settings/api-tokens status = %d, want %d", recorder.Code, http.StatusBadRequest)
		}
	}
}

func TestCreateAPITokenRequiresCSRF(t *testing.T) {
	tokens := newFakeAPITokenService()
	web := newSettingsHandlerWithAPITokens(t, &fakeApplicationService{}, tokens)

	form := url.Values{"display_name": {"job"}, "application_id": {"7"}, "expires": {"90"}}
	request := httptest.NewRequest(http.MethodPost, "/settings/api-tokens", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST /settings/api-tokens without CSRF status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestRevokeAPITokenRedirects(t *testing.T) {
	tokens := newFakeAPITokenService()
	tokens.tokens = []application.APIToken{{ID: 3, DisplayName: "old token"}}
	web := newSettingsHandlerWithAPITokens(t, &fakeApplicationService{}, tokens)

	form := url.Values{"csrf_token": {web.csrfToken}, "id": {"3"}}
	request := httptest.NewRequest(http.MethodPost, "/settings/api-tokens/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings/api-tokens/delete status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/settings?tab=api-tokens" {
		t.Fatalf("revoke Location = %q, want /settings?tab=api-tokens", got)
	}
	if tokens.revokeID != 3 {
		t.Fatalf("revoke ID = %d, want 3", tokens.revokeID)
	}
}

func TestRevokeAPITokenRendersNotFound(t *testing.T) {
	tokens := newFakeAPITokenService()
	tokens.revokeErr = errors.Join(errors.New("wrapped"), application.ErrAPITokenNotFound)
	web := newSettingsHandlerWithAPITokens(t, &fakeApplicationService{}, tokens)

	form := url.Values{"csrf_token": {web.csrfToken}, "id": {"3"}}
	request := httptest.NewRequest(http.MethodPost, "/settings/api-tokens/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("POST /settings/api-tokens/delete status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "could not be found") {
		t.Fatalf("revoke failure did not explain the missing token: %s", body)
	}
}
