package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type fakeAuthorizedEmailStore struct {
	allowed map[string]bool
	err     error
}

func (s *fakeAuthorizedEmailStore) IsAuthorizedEmail(_ context.Context, email string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.allowed[email], nil
}

func newTestService(t *testing.T, store *fakeAuthorizedEmailStore, userInfo func(http.ResponseWriter)) *Service {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			if r.Form.Get("code") != "authorization-code" {
				t.Errorf("token code = %q, want authorization-code", r.Form.Get("code"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"access-token","token_type":"Bearer","expires_in":3600}`))
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer access-token" {
				t.Errorf("userinfo Authorization = %q, want bearer token", r.Header.Get("Authorization"))
			}
			userInfo(w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	service, err := New(Config{
		ClientID:        "client-id",
		ClientSecret:    "client-secret",
		RedirectURL:     "https://redlaunch.example.com/auth/google/callback",
		SessionSecret:   strings.Repeat("s", 32),
		SessionDuration: time.Hour,
		OAuthEndpoint: oauth2.Endpoint{
			AuthURL:   server.URL + "/authorize",
			TokenURL:  server.URL + "/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		UserInfoURL: server.URL + "/userinfo",
		HTTPClient:  server.Client(),
		Now:         func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestAuthorizationURLIncludesStateAndConfiguredValues(t *testing.T) {
	service := newTestService(t, &fakeAuthorizedEmailStore{allowed: map[string]bool{}}, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	parsed, err := url.Parse(service.AuthorizationURL("state-token"))
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("client_id") != "client-id" || query.Get("redirect_uri") != "https://redlaunch.example.com/auth/google/callback" || query.Get("state") != "state-token" {
		t.Fatalf("authorization query = %v", query)
	}
	if query.Get("response_type") != "code" || query.Get("scope") != "openid email profile" {
		t.Fatalf("authorization query = %v, want code and identity scopes", query)
	}
}

func TestAuthorizationURLSupportsPerRequestRedirectURL(t *testing.T) {
	service := newTestService(t, &fakeAuthorizedEmailStore{allowed: map[string]bool{}}, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	const publicRedirectURL = "https://public.redlaunch.example/auth/google/callback"
	parsed, err := url.Parse(service.AuthorizationURLForRedirect("state-token", publicRedirectURL))
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != publicRedirectURL {
		t.Fatalf("per-request redirect_uri = %q, want %q", got, publicRedirectURL)
	}

	configured, err := url.Parse(service.AuthorizationURL("state-token"))
	if err != nil {
		t.Fatal(err)
	}
	if got := configured.Query().Get("redirect_uri"); got != "https://redlaunch.example.com/auth/google/callback" {
		t.Fatalf("configured redirect_uri after override = %q, want configured URL", got)
	}
}

func TestCompleteLoginSupportsPerRequestRedirectURL(t *testing.T) {
	var gotRedirectURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			gotRedirectURL = r.Form.Get("redirect_uri")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"access-token","token_type":"Bearer","expires_in":3600}`))
		case "/userinfo":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"email":"admin@example.com","email_verified":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	service, err := New(Config{
		ClientID:      "client-id",
		ClientSecret:  "client-secret",
		RedirectURL:   "https://redlaunch.example.com/auth/google/callback",
		SessionSecret: strings.Repeat("s", 32),
		OAuthEndpoint: oauth2.Endpoint{
			AuthURL:   server.URL + "/authorize",
			TokenURL:  server.URL + "/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		UserInfoURL: server.URL + "/userinfo",
		HTTPClient:  server.Client(),
	}, &fakeAuthorizedEmailStore{allowed: map[string]bool{"admin@example.com": true}})
	if err != nil {
		t.Fatal(err)
	}

	const publicRedirectURL = "https://public.redlaunch.example/auth/google/callback"
	if _, err := service.CompleteLoginForRedirect(context.Background(), "authorization-code", publicRedirectURL); err != nil {
		t.Fatal(err)
	}
	if gotRedirectURL != publicRedirectURL {
		t.Fatalf("token redirect_uri = %q, want %q", gotRedirectURL, publicRedirectURL)
	}
}

func TestCompleteLoginUsesVerifiedEmailAndAllowlist(t *testing.T) {
	store := &fakeAuthorizedEmailStore{allowed: map[string]bool{"admin@example.com": true}}
	service := newTestService(t, store, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"email":          "Admin@Example.COM",
			"email_verified": true,
			"name":           "Ada Lovelace",
			"picture":        "https://lh3.googleusercontent.com/a/avatar",
		})
	})

	user, err := service.CompleteLogin(context.Background(), "authorization-code")
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "admin@example.com" || user.Name != "Ada Lovelace" || user.PictureURL != "https://lh3.googleusercontent.com/a/avatar" {
		t.Fatalf("CompleteLogin() user = %#v, want normalized profile", user)
	}
}

