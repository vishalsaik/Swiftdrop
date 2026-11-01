package gateways

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/domain"
)

var claimedAt = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func lookupReq() domain.LookupRequest {
	return domain.LookupRequest{PaymentID: "pay-1", CreatedAt: claimedAt, AmountCents: 1447}
}

// intentJSON is a PaymentIntent in a list reply, tagged with our payment id.
func intentJSON(id, status, paymentID, extra string) string {
	return fmt.Sprintf(`{"id":%q,"status":%q,"metadata":{"payment_id":%q}%s}`, id, status, paymentID, extra)
}

func listJSON(hasMore bool, intents ...string) string {
	return fmt.Sprintf(`{"data":[%s],"has_more":%v}`, strings.Join(intents, ","), hasMore)
}

// ---------------------------------------------------------------------------
// what we send
// ---------------------------------------------------------------------------

func TestTheChargeCarriesOurIdsAsMetadata(t *testing.T) {
	var form url.Values
	g := stripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		reply(200, `{"id":"pi_1","status":"succeeded"}`)(w, r)
	})
	req := chargeReq
	req.IdempotencyKey = "pay-1"
	if _, err := g.Charge(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if form.Get("metadata[payment_id]") != "pay-1" || form.Get("metadata[order_id]") != "o1" {
		t.Errorf("metadata = %q / %q, want pay-1 and o1", form.Get("metadata[payment_id]"), form.Get("metadata[order_id]"))
	}
}

func TestNoMetadataIsSentForIdsThatAreNotThere(t *testing.T) {
	var form url.Values
	g := stripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		reply(200, `{"id":"pi_1","status":"succeeded"}`)(w, r)
	})
	if _, err := g.Charge(context.Background(), domain.PaymentRequest{AmountCents: 100, Currency: "USD", PaymentMethodID: "pm_x"}); err != nil {
		t.Fatal(err)
	}
	if _, has := form["metadata[payment_id]"]; has {
		t.Errorf("an empty payment id was sent: %v", form)
	}
	if _, has := form["metadata[order_id]"]; has {
		t.Errorf("an empty order id was sent: %v", form)
	}
}

func TestLookupListsTheIntentsCreatedSinceTheClaimWithoutChargingAnything(t *testing.T) {
	var method, path, auth string
	var query url.Values
	g := stripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, auth, query = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.URL.Query()
		reply(200, listJSON(false))(w, r)
	})
	if _, err := g.Lookup(context.Background(), lookupReq()); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || path != "/v1/payment_intents" {
		t.Errorf("request = %s %s, want a GET of the intents list: a lookup never charges", method, path)
	}
	if auth != "Bearer "+testKey {
		t.Errorf("Authorization = %q", auth)
	}
	wantSince := fmt.Sprint(claimedAt.Add(-time.Minute).Unix())
	if query.Get("created[gte]") != wantSince || query.Get("limit") != "100" || query.Get("starting_after") != "" {
		t.Errorf("query = %v, want created[gte]=%s (a minute of slack before the claim), limit=100 and no cursor", query, wantSince)
	}
}

func TestLookupNeverAsksForTimesBeforeTheEpoch(t *testing.T) {
	var query url.Values
	g := stripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		reply(200, listJSON(false))(w, r)
	})
	req := lookupReq()
	req.CreatedAt = time.Time{} // an unset time is far before 1970 as a unix number
	if _, err := g.Lookup(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if query.Get("created[gte]") != "0" {
		t.Errorf("created[gte] = %q, want 0", query.Get("created[gte]"))
	}
}

func TestLookupNeedsThePaymentId(t *testing.T) {
	g := stripeStub(t, func(http.ResponseWriter, *http.Request) { t.Error("Stripe was called without a payment id") })
	if _, err := g.Lookup(context.Background(), domain.LookupRequest{}); err == nil {
		t.Error("an empty payment id was accepted")
	}
}

// ---------------------------------------------------------------------------
// what we make of the answer
// ---------------------------------------------------------------------------

func TestLookupReadsTheIntentThatCarriesOurId(t *testing.T) {
	for _, c := range []struct {
		name   string
		intent string
		want   domain.LookupResult
	}{
		{"succeeded", intentJSON("pi_ok", "succeeded", "pay-1", ""),
			domain.LookupResult{Found: true, Status: domain.PaymentAuthorized, ProviderPaymentID: "pi_ok"}},
		{"refused", intentJSON("pi_no", "requires_payment_method", "pay-1", `,"last_payment_error":{"message":"Your card was declined."}`),
			domain.LookupResult{Found: true, Status: domain.PaymentDeclined, ProviderPaymentID: "pi_no", Reason: "Your card was declined."}},
		{"refused without a message", intentJSON("pi_no", "requires_payment_method", "pay-1", `,"last_payment_error":{}`),
			domain.LookupResult{Found: true, Status: domain.PaymentDeclined, ProviderPaymentID: "pi_no", Reason: "payment declined"}},
		{"canceled moved no money", intentJSON("pi_c", "canceled", "pay-1", ""),
			domain.LookupResult{Found: true, Status: domain.PaymentDeclined, ProviderPaymentID: "pi_c", Reason: "the payment was canceled"}},
		{"still processing", intentJSON("pi_p", "processing", "pay-1", ""),
			domain.LookupResult{Found: true, ProviderPaymentID: "pi_p"}},
		{"waiting for the customer", intentJSON("pi_a", "requires_action", "pay-1", ""),
			domain.LookupResult{Found: true, ProviderPaymentID: "pi_a"}},
		{"never confirmed", intentJSON("pi_n", "requires_payment_method", "pay-1", ""),
			domain.LookupResult{Found: true, ProviderPaymentID: "pi_n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := stripeStub(t, reply(200, listJSON(false, c.intent)))
			got, err := g.Lookup(context.Background(), lookupReq())
			if err != nil || *got != c.want {
				t.Errorf("Lookup = %+v, %v, want %+v", got, err, c.want)
			}
		})
	}
}

