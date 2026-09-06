// Package auth implements the Google OAuth login flow and signed sessions.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"redlaunch/internal/application"
)

const (
	defaultUserInfoURL  = "https://openidconnect.googleapis.com/v1/userinfo"
	defaultSessionAge   = 12 * time.Hour
	maxUserInfoBodySize = 64 << 10
	maxSessionValueSize = 4 << 10
	maxUserNameSize     = 256
	maxPictureURLSize   = 1024
)

var defaultOAuthEndpoint = oauth2.Endpoint{
	AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:  "https://oauth2.googleapis.com/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

var (
	// ErrNotAuthorized means that Google authenticated the user, but the
	// account is not present in the local authorized-email allowlist.
	ErrNotAuthorized = errors.New("Google account is not authorized")
	// ErrEmailNotVerified means that Google did not report a verified email
	// address for the authenticated account.
	ErrEmailNotVerified = errors.New("Google email address is not verified")
)

// AuthorizedEmailStore is the persistence boundary needed by Service.
type AuthorizedEmailStore interface {
	IsAuthorizedEmail(context.Context, string) (bool, error)
}

// Config contains the credentials and session settings for Google login.
type Config struct {
	ClientID        string
	ClientSecret    string
	RedirectURL     string
	SessionSecret   string
	CookieSecure    bool
	SessionDuration time.Duration
	OAuthEndpoint   oauth2.Endpoint
	UserInfoURL     string
	HTTPClient      *http.Client
	Now             func() time.Time
}

// User contains the non-secret identity details needed by the application UI.
// Email is the only field used for authorization; the other fields are
// presentation data returned by the identity provider.
type User struct {
	Email      string
	Name       string
	PictureURL string
}

// Service performs Google OAuth exchanges and validates signed sessions.
type Service struct {
	oauthConfig     *oauth2.Config
	store           AuthorizedEmailStore
	sessionSecret   []byte
	cookieSecure    bool
	sessionDuration time.Duration
	userInfoURL     string
	httpClient      *http.Client
	now             func() time.Time
}

// New creates a Google authentication service.
func New(config Config, store AuthorizedEmailStore) (*Service, error) {
	if strings.TrimSpace(config.ClientID) == "" {
		return nil, errors.New("Google client ID is required")
	}
	if strings.TrimSpace(config.ClientSecret) == "" {
		return nil, errors.New("Google client secret is required")
	}
	if strings.TrimSpace(config.RedirectURL) == "" {
		return nil, errors.New("Google redirect URL is required")
	}
	redirectURLValue, err := parseRedirectURL(config.RedirectURL)
	if err != nil {
		return nil, err
	}
	if len(config.SessionSecret) < 32 {
		return nil, errors.New("authentication session secret must contain at least 32 bytes")
	}
	if store == nil {
		return nil, errors.New("authorized-email store is required")
	}

	endpoint := config.OAuthEndpoint
	if endpoint.AuthURL == "" {
		endpoint.AuthURL = defaultOAuthEndpoint.AuthURL
	}
	if endpoint.TokenURL == "" {
		endpoint.TokenURL = defaultOAuthEndpoint.TokenURL
	}
	if endpoint.AuthURL == "" || endpoint.TokenURL == "" {
		return nil, errors.New("Google OAuth endpoint is incomplete")
	}

	sessionDuration := config.SessionDuration
	if sessionDuration == 0 {
		sessionDuration = defaultSessionAge
	}
	if sessionDuration <= 0 {
		return nil, errors.New("authentication session duration must be positive")
	}

	userInfoURL := strings.TrimSpace(config.UserInfoURL)
	if userInfoURL == "" {
		userInfoURL = defaultUserInfoURL
	}
	parsedUserInfoURL, err := url.Parse(userInfoURL)
	if err != nil || parsedUserInfoURL.Scheme == "" || parsedUserInfoURL.Host == "" || parsedUserInfoURL.User != nil || (parsedUserInfoURL.Scheme != "http" && parsedUserInfoURL.Scheme != "https") {
		return nil, errors.New("Google user-info URL must be an absolute HTTP or HTTPS URL")
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}

	return &Service{
		oauthConfig: &oauth2.Config{
			ClientID:     strings.TrimSpace(config.ClientID),
			ClientSecret: config.ClientSecret,
			RedirectURL:  redirectURLValue,
			Endpoint:     endpoint,
			Scopes:       []string{"openid", "email", "profile"},
		},
		store:           store,
		sessionSecret:   []byte(config.SessionSecret),
		cookieSecure:    config.CookieSecure,
		sessionDuration: sessionDuration,
		userInfoURL:     userInfoURL,
		httpClient:      httpClient,
		now:             now,
	}, nil
}

// Enabled reports whether the service is configured for login.
func (s *Service) Enabled() bool {
	return s != nil && s.oauthConfig != nil
}

// AuthorizationURL builds the Google authorization URL for one login attempt.
func (s *Service) AuthorizationURL(state string) string {
	return s.AuthorizationURLForRedirect(state, "")
}

// AuthorizationURLForRedirect builds the Google authorization URL with an
// optional per-request redirect URL. An empty redirectURL uses the configured
// redirect URL.
func (s *Service) AuthorizationURLForRedirect(state, redirectURL string) string {
	oauthConfig, err := s.oauthConfigForRedirect(redirectURL)
	if err != nil {
		return ""
	}
	return oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

// CompleteLogin exchanges a Google authorization code, reads the verified
// Google account identity, and checks its email against the local allowlist.
func (s *Service) CompleteLogin(ctx context.Context, code string) (User, error) {
	return s.CompleteLoginForRedirect(ctx, code, "")
}

// CompleteLoginForRedirect completes a Google login using an optional
// per-request redirect URL. The redirect URL must match the one used to start
// the authorization request; an empty redirectURL uses the configured value.
func (s *Service) CompleteLoginForRedirect(ctx context.Context, code, redirectURL string) (User, error) {
	oauthConfig, err := s.oauthConfigForRedirect(redirectURL)
	if err != nil {
		return User{}, err
	}
	if !s.Enabled() {
		return User{}, errors.New("Google authentication is not configured")
	}
	if strings.TrimSpace(code) == "" {
		return User{}, errors.New("Google authorization code is required")
	}

	oauthContext := context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
	token, err := oauthConfig.Exchange(oauthContext, code)
	if err != nil {
		return User{}, fmt.Errorf("exchange Google authorization code: %w", err)
	}
	if !token.Valid() {
		return User{}, errors.New("Google returned an invalid access token")
	}

	request, err := http.NewRequestWithContext(oauthContext, http.MethodGet, s.userInfoURL, nil)
	if err != nil {
		return User{}, fmt.Errorf("create Google user-info request: %w", err)
	}
	token.SetAuthHeader(request)
	response, err := s.httpClient.Do(request)
	if err != nil {
		return User{}, fmt.Errorf("read Google user info: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return User{}, fmt.Errorf("Google user-info request returned HTTP %d", response.StatusCode)
	}

	var user userInfo
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxUserInfoBodySize))
	if err := decoder.Decode(&user); err != nil {
		return User{}, fmt.Errorf("decode Google user info: %w", err)
	}
	if !user.EmailVerified {
		return User{}, ErrEmailNotVerified
	}
	email, err := application.ValidateEmail(user.Email)
	if err != nil {
		return User{}, fmt.Errorf("validate Google email: %w", err)
	}
	authorized, err := s.store.IsAuthorizedEmail(ctx, email)
	if err != nil {
		return User{}, fmt.Errorf("check authorized email: %w", err)
	}
	if !authorized {
		return User{}, ErrNotAuthorized
	}
	return normalizeUser(User{
		Email:      email,
		Name:       user.Name,
		PictureURL: user.PictureURL,
	})
}

func (s *Service) oauthConfigForRedirect(redirectURL string) (*oauth2.Config, error) {
	if !s.Enabled() {
		return nil, errors.New("Google authentication is not configured")
	}
	redirectURL = strings.TrimSpace(redirectURL)
	if redirectURL == "" {
		return s.oauthConfig, nil
	}
	redirectURL, err := parseRedirectURL(redirectURL)
	if err != nil {
		return nil, err
	}
	config := *s.oauthConfig
	config.RedirectURL = redirectURL
	return &config, nil
}

func parseRedirectURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("Google redirect URL is required")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("Google redirect URL must be an absolute HTTP or HTTPS URL")
	}
	return value, nil
}

