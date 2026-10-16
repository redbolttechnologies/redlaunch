package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

func remoteOAuthToken(t *testing.T, f oauthHandlerFixture, resource string) string {
	t.Helper()
	approved := f.decision(t, resource, "approve")
	if approved.Code != 303 {
		t.Fatalf("approval = %d %s", approved.Code, approved.Body.String())
	}
	location, _ := url.Parse(approved.Header().Get("Location"))
	w := f.exchange(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {f.clientID}, "code": {location.Query().Get("code")}, "redirect_uri": {oauthHandlerRedirect}, "code_verifier": {oauthHandlerVerifier}, "resource": {resource}})
	if w.Code != 200 {
		t.Fatalf("exchange = %d %s", w.Code, w.Body.String())
	}
	return decodeOAuthJSON(t, w.Body.String())["access_token"].(string)
}

func mcpRequest(f oauthHandlerFixture, path, token, body, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, oauthHandlerOrigin+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-11-25")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	f.web.Routes().ServeHTTP(w, r)
	return w
}

func TestMCPDiscoveryAndAuthenticationChallenges(t *testing.T) {
	f := newOAuthHandlerFixture(t)
	for _, path := range []string{"/mcp/manage", "/mcp/apps/1"} {
		metadata := oauthHandlerRequest(f.web, "GET", oauthHandlerOrigin+protectedResourcePrefix+path, nil, "")
		if metadata.Code != 200 {
			t.Fatalf("metadata = %d %s", metadata.Code, metadata.Body.String())
		}
		if decodeOAuthJSON(t, metadata.Body.String())["resource"] != oauthHandlerOrigin+path {
			t.Fatal("incorrect resource identifier")
		}
		w := mcpRequest(f, path, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "")
		if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), oauthHandlerOrigin+protectedResourcePrefix+path) {
			t.Fatalf("unauthenticated = %d %s", w.Code, w.Header().Get("WWW-Authenticate"))
		}
	}
	w := mcpRequest(f, "/mcp/manage", "invalid", `{}`, "")
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatal("missing invalid-token challenge")
	}
	// A browser session is not a machine credential.
	w = oauthHandlerRequest(f.web, "POST", oauthHandlerOrigin+"/mcp/manage", strings.NewReader(`{}`), "application/json", f.session)
	if w.Code != 401 {
		t.Fatalf("cookie authentication = %d, want 401", w.Code)
	}
}

func TestMCPAudienceIsolationAndInProcessManagement(t *testing.T) {
	f := newOAuthHandlerFixture(t)
	manage := remoteOAuthToken(t, f, oauthHandlerOrigin+"/mcp/manage")
	run := remoteOAuthToken(t, f, oauthHandlerOrigin+"/mcp/apps/1")
	for _, tc := range []struct{ token, path string }{{manage, "/mcp/apps/1"}, {run, "/mcp/manage"}, {run, "/mcp/apps/2"}} {
		w := mcpRequest(f, tc.path, tc.token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "")
		if w.Code != 403 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `error="insufficient_scope"`) {
			t.Fatalf("cross-resource = %d %s", w.Code, w.Body.String())
		}
	}
	w := mcpRequest(f, "/mcp/manage", manage, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_applications","arguments":{}}}`, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Talent hunt") || strings.Contains(w.Body.String(), `"isError":true`) {
		t.Fatalf("in-process management = %d %s", w.Code, w.Body.String())
	}
	w = mcpRequest(f, "/mcp/apps/1", run, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"run_service"`) || strings.Contains(w.Body.String(), `"list_applications"`) {
		t.Fatalf("run catalog = %s", w.Body.String())
	}
	w = mcpRequest(f, "/mcp/apps/1", run, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_service","arguments":{"application_id":2,"service_name":"migrate"}}}`, "")
	if !strings.Contains(w.Body.String(), `"isError":true`) || !strings.Contains(w.Body.String(), "403") {
		t.Fatalf("cross-application run = %s", w.Body.String())
	}
	// Changed origin/port identifies a different protected resource.
	r := httptest.NewRequest("POST", "http://127.0.0.1:9090/mcp/manage", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+manage)
	w = httptest.NewRecorder()
	f.web.Routes().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("different server origin = %d, want 403", w.Code)
	}
}

