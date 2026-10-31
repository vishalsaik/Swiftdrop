package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// GET /orders/{id} shows the order, a summary of its payment, and what is wrong
// or unfinished about the money.
// ---------------------------------------------------------------------------

func getView(t *testing.T, db *fakeDB, id string) map[string]any {
	t.Helper()
	rec := do(t, db, http.MethodGet, "/orders/"+id, "")
	wantStatus(t, rec, http.StatusOK)
	var m map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&m); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, rec.Body)
	}
	return m
}

func issueOf(m map[string]any) string {
	s, _ := m["paymentIssue"].(string)
	return s
}

func TestAnOrderWithNoPaymentHasNoPaymentFields(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderCreated)

	m := getView(t, db, "o1")
	if _, has := m["payment"]; has {
		t.Errorf("payment = %v, want it absent", m["payment"])
	}
	if _, has := m["paymentIssue"]; has {
		t.Errorf("paymentIssue = %v, want it absent", m["paymentIssue"])
	}
	if m["id"] != "o1" || m["state"] != "created" {
		t.Errorf("order fields = %v, want them at the top level", m)
	}
}

func TestAPaidOrderShowsItsPaymentAndNoIssue(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1447)
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusOK)

	m := getView(t, db, "o1")
	pay, _ := m["payment"].(map[string]any)
	if pay["status"] != "authorized" || pay["amountCents"] != float64(1447) || pay["refundStatus"] != "none" {
		t.Errorf("payment = %v, want authorized, 1447, refund none", pay)
	}
	if pay["id"] == "" || pay["id"] == nil {
		t.Error("payment id is missing")
	}
	if issueOf(m) != "" {
		t.Errorf("paymentIssue = %q, want none for a paid order", issueOf(m))
	}
}

func TestADeclinedPaymentShowsItsReason(t *testing.T) {
	g := declining("Your card was declined.")
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusPaymentRequired)

	m := getView(t, db, "o1")
	pay, _ := m["payment"].(map[string]any)
	if pay["status"] != "declined" || pay["reason"] != "Your card was declined." {
		t.Errorf("payment = %v, want the decline and its reason", pay)
	}
	if issueOf(m) != "declined" {
		t.Errorf("paymentIssue = %q, want declined (the customer may try again)", issueOf(m))
	}
	if pay["attempt"] != float64(1) || pay["maxAttempts"] != float64(domain.MaxPaymentAttempts) {
		t.Errorf("payment = %v, want attempt 1 of %d", pay, domain.MaxPaymentAttempts)
	}
}

// A payment written before attempts were numbered has none stored: it is attempt 1.
func TestAPaymentWithNoStoredAttemptIsAttemptOne(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute) // Attempt is left at its zero value

	pay, _ := getView(t, db, "o1")["payment"].(map[string]any)
	if pay["attempt"] != float64(1) {
		t.Errorf("attempt = %v, want 1", pay["attempt"])
	}
}

func TestThePaymentSummaryLeavesOutWhatACustomerShouldNotSee(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	p := db.payments["o1"]
	p.ProviderPaymentID, p.RefundError, p.RefundID = "pi_secret", "stripe: HTTP 500 internal detail", "re_secret"
	db.payments["o1"] = p

	rec := do(t, db, http.MethodGet, "/orders/o1", "")
	for _, leaked := range []string{"pi_secret", "internal detail", "re_secret", "providerPaymentId", "refundError", "leaseExpiresAt"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Errorf("the response leaks %q: %s", leaked, rec.Body)
		}
	}
}

// ---- the issue, for every situation the money can be in ---------------------------

func TestThePaymentIssueFollowsTheMoney(t *testing.T) {
	type setup func(db *fakeDB)
	cancelled := func(db *fakeDB, id string) {
		o := db.orders[id]
		o.State = domain.OrderCancelled
		db.orders[id] = o
	}
	chargedAndCancelled := func(refund domain.RefundStatus, pt domain.PaymentType) setup {
		return func(db *fakeDB) {
			seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
			o := db.orders["o1"]
			o.PaymentType = pt
			db.orders["o1"] = o
			p := db.payments["o1"]
			p.Status, p.ProviderPaymentID, p.RefundStatus = domain.PaymentAuthorized, "pi_1", refund
			db.payments["o1"] = p
			cancelled(db, "o1")
		}
	}
	tests := []struct {
		name  string
		setup setup
		want  string
	}{
		{"payment being confirmed", func(db *fakeDB) { seedProcessing(db, "o1", "pay-1", 1000, time.Minute) }, "confirming"},
		{"cancelled while being confirmed", func(db *fakeDB) {
			seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
			cancelled(db, "o1")
		}, "confirming"},
		{"charged and cancelled, refund due", chargedAndCancelled(domain.RefundNone, domain.PaymentCard), "refund_pending"},
		{"charged and cancelled, refund running", chargedAndCancelled(domain.RefundInProgress, domain.PaymentCard), "refund_pending"},
		{"refunded", chargedAndCancelled(domain.RefundDone, domain.PaymentCard), "refunded"},
		{"refund failed", chargedAndCancelled(domain.RefundFailed, domain.PaymentCard), "refund_failed"},
		{"wallet refund due", chargedAndCancelled(domain.RefundNone, domain.PaymentWallet), "refund_pending"},
		{"cash has nothing to refund", chargedAndCancelled(domain.RefundNone, domain.PaymentCash), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB()
			tt.setup(db)
			if got := issueOf(getView(t, db, "o1")); got != tt.want {
				t.Errorf("paymentIssue = %q, want %q", got, tt.want)
			}
		})
	}
}

// The whole story the dashboard tells: pay, cancel, and the refund shows up.
func TestPayingAndThenCancellingShowsARefundOnItsWay(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 2500)
	router := routerOf(g, g)
	wantStatus(t, payWith(t, db, router, "o1"), http.StatusOK)
	wantStatus(t, do(t, db, http.MethodPost, "/orders/o1/cancel", ""), http.StatusOK)

	m := getView(t, db, "o1")
	if m["state"] != "cancelled" || issueOf(m) != "refund_pending" {
		t.Errorf("state %v, issue %q, want cancelled with a refund pending", m["state"], issueOf(m))
	}

	// The refund job finishes it.
	p := db.payments["o1"]
	p.RefundStatus, p.RefundID = domain.RefundDone, "re_1"
	db.payments["o1"] = p
	if got := issueOf(getView(t, db, "o1")); got != "refunded" {
		t.Errorf("paymentIssue = %q after the refund, want refunded", got)
	}
}

func TestAnOrderViewWhoseOrderIsMissingStaysA404(t *testing.T) {
	wantStatus(t, do(t, newFakeDB(), http.MethodGet, "/orders/ghost", ""), http.StatusNotFound)
}

func TestAPaymentLookupFailureWhileViewingAnOrderIsA500(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderPaid)
	db.payGetErr = errors.New("connection refused")

	rec := do(t, db, http.MethodGet, "/orders/o1", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
}

func TestOnlyTheReadEndpointCarriesThePaymentView(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "paymentIssue") || strings.Contains(rec.Body.String(), `"payment":`) {
		t.Errorf("the /pay answer is a plain order: %s", rec.Body)
	}
}
