package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
	"redlaunch/internal/auth"
	"redlaunch/internal/service"
	"redlaunch/internal/store"
)

const (
	oauthHandlerOrigin    = "http://127.0.0.1:8080"
	oauthHandlerRedirect  = "http://127.0.0.1/callback"
	oauthHandlerVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	oauthHandlerChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

type oauthHandlerFixture struct {
	web      *Handler
	session  *http.Cookie
	csrf     *http.Cookie
	clientID string
}

func newOAuthHandlerFixture(t *testing.T) oauthHandlerFixture {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "oauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	users, err := service.NewUserService(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := users.Create(t.Context(), "admin@example.com", "test password"); err != nil {
		t.Fatal(err)
	}
	authentication, err := auth.New(auth.Config{SessionSecret: strings.Repeat("s", 32)}, database)
	if err != nil {
		t.Fatal(err)
	}
	oauth, err := service.NewOAuthService(database, database)
	if err != nil {
		t.Fatal(err)
	}
	apiTokens, err := service.NewAPITokenService(database, database)
	if err != nil {
		t.Fatal(err)
	}
	item, err := database.Create(t.Context(), application.Application{Name: "Talent hunt", FolderName: "talenthunt"})
	if err != nil {
		t.Fatal(err)
	}
	web, err := New(nil, &fakeApplicationService{applications: []application.Application{item}}, users, authentication, oauth, apiTokens)
	if err != nil {
		t.Fatal(err)
	}
	user, err := authentication.AuthenticatePassword(t.Context(), "admin@example.com", "test password", "ip")
	if err != nil {
		t.Fatal(err)
	}
	value, err := authentication.NewSession(user)
	if err != nil {
		t.Fatal(err)
	}
	fixture := oauthHandlerFixture{
		web:     web,
		session: &http.Cookie{Name: sessionCookieName, Value: value},
		csrf:    &http.Cookie{Name: csrfCookieName, Value: strings.Repeat("a", 64)},
	}
	fixture.clientID = fixture.register(t, "Codex", oauthHandlerRedirect)
	return fixture
}

func oauthHandlerRequest(web *Handler, method, target string, body io.Reader, contentType string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, body)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for _, cookie := range cookies {
		if cookie != nil {
			request.AddCookie(cookie)
		}
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	return recorder
}

func (f oauthHandlerFixture) register(t *testing.T, name, redirect string) string {
	t.Helper()
	body := `{"client_name":"` + name + `","redirect_uris":["` + redirect + `"],"token_endpoint_auth_method":"none"}`
	recorder := oauthHandlerRequest(f.web, http.MethodPost, oauthHandlerOrigin+"/oauth/register", strings.NewReader(body), "application/json")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST /oauth/register status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	clientID, _ := response["client_id"].(string)
	if !strings.HasPrefix(clientID, "rlc_") {
		t.Fatalf("client_id = %v, want rlc_ identifier", response["client_id"])
	}
	return clientID
}

func (f oauthHandlerFixture) authorizeQuery(resource string) url.Values {
	return url.Values{
		"client_id":             {f.clientID},
		"redirect_uri":          {oauthHandlerRedirect},
		"response_type":         {"code"},
		"state":                 {"state-1"},
		"code_challenge":        {oauthHandlerChallenge},
		"code_challenge_method": {"S256"},
		"resource":              {resource},
	}
}

func (f oauthHandlerFixture) decision(t *testing.T, resource, decision string) *httptest.ResponseRecorder {
	t.Helper()
	form := f.authorizeQuery(resource)
	form.Set("csrf_token", f.csrf.Value)
	form.Set("decision", decision)
	return oauthHandlerRequest(f.web, http.MethodPost, oauthHandlerOrigin+"/oauth/authorize", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", f.session, f.csrf)
}

func (f oauthHandlerFixture) exchange(t *testing.T, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return oauthHandlerRequest(f.web, http.MethodPost, oauthHandlerOrigin+"/oauth/token", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
}

func decodeOAuthJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode OAuth JSON %q: %v", body, err)
	}
	return response
}

