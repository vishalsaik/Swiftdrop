package providers

import (
	"context"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

// WalletProvider authorises payments from a stored wallet balance
// by delegating to a Gateway.
type WalletProvider struct {
	Gateway payment.Gateway
}

func (w *WalletProvider) Authorize(ctx context.Context, req domain.PaymentRequest) (*domain.PaymentResult, error) {
	return w.Gateway.Charge(ctx, req)
}
