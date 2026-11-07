package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// POST /orders/{id}/refund: a person records a refund they made outside the system
// ---------------------------------------------------------------------------

// seedFailedRefund is a cancelled, charged order whose refund failed, with the alert
// the system raised for it.
func seedFailedRefund(db *fakeDB, orderID, paymentID string, kind string) {
	seedPayment(db, orderID, paymentID, domain.PaymentAuthorized, domain.OrderCancelled, time.Hour, 1)
	p := db.payments[orderID]
	p.RefundStatus, p.RefundError, p.ProviderPaymentID = domain.RefundFailed, "gave up after 5 attempts", "pi_"+paymentID
	db.payments[orderID] = p
	_ = db.RaiseAlert(context.Background(), kind, paymentID, orderID, "the refund failed")
}

func recordRefund(t *testing.T, db *fakeDB, orderID, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, db, http.MethodPost, "/orders/"+orderID+"/refund", body)
}

func TestARefundDoneByHandIsRecordedAndTheCustomerIsToldTheyWerePaid(t *testing.T) {
	db := newFakeDB()
	seedFailedRefund(db, "o1", "pay-1", domain.AlertRefundGivenUp)

	rec := recordRefund(t, db, "o1", `{"reference":"re_manual_1","note":"paid from the Stripe dashboard"}`)
	wantStatus(t, rec, http.StatusOK)

	var view struct {
		State        string `json:"state"`
		PaymentIssue string `json:"paymentIssue"`
		Payment      struct {
			RefundStatus string `json:"refundStatus"`
		} `json:"payment"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Payment.RefundStatus != "refunded" || view.PaymentIssue != "refunded" || view.State != "cancelled" {
		t.Errorf("view = %+v, want the refund done and the customer told so, the order still cancelled", view)
	}
	if got := db.payments["o1"]; got.RefundStatus != domain.RefundDone || got.RefundID != "re_manual_1" {
		t.Errorf("payment = %+v", got)
	}
	// who did it is the signed-in account, never something the request says
	if len(db.manualRefunds) != 1 || db.manualRefunds[0] != (manualRefundCall{"o1", "re_manual_1", "staff@example.test", "paid from the Stripe dashboard"}) {
		t.Errorf("asked of the store: %+v", db.manualRefunds)
	}
	if len(db.openAlerts()) != 0 {
		t.Errorf("open alerts = %+v, want the refund alert closed", db.openAlerts())
	}
}

func TestEveryAlertAboutAFailedRefundIsClosedAndNoOtherIs(t *testing.T) {
	for _, kind := range []string{domain.AlertRefundGivenUp, domain.AlertRefundReversed, domain.AlertRefundUnrecorded} {
		db := newFakeDB()
		seedFailedRefund(db, "o1", "pay-1", kind)
		_ = db.RaiseAlert(context.Background(), domain.AlertContradictingEvent, "pay-1", "o1", "unrelated")
		_ = db.RaiseAlert(context.Background(), domain.AlertRefundGivenUp, "pay-other", "o2", "another payment")
		wantStatus(t, recordRefund(t, db, "o1", `{"reference":"r"}`), http.StatusOK)
		open := db.openAlerts()
		if len(open) != 2 {
			t.Errorf("%s: %d alerts left open, want the two that are not about this refund", kind, len(open))
		}
	}
}

func TestTheNoteIsOptionalAndTheReferenceIsTrimmed(t *testing.T) {
	db := newFakeDB()
	seedFailedRefund(db, "o1", "pay-1", domain.AlertRefundReversed)
	wantStatus(t, recordRefund(t, db, "o1", `{"reference":"  bank transfer 4471  "}`), http.StatusOK)
	if c := db.manualRefunds[0]; c.reference != "bank transfer 4471" || c.note != "" {
		t.Errorf("call = %+v", c)
	}
}

func TestWhatIsTypedIsCheckedBeforeAnythingIsDone(t *testing.T) {
	long := strings.Repeat("x", 201)
	for name, c := range map[string]struct{ body, field string }{
		"no reference":          {`{"note":"n"}`, "reference"},
		"a blank reference":     {`{"reference":"   "}`, "reference"},
		"a long reference":      {`{"reference":"` + long + `"}`, "reference"},
		"a control character":   {`{"reference":"a\u0007b"}`, "reference"},
		"a newline":             {`{"reference":"a\nb"}`, "reference"},
		"a long note":           {`{"reference":"r","note":"` + strings.Repeat("n", 501) + `"}`, "note"},
		"a note with a newline": {`{"reference":"r","note":"line one\nline two"}`, "note"},
	} {
		db := newFakeDB()
		seedFailedRefund(db, "o1", "pay-1", domain.AlertRefundGivenUp)
		rec := recordRefund(t, db, "o1", c.body)
		wantStatus(t, rec, http.StatusUnprocessableEntity)
		var body struct {
			Fields map[string]string `json:"fields"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body.Fields[c.field] == "" {
			t.Errorf("%s: fields = %v, want a problem with %q", name, body.Fields, c.field)
		}
		if len(db.manualRefunds) != 0 {
			t.Errorf("%s: the store was asked although the input was refused", name)
		}
	}
}