// NewSession creates a signed, expiring session value for an authorized user.
func (s *Service) NewSession(user User) (string, error) {
	if !s.Enabled() {
		return "", errors.New("Google authentication is not configured")
	}
	user, err := normalizeUser(user)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("create authentication session: %w", err)
	}
	payload := strings.Join([]string{
		"v2",
		encodeSessionPart(user.Email),
		encodeSessionPart(user.Name),
		encodeSessionPart(user.PictureURL),
		strconv.FormatInt(s.now().Add(s.sessionDuration).Unix(), 10),
		base64.RawURLEncoding.EncodeToString(nonce),
	}, "|")
	return encodeSignedValue(payload, s.sessionSecret), nil
}

// ValidateSession verifies a session signature, expiration, and current
// allowlist membership. A false result means the session is not valid.
func (s *Service) ValidateSession(ctx context.Context, value string) (User, bool, error) {
	if !s.Enabled() || len(value) == 0 || len(value) > maxSessionValueSize {
		return User{}, false, nil
	}
	payload, signature, ok := decodeSignedValue(value)
	if !ok || !hmac.Equal(signature, signValue(payload, s.sessionSecret)) {
		return User{}, false, nil
	}
	parts := strings.Split(payload, "|")
	user, expiresAt, ok := parseSessionUser(parts)
	if !ok {
		return User{}, false, nil
	}
	if s.now().Unix() >= expiresAt {
		return User{}, false, nil
	}
	authorized, err := s.store.IsAuthorizedEmail(ctx, user.Email)
	if err != nil {
		return User{}, false, fmt.Errorf("check authorized session email: %w", err)
	}
	if !authorized {
		return User{}, false, nil
	}
	return user, true, nil
}

