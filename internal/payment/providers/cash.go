package providers

import (
	"context"

	"github.com/google/uuid"
	"swiftdrop/internal/domain"
)

// CashProvider handles cash-on-delivery orders. There is no external
// gateway: the payment is authorised immediately by recording intent —
// the customer pays the driver on delivery.
type CashProvider struct{}

func (c *CashProvider) Authorize(_ context.Context, _ domain.PaymentRequest) (*domain.PaymentResult, error) {
	return &domain.PaymentResult{
		PaymentID: uuid.New().String(),
		Status:    domain.PaymentAuthorized,
	}, nil
}
