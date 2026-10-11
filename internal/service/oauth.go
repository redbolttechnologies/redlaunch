package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"redlaunch/internal/application"
)

const (
	oauthMaxRegisteredClients = 500
	oauthUnusedClientLifetime = 24 * time.Hour
	oauthMaxPendingCodes      = 256
	oauthMaxReplays           = 256
)

// OAuthRepository persists dynamically registered clients and grants.
type OAuthRepository interface {
	CreateOAuthClient(context.Context, application.OAuthClient) error
	GetOAuthClient(context.Context, string) (application.OAuthClient, error)
	CountOAuthClients(context.Context) (int, error)
	DeleteUnusedOAuthClients(context.Context, time.Time) error
	CreateOAuthGrant(context.Context, application.OAuthGrantIssue) (application.OAuthGrant, application.APIToken, error)
	GetOAuthGrantByRefreshHash(context.Context, []byte) (application.OAuthGrant, error)
	GetOAuthGrantBySupersededRefreshHash(context.Context, []byte) (application.OAuthGrant, error)
	GetOAuthGrantByAccessTokenHash(context.Context, []byte) (application.OAuthGrant, error)
	RotateOAuthGrant(context.Context, application.OAuthGrantRotation) (application.APIToken, error)
	RevokeOAuthGrant(context.Context, int64) error
}

// OAuthApplicationCatalog verifies that an application-scoped resource exists.
type OAuthApplicationCatalog interface {
	Get(context.Context, int64) (application.Application, error)
}

// OAuthService is the authorization server for MCP clients. Clients register
// dynamically, the owner approves a resource in the browser, and the client
// exchanges a PKCE-protected code for access and refresh tokens. Access tokens
// are ordinary API tokens, so the existing authentication and scope checks
// apply unchanged.
type OAuthService struct {
	repository   OAuthRepository
	applications OAuthApplicationCatalog
	now          func() time.Time

	mu    sync.Mutex
	codes map[[sha256.Size]byte]pendingOAuthCode

	// refreshMu serializes refreshes so parallel requests presenting the same
	// token rotate once; the others replay that rotation's result. replays is
	// guarded by refreshMu and lives in memory only, never in the database.
	refreshMu sync.Mutex
	replays   map[string]oauthReplay
}

type pendingOAuthCode struct {
	authorization application.OAuthAuthorization
	expiresAt     time.Time
}

type oauthReplay struct {
	response  application.OAuthTokenResponse
	expiresAt time.Time
}

// NewOAuthService constructs the authorization server.
func NewOAuthService(repository OAuthRepository, applications OAuthApplicationCatalog) (*OAuthService, error) {
	if repository == nil || applications == nil {
		return nil, errors.New("OAuth service dependencies are incomplete")
	}
	return &OAuthService{
		repository:   repository,
		applications: applications,
		now:          time.Now,
		codes:        make(map[[sha256.Size]byte]pendingOAuthCode),
		replays:      make(map[string]oauthReplay),
	}, nil
}

