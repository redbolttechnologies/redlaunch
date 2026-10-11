package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"redlaunch/internal/application"
)

// oauthTimeLayout is fixed width, so timestamp comparisons in SQL order
// correctly even when fractional seconds are present.
const oauthTimeLayout = "2006-01-02T15:04:05.000000000Z"

func formatOAuthTime(value time.Time) string {
	return value.UTC().Format(oauthTimeLayout)
}

func nullableApplicationID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// CreateOAuthClient persists one dynamically registered client.
func (s *Store) CreateOAuthClient(ctx context.Context, client application.OAuthClient) error {
	redirectURIs, err := json.Marshal(client.RedirectURIs)
	if err != nil {
		return fmt.Errorf("encode OAuth redirect URIs: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_clients (client_id, display_name, redirect_uris, created_at)
		VALUES (?, ?, ?, ?)`, client.ID, client.Name, string(redirectURIs), formatOAuthTime(client.CreatedAt)); err != nil {
		return fmt.Errorf("create OAuth client: %w", err)
	}
	return nil
}

// GetOAuthClient returns one registered client by its public client ID.
func (s *Store) GetOAuthClient(ctx context.Context, clientID string) (application.OAuthClient, error) {
	var client application.OAuthClient
	var redirectURIs, createdAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT client_id, display_name, redirect_uris, created_at
		FROM oauth_clients
		WHERE client_id = ?`, clientID).Scan(&client.ID, &client.Name, &redirectURIs, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return application.OAuthClient{}, application.ErrOAuthClientNotFound
	}
	if err != nil {
		return application.OAuthClient{}, fmt.Errorf("get OAuth client: %w", err)
	}
	if err := json.Unmarshal([]byte(redirectURIs), &client.RedirectURIs); err != nil {
		return application.OAuthClient{}, fmt.Errorf("decode OAuth redirect URIs: %w", err)
	}
	client.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return application.OAuthClient{}, fmt.Errorf("parse OAuth client timestamp: %w", err)
	}
	return client, nil
}

// CountOAuthClients returns how many clients are registered.
func (s *Store) CountOAuthClients(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_clients`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count OAuth clients: %w", err)
	}
	return count, nil
}

// DeleteUnusedOAuthClients removes clients that were registered before the
// cutoff and never received a grant.
func (s *Store) DeleteUnusedOAuthClients(ctx context.Context, createdBefore time.Time) error {
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM oauth_clients
		WHERE created_at < ?
			AND client_id NOT IN (SELECT client_id FROM oauth_grants)`, formatOAuthTime(createdBefore)); err != nil {
		return fmt.Errorf("delete unused OAuth clients: %w", err)
	}
	return nil
}

const oauthGrantColumns = `id, client_id, display_name, resource, scope, COALESCE(application_id, 0),
	access_token_id, refresh_token_hash, previous_refresh_token_hash, rotated_at, created_at,
	refresh_idle_expires_at, absolute_expires_at`

type oauthRowScanner interface {
	Scan(...any) error
}

func scanOAuthGrant(row oauthRowScanner) (application.OAuthGrant, error) {
	var grant application.OAuthGrant
	var rotatedAt, createdAt, idleExpiresAt, absoluteExpiresAt string
	if err := row.Scan(&grant.ID, &grant.ClientID, &grant.DisplayName, &grant.Resource, &grant.Scope,
		&grant.ApplicationID, &grant.AccessTokenID, &grant.RefreshTokenHash, &grant.PreviousRefreshTokenHash,
		&rotatedAt, &createdAt, &idleExpiresAt, &absoluteExpiresAt); err != nil {
		return application.OAuthGrant{}, err
	}
	var err error
	if rotatedAt != "" {
		if grant.RotatedAt, err = time.Parse(time.RFC3339Nano, rotatedAt); err != nil {
			return application.OAuthGrant{}, fmt.Errorf("parse OAuth rotation time: %w", err)
		}
	}
	if grant.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return application.OAuthGrant{}, fmt.Errorf("parse OAuth grant timestamp: %w", err)
	}
	if grant.RefreshIdleExpiresAt, err = time.Parse(time.RFC3339Nano, idleExpiresAt); err != nil {
		return application.OAuthGrant{}, fmt.Errorf("parse OAuth idle expiry: %w", err)
	}
	if grant.AbsoluteExpiresAt, err = time.Parse(time.RFC3339Nano, absoluteExpiresAt); err != nil {
		return application.OAuthGrant{}, fmt.Errorf("parse OAuth absolute expiry: %w", err)
	}
	return grant, nil
}

