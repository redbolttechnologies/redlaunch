package store

import (
	"errors"
	"testing"
	"time"

	"redlaunch/internal/application"
)

var oauthTestBase = time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

func openOAuthTestStore(t *testing.T) *Store {
	t.Helper()
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func createOAuthClientForTest(t *testing.T, database *Store, id string, createdAt time.Time) application.OAuthClient {
	t.Helper()
	client := application.OAuthClient{
		ID:           id,
		Name:         "Assistant",
		RedirectURIs: []string{"http://127.0.0.1/callback", "https://client.example/callback"},
		CreatedAt:    createdAt,
	}
	if err := database.CreateOAuthClient(t.Context(), client); err != nil {
		t.Fatal(err)
	}
	return client
}

func manageGrantIssueForTest(clientID string, refreshHash, accessHash string) application.OAuthGrantIssue {
	expires := oauthTestBase.Add(time.Hour)
	return application.OAuthGrantIssue{
		Grant: application.OAuthGrant{
			ClientID:             clientID,
			DisplayName:          "Assistant",
			Resource:             "https://vps.example.com/mcp/manage",
			Scope:                application.APITokenScopeManage,
			RefreshTokenHash:     []byte(refreshHash),
			CreatedAt:            oauthTestBase,
			RefreshIdleExpiresAt: oauthTestBase.Add(30 * 24 * time.Hour),
			AbsoluteExpiresAt:    oauthTestBase.Add(90 * 24 * time.Hour),
		},
		Access: application.APIToken{
			DisplayName: "Assistant",
			Prefix:      "rlr_access01",
			Scope:       application.APITokenScopeManage,
			CreatedAt:   oauthTestBase,
			ExpiresAt:   &expires,
		},
		AccessHash: []byte(accessHash),
	}
}

func TestOAuthMigrationUpgradesExistingDatabase(t *testing.T) {
	path := t.TempDir() + "/redlaunch.db"
	database, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`DROP TABLE oauth_superseded_refresh_tokens; DROP TABLE oauth_grants; DROP TABLE oauth_clients; DELETE FROM schema_migrations WHERE version = 26;`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CountOAuthClients(t.Context()); err != nil {
		t.Fatalf("OAuth tables missing after upgrade: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var applied int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 26`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration 26 recorded %d times (%v), want once", applied, err)
	}
}

func TestOAuthClientsAreStoredAndPrunedWhenUnused(t *testing.T) {
	database := openOAuthTestStore(t)
	stale := createOAuthClientForTest(t, database, "rlc_stale", oauthTestBase)
	used := createOAuthClientForTest(t, database, "rlc_used", oauthTestBase)

	got, err := database.GetOAuthClient(t.Context(), used.ID)
	if err != nil || got.Name != "Assistant" || len(got.RedirectURIs) != 2 || !got.CreatedAt.Equal(oauthTestBase) {
		t.Fatalf("GetOAuthClient() = %#v, %v", got, err)
	}
	if _, err := database.GetOAuthClient(t.Context(), "rlc_missing"); !errors.Is(err, application.ErrOAuthClientNotFound) {
		t.Fatalf("GetOAuthClient(missing) error = %v, want %v", err, application.ErrOAuthClientNotFound)
	}

	if _, _, err := database.CreateOAuthGrant(t.Context(), manageGrantIssueForTest(used.ID, "refresh-used", "access-used")); err != nil {
		t.Fatal(err)
	}
	if count, err := database.CountOAuthClients(t.Context()); err != nil || count != 2 {
		t.Fatalf("CountOAuthClients() = %d, %v; want 2", count, err)
	}

	if err := database.DeleteUnusedOAuthClients(t.Context(), oauthTestBase.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetOAuthClient(t.Context(), stale.ID); !errors.Is(err, application.ErrOAuthClientNotFound) {
		t.Fatalf("unused client survived pruning: %v", err)
	}
	if _, err := database.GetOAuthClient(t.Context(), used.ID); err != nil {
		t.Fatalf("client with a grant was pruned: %v", err)
	}
}

func TestOAuthGrantRotationIsCompareAndSwapAndRetiresOldAccessToken(t *testing.T) {
	database := openOAuthTestStore(t)
	createOAuthClientForTest(t, database, "rlc_rotate", oauthTestBase)
	grant, access, err := database.CreateOAuthGrant(t.Context(), manageGrantIssueForTest("rlc_rotate", "refresh-1", "access-1"))
	if err != nil {
		t.Fatal(err)
	}

	byRefresh, err := database.GetOAuthGrantByRefreshHash(t.Context(), []byte("refresh-1"))
	if err != nil || byRefresh.ID != grant.ID || byRefresh.AccessTokenID != access.ID {
		t.Fatalf("GetOAuthGrantByRefreshHash() = %#v, %v", byRefresh, err)
	}
	byAccess, err := database.GetOAuthGrantByAccessTokenHash(t.Context(), []byte("access-1"))
	if err != nil || byAccess.ID != grant.ID {
		t.Fatalf("GetOAuthGrantByAccessTokenHash() = %#v, %v", byAccess, err)
	}

	rotation := application.OAuthGrantRotation{
		GrantID:              grant.ID,
		ExpectedRefreshHash:  []byte("stale-refresh"),
		Access:               application.APIToken{DisplayName: "Assistant", Prefix: "rlr_access02", Scope: application.APITokenScopeManage, CreatedAt: oauthTestBase},
		AccessHash:           []byte("access-2"),
		RefreshHash:          []byte("refresh-2"),
		RotatedAt:            oauthTestBase.Add(time.Minute),
		RefreshIdleExpiresAt: oauthTestBase.Add(31 * 24 * time.Hour),
	}
	if _, err := database.RotateOAuthGrant(t.Context(), rotation); !errors.Is(err, application.ErrOAuthRotationConflict) {
		t.Fatalf("RotateOAuthGrant(stale) error = %v, want %v", err, application.ErrOAuthRotationConflict)
	}
	if _, err := database.GetAPITokenByHash(t.Context(), []byte("access-2")); !errors.Is(err, application.ErrAPITokenNotFound) {
		t.Fatal("conflicting rotation left a new access token behind")
	}

	rotation.ExpectedRefreshHash = []byte("refresh-1")
	next, err := database.RotateOAuthGrant(t.Context(), rotation)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := database.GetOAuthGrantByRefreshHash(t.Context(), []byte("refresh-1"))
	if err != nil || rotated.ID != grant.ID || string(rotated.RefreshTokenHash) != "refresh-2" || string(rotated.PreviousRefreshTokenHash) != "refresh-1" {
		t.Fatalf("rotated grant = %#v, %v; want refresh-2 current and refresh-1 previous", rotated, err)
	}
	if !rotated.RotatedAt.Equal(oauthTestBase.Add(time.Minute)) || rotated.AccessTokenID != next.ID {
		t.Fatalf("rotated grant metadata = %#v", rotated)
	}
	if _, err := database.GetAPIToken(t.Context(), access.ID); !errors.Is(err, application.ErrAPITokenNotFound) {
		t.Fatal("superseded access token survived rotation")
	}
	if _, err := database.GetOAuthGrantByAccessTokenHash(t.Context(), []byte("access-1")); !errors.Is(err, application.ErrOAuthGrantNotFound) {
		t.Fatal("old access token still resolves to a grant")
	}
	if current, err := database.GetAPITokenByHash(t.Context(), []byte("access-2")); err != nil || current.ID != next.ID {
		t.Fatalf("replacement access token = %#v, %v", current, err)
	}

	superseded, err := database.GetOAuthGrantBySupersededRefreshHash(t.Context(), []byte("refresh-1"))
	if err != nil || superseded.ID != grant.ID {
		t.Fatalf("superseded refresh token lookup = %#v, %v", superseded, err)
	}
	if err := database.RevokeOAuthGrant(t.Context(), grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetOAuthGrantBySupersededRefreshHash(t.Context(), []byte("refresh-1")); !errors.Is(err, application.ErrOAuthGrantNotFound) {
		t.Fatal("revoked grant still resolves by superseded refresh token")
	}
	if _, err := database.GetOAuthGrantByRefreshHash(t.Context(), []byte("refresh-2")); !errors.Is(err, application.ErrOAuthGrantNotFound) {
		t.Fatal("revoked grant still resolves by refresh token")
	}
	if _, err := database.GetAPITokenByHash(t.Context(), []byte("access-2")); !errors.Is(err, application.ErrAPITokenNotFound) {
		t.Fatal("revoked grant left its access token active")
	}
	if err := database.RevokeOAuthGrant(t.Context(), grant.ID); !errors.Is(err, application.ErrOAuthGrantNotFound) {
		t.Fatalf("RevokeOAuthGrant(missing) error = %v, want %v", err, application.ErrOAuthGrantNotFound)
	}
}

func TestOAuthRunGrantEndsWithItsApplication(t *testing.T) {
	database := openOAuthTestStore(t)
	createOAuthClientForTest(t, database, "rlc_runs", oauthTestBase)
	item, err := database.Create(t.Context(), application.Application{Name: "Talent hunt", FolderName: "talenthunt"})
	if err != nil {
		t.Fatal(err)
	}
	run := manageGrantIssueForTest("rlc_runs", "refresh-run", "access-run")
	run.Grant.Scope = application.APITokenScopeRun
	run.Grant.Resource = "https://vps.example.com/mcp/apps/1"
	run.Grant.ApplicationID = item.ID
	run.Access.Scope = application.APITokenScopeRun
	run.Access.ApplicationID = item.ID
	if _, _, err := database.CreateOAuthGrant(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	manage, _, err := database.CreateOAuthGrant(t.Context(), manageGrantIssueForTest("rlc_runs", "refresh-manage", "access-manage"))
	if err != nil {
		t.Fatal(err)
	}
	var reference any
	if err := database.db.QueryRow(`SELECT application_id FROM oauth_grants WHERE id = ?`, manage.ID).Scan(&reference); err != nil || reference != nil {
		t.Fatalf("management grant application_id = %v, %v; want NULL", reference, err)
	}

	if err := database.DeleteApplication(t.Context(), item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetOAuthGrantByRefreshHash(t.Context(), []byte("refresh-run")); !errors.Is(err, application.ErrOAuthGrantNotFound) {
		t.Fatal("run grant survived application deletion")
	}
	if _, err := database.GetAPITokenByHash(t.Context(), []byte("access-run")); !errors.Is(err, application.ErrAPITokenNotFound) {
		t.Fatal("run access token survived application deletion")
	}
	if _, err := database.GetOAuthGrantByRefreshHash(t.Context(), []byte("refresh-manage")); err != nil {
		t.Fatalf("management grant was removed with an application: %v", err)
	}
}

func TestOAuthGrantRejectsScopeWithoutApplication(t *testing.T) {
	database := openOAuthTestStore(t)
	createOAuthClientForTest(t, database, "rlc_bad", oauthTestBase)
	bad := manageGrantIssueForTest("rlc_bad", "refresh-bad", "access-bad")
	bad.Grant.Scope = application.APITokenScopeRun
	if _, _, err := database.CreateOAuthGrant(t.Context(), bad); err == nil {
		t.Fatal("run grant without an application persisted")
	}
	if _, err := database.GetAPITokenByHash(t.Context(), []byte("access-bad")); !errors.Is(err, application.ErrAPITokenNotFound) {
		t.Fatal("failed grant insert left its access token behind")
	}
}

func TestOAuthConnectionMetadataAndLastUseSurviveRotation(t *testing.T) {
	database := openOAuthTestStore(t)
	createOAuthClientForTest(t, database, "rlc_settings", oauthTestBase)
	grant, access, err := database.CreateOAuthGrant(t.Context(), manageGrantIssueForTest("rlc_settings", "settings-refresh-1", "settings-access-1"))
	if err != nil {
		t.Fatal(err)
	}
	manual, err := database.CreateAPIToken(t.Context(), application.APIToken{DisplayName: "Manual automation", Scope: application.APITokenScopeManage, Prefix: "rlr_manual"}, []byte("manual-hash"))
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := database.ListAPITokens(t.Context())
	if err != nil || len(tokens) != 1 || tokens[0].ID != manual.ID {
		t.Fatalf("manual token list = %#v, %v", tokens, err)
	}
	items, err := database.ListOAuthConnections(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatalf("connection list = %#v, %v", items, err)
	}
	item := items[0]
	if item.ID != grant.ID || item.ClientID != "rlc_settings" || item.Resource != grant.Resource || item.LastUsedAt != nil || !item.ExpiresAt.Equal(grant.RefreshIdleExpiresAt) {
		t.Fatalf("connection metadata = %#v", item)
	}
	used := oauthTestBase.Add(20 * time.Minute)
	if err := database.TouchAPITokenLastUsed(t.Context(), access.ID, used); err != nil {
		t.Fatal(err)
	}
	_, err = database.RotateOAuthGrant(t.Context(), application.OAuthGrantRotation{
		GrantID: grant.ID, ExpectedRefreshHash: []byte("settings-refresh-1"),
		Access:     application.APIToken{DisplayName: "Assistant", Prefix: "rlr_settings", Scope: application.APITokenScopeManage},
		AccessHash: []byte("settings-access-2"), RefreshHash: []byte("settings-refresh-2"),
		RotatedAt: oauthTestBase.Add(time.Hour), RefreshIdleExpiresAt: grant.AbsoluteExpiresAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err = database.ListOAuthConnections(t.Context())
	if err != nil || len(items) != 1 || items[0].ID != grant.ID {
		t.Fatalf("rotated list = %#v, %v", items, err)
	}
	if items[0].LastUsedAt == nil || !items[0].LastUsedAt.Equal(used) || !items[0].ExpiresAt.Equal(grant.AbsoluteExpiresAt) {
		t.Fatalf("rotation lost metadata = %#v", items[0])
	}
	if err := database.RevokeOAuthGrant(t.Context(), grant.ID); err != nil {
		t.Fatal(err)
	}
	items, err = database.ListOAuthConnections(t.Context())
	if err != nil || len(items) != 0 {
		t.Fatalf("revoked list = %#v, %v", items, err)
	}
	tokens, err = database.ListAPITokens(t.Context())
	if err != nil || len(tokens) != 1 || tokens[0].ID != manual.ID {
		t.Fatal("revocation changed a manual token")
	}
}
