package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	"redlaunch/internal/store"
)

func TestGoogleUsesSharedUsersAndRevokesSessions(t *testing.T) {
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	hash, err := HashPassword("test password")
	if err != nil {
		t.Fatal(err)
	}
	account, err := database.CreateUser(t.Context(), "admin@example.com", hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateUser(t.Context(), "keeper@example.com", hash); err != nil {
		t.Fatal(err)
	}
	email, verified := "ADMIN@EXAMPLE.COM", true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"test-token","token_type":"Bearer"}`))
		} else {
			if verified {
				_, _ = w.Write([]byte(`{"email":"` + email + `","email_verified":true}`))
			} else {
				_, _ = w.Write([]byte(`{"email":"` + email + `","email_verified":false}`))
			}
		}
	}))
	defer server.Close()
	authentication, err := New(Config{ClientID: "test-client", ClientSecret: "test-secret", RedirectURL: "http://localhost/auth/google/callback", SessionSecret: strings.Repeat("s", 32), OAuthEndpoint: oauth2.Endpoint{AuthURL: server.URL + "/authorize", TokenURL: server.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}, UserInfoURL: server.URL + "/userinfo", HTTPClient: server.Client()}, database)
	if err != nil {
		t.Fatal(err)
	}
	login := func() string {
		t.Helper()
		user, err := authentication.CompleteLogin(t.Context(), "test-code")
		if err != nil || user.AccountID != account.ID || user.Provider != "google" || user.Email != "admin@example.com" {
			t.Fatalf("Google did not link defined user: %+v %v", user, err)
		}
		session, err := authentication.NewSession(user)
		if err != nil {
			t.Fatal(err)
		}
		if _, valid, err := authentication.ValidateSession(t.Context(), session); err != nil || !valid {
			t.Fatal("Google session rejected", err)
		}
		return session
	}
	session := login()
	verified = false
	if _, err := authentication.CompleteLogin(t.Context(), "test-code"); !errors.Is(err, ErrEmailNotVerified) {
		t.Fatal("unverified email accepted", err)
	}
	verified = true
	email = "missing@example.com"
	if _, err := authentication.CompleteLogin(t.Context(), "test-code"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatal("undefined email accepted", err)
	}
	email = "admin@example.com"
	if err := database.ChangeUserPassword(t.Context(), account.ID, hash); err != nil {
		t.Fatal(err)
	}
	if _, valid, _ := authentication.ValidateSession(t.Context(), session); valid {
		t.Fatal("password change did not revoke Google session")
	}
	session = login()
	if err := database.DeleteUser(t.Context(), account.ID); err != nil {
		t.Fatal(err)
	}
	if _, valid, _ := authentication.ValidateSession(t.Context(), session); valid {
		t.Fatal("deleted Google user session accepted")
	}
	if _, err := authentication.CompleteLogin(t.Context(), "test-code"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatal("deleted Google user allowed", err)
	}
	replacement, err := database.CreateUser(t.Context(), "admin@example.com", hash)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == account.ID {
		t.Fatal("account ID reused")
	}
	if _, valid, _ := authentication.ValidateSession(t.Context(), session); valid {
		t.Fatal("recreating email revived deleted session")
	}
}
