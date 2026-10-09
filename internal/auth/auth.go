// Package auth implements local and Google login with signed sessions.
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

// Config contains optional Google credentials and shared session settings.
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
// Username and CredentialVersion identify local users; Email identifies Google
// users. Password hashes are never included in browser sessions.
type User struct {
	AccountID         int64
	Provider          string
	Username          string
	CredentialVersion int64
	Email             string
	Name              string
	PictureURL        string
}

// Service authenticates local and Google users and validates signed sessions.
type Service struct {
	localStore      LocalUserStore
	dummyHash       string
	hashSlots       chan struct{}
	loginLimiter    loginLimiter
	oauthConfig     *oauth2.Config
	store           AuthorizedEmailStore
	sessionSecret   []byte
	cookieSecure    bool
	sessionDuration time.Duration
	userInfoURL     string
	httpClient      *http.Client
	now             func() time.Time
}

// New creates an authentication service with optional Google login.
func New(config Config, store AuthorizedEmailStore) (*Service, error) {
	googleConfigured := strings.TrimSpace(config.ClientID) != "" || strings.TrimSpace(config.ClientSecret) != ""
	var redirectURLValue string
	if googleConfigured {
		if strings.TrimSpace(config.ClientID) == "" || strings.TrimSpace(config.ClientSecret) == "" {
			return nil, errors.New("Google client ID and secret must be configured together")
		}
		var err error
		redirectURLValue, err = parseRedirectURL(config.RedirectURL)
		if err != nil {
			return nil, err
		}
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

	s := &Service{
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
	}
	if !googleConfigured {
		s.oauthConfig = nil
	}
	s.localStore, _ = store.(LocalUserStore)
	if s.localStore != nil {
		var err error
		s.dummyHash, err = HashPassword("dummy login credential")
		if err != nil {
			return nil, err
		}
		s.hashSlots = make(chan struct{}, 2)
		s.loginLimiter.entries = make(map[string]loginAttempts)
	}
	if !s.GoogleEnabled() && !s.LocalEnabled() {
		return nil, errors.New("authentication store is required")
	}
	return s, nil
}

// Enabled reports whether the service is configured for login.
func (s *Service) Enabled() bool {
	return s != nil && len(s.sessionSecret) >= 32
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
	if !s.GoogleEnabled() {
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
	identity := User{Email: email, Name: user.Name, PictureURL: user.PictureURL}
	if _, ok := s.localStore.(interface {
		GetUserByID(context.Context, int64) (application.LocalUser, error)
	}); ok {
		account, err := s.localStore.GetLocalUser(ctx, email)
		if err != nil {
			return User{}, ErrNotAuthorized
		}
		identity.AccountID = account.ID
		identity.CredentialVersion = account.CredentialVersion
		identity.Provider = "google"
	}
	return normalizeUser(identity)
}

func (s *Service) oauthConfigForRedirect(redirectURL string) (*oauth2.Config, error) {
	if !s.GoogleEnabled() {
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
		return "", errors.New("authentication is not configured")
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
	if user.Username != "" {
		payload = strings.Join([]string{"local-v1", encodeSessionPart(user.Username), strconv.FormatInt(user.CredentialVersion, 10), strconv.FormatInt(s.now().Add(s.sessionDuration).Unix(), 10), base64.RawURLEncoding.EncodeToString(nonce)}, "|")
	}
	if user.AccountID > 0 {
		payload = strings.Join([]string{"user-v1", strconv.FormatInt(user.AccountID, 10), user.Provider, strconv.FormatInt(user.CredentialVersion, 10), encodeSessionPart(user.Email), encodeSessionPart(user.Name), encodeSessionPart(user.PictureURL), strconv.FormatInt(s.now().Add(s.sessionDuration).Unix(), 10), base64.RawURLEncoding.EncodeToString(nonce)}, "|")
	}
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
	if user.AccountID > 0 {
		repository, ok := s.localStore.(interface {
			GetUserByID(context.Context, int64) (application.LocalUser, error)
		})
		if !ok {
			return User{}, false, nil
		}
		account, err := repository.GetUserByID(ctx, user.AccountID)
		if errors.Is(err, application.ErrUserNotFound) {
			return User{}, false, nil
		}
		if err != nil {
			return User{}, false, errors.New("could not check session account")
		}
		if account.CredentialVersion != user.CredentialVersion || account.Email != user.Email || (user.Provider == "google" && !s.GoogleEnabled()) || (user.Provider == "password" && account.PasswordHash == "") {
			return User{}, false, nil
		}
		return user, true, nil
	}
	if user.Username != "" {
		if !s.LocalEnabled() {
			return User{}, false, nil
		}
		current, err := s.localStore.GetLocalUser(ctx, user.Username)
		if errors.Is(err, application.ErrLocalUserNotFound) {
			return User{}, false, nil
		}
		if err != nil {
			return User{}, false, errors.New("could not check session account")
		}
		return user, current.CredentialVersion == user.CredentialVersion, nil
	}
	if _, ok := s.localStore.(interface {
		GetUserByID(context.Context, int64) (application.LocalUser, error)
	}); ok {
		return User{}, false, nil
	}
	if !s.GoogleEnabled() {
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
	if user.AccountID > 0 {
		if user.CredentialVersion < 1 || (user.Provider != "password" && user.Provider != "google") {
			return User{}, errors.New("invalid account identity")
		}
		if user.Email != "" {
			email, err := application.ValidateEmail(user.Email)
			if err != nil {
				return User{}, err
			}
			user.Email = email
			user.Username = ""
			if user.Name == "" {
				user.Name = email
			}
		}
		if user.Email == "" {
			if user.Provider != "password" {
				return User{}, errors.New("email is required")
			}
			name, err := application.ValidateUsername(user.Username)
			if err != nil {
				return User{}, err
			}
			user.Username = name
			user.Name = name
		}
		if len(user.Name) > maxUserNameSize {
			user.Name = user.Email
		}
		if len(user.PictureURL) > maxPictureURLSize {
			user.PictureURL = ""
		}
		return user, nil
	}

	if user.Username != "" {
		username, err := application.ValidateUsername(user.Username)
		if err != nil || user.Email != "" || user.CredentialVersion < 1 {
			return User{}, errors.New("invalid local identity")
		}
		return User{Username: username, Name: username, CredentialVersion: user.CredentialVersion}, nil
	}
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
	if len(parts) == 0 {
		return User{}, 0, false
	}
	switch parts[0] {
	case "user-v1":
		if len(parts) != 9 || parts[8] == "" {
			return User{}, 0, false
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id < 1 {
			return User{}, 0, false
		}
		version, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			return User{}, 0, false
		}
		email, ok1 := decodeSessionPart(parts[4])
		name, ok2 := decodeSessionPart(parts[5])
		picture, ok3 := decodeSessionPart(parts[6])
		if !ok1 || !ok2 || !ok3 {
			return User{}, 0, false
		}
		expires, err := strconv.ParseInt(parts[7], 10, 64)
		if err != nil {
			return User{}, 0, false
		}
		user, err := normalizeUser(User{AccountID: id, Provider: parts[2], CredentialVersion: version, Email: email, Name: name, Username: name, PictureURL: picture})
		return user, expires, err == nil
	case "local-v1":
		if len(parts) != 5 || parts[4] == "" {
			return User{}, 0, false
		}
		username, ok := decodeSessionPart(parts[1])
		version, err := strconv.ParseInt(parts[2], 10, 64)
		if !ok || err != nil {
			return User{}, 0, false
		}
		expires, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			return User{}, 0, false
		}
		user, err := normalizeUser(User{Username: username, CredentialVersion: version})
		return user, expires, err == nil
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
