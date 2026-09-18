package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"redlaunch/internal/application"
)

// ListAPITokens returns operator-managed API tokens in creation order. Token
// hashes are verification material and are never returned; authentication
// uses GetAPITokenByHash instead.
func (s *Store) ListAPITokens(ctx context.Context) ([]application.APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, display_name, prefix, application_id, scope, created_at,
			expires_at, last_used_at
		FROM api_tokens
		ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list API tokens: %w", err)
	}
	defer rows.Close()

	var tokens []application.APIToken
	for rows.Next() {
		item, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate API tokens: %w", err)
	}
	return tokens, nil
}

// GetAPIToken returns one operator-managed API token by ID.
func (s *Store) GetAPIToken(ctx context.Context, id int64) (application.APIToken, error) {
	var item application.APIToken
	var createdAt string
	var expiresAt, lastUsedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, display_name, prefix, application_id, scope, created_at,
			expires_at, last_used_at
		FROM api_tokens
		WHERE id = ?`, id).Scan(
		&item.ID,
		&item.DisplayName,
		&item.Prefix,
		&item.ApplicationID,
		&item.Scope,
		&createdAt,
		&expiresAt,
		&lastUsedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return application.APIToken{}, application.ErrAPITokenNotFound
	}
	if err != nil {
		return application.APIToken{}, fmt.Errorf("get API token: %w", err)
	}
	if err := assignAPITokenTimestamps(&item, createdAt, expiresAt, lastUsedAt); err != nil {
		return application.APIToken{}, err
	}
	return item, nil
}

// GetAPITokenByHash returns the token metadata for one SHA-256 token hash.
// The hash itself is never returned.
func (s *Store) GetAPITokenByHash(ctx context.Context, tokenHash []byte) (application.APIToken, error) {
	if len(tokenHash) == 0 {
		return application.APIToken{}, application.ErrAPITokenNotFound
	}
	var item application.APIToken
	var createdAt string
	var expiresAt, lastUsedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, display_name, prefix, application_id, scope, created_at,
			expires_at, last_used_at
		FROM api_tokens
		WHERE token_hash = ?`, tokenHash).Scan(
		&item.ID,
		&item.DisplayName,
		&item.Prefix,
		&item.ApplicationID,
		&item.Scope,
		&createdAt,
		&expiresAt,
		&lastUsedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return application.APIToken{}, application.ErrAPITokenNotFound
	}
	if err != nil {
		return application.APIToken{}, fmt.Errorf("get API token by hash: %w", err)
	}
	if err := assignAPITokenTimestamps(&item, createdAt, expiresAt, lastUsedAt); err != nil {
		return application.APIToken{}, err
	}
	return item, nil
}

// CreateAPIToken persists one operator-managed API token with its SHA-256
// verifier. Plaintext tokens must never reach this layer.
func (s *Store) CreateAPIToken(ctx context.Context, item application.APIToken, tokenHash []byte) (application.APIToken, error) {
	if len(tokenHash) == 0 {
		return application.APIToken{}, application.ErrAPITokenHashMissing
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO api_tokens (display_name, prefix, token_hash, application_id,
			scope, created_at, expires_at, last_used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		item.DisplayName,
		item.Prefix,
		tokenHash,
		item.ApplicationID,
		item.Scope,
		item.CreatedAt.Format(time.RFC3339Nano),
		formatNullableAPITokenTime(item.ExpiresAt),
		formatNullableAPITokenTime(item.LastUsedAt),
	)
	if err != nil {
		return application.APIToken{}, fmt.Errorf("create API token: %w", err)
	}
	item.ID, err = result.LastInsertId()
	if err != nil {
		return application.APIToken{}, fmt.Errorf("read API token ID: %w", err)
	}
	return item, nil
}

// TouchAPITokenLastUsed records when a token last authenticated.
func (s *Store) TouchAPITokenLastUsed(ctx context.Context, id int64, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE api_tokens
		SET last_used_at = ?
		WHERE id = ?`, at.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("touch API token last use: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read touched API token count: %w", err)
	}
	if affected == 0 {
		return application.ErrAPITokenNotFound
	}
	return nil
}

// DeleteAPIToken removes one operator-managed API token by ID. Deletion takes
// effect immediately: authentication looks the hash up on every request.
func (s *Store) DeleteAPIToken(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM api_tokens
		WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete API token: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted API token count: %w", err)
	}
	if affected == 0 {
		return application.ErrAPITokenNotFound
	}
	return nil
}

type apiTokenScanner interface {
	Scan(...any) error
}

func scanAPIToken(scanner apiTokenScanner) (application.APIToken, error) {
	var item application.APIToken
	var createdAt string
	var expiresAt, lastUsedAt sql.NullString
	if err := scanner.Scan(
		&item.ID,
		&item.DisplayName,
		&item.Prefix,
		&item.ApplicationID,
		&item.Scope,
		&createdAt,
		&expiresAt,
		&lastUsedAt,
	); err != nil {
		return application.APIToken{}, fmt.Errorf("scan API token: %w", err)
	}
	if err := assignAPITokenTimestamps(&item, createdAt, expiresAt, lastUsedAt); err != nil {
		return application.APIToken{}, err
	}
	return item, nil
}

func assignAPITokenTimestamps(item *application.APIToken, createdAt string, expiresAt, lastUsedAt sql.NullString) error {
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return fmt.Errorf("parse API token timestamp: %w", err)
	}
	item.CreatedAt = created
	if expiresAt.Valid && expiresAt.String != "" {
		expires, err := time.Parse(time.RFC3339Nano, expiresAt.String)
		if err != nil {
			return fmt.Errorf("parse API token expiry: %w", err)
		}
		item.ExpiresAt = &expires
	}
	if lastUsedAt.Valid && lastUsedAt.String != "" {
		used, err := time.Parse(time.RFC3339Nano, lastUsedAt.String)
		if err != nil {
			return fmt.Errorf("parse API token last use: %w", err)
		}
		item.LastUsedAt = &used
	}
	return nil
}

func formatNullableAPITokenTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
