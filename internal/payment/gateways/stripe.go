package gateways

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"swiftdrop/internal/domain"
)

const (
	stripeBaseURL = "https://api.stripe.com"

	// maxResponseBytes caps how much of a reply we read, so a misbehaving
	// server cannot make us buffer an unbounded body.
	maxResponseBytes = 1 << 20
)

// StripeGateway implements payment.Gateway using Stripe's Payment Intents API.
type StripeGateway struct {
	apiKey string
	client *http.Client
	// baseURL is a field, not only a constant, so tests can point the gateway
	// at an httptest server instead of the real Stripe.
	baseURL string
}

func NewStripeGateway(apiKey string) *StripeGateway {
	return &StripeGateway{
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 30 * time.Second},
		baseURL: stripeBaseURL,
	}
}

// stripeIntent is the subset of Stripe's PaymentIntent object we care about.
type stripeIntent struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	LastPaymentError *struct {
		Message string `json:"message"`
	} `json:"last_payment_error"`
}

// stripeErrorBody is the envelope Stripe wraps every failed request in:
// {"error": {"type": ..., "code": ..., "message": ..., "payment_intent": {...}}}.
type stripeErrorBody struct {
	Error struct {
		Type          string        `json:"type"`
		Code          string        `json:"code"`
		Message       string        `json:"message"`
		PaymentIntent *stripeIntent `json:"payment_intent"`
	} `json:"error"`
}

// Charge translates a domain.PaymentRequest into a Stripe PaymentIntent,
// calls the Stripe API, then translates the answer back.
//
// The contract with the caller is that a decline and an error are different
// things. A decline is a definite "no": the card was refused and no money
// moved, so it comes back as a PaymentDeclined result with a nil error. An
// error means we do not know what happened (bad key, rate limit, Stripe is
// down, a payment still processing), so the caller must not treat it as a
// decline and cancel the order; it comes back as a non-nil error.
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
		g.baseURL+"/v1/payment_intents",
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("stripe: reading response: %w", err)
	}

	// Translate: Stripe response → domain.PaymentResult. The HTTP status
	// comes first: a rejected request has no PaymentIntent to look at.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return intentResult(body)
	}
	return failureResult(resp.StatusCode, body)
}

// intentResult handles a 2xx reply, where the body is a PaymentIntent. Only
// "succeeded" is a definite yes. A decline that Stripe reports as a status
// (requires_payment_method with a last_payment_error) is a definite no. Every
// other status is a payment we cannot call either way yet, such as
// "processing" (money may still move) or "requires_action" (the customer must
// authenticate), so it is an error and the order stays pending.
func intentResult(body []byte) (*domain.PaymentResult, error) {
	var intent stripeIntent
	if err := json.Unmarshal(body, &intent); err != nil {
		return nil, fmt.Errorf("stripe: decoding response: %w", err)
	}
	switch {
	case intent.Status == "succeeded":
		return &domain.PaymentResult{PaymentID: intent.ID, Status: domain.PaymentAuthorized}, nil
	case intent.Status == "requires_payment_method" && intent.LastPaymentError != nil:
		return &domain.PaymentResult{
			PaymentID: intent.ID,
			Status:    domain.PaymentDeclined,
			Reason:    declineReason(intent.LastPaymentError.Message),
		}, nil
	default:
		return nil, fmt.Errorf("stripe: payment intent %q has status %q, which this gateway cannot resolve", intent.ID, intent.Status)
	}
}

// failureResult handles a non-2xx reply. Only HTTP 402 with a card_error is a
// decline (Stripe's "the parameters were valid but the card was refused").
// Everything else, a wrong key (401), a bad request (400), a rate limit (429),
// an idempotency conflict (409) or a Stripe outage (5xx), is an error.
func failureResult(status int, body []byte) (*domain.PaymentResult, error) {
	var parsed stripeErrorBody
	_ = json.Unmarshal(body, &parsed) // an unreadable body just leaves the fields empty
	e := parsed.Error

	if status == http.StatusPaymentRequired && e.Type == "card_error" {
		result := &domain.PaymentResult{Status: domain.PaymentDeclined, Reason: declineReason(e.Message)}
		if e.PaymentIntent != nil {
			result.PaymentID = e.PaymentIntent.ID
		}
		return result, nil
	}
	return nil, fmt.Errorf("stripe: HTTP %d (type %q, code %q): %s", status, e.Type, e.Code, e.Message)
}

// declineReason is what the customer is told. Stripe's card-error messages are
// written to be shown to users; an empty one gets a plain fallback.
func declineReason(message string) string {
	if message == "" {
		return "payment declined"
	}
	return message
}
