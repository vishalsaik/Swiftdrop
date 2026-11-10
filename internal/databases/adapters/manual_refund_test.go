package adapters

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// A refund a person made outside the system is recorded afterwards. These need the same
// Postgres as the other adapter tests. The alerts they make are about the payment
// "pay-<id>", and are removed at the end.

// refundGivenUp is a charged, cancelled payment whose refund the job tried and gave up on,
// with the alert the job raised.
func refundGivenUp(t *testing.T, db *DBAdapter, id, providerID string) {
	t.Helper()
	ctx := context.Background()
	chargedAndCancelled(t, db, id, domain.PaymentCard, providerID)
	if _, ok, err := db.ClaimRefund(ctx, "pay-"+id, time.Minute); err != nil || !ok {
		t.Fatalf("ClaimRefund: ok=%v err=%v", ok, err)
	}
	if err := db.FailRefund(ctx, "pay-"+id, "gave up after 5 attempts", true); err != nil {
		t.Fatal(err)
	}
	raiseFor(t, db, domain.AlertRefundGivenUp, id)
}

// raiseFor opens an alert about the payment of order id, and removes it afterwards.
func raiseFor(t *testing.T, db *DBAdapter, kind, id string) {
	t.Helper()
	if err := db.RaiseAlert(context.Background(), kind, "pay-"+id, id, "message for "+kind); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(context.Background(), "DELETE FROM alerts WHERE subject = $1", "pay-"+id) })
}

func alertsAbout(t *testing.T, db *DBAdapter, id string) map[string]domain.Alert {
	t.Helper()
	all, err := db.ListAlerts(context.Background(), false, 1000)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]domain.Alert{}
	for _, a := range all {
		if a.Subject == "pay-"+id {
			out[a.Kind] = a
		}
	}
	return out
}

