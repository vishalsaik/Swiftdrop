package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
)

var (
	_ ports.RefundStore         = (*DBAdapter)(nil)
	_ ports.RefundReversalStore = (*DBAdapter)(nil)
)

// qualify prefixes every column of a column list, for queries that join tables
// whose columns share names (both have id, created_at and updated_at).
func qualify(prefix, columns string) string {
	cols := strings.Split(columns, ", ")
	for i, c := range cols {
		cols[i] = prefix + c
	}
	return strings.Join(cols, ", ")
}

// qualified is that for the payment columns.
func qualified(prefix string) string { return qualify(prefix, paymentColumns) }

// refundOwed is the condition that makes a refund due, for a payment p joined to
// its order o. Cash is excluded because no money moved through us, and a
// payment with no provider id could not be refunded anyway.
const refundOwed = `p.status = 'authorized'
		AND p.provider_payment_id <> ''
		AND o.state = 'cancelled'
		AND o.payment_type <> 'cash'
		AND (p.refund_status = 'none'
		     OR (p.refund_status = 'refunding' AND p.refund_lease_expires_at <= now()))`

func (db *DBAdapter) ReverseRefund(ctx context.Context, providerPaymentID, refundID, reason string) (domain.Payment, bool, error) {
	if providerPaymentID == "" || refundID == "" {
		return domain.Payment{}, false, errors.New("failed to reverse refund: the provider payment id and the refund id are required")
	}
	// Only the refund we recorded as done, and only that very refund: another one
	// failing (made by hand in the provider's dashboard) says nothing about ours.
	p, err := scanPayment(db.Pool.QueryRow(ctx, `
		UPDATE payments SET refund_status = 'failed', refund_error = $3, refund_manual_by = '', refund_manual_note = '', updated_at = now()
		WHERE provider_payment_id = $1 AND refund_status = 'refunded' AND refund_id = $2
		RETURNING `+paymentColumns, providerPaymentID, refundID, reason))
	if err == nil {
		return p, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, false, fmt.Errorf("failed to reverse refund: %w", err)
	}
	// Nothing changed: say how the payment stands, so the caller can tell a refund
	// still being made from one already marked failed.
	p, err = scanPayment(db.Pool.QueryRow(ctx,
		`SELECT `+paymentColumns+` FROM payments WHERE provider_payment_id = $1 ORDER BY attempt DESC LIMIT 1`, providerPaymentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, false, fmt.Errorf("%w: provider payment %s", ports.ErrPaymentNotFound, providerPaymentID)
	}
	if err != nil {
		return domain.Payment{}, false, fmt.Errorf("failed to reverse refund: %w", err)
	}
	return p, false, nil
}

func (db *DBAdapter) ListRefundable(ctx context.Context, limit int) ([]domain.Payment, error) {
	if limit <= 0 {
		return nil, errors.New("failed to list refunds: limit must be positive")
	}
	query := `
		SELECT ` + qualified("p.") + `
		FROM payments p JOIN orders o ON o.id = p.order_id
		WHERE ` + refundOwed + `
		ORDER BY p.updated_at, p.id
		LIMIT $1`
	rows, err := db.Pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list refunds: %w", err)
	}
	defer rows.Close()
	var out []domain.Payment
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to list refunds: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list refunds: %w", err)
	}
	return out, nil
}

func (db *DBAdapter) ClaimRefund(ctx context.Context, paymentID string, lease time.Duration) (domain.Payment, bool, error) {
	if lease <= 0 {
		return domain.Payment{}, false, errors.New("failed to claim refund: lease must be positive")
	}
	// The row only matches while the refund is due, and the update changes what
	// makes it due (refund_status, the lease), so of several simultaneous
	// claimers exactly one gets the row. The deadline comes from the database
	// clock so servers agree on it.
	query := `
		UPDATE payments p
		SET refund_status = 'refunding',
		    refund_attempts = p.refund_attempts + 1,
		    refund_lease_expires_at = now() + make_interval(secs => $2),
		    updated_at = now()
		FROM orders o
		WHERE p.id = $1 AND o.id = p.order_id AND ` + refundOwed + `
		RETURNING ` + qualified("p.")
	p, err := scanPayment(db.Pool.QueryRow(ctx, query, paymentID, lease.Seconds()))
	if err == nil {
		return p, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, false, fmt.Errorf("failed to claim refund: %w", err)
	}
	exists, qerr := paymentExists(ctx, db.Pool, paymentID)
	if qerr != nil {
		return domain.Payment{}, false, fmt.Errorf("failed to claim refund: %w", qerr)
	}
	if !exists {
		return domain.Payment{}, false, fmt.Errorf("%w: %s", ports.ErrPaymentNotFound, paymentID)
	}
	return domain.Payment{}, false, nil
}

