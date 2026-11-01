package handlers

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// Payments that never got an answer: what /pay, the webhook and the order view
// do with the two statuses that settle them (abandoned, needs_review) and with a
// payment so old that the provider may have forgotten its idempotency key.
// ---------------------------------------------------------------------------

// seedPayment stores a payment of the given status, claimed `ago` ago with its
// lease run out, on an order in the given state.
func seedPayment(db *fakeDB, orderID, paymentID string, status domain.PaymentStatus, orderState domain.OrderState, ago time.Duration, attempt int) {
	seedPayable(db, orderID, domain.PaymentCard, 1000)
	o := db.orders[orderID]
	o.State = orderState
	db.orders[orderID] = o
	now := time.Now()
	db.payments[orderID] = domain.Payment{
		ID: paymentID, OrderID: orderID, Status: status, AmountCents: 1000, Attempt: attempt,
		LeaseExpiresAt: now.Add(-time.Minute), CreatedAt: now.Add(-ago), UpdatedAt: now,
	}
}

// ---- an abandoned attempt ------------------------------------------------------

func TestAnAbandonedAttemptLetsTheCustomerPayAgainWithANewPayment(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayment(db, "o1", "pay-lost", domain.PaymentAbandoned, domain.OrderCreated, time.Hour, 1)

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusOK)

	if len(g.calls) != 1 || g.calls[0].IdempotencyKey == "pay-lost" || g.calls[0].IdempotencyKey == "" {
		t.Fatalf("provider calls = %+v, want one under a NEW idempotency key: the old attempt never reached the provider", g.calls)
	}
	p := db.payments["o1"]
	if p.ID == "pay-lost" || p.Attempt != 2 || p.Status != domain.PaymentAuthorized {
		t.Errorf("payment = %+v, want a new authorized attempt 2", p)
	}
	wantWrites(t, db, domain.OrderPaymentPending, domain.OrderPaid)
}

func TestAnAbandonedAttemptOnAnOrderThatIsNoLongerOpenCannotBePaid(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayment(db, "o1", "pay-lost", domain.PaymentAbandoned, domain.OrderCancelled, time.Hour, 3)

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusConflict)
	if len(g.calls) != 0 || len(db.stateWrites) != 0 {
		t.Errorf("provider calls = %d, writes = %v, want nothing", len(g.calls), db.stateWrites)
	}
}

// The reconciler gave up on the attempt while the request that held it was still
// working: the request learns it, instead of failing with a 500.
func TestARequestWhoseAttemptWasAbandonedUnderItIsToldSo(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, -time.Minute) // lease out: a retry takes it over
	db.beforeWrite = func(f *fakeDB, orderID string) {
		p := f.payments[orderID]
		p.Status = domain.PaymentAbandoned
		f.payments[orderID] = p
		flipTo(domain.OrderCreated)(f, orderID)
	}

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "ended without a charge", "try again")
}

// ---- a payment waiting for a person --------------------------------------------

func TestAPaymentUnderReviewRefusesAnotherAttempt(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayment(db, "o1", "pay-1", domain.PaymentNeedsReview, domain.OrderPaymentPending, 22*time.Hour, 1)

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "being checked")
	if len(g.calls) != 0 || len(db.stateWrites) != 0 || db.payments["o1"].ID != "pay-1" {
		t.Errorf("provider calls = %d, writes = %v, payment = %+v: nothing may start while a person looks", len(g.calls), db.stateWrites, db.payments["o1"])
	}
}

// ---- a payment too old to send again --------------------------------------------

