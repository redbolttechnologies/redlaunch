package application

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// OAuthAccessTokenLifetime bounds each access token. Access tokens are
	// ordinary API tokens; clients refresh them before they expire.
	OAuthAccessTokenLifetime = time.Hour
	// OAuthRefreshIdleLifetime expires a refresh token that has not been used.
	OAuthRefreshIdleLifetime = 30 * 24 * time.Hour
	// OAuthRefreshAbsoluteLifetime forces a new sign-in regardless of use.
	OAuthRefreshAbsoluteLifetime = 90 * 24 * time.Hour
	// OAuthAuthorizationCodeLifetime bounds the time between approval and exchange.
	OAuthAuthorizationCodeLifetime = time.Minute
	// OAuthRefreshGracePeriod lets a just-rotated refresh token succeed once more,
	// so a lost response or a concurrent refresh does not revoke the grant.
	OAuthRefreshGracePeriod = time.Minute

	// OAuthRefreshTokenPrefix keeps refresh tokens distinct from API tokens.
	OAuthRefreshTokenPrefix = "rlrf_"
	// OAuthCodeChallengeLength is the length of an S256 code challenge (RFC 7636).
	OAuthCodeChallengeLength = 43
	// OAuthCodeChallengeMethodS256 is the only PKCE method accepted.
	OAuthCodeChallengeMethodS256 = "S256"
	// OAuthResponseTypeCode is the only response type accepted.
	OAuthResponseTypeCode = "code"

	OAuthGrantTypeAuthorizationCode = "authorization_code"
	OAuthGrantTypeRefreshToken      = "refresh_token"

	// OAuthManageResourcePath is the MCP endpoint for server-wide management.
	OAuthManageResourcePath = "/mcp/manage"
	// OAuthApplicationResourcePrefix is the prefix of per-application MCP endpoints.
	OAuthApplicationResourcePrefix = "/mcp/apps/"

	MaxOAuthClientNameLength  = 100
	MaxOAuthRedirectURIs      = 10
	MaxOAuthRedirectURILength = 2048
	MaxOAuthStateLength       = 512
)

// OAuth protocol error codes (RFC 6749 section 5.2, RFC 7591 section 3.2.2,
// RFC 8707 section 2).
const (
	OAuthErrorInvalidRequest          = "invalid_request"
	OAuthErrorInvalidClient           = "invalid_client"
	OAuthErrorInvalidGrant            = "invalid_grant"
	OAuthErrorInvalidScope            = "invalid_scope"
	OAuthErrorInvalidTarget           = "invalid_target"
	OAuthErrorInvalidRedirectURI      = "invalid_redirect_uri"
	OAuthErrorInvalidClientMetadata   = "invalid_client_metadata"
	OAuthErrorUnsupportedGrantType    = "unsupported_grant_type"
	OAuthErrorUnsupportedResponseType = "unsupported_response_type"
	OAuthErrorAccessDenied            = "access_denied"
	OAuthErrorTemporarilyUnavailable  = "temporarily_unavailable"
	OAuthErrorServerError             = "server_error"
)

var (
	ErrOAuthClientNotFound   = errors.New("OAuth client not found")
	ErrOAuthGrantNotFound    = errors.New("OAuth grant not found")
	ErrOAuthRotationConflict = errors.New("OAuth refresh token was rotated concurrently")
)

// OAuthError is a protocol error that may be shown to the client. Its fields
// never contain tokens, hashes, or storage details.
type OAuthError struct {
	Code        string
	Description string
}

func (e *OAuthError) Error() string {
	return e.Code + ": " + e.Description
}

// NewOAuthError returns a protocol error with a standard OAuth error code.
func NewOAuthError(code, description string) error {
	return &OAuthError{Code: code, Description: description}
}

// OAuthClient is a dynamically registered public client. Public clients hold
// no secret; PKCE protects the authorization code.
type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
}

// OAuthClientRegistration carries the metadata submitted to /oauth/register.
type OAuthClientRegistration struct {
	ClientName              string
	RedirectURIs            []string
	GrantTypes              []string
	ResponseTypes           []string
	TokenEndpointAuthMethod string
}

