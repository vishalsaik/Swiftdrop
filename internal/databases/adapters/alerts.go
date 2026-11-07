package adapters

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
)

var (
	_ ports.AlertStore       = (*DBAdapter)(nil)
	_ ports.AlertNotifyStore = (*DBAdapter)(nil)
)

const alertColumns = `id, kind, subject, order_id, message, occurrences, created_at, last_seen_at, resolved_at, resolved_note, notified_at`

func scanAlert(row pgx.Row, extra ...any) (domain.Alert, error) {
	var a domain.Alert
	dest := append([]any{&a.ID, &a.Kind, &a.Subject, &a.OrderID, &a.Message, &a.Occurrences, &a.CreatedAt, &a.LastSeenAt, &a.ResolvedAt, &a.ResolvedNote, &a.NotifiedAt}, extra...)
	err := row.Scan(dest...)
	return a, err
}

func (db *DBAdapter) RaiseAlert(ctx context.Context, kind, subject, orderID, message string) error {
	if kind == "" || subject == "" || message == "" {
		return errors.New("failed to raise alert: kind, subject and message are required")
	}
	// One open alert per (kind, subject): raising it again only counts another
	// occurrence and keeps the newest message, so a delivery retried a hundred
	// times is one alert, not a hundred.
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO alerts (kind, subject, order_id, message) VALUES ($1, $2, $3, $4)
		ON CONFLICT (kind, subject) WHERE resolved_at IS NULL
		DO UPDATE SET occurrences = alerts.occurrences + 1,
		              last_seen_at = now(),
		              message = EXCLUDED.message,
		              order_id = CASE WHEN EXCLUDED.order_id <> '' THEN EXCLUDED.order_id ELSE alerts.order_id END`,
		kind, subject, orderID, message)
	if err != nil {
		return fmt.Errorf("failed to raise alert: %w", err)
	}
	return nil
}

func (db *DBAdapter) ListAlerts(ctx context.Context, openOnly bool, limit int) ([]domain.Alert, error) {
	if limit <= 0 {
		return nil, errors.New("failed to list alerts: limit must be positive")
	}
	where := ""
	if openOnly {
		where = "WHERE resolved_at IS NULL"
	}
	rows, err := db.Pool.Query(ctx, `SELECT `+alertColumns+` FROM alerts `+where+` ORDER BY last_seen_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list alerts: %w", err)
	}
	defer rows.Close()
	out := []domain.Alert{}
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to list alerts: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list alerts: %w", err)
	}
	return out, nil
}

func (db *DBAdapter) ResolveAlert(ctx context.Context, id int64, note string) (domain.Alert, error) {
	a, err := scanAlert(db.Pool.QueryRow(ctx, `
		UPDATE alerts SET resolved_at = now(), resolved_note = $2
		WHERE id = $1 AND resolved_at IS NULL
		RETURNING `+alertColumns, id, note))
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Alert{}, fmt.Errorf("failed to resolve alert: %w", err)
	}
	var exists bool
	if qerr := db.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM alerts WHERE id = $1)`, id).Scan(&exists); qerr != nil {
		return domain.Alert{}, fmt.Errorf("failed to resolve alert: %w", qerr)
	}
	if !exists {
		return domain.Alert{}, fmt.Errorf("%w: %d", ports.ErrAlertNotFound, id)
	}
	return domain.Alert{}, fmt.Errorf("%w: %d", ports.ErrAlertResolved, id)
}

func seconds(d time.Duration) float64 { return d.Seconds() }

func (db *DBAdapter) ClaimAlertsToNotify(ctx context.Context, limit int, lease, reminder time.Duration) ([]ports.AlertClaim, error) {
	if limit <= 0 || lease <= 0 {
		return nil, errors.New("failed to claim alerts: limit and lease must be positive")
	}
	// The database's clock decides what is due, as for payment leases, so servers whose
	// clocks differ cannot send the same alert twice. SKIP LOCKED lets two notifiers
	// running at once take different alerts instead of waiting for each other.
	rows, err := db.Pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM alerts
			WHERE resolved_at IS NULL
			  AND (notify_lease_until IS NULL OR notify_lease_until <= now())
			  AND (notified_at IS NULL OR ($3::float8 > 0 AND notified_at <= now() - $3::float8 * interval '1 second'))
			ORDER BY id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE alerts a SET notify_lease_until = now() + $2::float8 * interval '1 second'
		FROM due WHERE a.id = due.id
		RETURNING `+qualify("a.", alertColumns)+`, (a.notified_at IS NOT NULL), a.notify_failures`,
		limit, seconds(lease), seconds(reminder))
	if err != nil {
		return nil, fmt.Errorf("failed to claim alerts: %w", err)
	}
	defer rows.Close()
	out := []ports.AlertClaim{}
	for rows.Next() {
		var c ports.AlertClaim
		a, err := scanAlert(rows, &c.Reminder, &c.Failures)
		if err != nil {
			return nil, fmt.Errorf("failed to claim alerts: %w", err)
		}
		c.Alert = a
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to claim alerts: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alert.ID < out[j].Alert.ID })
	return out, nil
}

func (db *DBAdapter) MarkAlertNotified(ctx context.Context, id int64) error {
	if _, err := db.Pool.Exec(ctx, `
		UPDATE alerts SET notified_at = now(), notify_failures = 0, notify_lease_until = NULL WHERE id = $1`, id); err != nil {
		return fmt.Errorf("failed to mark alert %d as sent: %w", id, err)
	}
	return nil
}

func (db *DBAdapter) MarkAlertNotifyFailed(ctx context.Context, id int64, wait time.Duration) error {
	if _, err := db.Pool.Exec(ctx, `
		UPDATE alerts SET notify_failures = notify_failures + 1, notify_lease_until = now() + $2::float8 * interval '1 second'
		WHERE id = $1`, id, seconds(wait)); err != nil {
		return fmt.Errorf("failed to record a failed send of alert %d: %w", id, err)
	}
	return nil
}
