package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type localTestStore struct {
	user application.LocalUser
	err  error
}

func (s *localTestStore) GetLocalUser(_ context.Context, username string) (application.LocalUser, error) {
	if s.err != nil {
		return application.LocalUser{}, s.err
	}
	if username != s.user.Username {
		return application.LocalUser{}, application.ErrLocalUserNotFound
	}
	return s.user, nil
}
func (s *localTestStore) IsAuthorizedEmail(context.Context, string) (bool, error) { return true, nil }

func newLocalTestService(t *testing.T) (*Service, *localTestStore) {
	t.Helper()
	hash, err := HashPassword("password")
	if err != nil {
		t.Fatal(err)
	}
	store := &localTestStore{user: application.LocalUser{Username: "admin", PasswordHash: hash, CredentialVersion: 1}}
	service, err := New(Config{SessionSecret: strings.Repeat("s", 32)}, store)
	if err != nil {
		t.Fatal(err)
	}
	return service, store
}

func TestLocalLoginAndSessionRevocation(t *testing.T) {
	service, store := newLocalTestService(t)
	if !service.Enabled() || !service.LocalEnabled() || service.GoogleEnabled() || service.AuthorizationURL("state") != "" {
		t.Fatal("incorrect provider configuration")
	}
	now := time.Unix(1700000000, 0)
	service.now = func() time.Time { return now }
	user, err := service.AuthenticatePassword(t.Context(), " ADMIN ", "password", "127.0.0.1")
	if err != nil || user.Username != "admin" || user.Email != "" {
		t.Fatalf("login: %v, %v", user, err)
	}
	session, err := service.NewSession(user)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(session, store.user.PasswordHash) {
		t.Fatal("credential entered session")
	}
	if _, valid, err := service.ValidateSession(t.Context(), session); err != nil || !valid {
		t.Fatal("valid local session rejected", err)
	}
	if _, valid, _ := service.ValidateSession(t.Context(), session+"x"); valid {
		t.Fatal("tampered session accepted")
	}
	store.user.CredentialVersion++
	if _, valid, _ := service.ValidateSession(t.Context(), session); valid {
		t.Fatal("reset did not revoke session")
	}
	store.user.CredentialVersion--
	now = now.Add(13 * time.Hour)
	if _, valid, _ := service.ValidateSession(t.Context(), session); valid {
		t.Fatal("expired session accepted")
	}
	now = now.Add(-13 * time.Hour)
	store.user.Username = "other"
	if _, valid, _ := service.ValidateSession(t.Context(), session); valid {
		t.Fatal("removed account session accepted")
	}
}

func TestLocalLoginFailuresAndThrottle(t *testing.T) {
	service, store := newLocalTestService(t)
	now := time.Unix(1700000000, 0)
	service.now = func() time.Time { return now }
	for _, username := range []string{"admin", "missing", "<script>"} {
		if _, err := service.AuthenticatePassword(t.Context(), username, "incorrect password", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal("expected generic credential failure", err)
		}
	}
	for range 4 {
		_, _ = service.AuthenticatePassword(t.Context(), "admin", "incorrect password", "127.0.0.1")
	}
	if _, err := service.AuthenticatePassword(t.Context(), "admin", "password", "different-ip"); !errors.Is(err, ErrLoginThrottled) {
		t.Fatal("account limit not enforced", err)
	}
	now = now.Add(time.Minute)
	if _, err := service.AuthenticatePassword(t.Context(), "admin", "password", "127.0.0.1"); err != nil {
		t.Fatal("limit did not expire", err)
	}
	store.err = errors.New("sensitive database detail")
	if _, err := service.AuthenticatePassword(t.Context(), "admin", "password", "127.0.0.1"); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("database error was exposed")
	}
}

func TestLimiterBoundsIPAndMemory(t *testing.T) {
	limiter := loginLimiter{entries: make(map[string]loginAttempts)}
	now := time.Unix(1700000000, 0)
	for i := range 20 {
		if !limiter.allow(now, string(rune('a'+i)), "ip") {
			t.Fatal("unexpected early IP limit")
		}
	}
	if limiter.allow(now, "next", "ip") {
		t.Fatal("IP limit not enforced")
	}
	for i := range 4096 {
		limiter.entries[string(rune(i))] = loginAttempts{1, now.Add(time.Minute)}
	}
	if limiter.allow(now, "new-user", "new-ip") {
		t.Fatal("unbounded limiter map")
	}
	if !limiter.allow(now.Add(time.Minute), "new-user", "new-ip") {
		t.Fatal("expired entries not removed")
	}
}

func TestHashConcurrencyBound(t *testing.T) {
	service, _ := newLocalTestService(t)
	service.hashSlots <- struct{}{}
	service.hashSlots <- struct{}{}
	if _, err := service.AuthenticatePassword(t.Context(), "admin", "password", "ip"); !errors.Is(err, ErrLoginThrottled) {
		t.Fatal("hash concurrency not bounded", err)
	}
}

func TestBothProvidersKeepSeparateSessions(t *testing.T) {
	_, store := newLocalTestService(t)
	service, err := New(Config{ClientID: "client", ClientSecret: "secret", RedirectURL: "https://example.com/auth/google/callback", SessionSecret: strings.Repeat("s", 32)}, store)
	if err != nil {
		t.Fatal(err)
	}
	if !service.GoogleEnabled() || !service.LocalEnabled() {
		t.Fatal("both providers not enabled")
	}
	local, err := service.AuthenticatePassword(t.Context(), "admin", "password", "ip")
	if err != nil {
		t.Fatal(err)
	}
	localSession, err := service.NewSession(local)
	if err != nil {
		t.Fatal(err)
	}
	googleSession, err := service.NewSession(User{Email: "admin@example.com", Name: "Google Admin"})
	if err != nil {
		t.Fatal(err)
	}
	store.user.CredentialVersion++
	if _, valid, _ := service.ValidateSession(t.Context(), localSession); valid {
		t.Fatal("reset did not revoke local session")
	}
	if user, valid, err := service.ValidateSession(t.Context(), googleSession); err != nil || !valid || user.Email != "admin@example.com" || user.Username != "" {
		t.Fatal("local reset affected Google identity", err)
	}
	if _, err := service.NewSession(User{Username: "admin", Email: "admin@example.com", CredentialVersion: 1}); err == nil {
		t.Fatal("ambiguous identity accepted")
	}
}
