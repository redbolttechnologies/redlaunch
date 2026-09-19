package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"redlaunch/internal/application"
)

// ListServerSSHKeys returns operator-managed host keys in creation order.
func (s *Store) ListServerSSHKeys(ctx context.Context) ([]application.ServerSSHKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, display_name, public_key, key_fingerprint, created_at
		FROM server_ssh_keys
		ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list server SSH keys: %w", err)
	}
	defer rows.Close()

	var keys []application.ServerSSHKey
	for rows.Next() {
		item, err := scanServerSSHKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate server SSH keys: %w", err)
	}
	return keys, nil
}

// GetServerSSHKey returns one operator-managed host key by ID.
func (s *Store) GetServerSSHKey(ctx context.Context, id int64) (application.ServerSSHKey, error) {
	var item application.ServerSSHKey
	var createdAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, display_name, public_key, key_fingerprint, created_at
		FROM server_ssh_keys
		WHERE id = ?`, id).Scan(
		&item.ID,
		&item.DisplayName,
		&item.PublicKey,
		&item.KeyFingerprint,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return application.ServerSSHKey{}, application.ErrSSHKeyNotFound
	}
	if err != nil {
		return application.ServerSSHKey{}, fmt.Errorf("get server SSH key: %w", err)
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return application.ServerSSHKey{}, fmt.Errorf("parse server SSH key timestamp: %w", err)
	}
	return item, nil
}

// CreateServerSSHKey persists one operator-managed host key.
func (s *Store) CreateServerSSHKey(ctx context.Context, item application.ServerSSHKey) (application.ServerSSHKey, error) {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO server_ssh_keys (display_name, public_key, key_fingerprint,
			created_at)
		VALUES (?, ?, ?, ?)`,
		item.DisplayName,
		item.PublicKey,
		item.KeyFingerprint,
		item.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return application.ServerSSHKey{}, fmt.Errorf("create server SSH key: %w", err)
		}
		return application.ServerSSHKey{}, fmt.Errorf("create server SSH key: %w", err)
	}
	item.ID, err = result.LastInsertId()
	if err != nil {
		return application.ServerSSHKey{}, fmt.Errorf("read server SSH key ID: %w", err)
	}
	return item, nil
}

// DeleteServerSSHKey removes one operator-managed host key by ID.
func (s *Store) DeleteServerSSHKey(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM server_ssh_keys
		WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete server SSH key: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted server SSH key count: %w", err)
	}
	if affected == 0 {
		return application.ErrSSHKeyNotFound
	}
	return nil
}

type serverSSHKeyScanner interface {
	Scan(...any) error
}

func scanServerSSHKey(scanner serverSSHKeyScanner) (application.ServerSSHKey, error) {
	var item application.ServerSSHKey
	var createdAt string
	if err := scanner.Scan(
		&item.ID,
		&item.DisplayName,
		&item.PublicKey,
		&item.KeyFingerprint,
		&createdAt,
	); err != nil {
		return application.ServerSSHKey{}, fmt.Errorf("scan server SSH key: %w", err)
	}
	var err error
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return application.ServerSSHKey{}, fmt.Errorf("parse server SSH key timestamp: %w", err)
	}
	return item, nil
}
