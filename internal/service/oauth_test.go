package service

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/store"
)

const (
	oauthTestOrigin    = "https://vps.example.com"
	oauthTestManage    = oauthTestOrigin + "/mcp/manage"
	oauthTestRedir     = "http://127.0.0.1/callback"
	oauthTestVerify    = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	oauthTestChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

type oauthTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *oauthTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *oauthTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newOAuthServiceForTest uses a real SQLite store so compare-and-swap and
// cascade behavior are exercised, plus a controllable clock for expiry rules.
func newOAuthServiceForTest(t *testing.T) (*OAuthService, *store.Store, *oauthTestClock, int64) {
	t.Helper()
	database, err := store.Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	item, err := database.Create(t.Context(), application.Application{Name: "Talent hunt", FolderName: "talenthunt"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewOAuthService(database, database)
	if err != nil {
		t.Fatal(err)
	}
	// Anchored to real time because APITokenService.Authenticate checks expiry
	// against the wall clock.
	clock := &oauthTestClock{now: time.Now().UTC().Truncate(time.Second)}
	service.now = clock.Now
	return service, database, clock, item.ID
}

func oauthErrorCode(t *testing.T, err error) string {
	t.Helper()
	var protocolError *application.OAuthError
	if !errors.As(err, &protocolError) {
		t.Fatalf("error = %v, want an OAuth protocol error", err)
	}
	return protocolError.Code
}

func registerOAuthClientForTest(t *testing.T, service *OAuthService) application.OAuthClient {
	t.Helper()
	client, err := service.RegisterClient(t.Context(), application.OAuthClientRegistration{
		ClientName:   "Codex",
		RedirectURIs: []string{oauthTestRedir},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func oauthAuthorizationForTest(clientID, resource string) application.OAuthAuthorizationRequest {
	return application.OAuthAuthorizationRequest{
		ClientID:            clientID,
		RedirectURI:         oauthTestRedir,
		ResponseType:        "code",
		State:               "state-1",
		CodeChallenge:       oauthTestChallenge,
		CodeChallengeMethod: "S256",
		Resource:            resource,
	}
}

func approveOAuthForTest(t *testing.T, service *OAuthService, request application.OAuthAuthorizationRequest) string {
	t.Helper()
	location, err := service.CompleteAuthorization(t.Context(), oauthTestOrigin, request, true)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(location)
	if err != nil || parsed.Query().Get("state") != "state-1" {
		t.Fatalf("approval redirect = %q, %v; want state preserved", location, err)
	}
	code := parsed.Query().Get("code")
	if code == "" {
		t.Fatalf("approval redirect %q has no code", location)
	}
	return code
}

func exchangeOAuthCodeForTest(service *OAuthService, clientID, code, verifier, resource string) (application.OAuthTokenResponse, error) {
	return service.ExchangeToken(context.Background(), application.OAuthTokenRequest{
		GrantType:    application.OAuthGrantTypeAuthorizationCode,
		ClientID:     clientID,
		Code:         code,
		RedirectURI:  oauthTestRedir,
		CodeVerifier: verifier,
		Resource:     resource,
	})
}

func issueOAuthTokensForTest(t *testing.T, service *OAuthService, clientID, resource string) application.OAuthTokenResponse {
	t.Helper()
	code := approveOAuthForTest(t, service, oauthAuthorizationForTest(clientID, resource))
	tokens, err := exchangeOAuthCodeForTest(service, clientID, code, oauthTestVerify, resource)
	if err != nil {
		t.Fatal(err)
	}
	return tokens
}

func refreshOAuthForTest(service *OAuthService, clientID, refreshToken string) (application.OAuthTokenResponse, error) {
	return service.ExchangeToken(context.Background(), application.OAuthTokenRequest{
		GrantType:    application.OAuthGrantTypeRefreshToken,
		ClientID:     clientID,
		RefreshToken: refreshToken,
	})
}

func oauthAPITokensForTest(t *testing.T, database *store.Store) *APITokenService {
	t.Helper()
	apiTokens, err := NewAPITokenService(database, database)
	if err != nil {
		t.Fatal(err)
	}
	return apiTokens
}

func TestOAuthRegisterClientValidatesMetadata(t *testing.T) {
	service, _, _, _ := newOAuthServiceForTest(t)
	tooMany := make([]string, application.MaxOAuthRedirectURIs+1)
	for index := range tooMany {
		tooMany[index] = oauthTestRedir
	}
	for _, testCase := range []struct {
		name         string
		registration application.OAuthClientRegistration
		code         string
	}{
		{"no redirect URI", application.OAuthClientRegistration{}, application.OAuthErrorInvalidRedirectURI},
		{"plain HTTP host", application.OAuthClientRegistration{RedirectURIs: []string{"http://example.com/callback"}}, application.OAuthErrorInvalidRedirectURI},
		{"too many redirect URIs", application.OAuthClientRegistration{RedirectURIs: tooMany}, application.OAuthErrorInvalidRedirectURI},
		{"unsupported grant", application.OAuthClientRegistration{RedirectURIs: []string{oauthTestRedir}, GrantTypes: []string{"client_credentials"}}, application.OAuthErrorInvalidClientMetadata},
		{"implicit response", application.OAuthClientRegistration{RedirectURIs: []string{oauthTestRedir}, ResponseTypes: []string{"token"}}, application.OAuthErrorInvalidClientMetadata},
		{"client secret auth", application.OAuthClientRegistration{RedirectURIs: []string{oauthTestRedir}, TokenEndpointAuthMethod: "client_secret_basic"}, application.OAuthErrorInvalidClientMetadata},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.RegisterClient(t.Context(), testCase.registration)
			if code := oauthErrorCode(t, err); code != testCase.code {
				t.Fatalf("RegisterClient() code = %q, want %q", code, testCase.code)
			}
		})
	}

	client := registerOAuthClientForTest(t, service)
	if !strings.HasPrefix(client.ID, "rlc_") || client.Name != "Codex" {
		t.Fatalf("registered client = %#v, want rlc_ identifier and display name", client)
	}
}

func TestOAuthRegistrationPrunesUnusedClientsAndCapsTheTotal(t *testing.T) {
	service, database, clock, _ := newOAuthServiceForTest(t)
	stale := registerOAuthClientForTest(t, service)
	clock.Advance(25 * time.Hour)
	registerOAuthClientForTest(t, service)
	if _, err := database.GetOAuthClient(t.Context(), stale.ID); !errors.Is(err, application.ErrOAuthClientNotFound) {
		t.Fatalf("unused client survived pruning: %v", err)
	}

	for {
		count, err := database.CountOAuthClients(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if count >= oauthMaxRegisteredClients {
			break
		}
		registerOAuthClientForTest(t, service)
	}
	_, err := service.RegisterClient(t.Context(), application.OAuthClientRegistration{RedirectURIs: []string{oauthTestRedir}})
	if code := oauthErrorCode(t, err); code != application.OAuthErrorTemporarilyUnavailable {
		t.Fatalf("registration beyond cap code = %q, want temporarily_unavailable", code)
	}
}

func TestOAuthAuthorizationRejectsUnsafeOrUnknownRequests(t *testing.T) {
	service, _, _, applicationID := newOAuthServiceForTest(t)
	client := registerOAuthClientForTest(t, service)
	valid := oauthAuthorizationForTest(client.ID, oauthTestManage)

	for _, testCase := range []struct {
		name   string
		mutate func(*application.OAuthAuthorizationRequest)
		origin string
		code   string
	}{
		{"unknown client", func(r *application.OAuthAuthorizationRequest) { r.ClientID = "rlc_unknown" }, oauthTestOrigin, application.OAuthErrorInvalidClient},
		{"unregistered redirect", func(r *application.OAuthAuthorizationRequest) { r.RedirectURI = "http://127.0.0.1/other" }, oauthTestOrigin, application.OAuthErrorInvalidRedirectURI},
		{"implicit flow", func(r *application.OAuthAuthorizationRequest) { r.ResponseType = "token" }, oauthTestOrigin, application.OAuthErrorUnsupportedResponseType},
		{"missing PKCE", func(r *application.OAuthAuthorizationRequest) { r.CodeChallengeMethod = "" }, oauthTestOrigin, application.OAuthErrorInvalidRequest},
		{"plain PKCE", func(r *application.OAuthAuthorizationRequest) { r.CodeChallengeMethod = "plain" }, oauthTestOrigin, application.OAuthErrorInvalidRequest},
		{"resource from another origin", func(r *application.OAuthAuthorizationRequest) { r.Resource = "https://attacker.example.com/mcp/manage" }, oauthTestOrigin, application.OAuthErrorInvalidTarget},
		{"missing resource", func(r *application.OAuthAuthorizationRequest) { r.Resource = "" }, oauthTestOrigin, application.OAuthErrorInvalidTarget},
		{"unknown application", func(r *application.OAuthAuthorizationRequest) { r.Resource = oauthTestOrigin + "/mcp/apps/999" }, oauthTestOrigin, application.OAuthErrorInvalidTarget},
		{"scope for another resource", func(r *application.OAuthAuthorizationRequest) { r.Scope = "run" }, oauthTestOrigin, application.OAuthErrorInvalidScope},
		{"different origin", func(r *application.OAuthAuthorizationRequest) {}, "https://other.example.com", application.OAuthErrorInvalidTarget},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := valid
			testCase.mutate(&request)
			_, err := service.ValidateAuthorization(t.Context(), testCase.origin, request)
			if code := oauthErrorCode(t, err); code != testCase.code {
				t.Fatalf("ValidateAuthorization() code = %q, want %q", code, testCase.code)
			}
		})
	}

	authorization, err := service.ValidateAuthorization(t.Context(), oauthTestOrigin, valid)
	if err != nil || authorization.Resource.Scope != application.APITokenScopeManage || authorization.ApplicationName != "" {
		t.Fatalf("valid management request = %#v, %v", authorization, err)
	}
	runRequest := valid
	runRequest.Resource = oauthTestOrigin + "/mcp/apps/" + strconv.FormatInt(applicationID, 10)
	runAuthorization, err := service.ValidateAuthorization(t.Context(), oauthTestOrigin, runRequest)
	if err != nil || runAuthorization.Resource.Scope != application.APITokenScopeRun || runAuthorization.ApplicationName != "Talent hunt" {
		t.Fatalf("valid application request = %#v, %v", runAuthorization, err)
	}
}

func TestOAuthDenialRedirectsWithAccessDenied(t *testing.T) {
	service, _, _, _ := newOAuthServiceForTest(t)
	client := registerOAuthClientForTest(t, service)
	location, err := service.CompleteAuthorization(t.Context(), oauthTestOrigin, oauthAuthorizationForTest(client.ID, oauthTestManage), false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(location)
	if parsed.Query().Get("error") != application.OAuthErrorAccessDenied || parsed.Query().Get("state") != "state-1" || parsed.Query().Get("code") != "" {
		t.Fatalf("denial redirect = %q, want access_denied with state and no code", location)
	}
}

func TestOAuthCodeExchangeIsSingleUseAndBoundToPKCEClientAndResource(t *testing.T) {
	service, database, clock, _ := newOAuthServiceForTest(t)
	client := registerOAuthClientForTest(t, service)

	code := approveOAuthForTest(t, service, oauthAuthorizationForTest(client.ID, oauthTestManage))
	wrongVerifier := strings.Repeat("A", 43)
	if _, err := exchangeOAuthCodeForTest(service, client.ID, code, wrongVerifier, oauthTestManage); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatalf("wrong PKCE verifier error = %v, want invalid_grant", err)
	}
	if _, err := exchangeOAuthCodeForTest(service, client.ID, code, oauthTestVerify, oauthTestManage); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatal("authorization code survived a failed exchange")
	}

	code = approveOAuthForTest(t, service, oauthAuthorizationForTest(client.ID, oauthTestManage))
	if _, err := exchangeOAuthCodeForTest(service, client.ID, code, oauthTestVerify, oauthTestOrigin+"/mcp/apps/1"); oauthErrorCode(t, err) != application.OAuthErrorInvalidTarget {
		t.Fatal("token request for a different resource was accepted")
	}

	code = approveOAuthForTest(t, service, oauthAuthorizationForTest(client.ID, oauthTestManage))
	clock.Advance(application.OAuthAuthorizationCodeLifetime)
	if _, err := exchangeOAuthCodeForTest(service, client.ID, code, oauthTestVerify, oauthTestManage); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatal("expired authorization code accepted")
	}

	code = approveOAuthForTest(t, service, oauthAuthorizationForTest(client.ID, oauthTestManage))
	tokens, err := exchangeOAuthCodeForTest(service, client.ID, code, oauthTestVerify, oauthTestManage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exchangeOAuthCodeForTest(service, client.ID, code, oauthTestVerify, oauthTestManage); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatal("authorization code redeemed twice")
	}
	if tokens.TokenType != "Bearer" || tokens.ExpiresIn != 3600 || tokens.Scope != application.APITokenScopeManage || !strings.HasPrefix(tokens.AccessToken, application.APITokenPrefix) || !strings.HasPrefix(tokens.RefreshToken, application.OAuthRefreshTokenPrefix) {
		t.Fatalf("token response = %#v", tokens)
	}

	apiTokens := oauthAPITokensForTest(t, database)
	access, err := apiTokens.Authenticate(t.Context(), tokens.AccessToken)
	if err != nil || access.Scope != application.APITokenScopeManage || access.ApplicationID != 0 || access.DisplayName != "Codex" {
		t.Fatalf("access token as API token = %#v, %v", access, err)
	}
	if _, err := apiTokens.Authenticate(t.Context(), tokens.RefreshToken); err == nil {
		t.Fatal("refresh token accepted as an API token")
	}
}

func TestOAuthRunTokenIsPinnedToItsApplication(t *testing.T) {
	service, database, _, applicationID := newOAuthServiceForTest(t)
	client := registerOAuthClientForTest(t, service)
	resource := oauthTestOrigin + "/mcp/apps/" + strconv.FormatInt(applicationID, 10)
	tokens := issueOAuthTokensForTest(t, service, client.ID, resource)
	if tokens.Scope != application.APITokenScopeRun {
		t.Fatalf("run token scope = %q", tokens.Scope)
	}
	access, err := oauthAPITokensForTest(t, database).Authenticate(t.Context(), tokens.AccessToken)
	if err != nil || access.Scope != application.APITokenScopeRun || access.ApplicationID != applicationID {
		t.Fatalf("run access token = %#v, %v; want pinned to application %d", access, err, applicationID)
	}
}

func TestOAuthRefreshRotatesTokensAndRetiresTheOldAccessToken(t *testing.T) {
	service, database, _, _ := newOAuthServiceForTest(t)
	client := registerOAuthClientForTest(t, service)
	first := issueOAuthTokensForTest(t, service, client.ID, oauthTestManage)

	second, err := refreshOAuthForTest(service, client.ID, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken == first.RefreshToken || second.AccessToken == first.AccessToken {
		t.Fatal("refresh returned the same tokens; rotation is required for public clients")
	}
	apiTokens := oauthAPITokensForTest(t, database)
	if _, err := apiTokens.Authenticate(t.Context(), first.AccessToken); err == nil {
		t.Fatal("superseded access token still authenticates")
	}
	if _, err := apiTokens.Authenticate(t.Context(), second.AccessToken); err != nil {
		t.Fatalf("replacement access token rejected: %v", err)
	}
	if _, err := refreshOAuthForTest(service, client.ID, second.RefreshToken); err != nil {
		t.Fatalf("rotated refresh token rejected: %v", err)
	}
}

func TestOAuthRefreshGraceAllowsLostResponsesButReuseAfterwardRevokesTheGrant(t *testing.T) {
	service, database, clock, _ := newOAuthServiceForTest(t)
	client := registerOAuthClientForTest(t, service)
	first := issueOAuthTokensForTest(t, service, client.ID, oauthTestManage)
	second, err := refreshOAuthForTest(service, client.ID, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}

	// The response carrying second may have been lost. Retrying with the
	// superseded token inside the grace period returns the same pair.
	clock.Advance(10 * time.Second)
	replayed, err := refreshOAuthForTest(service, client.ID, first.RefreshToken)
	if err != nil || replayed != second {
		t.Fatalf("superseded token inside grace = %#v, %v; want the pair from its rotation", replayed, err)
	}

	// Presenting the superseded token after the grace period is reuse, so the grant ends.
	clock.Advance(application.OAuthRefreshGracePeriod)
	if _, err := refreshOAuthForTest(service, client.ID, first.RefreshToken); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatalf("reuse after grace error = %v, want invalid_grant", err)
	}
	if _, err := refreshOAuthForTest(service, client.ID, second.RefreshToken); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatal("grant survived reuse of a superseded refresh token")
	}
	if _, err := oauthAPITokensForTest(t, database).Authenticate(t.Context(), second.AccessToken); err == nil {
		t.Fatal("access token of a revoked grant still authenticates")
	}
}

