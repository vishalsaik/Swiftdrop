package handlers

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

// ---------------------------------------------------------------------------
// A refund the provider accepted and then reversed: the webhook turns it into a
// failed refund and an alert, and never asks for another refund.
// ---------------------------------------------------------------------------

func reversal(providerID, refundID string) *stubParser {
	return &stubParser{event: payment.Event{
		Kind: payment.EventRefundFailed, ID: "evt_r1", ProviderPaymentID: providerID, RefundID: refundID, Reason: "expired_or_canceled_card",
	}}
}

// seedRefunded stores a charged payment on a cancelled order whose refund is in the
// given state.
func seedRefunded(db *fakeDB, status domain.RefundStatus, refundID string) {
	seedPayment(db, "o1", "pay-1", domain.PaymentAuthorized, domain.OrderCancelled, time.Hour, 1)
	p := db.payments["o1"]
	p.ProviderPaymentID, p.RefundStatus, p.RefundID = "pi_1", status, refundID
	db.payments["o1"] = p
}

func TestARefundThatFailedAfterBeingAcceptedBecomesFailedAndAlertsAPerson(t *testing.T) {
	db := newFakeDB()
	seedRefunded(db, domain.RefundDone, "re_1")

	wantAck(t, postWebhook(webhookServer(db, reversal("pi_1", "re_1")), `{}`), "recorded")

	p := db.payments["o1"]
	if p.RefundStatus != domain.RefundFailed || !strings.Contains(p.RefundError, "re_1") || !strings.Contains(p.RefundError, "expired_or_canceled_card") {
		t.Errorf("payment = %+v, want a failed refund that names the refund and why", p)
	}
	if p.Status != domain.PaymentAuthorized {
		t.Errorf("status = %s: the charge itself stays authorized", p.Status)
	}
	got := db.openAlerts()
	if len(got) != 1 || got[0].Kind != domain.AlertRefundReversed || got[0].Subject != "pay-1" || got[0].OrderID != "o1" {
		t.Fatalf("alerts = %+v, want one refund_reversed for pay-1", got)
	}
	if !strings.Contains(got[0].Message, "has not got their money back") {
		t.Errorf("message = %q, a person must learn the customer is still owed", got[0].Message)
	}
	// the customer is told to contact support, like for any refund we gave up on
	if m := getView(t, db, "o1"); issueOf(m) != "refund_failed" {
		t.Errorf("paymentIssue = %q, want refund_failed", issueOf(m))
	}
}

func TestTheSameReportDeliveredAgainChangesNothingAndAlertsOnce(t *testing.T) {
	db := newFakeDB()
	seedRefunded(db, domain.RefundDone, "re_1")
	s := webhookServer(db, reversal("pi_1", "re_1"))

	wantAck(t, postWebhook(s, `{}`), "recorded")
	for range 3 {
		wantAck(t, postWebhook(s, `{}`), "already recorded")
	}
	if got := db.openAlerts(); len(got) != 1 || got[0].Occurrences != 1 {
		t.Errorf("alerts = %+v, want one, seen once", got)
	}
}

// Our own job is still making the refund and has not written down its id: the
// report is asked for again later instead of being lost.
func TestAReversalWhileOurJobIsStillRefundingIsRetried(t *testing.T) {
	logs := captureLog(t)
	db := newFakeDB()
	seedRefunded(db, domain.RefundInProgress, "")

	rec := postWebhook(webhookServer(db, reversal("pi_1", "re_1")), `{}`)
	wantStatus(t, rec, http.StatusServiceUnavailable)
	if db.payments["o1"].RefundStatus != domain.RefundInProgress || len(db.openAlerts()) != 0 {
		t.Errorf("payment = %+v, alerts = %+v, want nothing changed yet", db.payments["o1"], db.openAlerts())
	}
	if !strings.Contains(logs.String(), "asking for a redelivery") {
		t.Errorf("the reason for the 503 was not logged:\n%s", logs)
	}
}

func TestAFailureOfARefundThatIsNotOursChangesNothing(t *testing.T) {
	for name, c := range map[string]struct {
		status domain.RefundStatus
		id     string
	}{
		"no refund was made":                {domain.RefundNone, ""},
		"a different refund of the payment": {domain.RefundDone, "re_ours"},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			db := newFakeDB()
			seedRefunded(db, c.status, c.id)

			wantAck(t, postWebhook(webhookServer(db, reversal("pi_1", "re_by_hand")), `{}`), "not our refund")
			if db.payments["o1"].RefundStatus != c.status || len(db.openAlerts()) != 0 {
				t.Errorf("payment = %+v, alerts = %+v, want nothing changed", db.payments["o1"], db.openAlerts())
			}
			if !strings.Contains(logs.String(), "not the refund recorded") {
				t.Errorf("the mismatch was not logged:\n%s", logs)
			}
		})
	}
}

func TestAReversalForAPaymentWeDoNotHaveIsAcknowledged(t *testing.T) {
	db := newFakeDB()
	wantAck(t, postWebhook(webhookServer(db, reversal("pi_nobody", "re_1")), `{}`), "unknown payment")
}

func TestAStorageFailureRecordingAReversalIsA500ThatLeaksNothing(t *testing.T) {
	logs := captureLog(t)
	db := newFakeDB()
	seedRefunded(db, domain.RefundDone, "re_1")
	db.reverseErr = errors.New("connection refused")

	rec := postWebhook(webhookServer(db, reversal("pi_1", "re_1")), `{}`)
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
	if !strings.Contains(logs.String(), "reversing refund re_1") || !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("the failure and its cause must be logged:\n%s", logs)
	}
}

// Not being able to keep the alert must not change what the provider is told.
func TestAReversalIsRecordedEvenWhenTheAlertCannotBeKept(t *testing.T) {
	db := newFakeDB()
	seedRefunded(db, domain.RefundDone, "re_1")
	db.alertErr = errors.New("connection refused")

	wantAck(t, postWebhook(webhookServer(db, reversal("pi_1", "re_1")), `{}`), "recorded")
	if db.payments["o1"].RefundStatus != domain.RefundFailed {
		t.Errorf("refund = %s, want failed", db.payments["o1"].RefundStatus)
	}
}