func TestTheLimitsAreExactlyTwoHundredAndFiveHundredCharacters(t *testing.T) {
	db := newFakeDB()
	seedFailedRefund(db, "o1", "pay-1", domain.AlertRefundGivenUp)
	body := `{"reference":"` + strings.Repeat("é", 200) + `","note":"` + strings.Repeat("n", 500) + `"}`
	wantStatus(t, recordRefund(t, db, "o1", body), http.StatusOK) // letters are counted, not bytes
}

func TestABodyThatIsNotAcceptableIsRefused(t *testing.T) {
	db := newFakeDB()
	seedFailedRefund(db, "o1", "pay-1", domain.AlertRefundGivenUp)
	wantStatus(t, recordRefund(t, db, "o1", `{"reference":`), http.StatusBadRequest)
	wantStatus(t, recordRefund(t, db, "o1", `{"reference":"r","by":"someone else"}`), http.StatusBadRequest) // who is not for the request to say
	wantStatus(t, doWith(t, db, http.MethodPost, "/orders/o1/refund", `{"reference":"r"}`, "text/plain"), http.StatusUnsupportedMediaType)
	if len(db.manualRefunds) != 0 {
		t.Error("the store was asked")
	}
}

// What the refund looks like decides what may be recorded, and each refusal says why.
func TestOnlyAFailedRefundCanBeRecordedAndEachRefusalSaysWhy(t *testing.T) {
	cases := []struct {
		name   string
		status domain.PaymentStatus
		refund domain.RefundStatus
		why    string
	}{
		{"already done", domain.PaymentAuthorized, domain.RefundDone, "already recorded as done"},
		{"the job is working on it", domain.PaymentAuthorized, domain.RefundInProgress, "still working"},
		{"not tried yet", domain.PaymentAuthorized, domain.RefundNone, "no refund has been tried"},
		{"never set", domain.PaymentAuthorized, "", "no refund has been tried"},
		{"declined", domain.PaymentDeclined, domain.RefundNone, "never charged"},
		{"abandoned", domain.PaymentAbandoned, domain.RefundNone, "never charged"},
		{"waiting for an answer", domain.PaymentProcessing, domain.RefundNone, "never charged"},
	}
	for _, c := range cases {
		db := newFakeDB()
		seedPayment(db, "o1", "pay-1", c.status, domain.OrderCancelled, time.Hour, 1)
		p := db.payments["o1"]
		p.RefundStatus = c.refund
		db.payments["o1"] = p

		rec := recordRefund(t, db, "o1", `{"reference":"r"}`)
		wantStatus(t, rec, http.StatusConflict)
		wantErrorContains(t, rec, c.why)
		if got := db.payments["o1"]; got.RefundStatus != c.refund || got.RefundID != "" {
			t.Errorf("%s: the payment was changed: %+v", c.name, got)
		}
	}
}