func TestOAuthReplayIsMemoryOnlyAndDoesNotSurviveRestart(t *testing.T) {
	service, database, clock, _ := newOAuthServiceForTest(t)
	client := registerOAuthClientForTest(t, service)
	first := issueOAuthTokensForTest(t, service, client.ID, oauthTestManage)
	if _, err := refreshOAuthForTest(service, client.ID, first.RefreshToken); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewOAuthService(database, database)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = clock.Now
	clock.Advance(5 * time.Second)
	if _, err := refreshOAuthForTest(restarted, client.ID, first.RefreshToken); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatalf("superseded token after restart error = %v, want invalid_grant", err)
	}
}

func TestOAuthReusingAnOlderSupersededTokenRevokesTheGrant(t *testing.T) {
	service, database, _, _ := newOAuthServiceForTest(t)
	client := registerOAuthClientForTest(t, service)
	first := issueOAuthTokensForTest(t, service, client.ID, oauthTestManage)
	second, err := refreshOAuthForTest(service, client.ID, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	third, err := refreshOAuthForTest(service, client.ID, second.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}

	// first was rotated out two rotations ago. It is not the most recent
	// rotation, so it gets no replay even inside the grace period: it was copied.
	if _, err := refreshOAuthForTest(service, client.ID, first.RefreshToken); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatalf("older superseded token error = %v, want invalid_grant", err)
	}
	if _, err := refreshOAuthForTest(service, client.ID, third.RefreshToken); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatal("grant survived reuse of an older refresh token")
	}
	if _, err := oauthAPITokensForTest(t, database).Authenticate(t.Context(), third.AccessToken); err == nil {
		t.Fatal("access token of a revoked grant still authenticates")
	}
}

