package payment

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"swiftdrop/internal/domain"
)

// FakeGateway is a local stand-in for a real payment processor, so the app
// runs end to end with no network and no API key. Unlike MockGateway, whose
// result a test sets by hand, FakeGateway decides by itself and always the
// same way: an amount over LimitCents is declined, anything else is
// authorised. That makes a decline reproducible from curl.
type FakeGateway struct {
	// LimitCents is the largest amount that is still approved. Zero means no
	// limit, so a zero-value FakeGateway approves everything.
	LimitCents int64
}

var (
	_ Gateway  = (*FakeGateway)(nil)
	_ Refunder = (*FakeGateway)(nil)
	_ Looker   = (*FakeGateway)(nil)
)

// Lookup always says the fake has no record of the payment: it charges and
// answers in the same call and keeps nothing, so a payment whose answer was lost
// never happened as far as it is concerned.
func (f *FakeGateway) Lookup(ctx context.Context, _ domain.LookupRequest) (*domain.LookupResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &domain.LookupResult{Found: false}, nil
}

// Refund always accepts, and gives back an id that is the same for the same
// idempotency key, like a real provider's replay of an earlier answer.
func (f *FakeGateway) Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := req.IdempotencyKey
	if id == "" {
		id = uuid.New().String()
	}
	return &domain.RefundResult{RefundID: "fake-refund-" + id}, nil
}

func (f *FakeGateway) Charge(ctx context.Context, req domain.PaymentRequest) (*domain.PaymentResult, error) {
	// A real gateway gives up when the caller does; the fake keeps that
	// behaviour so cancellation can be tested without a network.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.LimitCents > 0 && req.AmountCents > f.LimitCents {
		return &domain.PaymentResult{
			PaymentID: "fake-" + uuid.New().String(),
			Status:    domain.PaymentDeclined,
			Reason:    fmt.Sprintf("amount %d cents is over the limit of %d cents", req.AmountCents, f.LimitCents),
		}, nil
	}
	return &domain.PaymentResult{
		PaymentID: "fake-" + uuid.New().String(),
		Status:    domain.PaymentAuthorized,
	}, nil
}
