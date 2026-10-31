package providers

import (
	"context"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

// CardProvider authorises card payments by delegating to a Gateway.
type CardProvider struct {
	Gateway payment.Gateway
}

func (c *CardProvider) Authorize(ctx context.Context, req domain.PaymentRequest) (*domain.PaymentResult, error) {
	return c.Gateway.Charge(ctx, req)
}

// Check lets the gateway refuse a request it could never fulfil. A gateway
// with no requirements of its own (the fake one) accepts everything.
func (c *CardProvider) Check(req domain.PaymentRequest) error {
	return checkWith(c.Gateway, req)
}
