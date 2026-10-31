package payment

import (
	"context"
	"errors"

	"swiftdrop/internal/domain"
)

type PaymentProvider interface {
	Authorize(ctx context.Context, req domain.PaymentRequest) (*domain.PaymentResult, error)
}

// ErrInvalidRequest means the caller sent a payment request that could never
// succeed (for example a card payment with no payment method). Nothing was
// charged or even sent to a provider, so it is the caller's to fix and retry.
var ErrInvalidRequest = errors.New("invalid payment request")

// RequestChecker is implemented by a provider or gateway that can refuse a
// request it could never fulfil. The handler asks BEFORE it claims the payment,
// because a claim that is then abandoned leaves a payment stuck in processing
// and an order stuck in payment_pending. Check must not call the network.
type RequestChecker interface {
	// Check returns an error wrapping ErrInvalidRequest, or nil.
	Check(req domain.PaymentRequest) error
}
