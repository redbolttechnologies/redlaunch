package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

func settingsMCPTokenPair(t *testing.T, f oauthHandlerFixture) (string, string, int64) {
	t.Helper()
	resource := oauthHandlerOrigin + "/mcp/manage"
	approved := f.decision(t, resource, "approve")
	location, _ := url.Parse(approved.Header().Get("Location"))
	w := f.exchange(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {f.clientID}, "code": {location.Query().Get("code")}, "redirect_uri": {oauthHandlerRedirect}, "code_verifier": {oauthHandlerVerifier}, "resource": {resource}})
	if w.Code != 200 {
		t.Fatalf("exchange = %d %s", w.Code, w.Body.String())
	}
	tokens := decodeOAuthJSON(t, w.Body.String())
	items, err := f.web.oauth.ListConnections(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatal("missing connection", err)
	}
	return tokens["access_token"].(string), tokens["refresh_token"].(string), items[0].ID
}

func TestSettingsMCPConnectionsMetadataAndEscaping(t *testing.T) {
	f := newOAuthHandlerFixture(t)
	f.clientID = f.register(t, "<script>alert(1)</script>", oauthHandlerRedirect)
	access, refresh, id := settingsMCPTokenPair(t, f)
	manual, err := f.web.apiTokens.Create(t.Context(), application.APITokenInput{DisplayName: "CI automation", Scope: application.APITokenScopeManage})
	if err != nil {
		t.Fatal(err)
	}
	w := oauthHandlerRequest(f.web, "GET", oauthHandlerOrigin+"/settings?tab=mcp", nil, "", f.session)
	if w.Code != 200 {
		t.Fatalf("settings = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, value := range []string{"MCP connections", "&lt;script&gt;alert(1)&lt;/script&gt;", "Last used", "Grant expires", "Never", "CI automation", manual.Token.Prefix, fmt.Sprintf("mcp_revoke=%d", id), oauthHandlerOrigin + "/mcp/manage"} {
		if !strings.Contains(body, value) {
			t.Fatalf("page missing %q", value)
		}
	}
	for _, value := range []string{access, refresh, access[:12], "<script>alert(1)</script>"} {
		if strings.Contains(body, value) {
			t.Fatal("settings exposed credential material or unescaped markup")
		}
	}
	if !strings.Contains(body, `id="mcp-tab" type="button" role="tab" aria-controls="mcp-panel" aria-selected="true"`) {
		t.Fatal("MCP tab not active server-side")
	}
	data, err := f.web.loadSettingsPageData(t.Context())
	if err != nil || len(data.APITokens) != 1 || data.APITokens[0].ID != manual.Token.ID {
		t.Fatal("OAuth access token leaked into manual list", err)
	}
	confirm := oauthHandlerRequest(f.web, "GET", fmt.Sprintf("%s/settings?tab=mcp&mcp_revoke=%d", oauthHandlerOrigin, id), nil, "", f.session)
	if confirm.Code != 200 || !strings.Contains(confirm.Body.String(), `data-mcp-dialog open`) || !strings.Contains(confirm.Body.String(), `name="confirm" value="yes"`) {
		t.Fatalf("confirmation = %d %s", confirm.Code, confirm.Body.String())
	}
	if managementStatusWithBearer(f, access) != 200 {
		t.Fatal("GET confirmation revoked the token")
	}
}

func TestSettingsMCPRevocationRequiresSessionCSRFAndConfirmation(t *testing.T) {
	f := newOAuthHandlerFixture(t)
	access, refresh, id := settingsMCPTokenPair(t, f)
	target := oauthHandlerOrigin + "/settings/mcp-connections/delete"
	form := url.Values{"id": {fmt.Sprint(id)}, "confirm": {"yes"}, "csrf_token": {f.csrf.Value}}
	for _, tc := range []struct {
		name          string
		session, csrf bool
		mutate        func(url.Values)
		status        int
	}{
		{"anonymous", false, true, func(url.Values) {}, 303},
		{"missing cookie", true, false, func(url.Values) {}, 403},
		{"wrong CSRF", true, true, func(v url.Values) { v.Set("csrf_token", strings.Repeat("b", 64)) }, 403},
		{"missing confirmation", true, true, func(v url.Values) { v.Del("confirm") }, 400},
		{"invalid ID", true, true, func(v url.Values) { v.Set("id", "../oauth") }, 400},
		{"unknown ID", true, true, func(v url.Values) { v.Set("id", "9999") }, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values, _ := url.ParseQuery(form.Encode())
			tc.mutate(values)
			var cookies []*http.Cookie
			if tc.session {
				cookies = append(cookies, f.session)
			}
			if tc.csrf {
				cookies = append(cookies, f.csrf)
			}
			w := oauthHandlerRequest(f.web, "POST", target, strings.NewReader(values.Encode()), "application/x-www-form-urlencoded", cookies...)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if managementStatusWithBearer(f, access) != 200 {
				t.Fatal("rejected request revoked connection")
			}
		})
	}
	w := oauthHandlerRequest(f.web, "POST", target, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", f.session, f.csrf)
	if w.Code != 303 || w.Header().Get("Location") != "/settings?tab=mcp" {
		t.Fatalf("revoke = %d %s", w.Code, w.Body.String())
	}
	if managementStatusWithBearer(f, access) != 401 {
		t.Fatal("revoked access token still works")
	}
	w = f.exchange(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {f.clientID}, "refresh_token": {refresh}})
	if w.Code != 400 {
		t.Fatal("revoked refresh still works")
	}
	w = oauthHandlerRequest(f.web, "GET", oauthHandlerOrigin+"/settings?tab=mcp", nil, "", f.session)
	if !strings.Contains(w.Body.String(), "No MCP connections are approved") {
		t.Fatal("empty connection state missing")
	}
}