func managementStatusWithBearer(f oauthHandlerFixture, token string) int {
	request := httptest.NewRequest(http.MethodPost, oauthHandlerOrigin+"/api/v1/management/list_applications", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.web.Routes().ServeHTTP(recorder, request)
	return recorder.Code
}

func TestOAuthMetadataIsPublicAndAdvertisesPKCEAndRegistration(t *testing.T) {
	fixture := newOAuthHandlerFixture(t)
	recorder := oauthHandlerRequest(fixture.web, http.MethodGet, oauthHandlerOrigin+"/.well-known/oauth-authorization-server", nil, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, want 200 without a session", recorder.Code)
	}
	metadata := decodeOAuthJSON(t, recorder.Body.String())
	if metadata["issuer"] != oauthHandlerOrigin || metadata["registration_endpoint"] != oauthHandlerOrigin+"/oauth/register" {
		t.Fatalf("metadata = %#v", metadata)
	}
	methods, _ := metadata["code_challenge_methods_supported"].([]any)
	if len(methods) != 1 || methods[0] != "S256" {
		t.Fatalf("code_challenge_methods_supported = %#v, want [S256]", methods)
	}

	remote := oauthHandlerRequest(fixture.web, http.MethodGet, "http://example.com/.well-known/oauth-authorization-server", nil, "")
	if remote.Code != http.StatusNotFound {
		t.Fatalf("metadata over plain HTTP on a public host status = %d, want 404", remote.Code)
	}
}

func TestOAuthRegisterRejectsUnsafeRedirectURIs(t *testing.T) {
	fixture := newOAuthHandlerFixture(t)
	recorder := oauthHandlerRequest(fixture.web, http.MethodPost, oauthHandlerOrigin+"/oauth/register", strings.NewReader(`{"redirect_uris":["http://example.com/callback"]}`), "application/json")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if response := decodeOAuthJSON(t, recorder.Body.String()); response["error"] != application.OAuthErrorInvalidRedirectURI {
		t.Fatalf("error = %v, want invalid_redirect_uri", response["error"])
	}

	malformed := oauthHandlerRequest(fixture.web, http.MethodPost, oauthHandlerOrigin+"/oauth/register", strings.NewReader("not json"), "application/json")
	if response := decodeOAuthJSON(t, malformed.Body.String()); malformed.Code != http.StatusBadRequest || response["error"] != application.OAuthErrorInvalidClientMetadata {
		t.Fatalf("malformed registration = %d %s", malformed.Code, malformed.Body.String())
	}
}

func TestOAuthAuthorizeRequiresSignInThenShowsConsent(t *testing.T) {
	fixture := newOAuthHandlerFixture(t)
	target := oauthHandlerOrigin + "/oauth/authorize?" + fixture.authorizeQuery(oauthHandlerOrigin+"/mcp/manage").Encode()

	anonymous := oauthHandlerRequest(fixture.web, http.MethodGet, target, nil, "")
	if anonymous.Code != http.StatusSeeOther || !strings.HasPrefix(anonymous.Header().Get("Location"), "/login?") {
		t.Fatalf("anonymous authorize = %d Location %q, want sign-in redirect", anonymous.Code, anonymous.Header().Get("Location"))
	}

	signedIn := oauthHandlerRequest(fixture.web, http.MethodGet, target, nil, "", fixture.session)
	if signedIn.Code != http.StatusOK {
		t.Fatalf("signed-in authorize status = %d: %s", signedIn.Code, signedIn.Body.String())
	}
	if signedIn.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("consent page is cacheable")
	}
	callback, _ := url.Parse(oauthHandlerRedirect)
	expectedPolicy := strings.Replace(defaultContentSecurityPolicy, "form-action 'self';", "form-action 'self' "+callback.Scheme+"://"+callback.Host+";", 1)
	if signedIn.Header().Get("Content-Security-Policy") != expectedPolicy {
		t.Fatal("consent policy must allow exactly the registered callback origin and retain other restrictions")
	}
	body := signedIn.Body.String()
	for _, expected := range []string{"Approve MCP connection", "Codex", "Manage this server", `name="decision" value="approve"`, `name="decision" value="deny"`, `name="code_challenge" value="` + oauthHandlerChallenge + `"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("consent page missing %q", expected)
		}
	}
}

func TestOAuthConsentPolicyAllowsOnlyCallbackOrigin(t *testing.T) {
	for _, tc := range []struct{ uri, source string }{
		{"http://127.0.0.1:19876/callback?state=not-in-policy", "http://127.0.0.1:19876"},
		{"http://localhost:54321/callback", "http://localhost:54321"},
		{"http://[::1]:54321/callback", "http://[::1]:54321"},
		{"https://assistant.example.com/oauth/callback?next=%3B", "https://assistant.example.com"},
	} {
		t.Run(tc.uri, func(t *testing.T) {
			policy, err := oauthConsentContentSecurityPolicy(tc.uri)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Replace(defaultContentSecurityPolicy, "form-action 'self';", "form-action 'self' "+tc.source+";", 1)
			if policy != want {
				t.Fatalf("policy = %q, want %q", policy, want)
			}
		})
	}
	for _, uri := range []string{
		"http://remote.example.com/callback",
		"javascript:alert(1)",
		"https://user@example.com/callback",
		"https://example.com/callback#fragment",
		"https://*.example.com/callback",
		"https://example.com;script-src/callback",
		"https://example.com\n/callback",
	} {
		if policy, err := oauthConsentContentSecurityPolicy(uri); err == nil || policy != "" {
			t.Fatalf("unsafe callback %q produced policy %q", uri, policy)
		}
	}
}

func TestOAuthCallbackPolicyIsScopedToValidatedConsent(t *testing.T) {
	fixture := newOAuthHandlerFixture(t)
	query := fixture.authorizeQuery(oauthHandlerOrigin + "/mcp/manage")
	query.Set("redirect_uri", "https://unregistered.example.com/callback")
	response := oauthHandlerRequest(fixture.web, http.MethodGet, oauthHandlerOrigin+"/oauth/authorize?"+query.Encode(), nil, "", fixture.session)
	if response.Code != http.StatusBadRequest || response.Header().Get("Content-Security-Policy") != defaultContentSecurityPolicy {
		t.Fatal("unregistered callback must not relax the form policy")
	}
	for _, path := range []string{"/login", "/settings"} {
		response := oauthHandlerRequest(fixture.web, http.MethodGet, oauthHandlerOrigin+path, nil, "", fixture.session)
		if response.Header().Get("Content-Security-Policy") != defaultContentSecurityPolicy {
			t.Fatalf("%s policy allows external form destinations", path)
		}
	}
}

func TestOAuthAuthorizeShowsErrorPageForUnsafeRequests(t *testing.T) {
	fixture := newOAuthHandlerFixture(t)
	noPKCE := fixture.authorizeQuery(oauthHandlerOrigin + "/mcp/manage")
	noPKCE.Del("code_challenge_method")
	recorder := oauthHandlerRequest(fixture.web, http.MethodGet, oauthHandlerOrigin+"/oauth/authorize?"+noPKCE.Encode(), nil, "", fixture.session)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "PKCE with an S256 code_challenge is required.") {
		t.Fatalf("request without PKCE = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Security-Policy") != defaultContentSecurityPolicy {
		t.Fatal("invalid authorization request relaxed the form policy")
	}

	otherOrigin := fixture.authorizeQuery("https://attacker.example.com/mcp/manage")
	recorder = oauthHandlerRequest(fixture.web, http.MethodGet, oauthHandlerOrigin+"/oauth/authorize?"+otherOrigin.Encode(), nil, "", fixture.session)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("foreign resource status = %d, want 400", recorder.Code)
	}

	publicHost := oauthHandlerRequest(fixture.web, http.MethodGet, "http://example.com/oauth/authorize?"+fixture.authorizeQuery(oauthHandlerOrigin+"/mcp/manage").Encode(), nil, "", fixture.session)
	if publicHost.Code != http.StatusBadRequest || !strings.Contains(publicHost.Body.String(), "does not serve") {
		t.Fatalf("authorize on an unconfigured public host = %d %s", publicHost.Code, publicHost.Body.String())
	}
}

func TestOAuthDecisionRequiresCSRFAndRedirectsWithCodeOrDenial(t *testing.T) {
	fixture := newOAuthHandlerFixture(t)
	resource := oauthHandlerOrigin + "/mcp/manage"

	form := fixture.authorizeQuery(resource)
	form.Set("decision", "approve")
	withoutCookie := oauthHandlerRequest(fixture.web, http.MethodPost, oauthHandlerOrigin+"/oauth/authorize", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", fixture.session)
	if withoutCookie.Code != http.StatusForbidden {
		t.Fatalf("decision without CSRF cookie status = %d, want 403", withoutCookie.Code)
	}

	form.Set("csrf_token", strings.Repeat("b", 64))
	wrongToken := oauthHandlerRequest(fixture.web, http.MethodPost, oauthHandlerOrigin+"/oauth/authorize", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", fixture.session, fixture.csrf)
	if wrongToken.Code != http.StatusForbidden {
		t.Fatalf("decision with mismatched CSRF token status = %d, want 403", wrongToken.Code)
	}

	approved := fixture.decision(t, resource, "approve")
	if approved.Code != http.StatusSeeOther {
		t.Fatalf("approval status = %d: %s", approved.Code, approved.Body.String())
	}
	location, err := url.Parse(approved.Header().Get("Location"))
	if err != nil || location.Scheme+"://"+location.Host+location.Path != oauthHandlerRedirect || location.Query().Get("state") != "state-1" || location.Query().Get("code") == "" {
		t.Fatalf("approval Location = %q, %v", approved.Header().Get("Location"), err)
	}

	denied := fixture.decision(t, resource, "deny")
	deniedLocation, _ := url.Parse(denied.Header().Get("Location"))
	if denied.Code != http.StatusSeeOther || deniedLocation.Query().Get("error") != application.OAuthErrorAccessDenied || deniedLocation.Query().Get("code") != "" {
		t.Fatalf("denial Location = %q", denied.Header().Get("Location"))
	}
}

func TestOAuthTokenFlowIssuesBearerTokensAcceptedByTheManagementAPI(t *testing.T) {
	fixture := newOAuthHandlerFixture(t)
	resource := oauthHandlerOrigin + "/mcp/manage"

	approved := fixture.decision(t, resource, "approve")
	location, _ := url.Parse(approved.Header().Get("Location"))
	code := location.Query().Get("code")

	wrong := fixture.exchange(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {fixture.clientID}, "code": {code}, "redirect_uri": {oauthHandlerRedirect}, "code_verifier": {strings.Repeat("A", 43)}, "resource": {resource}})
	if wrong.Code != http.StatusBadRequest || decodeOAuthJSON(t, wrong.Body.String())["error"] != application.OAuthErrorInvalidGrant {
		t.Fatalf("wrong verifier = %d %s", wrong.Code, wrong.Body.String())
	}
	if wrong.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token error response is cacheable")
	}

	approved = fixture.decision(t, resource, "approve")
	location, _ = url.Parse(approved.Header().Get("Location"))
	code = location.Query().Get("code")
	exchanged := fixture.exchange(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {fixture.clientID}, "code": {code}, "redirect_uri": {oauthHandlerRedirect}, "code_verifier": {oauthHandlerVerifier}, "resource": {resource}})
	if exchanged.Code != http.StatusOK {
		t.Fatalf("exchange status = %d: %s", exchanged.Code, exchanged.Body.String())
	}
	tokens := decodeOAuthJSON(t, exchanged.Body.String())
	access, _ := tokens["access_token"].(string)
	refresh, _ := tokens["refresh_token"].(string)
	if tokens["token_type"] != "Bearer" || tokens["expires_in"] != float64(3600) || tokens["scope"] != "manage" || !strings.HasPrefix(access, application.APITokenPrefix) || !strings.HasPrefix(refresh, application.OAuthRefreshTokenPrefix) {
		t.Fatalf("token response = %#v", tokens)
	}
	if status := managementStatusWithBearer(fixture, access); status != http.StatusOK {
		t.Fatalf("management API with OAuth access token status = %d, want 200", status)
	}

	refreshed := fixture.exchange(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {fixture.clientID}, "refresh_token": {refresh}})
	if refreshed.Code != http.StatusOK {
		t.Fatalf("refresh status = %d: %s", refreshed.Code, refreshed.Body.String())
	}
	next := decodeOAuthJSON(t, refreshed.Body.String())
	if managementStatusWithBearer(fixture, access) != http.StatusUnauthorized {
		t.Fatal("superseded access token still accepted by the management API")
	}
	nextAccess, _ := next["access_token"].(string)
	if managementStatusWithBearer(fixture, nextAccess) != http.StatusOK {
		t.Fatal("refreshed access token rejected by the management API")
	}
}

func TestOAuthRevokeEndsAccessThroughTheManagementAPI(t *testing.T) {
	fixture := newOAuthHandlerFixture(t)
	resource := oauthHandlerOrigin + "/mcp/manage"
	approved := fixture.decision(t, resource, "approve")
	location, _ := url.Parse(approved.Header().Get("Location"))
	exchanged := fixture.exchange(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {fixture.clientID}, "code": {location.Query().Get("code")}, "redirect_uri": {oauthHandlerRedirect}, "code_verifier": {oauthHandlerVerifier}, "resource": {resource}})
	tokens := decodeOAuthJSON(t, exchanged.Body.String())
	access, _ := tokens["access_token"].(string)
	refresh, _ := tokens["refresh_token"].(string)

	revoked := oauthHandlerRequest(fixture.web, http.MethodPost, oauthHandlerOrigin+"/oauth/revoke", strings.NewReader(url.Values{"token": {refresh}}.Encode()), "application/x-www-form-urlencoded")
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke status = %d: %s", revoked.Code, revoked.Body.String())
	}
	if status := managementStatusWithBearer(fixture, access); status != http.StatusUnauthorized {
		t.Fatalf("management API after revocation status = %d, want 401", status)
	}
	again := fixture.exchange(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {fixture.clientID}, "refresh_token": {refresh}})
	if again.Code != http.StatusBadRequest {
		t.Fatalf("refresh after revocation status = %d, want 400", again.Code)
	}
	missing := oauthHandlerRequest(fixture.web, http.MethodPost, oauthHandlerOrigin+"/oauth/revoke", strings.NewReader(""), "application/x-www-form-urlencoded")
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("revoke without token status = %d, want 400", missing.Code)
	}
}

func TestOAuthEndpointsAreAbsentWithoutAnAuthorizationServer(t *testing.T) {
	web, err := New(nil, &fakeApplicationService{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/oauth/register", "/oauth/token", "/oauth/revoke"} {
		recorder := oauthHandlerRequest(web, http.MethodPost, oauthHandlerOrigin+path, strings.NewReader(""), "application/json")
		if path == "/.well-known/oauth-authorization-server" {
			recorder = oauthHandlerRequest(web, http.MethodGet, oauthHandlerOrigin+path, nil, "")
		}
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s without an authorization server status = %d, want 404", path, recorder.Code)
		}
	}
}
