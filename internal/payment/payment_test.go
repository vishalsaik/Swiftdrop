package payment_test

import (
	"context"
	"errors"
	"testing"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/payment/providers"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

var anyReq = domain.PaymentRequest{
	OrderID:        "order-1",
	IdempotencyKey: "idem-1",
	AmountCents:    1500,
	Currency:       "USD",
}

func testRouter() *payment.Router {
	mock := &payment.MockGateway{}
	return payment.NewRouter(
		&providers.CardProvider{Gateway: mock},
		&providers.CashProvider{},
		&providers.WalletProvider{Gateway: mock},
	)
}

// ---------------------------------------------------------------------------
// MockGateway
// ---------------------------------------------------------------------------

func TestMockGateway_DefaultAuthorized(t *testing.T) {
	m := &payment.MockGateway{}
	result, err := m.Charge(context.Background(), anyReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != domain.PaymentAuthorized {
		t.Errorf("status = %s, want authorized", result.Status)
	}
	if result.PaymentID == "" {
		t.Error("PaymentID should not be empty")
	}
}

func TestMockGateway_ConfiguredResult(t *testing.T) {
	want := &domain.PaymentResult{PaymentID: "pay-99", Status: domain.PaymentDeclined, Reason: "insufficient funds"}
	m := &payment.MockGateway{Result: want}
	got, err := m.Charge(context.Background(), anyReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestMockGateway_ConfiguredError(t *testing.T) {
	boom := errors.New("network timeout")
	m := &payment.MockGateway{Err: boom}
	_, err := m.Charge(context.Background(), anyReq)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

// ---------------------------------------------------------------------------
// Router
// ---------------------------------------------------------------------------

func TestRouter_ReturnsCorrectProvider(t *testing.T) {
	r := testRouter()
	for _, pt := range domain.PaymentTypes {
		t.Run(string(pt), func(t *testing.T) {
			p, err := r.For(pt)
			if err != nil {
				t.Fatalf("For(%q) unexpected error: %v", pt, err)
			}
			if p == nil {
				t.Errorf("For(%q) returned nil provider", pt)
			}
		})
	}
}

func TestRouter_UnknownTypeErrors(t *testing.T) {
	r := testRouter()
	_, err := r.For("crypto")
	if err == nil {
		t.Error("expected error for unregistered payment type, got nil")
	}
}

func TestRouter_EmptyTypeErrors(t *testing.T) {
	r := testRouter()
	_, err := r.For("")
	if err == nil {
		t.Error("expected error for empty payment type, got nil")
	}
}

func TestRouter_ProviderExecutesCorrectly(t *testing.T) {
	r := testRouter()
	p, _ := r.For(domain.PaymentCash)
	result, err := p.Authorize(context.Background(), anyReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != domain.PaymentAuthorized {
		t.Errorf("cash: status = %s, want authorized", result.Status)
	}
}