// RegisterClient records a public client (RFC 7591). Clients that never
// received a grant are pruned, and the total is capped, so unauthenticated
// registration cannot grow the database without bound.
func (s *OAuthService) RegisterClient(ctx context.Context, input application.OAuthClientRegistration) (application.OAuthClient, error) {
	name, err := application.ValidateOAuthClientName(input.ClientName)
	if err != nil {
		return application.OAuthClient{}, err
	}
	if len(input.RedirectURIs) == 0 || len(input.RedirectURIs) > application.MaxOAuthRedirectURIs {
		return application.OAuthClient{}, application.NewOAuthError(application.OAuthErrorInvalidRedirectURI, "register between 1 and 10 redirect URIs")
	}
	redirectURIs := make([]string, 0, len(input.RedirectURIs))
	for _, value := range input.RedirectURIs {
		uri, err := application.ValidateOAuthRedirectURI(value)
		if err != nil {
			return application.OAuthClient{}, err
		}
		redirectURIs = append(redirectURIs, uri)
	}
	if !oauthValuesAllowed(input.GrantTypes, application.OAuthGrantTypeAuthorizationCode, application.OAuthGrantTypeRefreshToken) {
		return application.OAuthClient{}, application.NewOAuthError(application.OAuthErrorInvalidClientMetadata, "grant_types must be authorization_code or refresh_token")
	}
	if !oauthValuesAllowed(input.ResponseTypes, application.OAuthResponseTypeCode) {
		return application.OAuthClient{}, application.NewOAuthError(application.OAuthErrorInvalidClientMetadata, "response_types must be code")
	}
	if input.TokenEndpointAuthMethod != "" && input.TokenEndpointAuthMethod != "none" {
		return application.OAuthClient{}, application.NewOAuthError(application.OAuthErrorInvalidClientMetadata, "token_endpoint_auth_method must be none")
	}

	now := s.now().UTC()
	if err := s.repository.DeleteUnusedOAuthClients(ctx, now.Add(-oauthUnusedClientLifetime)); err != nil {
		return application.OAuthClient{}, fmt.Errorf("prune unused OAuth clients: %w", err)
	}
	count, err := s.repository.CountOAuthClients(ctx)
	if err != nil {
		return application.OAuthClient{}, fmt.Errorf("count OAuth clients: %w", err)
	}
	if count >= oauthMaxRegisteredClients {
		return application.OAuthClient{}, application.NewOAuthError(application.OAuthErrorTemporarilyUnavailable, "too many clients are registered; try again later")
	}
	id, err := newOAuthIdentifier("rlc_")
	if err != nil {
		return application.OAuthClient{}, err
	}
	client := application.OAuthClient{ID: id, Name: name, RedirectURIs: redirectURIs, CreatedAt: now}
	if err := s.repository.CreateOAuthClient(ctx, client); err != nil {
		return application.OAuthClient{}, fmt.Errorf("create OAuth client: %w", err)
	}
	return client, nil
}

// ValidateAuthorization checks an authorization request against the
// registered client, the PKCE challenge, and the requested resource. origin is
// the public origin this request was served from.
func (s *OAuthService) ValidateAuthorization(ctx context.Context, origin string, request application.OAuthAuthorizationRequest) (application.OAuthAuthorization, error) {
	client, err := s.repository.GetOAuthClient(ctx, request.ClientID)
	if errors.Is(err, application.ErrOAuthClientNotFound) {
		return application.OAuthAuthorization{}, application.NewOAuthError(application.OAuthErrorInvalidClient, "The client is not registered with this server.")
	}
	if err != nil {
		return application.OAuthAuthorization{}, fmt.Errorf("get OAuth client: %w", err)
	}
	if !application.OAuthRedirectURIMatches(client.RedirectURIs, request.RedirectURI) {
		return application.OAuthAuthorization{}, application.NewOAuthError(application.OAuthErrorInvalidRedirectURI, "redirect_uri is not registered for this client.")
	}
	if request.ResponseType != application.OAuthResponseTypeCode {
		return application.OAuthAuthorization{}, application.NewOAuthError(application.OAuthErrorUnsupportedResponseType, "response_type must be code.")
	}
	if request.CodeChallengeMethod != application.OAuthCodeChallengeMethodS256 || !application.ValidOAuthCodeChallenge(request.CodeChallenge) {
		return application.OAuthAuthorization{}, application.NewOAuthError(application.OAuthErrorInvalidRequest, "PKCE with an S256 code_challenge is required.")
	}
	if len(request.State) > application.MaxOAuthStateLength {
		return application.OAuthAuthorization{}, application.NewOAuthError(application.OAuthErrorInvalidRequest, "state is too long.")
	}
	resource, err := application.ParseOAuthResource(origin, request.Resource)
	if err != nil {
		return application.OAuthAuthorization{}, err
	}
	if err := application.ValidateOAuthScope(request.Scope, resource); err != nil {
		return application.OAuthAuthorization{}, err
	}
	authorization := application.OAuthAuthorization{
		Client:        client,
		Resource:      resource,
		RedirectURI:   request.RedirectURI,
		State:         request.State,
		CodeChallenge: request.CodeChallenge,
	}
	if resource.Scope == application.APITokenScopeRun {
		item, err := s.applications.Get(ctx, resource.ApplicationID)
		if errors.Is(err, application.ErrNotFound) {
			return application.OAuthAuthorization{}, application.NewOAuthError(application.OAuthErrorInvalidTarget, "The application does not exist.")
		}
		if err != nil {
			return application.OAuthAuthorization{}, fmt.Errorf("get OAuth application: %w", err)
		}
		authorization.ApplicationName = item.Name
	}
	return authorization, nil
}

