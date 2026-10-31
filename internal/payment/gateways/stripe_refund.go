package gateways

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

var _ payment.Refunder = (*StripeGateway)(nil)

// alreadyRefundedID is stored as the refund id when Stripe says the charge was
// already refunded in full: the money is back, which is what we wanted, but we
// were not told the refund's own id.
const alreadyRefundedID = "already_refunded"

// stripeRefund is the subset of Stripe's Refund object we read.
type stripeRefund struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Refund gives the whole amount of a charge back through Stripe's Refunds API.
//
// "succeeded" and "pending" are both a yes: Stripe has accepted the refund and
// the money is on its way (a pending refund finishes on its own, it can take
// days to show on the customer's statement). "failed", "canceled" and
// "requires_action" mean the money is not coming back by this refund, so they
// are errors and the caller tries again later. A charge that was already
// refunded is a success: asking twice must never be a problem.
func (g *StripeGateway) Refund(ctx context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	if req.ProviderPaymentID == "" {
		return nil, errors.New("stripe: refund needs the provider payment id")
	}
	if req.AmountCents <= 0 {
		return nil, fmt.Errorf("stripe: refund amount must be positive, got %d", req.AmountCents)
	}
	form := url.Values{
		"payment_intent": {req.ProviderPaymentID},
		"amount":         {fmt.Sprintf("%d", req.AmountCents)},
	}
	status, body, err := g.post(ctx, "/v1/refunds", form, req.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if status >= 200 && status < 300 {
		return refundResult(body)
	}

	var parsed stripeErrorBody
	_ = json.Unmarshal(body, &parsed) // an unreadable body just leaves the fields empty
	e := parsed.Error
	if status == http.StatusBadRequest && e.Code == "charge_already_refunded" {
		return &domain.RefundResult{RefundID: alreadyRefundedID}, nil
	}
	return nil, fmt.Errorf("stripe: refund: HTTP %d (type %q, code %q): %s", status, e.Type, e.Code, e.Message)
}

func refundResult(body []byte) (*domain.RefundResult, error) {
	var r stripeRefund
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("stripe: decoding refund: %w", err)
	}
	switch r.Status {
	case "succeeded", "pending":
		if r.ID == "" {
			return nil, errors.New("stripe: refund accepted but its reply has no id")
		}
		return &domain.RefundResult{RefundID: r.ID}, nil
	default:
		return nil, fmt.Errorf("stripe: refund %q has status %q, so the money is not on its way back", r.ID, r.Status)
	}
}
