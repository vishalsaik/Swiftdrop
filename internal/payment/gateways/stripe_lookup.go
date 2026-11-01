package gateways

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

var _ payment.Looker = (*StripeGateway)(nil)

const (
	// lookupPageSize is Stripe's largest page for a list call.
	lookupPageSize = 100
	// lookupMaxPages bounds one lookup. A lookup that has to give up before it has
	// seen every payment intent since the claim must not say "not found": that
	// would abandon a payment that may have been charged.
	lookupMaxPages = 20
	// lookupClockSlack is subtracted from the claim time when asking for the
	// intents created since, so a few seconds between our clock and Stripe's never
	// hide the one we are looking for.
	lookupClockSlack = time.Minute
)

// stripeIntentWithMetadata is a PaymentIntent as the list call returns it.
type stripeIntentWithMetadata struct {
	stripeIntent
	Metadata map[string]string `json:"metadata"`
}

// stripeIntentList is Stripe's list envelope.
type stripeIntentList struct {
	Data    []stripeIntentWithMetadata `json:"data"`
	HasMore bool                       `json:"has_more"`
}

// Lookup finds the payment intent our charge created, without charging again.
//
// Charge puts our payment id in the intent's metadata, and a lost reply is exactly
// the case where we do not know Stripe's own id, so this lists the intents created
// since the payment was claimed and picks the one with our id. (Stripe's search
// endpoint would do it in one call, but it is eventually consistent and not
// offered to every account; a list is always current.) A payment claimed before
// metadata was sent cannot be found this way.
//
// The answer follows Charge: "succeeded" is authorized, a refused payment is
// declined, a canceled intent moved no money and counts as declined, and every
// other status is found but not decided. No intent with our id after reading every
// page is "not found"; failing to read every page is an error, never "not found".
func (g *StripeGateway) Lookup(ctx context.Context, req domain.LookupRequest) (*domain.LookupResult, error) {
	if req.PaymentID == "" {
		return nil, fmt.Errorf("stripe: lookup needs the payment id")
	}
	since := max(req.CreatedAt.Add(-lookupClockSlack).Unix(), 0)
	startingAfter := ""
	for range lookupMaxPages {
		query := url.Values{
			"limit":        {strconv.Itoa(lookupPageSize)},
			"created[gte]": {strconv.FormatInt(since, 10)},
		}
		if startingAfter != "" {
			query.Set("starting_after", startingAfter)
		}
		status, body, err := g.get(ctx, "/v1/payment_intents", query)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			var parsed stripeErrorBody
			_ = json.Unmarshal(body, &parsed) // an unreadable body just leaves the fields empty
			e := parsed.Error
			return nil, fmt.Errorf("stripe: lookup: HTTP %d (type %q, code %q): %s", status, e.Type, e.Code, e.Message)
		}
		var page stripeIntentList
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("stripe: decoding payment intents: %w", err)
		}
		for _, intent := range page.Data {
			if intent.Metadata["payment_id"] == req.PaymentID {
				return lookupResult(intent.stripeIntent), nil
			}
		}
		if !page.HasMore || len(page.Data) == 0 {
			return &domain.LookupResult{Found: false}, nil
		}
		startingAfter = page.Data[len(page.Data)-1].ID
	}
	return nil, fmt.Errorf("stripe: lookup: more than %d payment intents since the payment was claimed, not looked through", lookupMaxPages*lookupPageSize)
}

// lookupResult translates the intent we found into what we know about the payment.
func lookupResult(intent stripeIntent) *domain.LookupResult {
	switch {
	case intent.Status == "succeeded":
		return &domain.LookupResult{Found: true, Status: domain.PaymentAuthorized, ProviderPaymentID: intent.ID}
	case intent.Status == "requires_payment_method" && intent.LastPaymentError != nil:
		return &domain.LookupResult{Found: true, Status: domain.PaymentDeclined, ProviderPaymentID: intent.ID, Reason: declineReason(intent.LastPaymentError.Message)}
	case intent.Status == "canceled":
		return &domain.LookupResult{Found: true, Status: domain.PaymentDeclined, ProviderPaymentID: intent.ID, Reason: "the payment was canceled"}
	default:
		// processing, requires_action and the rest: Stripe has it, nothing is decided.
		return &domain.LookupResult{Found: true, ProviderPaymentID: intent.ID}
	}
}

// get sends one GET to the Stripe API, like post: a non-nil error means no answer
// was read at all, and an answer of any status is not an error here.
func (g *StripeGateway) get(ctx context.Context, path string, query url.Values) (int, []byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return 0, nil, fmt.Errorf("stripe: building request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+g.apiKey)

	resp, err := g.client.Do(httpReq)
	if err != nil {
		return 0, nil, fmt.Errorf("stripe: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("stripe: reading response: %w", err)
	}
	return resp.StatusCode, body, nil
}
