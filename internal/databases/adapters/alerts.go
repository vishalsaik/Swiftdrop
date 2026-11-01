package adapters

import (
	"context"
	"errors"
	"fmt"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
)

var _ ports.AlertStore = (*DBAdapter)(nil)

const alertColumns = `id, kind, subject, order_id, message, occurrences, created_at, last_seen_at, resolved_at, resolved_note`

func scanAlert(row pgx.Row) (domain.Alert, error) {
	var a domain.Alert
	err := row.Scan(&a.ID, &a.Kind, &a.Subject, &a.OrderID, &a.Message, &a.Occurrences, &a.CreatedAt, &a.LastSeenAt, &a.ResolvedAt, &a.ResolvedNote)
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
