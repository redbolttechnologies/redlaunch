package store

import (
	"errors"
	"testing"
	"time"

	"redlaunch/internal/application"
)

func TestAPITokenRoundTrip(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	item, err := database.Create(t.Context(), application.Application{Name: "Talent hunt", FolderName: "talenthunt"})
	if err != nil {
		t.Fatal(err)
	}

	created, err := database.CreateAPIToken(t.Context(), application.APIToken{
		DisplayName:   "talenthunt migrate via GHA",
		Prefix:        "rlr_01234567",
		ApplicationID: item.ID,
		Scope:         application.APITokenScopeRun,
	}, []byte("test-hash-1"))
	if err != nil {
		t.Fatal(err)
	}
	if created.ID < 1 {
		t.Fatalf("created API token ID = %d, want positive", created.ID)
	}

	expires := time.Now().UTC().Add(90 * 24 * time.Hour).Truncate(time.Second)
	second, err := database.CreateAPIToken(t.Context(), application.APIToken{
		DisplayName:   "second token",
		Prefix:        "rlr_89abcdef",
		ApplicationID: item.ID,
		Scope:         application.APITokenScopeRun,
		ExpiresAt:     &expires,
	}, []byte("test-hash-2"))
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := database.ListAPITokens(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0].ID != created.ID || tokens[1].ID != second.ID {
		t.Fatalf("listed API tokens = %#v, want creation order", tokens)
	}
	if tokens[0].DisplayName != "talenthunt migrate via GHA" || tokens[0].Prefix != "rlr_01234567" {
		t.Fatalf("first API token = %#v, want display metadata", tokens[0])
	}
	if tokens[0].ExpiresAt != nil || tokens[0].LastUsedAt != nil {
		t.Fatalf("fresh API token timestamps = (%v, %v), want nil", tokens[0].ExpiresAt, tokens[0].LastUsedAt)
	}

	byHash, err := database.GetAPITokenByHash(t.Context(), []byte("test-hash-2"))
	if err != nil {
		t.Fatal(err)
	}
	if byHash.ID != second.ID || byHash.ExpiresAt == nil || !byHash.ExpiresAt.Equal(expires) {
		t.Fatalf("token by hash = %#v, want stored expiry %s", byHash, expires)
	}

	if _, err := database.GetAPITokenByHash(t.Context(), []byte("unknown-hash")); !errors.Is(err, application.ErrAPITokenNotFound) {
		t.Fatalf("GetAPITokenByHash(unknown) error = %v, want %v", err, application.ErrAPITokenNotFound)
	}

	usedAt := time.Now().UTC().Truncate(time.Second)
	if err := database.TouchAPITokenLastUsed(t.Context(), created.ID, usedAt); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetAPIToken(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(usedAt) {
		t.Fatalf("last used = %v, want %s", got.LastUsedAt, usedAt)
	}

	if err := database.DeleteAPIToken(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetAPIToken(t.Context(), created.ID); !errors.Is(err, application.ErrAPITokenNotFound) {
		t.Fatalf("GetAPIToken(deleted) error = %v, want %v", err, application.ErrAPITokenNotFound)
	}
	if err := database.DeleteAPIToken(t.Context(), created.ID); !errors.Is(err, application.ErrAPITokenNotFound) {
		t.Fatalf("DeleteAPIToken(missing) error = %v, want %v", err, application.ErrAPITokenNotFound)
	}
	if _, err := database.GetAPITokenByHash(t.Context(), []byte("test-hash-1")); !errors.Is(err, application.ErrAPITokenNotFound) {
		t.Fatalf("GetAPITokenByHash(deleted) error = %v, want %v", err, application.ErrAPITokenNotFound)
	}
}

func TestCreateAPITokenRejectsMissingHash(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if _, err := database.CreateAPIToken(t.Context(), application.APIToken{
		DisplayName: "no hash",
		Prefix:      "rlr_00000000",
		Scope:       application.APITokenScopeRun,
	}, nil); !errors.Is(err, application.ErrAPITokenHashMissing) {
		t.Fatalf("CreateAPIToken(nil hash) error = %v, want %v", err, application.ErrAPITokenHashMissing)
	}
}
