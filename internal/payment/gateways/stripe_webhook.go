package gateways

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

const (
	stripeSignatureHeader = "Stripe-Signature"

	// stripeTolerance is how far a signed timestamp may be from our clock.
	// Stripe's own libraries default to five minutes. It is what stops someone
	// from replaying a captured request later, and zero would switch the check off.
	stripeTolerance = 5 * time.Minute
)

// StripeWebhook implements payment.EventParser for the events Stripe pushes to
// us. It checks that a request really came from Stripe, then turns the events
// we care about into a payment.Event:
//
//	payment_intent.succeeded       -> authorized
//	payment_intent.payment_failed  -> declined
//	refund.failed                  -> a refund failed (EventRefundFailed)
//	refund.updated, when the refund's status became failed or canceled -> the same
//
// A refund event is matched on the payment intent it refunds, because Stripe
// changes a refund on its own and the event is not caused by a request of ours.
//
// Our payment is found through the event's request.idempotency_key, which
// Stripe fills in from the Idempotency-Key header of the charge that caused the
// event, and that header is our payment id. An event no request of ours caused
// has no key, so it cannot be matched and is ignored.
type StripeWebhook struct {
	secret    []byte
	tolerance time.Duration
	// now is a field so tests can fix the clock.
	now func() time.Time
}

// NewStripeWebhook returns a parser that verifies signatures with the endpoint's
// signing secret (the whsec_ value Stripe shows for the webhook endpoint).
func NewStripeWebhook(secret string) *StripeWebhook {
	return &StripeWebhook{secret: []byte(secret), tolerance: stripeTolerance, now: time.Now}
}

// stripeEvent is the subset of Stripe's Event object we read.
type stripeEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		// Object is a PaymentIntent or a Refund, depending on the type.
		Object json.RawMessage `json:"object"`
	} `json:"data"`
	// Request is raw because Stripe's oldest API versions send a string here
	// instead of an object. Anything that is not an object just has no key.
	Request json.RawMessage `json:"request"`
}

// idempotencyKey returns the key of the API request that caused the event, or
// "" if no request of ours did.
func (e stripeEvent) idempotencyKey() string {
	var req struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	_ = json.Unmarshal(e.Request, &req) // anything that is not an object leaves the key empty
	return req.IdempotencyKey
}

// ParseEvent verifies the request and translates the event.
func (w *StripeWebhook) ParseEvent(payload []byte, header http.Header) (payment.Event, error) {
	if err := w.verify(payload, header.Get(stripeSignatureHeader)); err != nil {
		return payment.Event{}, fmt.Errorf("%w: %v", payment.ErrInvalidEvent, err)
	}

	var e stripeEvent
	if err := json.Unmarshal(payload, &e); err != nil {
		return payment.Event{}, fmt.Errorf("%w: decoding the event: %v", payment.ErrInvalidEvent, err)
	}

	if e.Type == "refund.failed" || e.Type == "refund.updated" {
		return refundEvent(e)
	}

	var intent stripeIntent
	if err := json.Unmarshal(e.Data.Object, &intent); err != nil && isPaymentIntentEvent(e.Type) {
		return payment.Event{}, fmt.Errorf("%w: decoding the payment intent of event %s: %v", payment.ErrInvalidEvent, e.ID, err)
	}

	var status domain.PaymentStatus
	reason := ""
	switch e.Type {
	case "payment_intent.succeeded":
		status = domain.PaymentAuthorized
	case "payment_intent.payment_failed":
		status = domain.PaymentDeclined
		message := ""
		if intent.LastPaymentError != nil {
			message = intent.LastPaymentError.Message
		}
		reason = declineReason(message)
	default:
		return payment.Event{}, fmt.Errorf("%w: %s is not a payment outcome (event %s)", payment.ErrEventIgnored, e.Type, e.ID)
	}

	key := e.idempotencyKey()
	if key == "" {
		return payment.Event{}, fmt.Errorf("%w: event %s (%s) was not caused by one of our payment requests", payment.ErrEventIgnored, e.ID, e.Type)
	}
	return payment.Event{
		ID:                e.ID,
		PaymentID:         key,
		ProviderPaymentID: intent.ID,
		Status:            status,
		Reason:            reason,
	}, nil
}

func isPaymentIntentEvent(eventType string) bool {
	return eventType == "payment_intent.succeeded" || eventType == "payment_intent.payment_failed"
}

// stripeRefundObject is the subset of Stripe's Refund object we read from an event.
type stripeRefundObject struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason"`
	PaymentIntent string `json:"payment_intent"`
}

// refundEvent translates refund.failed, and refund.updated when the refund ended
// up failed or canceled ("cancelled" is accepted too: Stripe's guide spells it
// both ways). Any other refund update is not news.
func refundEvent(e stripeEvent) (payment.Event, error) {
	var r stripeRefundObject
	if err := json.Unmarshal(e.Data.Object, &r); err != nil {
		return payment.Event{}, fmt.Errorf("%w: decoding the refund of event %s: %v", payment.ErrInvalidEvent, e.ID, err)
	}
	failed := e.Type == "refund.failed" || r.Status == "failed" || r.Status == "canceled" || r.Status == "cancelled"
	if !failed {
		return payment.Event{}, fmt.Errorf("%w: refund %s is %q, not a failure (event %s)", payment.ErrEventIgnored, r.ID, r.Status, e.ID)
	}
	if r.ID == "" || r.PaymentIntent == "" {
		return payment.Event{}, fmt.Errorf("%w: event %s (%s) names no refund or no payment intent", payment.ErrEventIgnored, e.ID, e.Type)
	}
	reason := r.FailureReason
	if reason == "" {
		reason = "unknown"
	}
	return payment.Event{
		Kind:              payment.EventRefundFailed,
		ID:                e.ID,
		ProviderPaymentID: r.PaymentIntent,
		RefundID:          r.ID,
		Reason:            reason,
	}, nil
}

// verify checks the Stripe-Signature header: "t=<unix time>,v1=<hex>,...". The
// signature is an HMAC-SHA256, keyed with the endpoint secret, of
// "<t>.<raw body>". It must be computed over the bytes exactly as received, so
// the body is never decoded and re-encoded first.
func (w *StripeWebhook) verify(payload []byte, header string) error {
	if len(w.secret) == 0 {
		// An empty key would let anyone forge a valid signature.
		return errors.New("no signing secret is configured")
	}
	if header == "" {
		return errors.New("missing " + stripeSignatureHeader + " header")
	}

	var timestamp string
	var signatures []string
	for part := range strings.SplitSeq(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			timestamp = value
		case "v1": // other schemes (Stripe's test-only v0) are ignored on purpose
			signatures = append(signatures, value)
		}
	}
	if timestamp == "" || len(signatures) == 0 {
		return errors.New("malformed " + stripeSignatureHeader + " header")
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("malformed signature timestamp")
	}

	mac := hmac.New(sha256.New, w.secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := mac.Sum(nil)

	// While Stripe rolls a secret it signs with both the old and the new one, so
	// the header can carry several v1 values: any one matching is enough.
	matched := false
	for _, s := range signatures {
		got, err := hex.DecodeString(s)
		if err != nil {
			continue
		}
		if hmac.Equal(got, expected) { // constant time
			matched = true
		}
	}
	if !matched {
		return errors.New("signature does not match")
	}

	age := w.now().Sub(time.Unix(seconds, 0))
	if age < 0 {
		age = -age
	}
	if age > w.tolerance {
		return fmt.Errorf("signature timestamp is %s from now, more than the allowed %s", age.Round(time.Second), w.tolerance)
	}
	return nil
}