// CookieSecure reports whether cookies should be marked Secure when the app
// is behind a TLS-terminating proxy.
func (s *Service) CookieSecure() bool {
	return s != nil && s.cookieSecure
}

// SessionDuration returns the configured session lifetime for cookie metadata.
func (s *Service) SessionDuration() time.Duration {
	if s == nil {
		return 0
	}
	return s.sessionDuration
}

type userInfo struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	PictureURL    string `json:"picture"`
}

func normalizeUser(user User) (User, error) {
	email, err := application.ValidateEmail(user.Email)
	if err != nil {
		return User{}, err
	}
	user.Email = email
	user.Name = strings.TrimSpace(user.Name)
	if user.Name == "" || len(user.Name) > maxUserNameSize {
		user.Name = fallbackUserName(email)
	}
	user.PictureURL = strings.TrimSpace(user.PictureURL)
	if len(user.PictureURL) > maxPictureURLSize {
		user.PictureURL = ""
	}
	return user, nil
}

func fallbackUserName(email string) string {
	localPart, _, ok := strings.Cut(email, "@")
	if !ok || localPart == "" {
		return email
	}
	localPart = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(localPart)
	words := strings.Fields(localPart)
	for index, word := range words {
		runes := []rune(word)
		if len(runes) == 0 {
			continue
		}
		runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
		words[index] = string(runes)
	}
	if len(words) == 0 {
		return email
	}
	return strings.Join(words, " ")
}

func encodeSessionPart(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeSessionPart(value string) (string, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return string(decoded), err == nil
}

func parseSessionUser(parts []string) (User, int64, bool) {
	switch parts[0] {
	case "v1":
		if len(parts) != 4 || parts[3] == "" {
			return User{}, 0, false
		}
		email, err := application.ValidateEmail(parts[1])
		if err != nil {
			return User{}, 0, false
		}
		expiresAt, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return User{}, 0, false
		}
		user, err := normalizeUser(User{Email: email})
		return user, expiresAt, err == nil
	case "v2":
		if len(parts) != 6 || parts[5] == "" {
			return User{}, 0, false
		}
		email, emailOK := decodeSessionPart(parts[1])
		name, nameOK := decodeSessionPart(parts[2])
		pictureURL, pictureOK := decodeSessionPart(parts[3])
		if !emailOK || !nameOK || !pictureOK {
			return User{}, 0, false
		}
		expiresAt, err := strconv.ParseInt(parts[4], 10, 64)
		if err != nil {
			return User{}, 0, false
		}
		user, err := normalizeUser(User{Email: email, Name: name, PictureURL: pictureURL})
		return user, expiresAt, err == nil
	default:
		return User{}, 0, false
	}
}

func encodeSignedValue(payload string, secret []byte) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(signValue(payload, secret))
}

func decodeSignedValue(value string) (string, []byte, bool) {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", nil, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", nil, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", nil, false
	}
	return string(decoded), signature, true
}

func signValue(value string, secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}