func (db *DBAdapter) CompleteRefund(ctx context.Context, paymentID, refundID string) error {
	if refundID == "" {
		return errors.New("failed to complete refund: refund id is required")
	}
	tag, err := db.Pool.Exec(ctx, `
		UPDATE payments
		SET refund_status = 'refunded', refund_id = $2, refund_error = '',
		    refund_lease_expires_at = NULL, updated_at = now()
		WHERE id = $1 AND refund_status = 'refunding'`, paymentID, refundID)
	return db.refundWriteResult(ctx, tag.RowsAffected(), err, paymentID, "complete")
}

func (db *DBAdapter) FailRefund(ctx context.Context, paymentID, message string, giveUp bool) error {
	// Giving up is final; otherwise only the reason is recorded and the lease
	// keeps running, which is the wait before the next attempt.
	status, release := "refunding", "refund_lease_expires_at"
	if giveUp {
		status, release = "failed", "NULL"
	}
	tag, err := db.Pool.Exec(ctx, `
		UPDATE payments
		SET refund_status = $2, refund_error = $3, refund_lease_expires_at = `+release+`, updated_at = now()
		WHERE id = $1 AND refund_status = 'refunding'`, paymentID, status, message)
	return db.refundWriteResult(ctx, tag.RowsAffected(), err, paymentID, "fail")
}

// refundWriteResult turns the outcome of a guarded refund write into the error
// callers expect: zero rows is either a payment that is not there or a refund
// that was not in progress.
func (db *DBAdapter) refundWriteResult(ctx context.Context, rows int64, err error, paymentID, doing string) error {
	if err != nil {
		return fmt.Errorf("failed to %s refund: %w", doing, err)
	}
	if rows > 0 {
		return nil
	}
	exists, qerr := paymentExists(ctx, db.Pool, paymentID)
	if qerr != nil {
		return fmt.Errorf("failed to %s refund: %w", doing, qerr)
	}
	if !exists {
		return fmt.Errorf("%w: %s", ports.ErrPaymentNotFound, paymentID)
	}
	return fmt.Errorf("%w: %s", ports.ErrRefundNotInProgress, paymentID)
}

var _ ports.ManualRefundStore = (*DBAdapter)(nil)

// refundAlertKinds are the alerts that say "a person must get this customer their money":
// recording the refund answers them.
var refundAlertKinds = []string{domain.AlertRefundGivenUp, domain.AlertRefundReversed, domain.AlertRefundUnrecorded}

func (db *DBAdapter) RecordManualRefund(ctx context.Context, orderID, reference, by, note string) (domain.Payment, bool, error) {
	if orderID == "" || reference == "" || by == "" {
		return domain.Payment{}, false, errors.New("failed to record the refund: the order, the reference and who is recording it are required")
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return domain.Payment{}, false, fmt.Errorf("failed to record the refund: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // does nothing once committed

	// Only a refund that failed, of a payment that was taken. The status is compared in the
	// same statement that changes it, so of two people recording it at once exactly one
	// does, and a refund the job is making cannot be recorded over.
	p, err := scanPayment(tx.QueryRow(ctx, `
		UPDATE payments
		SET refund_status = 'refunded', refund_id = $2, refund_error = '', refund_lease_expires_at = NULL,
		    refund_manual_by = $3, refund_manual_note = $4, updated_at = now()
		WHERE order_id = $1 AND status = 'authorized' AND refund_status = 'failed'
		RETURNING `+paymentColumns, orderID, reference, by, note))
	if err == nil {
		resolved := "refunded by hand (" + reference + "), recorded by " + by
		if note != "" {
			resolved += ": " + note
		}
		if _, err := tx.Exec(ctx, `
			UPDATE alerts SET resolved_at = now(), resolved_note = $2
			WHERE subject = $1 AND kind = ANY($3) AND resolved_at IS NULL`, p.ID, resolved, refundAlertKinds); err != nil {
			return domain.Payment{}, false, fmt.Errorf("failed to record the refund: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Payment{}, false, fmt.Errorf("failed to record the refund: %w", err)
		}
		return p, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, false, fmt.Errorf("failed to record the refund: %w", err)
	}
	// Nothing changed: say how the payment stands, so the caller can say why.
	p, err = scanPayment(tx.QueryRow(ctx,
		`SELECT `+paymentColumns+` FROM payments WHERE order_id = $1 ORDER BY attempt DESC LIMIT 1`, orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, false, fmt.Errorf("%w: order %s", ports.ErrPaymentNotFound, orderID)
	}
	if err != nil {
		return domain.Payment{}, false, fmt.Errorf("failed to record the refund: %w", err)
	}
	return p, false, nil
}