func manualColumns(t *testing.T, db *DBAdapter, paymentID string) (by, note string) {
	t.Helper()
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT refund_manual_by, refund_manual_note FROM payments WHERE id = $1`, paymentID).Scan(&by, &note); err != nil {
		t.Fatal(err)
	}
	return by, note
}

func TestAFailedRefundDoneByHandIsRecordedWithWhoAndWhy(t *testing.T) {
	db := testAdapter(t)
	refundGivenUp(t, db, "mr-ok", "pi_mr_ok")

	p, recorded, err := db.RecordManualRefund(context.Background(), "mr-ok", "re_hand_1", "dee@example.com", "paid from the Stripe dashboard")
	if err != nil || !recorded {
		t.Fatalf("RecordManualRefund = %+v, %v, %v", p, recorded, err)
	}
	if p.ID != "pay-mr-ok" || p.RefundStatus != domain.RefundDone || p.RefundID != "re_hand_1" || p.RefundError != "" || p.RefundLeaseExpiresAt != nil {
		t.Errorf("payment = %+v, want a done refund with its reference and no error", p)
	}
	got := refundOf(t, db, "pay-mr-ok")
	if got.RefundStatus != domain.RefundDone || got.RefundID != "re_hand_1" || got.Status != domain.PaymentAuthorized {
		t.Errorf("stored = %+v: the charge stays authorized and the refund is done", got)
	}
	if by, note := manualColumns(t, db, "pay-mr-ok"); by != "dee@example.com" || note != "paid from the Stripe dashboard" {
		t.Errorf("recorded by %q with %q", by, note)
	}
	// the ledger says it was by hand, by whom and what they wrote: the trigger writes it
	wantLedger(t, db, "mr-ok",
		"claimed|amount_cents=1447", "authorized|provider_payment_id=pi_mr_ok",
		"refund_started|attempt 1", "refund_failed|gave up after 5 attempts",
		"refunded_by_hand|re_hand_1 recorded by dee@example.com: paid from the Stripe dashboard")
	for _, id := range dueIDs(t, db) {
		if id == "pay-mr-ok" {
			t.Error("a refund recorded as done is due again: the job would refund twice")
		}
	}
}

func TestWithoutANoteTheLedgerHasNoColon(t *testing.T) {
	db := testAdapter(t)
	refundGivenUp(t, db, "mr-nonote", "pi_mr_nonote")
	if _, recorded, err := db.RecordManualRefund(context.Background(), "mr-nonote", "bank transfer 4471", "dee@example.com", ""); err != nil || !recorded {
		t.Fatalf("recorded=%v err=%v", recorded, err)
	}
	wantLedger(t, db, "mr-nonote",
		"claimed|amount_cents=1447", "authorized|provider_payment_id=pi_mr_nonote",
		"refund_started|attempt 1", "refund_failed|gave up after 5 attempts",
		"refunded_by_hand|bank transfer 4471 recorded by dee@example.com")
	// and the alert it closes says no more than there is to say
	if got := alertsAbout(t, db, "mr-nonote")[domain.AlertRefundGivenUp].ResolvedNote; got != "refunded by hand (bank transfer 4471), recorded by dee@example.com" {
		t.Errorf("alert note = %q", got)
	}
}

func TestRecordingARefundClosesTheAlertsAboutItAndNoOthers(t *testing.T) {
	db := testAdapter(t)
	refundGivenUp(t, db, "mr-alerts", "pi_mr_alerts")
	raiseFor(t, db, domain.AlertRefundReversed, "mr-alerts")
	raiseFor(t, db, domain.AlertRefundUnrecorded, "mr-alerts")
	raiseFor(t, db, domain.AlertContradictingEvent, "mr-alerts") // not about the refund
	refundGivenUp(t, db, "mr-other", "pi_mr_other")              // another payment's alert

	if _, recorded, err := db.RecordManualRefund(context.Background(), "mr-alerts", "re_9", "dee@example.com", "done"); err != nil || !recorded {
		t.Fatalf("recorded=%v err=%v", recorded, err)
	}
	mine := alertsAbout(t, db, "mr-alerts")
	for _, kind := range []string{domain.AlertRefundGivenUp, domain.AlertRefundReversed, domain.AlertRefundUnrecorded} {
		a := mine[kind]
		if a.Open() || !strings.Contains(a.ResolvedNote, "refunded by hand (re_9)") || !strings.Contains(a.ResolvedNote, "dee@example.com") || !strings.Contains(a.ResolvedNote, ": done") {
			t.Errorf("%s alert = open %v, note %q, want it closed and saying who and what", kind, a.Open(), a.ResolvedNote)
		}
	}
	if !mine[domain.AlertContradictingEvent].Open() {
		t.Error("an alert that is not about the refund was closed")
	}
	if !alertsAbout(t, db, "mr-other")[domain.AlertRefundGivenUp].Open() {
		t.Error("another payment's alert was closed")
	}
}

func TestAnAlertThatWasAlreadyResolvedKeepsItsNote(t *testing.T) {
	db := testAdapter(t)
	refundGivenUp(t, db, "mr-keep", "pi_mr_keep")
	a := alertsAbout(t, db, "mr-keep")[domain.AlertRefundGivenUp]
	if _, err := db.ResolveAlert(context.Background(), a.ID, "someone looked"); err != nil {
		t.Fatal(err)
	}
	if _, recorded, err := db.RecordManualRefund(context.Background(), "mr-keep", "re_1", "dee@example.com", ""); err != nil || !recorded {
		t.Fatalf("recorded=%v err=%v", recorded, err)
	}
	if got := alertsAbout(t, db, "mr-keep")[domain.AlertRefundGivenUp]; got.ResolvedNote != "someone looked" {
		t.Errorf("note = %q, want the earlier one kept", got.ResolvedNote)
	}
}

// Only a refund that failed can be recorded. Anything else is handed back as it is, and
// nothing changes: not the payment, not the alert, not the ledger.
func TestOnlyAFailedRefundCanBeRecorded(t *testing.T) {
	type setup func(t *testing.T, db *DBAdapter, id string)
	ctx := context.Background()
	for name, c := range map[string]struct {
		prepare setup
		refund  domain.RefundStatus
	}{
		"not tried yet": {func(t *testing.T, db *DBAdapter, id string) {
			chargedAndCancelled(t, db, id, domain.PaymentCard, "pi_"+id)
		}, domain.RefundNone},
		"being made": {func(t *testing.T, db *DBAdapter, id string) {
			chargedAndCancelled(t, db, id, domain.PaymentCard, "pi_"+id)
			if _, ok, err := db.ClaimRefund(ctx, "pay-"+id, time.Minute); err != nil || !ok {
				t.Fatal(ok, err)
			}
		}, domain.RefundInProgress},
		"already done": {func(t *testing.T, db *DBAdapter, id string) {
			chargedAndCancelled(t, db, id, domain.PaymentCard, "pi_"+id)
			if _, ok, err := db.ClaimRefund(ctx, "pay-"+id, time.Minute); err != nil || !ok {
				t.Fatal(ok, err)
			}
			if err := db.CompleteRefund(ctx, "pay-"+id, "re_job"); err != nil {
				t.Fatal(err)
			}
		}, domain.RefundDone},
	} {
		id := "mr-no-" + strings.ReplaceAll(name, " ", "-")
		d := testAdapter(t)
		c.prepare(t, d, id)
		raiseFor(t, d, domain.AlertRefundGivenUp, id)
		before := ledger(t, d, id)

		p, recorded, err := d.RecordManualRefund(ctx, id, "re_hand", "dee@example.com", "n")
		if err != nil || recorded {
			t.Errorf("%s: recorded=%v err=%v, want it refused", name, recorded, err)
		}
		if p.RefundStatus != c.refund || p.ID != "pay-"+id {
			t.Errorf("%s: payment handed back = %+v, want it as it stands (refund %q)", name, p, c.refund)
		}
		if got := refundOf(t, d, "pay-"+id); got.RefundStatus != c.refund || got.RefundID == "re_hand" {
			t.Errorf("%s: stored = %+v, want nothing changed", name, got)
		}
		if !alertsAbout(t, d, id)[domain.AlertRefundGivenUp].Open() {
			t.Errorf("%s: the alert was closed although nothing was recorded", name)
		}
		if after := ledger(t, d, id); strings.Join(after, "|") != strings.Join(before, "|") {
			t.Errorf("%s: the ledger changed: %v -> %v", name, before, after)
		}
	}
}

func TestAPaymentThatWasNeverTakenCannotBeRecorded(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "mr-declined")
	claim(t, db, "mr-declined", "pay-mr-declined", time.Minute)
	if _, err := db.CompletePayment(ctx, "pay-mr-declined", domain.PaymentDeclined, "", "insufficient funds"); err != nil {
		t.Fatal(err)
	}
	p, recorded, err := db.RecordManualRefund(ctx, "mr-declined", "re_x", "dee@example.com", "")
	if err != nil || recorded || p.Status != domain.PaymentDeclined {
		t.Errorf("recorded=%v err=%v payment=%+v, want a refusal that shows the declined payment", recorded, err, p)
	}
}

func TestAnOrderWithoutAPaymentHasNothingToRecord(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "mr-nopay")
	for _, id := range []string{"mr-nopay", "mr-does-not-exist"} {
		_, recorded, err := db.RecordManualRefund(context.Background(), id, "re_x", "dee@example.com", "")
		if !errors.Is(err, ports.ErrPaymentNotFound) || recorded {
			t.Errorf("%s: recorded=%v err=%v, want ErrPaymentNotFound", id, recorded, err)
		}
	}
}

func TestOfTwoPeopleRecordingAtOnceExactlyOneDoes(t *testing.T) {
	db := testAdapter(t)
	refundGivenUp(t, db, "mr-race", "pi_mr_race")

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, recorded, err := db.RecordManualRefund(context.Background(), "mr-race", "re_"+string(rune('a'+i)), "dee@example.com", "")
			if err != nil {
				t.Error(err)
			}
			if recorded {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("%d recordings succeeded, want exactly 1", wins)
	}
	byHand := 0
	for _, line := range ledger(t, db, "mr-race") {
		if strings.HasPrefix(line, "refunded_by_hand|") {
			byHand++
		}
	}
	if byHand != 1 {
		t.Errorf("%d by-hand lines in the ledger, want 1", byHand)
	}
}

// A refund recorded by hand with the provider's own refund id can still be reversed by the
// provider; then it is failed again, the manual record is cleared, and it can be recorded anew.
func TestARecordedRefundThatTheProviderReversesCanBeRecordedAgain(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	refundGivenUp(t, db, "mr-again", "pi_mr_again")
	if _, recorded, err := db.RecordManualRefund(ctx, "mr-again", "re_again", "dee@example.com", "first"); err != nil || !recorded {
		t.Fatalf("first: recorded=%v err=%v", recorded, err)
	}
	if _, reversed, err := db.ReverseRefund(ctx, "pi_mr_again", "re_again", "reversed: expired card"); err != nil || !reversed {
		t.Fatalf("reverse: reversed=%v err=%v", reversed, err)
	}
	if by, note := manualColumns(t, db, "pay-mr-again"); by != "" || note != "" {
		t.Errorf("after the reversal the manual record is %q / %q, want it cleared", by, note)
	}
	if _, recorded, err := db.RecordManualRefund(ctx, "mr-again", "re_again_2", "ann@example.com", "second"); err != nil || !recorded {
		t.Fatalf("second: recorded=%v err=%v", recorded, err)
	}
	wantLedger(t, db, "mr-again",
		"claimed|amount_cents=1447", "authorized|provider_payment_id=pi_mr_again",
		"refund_started|attempt 1", "refund_failed|gave up after 5 attempts",
		"refunded_by_hand|re_again recorded by dee@example.com: first",
		"refund_failed|reversed: expired card",
		"refunded_by_hand|re_again_2 recorded by ann@example.com: second")
}

// A refund the job makes is still written as an ordinary refund, not as one by hand.
func TestARefundTheJobMakesIsNotCalledByHand(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	chargedAndCancelled(t, db, "mr-job", domain.PaymentCard, "pi_mr_job")
	if _, ok, err := db.ClaimRefund(ctx, "pay-mr-job", time.Minute); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := db.CompleteRefund(ctx, "pay-mr-job", "re_job"); err != nil {
		t.Fatal(err)
	}
	wantLedger(t, db, "mr-job",
		"claimed|amount_cents=1447", "authorized|provider_payment_id=pi_mr_job", "refund_started|attempt 1", "refunded|refund_id=re_job")
}

func TestRecordingNeedsTheOrderTheReferenceAndWho(t *testing.T) {
	db := testAdapter(t)
	refundGivenUp(t, db, "mr-args", "pi_mr_args")
	for _, c := range [][3]string{{"", "r", "who"}, {"mr-args", "", "who"}, {"mr-args", "r", ""}} {
		if _, recorded, err := db.RecordManualRefund(context.Background(), c[0], c[1], c[2], ""); err == nil || recorded {
			t.Errorf("%v: recorded=%v err=%v, want an error", c, recorded, err)
		}
	}
	if got := refundOf(t, db, "pay-mr-args"); got.RefundStatus != domain.RefundFailed {
		t.Errorf("refund = %q, want it left failed", got.RefundStatus)
	}
}

func TestARecordingThatFailsIsAnErrorAndChangesNothing(t *testing.T) {
	db := testAdapter(t)
	refundGivenUp(t, db, "mr-ctx", "pi_mr_ctx")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, recorded, err := db.RecordManualRefund(ctx, "mr-ctx", "re_1", "dee@example.com", ""); err == nil || recorded || errors.Is(err, ports.ErrPaymentNotFound) {
		t.Errorf("recorded=%v err=%v, want an internal error", recorded, err)
	}
	if got := refundOf(t, db, "pay-mr-ctx"); got.RefundStatus != domain.RefundFailed {
		t.Errorf("refund = %q, want it left failed", got.RefundStatus)
	}
}

// An order paid again after a decline has several payments. The refund is of the one that
// was taken, and a refusal describes that one, not an earlier attempt.
func TestWithSeveralAttemptsItIsTheOneThatWasTakenThatIsRefunded(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "mr-attempts")
	startAttempt(t, db, "mr-attempts", "pay-mr-attempts-1")
	decline(t, db, "mr-attempts", "pay-mr-attempts-1")
	startAttempt(t, db, "mr-attempts", "pay-mr-attempts-2")
	if _, err := db.CompletePayment(ctx, "pay-mr-attempts-2", domain.PaymentAuthorized, "pi_mr_attempts", ""); err != nil {
		t.Fatal(err)
	}
	cancelOrder(t, db, "mr-attempts")
	if _, ok, err := db.ClaimRefund(ctx, "pay-mr-attempts-2", time.Minute); err != nil || !ok {
		t.Fatalf("ClaimRefund: ok=%v err=%v", ok, err)
	}

	// being refunded: refused, and the payment handed back is the attempt that was taken
	p, recorded, err := db.RecordManualRefund(ctx, "mr-attempts", "re_x", "dee@example.com", "")
	if err != nil || recorded || p.ID != "pay-mr-attempts-2" || p.Attempt != 2 || p.RefundStatus != domain.RefundInProgress {
		t.Fatalf("recorded=%v err=%v payment=%+v, want a refusal that shows attempt 2", recorded, err, p)
	}

	if err := db.FailRefund(ctx, "pay-mr-attempts-2", "gave up", true); err != nil {
		t.Fatal(err)
	}
	p, recorded, err = db.RecordManualRefund(ctx, "mr-attempts", "re_hand", "dee@example.com", "")
	if err != nil || !recorded || p.ID != "pay-mr-attempts-2" || p.RefundStatus != domain.RefundDone {
		t.Fatalf("recorded=%v err=%v payment=%+v, want attempt 2 refunded", recorded, err, p)
	}
	if first := refundOf(t, db, "pay-mr-attempts-1"); first.Status != domain.PaymentDeclined || first.RefundStatus == domain.RefundDone {
		t.Errorf("the declined attempt was touched: %+v", first)
	}
}
