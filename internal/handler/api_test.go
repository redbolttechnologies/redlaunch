package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type fakeAPITokenService struct {
	tokens     []application.APIToken
	nextID     int64
	createErr  error
	revokeID   int64
	revokeErr  error
	authToken  application.APIToken
	authErr    error
	authCalls  int
	expiredIDs map[int64]bool
}

func newFakeAPITokenService() *fakeAPITokenService {
	return &fakeAPITokenService{nextID: 1}
}

func (s *fakeAPITokenService) Authenticate(_ context.Context, plaintext string) (application.APIToken, error) {
	s.authCalls++
	if s.authErr != nil {
		return application.APIToken{}, s.authErr
	}
	if s.expiredIDs[s.authToken.ID] {
		return application.APIToken{}, application.ErrAPITokenExpired
	}
	if plaintext == "valid-token" || (s.authToken.ID != 0 && plaintext == "pinned-token") {
		return s.authToken, nil
	}
	return application.APIToken{}, application.ErrAPITokenInvalid
}

func (s *fakeAPITokenService) List(context.Context) ([]application.APIToken, error) {
	return append([]application.APIToken(nil), s.tokens...), nil
}

func (s *fakeAPITokenService) Create(_ context.Context, input application.APITokenInput) (application.APITokenSetup, error) {
	if s.createErr != nil {
		return application.APITokenSetup{}, s.createErr
	}
	name, err := application.ValidateAPITokenDisplayName(input.DisplayName)
	if err != nil {
		return application.APITokenSetup{}, err
	}
	if input.ApplicationID < 1 {
		return application.APITokenSetup{}, application.ErrAPITokenApplicationRequired
	}
	token := application.APIToken{
		ID:            s.nextID,
		DisplayName:   name,
		Prefix:        "rlr_testtoken",
		ApplicationID: input.ApplicationID,
		Scope:         application.APITokenScopeRun,
		CreatedAt:     time.Now().UTC(),
	}
	s.nextID++
	s.tokens = append(s.tokens, token)
	return application.APITokenSetup{Token: token, Plaintext: "rlr_testtokenplaintext"}, nil
}

func (s *fakeAPITokenService) Revoke(_ context.Context, id int64) error {
	s.revokeID = id
	if s.revokeErr != nil {
		return s.revokeErr
	}
	for index, token := range s.tokens {
		if token.ID == id {
			s.tokens = append(s.tokens[:index], s.tokens[index+1:]...)
			return nil
		}
	}
	return application.ErrAPITokenNotFound
}

func newAPIHandler(t *testing.T, applications *fakeApplicationService, tokens *fakeAPITokenService) *Handler {
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

func apiApplications() *fakeApplicationService {
	return &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Talent hunt", FolderName: "talenthunt"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "migrate"}},
	}
}

func validAPIToken() application.APIToken {
	return application.APIToken{ID: 3, DisplayName: "migrate", Prefix: "rlr_testtoken", ApplicationID: 7, Scope: application.APITokenScopeRun}
}

func decodeAPIResponse(t *testing.T, body string) map[string]string {
	t.Helper()
	var response map[string]string
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode API response %q: %v", body, err)
	}
	return response
}

func TestAPIRunRequiresBearerAuthentication(t *testing.T) {
	applications := apiApplications()
	tokens := newFakeAPITokenService()
	tokens.authToken = validAPIToken()
	web := newAPIHandler(t, applications, tokens)
	handler := web.Routes()

	for _, testCase := range []struct {
		name   string
		header string
	}{
		{name: "missing", header: ""},
		{name: "wrong scheme", header: "Token valid-token"},
		{name: "unknown token", header: "Bearer unknown-token"},
		{name: "malformed", header: "Bearer rlr_short"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/applications/7/services/migrate/run", nil)
			if testCase.header != "" {
				request.Header.Set("Authorization", testCase.header)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("POST run status = %d, want %d", recorder.Code, http.StatusUnauthorized)
			}
			if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
				t.Fatalf("POST run content type = %q, want application/json", contentType)
			}
			if response := decodeAPIResponse(t, recorder.Body.String()); response["error"] == "" {
				t.Fatalf("POST run did not render a JSON error: %s", recorder.Body.String())
			}
			if action, _, _ := applications.getServiceAction(); action != "" {
				t.Fatalf("unauthenticated run action = %q, want no action", action)
			}
		})
	}
}

