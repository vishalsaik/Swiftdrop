package adapters

import (
	"context"
	"errors"
	"fmt"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ ports.AuthStore = (*DBAdapter)(nil)

const userColumns = `id, email, name, role, disabled_at, created_at, must_change_password`

func scanUser(row pgx.Row, extra ...any) (domain.User, error) {
	var u domain.User
	dest := append([]any{&u.ID, &u.Email, &u.Name, &u.Role, &u.DisabledAt, &u.CreatedAt, &u.MustChangePassword}, extra...)
	err := row.Scan(dest...)
	return u, err
}

func (db *DBAdapter) CreateUser(ctx context.Context, u domain.User, passwordHash string) (domain.User, error) {
	if u.ID == "" || u.Email == "" || u.Name == "" || passwordHash == "" || !u.Role.Valid() {
		return domain.User{}, errors.New("failed to create user: id, email, name, role and password hash are required")
	}
	created, err := scanUser(db.Pool.QueryRow(ctx, `
		INSERT INTO users (id, email, name, role, password_hash, must_change_password) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+userColumns, u.ID, u.Email, u.Name, string(u.Role), passwordHash, u.MustChangePassword))
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return created, nil
	case errors.As(err, &pgErr) && pgErr.Code == uniqueViolation:
		return domain.User{}, ports.ErrEmailTaken
	default:
		return domain.User{}, fmt.Errorf("failed to create user: %w", err)
	}
}

func (db *DBAdapter) GetUserByEmail(ctx context.Context, email string) (domain.User, string, error) {
	var hash string
	u, err := scanUser(db.Pool.QueryRow(ctx, `SELECT `+userColumns+`, password_hash FROM users WHERE email = $1`, email), &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, "", ports.ErrUserNotFound
	}
	if err != nil {
		return domain.User{}, "", fmt.Errorf("failed to fetch user: %w", err)
	}
	return u, hash, nil
}

func (db *DBAdapter) GetUserByID(ctx context.Context, id string) (domain.User, error) {
	u, err := scanUser(db.Pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ports.ErrUserNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("failed to fetch user: %w", err)
	}
	return u, nil
}

func (db *DBAdapter) UpdatePasswordHash(ctx context.Context, userID, passwordHash string) error {
	if passwordHash == "" {
		return errors.New("failed to update password: the hash is required")
	}
	tag, err := db.Pool.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, passwordHash)
	if err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrUserNotFound
	}
	return nil
}

func (db *DBAdapter) CreateSession(ctx context.Context, s domain.Session) error {
	if s.ID == "" || s.UserID == "" || s.TokenHash == "" || s.CSRFToken == "" || !s.ExpiresAt.After(s.CreatedAt) {
		return errors.New("failed to create session: id, user, token hash, csrf token and an end after the start are required")
	}
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, csrf_token, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.ID, s.UserID, s.TokenHash, s.CSRFToken, s.CreatedAt, s.LastSeenAt, s.ExpiresAt)
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	return nil
}

func (db *DBAdapter) GetSession(ctx context.Context, tokenHash string) (domain.Session, domain.User, error) {
	var s domain.Session
	var u domain.User
	err := db.Pool.QueryRow(ctx, `
		SELECT s.id, s.user_id, s.token_hash, s.csrf_token, s.created_at, s.last_seen_at, s.expires_at,
		       u.id, u.email, u.name, u.role, u.disabled_at, u.created_at, u.must_change_password
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1`, tokenHash).Scan(
		&s.ID, &s.UserID, &s.TokenHash, &s.CSRFToken, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt,
		&u.ID, &u.Email, &u.Name, &u.Role, &u.DisabledAt, &u.CreatedAt, &u.MustChangePassword)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, domain.User{}, ports.ErrSessionNotFound
	}
	if err != nil {
		return domain.Session{}, domain.User{}, fmt.Errorf("failed to fetch session: %w", err)
	}
	return s, u, nil
}

func (db *DBAdapter) TouchSession(ctx context.Context, sessionID string, at time.Time) error {
	// Never backwards: two servers writing "last seen" at once must not make it older.
	if _, err := db.Pool.Exec(ctx, `UPDATE sessions SET last_seen_at = GREATEST(last_seen_at, $2) WHERE id = $1`, sessionID, at); err != nil {
		return fmt.Errorf("failed to update session: %w", err)
	}
	return nil
}

func (db *DBAdapter) DeleteSession(ctx context.Context, sessionID string) error {
	if _, err := db.Pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID); err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

func (db *DBAdapter) DeleteUserSessions(ctx context.Context, userID, exceptSessionID string) (int64, error) {
	tag, err := db.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND id <> $2`, userID, exceptSessionID)
	if err != nil {
		return 0, fmt.Errorf("failed to delete sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (db *DBAdapter) DeleteExpiredSessions(ctx context.Context, now, idleBefore time.Time) (int64, error) {
	tag, err := db.Pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= $1 OR last_seen_at < $2`, now, idleBefore)
	if err != nil {
		return 0, fmt.Errorf("failed to delete expired sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