// CompleteAuthorization records the owner's decision. Approval issues a
// single-use code; denial returns access_denied. Either way the client gets a
// redirect URL to follow.
func (s *OAuthService) CompleteAuthorization(ctx context.Context, origin string, request application.OAuthAuthorizationRequest, approved bool) (string, error) {
	authorization, err := s.ValidateAuthorization(ctx, origin, request)
	if err != nil {
		return "", err
	}
	if !approved {
		return oauthRedirectURL(authorization.RedirectURI, authorization.State, url.Values{
			"error":             {application.OAuthErrorAccessDenied},
			"error_description": {"The owner denied access."},
		})
	}
	code, err := newOAuthCode()
	if err != nil {
		return "", err
	}
	now := s.now()
	s.mu.Lock()
	for key, pending := range s.codes {
		if !now.Before(pending.expiresAt) {
			delete(s.codes, key)
		}
	}
	if len(s.codes) >= oauthMaxPendingCodes {
		s.mu.Unlock()
		return "", application.NewOAuthError(application.OAuthErrorTemporarilyUnavailable, "too many authorizations are pending; try again shortly.")
	}
	s.codes[sha256.Sum256([]byte(code))] = pendingOAuthCode{
		authorization: authorization,
		expiresAt:     now.Add(application.OAuthAuthorizationCodeLifetime),
	}
	s.mu.Unlock()
	return oauthRedirectURL(authorization.RedirectURI, authorization.State, url.Values{"code": {code}})
}

// ExchangeToken handles the authorization_code and refresh_token grants.
func (s *OAuthService) ExchangeToken(ctx context.Context, request application.OAuthTokenRequest) (application.OAuthTokenResponse, error) {
	switch request.GrantType {
	case application.OAuthGrantTypeAuthorizationCode:
		return s.exchangeAuthorizationCode(ctx, request)
	case application.OAuthGrantTypeRefreshToken:
		return s.refreshAccessToken(ctx, request)
	default:
		return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorUnsupportedGrantType, "grant_type must be authorization_code or refresh_token.")
	}
}

func (s *OAuthService) exchangeAuthorizationCode(ctx context.Context, request application.OAuthTokenRequest) (application.OAuthTokenResponse, error) {
	if request.Code == "" || request.ClientID == "" || request.RedirectURI == "" || request.CodeVerifier == "" {
		return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorInvalidRequest, "code, client_id, redirect_uri, and code_verifier are required.")
	}
	now := s.now()
	key := sha256.Sum256([]byte(request.Code))
	s.mu.Lock()
	pending, found := s.codes[key]
	// Codes are single-use even when the exchange below fails.
	delete(s.codes, key)
	s.mu.Unlock()
	if !found || !now.Before(pending.expiresAt) {
		return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorInvalidGrant, "The authorization code is invalid or has expired.")
	}
	authorization := pending.authorization
	if authorization.Client.ID != request.ClientID || authorization.RedirectURI != request.RedirectURI {
		return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorInvalidGrant, "The authorization code was issued to a different client or redirect_uri.")
	}
	if request.Resource != authorization.Resource.URI {
		return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorInvalidTarget, "resource must match the authorization request.")
	}
	if !application.VerifyOAuthPKCE(request.CodeVerifier, authorization.CodeChallenge) {
		return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorInvalidGrant, "PKCE verification failed.")
	}

	accessToken, accessHash, err := newOAuthAccessToken()
	if err != nil {
		return application.OAuthTokenResponse{}, err
	}
	refreshToken, refreshHash, err := newOAuthRefreshToken()
	if err != nil {
		return application.OAuthTokenResponse{}, err
	}
	absolute := now.Add(application.OAuthRefreshAbsoluteLifetime)
	idle := oauthEarlier(now.Add(application.OAuthRefreshIdleLifetime), absolute)
	expires := now.Add(application.OAuthAccessTokenLifetime)
	_, _, err = s.repository.CreateOAuthGrant(ctx, application.OAuthGrantIssue{
		Grant: application.OAuthGrant{
			ClientID:             authorization.Client.ID,
			DisplayName:          authorization.Client.Name,
			Resource:             authorization.Resource.URI,
			Scope:                authorization.Resource.Scope,
			ApplicationID:        authorization.Resource.ApplicationID,
			RefreshTokenHash:     refreshHash,
			CreatedAt:            now,
			RefreshIdleExpiresAt: idle,
			AbsoluteExpiresAt:    absolute,
		},
		Access: application.APIToken{
			DisplayName:   authorization.Client.Name,
			Prefix:        accessToken[:application.APITokenPrefixLength],
			ApplicationID: authorization.Resource.ApplicationID,
			Scope:         authorization.Resource.Scope,
			CreatedAt:     now,
			ExpiresAt:     &expires,
		},
		AccessHash: accessHash,
	})
	if err != nil {
		return application.OAuthTokenResponse{}, fmt.Errorf("create OAuth grant: %w", err)
	}
	return oauthTokenResponse(accessToken, refreshToken, authorization.Resource.Scope), nil
}

