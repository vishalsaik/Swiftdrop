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
