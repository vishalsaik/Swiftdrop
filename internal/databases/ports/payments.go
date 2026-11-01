package ports

import (
	"context"
	"errors"
	"time"

	"swiftdrop/internal/domain"
)

// Errors every PaymentStore implementation returns (wrapped with %w and an id)
// so callers match with errors.Is and know nothing about the storage driver.
var (
	ErrPaymentNotFound = errors.New("payment not found")

	// ErrPaymentNotProcessing means a completion found the payment already
	// finished (no longer processing, and not waiting for review either): someone
	// else recorded its outcome first. Nothing was changed.
	ErrPaymentNotProcessing = errors.New("payment is not processing")
)

// PaymentStore keeps our record of each payment attempt. It is the storage
// side of idempotent payments: one payment per order, claimed atomically, so
// of any number of simultaneous or retried requests exactly one does the work.
//
// Every deadline is computed by the database clock, not the caller's, so
// several servers with drifting clocks still agree on whether a lease expired.
type PaymentStore interface {
	// ClaimPayment tries to start a payment for p.OrderID using p.ID and
	// p.AmountCents, with a lease of the given length. If the order has no
	// payment yet it creates one in status processing and returns it with
	// claimed = true: the caller owns the attempt. If the order already has a
	// payment it changes nothing and returns that one with claimed = false.
	// ErrOrderNotFound if there is no such order.
	ClaimPayment(ctx context.Context, p domain.Payment, lease time.Duration) (payment domain.Payment, claimed bool, err error)

	// GetPaymentByOrder returns the order's payment, or ErrPaymentNotFound.
	GetPaymentByOrder(ctx context.Context, orderID string) (domain.Payment, error)

	// GetPayment returns the payment with the given id, or ErrPaymentNotFound.
	// A provider's webhook names a payment by its id (our idempotency key), not
	// by the order, so this is how an event finds its payment.
	GetPayment(ctx context.Context, id string) (domain.Payment, error)

	// ListPaymentEvents returns the history of every payment of the order, oldest
	// first (empty if there is none). It is the ledger the database keeps by
	// itself: see payment_events in schema.sql.
	ListPaymentEvents(ctx context.Context, orderID string) ([]domain.PaymentEvent, error)

	// CompletePayment records the outcome (authorized, declined or abandoned) of a
	// payment, but only while it still waits for one (processing, or needs_review:
	// the provider's answer may arrive after a person was asked to look), so a late
	// or duplicate writer cannot overwrite the result. ErrPaymentNotProcessing if
	// it was already completed, ErrPaymentNotFound if there is no such payment.
	CompletePayment(ctx context.Context, id string, status domain.PaymentStatus, providerPaymentID, reason string) (domain.Payment, error)

	// TakeOverPayment extends the lease of a processing payment, but only if
	// its current lease has expired. ok = true: the caller now holds the claim
	// and should reconcile with the provider. ok = false: the payment is
	// finished or someone else holds a live lease, so the caller should read
	// it again. Of several simultaneous takers exactly one gets ok = true.
	// ErrPaymentNotFound if there is no such payment.
	TakeOverPayment(ctx context.Context, id string, lease time.Duration) (payment domain.Payment, ok bool, err error)

	// StartPayment claims the payment (like ClaimPayment) AND moves its order
	// from orderFrom to orderTo (compare-and-set, like UpdateOrderState), in one
	// transaction: either both happen or neither does, so there is never a
	// payment row for an order that did not move, or the reverse. claimed =
	// false means the order already has a payment: nothing was written and the
	// existing payment is returned. ErrStateConflict if the order was no longer
	// in orderFrom (the claim is rolled back); ErrOrderNotFound if there is no
	// such order.
	StartPayment(ctx context.Context, p domain.Payment, lease time.Duration, orderFrom, orderTo string) (payment domain.Payment, claimed bool, err error)

	// ListUnsettledPayments returns the payments that wait for an answer and that a
	// reconciler should ask the provider about, oldest first, at most limit: those
	// needing review, and those processing whose lease has run out (nobody is
	// working on them) and that were claimed at least minAge ago. The clock is the
	// database's.
	ListUnsettledPayments(ctx context.Context, minAge time.Duration, limit int) ([]domain.Payment, error)

	// MarkPaymentNeedsReview moves a processing payment to needs_review, with the
	// reason a person will read, so that no new attempt is started for the order
	// until it is settled. ok = false: it was no longer processing, so nothing
	// changed. The order is not touched. ErrPaymentNotFound if there is no such
	// payment.
	MarkPaymentNeedsReview(ctx context.Context, id, reason string) (payment domain.Payment, ok bool, err error)

	// FinishPayment records the outcome of a processing payment (like
	// CompletePayment) AND moves its order from orderFrom to orderTo, in one
	// transaction, and returns the updated order. If either half cannot be
	// done neither is: ErrPaymentNotProcessing if the payment was already
	// completed, ErrStateConflict if the order was no longer in orderFrom,
	// ErrPaymentNotFound if there is no such payment.
	FinishPayment(ctx context.Context, paymentID string, status domain.PaymentStatus, providerPaymentID, reason, orderFrom, orderTo string) (domain.Order, error)
}

// Store is everything the handlers need from storage: orders and payments.
// One value (the Postgres adapter) implements both, which is what lets a
// payment and its order change in a single transaction.
type Store interface {
	IDatabase
	PaymentStore
	AlertStore
}
