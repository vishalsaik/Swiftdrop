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

var _ ports.PaymentStore = (*DBAdapter)(nil)

// foreignKeyViolation is Postgres' SQLSTATE for a row that references a row
// that does not exist (here: a payment for an order that is not there).
const foreignKeyViolation = "23503"

// paymentColumns is the one column list every payment query returns. It must
// stay in the same order as the Scan in scanPayment.
const paymentColumns = `id, order_id, status, amount_cents, provider_payment_id, reason, lease_expires_at, created_at, updated_at`

func scanPayment(row pgx.Row) (domain.Payment, error) {
	var p domain.Payment
	err := row.Scan(
		&p.ID,
		&p.OrderID,
		&p.Status,
		&p.AmountCents,
		&p.ProviderPaymentID,
		&p.Reason,
		&p.LeaseExpiresAt,
		&p.CreatedAt,
		&p.UpdatedAt,
	)
	return p, err
}

// paymentExists tells a payment that is not there from one that is there but
// not in the state a guarded write needed. It is only used to pick the error
// after a zero-row write; the write itself already decided any race.
func (db *DBAdapter) paymentExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := db.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM payments WHERE id = $1)`, id).Scan(&exists)
	return exists, err
}

func (db *DBAdapter) ClaimPayment(ctx context.Context, p domain.Payment, lease time.Duration) (domain.Payment, bool, error) {
	if p.ID == "" || p.OrderID == "" {
		return domain.Payment{}, false, errors.New("failed to claim payment: id and order id are required")
	}
	if lease <= 0 {
		return domain.Payment{}, false, errors.New("failed to claim payment: lease must be positive")
	}
	// The UNIQUE constraint on order_id makes this atomic: when two requests
	// insert for the same order at once, one row is written and the other
	// statement does nothing, whichever of them the database sees first. The
	// deadline comes from the database clock so servers agree on it.
	query := `
		INSERT INTO payments (id, order_id, status, amount_cents, lease_expires_at)
		VALUES ($1, $2, 'processing', $3, now() + make_interval(secs => $4))
		ON CONFLICT (order_id) DO NOTHING
		RETURNING ` + paymentColumns
	claimed, err := scanPayment(db.Pool.QueryRow(ctx, query, p.ID, p.OrderID, p.AmountCents, lease.Seconds()))
	if err == nil {
		return claimed, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing was inserted: the order already has a payment. Hand it back.
		existing, getErr := db.GetPaymentByOrder(ctx, p.OrderID)
		if getErr != nil {
			return domain.Payment{}, false, getErr
		}
		return existing, false, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
		return domain.Payment{}, false, fmt.Errorf("%w: %s", ports.ErrOrderNotFound, p.OrderID)
	}
	return domain.Payment{}, false, fmt.Errorf("failed to claim payment: %w", err)
}

func (db *DBAdapter) GetPaymentByOrder(ctx context.Context, orderID string) (domain.Payment, error) {
	query := `SELECT ` + paymentColumns + ` FROM payments WHERE order_id = $1`
	p, err := scanPayment(db.Pool.QueryRow(ctx, query, orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, fmt.Errorf("%w: order %s", ports.ErrPaymentNotFound, orderID)
	}
	if err != nil {
		return domain.Payment{}, fmt.Errorf("failed to fetch payment: %w", err)
	}
	return p, nil
}

func (db *DBAdapter) CompletePayment(ctx context.Context, id string, status domain.PaymentStatus, providerPaymentID, reason string) (domain.Payment, error) {
	if status != domain.PaymentAuthorized && status != domain.PaymentDeclined {
		return domain.Payment{}, fmt.Errorf("failed to complete payment %s: %q is not a final status", id, status)
	}
	// Compare-and-set again: only a payment that is still processing can be
	// completed, so a late or duplicate writer cannot overwrite the result.
	query := `
		UPDATE payments
		SET status = $2, provider_payment_id = $3, reason = $4, updated_at = now()
		WHERE id = $1 AND status = 'processing'
		RETURNING ` + paymentColumns
	p, err := scanPayment(db.Pool.QueryRow(ctx, query, id, status, providerPaymentID, reason))
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, fmt.Errorf("failed to complete payment: %w", err)
	}
	exists, qerr := db.paymentExists(ctx, id)
	if qerr != nil {
		return domain.Payment{}, fmt.Errorf("failed to complete payment: %w", qerr)
	}
	if !exists {
		return domain.Payment{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotFound, id)
	}
	return domain.Payment{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotProcessing, id)
}

func (db *DBAdapter) TakeOverPayment(ctx context.Context, id string, lease time.Duration) (domain.Payment, bool, error) {
	if lease <= 0 {
		return domain.Payment{}, false, errors.New("failed to take over payment: lease must be positive")
	}
	// The row only matches while it is processing AND its lease has run out, so
	// when several requests notice the expiry together, one of them gets the row.
	query := `
		UPDATE payments
		SET lease_expires_at = now() + make_interval(secs => $2), updated_at = now()
		WHERE id = $1 AND status = 'processing' AND lease_expires_at <= now()
		RETURNING ` + paymentColumns
	p, err := scanPayment(db.Pool.QueryRow(ctx, query, id, lease.Seconds()))
	if err == nil {
		return p, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, false, fmt.Errorf("failed to take over payment: %w", err)
	}
	exists, qerr := db.paymentExists(ctx, id)
	if qerr != nil {
		return domain.Payment{}, false, fmt.Errorf("failed to take over payment: %w", qerr)
	}
	if !exists {
		return domain.Payment{}, false, fmt.Errorf("%w: %s", ports.ErrPaymentNotFound, id)
	}
	return domain.Payment{}, false, nil
}
