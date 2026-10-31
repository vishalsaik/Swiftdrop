package providers_test

import (
	"context"
	"errors"
	"testing"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/payment/providers"
)

// pickyGateway refuses requests without a payment method, like Stripe's.
type pickyGateway struct{ payment.MockGateway }

func (*pickyGateway) Check(req domain.PaymentRequest) error {
	if req.PaymentMethodID == "" {
		return payment.ErrInvalidRequest
	}
	return nil
}

var (
	_ payment.RequestChecker = (*providers.CardProvider)(nil)
	_ payment.RequestChecker = (*providers.WalletProvider)(nil)
)

func TestCardAndWalletProvidersLetTheirGatewayRefuseARequest(t *testing.T) {
	withMethod := domain.PaymentRequest{PaymentMethodID: "pm_x"}
	without := domain.PaymentRequest{}
	checkers := map[string]payment.RequestChecker{
		"card":   &providers.CardProvider{Gateway: &pickyGateway{}},
		"wallet": &providers.WalletProvider{Gateway: &pickyGateway{}},
	}
	for name, c := range checkers {
		t.Run(name, func(t *testing.T) {
			if err := c.Check(withMethod); err != nil {
				t.Errorf("Check with a method = %v, want accepted", err)
			}
			if err := c.Check(without); !errors.Is(err, payment.ErrInvalidRequest) {
				t.Errorf("Check without a method = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

// A gateway that does not implement RequestChecker has no requirements.
func TestCardAndWalletProvidersAcceptWhenTheirGatewayHasNoRequirements(t *testing.T) {
	checkers := map[string]payment.RequestChecker{
		"card":   &providers.CardProvider{Gateway: &payment.MockGateway{}},
		"wallet": &providers.WalletProvider{Gateway: &payment.FakeGateway{LimitCents: 100}},
	}
	for name, c := range checkers {
		t.Run(name, func(t *testing.T) {
			if err := c.Check(domain.PaymentRequest{}); err != nil {
				t.Errorf("Check = %v, want accepted", err)
			}
		})
	}
}

// Cash needs no payment method, so it has nothing to check.
func TestCashProviderIsNotARequestChecker(t *testing.T) {
	var p payment.PaymentProvider = &providers.CashProvider{}
	if _, ok := p.(payment.RequestChecker); ok {
		t.Error("CashProvider must not require anything of the request")
	}
	if _, err := p.Authorize(context.Background(), domain.PaymentRequest{}); err != nil {
		t.Errorf("cash without a method: %v", err)
	}
}