func TestARetryOfAPaymentTooOldForTheProviderToRememberIsNotSentAgain(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayment(db, "o1", "pay-old", domain.PaymentProcessing, domain.OrderPaymentPending, 21*time.Hour, 1)

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "being checked")
	if len(g.calls) != 0 {
		t.Fatalf("the provider was called %d times: a key that old may be forgotten, and a send would charge twice", len(g.calls))
	}
	if p := db.payments["o1"]; p.Status != domain.PaymentNeedsReview || !strings.Contains(p.Reason, "20h") {
		t.Errorf("payment = %+v, want it handed to a person with the reason", p)
	}
	got := db.openAlerts()
	if len(got) != 1 || got[0].Kind != domain.AlertPaymentNeedsReview || got[0].Subject != "pay-old" || got[0].OrderID != "o1" {
		t.Fatalf("alerts = %+v, want one payment_needs_review for pay-old", got)
	}
	if !strings.Contains(got[0].Message, "not sent again") {
		t.Errorf("message = %q, a person must learn why nothing was sent", got[0].Message)
	}

	// asking again changes nothing and does not raise a second alert
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusConflict)
	if got := db.openAlerts(); len(got) != 1 || got[0].Occurrences != 1 {
		t.Errorf("alerts = %+v after a second retry, want still one, seen once", got)
	}
}

func TestARetryOfARecentPaymentIsStillSentAgainWithTheSameKey(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayment(db, "o1", "pay-recent", domain.PaymentProcessing, domain.OrderPaymentPending, 19*time.Hour, 1)

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusOK)
	if len(g.calls) != 1 || g.calls[0].IdempotencyKey != "pay-recent" {
		t.Errorf("provider calls = %+v, want one under the original key", g.calls)
	}
	if len(db.openAlerts()) != 0 {
		t.Errorf("alerts = %+v, a recent retry is nothing to alert", db.openAlerts())
	}
}

func TestAPaymentThatWasAnsweredWhileTheRetryDecidedIsReplayed(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayment(db, "o1", "pay-old", domain.PaymentProcessing, domain.OrderPaymentPending, 21*time.Hour, 1)
	db.beforeMark = func(f *fakeDB) { // the webhook records the charge first
		p := f.payments["o1"]
		p.Status = domain.PaymentAuthorized
		f.payments["o1"] = p
		flipTo(domain.OrderPaid)(f, "o1")
	}

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusOK)
	if len(g.calls) != 0 || len(db.openAlerts()) != 0 {
		t.Errorf("provider calls = %d, alerts = %+v, want the recorded answer replayed and nobody alerted", len(g.calls), db.openAlerts())
	}
}

func TestAFailureHandingAnOldPaymentToAPersonIsA500AndNothingIsSent(t *testing.T) {
	logs := captureLog(t)
	g := approving()
	db := newFakeDB()
	seedPayment(db, "o1", "pay-old", domain.PaymentProcessing, domain.OrderPaymentPending, 21*time.Hour, 1)
	db.markErr = errors.New("boom: connection refused")

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusInternalServerError)
	if !strings.Contains(logs.String(), "marking payment pay-old for review") || !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("the failure and its cause must be logged:\n%s", logs)
	}
	if len(g.calls) != 0 || strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("provider calls = %d, body = %s, want nothing sent and the cause kept out of the answer", len(g.calls), rec.Body)
	}
}

// ---- a reconciler that settles a payment under a waiting retry --------------------

func TestARetryThatLosesTheTakeoverToTheReconcilerSaysWhatBecameOfThePayment(t *testing.T) {
	for _, c := range []struct {
		name   string
		status domain.PaymentStatus
		want   string
	}{
		{"handed to a person", domain.PaymentNeedsReview, "being checked"},
		{"abandoned", domain.PaymentAbandoned, "ended without a charge"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := approving()
			db := newFakeDB()
			seedProcessing(db, "o1", "pay-1", 1000, -time.Minute)
			db.beforeTakeOver = func(f *fakeDB) {
				p := f.payments["o1"]
				p.Status = c.status
				f.payments["o1"] = p
			}
			rec := payWith(t, db, routerOf(g, g), "o1")
			wantStatus(t, rec, http.StatusConflict)
			wantErrorContains(t, rec, c.want)
			if len(g.calls) != 0 {
				t.Errorf("the provider was called %d times", len(g.calls))
			}
		})
	}
}

// ---- the webhook ------------------------------------------------------------------

