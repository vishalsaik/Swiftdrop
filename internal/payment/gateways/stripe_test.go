package gateways

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"swiftdrop/internal/domain"
)

const testKey = "test-api-key-123"

var chargeReq = domain.PaymentRequest{
	OrderID:         "o1",
	AmountCents:     1447,
	Currency:        "USD",
	PaymentMethodID: "pm_test_token",
}

// stripeStub starts a local server that plays Stripe and returns a gateway
// pointed at it. The server is closed when the test ends.
func stripeStub(t *testing.T, h http.HandlerFunc) *StripeGateway {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	g := NewStripeGateway(testKey)
	g.baseURL = srv.URL
	return g
}

// reply answers every request with the given status and JSON body.
func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// ---------------------------------------------------------------------------
// what we send
// ---------------------------------------------------------------------------

func TestStripeSendsTheChargeAsAPaymentIntent(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotType string
	var gotForm url.Values
	g := stripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth, gotType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		_ = r.ParseForm()
		gotForm = r.PostForm
		reply(200, `{"id":"pi_1","status":"succeeded"}`)(w, r)
	})

	if _, err := g.Charge(context.Background(), chargeReq); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/payment_intents" {
		t.Errorf("request = %s %s, want POST /v1/payment_intents", gotMethod, gotPath)
	}
	if gotAuth != "Bearer "+testKey {
		t.Errorf("Authorization = %q, want the bearer key", gotAuth)
	}
	if gotType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want form encoding", gotType)
	}
	for field, want := range map[string]string{
		"amount":         "1447",
		"currency":       "usd", // Stripe wants lower case
		"payment_method": "pm_test_token",
		"confirm":        "true",
	} {
		if got := gotForm.Get(field); got != want {
			t.Errorf("form %s = %q, want %q", field, got, want)
		}
	}
}

func TestStripeForwardsTheIdempotencyKeyOnlyWhenThereIsOne(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"with a key", "idem-42", "idem-42"},
		{"without a key", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			var present bool
			g := stripeStub(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("Idempotency-Key")
				_, present = r.Header["Idempotency-Key"]
				reply(200, `{"id":"pi_1","status":"succeeded"}`)(w, r)
			})
			req := chargeReq
			req.IdempotencyKey = tt.key
			if _, err := g.Charge(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if got != tt.want || present != (tt.key != "") {
				t.Errorf("Idempotency-Key = %q (present=%v), want %q", got, present, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// what Stripe says back: definite yes, definite no, or "we do not know"
// ---------------------------------------------------------------------------

func TestStripeSucceededIsAuthorized(t *testing.T) {
	g := stripeStub(t, reply(200, `{"id":"pi_ok","status":"succeeded"}`))
	got, err := g.Charge(context.Background(), chargeReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != domain.PaymentAuthorized || got.PaymentID != "pi_ok" || got.Reason != "" {
		t.Errorf("result = %+v, want authorized pi_ok with no reason", got)
	}
}

func TestStripeCardDeclinesAreDeclines(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantID     string
		wantReason string
	}{
		{
			"402 card_error, as Stripe sends a declined card",
			402,
			`{"error":{"type":"card_error","code":"card_declined","decline_code":"generic_decline",
			  "message":"Your card was declined.","payment_intent":{"id":"pi_dec","status":"requires_payment_method"}}}`,
			"pi_dec", "Your card was declined.",
		},
		{
			"402 card_error with no message gets a fallback",
			402,
			`{"error":{"type":"card_error","payment_intent":{"id":"pi_dec2"}}}`,
			"pi_dec2", "payment declined",
		},
		{
			"402 card_error with no payment intent still declines",
			402,
			`{"error":{"type":"card_error","message":"Your card has insufficient funds."}}`,
			"", "Your card has insufficient funds.",
		},
		{
			"200 requires_payment_method with a last_payment_error",
			200,
			`{"id":"pi_dec3","status":"requires_payment_method","last_payment_error":{"message":"Card expired."}}`,
			"pi_dec3", "Card expired.",
		},
		{
			"200 requires_payment_method with an empty error message",
			200,
			`{"id":"pi_dec4","status":"requires_payment_method","last_payment_error":{}}`,
			"pi_dec4", "payment declined",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := stripeStub(t, reply(tt.status, tt.body))
			got, err := g.Charge(context.Background(), chargeReq)
			if err != nil {
				t.Fatalf("a decline must not be an error, got %v", err)
			}
			if got.Status != domain.PaymentDeclined || got.PaymentID != tt.wantID || got.Reason != tt.wantReason {
				t.Errorf("result = %+v, want declined id=%q reason=%q", got, tt.wantID, tt.wantReason)
			}
		})
	}
}

// Everything below is "we do not know what happened", so it must be an error
// and never a decline: a decline cancels the customer's order.
func TestStripeFailuresAreErrorsNotDeclines(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string // a fragment the error must carry, for the log
	}{
		{"400 missing payment method", 400, `{"error":{"type":"invalid_request_error","code":"parameter_missing","message":"Missing required param: payment_method."}}`, "invalid_request_error"},
		{"401 wrong key", 401, `{"error":{"type":"invalid_request_error","message":"Invalid API Key provided."}}`, "HTTP 401"},
		{"403 key lacks permission", 403, `{"error":{"type":"invalid_request_error","message":"This API key does not have permission."}}`, "HTTP 403"},
		{"404", 404, `{"error":{"type":"invalid_request_error","message":"No such payment_method."}}`, "HTTP 404"},
		{"409 idempotency conflict", 409, `{"error":{"type":"idempotency_error","message":"Keys for idempotent requests can only be used with the same parameters."}}`, "idempotency_error"},
		{"424 dependency failed", 424, `{"error":{"type":"api_error","message":"dependency failed"}}`, "HTTP 424"},
		{"429 rate limited", 429, `{"error":{"type":"invalid_request_error","message":"Too many requests."}}`, "HTTP 429"},
		{"500", 500, `{"error":{"type":"api_error","message":"Something went wrong."}}`, "HTTP 500"},
		{"502 with an HTML body", 502, `<html>Bad Gateway</html>`, "HTTP 502"},
		{"503 with an empty body", 503, ``, "HTTP 503"},
		{"402 that is not a card error", 402, `{"error":{"type":"api_error","message":"request failed"}}`, "HTTP 402"},
		{"402 with an unreadable body", 402, `not json`, "HTTP 402"},
		{"400 even when the body says card_error", 400, `{"error":{"type":"card_error","message":"Your card was declined."}}`, "HTTP 400"},
		{"500 even when the body says card_error", 500, `{"error":{"type":"card_error","message":"Your card was declined."}}`, "HTTP 500"},
		{"200 processing", 200, `{"id":"pi_p","status":"processing"}`, "processing"},
		{"200 requires_action", 200, `{"id":"pi_a","status":"requires_action"}`, "requires_action"},
		{"200 requires_confirmation", 200, `{"id":"pi_c","status":"requires_confirmation"}`, "requires_confirmation"},
		{"200 requires_capture", 200, `{"id":"pi_cap","status":"requires_capture"}`, "requires_capture"},
		{"200 canceled", 200, `{"id":"pi_x","status":"canceled"}`, "canceled"},
		{"200 requires_payment_method but no error recorded", 200, `{"id":"pi_n","status":"requires_payment_method"}`, "requires_payment_method"},
		{"200 with an empty status", 200, `{"id":"pi_e"}`, "pi_e"},
		{"200 with a malformed body", 200, `{"id":`, "decoding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := stripeStub(t, reply(tt.status, tt.body))
			got, err := g.Charge(context.Background(), chargeReq)
			if err == nil {
				t.Fatalf("expected an error, got result %+v", got)
			}
			if got != nil {
				t.Errorf("an error must come with no result, got %+v", got)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("the API key leaked into the error: %q", err)
			}
		})
	}
}

