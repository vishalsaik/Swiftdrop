package gateways

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

const whSecret = "whsec_test_secret"

var whNow = time.Unix(1_800_000_000, 0)

// sign computes the v1 signature Stripe would put in the header.
func sign(secret string, at time.Time, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", at.Unix(), payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// header builds a Stripe-Signature header that is valid for the payload.
func header(secret string, at time.Time, payload string) http.Header {
	h := http.Header{}
	h.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", at.Unix(), sign(secret, at, payload)))
	return h
}

func webhook() *StripeWebhook {
	w := NewStripeWebhook(whSecret)
	w.now = func() time.Time { return whNow }
	return w
}

// event builds the JSON of a Stripe event. request is the raw JSON of its
// "request" member.
func event(typ, request, intent string) string {
	return fmt.Sprintf(`{"id":"evt_1","object":"event","type":%q,"data":{"object":%s},"request":%s}`, typ, intent, request)
}

const (
	ourRequest   = `{"id":"req_1","idempotency_key":"pay-1"}`
	succeeded    = `{"id":"pi_1","status":"succeeded"}`
	failedIntent = `{"id":"pi_2","status":"requires_payment_method","last_payment_error":{"message":"Your card was declined."}}`
)

// parse signs the payload correctly and parses it.
func parse(t *testing.T, payload string) (payment.Event, error) {
	t.Helper()
	return webhook().ParseEvent([]byte(payload), header(whSecret, whNow, payload))
}

func TestASucceededPaymentBecomesAnAuthorizedEvent(t *testing.T) {
	got, err := parse(t, event("payment_intent.succeeded", ourRequest, succeeded))
	if err != nil {
		t.Fatal(err)
	}
	want := payment.Event{ID: "evt_1", PaymentID: "pay-1", ProviderPaymentID: "pi_1", Status: domain.PaymentAuthorized}
	if got != want {
		t.Errorf("event = %+v, want %+v", got, want)
	}
}

func TestAFailedPaymentBecomesADeclinedEventWithItsReason(t *testing.T) {
	got, err := parse(t, event("payment_intent.payment_failed", ourRequest, failedIntent))
	if err != nil {
		t.Fatal(err)
	}
	want := payment.Event{ID: "evt_1", PaymentID: "pay-1", ProviderPaymentID: "pi_2", Status: domain.PaymentDeclined, Reason: "Your card was declined."}
	if got != want {
		t.Errorf("event = %+v, want %+v", got, want)
	}
}

func TestAFailedPaymentWithoutAMessageGetsTheFallbackReason(t *testing.T) {
	for name, intent := range map[string]string{
		"no error at all":  `{"id":"pi_2"}`,
		"error no message": `{"id":"pi_2","last_payment_error":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parse(t, event("payment_intent.payment_failed", ourRequest, intent))
			if err != nil {
				t.Fatal(err)
			}
			if got.Reason != "payment declined" {
				t.Errorf("reason = %q, want the fallback", got.Reason)
			}
		})
	}
}

// A success carries no reason even if the object happens to hold an old error.
func TestASucceededEventHasNoReason(t *testing.T) {
	intent := `{"id":"pi_1","status":"succeeded","last_payment_error":{"message":"earlier try failed"}}`
	got, err := parse(t, event("payment_intent.succeeded", ourRequest, intent))
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "" {
		t.Errorf("reason = %q, want none", got.Reason)
	}
}

// ---- events that are genuine but not ours to act on --------------------------

func TestEventsWeDoNotActOnAreIgnoredNotRejected(t *testing.T) {
	cases := map[string]string{
		"another event type":          event("charge.succeeded", ourRequest, succeeded),
		"a payment_intent.created":    event("payment_intent.created", ourRequest, succeeded),
		"no request (null)":           event("payment_intent.succeeded", `null`, succeeded),
		"no idempotency key":          event("payment_intent.succeeded", `{"id":"req_1","idempotency_key":null}`, succeeded),
		"an empty idempotency key":    event("payment_intent.succeeded", `{"id":"req_1","idempotency_key":""}`, succeeded),
		"a request of the old string": event("payment_intent.succeeded", `"req_1"`, succeeded),
		"no request member":           `{"id":"evt_1","type":"payment_intent.succeeded","data":{"object":{"id":"pi_1"}}}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parse(t, payload)
			if !errors.Is(err, payment.ErrEventIgnored) {
				t.Errorf("error = %v, want one wrapping ErrEventIgnored", err)
			}
			if errors.Is(err, payment.ErrInvalidEvent) {
				t.Errorf("error = %v must not also be ErrInvalidEvent", err)
			}
		})
	}
}

// ---- requests that are not genuine ---------------------------------------------

