package adapters

import (
	"context"
	"errors"
	"fmt"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ ports.AccountAdminStore = (*DBAdapter)(nil)

// Managing other people's accounts. Each change below is one transaction that also writes the
// line of history that says who did it.

func (db *DBAdapter) ListUsers(ctx context.Context, q ports.UserQuery) ([]domain.User, error) {
	if q.Limit <= 0 {
		return nil, errors.New("failed to list users: limit must be positive")
	}
	switch q.Status {
	case "", "active", "disabled":
	default:
		return nil, fmt.Errorf("failed to list users: status %q is not active or disabled", q.Status)
	}
	// strpos on the lower-cased text, not LIKE: what someone types is not a pattern.
	rows, err := db.Pool.Query(ctx, `
		SELECT `+userColumns+` FROM users
		WHERE ($1 = '' OR role = $1)
		  AND ($2 = '' OR ($2 = 'active' AND disabled_at IS NULL) OR ($2 = 'disabled' AND disabled_at IS NOT NULL))
		  AND ($3 = '' OR strpos(lower(email), lower($3)) > 0 OR strpos(lower(name), lower($3)) > 0)
		ORDER BY created_at DESC, id
		LIMIT $4`, string(q.Role), q.Status, q.Search, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list users: %w", err)
	}
	defer rows.Close()
	out := []domain.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to list users: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list users: %w", err)
	}
	return out, nil
}

// withTx runs fn in a transaction and commits it if fn returns nil.
func (db *DBAdapter) withTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // does nothing once committed
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func recordUserEvent(ctx context.Context, tx pgx.Tx, userID, actorID, actorEmail, kind, detail string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO user_events (user_id, actor_id, actor_email, kind, detail) VALUES ($1, $2, $3, $4, $5)`,
		userID, actorID, actorEmail, kind, detail)
	return err
}

func (db *DBAdapter) CreateUserBy(ctx context.Context, actor domain.User, u domain.User, passwordHash string) (domain.User, error) {
	if actor.ID == "" || actor.Email == "" {
		return domain.User{}, errors.New("failed to create user: who is creating it is required")
	}
	if u.ID == "" || u.Email == "" || u.Name == "" || passwordHash == "" || !u.Role.Valid() {
		return domain.User{}, errors.New("failed to create user: id, email, name, role and password hash are required")
	}
	var created domain.User
	err := db.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		created, err = scanUser(tx.QueryRow(ctx, `
			INSERT INTO users (id, email, name, role, password_hash, must_change_password) VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING `+userColumns, u.ID, u.Email, u.Name, string(u.Role), passwordHash, u.MustChangePassword))
		if err != nil {
			return err
		}
		return recordUserEvent(ctx, tx, created.ID, actor.ID, actor.Email, domain.UserCreated, "role="+string(created.Role))
	})
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

func (db *DBAdapter) SetUserDisabled(ctx context.Context, actor domain.User, userID string, disabled bool) (domain.User, bool, error) {
	if actor.ID == "" || actor.Email == "" || userID == "" {
		return domain.User{}, false, errors.New("failed to change the account: who is acting and which account are required")
	}
	if disabled && actor.ID == userID {
		return domain.User{}, false, ports.ErrOwnAccount
	}
	var user domain.User
	changed := false
	err := db.withTx(ctx, func(tx pgx.Tx) error {
		// Lock every active staff account, in a fixed order, before deciding. Two staff members
		// disabling each other at the same moment then take turns: the second finds one active
		// staff account left and is refused, instead of both succeeding and leaving none.
		rows, err := tx.Query(ctx, `SELECT id FROM users WHERE role = 'staff' AND disabled_at IS NULL ORDER BY id FOR UPDATE`)
		if err != nil {
			return err
		}
		var activeStaff []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			activeStaff = append(activeStaff, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		target, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1 FOR UPDATE`, userID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ports.ErrUserNotFound, userID)
		}
		if err != nil {
			return err
		}
		user = target
		if disabled == target.Disabled() {
			return nil // already so: nothing to write
		}
		if disabled && target.Role == domain.RoleStaff && len(activeStaff) <= 1 {
			return ports.ErrLastStaff
		}

		set := "disabled_at = now()"
		kind := domain.UserDisabled
		if !disabled {
			set, kind = "disabled_at = NULL", domain.UserEnabled
		}
		user, err = scanUser(tx.QueryRow(ctx, `UPDATE users SET `+set+` WHERE id = $1 RETURNING `+userColumns, userID))
		if err != nil {
			return err
		}
		if disabled {
			// a switched-off account is signed out everywhere at once, not at its next request
			if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
				return err
			}
		}
		changed = true
		return recordUserEvent(ctx, tx, userID, actor.ID, actor.Email, kind, "")
	})
	if err != nil {
		if errors.Is(err, ports.ErrUserNotFound) || errors.Is(err, ports.ErrLastStaff) {
			return domain.User{}, false, err
		}
		return domain.User{}, false, fmt.Errorf("failed to change the account: %w", err)
	}
	return user, changed, nil
}

