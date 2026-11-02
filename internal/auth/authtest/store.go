// Package authtest is an in-memory ports.AuthStore for tests. It follows the
// Postgres adapter's contract (unique emails, a session found by its token hash
// together with its account, ending a gone session is not an error), so a test
// that passes against it is a test of the real behaviour, and it lets the auth
// service and the HTTP handlers be tested without a database.
package authtest

import (
	"context"
	"sync"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

var _ ports.AuthStore = (*Store)(nil)

// Stored is an account with its password hash.
type Stored struct {
	User domain.User
	Hash string
}

// Store keeps accounts and sessions in memory. The Err fields make the matching
// call fail, to test what the callers do when storage breaks.
type Store struct {
	mu       sync.Mutex
	Users    map[string]*Stored // by email
	Sessions map[string]domain.Session

	CreateUserErr, GetUserErr, UpdateErr, CreateSessionErr, GetSessionErr,
	TouchErr, DeleteSessionErr, DeleteUserSessionsErr, DeleteExpiredErr error

	// Touches counts the calls to TouchSession.
	Touches int
}

// New returns an empty Store.
func New() *Store {
	return &Store{Users: map[string]*Stored{}, Sessions: map[string]domain.Session{}}
}

// SessionCount is how many sessions are stored.
func (m *Store) SessionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Sessions)
}

// FailDeleteExpired makes DeleteExpiredSessions fail from now on. It takes the lock,
// so it is safe to call while a background loop is using the store.
func (m *Store) FailDeleteExpired(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DeleteExpiredErr = err
}

func (m *Store) CreateUser(_ context.Context, u domain.User, hash string) (domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CreateUserErr != nil {
		return domain.User{}, m.CreateUserErr
	}
	if _, taken := m.Users[u.Email]; taken {
		return domain.User{}, ports.ErrEmailTaken
	}
	u.CreatedAt = time.Now()
	m.Users[u.Email] = &Stored{User: u, Hash: hash}
	return u, nil
}

func (m *Store) GetUserByEmail(_ context.Context, email string) (domain.User, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetUserErr != nil {
		return domain.User{}, "", m.GetUserErr
	}
	s, ok := m.Users[email]
	if !ok {
		return domain.User{}, "", ports.ErrUserNotFound
	}
	return s.User, s.Hash, nil
}

func (m *Store) GetUserByID(_ context.Context, id string) (domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.Users {
		if s.User.ID == id {
			return s.User, nil
		}
	}
	return domain.User{}, ports.ErrUserNotFound
}

func (m *Store) UpdatePasswordHash(_ context.Context, id, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	for _, s := range m.Users {
		if s.User.ID == id {
			s.Hash = hash
			return nil
		}
	}
	return ports.ErrUserNotFound
}

func (m *Store) CreateSession(_ context.Context, s domain.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CreateSessionErr != nil {
		return m.CreateSessionErr
	}
	m.Sessions[s.TokenHash] = s
	return nil
}

func (m *Store) GetSession(_ context.Context, tokenHash string) (domain.Session, domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetSessionErr != nil {
		return domain.Session{}, domain.User{}, m.GetSessionErr
	}
	s, ok := m.Sessions[tokenHash]
	if !ok {
		return domain.Session{}, domain.User{}, ports.ErrSessionNotFound
	}
	for _, u := range m.Users {
		if u.User.ID == s.UserID {
			return s, u.User, nil
		}
	}
	return domain.Session{}, domain.User{}, ports.ErrSessionNotFound
}

func (m *Store) TouchSession(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Touches++
	if m.TouchErr != nil {
		return m.TouchErr
	}
	for h, s := range m.Sessions {
		if s.ID == id {
			s.LastSeenAt = at
			m.Sessions[h] = s
		}
	}
	return nil
}

func (m *Store) DeleteSession(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DeleteSessionErr != nil {
		return m.DeleteSessionErr
	}
	for h, s := range m.Sessions {
		if s.ID == id {
			delete(m.Sessions, h)
		}
	}
	return nil
}

func (m *Store) DeleteUserSessions(_ context.Context, userID, except string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DeleteUserSessionsErr != nil {
		return 0, m.DeleteUserSessionsErr
	}
	var n int64
	for h, s := range m.Sessions {
		if s.UserID == userID && s.ID != except {
			delete(m.Sessions, h)
			n++
		}
	}
	return n, nil
}

func (m *Store) DeleteExpiredSessions(_ context.Context, now, idleBefore time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DeleteExpiredErr != nil {
		return 0, m.DeleteExpiredErr
	}
	var n int64
	for h, s := range m.Sessions {
		if !s.ExpiresAt.After(now) || s.LastSeenAt.Before(idleBefore) {
			delete(m.Sessions, h)
			n++
		}
	}
	return n, nil
}
