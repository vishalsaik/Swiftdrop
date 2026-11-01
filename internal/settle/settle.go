// Package settle records a payment provider's answer for a payment that was
// waiting for one, whoever got the answer: a webhook the provider pushed, or the
// reconciler asking after a lost reply.
//
// A provider's answer is a fact about money, and an order is a fact about the
// kitchen. Usually they change together (FinishPayment). But an order can be
// cancelled while a payment is in flight; then the answer arrives for an order
// that no longer waits for it. The order must stay cancelled, and the answer
// must still be kept: an authorized payment on a cancelled order is exactly what
// the refund job looks for. Dropping it would leave money taken with nothing to
// say so.
package settle

import (
	"context"
	"errors"
	"fmt"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// Result says what recording an answer did.
type Result int

const (
	// Recorded: the answer was written and the order moved with it.
	Recorded Result = iota + 1
	// AlreadyRecorded: the payment was no longer waiting (someone else recorded
	// an answer first, or there is no such payment). Nothing was changed.
	AlreadyRecorded
	// OrderMovedOn: the order had moved on while the payment was waiting (it was
	// cancelled). The order was left alone and the answer was kept on the payment.
	OrderMovedOn
)

// UnrecordedError means the order had moved on and the answer could not be kept
// either, so money may have moved with nothing to say so.
type UnrecordedError struct {
	Conflict error // why the order could not move with the payment
	Err      error // why the answer could not be kept
}

func (e *UnrecordedError) Error() string {
	return fmt.Sprintf("the answer could not be recorded after the order moved on (%v): %v", e.Conflict, e.Err)
}

func (e *UnrecordedError) Unwrap() error { return e.Err }

// Store is the storage Apply writes through.
type Store interface {
	FinishPayment(ctx context.Context, paymentID string, status domain.PaymentStatus, providerPaymentID, reason, orderFrom, orderTo string) (domain.Order, error)
	CompletePayment(ctx context.Context, id string, status domain.PaymentStatus, providerPaymentID, reason string) (domain.Payment, error)
}

// Apply records status (authorized, declined or abandoned) for p, which waits for
// an answer, and moves its order with it: to paid on a success, and on a failure
// back to created for another attempt, or to cancelled after the last one. Of
// several writers, exactly one records; the others get AlreadyRecorded.
func Apply(ctx context.Context, db Store, p domain.Payment, status domain.PaymentStatus, providerPaymentID, reason string) (Result, error) {
	to := domain.OrderPaid
	if status != domain.PaymentAuthorized {
		to = p.OrderStateAfterFailure()
	}
	_, err := db.FinishPayment(ctx, p.ID, status, providerPaymentID, reason,
		string(domain.OrderPaymentPending), string(to))
	switch {
	case err == nil:
		return Recorded, nil
	case errors.Is(err, ports.ErrPaymentNotProcessing), errors.Is(err, ports.ErrPaymentNotFound):
		// Another writer finished it between our read and our write.
		return AlreadyRecorded, nil
	case errors.Is(err, ports.ErrStateConflict):
		// The answer is kept without touching the order. Finding the payment
		// already recorded is fine.
		if _, keepErr := db.CompletePayment(ctx, p.ID, status, providerPaymentID, reason); keepErr != nil &&
			!errors.Is(keepErr, ports.ErrPaymentNotProcessing) {
			return 0, &UnrecordedError{Conflict: err, Err: keepErr}
		}
		return OrderMovedOn, nil
	default:
		return 0, err
	}
}