func (db *DBAdapter) ResetUserPassword(ctx context.Context, actor domain.User, userID, passwordHash string) (domain.User, error) {
	if actor.ID == "" || actor.Email == "" || userID == "" || passwordHash == "" {
		return domain.User{}, errors.New("failed to reset the password: who is acting, which account and the new hash are required")
	}
	if actor.ID == userID {
		return domain.User{}, ports.ErrOwnAccount
	}
	var user domain.User
	err := db.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		user, err = scanUser(tx.QueryRow(ctx, `
			UPDATE users SET password_hash = $2, must_change_password = true WHERE id = $1 RETURNING `+userColumns, userID, passwordHash))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ports.ErrUserNotFound, userID)
		}
		if err != nil {
			return err
		}
		// whoever knew the old password, or held a sign-in made with it, is out
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
			return err
		}
		return recordUserEvent(ctx, tx, userID, actor.ID, actor.Email, domain.UserPasswordReset, "")
	})
	if err != nil {
		if errors.Is(err, ports.ErrUserNotFound) {
			return domain.User{}, err
		}
		return domain.User{}, fmt.Errorf("failed to reset the password: %w", err)
	}
	return user, nil
}

func (db *DBAdapter) ChangePasswordHash(ctx context.Context, userID, passwordHash string) error {
	if userID == "" || passwordHash == "" {
		return errors.New("failed to change the password: the account and the new hash are required")
	}
	err := db.withTx(ctx, func(tx pgx.Tx) error {
		var email string
		err := tx.QueryRow(ctx, `
			UPDATE users SET password_hash = $2, must_change_password = false WHERE id = $1 RETURNING email`, userID, passwordHash).Scan(&email)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ports.ErrUserNotFound, userID)
		}
		if err != nil {
			return err
		}
		return recordUserEvent(ctx, tx, userID, userID, email, domain.UserPasswordChanged, "")
	})
	if err != nil {
		if errors.Is(err, ports.ErrUserNotFound) {
			return err
		}
		return fmt.Errorf("failed to change the password: %w", err)
	}
	return nil
}

func (db *DBAdapter) ListUserEvents(ctx context.Context, userID string, limit int) ([]domain.UserEvent, error) {
	if limit <= 0 {
		return nil, errors.New("failed to list the history: limit must be positive")
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT id, user_id, actor_id, actor_email, kind, detail, at FROM user_events
		WHERE user_id = $1 ORDER BY id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list the history: %w", err)
	}
	defer rows.Close()
	out := []domain.UserEvent{}
	for rows.Next() {
		var e domain.UserEvent
		if err := rows.Scan(&e.ID, &e.UserID, &e.ActorID, &e.ActorEmail, &e.Kind, &e.Detail, &e.At); err != nil {
			return nil, fmt.Errorf("failed to list the history: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list the history: %w", err)
	}
	return out, nil
}