func TestAnOrderWithNoPaymentHasNothingToRefund(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderCancelled)
	rec := recordRefund(t, db, "o1", `{"reference":"r"}`)
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "no payment")
}

func TestAnOrderThatDoesNotExistIsNotFound(t *testing.T) {
	wantStatus(t, recordRefund(t, newFakeDB(), "ghost", `{"reference":"r"}`), http.StatusNotFound)
}

func TestARefundCanOnlyBeRecordedOnce(t *testing.T) {
	db := newFakeDB()
	seedFailedRefund(db, "o1", "pay-1", domain.AlertRefundGivenUp)
	wantStatus(t, recordRefund(t, db, "o1", `{"reference":"first"}`), http.StatusOK)
	rec := recordRefund(t, db, "o1", `{"reference":"second"}`)
	wantStatus(t, rec, http.StatusConflict)
	if db.payments["o1"].RefundID != "first" {
		t.Errorf("refund id = %q: a second recording must not overwrite the first", db.payments["o1"].RefundID)
	}
}

func TestAStorageFailureIsA500ThatLeaksNothing(t *testing.T) {
	db := newFakeDB()
	seedFailedRefund(db, "o1", "pay-1", domain.AlertRefundGivenUp)
	db.manualRefundErr = errors.New(`pq: connection to "10.1.2.3" refused`)
	rec := recordRefund(t, db, "o1", `{"reference":"r"}`)
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "10.1.2.3") {
		t.Errorf("the error leaks the cause: %s", rec.Body)
	}
}

func TestOnlyStaffMayRecordARefundAndItNeedsTheCSRFToken(t *testing.T) {
	w := newWorld(t)
	customer := w.customer(t, "ada@example.com")
	staff := w.staff(t, "dispatch@example.com")
	anonymous := w.browser(t)
	seedFailedRefund(w.db, "o1", "pay-1", domain.AlertRefundGivenUp)
	w.db.seedWith("o1", domain.OrderCancelled, func(o *domain.Order) { o.CustomerID = customer.userID })
	body := `{"reference":"r"}`

	wantStatus(t, anonymous.do(http.MethodPost, "/orders/o1/refund", body), http.StatusUnauthorized)
	wantStatus(t, customer.do(http.MethodPost, "/orders/o1/refund", body), http.StatusForbidden) // even for their own order
	if len(w.db.manualRefunds) != 0 {
		t.Fatal("the store was asked on behalf of someone who may not")
	}

	staff.noCSRF = true
	wantStatus(t, staff.do(http.MethodPost, "/orders/o1/refund", body), http.StatusForbidden)
	staff.noCSRF = false
	rec := staff.do(http.MethodPost, "/orders/o1/refund", body)
	wantStatus(t, rec, http.StatusOK)
	if c := w.db.manualRefunds[0]; c.by != "dispatch@example.com" {
		t.Errorf("recorded by %q, want the signed-in account's email", c.by)
	}
}

func TestTheRefundRouteTakesOnlyPOST(t *testing.T) {
	db := newFakeDB()
	seedFailedRefund(db, "o1", "pay-1", domain.AlertRefundGivenUp)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if got := do(t, db, method, "/orders/o1/refund", "").Code; got != http.StatusMethodNotAllowed {
			t.Errorf("%s /orders/o1/refund = %d, want 405", method, got)
		}
	}
}

func TestTheWhyOfARefusalIsWorkedOutFromHowThePaymentStands(t *testing.T) {
	// the update did not apply although the refund was failed: it moved in between
	p := domain.Payment{Status: domain.PaymentAuthorized, RefundStatus: domain.RefundFailed}
	if got := whyNotRecorded(p); !strings.Contains(got, "changed while") {
		t.Errorf("why = %q", got)
	}
}