func (s *OAuthService) refreshAccessToken(ctx context.Context, request application.OAuthTokenRequest) (application.OAuthTokenResponse, error) {
	presented, ok := application.SplitOAuthRefreshToken(request.RefreshToken)
	if !ok {
		return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorInvalidGrant, "The refresh token is invalid.")
	}
	presentedHash := sha256Sum(presented)

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	grant, err := s.repository.GetOAuthGrantByRefreshHash(ctx, presentedHash)
	superseded := false
	if errors.Is(err, application.ErrOAuthGrantNotFound) {
		// Older superseded tokens still identify their grant, so reusing one
		// revokes it even after several rotations.
		grant, err = s.repository.GetOAuthGrantBySupersededRefreshHash(ctx, presentedHash)
		superseded = true
	}
	if errors.Is(err, application.ErrOAuthGrantNotFound) {
		return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorInvalidGrant, "The refresh token is invalid.")
	}
	if err != nil {
		return application.OAuthTokenResponse{}, fmt.Errorf("get OAuth grant: %w", err)
	}
	if request.ClientID != grant.ClientID {
		return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorInvalidGrant, "The refresh token was issued to a different client.")
	}
	if request.Resource != "" && request.Resource != grant.Resource {
		return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorInvalidTarget, "resource must match the grant.")
	}
	now := s.now()
	if !now.Before(grant.AbsoluteExpiresAt) || !now.Before(grant.RefreshIdleExpiresAt) {
		return s.rejectGrant(ctx, grant.ID, "The refresh token has expired. Sign in again.")
	}
	if superseded {
		return s.rejectGrant(ctx, grant.ID, "The refresh token was already used. Sign in again.")
	}
	if bytes.Equal(grant.RefreshTokenHash, presentedHash) {
		return s.rotateRefreshToken(ctx, grant, presentedHash, now)
	}

	// The presented token is the one this grant rotated most recently. Inside
	// the grace period the client gets the same pair that rotation produced,
	// which covers lost responses and parallel requests. Anywhere else, the
	// token was copied, so the grant is revoked.
	if replay, ok := s.replays[string(presentedHash)]; ok && !grant.RotatedAt.IsZero() && now.Before(replay.expiresAt) {
		return replay.response, nil
	}
	return s.rejectGrant(ctx, grant.ID, "The refresh token was already used. Sign in again.")
}

func (s *OAuthService) rotateRefreshToken(ctx context.Context, grant application.OAuthGrant, presentedHash []byte, now time.Time) (application.OAuthTokenResponse, error) {
	s.pruneReplays(now)
	if len(s.replays) >= oauthMaxReplays {
		return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorTemporarilyUnavailable, "too many refreshes are in progress; try again shortly.")
	}
	accessToken, accessHash, err := newOAuthAccessToken()
	if err != nil {
		return application.OAuthTokenResponse{}, err
	}
	refreshToken, refreshHash, err := newOAuthRefreshToken()
	if err != nil {
		return application.OAuthTokenResponse{}, err
	}
	expires := now.Add(application.OAuthAccessTokenLifetime)
	_, err = s.repository.RotateOAuthGrant(ctx, application.OAuthGrantRotation{
		GrantID:             grant.ID,
		ExpectedRefreshHash: grant.RefreshTokenHash,
		Access: application.APIToken{
			DisplayName:   grant.DisplayName,
			Prefix:        accessToken[:application.APITokenPrefixLength],
			ApplicationID: grant.ApplicationID,
			Scope:         grant.Scope,
			CreatedAt:     now,
			ExpiresAt:     &expires,
		},
		AccessHash:           accessHash,
		RefreshHash:          refreshHash,
		RotatedAt:            now,
		RefreshIdleExpiresAt: oauthEarlier(now.Add(application.OAuthRefreshIdleLifetime), grant.AbsoluteExpiresAt),
	})
	if errors.Is(err, application.ErrOAuthRotationConflict) {
		return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorInvalidGrant, "The refresh token was rotated concurrently. Try again.")
	}
	if err != nil {
		return application.OAuthTokenResponse{}, fmt.Errorf("rotate OAuth grant: %w", err)
	}
	response := oauthTokenResponse(accessToken, refreshToken, grant.Scope)
	s.replays[string(presentedHash)] = oauthReplay{response: response, expiresAt: now.Add(application.OAuthRefreshGracePeriod)}
	return response, nil
}

