package application

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateOAuthRedirectURI(t *testing.T) {
	for _, value := range []string{
		"https://client.example/callback",
		"http://127.0.0.1:53000/callback",
		"http://localhost/callback",
		"http://[::1]:8000/callback",
	} {
		if _, err := ValidateOAuthRedirectURI(value); err != nil {
			t.Fatalf("ValidateOAuthRedirectURI(%q) error = %v, want accepted", value, err)
		}
	}
	for _, value := range []string{
		"",
		"http://example.com/callback",
		"myapp://callback",
		"javascript:alert(1)",
		"https://client.example/callback#fragment",
		"https://user@client.example/callback",
		"https:///callback",
	} {
		if _, err := ValidateOAuthRedirectURI(value); err == nil {
			t.Fatalf("ValidateOAuthRedirectURI(%q) accepted, want rejected", value)
		}
	}
}

func TestOAuthRedirectURIMatchesIgnoresLoopbackPortOnly(t *testing.T) {
	registered := []string{"http://127.0.0.1/callback", "https://client.example/callback"}
	for _, requested := range []string{
		"http://127.0.0.1:61234/callback",
		"http://127.0.0.1/callback",
		"https://client.example/callback",
	} {
		if !OAuthRedirectURIMatches(registered, requested) {
			t.Fatalf("OAuthRedirectURIMatches(%q) = false, want true", requested)
		}
	}
	for _, requested := range []string{
		"http://localhost:61234/callback",
		"http://127.0.0.1:61234/other",
		"http://127.0.0.1:61234/callback?extra=1",
		"http://127.0.0.1:61234/callback#fragment",
		"https://client.example/other",
		"https://client.example:8443/callback",
		"https://attacker.example/callback",
	} {
		if OAuthRedirectURIMatches(registered, requested) {
			t.Fatalf("OAuthRedirectURIMatches(%q) = true, want false", requested)
		}
	}
}

func TestParseOAuthResourceAcceptsOnlyThisServersEndpoints(t *testing.T) {
	const origin = "https://vps.example.com"

	manage, err := ParseOAuthResource(origin, origin+"/mcp/manage")
	if err != nil || manage.Scope != APITokenScopeManage || manage.ApplicationID != 0 {
		t.Fatalf("manage resource = %#v, %v", manage, err)
	}
	run, err := ParseOAuthResource(origin, origin+"/mcp/apps/7")
	if err != nil || run.Scope != APITokenScopeRun || run.ApplicationID != 7 {
		t.Fatalf("run resource = %#v, %v", run, err)
	}
	for _, value := range []string{
		"",
		"/mcp/manage",
		"https://other.example.com/mcp/manage",
		"https://vps.example.com/mcp/manage/",
		"https://vps.example.com/mcp/manage?x=1",
		"https://vps.example.com/mcp/apps/07",
		"https://vps.example.com/mcp/apps/0",
		"https://vps.example.com/mcp/apps/-1",
		"https://vps.example.com/mcp/apps/seven",
		"https://vps.example.com/mcp/apps/",
		"https://vps.example.com/mcp",
	} {
		if _, err := ParseOAuthResource(origin, value); err == nil {
			t.Fatalf("ParseOAuthResource(%q) accepted, want rejected", value)
		}
	}
}

func TestValidateOAuthScopeMatchesResource(t *testing.T) {
	resource := OAuthResource{Scope: APITokenScopeRun, ApplicationID: 7}
	for _, scope := range []string{"", "run", " run "} {
		if err := ValidateOAuthScope(scope, resource); err != nil {
			t.Fatalf("ValidateOAuthScope(%q) error = %v, want accepted", scope, err)
		}
	}
	for _, scope := range []string{"manage", "run manage", "run:status"} {
		err := ValidateOAuthScope(scope, resource)
		var protocolError *OAuthError
		if !errors.As(err, &protocolError) || protocolError.Code != OAuthErrorInvalidScope {
			t.Fatalf("ValidateOAuthScope(%q) error = %v, want invalid_scope", scope, err)
		}
	}
}

func TestVerifyOAuthPKCEUsesRFC7636Vector(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if !VerifyOAuthPKCE(verifier, challenge) {
		t.Fatal("RFC 7636 appendix B vector rejected")
	}
	if VerifyOAuthPKCE(verifier+"x", challenge) {
		t.Fatal("altered verifier accepted")
	}
	if VerifyOAuthPKCE("short", challenge) {
		t.Fatal("verifier shorter than 43 characters accepted")
	}
	if VerifyOAuthPKCE(strings.Repeat("a", 42)+"+", challenge) {
		t.Fatal("verifier with reserved characters accepted")
	}
	if ValidOAuthCodeChallenge(challenge[:42]) {
		t.Fatal("short challenge accepted")
	}
}

func TestSplitOAuthRefreshTokenRejectsAPITokens(t *testing.T) {
	secret := strings.Repeat("ab", APITokenSecretBytes)
	refresh := FormatOAuthRefreshToken(secret)
	if _, ok := SplitOAuthRefreshToken(refresh); !ok {
		t.Fatalf("SplitOAuthRefreshToken(%q) rejected a well-formed refresh token", refresh)
	}
	if _, ok := SplitOAuthRefreshToken(FormatAPIToken(secret)); ok {
		t.Fatal("API token accepted as a refresh token")
	}
	if _, ok := SplitOAuthRefreshToken(OAuthRefreshTokenPrefix + "not-hex"); ok {
		t.Fatal("malformed refresh token accepted")
	}
}

func TestValidateOAuthClientName(t *testing.T) {
	name, err := ValidateOAuthClientName("   ")
	if err != nil || name != "MCP client" {
		t.Fatalf("empty client name = %q, %v; want generic label", name, err)
	}
	if _, err := ValidateOAuthClientName(strings.Repeat("n", MaxOAuthClientNameLength+1)); err == nil {
		t.Fatal("overlong client name accepted")
	}
	if _, err := ValidateOAuthClientName("Assistant\nforged line"); err == nil {
		t.Fatal("client name with a control character accepted")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"localhost", "LOCALHOST", "127.0.0.1", "::1"} {
		if !IsLoopbackHost(host) {
			t.Fatalf("IsLoopbackHost(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"example.com", "192.168.1.10", "localhost.example.com", ""} {
		if IsLoopbackHost(host) {
			t.Fatalf("IsLoopbackHost(%q) = true, want false", host)
		}
	}
}