func scanOAuthGrantOrNotFound(row oauthRowScanner, operation string) (application.OAuthGrant, error) {
	grant, err := scanOAuthGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return application.OAuthGrant{}, application.ErrOAuthGrantNotFound
	}
	if err != nil {
		return application.OAuthGrant{}, fmt.Errorf("%s: %w", operation, err)
	}
	return grant, nil
}

// CreateOAuthGrant stores a grant and its first access token in one transaction.
func (s *Store) CreateOAuthGrant(ctx context.Context, issue application.OAuthGrantIssue) (application.OAuthGrant, application.APIToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.OAuthGrant{}, application.APIToken{}, fmt.Errorf("begin OAuth grant: %w", err)
	}
	defer tx.Rollback()

	access, err := insertAPIToken(ctx, tx, issue.Access, issue.AccessHash)
	if err != nil {
		return application.OAuthGrant{}, application.APIToken{}, err
	}
	grant := issue.Grant
	grant.AccessTokenID = access.ID
	result, err := tx.ExecContext(ctx, `
		INSERT INTO oauth_grants (client_id, display_name, resource, scope, application_id,
			access_token_id, refresh_token_hash, created_at, refresh_idle_expires_at, absolute_expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		grant.ClientID, grant.DisplayName, grant.Resource, grant.Scope, nullableApplicationID(grant.ApplicationID),
		access.ID, grant.RefreshTokenHash, formatOAuthTime(grant.CreatedAt),
		formatOAuthTime(grant.RefreshIdleExpiresAt), formatOAuthTime(grant.AbsoluteExpiresAt))
	if err != nil {
		return application.OAuthGrant{}, application.APIToken{}, fmt.Errorf("create OAuth grant: %w", err)
	}
	if grant.ID, err = result.LastInsertId(); err != nil {
		return application.OAuthGrant{}, application.APIToken{}, fmt.Errorf("read OAuth grant ID: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return application.OAuthGrant{}, application.APIToken{}, fmt.Errorf("commit OAuth grant: %w", err)
	}
	return grant, access, nil
}

// GetOAuthGrantByRefreshHash returns the grant whose current or immediately
// previous refresh token matches hash. The caller decides which one matched.
func (s *Store) GetOAuthGrantByRefreshHash(ctx context.Context, hash []byte) (application.OAuthGrant, error) {
	if len(hash) == 0 {
		return application.OAuthGrant{}, application.ErrOAuthGrantNotFound
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT `+oauthGrantColumns+`
		FROM oauth_grants
		WHERE refresh_token_hash = ? OR previous_refresh_token_hash = ?`, hash, hash)
	return scanOAuthGrantOrNotFound(row, "get OAuth grant by refresh token")
}

// GetOAuthGrantByAccessTokenHash returns the grant that owns an access token.
func (s *Store) GetOAuthGrantByAccessTokenHash(ctx context.Context, hash []byte) (application.OAuthGrant, error) {
	if len(hash) == 0 {
		return application.OAuthGrant{}, application.ErrOAuthGrantNotFound
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT `+oauthGrantColumns+`
		FROM oauth_grants
		WHERE access_token_id IN (SELECT id FROM api_tokens WHERE token_hash = ?)`, hash)
	return scanOAuthGrantOrNotFound(row, "get OAuth grant by access token")
}

// GetOAuthGrantBySupersededRefreshHash returns the grant that once held a
// refresh token which has since been rotated out. Presenting such a token is
// reuse, so the caller uses this to find the grant it must revoke.
func (s *Store) GetOAuthGrantBySupersededRefreshHash(ctx context.Context, hash []byte) (application.OAuthGrant, error) {
	if len(hash) == 0 {
		return application.OAuthGrant{}, application.ErrOAuthGrantNotFound
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT `+oauthGrantColumns+`
		FROM oauth_grants
		WHERE id IN (SELECT grant_id FROM oauth_superseded_refresh_tokens WHERE refresh_token_hash = ?)`, hash)
	return scanOAuthGrantOrNotFound(row, "get OAuth grant by superseded refresh token")
}

