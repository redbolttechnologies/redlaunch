package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type apiTokenRepositoryStub struct {
	mu     sync.Mutex
	nextID int64
	items  map[int64]apiTokenRow
}

type apiTokenRow struct {
	token application.APIToken
	hash  string
}

func newAPITokenRepositoryStub() *apiTokenRepositoryStub {
	return &apiTokenRepositoryStub{items: make(map[int64]apiTokenRow)}
}

func (r *apiTokenRepositoryStub) ListAPITokens(context.Context) ([]application.APIToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var tokens []application.APIToken
	for id := int64(1); id <= r.nextID; id++ {
		if row, ok := r.items[id]; ok {
			tokens = append(tokens, row.token)
		}
	}
	return tokens, nil
}

func (r *apiTokenRepositoryStub) GetAPITokenByHash(_ context.Context, hash []byte) (application.APIToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.items {
		if row.hash == string(hash) {
			return row.token, nil
		}
	}
	return application.APIToken{}, application.ErrAPITokenNotFound
}

func (r *apiTokenRepositoryStub) CreateAPIToken(_ context.Context, item application.APIToken, hash []byte) (application.APIToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	item.ID = r.nextID
	r.items[item.ID] = apiTokenRow{token: item, hash: string(hash)}
	return item, nil
}

func (r *apiTokenRepositoryStub) TouchAPITokenLastUsed(_ context.Context, id int64, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.items[id]
	if !ok {
		return application.ErrAPITokenNotFound
	}
	row.token.LastUsedAt = &at
	r.items[id] = row
	return nil
}

func (r *apiTokenRepositoryStub) DeleteAPIToken(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[id]; !ok {
		return application.ErrAPITokenNotFound
	}
	delete(r.items, id)
	return nil
}

type apiTokenCatalogStub struct {
	applications map[int64]application.Application
	err          error
}

func (c *apiTokenCatalogStub) Get(_ context.Context, id int64) (application.Application, error) {
	if c.err != nil {
		return application.Application{}, c.err
	}
	item, ok := c.applications[id]
	if !ok {
		return application.Application{}, application.ErrNotFound
	}
	return item, nil
}

func newAPITokenServiceForTest() (*APITokenService, *apiTokenRepositoryStub) {
	repository := newAPITokenRepositoryStub()
	catalog := &apiTokenCatalogStub{applications: map[int64]application.Application{
		7: {ID: 7, Name: "Talent hunt", FolderName: "talenthunt"},
	}}
	service, err := NewAPITokenService(repository, catalog)
	if err != nil {
		panic(err)
	}
	return service, repository
}

