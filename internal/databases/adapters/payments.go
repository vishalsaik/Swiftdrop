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

var (
	_ ports.PaymentStore = (*DBAdapter)(nil)
	_ ports.Store        = (*DBAdapter)(nil)
)

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

// querier is what the pool and a transaction have in common. The payment
// queries below take one, so the same SQL runs either as a single statement
// (the pool) or as one step of a transaction (StartPayment, FinishPayment).
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// paymentExists tells a payment that is not there from one that is there but
// not in the state a guarded write needed. It is only used to pick the error
// after a zero-row write; the write itself already decided any race.
func paymentExists(ctx context.Context, q querier, id string) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM payments WHERE id = $1)`, id).Scan(&exists)
	return exists, err
}

func getPaymentByOrder(ctx context.Context, q querier, orderID string) (domain.Payment, error) {
	query := `SELECT ` + paymentColumns + ` FROM payments WHERE order_id = $1`
	p, err := scanPayment(q.QueryRow(ctx, query, orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, fmt.Errorf("%w: order %s", ports.ErrPaymentNotFound, orderID)
	}
	if err != nil {
		return domain.Payment{}, fmt.Errorf("failed to fetch payment: %w", err)
	}
	return p, nil
}

func claimPayment(ctx context.Context, q querier, p domain.Payment, lease time.Duration) (domain.Payment, bool, error) {
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
	claimed, err := scanPayment(q.QueryRow(ctx, query, p.ID, p.OrderID, p.AmountCents, lease.Seconds()))
	if err == nil {
		return claimed, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing was inserted: the order already has a payment. Hand it back.
		existing, getErr := getPaymentByOrder(ctx, q, p.OrderID)
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

func completePayment(ctx context.Context, q querier, id string, status domain.PaymentStatus, providerPaymentID, reason string) (domain.Payment, error) {
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
	p, err := scanPayment(q.QueryRow(ctx, query, id, status, providerPaymentID, reason))
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, fmt.Errorf("failed to complete payment: %w", err)
	}
	exists, qerr := paymentExists(ctx, q, id)
	if qerr != nil {
		return domain.Payment{}, fmt.Errorf("failed to complete payment: %w", qerr)
	}
	if !exists {
		return domain.Payment{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotFound, id)
	}
	return domain.Payment{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotProcessing, id)
}

func (db *DBAdapter) ClaimPayment(ctx context.Context, p domain.Payment, lease time.Duration) (domain.Payment, bool, error) {
	return claimPayment(ctx, db.Pool, p, lease)
}

func (db *DBAdapter) GetPaymentByOrder(ctx context.Context, orderID string) (domain.Payment, error) {
	return getPaymentByOrder(ctx, db.Pool, orderID)
}

func (db *DBAdapter) CompletePayment(ctx context.Context, id string, status domain.PaymentStatus, providerPaymentID, reason string) (domain.Payment, error) {
	return completePayment(ctx, db.Pool, id, status, providerPaymentID, reason)
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
	exists, qerr := paymentExists(ctx, db.Pool, id)
	if qerr != nil {
		return domain.Payment{}, false, fmt.Errorf("failed to take over payment: %w", qerr)
	}
	if !exists {
		return domain.Payment{}, false, fmt.Errorf("%w: %s", ports.ErrPaymentNotFound, id)
	}
	return domain.Payment{}, false, nil
}

func (db *DBAdapter) StartPayment(ctx context.Context, p domain.Payment, lease time.Duration, orderFrom, orderTo string) (domain.Payment, bool, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return domain.Payment{}, false, fmt.Errorf("failed to start payment: %w", err)
	}
	// Rolling back after a successful Commit is a no-op, so this covers every
	// early return: nothing is written unless we reach Commit.
	defer func() { _ = tx.Rollback(ctx) }()

	payment, claimed, err := claimPayment(ctx, tx, p, lease)
	if err != nil {
		return domain.Payment{}, false, err
	}
	if !claimed {
		// The order already has a payment, so nothing was written and the
		// order must not move: hand the existing payment back.
		return payment, false, nil
	}
	// We own the payment, so now move the order, compare-and-set, in the same
	// transaction. If the order is no longer in orderFrom, the rollback also
	// removes the payment row we just inserted.
	tag, err := tx.Exec(ctx, `UPDATE orders SET state = $1 WHERE id = $2 AND state = $3`, orderTo, p.OrderID, orderFrom)
	if err != nil {
		return domain.Payment{}, false, fmt.Errorf("failed to start payment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Payment{}, false, fmt.Errorf("%w: %s", ports.ErrStateConflict, p.OrderID)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Payment{}, false, fmt.Errorf("failed to start payment: %w", err)
	}
	return payment, true, nil
}

func (db *DBAdapter) FinishPayment(ctx context.Context, paymentID string, status domain.PaymentStatus, providerPaymentID, reason, orderFrom, orderTo string) (domain.Order, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return domain.Order{}, fmt.Errorf("failed to finish payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	p, err := completePayment(ctx, tx, paymentID, status, providerPaymentID, reason)
	if err != nil {
		return domain.Order{}, err
	}
	query := `
		UPDATE orders
		SET state = $1
		WHERE id = $2 AND state = $3
		RETURNING ` + orderColumns
	o, err := scanOrder(tx.QueryRow(ctx, query, orderTo, p.OrderID, orderFrom))
	if errors.Is(err, pgx.ErrNoRows) {
		// The payment's order exists (foreign key), so zero rows means it is
		// no longer in orderFrom. Rolling back un-records the payment outcome.
		return domain.Order{}, fmt.Errorf("%w: %s", ports.ErrStateConflict, p.OrderID)
	}
	if err != nil {
		return domain.Order{}, fmt.Errorf("failed to finish payment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Order{}, fmt.Errorf("failed to finish payment: %w", err)
	}
	return o, nil
}
