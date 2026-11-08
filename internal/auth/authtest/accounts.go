package authtest

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

var _ ports.AccountAdminStore = (*Store)(nil)

var errBadLimit = errors.New("limit must be positive")

// Managing other people's accounts, following the Postgres adapter's contract: the same rules
// (not your own account, never the last active staff account), the same history lines, and
// switching an account off ends its sign-ins. A change and its history line happen under one
// lock, as they do in one transaction.

func (m *Store) event(userID string, actor domain.User, kind, detail string) {
	m.nextEventID++
	m.Events = append(m.Events, domain.UserEvent{
		ID: m.nextEventID, UserID: userID, ActorID: actor.ID, ActorEmail: actor.Email, Kind: kind, Detail: detail, At: time.Now(),
	})
}

func (m *Store) byID(id string) (*Stored, bool) {
	for _, s := range m.Users {
		if s.User.ID == id {
			return s, true
		}
	}
	return nil, false
}

func (m *Store) endSessions(userID string) {
	for h, s := range m.Sessions {
		if s.UserID == userID {
			delete(m.Sessions, h)
		}
	}
}

func (m *Store) ListUsers(_ context.Context, q ports.UserQuery) ([]domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.AdminErr != nil {
		return nil, m.AdminErr
	}
	m.LastUserQuery = q
	if q.Limit <= 0 {
		return nil, errBadLimit
	}
	var out []domain.User
	search := strings.ToLower(q.Search)
	for _, s := range m.Users {
		u := s.User
		switch {
		case q.Role != "" && u.Role != q.Role:
		case q.Status == "active" && u.Disabled():
		case q.Status == "disabled" && !u.Disabled():
		case search != "" && !strings.Contains(strings.ToLower(u.Email), search) && !strings.Contains(strings.ToLower(u.Name), search):
		default:
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	if out == nil {
		out = []domain.User{}
	}
	return out, nil
}

func (m *Store) CreateUserBy(_ context.Context, actor domain.User, u domain.User, hash string) (domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.AdminErr != nil {
		return domain.User{}, m.AdminErr
	}
	if _, taken := m.Users[u.Email]; taken {
		return domain.User{}, ports.ErrEmailTaken
	}
	u.CreatedAt = time.Now()
	m.Users[u.Email] = &Stored{User: u, Hash: hash}
	m.event(u.ID, actor, domain.UserCreated, "role="+string(u.Role))
	return u, nil
}

func (m *Store) SetUserDisabled(_ context.Context, actor domain.User, userID string, disabled bool) (domain.User, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.AdminErr != nil {
		return domain.User{}, false, m.AdminErr
	}
	if disabled && actor.ID == userID {
		return domain.User{}, false, ports.ErrOwnAccount
	}
	target, ok := m.byID(userID)
	if !ok {
		return domain.User{}, false, ports.ErrUserNotFound
	}
	if disabled == target.User.Disabled() {
		return target.User, false, nil
	}
	if disabled && target.User.Role == domain.RoleStaff {
		active := 0
		for _, s := range m.Users {
			if s.User.Role == domain.RoleStaff && !s.User.Disabled() {
				active++
			}
		}
		if active <= 1 {
			return domain.User{}, false, ports.ErrLastStaff
		}
	}
	kind := domain.UserEnabled
	if disabled {
		now := time.Now()
		target.User.DisabledAt = &now
		m.endSessions(userID)
		kind = domain.UserDisabled
	} else {
		target.User.DisabledAt = nil
	}
	m.event(userID, actor, kind, "")
	return target.User, true, nil
}

func (m *Store) ResetUserPassword(_ context.Context, actor domain.User, userID, hash string) (domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.AdminErr != nil {
		return domain.User{}, m.AdminErr
	}
	if actor.ID == userID {
		return domain.User{}, ports.ErrOwnAccount
	}
	target, ok := m.byID(userID)
	if !ok {
		return domain.User{}, ports.ErrUserNotFound
	}
	target.Hash = hash
	target.User.MustChangePassword = true
	m.endSessions(userID)
	m.event(userID, actor, domain.UserPasswordReset, "")
	return target.User, nil
}

func (m *Store) ChangePasswordHash(_ context.Context, userID, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	target, ok := m.byID(userID)
	if !ok {
		return ports.ErrUserNotFound
	}
	target.Hash = hash
	target.User.MustChangePassword = false
	m.event(userID, target.User, domain.UserPasswordChanged, "")
	return nil
}

func (m *Store) ListUserEvents(_ context.Context, userID string, limit int) ([]domain.UserEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.AdminErr != nil {
		return nil, m.AdminErr
	}
	if limit <= 0 {
		return nil, errBadLimit
	}
	out := []domain.UserEvent{}
	for i := len(m.Events) - 1; i >= 0 && len(out) < limit; i-- {
		if m.Events[i].UserID == userID {
			out = append(out, m.Events[i])
		}
	}
	return out, nil
}