func TestMCPOriginMethodsAndRevocation(t *testing.T) {
	f := newOAuthHandlerFixture(t)
	token := remoteOAuthToken(t, f, oauthHandlerOrigin+"/mcp/manage")
	w := mcpRequest(f, "/mcp/manage", token, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "https://attacker.example")
	if w.Code != 403 {
		t.Fatal("foreign Origin accepted")
	}
	w = mcpRequest(f, "/mcp/manage", token, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, oauthHandlerOrigin)
	if w.Code != 200 {
		t.Fatalf("same Origin = %d %s", w.Code, w.Body.String())
	}
	for _, method := range []string{"GET", "DELETE"} {
		w = oauthHandlerRequest(f.web, method, oauthHandlerOrigin+"/mcp/manage", nil, "")
		if w.Code != 405 || w.Header().Get("Allow") != "POST" {
			t.Fatalf("%s = %d, want 405", method, w.Code)
		}
	}
	revoked := oauthHandlerRequest(f.web, "POST", oauthHandlerOrigin+"/oauth/revoke", strings.NewReader(url.Values{"token": {token}}.Encode()), "application/x-www-form-urlencoded")
	if revoked.Code != 200 {
		t.Fatal("revocation failed")
	}
	w = mcpRequest(f, "/mcp/manage", token, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "")
	if w.Code != 401 {
		t.Fatalf("revoked token = %d, want 401", w.Code)
	}
}

func TestMCPRejectsStaticAndExpiredTokens(t *testing.T) {
	f := newOAuthHandlerFixture(t)
	manual, err := f.web.apiTokens.Create(t.Context(), application.APITokenInput{DisplayName: "Manual token", Scope: application.APITokenScopeManage})
	if err != nil {
		t.Fatal(err)
	}
	w := mcpRequest(f, "/mcp/manage", manual.Plaintext, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "")
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("manual token = %d %s", w.Code, w.Body.String())
	}
	if managementStatusWithBearer(f, manual.Plaintext) != 200 {
		t.Fatal("manual token no longer works with the REST API")
	}
	token := remoteOAuthToken(t, f, oauthHandlerOrigin+"/mcp/manage")
	f.web.apiTokens = &fakeAPITokenService{authErr: application.ErrAPITokenExpired}
	w = mcpRequest(f, "/mcp/manage", token, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "")
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("expired token = %d %s", w.Code, w.Body.String())
	}
}

func TestMCPDiscoveryPublicDomainAndSecureCookieTunnel(t *testing.T) {
	f := newOAuthHandlerFixture(t)
	f.web.redlaunchDomains = &fakeApplicationService{redlaunchDomains: []application.RedlaunchDomain{{Name: "admin.example.com"}}}
	w := oauthHandlerRequest(f.web, "GET", "https://admin.example.com"+protectedResourcePrefix+"/mcp/manage", nil, "")
	if w.Code != 200 || decodeOAuthJSON(t, w.Body.String())["resource"] != "https://admin.example.com/mcp/manage" {
		t.Fatalf("public discovery = %d %s", w.Code, w.Body.String())
	}
	w = oauthHandlerRequest(f.web, "GET", "https://attacker.example"+protectedResourcePrefix+"/mcp/manage", nil, "")
	if w.Code != 404 {
		t.Fatal("unconfigured HTTPS Host accepted")
	}
	// Secure browser cookie settings must not turn the tunnel's MCP origin
	// into https when its actual advertised origin is loopback http.
	token := remoteOAuthToken(t, f, oauthHandlerOrigin+"/mcp/manage")
	f.web.cookieSecure = true
	w = mcpRequest(f, "/mcp/manage", token, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, oauthHandlerOrigin)
	if w.Code != 200 {
		t.Fatalf("secure-cookie tunnel = %d %s", w.Code, w.Body.String())
	}
}

func remoteToolPayload(t *testing.T, w *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rpc) != nil || rpc.Result.IsError || len(rpc.Result.Content) != 1 {
		t.Fatalf("tool failed: %d %s", w.Code, w.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestMCPMutationUsesExistingManagementJobs(t *testing.T) {
	f := newOAuthHandlerFixture(t)
	t.Cleanup(func() { _ = f.web.Shutdown(context.Background()) })
	token := remoteOAuthToken(t, f, oauthHandlerOrigin+"/mcp/manage")
	w := mcpRequest(f, "/mcp/manage", token, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_application","arguments":{"name":"Remote app","folder_name":"remote-app"}}}`, "")
	created := remoteToolPayload(t, w)
	var id string
	if json.Unmarshal(created["job_id"], &id) != nil || id == "" {
		t.Fatal("mutation did not return a job ID")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_management_job","arguments":{"job_id":%q}}}`, id)
		result := remoteToolPayload(t, mcpRequest(f, "/mcp/manage", token, body, ""))
		if string(result["status"]) == `"complete"` {
			if !strings.Contains(string(result["result"]), "Remote app") {
				t.Fatal("job returned the wrong result")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("remote management job timed out")
}
