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

// Check lets the gateway refuse a request it could never fulfil.
func (w *WalletProvider) Check(req domain.PaymentRequest) error {
	return checkWith(w.Gateway, req)
}
