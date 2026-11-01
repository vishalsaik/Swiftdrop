package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// the alert endpoints
// ---------------------------------------------------------------------------

func alertsFrom(t *testing.T, rec *httptest.ResponseRecorder) []domain.Alert {
	t.Helper()
	wantStatus(t, rec, http.StatusOK)
	wantContentTypeJSON(t, rec)
	var body struct {
		Alerts []domain.Alert `json:"alerts"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, rec.Body)
	}
	return body.Alerts
}

func seedAlert(db *fakeDB, kind, subject string) {
	_ = db.RaiseAlert(context.Background(), kind, subject, "o-"+subject, "problem with "+subject)
}

func TestNoAlertsIsAnEmptyListNotNull(t *testing.T) {
	rec := do(t, newFakeDB(), http.MethodGet, "/alerts", "")
	wantStatus(t, rec, http.StatusOK)
	if got := strings.TrimSpace(rec.Body.String()); got != `{"alerts":[]}` {
		t.Errorf("body = %s, want an empty list a screen can loop over", got)
	}
}

func TestTheOpenAlertsAreListedAndTheResolvedOnesOnlyOnRequest(t *testing.T) {
	db := newFakeDB()
	seedAlert(db, domain.AlertUnrecordedCharge, "p1")
	seedAlert(db, domain.AlertRefundGivenUp, "p2")
	wantStatus(t, do(t, db, http.MethodPost, "/alerts/1/resolve", ""), http.StatusOK)

	open := alertsFrom(t, do(t, db, http.MethodGet, "/alerts", ""))
	if len(open) != 1 || open[0].Subject != "p2" {
		t.Errorf("open alerts = %+v, want only p2", open)
	}
	all := alertsFrom(t, do(t, db, http.MethodGet, "/alerts?all=true", ""))
	if len(all) != 2 {
		t.Errorf("all alerts = %d, want 2", len(all))
	}
	if open[0].OrderID != "o-p2" || open[0].Kind != domain.AlertRefundGivenUp || open[0].Message == "" {
		t.Errorf("alert lost a field: %+v", open[0])
	}
}

func TestAStoreFailureListingAlertsIsA500ThatLeaksNothing(t *testing.T) {
	db := newFakeDB()
	db.alertErr = errors.New("connection refused")
	rec := do(t, db, http.MethodGet, "/alerts", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
}

func TestResolvingAnAlertClosesItWithANote(t *testing.T) {
	db := newFakeDB()
	seedAlert(db, domain.AlertRefundGivenUp, "p1")

	rec := do(t, db, http.MethodPost, "/alerts/1/resolve", `{"note":"refunded by hand"}`)
	wantStatus(t, rec, http.StatusOK)
	var a domain.Alert
	if err := json.NewDecoder(rec.Body).Decode(&a); err != nil {
		t.Fatal(err)
	}
	if a.Open() || a.ResolvedNote != "refunded by hand" || a.ID != 1 {
		t.Errorf("alert = %+v", a)
	}
	if len(db.openAlerts()) != 0 {
		t.Error("the alert is still open")
	}
}

func TestResolvingNeedsNoBody(t *testing.T) {
	db := newFakeDB()
	seedAlert(db, domain.AlertRefundGivenUp, "p1")
	wantStatus(t, do(t, db, http.MethodPost, "/alerts/1/resolve", ""), http.StatusOK)
}

func TestResolvingTheWrongAlertIsRefusedClearly(t *testing.T) {
	db := newFakeDB()
	seedAlert(db, domain.AlertRefundGivenUp, "p1")
	wantStatus(t, do(t, db, http.MethodPost, "/alerts/1/resolve", ""), http.StatusOK)

	again := do(t, db, http.MethodPost, "/alerts/1/resolve", "")
	wantStatus(t, again, http.StatusConflict)
	wantErrorContains(t, again, "already resolved")

	missing := do(t, db, http.MethodPost, "/alerts/99/resolve", "")
	wantStatus(t, missing, http.StatusNotFound)
	wantErrorContains(t, missing, "99")
}

func TestABadAlertIdOrBodyIsA400(t *testing.T) {
	db := newFakeDB()
	seedAlert(db, domain.AlertRefundGivenUp, "p1")
	for _, id := range []string{"0", "-3", "abc", "1.5", "99999999999999999999"} {
		wantStatus(t, do(t, db, http.MethodPost, "/alerts/"+id+"/resolve", ""), http.StatusBadRequest)
	}
	wantStatus(t, do(t, db, http.MethodPost, "/alerts/1/resolve", `{"note":`), http.StatusBadRequest)
	if len(db.openAlerts()) != 1 {
		t.Error("a refused request resolved the alert")
	}
}

func TestAnOversizedResolveBodyIs413(t *testing.T) {
	db := newFakeDB()
	seedAlert(db, domain.AlertRefundGivenUp, "p1")
	huge := `{"note":"` + strings.Repeat("a", maxBodyBytes) + `"}`
	wantStatus(t, do(t, db, http.MethodPost, "/alerts/1/resolve", huge), http.StatusRequestEntityTooLarge)
}

func TestAStoreFailureResolvingAnAlertIsA500ThatLeaksNothing(t *testing.T) {
	db := newFakeDB()
	seedAlert(db, domain.AlertRefundGivenUp, "p1")
	db.alertErr = errors.New("connection refused")
	rec := do(t, db, http.MethodPost, "/alerts/1/resolve", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
}

func TestOnlyTheRightMethodsReachTheAlertRoutes(t *testing.T) {
	db := newFakeDB()
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/alerts"}, {http.MethodDelete, "/alerts"}, {http.MethodGet, "/alerts/1/resolve"}, {http.MethodPut, "/alerts/1/resolve"},
	} {
		if got := do(t, db, c.method, c.path, "").Code; got != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", c.method, c.path, got)
		}
	}
}

// ---------------------------------------------------------------------------
// the problems that raise one
// ---------------------------------------------------------------------------

// A charge that moved money but could not be written down needs a person.
func TestAnUnrecordedChargeOnAnOrderThatMovedOnRaisesAnAlert(t *testing.T) {
	logs := captureLog(t)
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	db.beforeWrite = func(f *fakeDB, orderID string) {
		if f.payments[orderID].ID != "" {
			flipTo(domain.OrderCancelled)(f, orderID)
		}
	}
	db.completeErr = errors.New("connection refused")

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusInternalServerError)
	got := db.openAlerts()
	if len(got) != 1 || got[0].Kind != domain.AlertUnrecordedCharge || got[0].Subject != db.payments["o1"].ID || got[0].OrderID != "o1" {
		t.Fatalf("alerts = %+v, want one unrecorded_charge for the payment", got)
	}
	if !strings.Contains(got[0].Message, "could not be recorded") || !strings.Contains(logs.String(), "NEEDS REVIEW") {
		t.Errorf("message %q or log missing:\n%s", got[0].Message, logs)
	}
}

func TestAWebhookThatContradictsTheRecordRaisesOneAlertHoweverOftenItIsDelivered(t *testing.T) {
	db := newFakeDB()
	g := declining("no funds")
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusPaymentRequired)
	id := db.payments["o1"].ID
	s := webhookServer(db, eventFor(id, domain.PaymentAuthorized))

	for range 5 {
		wantAck(t, postWebhook(s, `{}`), "already recorded")
	}
	got := db.openAlerts()
	if len(got) != 1 || got[0].Kind != domain.AlertContradictingEvent || got[0].Subject != id || got[0].Occurrences != 5 {
		t.Errorf("alerts = %+v, want one contradicting_event counted 5 times", got)
	}
}

func TestAWebhookChargeThatCannotBeRecordedRaisesAnAlertAndIsRetried(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	o := db.orders["o1"]
	o.State = domain.OrderCancelled
	db.orders["o1"] = o
	db.completeErr = errors.New("connection refused")

	wantStatus(t, postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized)), `{}`), http.StatusInternalServerError)
	got := db.openAlerts()
	if len(got) != 1 || got[0].Kind != domain.AlertUnrecordedCharge || got[0].Subject != "pay-1" || got[0].OrderID != "o1" {
		t.Errorf("alerts = %+v, want one unrecorded_charge for pay-1", got)
	}
}

// Not being able to keep an alert must never change what the customer or the
// provider is told: the log line remains.
func TestFailingToKeepAnAlertChangesNothingElse(t *testing.T) {
	logs := captureLog(t)
	db := newFakeDB()
	g := declining("no funds")
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusPaymentRequired)
	id := db.payments["o1"].ID
	db.alertErr = errors.New("connection refused")

	wantAck(t, postWebhook(webhookServer(db, eventFor(id, domain.PaymentAuthorized)), `{}`), "already recorded")
	if !strings.Contains(logs.String(), "NEEDS REVIEW") || !strings.Contains(logs.String(), "could not keep the alert") {
		t.Errorf("the problem and the failure to keep it must both be logged:\n%s", logs)
	}
}

// An alert is written even when the request that found the problem has gone.
func TestAnAlertSurvivesTheRequestThatRaisedIt(t *testing.T) {
	db := newFakeDB()
	g := declining("no funds")
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusPaymentRequired)
	id := db.payments["o1"].ID

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", body)
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := contextCancelled()
	defer cancel()
	serve(webhookServer(db, eventFor(id, domain.PaymentAuthorized)), req.WithContext(ctx))
	if len(db.openAlerts()) != 1 {
		t.Errorf("alerts = %+v, want the alert kept although the request was cancelled", db.alertsForDebug())
	}
}

// The same report delivered again agrees with the record, so it is not a problem.
func TestAWebhookThatAgreesWithTheRecordRaisesNoAlert(t *testing.T) {
	db := newFakeDB()
	g := approving()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusOK)
	id := db.payments["o1"].ID
	s := webhookServer(db, eventFor(id, domain.PaymentAuthorized))

	for range 3 {
		wantAck(t, postWebhook(s, `{}`), "already recorded")
	}
	if got := db.openAlerts(); len(got) != 0 {
		t.Errorf("alerts = %+v, want none", got)
	}
}

// An alert store that hangs must not hold the request that found the problem.
func TestAnAlertStoreThatHangsDoesNotHoldTheRequest(t *testing.T) {
	logs := captureLog(t)
	old := alertTimeout
	alertTimeout = 50 * time.Millisecond
	t.Cleanup(func() { alertTimeout = old })

	db := newFakeDB()
	g := declining("no funds")
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusPaymentRequired)
	id := db.payments["o1"].ID
	db.alertBlock = true

	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() { answered <- postWebhook(webhookServer(db, eventFor(id, domain.PaymentAuthorized)), `{}`) }()
	select {
	case rec := <-answered:
		wantAck(t, rec, "already recorded")
	case <-time.After(3 * time.Second):
		t.Fatal("the request is still waiting for the alert store")
	}
	if !strings.Contains(logs.String(), "could not keep the alert") {
		t.Errorf("the failure to keep the alert must be logged:\n%s", logs)
	}
}

func (f *fakeDB) alertsForDebug() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fmt.Sprint(f.alerts)
}