func TestRequestsThatFailVerificationAreInvalid(t *testing.T) {
	payload := event("payment_intent.succeeded", ourRequest, succeeded)
	good := sign(whSecret, whNow, payload)
	ts := whNow.Unix()

	cases := map[string]http.Header{
		"no header":                       {},
		"an empty header":                 {"Stripe-Signature": {""}},
		"garbage":                         {"Stripe-Signature": {"nonsense"}},
		"no timestamp":                    {"Stripe-Signature": {"v1=" + good}},
		"no v1 signature":                 {"Stripe-Signature": {fmt.Sprintf("t=%d", ts)}},
		"a timestamp that is text":        {"Stripe-Signature": {"t=yesterday,v1=" + good}},
		"only a v0 signature":             {"Stripe-Signature": {fmt.Sprintf("t=%d,v0=%s", ts, good)}},
		"a signature that is not hex":     {"Stripe-Signature": {fmt.Sprintf("t=%d,v1=zzzz", ts)}},
		"a signature for another secret":  header("whsec_other", whNow, payload),
		"a signature for another payload": header(whSecret, whNow, payload+" "),
		"a signature for another time":    {"Stripe-Signature": {fmt.Sprintf("t=%d,v1=%s", ts+1, good)}},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := webhook().ParseEvent([]byte(payload), h)
			if !errors.Is(err, payment.ErrInvalidEvent) {
				t.Errorf("error = %v, want one wrapping ErrInvalidEvent", err)
			}
			if errors.Is(err, payment.ErrEventIgnored) {
				t.Errorf("error = %v must not also be ErrEventIgnored", err)
			}
		})
	}
}

// A tampered body must fail even when the original signature is attached.
func TestATamperedBodyIsRejected(t *testing.T) {
	payload := event("payment_intent.succeeded", ourRequest, succeeded)
	h := header(whSecret, whNow, payload)
	tampered := strings.Replace(payload, "pay-1", "pay-2", 1)
	if _, err := webhook().ParseEvent([]byte(tampered), h); !errors.Is(err, payment.ErrInvalidEvent) {
		t.Errorf("error = %v, want ErrInvalidEvent", err)
	}
}

func TestTheTimestampMustBeWithinTheTolerance(t *testing.T) {
	payload := event("payment_intent.succeeded", ourRequest, succeeded)
	cases := map[string]struct {
		at time.Time
		ok bool
	}{
		"now":                       {whNow, true},
		"the edge, in the past":     {whNow.Add(-stripeTolerance), true},
		"just past the past edge":   {whNow.Add(-stripeTolerance - time.Second), false},
		"the edge, in the future":   {whNow.Add(stripeTolerance), true},
		"just past the future edge": {whNow.Add(stripeTolerance + time.Second), false},
		"a day old (a replay)":      {whNow.Add(-24 * time.Hour), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := webhook().ParseEvent([]byte(payload), header(whSecret, c.at, payload))
			if c.ok && err != nil {
				t.Errorf("err = %v, want accepted", err)
			}
			if !c.ok && !errors.Is(err, payment.ErrInvalidEvent) {
				t.Errorf("err = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

// While Stripe rolls a secret the header carries a signature for each secret.
func TestAnyOneMatchingSignatureIsEnough(t *testing.T) {
	payload := event("payment_intent.succeeded", ourRequest, succeeded)
	good := sign(whSecret, whNow, payload)
	old := sign("whsec_old", whNow, payload)
	for name, sigs := range map[string]string{
		"good then stale":      "v1=" + good + ",v1=" + old,
		"stale then good":      "v1=" + old + ",v1=" + good,
		"with a v0 and spaces": "v0=abc, v1=" + good,
	} {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			h.Set("Stripe-Signature", fmt.Sprintf("t=%d,%s", whNow.Unix(), sigs))
			if _, err := webhook().ParseEvent([]byte(payload), h); err != nil {
				t.Errorf("err = %v, want accepted", err)
			}
		})
	}
}

// An unconfigured (empty) secret must never verify anything: a signature made
// with an empty key is trivial to forge.
func TestAnEmptySecretRefusesEverything(t *testing.T) {
	payload := event("payment_intent.succeeded", ourRequest, succeeded)
	w := NewStripeWebhook("")
	w.now = func() time.Time { return whNow }
	_, err := w.ParseEvent([]byte(payload), header("", whNow, payload))
	if !errors.Is(err, payment.ErrInvalidEvent) {
		t.Errorf("err = %v, want ErrInvalidEvent", err)
	}
}

// Verification comes first: a body that is not an event is only reported as
// such once the signature has proved it came from Stripe.
func TestABodyThatIsNotAnEventIsInvalid(t *testing.T) {
	_, err := parse(t, `not json`)
	if !errors.Is(err, payment.ErrInvalidEvent) {
		t.Errorf("err = %v, want ErrInvalidEvent", err)
	}
}

func TestAnUnsignedBodyIsNeverParsedAsAnEvent(t *testing.T) {
	payload := event("payment_intent.succeeded", ourRequest, succeeded)
	_, err := webhook().ParseEvent([]byte(payload), http.Header{})
	if err == nil || errors.Is(err, payment.ErrEventIgnored) {
		t.Errorf("err = %v, want a rejection, not an acknowledgement", err)
	}
}

// The constructor wires the real clock and the five minute tolerance.
func TestNewStripeWebhookUsesTheRealClock(t *testing.T) {
	w := NewStripeWebhook(whSecret)
	payload := event("payment_intent.succeeded", ourRequest, succeeded)
	if _, err := w.ParseEvent([]byte(payload), header(whSecret, time.Now(), payload)); err != nil {
		t.Errorf("a freshly signed event was rejected: %v", err)
	}
	if _, err := w.ParseEvent([]byte(payload), header(whSecret, time.Now().Add(-time.Hour), payload)); !errors.Is(err, payment.ErrInvalidEvent) {
		t.Errorf("an hour-old event: err = %v, want ErrInvalidEvent", err)
	}
}
