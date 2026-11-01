package ports

import (
	"context"
	"errors"
	"time"

	"swiftdrop/internal/domain"
)

// ErrRefundNotInProgress means a refund write found the payment's refund not
// held by anyone (never claimed, or already finished). Nothing was changed.
var ErrRefundNotInProgress = errors.New("refund is not in progress")

// RefundReversalStore records that a refund the provider accepted was later
// reversed. The webhook handler needs it, so it is part of Store.
type RefundReversalStore interface {
	// ReverseRefund moves the refund of the payment the provider knows as
	// providerPaymentID from refunded to failed, with the reason a person will
	// read, but only when that refund is the one with id refundID. reversed =
	// false means nothing was changed, and payment is the payment as it stands:
	// its refund may be still in progress, already failed, not ours, or never
	// made. ErrPaymentNotFound if no payment has that provider id.
	ReverseRefund(ctx context.Context, providerPaymentID, refundID, reason string) (payment domain.Payment, reversed bool, err error)
}

// RefundStore is what the refund job needs from storage. A refund is DUE when a
// payment was authorized (money taken), its order is cancelled, it was charged
// online (cash moves no money through us) and its refund has not been done. The
// job claims each due refund with a lease before it calls the provider, exactly
// as /pay claims a charge, so several servers running the job never refund the
// same payment twice.
type RefundStore interface {
	// ListRefundable returns up to limit payments whose refund is due and not
	// held by a live lease, oldest first. A refund whose lease has expired (its
	// worker died) is due again.
	ListRefundable(ctx context.Context, limit int) ([]domain.Payment, error)

	// ClaimRefund takes a due refund: it counts an attempt and sets a lease of
	// the given length, by the database clock. ok = false means it is not
	// claimable (finished, held by someone else, or no longer owed): read it
	// again. Of several simultaneous claimers exactly one gets ok = true.
	// ErrPaymentNotFound if there is no such payment.
	ClaimRefund(ctx context.Context, paymentID string, lease time.Duration) (payment domain.Payment, ok bool, err error)

	// CompleteRefund records that the provider accepted the refund.
	// ErrRefundNotInProgress if it is not being refunded, ErrPaymentNotFound if
	// there is no such payment.
	CompleteRefund(ctx context.Context, paymentID, refundID string) error

	// FailRefund records why an attempt failed. With giveUp it moves the refund
	// to failed (a person must look); otherwise it stays in progress and is due
	// again when its lease runs out. Same errors as CompleteRefund.
	FailRefund(ctx context.Context, paymentID, message string, giveUp bool) error
}
