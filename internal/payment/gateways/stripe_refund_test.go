package gateways

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/domain"
)

var refundReq = domain.RefundRequest{
	PaymentID:         "pay-1",
	ProviderPaymentID: "pi_charged",
	AmountCents:       1447,
	IdempotencyKey:    "refund-pay-1",
}

// ---- what we send --------------------------------------------------------------

func TestStripeSendsTheRefundToTheRefundsEndpoint(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotKey string
	var gotForm url.Values
	g := stripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth, gotKey = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		_ = r.ParseForm()
		gotForm = r.PostForm
		reply(200, `{"id":"re_1","status":"succeeded"}`)(w, r)
	})

	if _, err := g.Refund(context.Background(), refundReq); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/refunds" {
		t.Errorf("request = %s %s, want POST /v1/refunds", gotMethod, gotPath)
	}
	if gotAuth != "Bearer "+testKey {
		t.Errorf("Authorization = %q, want the bearer key", gotAuth)
	}
	if gotKey != "refund-pay-1" {
		t.Errorf("Idempotency-Key = %q, want the refund's key", gotKey)
	}
	for field, want := range map[string]string{"payment_intent": "pi_charged", "amount": "1447"} {
		if got := gotForm.Get(field); got != want {
			t.Errorf("form %s = %q, want %q", field, got, want)
		}
	}
}

func TestStripeRefundSendsNoKeyHeaderWithoutAKey(t *testing.T) {
	var present bool
	g := stripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["Idempotency-Key"]
		reply(200, `{"id":"re_1","status":"succeeded"}`)(w, r)
	})
	req := refundReq
	req.IdempotencyKey = ""
	if _, err := g.Refund(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Error("an Idempotency-Key header was sent with no key")
	}
}

// Nothing is sent for a request that could never be valid.
func TestStripeRefundRefusesAnInvalidRequestWithoutCallingStripe(t *testing.T) {
	called := false
	g := stripeStub(t, func(http.ResponseWriter, *http.Request) { called = true })
	for name, req := range map[string]domain.RefundRequest{
		"no provider id": {AmountCents: 100},
		"zero amount":    {ProviderPaymentID: "pi_1"},
		"negative":       {ProviderPaymentID: "pi_1", AmountCents: -5},
	} {
		if _, err := g.Refund(context.Background(), req); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if called {
		t.Error("Stripe was called for an invalid refund")
	}
}

// ---- what Stripe says back -------------------------------------------------------

func TestStripeRefundAcceptedIsASuccess(t *testing.T) {
	for _, status := range []string{"succeeded", "pending"} {
		t.Run(status, func(t *testing.T) {
			g := stripeStub(t, reply(200, `{"id":"re_ok","status":"`+status+`"}`))
			got, err := g.Refund(context.Background(), refundReq)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.RefundID != "re_ok" {
				t.Errorf("refund id = %q, want re_ok", got.RefundID)
			}
		})
	}
}

// Anything but a refund that is on its way is an error, so the job tries again.
func TestStripeRefundThatIsNotOnItsWayIsAnError(t *testing.T) {
	for _, status := range []string{"failed", "canceled", "requires_action", "", "something_new"} {
		t.Run("status "+status, func(t *testing.T) {
			g := stripeStub(t, reply(200, `{"id":"re_x","status":"`+status+`"}`))
			got, err := g.Refund(context.Background(), refundReq)
			if err == nil || got != nil {
				t.Errorf("got %+v, %v, want an error", got, err)
			}
		})
	}
}

func TestStripeRefundReplyWithoutAnIdIsAnError(t *testing.T) {
	g := stripeStub(t, reply(200, `{"status":"succeeded"}`))
	if got, err := g.Refund(context.Background(), refundReq); err == nil || got != nil {
		t.Errorf("got %+v, %v, want an error", got, err)
	}
}

func TestStripeRefundReplyThatIsNotJSONIsAnError(t *testing.T) {
	g := stripeStub(t, reply(200, `<html>gateway timeout</html>`))
	if got, err := g.Refund(context.Background(), refundReq); err == nil || got != nil {
		t.Errorf("got %+v, %v, want an error", got, err)
	}
}

// Asking again for a charge that is already refunded must not be a problem.
func TestStripeRefundOfAnAlreadyRefundedChargeIsASuccess(t *testing.T) {
	body := `{"error":{"type":"invalid_request_error","code":"charge_already_refunded","message":"Charge ch_1 has already been refunded."}}`
	g := stripeStub(t, reply(400, body))
	got, err := g.Refund(context.Background(), refundReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.RefundID != alreadyRefundedID {
		t.Errorf("refund id = %q, want the %q marker", got.RefundID, alreadyRefundedID)
	}
}

// The already-refunded code only counts as a 400, as Stripe sends it.
func TestStripeRefundAlreadyRefundedCodeUnderAnotherStatusIsAnError(t *testing.T) {
	body := `{"error":{"type":"invalid_request_error","code":"charge_already_refunded","message":"x"}}`
	for _, status := range []int{401, 404, 429, 500} {
		g := stripeStub(t, reply(status, body))
		if got, err := g.Refund(context.Background(), refundReq); err == nil || got != nil {
			t.Errorf("HTTP %d: got %+v, %v, want an error", status, got, err)
		}
	}
}

func TestStripeRefundFailuresAreErrorsThatSayWhy(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   []string
	}{
		{"a wrong key", 401, `{"error":{"type":"invalid_request_error","message":"Invalid API Key provided"}}`, []string{"401", "Invalid API Key"}},
		{"no such charge", 404, `{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such payment_intent"}}`, []string{"404", "resource_missing", "No such payment_intent"}},
		{"another bad request", 400, `{"error":{"type":"invalid_request_error","code":"charge_disputed","message":"disputed"}}`, []string{"400", "charge_disputed"}},
		{"a rate limit", 429, `{"error":{"type":"rate_limit_error","message":"slow down"}}`, []string{"429"}},
		{"an outage", 503, `not json`, []string{"503"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := stripeStub(t, reply(tt.status, tt.body))
			got, err := g.Refund(context.Background(), refundReq)
			if err == nil || got != nil {
				t.Fatalf("got %+v, %v, want an error", got, err)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}

func TestStripeRefundUnreachableIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	g := NewStripeGateway(testKey)
	g.baseURL = srv.URL
	srv.Close()
	if got, err := g.Refund(context.Background(), refundReq); err == nil || got != nil {
		t.Errorf("got %+v, %v, want an error", got, err)
	}
}

func TestStripeRefundStopsWhenTheCallerGivesUp(t *testing.T) {
	release := make(chan struct{})
	g := stripeStub(t, func(http.ResponseWriter, *http.Request) { <-release })
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := g.Refund(ctx, refundReq); err == nil {
		t.Error("want an error when the caller gives up")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the refund did not stop when its context ended")
	}
}

func TestStripeRefundBadRequestURLIsAnError(t *testing.T) {
	g := NewStripeGateway(testKey)
	g.baseURL = "http://bad host/"
	if _, err := g.Refund(context.Background(), refundReq); err == nil {
		t.Error("want an error for an unusable URL")
	}
}

func TestStripeRefundOversizedReplyIsAnError(t *testing.T) {
	g := stripeStub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"re_1","status":"succeeded","pad":"` + strings.Repeat("a", maxResponseBytes+10) + `"}`))
	})
	if got, err := g.Refund(context.Background(), refundReq); err == nil || got != nil {
		t.Errorf("got %+v, %v, want an error for a reply cut at the size cap", got, err)
	}
}