type failedConnectionRevoke struct{ oauthService }

func (s failedConnectionRevoke) RevokeConnection(context.Context, int64) error {
	return errors.New("test repository failure")
}

func TestSettingsMCPRevocationErrorReopensConfirmation(t *testing.T) {
	f := newOAuthHandlerFixture(t)
	access, _, id := settingsMCPTokenPair(t, f)
	f.web.oauth = failedConnectionRevoke{f.web.oauth}
	form := url.Values{"id": {fmt.Sprint(id)}, "confirm": {"yes"}, "csrf_token": {f.csrf.Value}}
	w := oauthHandlerRequest(f.web, "POST", oauthHandlerOrigin+"/settings/mcp-connections/delete", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", f.session, f.csrf)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "could not be revoked right now") || !strings.Contains(w.Body.String(), `data-mcp-dialog open`) {
		t.Fatalf("error state = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "test repository failure") || strings.Contains(w.Body.String(), access) {
		t.Fatal("error response leaked internal details")
	}
}

type settingsConnectionList struct {
	oauthService
	items []application.OAuthConnection
	err   error
}

func (s settingsConnectionList) ListConnections(context.Context) ([]application.OAuthConnection, error) {
	return s.items, s.err
}

func TestSettingsMCPExpiredAndListErrorStates(t *testing.T) {
	f := newOAuthHandlerFixture(t)
	_, _, _ = settingsMCPTokenPair(t, f)
	items, err := f.web.oauth.ListConnections(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	items[0].ExpiresAt = time.Now().Add(-time.Hour)
	items[0].Expired = true
	f.web.oauth = settingsConnectionList{f.web.oauth, items, nil}
	w := oauthHandlerRequest(f.web, "GET", oauthHandlerOrigin+"/settings?tab=mcp", nil, "", f.session)
	if w.Code != 200 || !strings.Contains(w.Body.String(), ">Expired</span>") || !strings.Contains(w.Body.String(), "data-mcp-revoke") {
		t.Fatalf("expired state = %d", w.Code)
	}
	f.web.oauth = settingsConnectionList{f.web.oauth, nil, errors.New("test database failure")}
	w = oauthHandlerRequest(f.web, "GET", oauthHandlerOrigin+"/settings?tab=mcp", nil, "", f.session)
	if w.Code != 500 || strings.Contains(w.Body.String(), "test database failure") {
		t.Fatalf("list error = %d %s", w.Code, w.Body.String())
	}
}
