package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	"redlaunch/internal/application"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrLoginThrottled     = errors.New("too many login attempts")
)

type LocalUserStore interface {
	GetLocalUser(context.Context, string) (application.LocalUser, error)
}

// loginLimiter bounds attempts and memory. Entries expire after one minute;
// forwarded IP headers are deliberately not trusted by the HTTP adapter.
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]loginAttempts
}
type loginAttempts struct {
	count   int
	expires time.Time
}

func (l *loginLimiter) allow(now time.Time, username, address string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, attempts := range l.entries {
		if !now.Before(attempts.expires) {
			delete(l.entries, key)
		}
	}
	keys := []string{"user:" + username, "ip:" + address}
	limits := []int{5, 20}
	missing := 0
	for i, key := range keys {
		if attempts, ok := l.entries[key]; ok {
			if attempts.count >= limits[i] {
				return false
			}
		} else {
			missing++
		}
	}
	if len(l.entries)+missing > 4096 {
		return false
	}
	for _, key := range keys {
		attempts := l.entries[key]
		if attempts.count == 0 {
			attempts.expires = now.Add(time.Minute)
		}
		attempts.count++
		l.entries[key] = attempts
	}
	return true
}

func (s *Service) LocalEnabled() bool  { return s != nil && s.localStore != nil }
func (s *Service) GoogleEnabled() bool { return s != nil && s.oauthConfig != nil }

// AuthenticatePassword checks credentials without revealing account existence.
func (s *Service) AuthenticatePassword(ctx context.Context, username, password, address string) (User, error) {
	if !s.LocalEnabled() {
		return User{}, ErrInvalidCredentials
	}
	identifier := username
	username, err := application.ValidateEmail(identifier)
	if err != nil { // Existing username-only accounts can sign in until migrated.
		username, err = application.ValidateUsername(identifier)
		if err != nil {
			username = ""
		}
	}
	if !s.loginLimiter.allow(s.now(), username, address) {
		return User{}, ErrLoginThrottled
	}
	select {
	case s.hashSlots <- struct{}{}:
		defer func() { <-s.hashSlots }()
	default:
		return User{}, ErrLoginThrottled
	}
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	user, err := s.localStore.GetLocalUser(ctx, username)
	if err != nil && !errors.Is(err, application.ErrLocalUserNotFound) {
		return User{}, errors.New("could not check login credentials")
	}
	hash := user.PasswordHash
	if err != nil || hash == "" {
		hash = s.dummyHash
	}
	valid := verifyPassword(password, hash)
	if !valid || err != nil || user.PasswordHash == "" {
		return User{}, ErrInvalidCredentials
	}
	identity := User{AccountID: user.ID, Email: user.Email, Username: user.Username, Name: user.Email, Provider: "password", CredentialVersion: user.CredentialVersion}
	if user.ID == 0 {
		identity.Provider = ""
	}
	return normalizeUser(identity)
}