func TestOAuthParallelRefreshesConvergeOnOneRotation(t *testing.T) {
	service, _, _, _ := newOAuthServiceForTest(t)
	client := registerOAuthClientForTest(t, service)
	tokens := issueOAuthTokensForTest(t, service, client.ID, oauthTestManage)

	const attempts = 4
	var wait sync.WaitGroup
	type outcome struct {
		tokens application.OAuthTokenResponse
		err    error
	}
	results := make(chan outcome, attempts)
	for range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, err := refreshOAuthForTest(service, client.ID, tokens.RefreshToken)
			results <- outcome{response, err}
		}()
	}
	wait.Wait()
	close(results)
	var first application.OAuthTokenResponse
	for result := range results {
		if result.err != nil {
			t.Fatalf("parallel refresh failed: %v", result.err)
		}
		if first.RefreshToken == "" {
			first = result.tokens
		}
		if result.tokens != first {
			t.Fatal("parallel refreshes produced different token pairs; the client would fork its state")
		}
	}
}

func TestOAuthRefreshExpiresWhenIdleOrAbsolute(t *testing.T) {
	service, _, clock, _ := newOAuthServiceForTest(t)
	client := registerOAuthClientForTest(t, service)

	idle := issueOAuthTokensForTest(t, service, client.ID, oauthTestManage)
	clock.Advance(application.OAuthRefreshIdleLifetime + time.Hour)
	if _, err := refreshOAuthForTest(service, client.ID, idle.RefreshToken); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatalf("idle refresh token error = %v, want invalid_grant", err)
	}

	// Regular use keeps the idle timer alive, but the absolute lifetime still ends the grant.
	absolute := issueOAuthTokensForTest(t, service, client.ID, oauthTestManage)
	current := absolute.RefreshToken
	for step := range 4 {
		clock.Advance(20 * 24 * time.Hour)
		tokens, err := refreshOAuthForTest(service, client.ID, current)
		if err != nil {
			t.Fatalf("refresh %d within lifetime failed: %v", step, err)
		}
		current = tokens.RefreshToken
	}
	clock.Advance(20 * 24 * time.Hour)
	if _, err := refreshOAuthForTest(service, client.ID, current); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatal("refresh token outlived its absolute lifetime")
	}
}

