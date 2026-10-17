package providers_test

import (
	"context"
	"errors"
	"testing"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/payment/providers"
)

var anyReq = domain.PaymentRequest{
	OrderID:        "order-1",
	IdempotencyKey: "idem-1",
	AmountCents:    1500,
	Currency:       "USD",
}

// ---------------------------------------------------------------------------
// CashProvider
// ---------------------------------------------------------------------------

func TestCashProvider_AlwaysAuthorized(t *testing.T) {
	c := &providers.CashProvider{}
	result, err := c.Authorize(context.Background(), anyReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != domain.PaymentAuthorized {
		t.Errorf("status = %s, want authorized", result.Status)
	}
}

func TestCashProvider_UniquePaymentIDs(t *testing.T) {
	c := &providers.CashProvider{}
	r1, _ := c.Authorize(context.Background(), anyReq)
	r2, _ := c.Authorize(context.Background(), anyReq)
	if r1.PaymentID == "" || r2.PaymentID == "" {
		t.Error("PaymentID must not be empty")
	}
	if r1.PaymentID == r2.PaymentID {
		t.Error("each Authorize call should produce a unique PaymentID")
	}
}

// ---------------------------------------------------------------------------
// CardProvider
// ---------------------------------------------------------------------------

func TestCardProvider_DelegatesToGateway(t *testing.T) {
	want := &domain.PaymentResult{PaymentID: "pi_stripe_123", Status: domain.PaymentAuthorized}
	p := &providers.CardProvider{Gateway: &payment.MockGateway{Result: want}}
	got, err := p.Authorize(context.Background(), anyReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestCardProvider_GatewayDeclined(t *testing.T) {
	declined := &domain.PaymentResult{PaymentID: "pi_x", Status: domain.PaymentDeclined, Reason: "card expired"}
	p := &providers.CardProvider{Gateway: &payment.MockGateway{Result: declined}}
	result, err := p.Authorize(context.Background(), anyReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != domain.PaymentDeclined {
		t.Errorf("status = %s, want declined", result.Status)
	}
	if result.Reason != "card expired" {
		t.Errorf("reason = %q, want %q", result.Reason, "card expired")
	}
}

func TestCardProvider_GatewayError(t *testing.T) {
	boom := errors.New("stripe unreachable")
	p := &providers.CardProvider{Gateway: &payment.MockGateway{Err: boom}}
	_, err := p.Authorize(context.Background(), anyReq)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

// ---------------------------------------------------------------------------
// WalletProvider
// ---------------------------------------------------------------------------

func TestWalletProvider_DelegatesToGateway(t *testing.T) {
	want := &domain.PaymentResult{PaymentID: "wal_456", Status: domain.PaymentAuthorized}
	p := &providers.WalletProvider{Gateway: &payment.MockGateway{Result: want}}
	got, err := p.Authorize(context.Background(), anyReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestWalletProvider_InsufficientFunds(t *testing.T) {
	declined := &domain.PaymentResult{PaymentID: "wal_x", Status: domain.PaymentDeclined, Reason: "insufficient funds"}
	p := &providers.WalletProvider{Gateway: &payment.MockGateway{Result: declined}}
	result, err := p.Authorize(context.Background(), anyReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != domain.PaymentDeclined {
		t.Errorf("status = %s, want declined", result.Status)
	}
}
