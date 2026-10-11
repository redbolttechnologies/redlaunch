package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

type fakeManagementAccess struct {
	selfUpdateService
	calls int
	mode  string
	err   error
}

func (s *fakeManagementAccess) QueueManagementAccessMode(_ context.Context, mode string) error {
	s.calls++
	s.mode = mode
	return s.err
}

func managementAccessFixture(t *testing.T) (oauthHandlerFixture, *fakeManagementAccess) {
	t.Helper()
	f := newOAuthHandlerFixture(t)
	s := &fakeManagementAccess{}
	f.web.selfUpdater = s
	f.web.redlaunchDomains = &fakeApplicationService{redlaunchDomains: []application.RedlaunchDomain{{Name: "manager.example.com"}}}
	return f, s
}

func TestManagementAccessSettingsShowsRunningModeAndConfirmation(t *testing.T) {
	f, _ := managementAccessFixture(t)
	w := oauthHandlerRequest(f.web, "GET", oauthHandlerOrigin+"/settings", nil, "", f.session)
	if w.Code != 200 {
		t.Fatal("settings did not render")
	}
	for _, text := range []string{"Management access mode", "Running mode:", "SSH-only", "Managed HTTPS", `action="/settings/management-access"`, `name="confirm" value="yes" required`, `name="current_mode" value="ssh-only"`, "does not change port bindings"} {
		if !strings.Contains(w.Body.String(), text) {
			t.Fatalf("missing control %q", text)
		}
	}
	f.web.accessMode = accessModeManagedHTTPS
	f.web.cookieSecure = true
	w = oauthHandlerRequest(f.web, "GET", oauthHandlerOrigin+"/settings", nil, "", f.session)
	if !strings.Contains(w.Body.String(), `value="managed-https" selected`) || !strings.Contains(w.Body.String(), "AUTH_COOKIE_SECURE=true") {
		t.Fatal("effective mode or explicit secure-cookie warning missing")
	}
}

func TestManagementAccessRequiresSessionCSRFAcknowledgementAndFreshMode(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		mode, confirm, current string
		session, csrf          bool
		status                 int
	}{
		{"anonymous", "managed-https", "yes", "ssh-only", false, true, 303},
		{"no CSRF", "managed-https", "yes", "ssh-only", true, false, 403},
		{"no confirmation", "managed-https", "", "ssh-only", true, true, 400},
		{"invalid mode", "<script>bad</script>", "yes", "ssh-only", true, true, 400},
		{"stale mode", "managed-https", "yes", "managed-https", true, true, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := managementAccessFixture(t)
			form := url.Values{"access_mode": {tc.mode}, "confirm": {tc.confirm}, "current_mode": {tc.current}, "csrf_token": {f.csrf.Value}}
			var cookies []*http.Cookie
			if tc.session {
				cookies = append(cookies, f.session)
			}
			if tc.csrf {
				cookies = append(cookies, f.csrf)
			}
			w := oauthHandlerRequest(f.web, "POST", oauthHandlerOrigin+"/settings/management-access", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", cookies...)
			if w.Code != tc.status || s.calls != 0 {
				t.Fatalf("status=%d calls=%d", w.Code, s.calls)
			}
			if strings.Contains(w.Body.String(), "<script>bad</script>") {
				t.Fatal("unescaped input rendered")
			}
		})
	}
}

func TestManagementAccessQueuesRestartWithoutChangingRunningSecurity(t *testing.T) {
	f, s := managementAccessFixture(t)
	form := url.Values{"access_mode": {"managed-https"}, "confirm": {"yes"}, "current_mode": {"ssh-only"}, "csrf_token": {f.csrf.Value}}
	w := oauthHandlerRequest(f.web, "POST", oauthHandlerOrigin+"/settings/management-access", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", f.session, f.csrf)
	if w.Code != 200 || s.calls != 1 || s.mode != "managed-https" || !strings.Contains(w.Body.String(), "a restart was queued") {
		t.Fatalf("save failed: status=%d calls=%d", w.Code, s.calls)
	}
	if f.web.accessMode != accessModeSSHOnly {
		t.Fatal("running security mutated before restart")
	}
}

func TestManagementAccessRejectsMissingDomainAndHidesServiceErrors(t *testing.T) {
	f, s := managementAccessFixture(t)
	form := url.Values{"access_mode": {"managed-https"}, "confirm": {"yes"}, "current_mode": {"ssh-only"}, "csrf_token": {f.csrf.Value}}
	f.web.redlaunchDomains = &fakeApplicationService{}
	w := oauthHandlerRequest(f.web, "POST", oauthHandlerOrigin+"/settings/management-access", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", f.session, f.csrf)
	if w.Code != 400 || s.calls != 0 || !strings.Contains(w.Body.String(), "Add a Redlaunch domain") {
		t.Fatal("managed HTTPS allowed without a domain")
	}
	f.web.redlaunchDomains = &fakeApplicationService{redlaunchDomains: []application.RedlaunchDomain{{Name: "manager.example.com"}}}
	s.err = errors.New("TEST_ONLY_INTERNAL_DETAIL")
	w = oauthHandlerRequest(f.web, "POST", oauthHandlerOrigin+"/settings/management-access", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", f.session, f.csrf)
	if w.Code != 500 || strings.Contains(w.Body.String(), "TEST_ONLY_INTERNAL_DETAIL") || !strings.Contains(w.Body.String(), "could not be confirmed") {
		t.Fatal("service failure was not safely rendered")
	}
	f.web.selfUpdater = nil
	w = oauthHandlerRequest(f.web, "GET", oauthHandlerOrigin+"/settings", nil, "", f.session)
	if !strings.Contains(w.Body.String(), `name="access_mode" aria-describedby="management-access-help" disabled`) || !strings.Contains(w.Body.String(), "restart service is not configured") {
		t.Fatal("unavailable state missing")
	}
	w = oauthHandlerRequest(f.web, "POST", oauthHandlerOrigin+"/settings/management-access", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", f.session, f.csrf)
	if w.Code != 503 {
		t.Fatal("missing restart service accepted a save")
	}
}

func TestManagementAccessRejectsCrossOriginSubmission(t *testing.T) {
	f, s := managementAccessFixture(t)
	form := url.Values{"access_mode": {"managed-https"}, "confirm": {"yes"}, "current_mode": {"ssh-only"}, "csrf_token": {f.csrf.Value}}
	r := httptest.NewRequest("POST", oauthHandlerOrigin+"/settings/management-access", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://attacker.example.com")
	r.AddCookie(f.session)
	r.AddCookie(f.csrf)
	w := httptest.NewRecorder()
	f.web.Routes().ServeHTTP(w, r)
	if w.Code != 403 || s.calls != 0 {
		t.Fatal("cross-origin restart accepted")
	}
}
