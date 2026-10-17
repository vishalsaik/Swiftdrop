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

var _ Gateway = (*FakeGateway)(nil)

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
