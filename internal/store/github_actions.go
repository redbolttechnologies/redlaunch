package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"redlaunch/internal/application"
)

// GetGitHubActions returns the repository integration for one application.
func (s *Store) GetGitHubActions(ctx context.Context, applicationID int64) (application.GitHubActionsIntegration, error) {
	var item application.GitHubActionsIntegration
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, application_id, repository, branch, dockerfile, build_context,
			service_name, image_name, server_host, server_port, ssh_username,
			public_key, key_fingerprint, created_at, updated_at
		FROM github_actions_integrations
		WHERE application_id = ?`, applicationID).Scan(
		&item.ID,
		&item.ApplicationID,
		&item.Repository,
		&item.Branch,
		&item.Dockerfile,
		&item.BuildContext,
		&item.ServiceName,
		&item.ImageName,
		&item.ServerHost,
		&item.ServerPort,
		&item.SSHUsername,
		&item.PublicKey,
		&item.KeyFingerprint,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return application.GitHubActionsIntegration{}, application.ErrGitHubActionsNotConfigured
	}
	if err != nil {
		return application.GitHubActionsIntegration{}, fmt.Errorf("get GitHub Actions integration: %w", err)
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return application.GitHubActionsIntegration{}, fmt.Errorf("parse GitHub Actions creation timestamp: %w", err)
	}
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return application.GitHubActionsIntegration{}, fmt.Errorf("parse GitHub Actions update timestamp: %w", err)
	}
	return item, nil
}

// ListGitHubActions returns all integrations in stable application order.
func (s *Store) ListGitHubActions(ctx context.Context) ([]application.GitHubActionsIntegration, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, application_id, repository, branch, dockerfile, build_context,
			service_name, image_name, server_host, server_port, ssh_username,
			public_key, key_fingerprint, created_at, updated_at
		FROM github_actions_integrations
		ORDER BY application_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list GitHub Actions integrations: %w", err)
	}
	defer rows.Close()

	var integrations []application.GitHubActionsIntegration
	for rows.Next() {
		item, err := scanGitHubActionsIntegration(rows)
		if err != nil {
			return nil, err
		}
		integrations = append(integrations, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate GitHub Actions integrations: %w", err)
	}
	return integrations, nil
}

// SaveGitHubActions creates or replaces one application's integration.
func (s *Store) SaveGitHubActions(ctx context.Context, item application.GitHubActionsIntegration) (application.GitHubActionsIntegration, error) {
	now := time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO github_actions_integrations (
			application_id, repository, branch, dockerfile, build_context,
			service_name, image_name, server_host, server_port, ssh_username,
			public_key, key_fingerprint, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(application_id) DO UPDATE SET
			repository = excluded.repository,
			branch = excluded.branch,
			dockerfile = excluded.dockerfile,
			build_context = excluded.build_context,
			service_name = excluded.service_name,
			image_name = excluded.image_name,
			server_host = excluded.server_host,
			server_port = excluded.server_port,
			ssh_username = excluded.ssh_username,
			public_key = excluded.public_key,
			key_fingerprint = excluded.key_fingerprint,
			updated_at = excluded.updated_at`,
		item.ApplicationID,
		item.Repository,
		item.Branch,
		item.Dockerfile,
		item.BuildContext,
		item.ServiceName,
		item.ImageName,
		item.ServerHost,
		item.ServerPort,
		item.SSHUsername,
		item.PublicKey,
		item.KeyFingerprint,
		item.CreatedAt.Format(time.RFC3339Nano),
		item.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return application.GitHubActionsIntegration{}, fmt.Errorf("save GitHub Actions integration: %w", err)
	}
	return s.GetGitHubActions(ctx, item.ApplicationID)
}

// DeleteGitHubActions removes an application's repository integration.
func (s *Store) DeleteGitHubActions(ctx context.Context, applicationID int64) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM github_actions_integrations
		WHERE application_id = ?`, applicationID)
	if err != nil {
		return fmt.Errorf("delete GitHub Actions integration: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted GitHub Actions integration count: %w", err)
	}
	if affected == 0 {
		return application.ErrGitHubActionsNotConfigured
	}
	return nil
}

type githubActionsScanner interface {
	Scan(...any) error
}

func scanGitHubActionsIntegration(scanner githubActionsScanner) (application.GitHubActionsIntegration, error) {
	var item application.GitHubActionsIntegration
	var createdAt, updatedAt string
	if err := scanner.Scan(
		&item.ID,
		&item.ApplicationID,
		&item.Repository,
		&item.Branch,
		&item.Dockerfile,
		&item.BuildContext,
		&item.ServiceName,
		&item.ImageName,
		&item.ServerHost,
		&item.ServerPort,
		&item.SSHUsername,
		&item.PublicKey,
		&item.KeyFingerprint,
		&createdAt,
		&updatedAt,
	); err != nil {
		return application.GitHubActionsIntegration{}, fmt.Errorf("scan GitHub Actions integration: %w", err)
	}
	var err error
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return application.GitHubActionsIntegration{}, fmt.Errorf("parse GitHub Actions creation timestamp: %w", err)
	}
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return application.GitHubActionsIntegration{}, fmt.Errorf("parse GitHub Actions update timestamp: %w", err)
	}
	return item, nil
}