func TestAPITokenServiceCreateReturnsPlaintextOnce(t *testing.T) {
	service, repository := newAPITokenServiceForTest()

	setup, err := service.Create(t.Context(), application.APITokenInput{
		DisplayName:   "talenthunt migrate via GHA",
		ApplicationID: 7,
		ExpiresInDays: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	plaintext, ok := application.SplitAPIToken(setup.Plaintext)
	if !ok || plaintext != setup.Plaintext {
		t.Fatalf("plaintext = %q, want rlr_ + 64 hex characters", setup.Plaintext)
	}
	if setup.Token.ID < 1 || setup.Token.Prefix != setup.Plaintext[:application.APITokenPrefixLength] {
		t.Fatalf("created token = %#v, want persisted metadata with prefix hint", setup.Token)
	}
	if setup.Token.ExpiresAt == nil {
		t.Fatal("created token expiry = nil, want 90-day expiry")
	}
	if setup.Token.Scope != application.APITokenScopeRun {
		t.Fatalf("created token scope = %q, want run", setup.Token.Scope)
	}

	// The repository must hold only the verifier, never the plaintext.
	sum := sha256.Sum256([]byte(setup.Plaintext))
	repository.mu.Lock()
	row := repository.items[setup.Token.ID]
	repository.mu.Unlock()
	if row.hash != string(sum[:]) {
		t.Fatal("stored verifier is not the SHA-256 of the plaintext")
	}

	authenticated, err := service.Authenticate(t.Context(), setup.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.ID != setup.Token.ID {
		t.Fatalf("authenticated token ID = %d, want %d", authenticated.ID, setup.Token.ID)
	}
	if authenticated.LastUsedAt == nil {
		t.Fatal("authenticated token last use = nil, want recorded")
	}
}

func TestAPITokenServiceCreateValidatesInput(t *testing.T) {
	service, _ := newAPITokenServiceForTest()

	if _, err := service.Create(t.Context(), application.APITokenInput{ApplicationID: 7}); !errors.Is(err, application.ErrAPITokenDisplayNameRequired) {
		t.Fatalf("Create(empty name) error = %v, want %v", err, application.ErrAPITokenDisplayNameRequired)
	}
	if _, err := service.Create(t.Context(), application.APITokenInput{DisplayName: "x"}); !errors.Is(err, application.ErrAPITokenApplicationRequired) {
		t.Fatalf("Create(no app) error = %v, want %v", err, application.ErrAPITokenApplicationRequired)
	}
	if _, err := service.Create(t.Context(), application.APITokenInput{DisplayName: "x", ApplicationID: 404}); !errors.Is(err, application.ErrAPITokenApplicationNotFound) {
		t.Fatalf("Create(unknown app) error = %v, want %v", err, application.ErrAPITokenApplicationNotFound)
	}
	if _, err := service.Create(t.Context(), application.APITokenInput{DisplayName: "x", ApplicationID: 7, ExpiresInDays: 367}); !errors.Is(err, application.ErrAPITokenExpiryInvalid) {
		t.Fatalf("Create(367 days) error = %v, want %v", err, application.ErrAPITokenExpiryInvalid)
	}
}

func TestAPITokenServiceAuthenticateRejectsUnknownMalformedAndExpired(t *testing.T) {
	service, repository := newAPITokenServiceForTest()

	setup, err := service.Create(t.Context(), application.APITokenInput{DisplayName: "job", ApplicationID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if setup.Token.ExpiresAt != nil {
		t.Fatalf("never-expire token expiry = %v, want nil", setup.Token.ExpiresAt)
	}

	for _, value := range []string{"", "Bearer abc", "rlr_short", "rlr_" + strings.Repeat("zz", 32)} {
		if _, err := service.Authenticate(t.Context(), value); !errors.Is(err, application.ErrAPITokenInvalid) {
			t.Fatalf("Authenticate(%q) error = %v, want %v", value, err, application.ErrAPITokenInvalid)
		}
	}

	unknown := application.FormatAPIToken("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if _, err := service.Authenticate(t.Context(), unknown); !errors.Is(err, application.ErrAPITokenInvalid) {
		t.Fatalf("Authenticate(unknown) error = %v, want %v", unknown, application.ErrAPITokenInvalid)
	}

	// Expire the token directly, then revoke it: both must fail closed.
	past := time.Now().UTC().Add(-time.Hour)
	repository.mu.Lock()
	row := repository.items[setup.Token.ID]
	row.token.ExpiresAt = &past
	repository.items[setup.Token.ID] = row
	repository.mu.Unlock()
	if _, err := service.Authenticate(t.Context(), setup.Plaintext); !errors.Is(err, application.ErrAPITokenExpired) {
		t.Fatalf("Authenticate(expired) error = %v, want %v", err, application.ErrAPITokenExpired)
	}

	if err := service.Revoke(t.Context(), setup.Token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(t.Context(), setup.Plaintext); !errors.Is(err, application.ErrAPITokenInvalid) {
		t.Fatalf("Authenticate(revoked) error = %v, want %v", err, application.ErrAPITokenInvalid)
	}
	if err := service.Revoke(t.Context(), setup.Token.ID); !errors.Is(err, application.ErrAPITokenNotFound) {
		t.Fatalf("Revoke(missing) error = %v, want %v", err, application.ErrAPITokenNotFound)
	}
}

func TestNewAPITokenServiceRejectsMissingDependencies(t *testing.T) {
	catalog := &apiTokenCatalogStub{}
	if _, err := NewAPITokenService(nil, catalog); err == nil {
		t.Fatal("NewAPITokenService(nil repository) error = nil, want dependency error")
	}
	if _, err := NewAPITokenService(newAPITokenRepositoryStub(), nil); err == nil {
		t.Fatal("NewAPITokenService(nil catalog) error = nil, want dependency error")
	}
}