func TestStripeUnreachableIsAnError(t *testing.T) {
	// A server that has been shut down leaves a port nothing listens on.
	closed := httptest.NewServer(http.NotFoundHandler())
	g := NewStripeGateway(testKey)
	g.baseURL = closed.URL
	closed.Close()

	got, err := g.Charge(context.Background(), chargeReq)
	if err == nil || got != nil {
		t.Fatalf("got result %+v err %v, want an error and no result", got, err)
	}
	if !strings.Contains(err.Error(), "request failed") {
		t.Errorf("error = %q, want it to say the request failed", err)
	}
}

func TestStripeStopsWhenTheCallerGivesUp(t *testing.T) {
	g := stripeStub(t, reply(200, `{"id":"pi_1","status":"succeeded"}`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := g.Charge(ctx, chargeReq)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got != nil {
		t.Errorf("result = %+v, want none", got)
	}
}

// A reply bigger than the cap is cut off, which makes it unreadable, so it is
// an error rather than something we buffer whole.
func TestStripeOversizedReplyIsAnError(t *testing.T) {
	huge := `{"id":"pi_big","status":"succeeded","padding":"` + strings.Repeat("a", 2*maxResponseBytes) + `"}`
	g := stripeStub(t, reply(200, huge))
	got, err := g.Charge(context.Background(), chargeReq)
	if err == nil || got != nil {
		t.Fatalf("got result %+v err %v, want an error", got, err)
	}
}

// The connection dies halfway through the reply: the status line said 200, but
// the body never fully arrives, so we cannot know the outcome.
func TestStripeReplyCutOffMidBodyIsAnError(t *testing.T) {
	g := stripeStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "500") // promise more than we send
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"pi_cut",`))
	})
	got, err := g.Charge(context.Background(), chargeReq)
	if err == nil || got != nil {
		t.Fatalf("got result %+v err %v, want an error", got, err)
	}
	if !strings.Contains(err.Error(), "reading response") {
		t.Errorf("error = %q, want it to say the response could not be read", err)
	}
}

func TestStripeBadRequestURLIsAnError(t *testing.T) {
	g := NewStripeGateway(testKey)
	g.baseURL = "http://bad host/" // a space is not valid in a host
	got, err := g.Charge(context.Background(), chargeReq)
	if err == nil || got != nil {
		t.Fatalf("got result %+v err %v, want an error", got, err)
	}
	if !strings.Contains(err.Error(), "building request") {
		t.Errorf("error = %q, want it to say the request could not be built", err)
	}
}