func TestAPIRunWithoutSessionOrCSRFReachesBearerAuth(t *testing.T) {
	// With login enabled, browser routes redirect to /login, but the machine
	// API must answer 401 JSON: this proves the middleware exemptions without
	// any session or CSRF cookie present.
	applications := apiApplications()
	tokens := newFakeAPITokenService()
	tokens.authToken = validAPIToken()
	web, err := New(nil, applications, &fakeAuthenticationService{enabled: true}, tokens)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	apiRequest := httptest.NewRequest(http.MethodPost, "/api/v1/applications/7/services/migrate/run", nil)
	apiRecorder := httptest.NewRecorder()
	handler.ServeHTTP(apiRecorder, apiRequest)
	if apiRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("API run without credentials status = %d, want %d", apiRecorder.Code, http.StatusUnauthorized)
	}
	if response := decodeAPIResponse(t, apiRecorder.Body.String()); response["error"] == "" {
		t.Fatalf("API run without credentials did not render a JSON error: %s", apiRecorder.Body.String())
	}

	browserRequest := httptest.NewRequest(http.MethodPost, "/applications/7/services/migrate/run", nil)
	browserRequest.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	browserRecorder := httptest.NewRecorder()
	handler.ServeHTTP(browserRecorder, browserRequest)
	if browserRecorder.Code != http.StatusSeeOther {
		t.Fatalf("browser run without session status = %d, want %d", browserRecorder.Code, http.StatusSeeOther)
	}
	if location := browserRecorder.Header().Get("Location"); !strings.HasPrefix(location, "/login") {
		t.Fatalf("browser run without session Location = %q, want login redirect", location)
	}
}

func TestAPIRunEnforcesApplicationScope(t *testing.T) {
	applications := apiApplications()
	tokens := newFakeAPITokenService()
	tokens.authToken = validAPIToken()
	web := newAPIHandler(t, applications, tokens)
	handler := web.Routes()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/applications/8/services/migrate/run", nil)
	request.Header.Set("Authorization", "Bearer valid-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-application run status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if action, _, _ := applications.getServiceAction(); action != "" {
		t.Fatalf("cross-application run action = %q, want no action", action)
	}
}

func TestAPIRunRejectsUnknownServiceFast(t *testing.T) {
	applications := apiApplications()
	tokens := newFakeAPITokenService()
	tokens.authToken = validAPIToken()
	web := newAPIHandler(t, applications, tokens)
	handler := web.Routes()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/applications/7/services/missing/run", nil)
	request.Header.Set("Authorization", "Bearer valid-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown service run status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if action, _, _ := applications.getServiceAction(); action != "" {
		t.Fatalf("unknown service run action = %q, want no action", action)
	}
}

