package payment_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

func TestFakeGatewayDeclinesByAmount(t *testing.T) {
	tests := []struct {
		name   string
		limit  int64
		amount int64
		want   domain.PaymentStatus
	}{
		{"under the limit", 10_000, 9_999, domain.PaymentAuthorized},
		{"exactly the limit is approved", 10_000, 10_000, domain.PaymentAuthorized},
		{"one cent over is declined", 10_000, 10_001, domain.PaymentDeclined},
		{"far over is declined", 10_000, 500_000, domain.PaymentDeclined},
		{"zero amount is approved", 10_000, 0, domain.PaymentAuthorized},
		{"zero limit means no limit", 0, 999_999_999, domain.PaymentAuthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &payment.FakeGateway{LimitCents: tt.limit}
			got, err := g.Charge(context.Background(), domain.PaymentRequest{AmountCents: tt.amount})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Status != tt.want {
				t.Errorf("status = %s, want %s", got.Status, tt.want)
			}
			if got.PaymentID == "" {
				t.Error("PaymentID must always be set")
			}
		})
	}
}

func TestFakeGatewayDeclineExplainsWhy(t *testing.T) {
	g := &payment.FakeGateway{LimitCents: 100}
	got, _ := g.Charge(context.Background(), domain.PaymentRequest{AmountCents: 250})
	if got.Reason == "" || !strings.Contains(got.Reason, "250") || !strings.Contains(got.Reason, "100") {
		t.Errorf("Reason = %q, want it to name the amount and the limit", got.Reason)
	}
	approved, _ := (&payment.FakeGateway{LimitCents: 100}).Charge(context.Background(), domain.PaymentRequest{AmountCents: 50})
	if approved.Reason != "" {
		t.Errorf("an approval must not carry a reason, got %q", approved.Reason)
	}
}

func TestFakeGatewayGivesEachChargeItsOwnID(t *testing.T) {
	g := &payment.FakeGateway{}
	a, _ := g.Charge(context.Background(), domain.PaymentRequest{})
	b, _ := g.Charge(context.Background(), domain.PaymentRequest{})
	if a.PaymentID == b.PaymentID {
		t.Errorf("two charges shared the id %q", a.PaymentID)
	}
}

func TestFakeGatewayStopsWhenTheCallerGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := (&payment.FakeGateway{}).Charge(ctx, domain.PaymentRequest{AmountCents: 1})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got != nil {
		t.Errorf("result = %+v, want nil alongside an error", got)
	}
}
