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

func history(t *testing.T, db *fakeDB, id string) []domain.PaymentEvent {
	t.Helper()
	rec := do(t, db, http.MethodGet, "/orders/"+id+"/payment-history", "")
	wantStatus(t, rec, http.StatusOK)
	wantContentTypeJSON(t, rec)
	var body struct {
		Events []domain.PaymentEvent `json:"events"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, rec.Body)
	}
	return body.Events
}

func TestAnOrderWithNoPaymentHasAnEmptyHistoryNotNull(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderCreated)
	rec := do(t, db, http.MethodGet, "/orders/o1/payment-history", "")
	wantStatus(t, rec, http.StatusOK)
	if got := strings.TrimSpace(rec.Body.String()); got != `{"events":[]}` {
		t.Errorf("body = %s, want an empty list a screen can loop over", got)
	}
}

func TestTheHistoryIsReturnedInTheOrderTheLedgerGaveIt(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderCancelled)
	db.seed("o2", domain.OrderPaid)
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	db.events = map[string][]domain.PaymentEvent{
		"o1": {
			{ID: 1, PaymentID: "p1", At: at, Kind: "claimed", Detail: "amount_cents=1000"},
			{ID: 2, PaymentID: "p1", At: at.Add(time.Second), Kind: "authorized", Detail: "provider_payment_id=pi_1"},
			{ID: 3, PaymentID: "p1", At: at.Add(time.Minute), Kind: "refunded", Detail: "refund_id=re_1"},
		},
		"o2": {{ID: 4, PaymentID: "p2", At: at, Kind: "claimed"}},
	}

	got := history(t, db, "o1")
	if len(got) != 3 || got[0].Kind != "claimed" || got[1].Kind != "authorized" || got[2].Kind != "refunded" {
		t.Fatalf("events = %+v, want claimed, authorized, refunded", got)
	}
	if got[1].Detail != "provider_payment_id=pi_1" || !got[1].At.Equal(at.Add(time.Second)) || got[1].PaymentID != "p1" || got[1].ID != 2 {
		t.Errorf("an event lost a field: %+v", got[1])
	}
	for _, e := range got {
		if e.PaymentID == "p2" {
			t.Error("another order's payment is in this order's history")
		}
	}
}

func TestTheHistoryOfAMissingOrderIsA404(t *testing.T) {
	wantStatus(t, do(t, newFakeDB(), http.MethodGet, "/orders/ghost/payment-history", ""), http.StatusNotFound)
}

func TestAStoreFailureReadingTheHistoryIsA500ThatLeaksNothing(t *testing.T) {
	logs := captureLog(t)
	db := newFakeDB()
	db.seed("o1", domain.OrderPaid)
	db.eventsErr = errors.New("connection refused")

	rec := do(t, db, http.MethodGet, "/orders/o1/payment-history", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("the cause is not in the log:\n%s", logs)
	}
}

func TestTheHistoryCanOnlyBeRead(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderPaid)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if got := do(t, db, method, "/orders/o1/payment-history", "").Code; got != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", method, got)
		}
	}
}