func TestAPIRunDispatchesJobAndReportsCompletion(t *testing.T) {
	applications := apiApplications()
	tokens := newFakeAPITokenService()
	tokens.authToken = validAPIToken()
	web := newAPIHandler(t, applications, tokens)
	handler := web.Routes()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/applications/7/services/migrate/run", nil)
	request.Header.Set("Authorization", "Bearer valid-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("dispatch status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
	dispatch := decodeAPIResponse(t, recorder.Body.String())
	if dispatch["status"] != apiRunJobStateRunning || dispatch["job_id"] == "" || dispatch["status_url"] == "" {
		t.Fatalf("dispatch response = %v, want running job with status URL", dispatch)
	}
	if !strings.HasPrefix(dispatch["status_url"], "/api/v1/applications/7/services/migrate/run/status?id=") {
		t.Fatalf("status URL = %q, want scoped run status path", dispatch["status_url"])
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		statusRequest := httptest.NewRequest(http.MethodGet, dispatch["status_url"], nil)
		statusRequest.Header.Set("Authorization", "Bearer valid-token")
		statusRecorder := httptest.NewRecorder()
		handler.ServeHTTP(statusRecorder, statusRequest)
		if statusRecorder.Code != http.StatusOK {
			t.Fatalf("status code = %d, want %d", statusRecorder.Code, http.StatusOK)
		}
		status := decodeAPIResponse(t, statusRecorder.Body.String())
		if status["status"] == apiRunJobStateComplete {
			break
		}
		if status["status"] != apiRunJobStateRunning {
			t.Fatalf("job status = %v, want running then complete", status)
		}
		if time.Now().After(deadline) {
			t.Fatal("run job did not complete in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if action, _, name := applications.getServiceAction(); action != "run" || name != "migrate" {
		t.Fatalf("service action = (%q, %q), want (run, migrate)", action, name)
	}
}

func TestAPIRunReportsFailureDetail(t *testing.T) {
	applications := apiApplications()
	applications.serviceActionErr = errors.New("run service: migration failed: relation does not exist")
	tokens := newFakeAPITokenService()
	tokens.authToken = validAPIToken()
	web := newAPIHandler(t, applications, tokens)
	handler := web.Routes()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/applications/7/services/migrate/run", nil)
	request.Header.Set("Authorization", "Bearer valid-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("dispatch status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
	statusURL := decodeAPIResponse(t, recorder.Body.String())["status_url"]

	deadline := time.Now().Add(5 * time.Second)
	for {
		statusRequest := httptest.NewRequest(http.MethodGet, statusURL, nil)
		statusRequest.Header.Set("Authorization", "Bearer valid-token")
		statusRecorder := httptest.NewRecorder()
		handler.ServeHTTP(statusRecorder, statusRequest)
		status := decodeAPIResponse(t, statusRecorder.Body.String())
		if status["status"] == apiRunJobStateFailed {
			if !strings.Contains(status["detail"], "relation does not exist") {
				t.Fatalf("failure detail = %q, want migration cause", status["detail"])
			}
			if strings.Contains(status["detail"], "run service:") {
				t.Fatalf("failure detail kept the operational prefix: %q", status["detail"])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run job did not fail in time: %v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAPIRunStatusRequiresScope(t *testing.T) {
	applications := apiApplications()
	tokens := newFakeAPITokenService()
	tokens.authToken = validAPIToken()
	web := newAPIHandler(t, applications, tokens)
	handler := web.Routes()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/applications/7/services/migrate/run/status?id=missing", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status without credentials = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/applications/7/services/migrate/run/status?id=missing", nil)
	request.Header.Set("Authorization", "Bearer valid-token")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown job status = %d, want %d", recorder.Code, http.StatusNotFound)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/applications/8/services/migrate/run/status?id=missing", nil)
	request.Header.Set("Authorization", "Bearer valid-token")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-application status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestAPIRunReportsExpiredToken(t *testing.T) {
	applications := apiApplications()
	tokens := newFakeAPITokenService()
	tokens.authToken = validAPIToken()
	tokens.expiredIDs = map[int64]bool{validAPIToken().ID: true}
	web := newAPIHandler(t, applications, tokens)
	handler := web.Routes()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/applications/7/services/migrate/run", nil)
	request.Header.Set("Authorization", "Bearer pinned-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if response := decodeAPIResponse(t, recorder.Body.String()); !strings.Contains(response["error"], "expired") {
		t.Fatalf("expired token error = %q, want expiry message", response["error"])
	}
	if action, _, _ := applications.getServiceAction(); action != "" {
		t.Fatalf("expired token action = %q, want no action", action)
	}
}