// RotateOAuthGrant swaps in a new access token and refresh token. The current
// refresh hash must still match; otherwise another request rotated first and
// ErrOAuthRotationConflict is returned.
func (s *Store) RotateOAuthGrant(ctx context.Context, rotation application.OAuthGrantRotation) (application.APIToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.APIToken{}, fmt.Errorf("begin OAuth rotation: %w", err)
	}
	defer tx.Rollback()

	var previousAccessTokenID int64
	err = tx.QueryRowContext(ctx, `
		SELECT access_token_id
		FROM oauth_grants
		WHERE id = ? AND refresh_token_hash = ?`, rotation.GrantID, rotation.ExpectedRefreshHash).Scan(&previousAccessTokenID)
	if errors.Is(err, sql.ErrNoRows) {
		return application.APIToken{}, application.ErrOAuthRotationConflict
	}
	if err != nil {
		return application.APIToken{}, fmt.Errorf("read OAuth grant for rotation: %w", err)
	}
	access, err := insertAPIToken(ctx, tx, rotation.Access, rotation.AccessHash)
	if err != nil {
		return application.APIToken{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE oauth_grants
		SET access_token_id = ?,
			previous_refresh_token_hash = refresh_token_hash,
			refresh_token_hash = ?,
			rotated_at = ?,
			refresh_idle_expires_at = ?
		WHERE id = ? AND refresh_token_hash = ?`,
		access.ID, rotation.RefreshHash, formatOAuthTime(rotation.RotatedAt),
		formatOAuthTime(rotation.RefreshIdleExpiresAt), rotation.GrantID, rotation.ExpectedRefreshHash)
	if err != nil {
		return application.APIToken{}, fmt.Errorf("update OAuth grant rotation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return application.APIToken{}, fmt.Errorf("read rotated OAuth grant count: %w", err)
	}
	if affected != 1 {
		return application.APIToken{}, application.ErrOAuthRotationConflict
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO oauth_superseded_refresh_tokens (refresh_token_hash, grant_id, superseded_at)
		VALUES (?, ?, ?)`, rotation.ExpectedRefreshHash, rotation.GrantID, formatOAuthTime(rotation.RotatedAt)); err != nil {
		return application.APIToken{}, fmt.Errorf("record superseded OAuth refresh token: %w", err)
	}
	// Superseded tokens only matter while the grant can still be used.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM oauth_superseded_refresh_tokens
		WHERE grant_id = ? AND superseded_at < ?`, rotation.GrantID,
		formatOAuthTime(rotation.RotatedAt.Add(-application.OAuthRefreshAbsoluteLifetime))); err != nil {
		return application.APIToken{}, fmt.Errorf("prune superseded OAuth refresh tokens: %w", err)
	}
	// The grant already points at the replacement, so deleting the superseded
	// access token does not cascade to the grant.
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, previousAccessTokenID); err != nil {
		return application.APIToken{}, fmt.Errorf("delete superseded OAuth access token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return application.APIToken{}, fmt.Errorf("commit OAuth rotation: %w", err)
	}
	return access, nil
}

// RevokeOAuthGrant deletes a grant and its current access token. Access
// tokens of a revoked grant stop authenticating on the next request.
func (s *Store) RevokeOAuthGrant(ctx context.Context, grantID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin OAuth revocation: %w", err)
	}
	defer tx.Rollback()

	var accessTokenID int64
	err = tx.QueryRowContext(ctx, `SELECT access_token_id FROM oauth_grants WHERE id = ?`, grantID).Scan(&accessTokenID)
	if errors.Is(err, sql.ErrNoRows) {
		return application.ErrOAuthGrantNotFound
	}
	if err != nil {
		return fmt.Errorf("read OAuth grant for revocation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_grants WHERE id = ?`, grantID); err != nil {
		return fmt.Errorf("delete OAuth grant: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, accessTokenID); err != nil {
		return fmt.Errorf("delete OAuth access token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit OAuth revocation: %w", err)
	}
	return nil
}
