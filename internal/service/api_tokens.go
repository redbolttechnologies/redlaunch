package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"redlaunch/internal/application"
)

// APITokenRepository is the persistence capability required by the
// operator-managed API token use case. Plaintext tokens must never reach this
// layer; only their SHA-256 verifiers are stored.
type APITokenRepository interface {
	ListAPITokens(context.Context) ([]application.APIToken, error)
	GetAPITokenByHash(context.Context, []byte) (application.APIToken, error)
	CreateAPIToken(context.Context, application.APIToken, []byte) (application.APIToken, error)
	TouchAPITokenLastUsed(context.Context, int64, time.Time) error
	DeleteAPIToken(context.Context, int64) error
}

// APITokenApplicationCatalog is the managed-application capability used to
// verify that a token pins an existing application.
type APITokenApplicationCatalog interface {
	Get(context.Context, int64) (application.Application, error)
}

// APITokenService manages operator-issued machine credentials for external
// automation such as GitHub Actions. Token plaintext is generated with
// crypto/rand, hashed with SHA-256 for storage, and returned transiently at
// creation; only metadata and the verifier persist.
type APITokenService struct {
	repository   APITokenRepository
	applications APITokenApplicationCatalog

	mu sync.Mutex
}

// NewAPITokenService constructs the operator-managed API token service.
func NewAPITokenService(repository APITokenRepository, applications APITokenApplicationCatalog) (*APITokenService, error) {
	if repository == nil || applications == nil {
		return nil, errors.New("API token service dependencies are incomplete")
	}
	return &APITokenService{repository: repository, applications: applications}, nil
}

// List returns operator-managed API tokens in creation order.
func (s *APITokenService) List(ctx context.Context) ([]application.APIToken, error) {
	return s.repository.ListAPITokens(ctx)
}

// Create issues one API token for an existing application and returns the
// plaintext exactly once. Callers must render it immediately and never store
// or log it.
func (s *APITokenService) Create(ctx context.Context, input application.APITokenInput) (application.APITokenSetup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	displayName, err := application.ValidateAPITokenDisplayName(input.DisplayName)
	if err != nil {
		return application.APITokenSetup{}, err
	}
	if input.ApplicationID < 1 {
		return application.APITokenSetup{}, application.ErrAPITokenApplicationRequired
	}
	if _, err := s.applications.Get(ctx, input.ApplicationID); err != nil {
		if errors.Is(err, application.ErrNotFound) {
			return application.APITokenSetup{}, application.ErrAPITokenApplicationNotFound
		}
		return application.APITokenSetup{}, fmt.Errorf("get API token application: %w", err)
	}
	if err := application.ValidateAPITokenExpiry(input.ExpiresInDays); err != nil {
		return application.APITokenSetup{}, err
	}

	secret := make([]byte, application.APITokenSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return application.APITokenSetup{}, fmt.Errorf("generate API token secret: %w", err)
	}
	plaintext := application.FormatAPIToken(hex.EncodeToString(secret))
	sum := sha256.Sum256([]byte(plaintext))

	now := time.Now().UTC()
	item := application.APIToken{
		DisplayName:   displayName,
		Prefix:        plaintext[:application.APITokenPrefixLength],
		ApplicationID: input.ApplicationID,
		Scope:         application.APITokenScopeRun,
		CreatedAt:     now,
	}
	if input.ExpiresInDays > 0 {
		expires := now.Add(time.Duration(input.ExpiresInDays) * 24 * time.Hour)
		item.ExpiresAt = &expires
	}
	created, err := s.repository.CreateAPIToken(ctx, item, sum[:])
	if err != nil {
		return application.APITokenSetup{}, fmt.Errorf("persist API token: %w", err)
	}
	return application.APITokenSetup{Token: created, Plaintext: plaintext}, nil
}

// Revoke deletes one API token. Authentication looks the verifier up on every
// request, so revocation takes effect immediately.
func (s *APITokenService) Revoke(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.repository.DeleteAPIToken(ctx, id); err != nil {
		return err
	}
	return nil
}

// Authenticate verifies one presented token and returns its metadata. Unknown
// or malformed tokens report ErrAPITokenInvalid without disclosing which;
// expired tokens report ErrAPITokenExpired. Successful authentication records
// the last-use timestamp.
func (s *APITokenService) Authenticate(ctx context.Context, plaintext string) (application.APIToken, error) {
	normalized, ok := application.SplitAPIToken(plaintext)
	if !ok {
		return application.APIToken{}, application.ErrAPITokenInvalid
	}
	sum := sha256.Sum256([]byte(normalized))
	item, err := s.repository.GetAPITokenByHash(ctx, sum[:])
	if err != nil {
		if errors.Is(err, application.ErrAPITokenNotFound) {
			return application.APIToken{}, application.ErrAPITokenInvalid
		}
		return application.APIToken{}, fmt.Errorf("lookup API token: %w", err)
	}
	if item.Expired(time.Now().UTC()) {
		return application.APIToken{}, application.ErrAPITokenExpired
	}
	if item.Scope != application.APITokenScopeRun {
		return application.APIToken{}, application.ErrAPITokenScopeInvalid
	}
	now := time.Now().UTC()
	if err := s.repository.TouchAPITokenLastUsed(ctx, item.ID, now); err != nil {
		return application.APIToken{}, fmt.Errorf("record API token use: %w", err)
	}
	item.LastUsedAt = &now
	return item, nil
}
