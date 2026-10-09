package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"redlaunch/internal/application"
	"strings"
	"time"
)

func (s *Store) CreateLocalUser(ctx context.Context, email, hash string) error {
	_, err := s.CreateUser(ctx, email, hash)
	return err
}

func (s *Store) CreateUser(ctx context.Context, email, hash string) (application.UserAccount, error) {
	email, err := application.ValidateEmail(email)
	if err != nil {
		return application.UserAccount{}, err
	}
	if hash == "" {
		return application.UserAccount{}, errors.New("password hash is required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `INSERT INTO users(email,password_hash,created_at,updated_at) VALUES(?,?,?,?)`, email, hash, now, now)
	if isUniqueConstraint(err) {
		return application.UserAccount{}, application.ErrUserExists
	}
	if err != nil {
		return application.UserAccount{}, fmt.Errorf("create user: %w", err)
	}
	id, err := result.LastInsertId()
	return application.UserAccount{ID: id, Email: email, HasPassword: true, CreatedAt: now}, err
}

func scanLoginUser(row *sql.Row) (application.LocalUser, error) {
	var user application.LocalUser
	err := row.Scan(&user.ID, &user.Email, &user.Username, &user.PasswordHash, &user.CredentialVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return user, application.ErrUserNotFound
	}
	if err != nil {
		return user, fmt.Errorf("read user credentials: %w", err)
	}
	return user, nil
}

func (s *Store) GetLocalUser(ctx context.Context, identifier string) (application.LocalUser, error) {
	// Legacy usernames remain usable only until the account is assigned an email.
	return scanLoginUser(s.db.QueryRowContext(ctx, `SELECT id,COALESCE(email,''),COALESCE(legacy_username,''),password_hash,credential_version FROM users WHERE email=? OR (email IS NULL AND legacy_username=?)`, strings.ToLower(strings.TrimSpace(identifier)), strings.ToLower(strings.TrimSpace(identifier))))
}
func (s *Store) GetUserByID(ctx context.Context, id int64) (application.LocalUser, error) {
	return scanLoginUser(s.db.QueryRowContext(ctx, `SELECT id,COALESCE(email,''),COALESCE(legacy_username,''),password_hash,credential_version FROM users WHERE id=?`, id))
}

func (s *Store) ResetLocalPassword(ctx context.Context, email, hash string) error {
	user, err := s.GetLocalUser(ctx, email)
	if err != nil {
		return err
	}
	return s.ChangeUserPassword(ctx, user.ID, hash)
}
func (s *Store) ChangeUserPassword(ctx context.Context, id int64, hash string) error {
	if hash == "" {
		return errors.New("password hash is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash=?,credential_version=credential_version+1,updated_at=? WHERE id=?`, hash, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return application.ErrUserNotFound
	}
	return nil
}
func (s *Store) HasLocalUsers(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE password_hash!='')`).Scan(&exists)
	return exists, err
}
func (s *Store) ListUsers(ctx context.Context) ([]application.UserAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(email,''),COALESCE(legacy_username,''),password_hash!='',created_at FROM users ORDER BY COALESCE(email,legacy_username),id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []application.UserAccount
	for rows.Next() {
		var user application.UserAccount
		if err := rows.Scan(&user.ID, &user.Email, &user.LegacyUsername, &user.HasPassword, &user.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	// Count and deletion are one SQLite statement: concurrent requests cannot
	// both pass a separate count check and remove the final two accounts.
	result, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=? AND (SELECT COUNT(*) FROM users)>1`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := s.GetUserByID(ctx, id); err != nil {
		return err
	}
	return application.ErrLastUser
}
func (s *Store) AssignUserEmail(ctx context.Context, id int64, email string) error {
	email, err := application.ValidateEmail(email)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE users SET email=?,legacy_username=NULL,credential_version=credential_version+1,updated_at=? WHERE id=? AND email IS NULL`, email, time.Now().UTC().Format(time.RFC3339Nano), id)
	if isUniqueConstraint(err) {
		return application.ErrUserExists
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return application.ErrUserNotFound
	}
	return nil
}
