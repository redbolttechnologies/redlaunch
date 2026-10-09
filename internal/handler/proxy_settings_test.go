package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type fakeProxyPolicyService struct {
	saveErr  error
	saveGate chan struct{}
	fakeApplicationService
	policy, global application.ProxySettings
	saved          int
	savedID        int64
}

func (f *fakeProxyPolicyService) GetProxySettings(context.Context, int64) (application.ProxySettings, application.ProxySettings, error) {
	return f.policy, f.global, nil
}
func (f *fakeProxyPolicyService) SaveProxySettings(ctx context.Context, id int64, p application.ProxySettings) error {
	if f.saveGate != nil {
		select {
		case <-f.saveGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved++
	f.savedID = id
	f.policy = p
	return nil
}

func TestProxySettingsPagesAndSave(t *testing.T) {
	fake := &fakeProxyPolicyService{fakeApplicationService: fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Test <app>"}}}}
	web, err := New(nil, fake)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/proxy/settings", "/applications/7?tab=proxy"} {
		w := httptest.NewRecorder()
		web.Routes().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if path == "/applications/7?tab=proxy" && os.Getenv("REDLAUNCH_PROXY_UI_FIXTURE") != "" {
			if err := os.WriteFile(os.Getenv("REDLAUNCH_PROXY_UI_FIXTURE"), w.Body.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for _, want := range []string{"Compression", "Security headers", "Request body limit", "Upstream timeouts", "Cache-Control", `name="csrf_token"`} {
			if !strings.Contains(w.Body.String(), want) {
				t.Fatalf("missing %q", want)
			}
		}
		if strings.Contains(w.Body.String(), "Test <app>") {
			t.Fatal("unescaped app name")
		}
	}

	wRedirect := httptest.NewRecorder()
	web.Routes().ServeHTTP(wRedirect, httptest.NewRequest("GET", "/applications/7/proxy-settings", nil))
	if wRedirect.Code != http.StatusSeeOther || wRedirect.Header().Get("Location") != "/applications/7?tab=proxy" {
		t.Fatalf("legacy page did not redirect to tab: %d %s", wRedirect.Code, wRedirect.Header().Get("Location"))
	}
	form := url.Values{"csrf_token": {web.csrfToken}, "cache_enabled": {"on"}, "cache_control": {"private, no-store"}, "body_enabled": {"on"}, "body_bytes": {"1024"}}
	req := httptest.NewRequest("POST", "/applications/7/proxy-settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	w := httptest.NewRecorder()
	web.Routes().ServeHTTP(w, req)
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	job := web.proxyActionJobs.get(location.Query().Get("proxy_action_job"))
	if job == nil {
		t.Fatal("missing settings job")
	}
	deadline := time.Now().Add(5 * time.Second)
	for job.snapshot().State == proxyActionJobStateRunning && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if job.snapshot().State != proxyActionJobStateComplete {
		t.Fatal("job did not complete")
	}
	if job.snapshot().CloseURL != "/applications/7?tab=proxy" {
		t.Fatal("job lost return URL")
	}
	if w.Code != 303 || fake.saved != 1 || fake.savedID != 7 || *fake.policy.CacheControl != "private, no-store" || fake.policy.BodyLimit.Bytes != 1024 {
		t.Fatalf("save failed: %d %#v", w.Code, fake)
	}
}

func TestProxySettingsRejectInvalidAndCSRF(t *testing.T) {
	fake := &fakeProxyPolicyService{}
	web, err := New(nil, fake)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		csrf   bool
		value  string
		status int
	}{{false, "1024", 403}, {true, "-1", 400}, {true, "not a number", 400}} {
		form := url.Values{"body_enabled": {"on"}, "body_bytes": {tc.value}}
		if tc.csrf {
			form.Set("csrf_token", web.csrfToken)
		}
		req := httptest.NewRequest("POST", "/proxy/settings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
		w := httptest.NewRecorder()
		web.Routes().ServeHTTP(w, req)
		if w.Code != tc.status || fake.saved != 0 {
			t.Fatalf("status %d, saves %d", w.Code, fake.saved)
		}
		if tc.csrf && !strings.Contains(w.Body.String(), `value="`+tc.value+`"`) {
			t.Fatal("invalid value was not preserved")
		}
	}
	w := httptest.NewRecorder()
	web.Routes().ServeHTTP(w, httptest.NewRequest("GET", "/applications/999/proxy-settings", nil))
	if w.Code != 404 {
		t.Fatalf("missing app returned %d", w.Code)
	}
}

func TestProxySettingsSaveRunsInBackgroundAndReportsFailure(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeProxyPolicyService{saveGate: gate, saveErr: errors.New("private infrastructure detail")}
	web, err := New(nil, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer close(gate)
	form := url.Values{"csrf_token": {web.csrfToken}, "cache_enabled": {"on"}, "cache_control": {"no-store"}}
	submit := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/proxy/settings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
		w := httptest.NewRecorder()
		web.Routes().ServeHTTP(w, req)
		return w
	}
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() { responses <- submit() }()
	var response *httptest.ResponseRecorder
	select {
	case response = <-responses:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP response blocked on proxy operation")
	}
	if response.Code != 303 {
		t.Fatalf("save returned %d", response.Code)
	}
	if w := submit(); w.Code != 409 {
		t.Fatalf("concurrent save returned %d", w.Code)
	}
	location, _ := url.Parse(response.Header().Get("Location"))
	job := web.proxyActionJobs.get(location.Query().Get("proxy_action_job"))
	if job == nil {
		t.Fatal("missing job")
	}
	gate <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for job.snapshot().State == proxyActionJobStateRunning && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	w := httptest.NewRecorder()
	web.Routes().ServeHTTP(w, httptest.NewRequest("GET", "/proxy/action/status?id="+job.id, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Proxy settings failed") || strings.Contains(w.Body.String(), "private infrastructure detail") {
		t.Fatalf("incorrect failure response: %s", w.Body.String())
	}
}

func TestApplicationProxyTabOrderAndValidation(t *testing.T) {
	fake := &fakeProxyPolicyService{fakeApplicationService: fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Proxy app"}}}}
	web, err := New(nil, fake)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	web.Routes().ServeHTTP(w, httptest.NewRequest("GET", "/applications/7?tab=proxy", nil))
	body := w.Body.String()
	domains, proxy, settings := strings.Index(body, `id="domains-tab"`), strings.Index(body, `id="proxy-tab"`), strings.Index(body, `id="settings-tab"`)
	if w.Code != 200 || domains < 0 || proxy <= domains || settings <= proxy {
		t.Fatal("Proxy tab is not between Domains and Settings")
	}
	if strings.Contains(body, `href="/applications/7/proxy-settings"`) {
		t.Fatal("separate Proxy settings button is still present")
	}
	if !strings.Contains(body, `id="proxy-tab" type="button" role="tab" aria-controls="proxy-panel" aria-selected="true"`) || !strings.Contains(body, `action="/applications/7/proxy-settings"`) {
		t.Fatal("Proxy tab or form is not active")
	}
	form := url.Values{"csrf_token": {web.csrfToken}, "body_enabled": {"on"}, "body_bytes": {"invalid"}}
	req := httptest.NewRequest("POST", "/applications/7/proxy-settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	w = httptest.NewRecorder()
	web.Routes().ServeHTTP(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `data-application-initial-tab="proxy"`) || !strings.Contains(w.Body.String(), `value="invalid"`) || !strings.Contains(w.Body.String(), "Invalid proxy settings") || fake.saved != 0 {
		t.Fatalf("invalid submission did not stay in Proxy tab: %d", w.Code)
	}
}
