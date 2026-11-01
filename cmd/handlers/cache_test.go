package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/paymentcache"
)

// ---------------------------------------------------------------------------
// A cache in front of the payment lookup of /pay. A retried /pay on a paid order
// is the read that is asked most, and for a paid order the answer never changes.
// ---------------------------------------------------------------------------

func payOnce(t *testing.T, s *Server, id string) *httptest.ResponseRecorder {
	t.Helper()
	return serve(s, newPost("/orders/"+id+"/pay"))
}

// getViewVia reads an order's view through the given server.
func getViewVia(t *testing.T, s *Server, id string) map[string]any {
	t.Helper()
	rec := serve(s, httptest.NewRequest(http.MethodGet, "/orders/"+id, nil))
	wantStatus(t, rec, http.StatusOK)
	var m map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&m); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, rec.Body)
	}
	return m
}

func TestARetriedPayOnAPaidOrderStopsReadingThePaymentFromTheDatabase(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	s := NewServer(db, routerOf(g, g))
	cache := paymentcache.New(db, 100)
	s.Lookup = cache

	if res := payOnce(t, s, "o1"); res.Code != http.StatusOK { // the first pay: no payment yet
		t.Fatalf("first pay = %d", res.Code)
	}
	if res := payOnce(t, s, "o1"); res.Code != http.StatusOK { // the first retry reads it and keeps it
		t.Fatalf("first retry = %d", res.Code)
	}
	reads := db.payGetCalls
	for range 5 {
		if res := payOnce(t, s, "o1"); res.Code != http.StatusOK {
			t.Fatalf("retry = %d, want the success replayed", res.Code)
		}
	}
	if db.payGetCalls != reads {
		t.Errorf("the payment was read from the database %d more times, want none", db.payGetCalls-reads)
	}
	if len(g.calls) != 1 {
		t.Errorf("the provider was called %d times, want once: a retry never charges again", len(g.calls))
	}
	if hits, _ := cache.Stats(); hits != 5 {
		t.Errorf("hits = %d, want 5", hits)
	}
}

// What a customer sees about a refund is read from the database, not from the
// cache that serves /pay, so a refund made after the payment was cached shows.
func TestTheOrderViewIsNeverServedFromTheCache(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	s := NewServer(db, routerOf(g, g))
	s.Lookup = paymentcache.New(db, 100)
	payOnce(t, s, "o1")
	payOnce(t, s, "o1") // now cached

	o := db.orders["o1"]
	o.State = domain.OrderCancelled
	db.orders["o1"] = o
	p := db.payments["o1"]
	p.RefundStatus = domain.RefundDone
	db.payments["o1"] = p

	m := getViewVia(t, s, "o1")
	if issueOf(m) != "refunded" {
		t.Errorf("paymentIssue = %q, want refunded: the view must read the current payment", issueOf(m))
	}
}

// A payment that is still being worked on is never kept, so the cache cannot hide
// the answer from a retry.
func TestAPaymentThatIsStillProcessingIsReadFreshOnEveryRetry(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, 1<<40) // a live lease for a very long time
	s := NewServer(db, routerOf(g, g))
	s.Lookup = paymentcache.New(db, 100)

	for range 3 {
		if res := payOnce(t, s, "o1"); res.Code != http.StatusConflict {
			t.Fatalf("pay = %d, want 409 while it is processing", res.Code)
		}
	}
	// now it is answered; the next retry sees it at once
	p := db.payments["o1"]
	p.Status = domain.PaymentAuthorized
	db.payments["o1"] = p
	o := db.orders["o1"]
	o.State = domain.OrderPaid
	db.orders["o1"] = o
	if res := payOnce(t, s, "o1"); res.Code != http.StatusOK {
		t.Errorf("pay = %d, want 200: the answer must not be hidden by what was read before", res.Code)
	}
}

func TestWithoutACacheTheServerReadsTheDatabase(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	s := NewServer(db, routerOf(g, g))
	payOnce(t, s, "o1")
	before := db.payGetCalls
	payOnce(t, s, "o1")
	payOnce(t, s, "o1")
	if db.payGetCalls-before != 2 {
		t.Errorf("database reads = %d, want one per retry", db.payGetCalls-before)
	}
}
