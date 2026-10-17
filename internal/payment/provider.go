package payment

import (
	"context"
	"swiftdrop/internal/domain"
)

type PaymentProvider interface {
	Authorize(ctx context.Context, req domain.PaymentRequest) (*domain.PaymentResult, error)
}