func TestCompleteLoginRejectsUnverifiedOrUnauthorizedEmail(t *testing.T) {
	tests := []struct {
		name     string
		verified bool
		allowed  bool
		wantErr  error
	}{
		{name: "unverified", verified: false, allowed: true, wantErr: ErrEmailNotVerified},
		{name: "not allowed", verified: true, allowed: false, wantErr: ErrNotAuthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newTestService(t, &fakeAuthorizedEmailStore{allowed: map[string]bool{"admin@example.com": tt.allowed}}, func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"email":          "admin@example.com",
					"email_verified": tt.verified,
				})
			})
			if _, err := service.CompleteLogin(context.Background(), "authorization-code"); !errors.Is(err, tt.wantErr) {
				t.Fatalf("CompleteLogin() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestSessionIsSignedExpiringAndRevocationAware(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	store := &fakeAuthorizedEmailStore{allowed: map[string]bool{"admin@example.com": true}}
	service := newTestService(t, store, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	service.now = func() time.Time { return now }

	session, err := service.NewSession(User{Email: "Admin@Example.COM", Name: "Ada Lovelace", PictureURL: "https://lh3.googleusercontent.com/a/avatar"})
	if err != nil {
		t.Fatal(err)
	}
	user, valid, err := service.ValidateSession(context.Background(), session)
	if err != nil || !valid || user.Email != "admin@example.com" || user.Name != "Ada Lovelace" || user.PictureURL != "https://lh3.googleusercontent.com/a/avatar" {
		t.Fatalf("ValidateSession() = (%#v, %t, %v), want authorized session", user, valid, err)
	}

	tampered := session[:len(session)-1] + "x"
	if _, valid, err := service.ValidateSession(context.Background(), tampered); err != nil || valid {
		t.Fatalf("ValidateSession(tampered) = (%t, %v), want false, nil", valid, err)
	}
	store.allowed["admin@example.com"] = false
	if _, valid, err := service.ValidateSession(context.Background(), session); err != nil || valid {
		t.Fatalf("ValidateSession(revoked) = (%t, %v), want false, nil", valid, err)
	}
	store.allowed["admin@example.com"] = true
	service.now = func() time.Time { return now.Add(time.Hour) }
	if _, valid, err := service.ValidateSession(context.Background(), session); err != nil || valid {
		t.Fatalf("ValidateSession(expired) = (%t, %v), want false, nil", valid, err)
	}
}

func TestValidateSessionAcceptsLegacyEmailOnlySession(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	store := &fakeAuthorizedEmailStore{allowed: map[string]bool{"admin@example.com": true}}
	service := newTestService(t, store, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	service.now = func() time.Time { return now }

	payload := strings.Join([]string{
		"v1",
		"admin@example.com",
		strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		"legacy-nonce",
	}, "|")
	session := encodeSignedValue(payload, service.sessionSecret)

	user, valid, err := service.ValidateSession(context.Background(), session)
	if err != nil || !valid || user.Email != "admin@example.com" || user.Name != "Admin" || user.PictureURL != "" {
		t.Fatalf("ValidateSession(legacy) = (%#v, %t, %v), want email-only user", user, valid, err)
	}
}

func TestNewRequiresStrongSessionSecret(t *testing.T) {
	_, err := New(Config{
		ClientID:      "client-id",
		ClientSecret:  "client-secret",
		RedirectURL:   "https://redlaunch.example.com/auth/google/callback",
		SessionSecret: "short",
	}, &fakeAuthorizedEmailStore{})
	if err == nil {
		t.Fatal("New() returned nil error for a short session secret")
	}
}
