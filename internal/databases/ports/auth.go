package ports

import (
	"context"
	"errors"
	"time"

	"swiftdrop/internal/domain"
)

var (
	// ErrEmailTaken means an account with that email already exists.
	ErrEmailTaken = errors.New("an account with that email already exists")
	// ErrUserNotFound means there is no such account.
	ErrUserNotFound = errors.New("user not found")
	// ErrSessionNotFound means there is no such session (never made, signed out,
	// or cleaned up).
	ErrSessionNotFound = errors.New("session not found")
)

// AuthStore keeps accounts and sign-ins. Emails are compared in lower case: the
// caller passes them already normalised.
type AuthStore interface {
	// CreateUser stores a new account with the hash of its password.
	// ErrEmailTaken if the email is in use.
	CreateUser(ctx context.Context, u domain.User, passwordHash string) (domain.User, error)

	// GetUserByEmail returns an account and the hash of its password, or
	// ErrUserNotFound.
	GetUserByEmail(ctx context.Context, email string) (domain.User, string, error)

	// GetUserByID returns an account, or ErrUserNotFound.
	GetUserByID(ctx context.Context, id string) (domain.User, error)

	// UpdatePasswordHash replaces an account's password hash. ErrUserNotFound if
	// there is no such account.
	UpdatePasswordHash(ctx context.Context, userID, passwordHash string) error

	// CreateSession stores a sign-in.
	CreateSession(ctx context.Context, s domain.Session) error

	// GetSession returns the sign-in whose token hash this is, with its account,
	// or ErrSessionNotFound.
	GetSession(ctx context.Context, tokenHash string) (domain.Session, domain.User, error)

	// TouchSession records that a sign-in was used at the given time.
	TouchSession(ctx context.Context, sessionID string, at time.Time) error

	// DeleteSession ends one sign-in. Ending one that is already gone is not an
	// error.
	DeleteSession(ctx context.Context, sessionID string) error

	// DeleteUserSessions ends every sign-in of an account except the one named
	// (pass "" to end them all) and says how many it ended.
	DeleteUserSessions(ctx context.Context, userID, exceptSessionID string) (int64, error)

	// DeleteExpiredSessions removes sign-ins that can no longer be used: past
	// their end, or idle since before the given time. It says how many.
	DeleteExpiredSessions(ctx context.Context, now, idleBefore time.Time) (int64, error)
}
