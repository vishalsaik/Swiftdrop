package payment

import (
	"context"

	"swiftdrop/internal/domain"
)

// Refunder gives money back through an external payment processor. It is a
// separate, small interface from Gateway on purpose: charging and refunding are
// different jobs that happen in different places (a request and a background
// job), and a gateway that can only charge, like the test Mock, stays valid.
// StripeGateway and FakeGateway implement both.
//
// Refund returns an error for anything that is not "the provider accepted the
// refund", and the caller retries later. Asking again with the same
// IdempotencyKey is safe, and so is asking for a payment that was already
// refunded: the implementation reports that as success, not as an error.
type Refunder interface {
	Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error)
}
