package payment

import (
	"context"

	"swiftdrop/internal/domain"
)

// Gateway is the single adapter contract for any external payment processor.
// Both CardProvider and WalletProvider hold one — concrete adapters
// (StripeGateway, WalletServiceGateway, etc.) implement this interface.
type Gateway interface {
	Charge(ctx context.Context, req domain.PaymentRequest) (*domain.PaymentResult, error)
}
