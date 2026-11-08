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
	// ErrOwnAccount means someone tried to do to their own account what only another
	// person may do to it (disable it, reset its password).
	ErrOwnAccount = errors.New("that cannot be done to your own account")
	// ErrLastStaff means the change would leave no active staff account, and with it
	// nobody who could manage the rest.
	ErrLastStaff = errors.New("that would leave no active staff account")
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

// UserQuery says which accounts to list. The zero value lists the newest.
type UserQuery struct {
	Role   domain.Role // "" for any
	Status string      // "", "active" or "disabled"
	Search string      // part of the email or the name, any case
	Limit  int         // at most this many; must be positive
}

// AccountAdminStore is what managing other people's accounts needs. Every change is one
// transaction that also writes the history line, so a change nobody can account for cannot
// happen, and each takes the account that is acting (id and email) for that line.
type AccountAdminStore interface {
	// ListUsers returns accounts, newest first.
	ListUsers(ctx context.Context, q UserQuery) ([]domain.User, error)

	// CreateUserBy stores a new account like CreateUser and records that actor made it.
	// ErrEmailTaken if the email is in use.
	CreateUserBy(ctx context.Context, actor domain.User, u domain.User, passwordHash string) (domain.User, error)

	// SetUserDisabled switches an account off or on. Switching it off also ends all its
	// sign-ins, in the same transaction. changed = false means it already was so: nothing
	// was written. ErrUserNotFound; ErrOwnAccount if actor is the account; ErrLastStaff if it
	// is the only active staff account. The last-staff rule holds even when two staff disable
	// each other at once: the active staff accounts are locked while it is checked.
	SetUserDisabled(ctx context.Context, actor domain.User, userID string, disabled bool) (user domain.User, changed bool, err error)

	// ResetUserPassword replaces an account's password hash with a temporary one, marks the
	// account as having to choose its own, and ends all its sign-ins. ErrUserNotFound;
	// ErrOwnAccount if actor is the account (they use the change-password form).
	ResetUserPassword(ctx context.Context, actor domain.User, userID, passwordHash string) (domain.User, error)

	// ChangePasswordHash is a person replacing their own password: it clears the
	// must-change mark and records it. ErrUserNotFound.
	ChangePasswordHash(ctx context.Context, userID, passwordHash string) error

	// ListUserEvents returns up to limit lines of an account's history, newest first.
	ListUserEvents(ctx context.Context, userID string, limit int) ([]domain.UserEvent, error)
}