// pruneReplays drops rotations whose grace period has ended. Callers hold refreshMu.
func (s *OAuthService) pruneReplays(now time.Time) {
	for key, replay := range s.replays {
		if !now.Before(replay.expiresAt) {
			delete(s.replays, key)
		}
	}
}

// rejectGrant revokes a grant after misuse or expiry, then returns the
// invalid_grant error. A failed revocation is reported as a server error so
// that a copied token is never left active silently.
func (s *OAuthService) rejectGrant(ctx context.Context, grantID int64, description string) (application.OAuthTokenResponse, error) {
	if err := s.repository.RevokeOAuthGrant(ctx, grantID); err != nil && !errors.Is(err, application.ErrOAuthGrantNotFound) {
		return application.OAuthTokenResponse{}, fmt.Errorf("revoke OAuth grant: %w", err)
	}
	return application.OAuthTokenResponse{}, application.NewOAuthError(application.OAuthErrorInvalidGrant, description)
}

// RevokeToken ends the grant that owns a refresh or access token (RFC 7009).
// Unknown tokens succeed silently, so the endpoint cannot be used to probe
// which tokens are valid.
func (s *OAuthService) RevokeToken(ctx context.Context, token string) error {
	hash := sha256Sum(token)
	grant, err := s.repository.GetOAuthGrantByRefreshHash(ctx, hash)
	if errors.Is(err, application.ErrOAuthGrantNotFound) {
		grant, err = s.repository.GetOAuthGrantBySupersededRefreshHash(ctx, hash)
	}
	if errors.Is(err, application.ErrOAuthGrantNotFound) {
		grant, err = s.repository.GetOAuthGrantByAccessTokenHash(ctx, hash)
	}
	if errors.Is(err, application.ErrOAuthGrantNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get OAuth grant for revocation: %w", err)
	}
	if err := s.repository.RevokeOAuthGrant(ctx, grant.ID); err != nil && !errors.Is(err, application.ErrOAuthGrantNotFound) {
		return fmt.Errorf("revoke OAuth grant: %w", err)
	}
	return nil
}

func oauthValuesAllowed(values []string, allowed ...string) bool {
	for _, value := range values {
		found := false
		for _, candidate := range allowed {
			if value == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func oauthEarlier(first, second time.Time) time.Time {
	if first.Before(second) {
		return first
	}
	return second
}

func oauthRedirectURL(redirectURI, state string, params url.Values) (string, error) {
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return "", fmt.Errorf("parse registered redirect URI: %w", err)
	}
	query := parsed.Query()
	for key, values := range params {
		query[key] = values
	}
	if state != "" {
		query.Set("state", state)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func oauthTokenResponse(accessToken, refreshToken, scope string) application.OAuthTokenResponse {
	return application.OAuthTokenResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    int64(application.OAuthAccessTokenLifetime / time.Second),
		RefreshToken: refreshToken,
		Scope:        scope,
	}
}

func newOAuthAccessToken() (string, []byte, error) {
	secret, err := randomHex(application.APITokenSecretBytes)
	if err != nil {
		return "", nil, err
	}
	plaintext := application.FormatAPIToken(secret)
	return plaintext, sha256Sum(plaintext), nil
}

func newOAuthRefreshToken() (string, []byte, error) {
	secret, err := randomHex(application.APITokenSecretBytes)
	if err != nil {
		return "", nil, err
	}
	plaintext := application.FormatOAuthRefreshToken(secret)
	return plaintext, sha256Sum(plaintext), nil
}

func newOAuthIdentifier(prefix string) (string, error) {
	secret, err := randomHex(16)
	if err != nil {
		return "", err
	}
	return prefix + secret, nil
}

func newOAuthCode() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate OAuth code: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func randomHex(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate OAuth secret: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func sha256Sum(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}