func TestOAuthRevocationEndsTheGrantForRefreshAndAccessTokens(t *testing.T) {
	service, database, _, _ := newOAuthServiceForTest(t)
	client := registerOAuthClientForTest(t, service)
	first := issueOAuthTokensForTest(t, service, client.ID, oauthTestManage)
	if err := service.RevokeToken(t.Context(), first.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := refreshOAuthForTest(service, client.ID, first.RefreshToken); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatal("refresh token survived revocation")
	}
	if _, err := oauthAPITokensForTest(t, database).Authenticate(t.Context(), first.AccessToken); err == nil {
		t.Fatal("access token survived revocation by refresh token")
	}

	second := issueOAuthTokensForTest(t, service, client.ID, oauthTestManage)
	if err := service.RevokeToken(t.Context(), second.AccessToken); err != nil {
		t.Fatal(err)
	}
	if _, err := refreshOAuthForTest(service, client.ID, second.RefreshToken); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatal("refresh token survived revocation by access token")
	}

	if err := service.RevokeToken(t.Context(), "rlr_"+strings.Repeat("0", 64)); err != nil {
		t.Fatalf("revoking an unknown token = %v, want success", err)
	}
}

func TestOAuthRefreshRejectsOtherClientsAndAccessTokens(t *testing.T) {
	service, _, _, _ := newOAuthServiceForTest(t)
	client := registerOAuthClientForTest(t, service)
	other := registerOAuthClientForTest(t, service)
	tokens := issueOAuthTokensForTest(t, service, client.ID, oauthTestManage)

	if _, err := refreshOAuthForTest(service, other.ID, tokens.RefreshToken); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatal("another client refreshed the grant")
	}
	if _, err := refreshOAuthForTest(service, client.ID, tokens.AccessToken); oauthErrorCode(t, err) != application.OAuthErrorInvalidGrant {
		t.Fatal("access token accepted at the refresh grant")
	}
	if _, err := refreshOAuthForTest(service, client.ID, tokens.RefreshToken); err != nil {
		t.Fatalf("legitimate refresh rejected after misuse attempts: %v", err)
	}
}