// OAuthResource is the MCP endpoint a token is bound to (RFC 8707).
type OAuthResource struct {
	URI           string
	Scope         string
	ApplicationID int64
}

// OAuthGrant ties a client's refresh token to the access token it currently
// holds. Only SHA-256 hashes of the tokens are stored.
type OAuthGrant struct {
	ID                       int64
	ClientID                 string
	DisplayName              string
	Resource                 string
	Scope                    string
	ApplicationID            int64
	AccessTokenID            int64
	RefreshTokenHash         []byte
	PreviousRefreshTokenHash []byte
	RotatedAt                time.Time
	CreatedAt                time.Time
	RefreshIdleExpiresAt     time.Time
	AbsoluteExpiresAt        time.Time
}

// OAuthGrantIssue is a new grant with its first access token.
type OAuthGrantIssue struct {
	Grant      OAuthGrant
	Access     APIToken
	AccessHash []byte
}

// OAuthGrantRotation replaces a grant's access and refresh tokens. The
// expected refresh hash makes the update a compare-and-swap, so two concurrent
// refreshes cannot both succeed from the same state.
type OAuthGrantRotation struct {
	GrantID              int64
	ExpectedRefreshHash  []byte
	Access               APIToken
	AccessHash           []byte
	RefreshHash          []byte
	RotatedAt            time.Time
	RefreshIdleExpiresAt time.Time
}

// OAuthAuthorizationRequest holds the parameters of an authorization request.
type OAuthAuthorizationRequest struct {
	ClientID            string
	RedirectURI         string
	ResponseType        string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string
	Scope               string
}

// OAuthAuthorization is an authorization request that passed validation.
type OAuthAuthorization struct {
	Client          OAuthClient
	Resource        OAuthResource
	ApplicationName string
	RedirectURI     string
	State           string
	CodeChallenge   string
}

// OAuthTokenRequest holds the parameters of a token request.
type OAuthTokenRequest struct {
	GrantType    string
	ClientID     string
	Code         string
	RedirectURI  string
	CodeVerifier string
	RefreshToken string
	Resource     string
}

// OAuthTokenResponse is a successful token response (RFC 6749 section 5.1).
type OAuthTokenResponse struct {
	AccessToken  string
	TokenType    string
	ExpiresIn    int64
	RefreshToken string
	Scope        string
}

// OAuthConnection is safe connection metadata for Settings. Token values,
// hashes, and redirect URIs are deliberately absent from this projection.
type OAuthConnection struct {
	ID            int64
	ClientID      string
	ClientName    string
	Resource      string
	Scope         string
	ApplicationID int64
	CreatedAt     time.Time
	LastUsedAt    *time.Time
	ExpiresAt     time.Time
	Expired       bool
}

// ValidateOAuthClientName trims a client's display name. A missing name falls
// back to a generic label so minimal clients can still register.
func ValidateOAuthClientName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "MCP client", nil
	}
	if utf8.RuneCountInString(name) > MaxOAuthClientNameLength {
		return "", NewOAuthError(OAuthErrorInvalidClientMetadata, "client_name is too long")
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return "", NewOAuthError(OAuthErrorInvalidClientMetadata, "client_name contains control characters")
		}
	}
	return name, nil
}

