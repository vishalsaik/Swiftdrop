package payment_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

func TestFakeGatewayRefundsEverythingAndRepeatsItsAnswerForTheSameKey(t *testing.T) {
	g := &payment.FakeGateway{LimitCents: 100}
	req := domain.RefundRequest{PaymentID: "p1", ProviderPaymentID: "fake-1", AmountCents: 999_999, IdempotencyKey: "refund-p1"}

	first, err := g.Refund(context.Background(), req)
	if err != nil || first.RefundID == "" {
		t.Fatalf("Refund = %+v, %v, want an accepted refund with an id", first, err)
	}
	again, err := g.Refund(context.Background(), req)
	if err != nil || again.RefundID != first.RefundID {
		t.Errorf("same key gave %+v, %v, want the same refund id %q", again, err, first.RefundID)
	}
	other, _ := g.Refund(context.Background(), domain.RefundRequest{IdempotencyKey: "refund-p2"})
	if other.RefundID == first.RefundID {
		t.Error("different keys must give different refund ids")
	}
}

func TestFakeGatewayRefundWithoutAKeyGetsAFreshId(t *testing.T) {
	g := &payment.FakeGateway{}
	a, _ := g.Refund(context.Background(), domain.RefundRequest{})
	b, _ := g.Refund(context.Background(), domain.RefundRequest{})
	if a.RefundID == "" || a.RefundID == b.RefundID {
		t.Errorf("ids %q and %q, want two different non-empty ids", a.RefundID, b.RefundID)
	}
}

func TestFakeGatewayRefundStopsWhenTheCallerGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := (&payment.FakeGateway{}).Refund(ctx, domain.RefundRequest{}); err == nil || got != nil {
		t.Errorf("got %+v, %v, want the context error", got, err)
	}
}

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

func TestFakeGatewayKeepsNoRecordSoALookupFindsNothing(t *testing.T) {
	g := &payment.FakeGateway{}
	res, err := g.Lookup(context.Background(), domain.LookupRequest{PaymentID: "p1"})
	if err != nil || res == nil || res.Found {
		t.Errorf("Lookup = %+v, %v, want an answer that says not found", res, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Lookup(ctx, domain.LookupRequest{PaymentID: "p1"}); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context gave %v, want context.Canceled", err)
	}
}
