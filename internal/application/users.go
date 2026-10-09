package application

import "errors"

var (
	ErrLastUser     = errors.New("the last user cannot be deleted")
	ErrUserNotFound = ErrLocalUserNotFound
	ErrUserExists   = ErrLocalUserExists
)

// UserAccount contains only metadata safe to display in Settings.
type UserAccount struct {
	ID             int64
	Email          string
	LegacyUsername string
	HasPassword    bool
	CreatedAt      string
}
