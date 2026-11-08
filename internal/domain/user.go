package domain

import "time"

// Role says what a signed-in person may do. There are two: a customer, who can
// only see and act on their own orders, and staff, who run the kitchen and the
// delivery and see every order.
type Role string

const (
	RoleCustomer Role = "customer"
	RoleStaff    Role = "staff"
)

// Valid reports whether r is one of the roles.
func (r Role) Valid() bool { return r == RoleCustomer || r == RoleStaff }

// User is an account. The password is never part of it: only a slow salted hash
// is stored, and it is read through the auth store when a password is checked.
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  Role   `json:"role"`
	// DisabledAt is set when an account is switched off. Nil for a working account.
	DisabledAt *time.Time `json:"disabledAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	// MustChangePassword is set while the password is a temporary one that somebody else
	// chose and knows: the account can do nothing but replace it.
	MustChangePassword bool `json:"mustChangePassword,omitempty"`
}

// Disabled reports whether the account is switched off.
func (u User) Disabled() bool { return u.DisabledAt != nil }

// What was done to an account, for the history of it.
const (
	UserCreated         = "created"
	UserDisabled        = "disabled"
	UserEnabled         = "enabled"
	UserPasswordReset   = "password_reset"   // a staff member replaced it with a temporary one
	UserPasswordChanged = "password_changed" // the person chose their own
)

// UserEvent is one line of an account's history.
type UserEvent struct {
	ID         int64     `json:"id"`
	UserID     string    `json:"userId"`
	ActorID    string    `json:"actorId"`
	ActorEmail string    `json:"actorEmail"`
	Kind       string    `json:"kind"`
	Detail     string    `json:"detail,omitempty"`
	At         time.Time `json:"at"`
}

// Session is one sign-in. Only the hash of its secret token is stored, so a copy
// of the database cannot be used to act as anyone.
type Session struct {
	ID        string
	UserID    string
	TokenHash string // SHA-256 of the token the browser holds, hex
	// CSRFToken must come back in a header on every state-changing request, which
	// a page on another site cannot read or send.
	CSRFToken  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	// ExpiresAt is the latest moment the session can live, however active it is.
	ExpiresAt time.Time
}