// ValidateOAuthRedirectURI accepts HTTPS URIs and loopback HTTP URIs (RFC 8252).
func ValidateOAuthRedirectURI(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > MaxOAuthRedirectURILength || strings.Contains(value, "#") {
		return "", NewOAuthError(OAuthErrorInvalidRedirectURI, "redirect_uri is missing, too long, or contains a fragment")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil {
		return "", NewOAuthError(OAuthErrorInvalidRedirectURI, "redirect_uri must be an absolute URI")
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !IsLoopbackHost(parsed.Hostname()) {
			return "", NewOAuthError(OAuthErrorInvalidRedirectURI, "http redirect URIs must use a loopback address")
		}
	default:
		return "", NewOAuthError(OAuthErrorInvalidRedirectURI, "redirect_uri must use https or a loopback http address")
	}
	return value, nil
}

// IsLoopbackHost reports whether host names this machine: localhost or a
// loopback IP literal.
func IsLoopbackHost(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// OAuthRedirectURIMatches reports whether requested is one of the registered
// redirect URIs. Loopback URIs match on any port (RFC 8252 section 7.3); all
// other URIs must match exactly.
func OAuthRedirectURIMatches(registered []string, requested string) bool {
	for _, candidate := range registered {
		if candidate == requested || loopbackRedirectURIMatches(candidate, requested) {
			return true
		}
	}
	return false
}

func loopbackRedirectURIMatches(registered, requested string) bool {
	if strings.Contains(requested, "#") {
		return false
	}
	left, err := url.Parse(registered)
	if err != nil {
		return false
	}
	right, err := url.Parse(requested)
	if err != nil || right.User != nil {
		return false
	}
	if left.Scheme != "http" || right.Scheme != "http" || !IsLoopbackHost(left.Hostname()) || !IsLoopbackHost(right.Hostname()) {
		return false
	}
	return strings.EqualFold(left.Hostname(), right.Hostname()) && left.Path == right.Path && left.RawQuery == right.RawQuery
}

// OAuthManageResource returns the management endpoint for an origin.
func OAuthManageResource(origin string) string {
	return origin + OAuthManageResourcePath
}

// OAuthApplicationResource returns the run endpoint for one application.
func OAuthApplicationResource(origin string, applicationID int64) string {
	return origin + OAuthApplicationResourcePrefix + strconv.FormatInt(applicationID, 10)
}

// ParseOAuthResource accepts only the endpoints served by origin, so each
// token is bound to exactly one of them.
func ParseOAuthResource(origin, value string) (OAuthResource, error) {
	if value == origin+OAuthManageResourcePath {
		return OAuthResource{URI: value, Scope: APITokenScopeManage}, nil
	}
	if text, ok := strings.CutPrefix(value, origin+OAuthApplicationResourcePrefix); ok {
		id, err := strconv.ParseInt(text, 10, 64)
		if err == nil && id > 0 && strconv.FormatInt(id, 10) == text {
			return OAuthResource{URI: value, Scope: APITokenScopeRun, ApplicationID: id}, nil
		}
	}
	return OAuthResource{}, NewOAuthError(OAuthErrorInvalidTarget, "resource must be this server's management or application MCP endpoint")
}

// ValidateOAuthScope accepts an omitted scope or the one scope the resource implies.
func ValidateOAuthScope(requested string, resource OAuthResource) error {
	fields := strings.Fields(requested)
	if len(fields) == 0 || (len(fields) == 1 && fields[0] == resource.Scope) {
		return nil
	}
	return NewOAuthError(OAuthErrorInvalidScope, "scope must match the requested resource")
}

// ValidOAuthCodeChallenge checks the shape of an S256 code challenge.
func ValidOAuthCodeChallenge(challenge string) bool {
	return len(challenge) == OAuthCodeChallengeLength && isUnreserved(challenge)
}

// ValidOAuthCodeVerifier checks the shape of a PKCE code verifier (RFC 7636 section 4.1).
func ValidOAuthCodeVerifier(verifier string) bool {
	return len(verifier) >= 43 && len(verifier) <= 128 && isUnreserved(verifier)
}

// VerifyOAuthPKCE reports whether verifier matches the S256 challenge.
func VerifyOAuthPKCE(verifier, challenge string) bool {
	if !ValidOAuthCodeVerifier(verifier) || !ValidOAuthCodeChallenge(challenge) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(expected), []byte(challenge)) == 1
}

func isUnreserved(value string) bool {
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9':
		case character == '-', character == '.', character == '_', character == '~':
		default:
			return false
		}
	}
	return true
}

// FormatOAuthRefreshToken renders a refresh token from its secret hex encoding.
func FormatOAuthRefreshToken(secretHex string) string {
	return OAuthRefreshTokenPrefix + secretHex
}

// SplitOAuthRefreshToken validates the shape of a presented refresh token
// without touching storage.
func SplitOAuthRefreshToken(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	secret, ok := strings.CutPrefix(trimmed, OAuthRefreshTokenPrefix)
	if !ok || len(secret) != APITokenSecretBytes*2 {
		return "", false
	}
	if _, err := hex.DecodeString(secret); err != nil {
		return "", false
	}
	return trimmed, true
}