func TestTheProvidersAnswerSettlesAPaymentThatWasHandedToAPerson(t *testing.T) {
	for _, c := range []struct {
		status    domain.PaymentStatus
		wantOrder domain.OrderState
	}{{domain.PaymentAuthorized, domain.OrderPaid}, {domain.PaymentDeclined, domain.OrderCreated}} {
		t.Run(string(c.status), func(t *testing.T) {
			db := newFakeDB()
			seedPayment(db, "o1", "pay-1", domain.PaymentNeedsReview, domain.OrderPaymentPending, 30*time.Hour, 1)

			wantAck(t, postWebhook(webhookServer(db, eventFor("pay-1", c.status)), `{}`), "recorded")
			if p := db.payments["o1"]; p.Status != c.status {
				t.Errorf("payment = %s, want %s", p.Status, c.status)
			}
			if db.orders["o1"].State != c.wantOrder {
				t.Errorf("order = %s, want %s", db.orders["o1"].State, c.wantOrder)
			}
			if len(db.openAlerts()) != 0 {
				t.Errorf("alerts = %+v: a late truth is not a contradiction", db.openAlerts())
			}
		})
	}
}

// We gave up on the attempt and the provider says it was charged after all:
// money moved that our record denies, and only a person can sort that out.
func TestAChargeTheProviderReportsAfterWeAbandonedTheAttemptRaisesAnAlert(t *testing.T) {
	db := newFakeDB()
	seedPayment(db, "o1", "pay-1", domain.PaymentAbandoned, domain.OrderCreated, 2*time.Hour, 1)

	wantAck(t, postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized)), `{}`), "already recorded")
	got := db.openAlerts()
	if len(got) != 1 || got[0].Kind != domain.AlertContradictingEvent || got[0].Subject != "pay-1" || !strings.Contains(got[0].Message, "abandoned") {
		t.Errorf("alerts = %+v, want one contradicting_event that says abandoned", got)
	}
	if p := db.payments["o1"]; p.Status != domain.PaymentAbandoned || db.orders["o1"].State != domain.OrderCreated {
		t.Errorf("payment %s, order %s: nothing may be rewritten behind a person's back", p.Status, db.orders["o1"].State)
	}
}

// ---- what a screen is told --------------------------------------------------------

func TestAPaymentUnderReviewShowsAsConfirmingAndKeepsItsReasonPrivate(t *testing.T) {
	db := newFakeDB()
	seedPayment(db, "o1", "pay-1", domain.PaymentNeedsReview, domain.OrderPaymentPending, 22*time.Hour, 1)
	p := db.payments["o1"]
	p.Reason = "the provider could not be asked: HTTP 503 from api.stripe.com"
	db.payments["o1"] = p

	m := getView(t, db, "o1")
	pay, _ := m["payment"].(map[string]any)
	if issueOf(m) != "confirming" || pay["status"] != "needs_review" {
		t.Errorf("issue = %q, payment = %v, want confirming and needs_review", issueOf(m), pay)
	}
	if _, has := pay["reason"]; has {
		t.Errorf("payment = %v: the reason a person is asked to look is not for the customer", pay)
	}
}

func TestAnAbandonedAttemptShowsAsAbandonedAndKeepsItsReasonPrivate(t *testing.T) {
	db := newFakeDB()
	seedPayment(db, "o1", "pay-1", domain.PaymentAbandoned, domain.OrderCreated, 2*time.Hour, 1)
	p := db.payments["o1"]
	p.Reason = "no record at the provider after 2h0m0s"
	db.payments["o1"] = p

	m := getView(t, db, "o1")
	pay, _ := m["payment"].(map[string]any)
	if issueOf(m) != "abandoned" || pay["status"] != "abandoned" || pay["attempt"] != float64(1) {
		t.Errorf("issue = %q, payment = %v, want abandoned, attempt 1", issueOf(m), pay)
	}
	if _, has := pay["reason"]; has {
		t.Errorf("payment = %v: the reason is for the people running the service", pay)
	}
}
