package gateways

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"swiftdrop/internal/domain"
)

// StripeGateway implements payment.Gateway using Stripe's Payment Intents API.
type StripeGateway struct {
	apiKey string
	client *http.Client
}

func NewStripeGateway(apiKey string) *StripeGateway {
	return &StripeGateway{
		apiKey: apiKey,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// stripeIntent is the subset of Stripe's PaymentIntent response we care about.
type stripeIntent struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	LastPaymentError *struct {
		Message string `json:"message"`
	} `json:"last_payment_error"`
}

// Charge translates a domain.PaymentRequest into a Stripe PaymentIntent,
// calls the Stripe API, then translates the response back to domain.PaymentResult.
func (g *StripeGateway) Charge(ctx context.Context, req domain.PaymentRequest) (*domain.PaymentResult, error) {
	// Translate: domain → Stripe API format
	form := url.Values{
		"amount":         {fmt.Sprintf("%d", req.AmountCents)},
		"currency":       {strings.ToLower(req.Currency)},
		"payment_method": {req.PaymentMethodID},
		"confirm":        {"true"},
		"automatic_payment_methods[enabled]":         {"true"},
		"automatic_payment_methods[allow_redirects]": {"never"},
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		"https://api.stripe.com/v1/payment_intents",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("stripe: building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "Bearer "+g.apiKey)
	if req.IdempotencyKey != "" {
		httpReq.Header.Set("Idempotency-Key", req.IdempotencyKey)
	}

	resp, err := g.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("stripe: request failed: %w", err)
	}
	defer resp.Body.Close()

	var intent stripeIntent
	if err := json.NewDecoder(resp.Body).Decode(&intent); err != nil {
		return nil, fmt.Errorf("stripe: decoding response: %w", err)
	}

	// Translate: Stripe response → domain.PaymentResult
	if intent.Status == "succeeded" {
		return &domain.PaymentResult{
			PaymentID: intent.ID,
			Status:    domain.PaymentAuthorized,
		}, nil
	}

	reason := "payment declined"
	if intent.LastPaymentError != nil {
		reason = intent.LastPaymentError.Message
	}
	return &domain.PaymentResult{
		PaymentID: intent.ID,
		Status:    domain.PaymentDeclined,
		Reason:    reason,
	}, nil
}