func TestLookupIgnoresIntentsThatAreNotOurs(t *testing.T) {
	g := stripeStub(t, reply(200, listJSON(false,
		intentJSON("pi_other", "succeeded", "pay-2", ""),
		`{"id":"pi_bare","status":"succeeded"}`,
		`{"id":"pi_empty","status":"succeeded","metadata":{}}`,
		intentJSON("pi_ours", "succeeded", "pay-1", ""))))
	got, err := g.Lookup(context.Background(), lookupReq())
	if err != nil || got.ProviderPaymentID != "pi_ours" {
		t.Errorf("Lookup = %+v, %v, want pi_ours", got, err)
	}
}

func TestLookupSaysNotFoundOnlyAfterReadingEveryPage(t *testing.T) {
	var cursors []string
	g := stripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("starting_after")
		cursors = append(cursors, after)
		switch after {
		case "":
			reply(200, listJSON(true, intentJSON("pi_a", "succeeded", "pay-9", ""), intentJSON("pi_b", "succeeded", "pay-8", "")))(w, r)
		case "pi_b":
			reply(200, listJSON(false, intentJSON("pi_c", "succeeded", "pay-7", "")))(w, r)
		default:
			t.Errorf("unexpected cursor %q", after)
		}
	})
	got, err := g.Lookup(context.Background(), lookupReq())
	if err != nil || got.Found {
		t.Errorf("Lookup = %+v, %v, want not found", got, err)
	}
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "pi_b" {
		t.Errorf("cursors = %q, want the second page asked for after the last intent of the first", cursors)
	}
}

func TestLookupFindsAnIntentOnALaterPageAndStopsThere(t *testing.T) {
	calls := 0
	g := stripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("starting_after") == "" {
			reply(200, listJSON(true, intentJSON("pi_a", "succeeded", "pay-9", "")))(w, r)
			return
		}
		reply(200, listJSON(true, intentJSON("pi_ours", "succeeded", "pay-1", "")))(w, r)
	})
	got, err := g.Lookup(context.Background(), lookupReq())
	if err != nil || got.ProviderPaymentID != "pi_ours" || calls != 2 {
		t.Errorf("Lookup = %+v, %v after %d calls, want pi_ours after 2", got, err, calls)
	}
}

// A search that was cut short must never be taken for "Stripe has no record":
// that would give up a payment that may have been charged.
func TestALookupThatCannotReadEverythingIsAnErrorNotNotFound(t *testing.T) {
	calls := 0
	g := stripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		reply(200, listJSON(true, intentJSON(fmt.Sprintf("pi_%d", calls), "succeeded", "pay-other", "")))(w, r)
	})
	got, err := g.Lookup(context.Background(), lookupReq())
	if err == nil || got != nil {
		t.Fatalf("Lookup = %+v, %v, want an error", got, err)
	}
	if calls != lookupMaxPages || calls < 10 {
		t.Errorf("%d pages read, want the limit of %d (and a limit that really reads far back)", calls, lookupMaxPages)
	}
}

func TestAnEmptyPageThatSaysThereIsMoreDoesNotLoopForever(t *testing.T) {
	calls := 0
	g := stripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		reply(200, `{"data":[],"has_more":true}`)(w, r)
	})
	got, err := g.Lookup(context.Background(), lookupReq())
	if err != nil || got.Found || calls != 1 {
		t.Errorf("Lookup = %+v, %v after %d calls, want not found after one", got, err, calls)
	}
}

func TestLookupFailuresAreErrorsThatNeverLookLikeNotFound(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"a wrong key", 401, `{"error":{"type":"invalid_request_error","code":"api_key_invalid","message":"Invalid API Key"}}`, "HTTP 401"},
		{"a rate limit", 429, `{"error":{"type":"rate_limit_error","message":"slow down"}}`, "HTTP 429"},
		{"an outage with no body", 503, ``, "HTTP 503"},
		{"a 200 that is not JSON", 200, `{"data":`, "decoding"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := stripeStub(t, reply(c.status, c.body))
			got, err := g.Lookup(context.Background(), lookupReq())
			if err == nil || got != nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Lookup = %+v, %v, want an error containing %q", got, err, c.want)
			}
		})
	}
}

func TestLookupFailsWhenStripeCannotBeReached(t *testing.T) {
	g := NewStripeGateway(testKey)
	g.baseURL = "http://127.0.0.1:1" // nothing listens there
	if got, err := g.Lookup(context.Background(), lookupReq()); err == nil || got != nil {
		t.Errorf("Lookup = %+v, %v, want an error", got, err)
	}
}

func TestLookupGivesUpWhenTheCallerDoes(t *testing.T) {
	g := stripeStub(t, reply(200, listJSON(false)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := g.Lookup(ctx, lookupReq()); err == nil || got != nil {
		t.Errorf("Lookup = %+v, %v, want an error", got, err)
	}
}

func TestLookupRefusesARequestItCannotBuild(t *testing.T) {
	g := NewStripeGateway(testKey)
	g.baseURL = "http://bad host" // not a URL
	if _, err := g.Lookup(context.Background(), lookupReq()); err == nil || !strings.Contains(err.Error(), "building request") {
		t.Errorf("error = %v, want one about building the request", err)
	}
}

func TestLookupFailsWhenTheReplyCannotBeRead(t *testing.T) {
	g := stripeStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100") // promises more than it sends
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data"`))
	})
	if got, err := g.Lookup(context.Background(), lookupReq()); err == nil || got != nil {
		t.Errorf("Lookup = %+v, %v, want an error", got, err)
	}
}
