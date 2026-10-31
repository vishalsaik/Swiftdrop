package adapters

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// The ledger is written by a trigger on payments (see schema.sql), so these tests
// change payments through the normal methods and read the history back.

// ledger returns the history of an order as "kind|detail" strings, oldest first.
func ledger(t *testing.T, db *DBAdapter, orderID string) []string {
	t.Helper()
	events, err := db.ListPaymentEvents(context.Background(), orderID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		out = append(out, e.Kind+"|"+e.Detail)
	}
	return out
}

func wantLedger(t *testing.T, db *DBAdapter, orderID string, want ...string) {
	t.Helper()
	got := ledger(t, db, orderID)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("history of %s =\n  %s\nwant\n  %s", orderID, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestEveryStepOfAPaymentIsRecordedInTheLedger(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "lg-steps")

	claim(t, db, "lg-steps", "pay-lg-steps", time.Minute)
	wantLedger(t, db, "lg-steps", "claimed|amount_cents=1447")

	expire(t, db, "pay-lg-steps")
	if _, ok, err := db.TakeOverPayment(ctx, "pay-lg-steps", time.Minute); err != nil || !ok {
		t.Fatalf("takeover: ok=%v err=%v", ok, err)
	}
	if _, err := db.CompletePayment(ctx, "pay-lg-steps", domain.PaymentAuthorized, "pi_lg", ""); err != nil {
		t.Fatal(err)
	}
	wantLedger(t, db, "lg-steps",
		"claimed|amount_cents=1447", "taken_over|", "authorized|provider_payment_id=pi_lg")
}

// Only a takeover moves a lease forward. A lease that merely runs out (or is made
// to run out) is not something that happened to the payment, so it is not recorded.
func TestALeaseThatRunsOutIsNotATakeover(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "lg-expire")
	claim(t, db, "lg-expire", "pay-lg-expire", time.Minute)
	expire(t, db, "pay-lg-expire")
	wantLedger(t, db, "lg-expire", "claimed|amount_cents=1447")
}

func TestADeclineIsRecordedWithItsReason(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "lg-decline")
	claim(t, db, "lg-decline", "pay-lg-decline", time.Minute)
	if _, err := db.CompletePayment(context.Background(), "pay-lg-decline", domain.PaymentDeclined, "pi_d", "Your card was declined."); err != nil {
		t.Fatal(err)
	}
	wantLedger(t, db, "lg-decline", "claimed|amount_cents=1447", "declined|Your card was declined.")
}

func TestARefusedTakeoverAndARefusedSecondCompletionWriteNothing(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "lg-refused")
	claim(t, db, "lg-refused", "pay-lg-refused", time.Hour)

	if _, ok, _ := db.TakeOverPayment(ctx, "pay-lg-refused", time.Minute); ok {
		t.Fatal("took over a live lease")
	}
	if _, err := db.CompletePayment(ctx, "pay-lg-refused", domain.PaymentAuthorized, "pi_1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompletePayment(ctx, "pay-lg-refused", domain.PaymentDeclined, "pi_2", "late"); !errors.Is(err, ports.ErrPaymentNotProcessing) {
		t.Fatalf("second completion error = %v", err)
	}
	wantLedger(t, db, "lg-refused", "claimed|amount_cents=1447", "authorized|provider_payment_id=pi_1")
}

// The history is part of the same transaction as the change: a change that is
// rolled back leaves no trace.
func TestAPaymentThatCouldNotBeRecordedLeavesNoHistory(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "lg-rollback")
	if _, claimed, err := db.StartPayment(ctx, domain.Payment{ID: "pay-lg-rollback", OrderID: "lg-rollback", AmountCents: 500}, time.Minute,
		string(domain.OrderCreated), string(domain.OrderPaymentPending)); err != nil || !claimed {
		t.Fatalf("start: claimed=%v err=%v", claimed, err)
	}
	wantLedger(t, db, "lg-rollback", "claimed|amount_cents=500")

	// The order is cancelled before the answer is recorded: the finish is rolled back.
	if _, err := db.Pool.Exec(ctx, "UPDATE orders SET state = 'cancelled' WHERE id = 'lg-rollback'"); err != nil {
		t.Fatal(err)
	}
	_, err := db.FinishPayment(ctx, "pay-lg-rollback", domain.PaymentAuthorized, "pi_x", "",
		string(domain.OrderPaymentPending), string(domain.OrderPaid))
	if !errors.Is(err, ports.ErrStateConflict) {
		t.Fatalf("finish error = %v, want a state conflict", err)
	}
	wantLedger(t, db, "lg-rollback", "claimed|amount_cents=500") // no "authorized"

	// Keeping the answer for the cancelled order is a separate, recorded step.
	if _, err := db.CompletePayment(ctx, "pay-lg-rollback", domain.PaymentAuthorized, "pi_x", ""); err != nil {
		t.Fatal(err)
	}
	wantLedger(t, db, "lg-rollback", "claimed|amount_cents=500", "authorized|provider_payment_id=pi_x")
}

func TestAClaimThatLosesTheOrderRaceLeavesNothingBehind(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "lg-lostclaim")
	if _, err := db.Pool.Exec(ctx, "UPDATE orders SET state = 'cancelled' WHERE id = 'lg-lostclaim'"); err != nil {
		t.Fatal(err)
	}
	_, claimed, err := db.StartPayment(ctx, domain.Payment{ID: "pay-lg-lostclaim", OrderID: "lg-lostclaim", AmountCents: 500}, time.Minute,
		string(domain.OrderCreated), string(domain.OrderPaymentPending))
	if !errors.Is(err, ports.ErrStateConflict) || claimed {
		t.Fatalf("claimed=%v err=%v, want a state conflict", claimed, err)
	}
	wantLedger(t, db, "lg-lostclaim") // the claim was rolled back with its history
}

func TestARefundIsRecordedStepByStep(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	chargedAndCancelled(t, db, "lg-refund", domain.PaymentCard, "pi_r")
	want := []string{"claimed|amount_cents=1447", "authorized|provider_payment_id=pi_r"}

	if _, ok, _ := db.ClaimRefund(ctx, "pay-lg-refund", time.Hour); !ok {
		t.Fatal("claim failed")
	}
	if err := db.FailRefund(ctx, "pay-lg-refund", "stripe: HTTP 500", false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE payments SET refund_lease_expires_at = now() - interval '1 second' WHERE id = 'pay-lg-refund'"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.ClaimRefund(ctx, "pay-lg-refund", time.Hour); !ok {
		t.Fatal("second claim failed")
	}
	if err := db.CompleteRefund(ctx, "pay-lg-refund", "re_77"); err != nil {
		t.Fatal(err)
	}
	want = append(want,
		"refund_started|attempt 1",
		"refund_attempt_failed|stripe: HTTP 500",
		"refund_started|attempt 2",
		"refunded|refund_id=re_77")
	wantLedger(t, db, "lg-refund", want...)
}

func TestAGivenUpRefundIsRecordedWithItsReason(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	chargedAndCancelled(t, db, "lg-giveup", domain.PaymentCard, "pi_g")
	if _, ok, _ := db.ClaimRefund(ctx, "pay-lg-giveup", time.Minute); !ok {
		t.Fatal("claim failed")
	}
	if err := db.FailRefund(ctx, "pay-lg-giveup", "no such charge", true); err != nil {
		t.Fatal(err)
	}
	got := ledger(t, db, "lg-giveup")
	if last := got[len(got)-1]; last != "refund_failed|no such charge" {
		t.Errorf("last event = %q, want the give-up with its reason", last)
	}
}

func TestChangesThatAreNotWorthARecordWriteNoHistory(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "lg-quiet")
	claim(t, db, "lg-quiet", "pay-lg-quiet", time.Minute)
	if _, err := db.Pool.Exec(ctx, "UPDATE payments SET updated_at = now(), amount_cents = amount_cents WHERE id = 'pay-lg-quiet'"); err != nil {
		t.Fatal(err)
	}
	wantLedger(t, db, "lg-quiet", "claimed|amount_cents=1447")
}

func TestTheHistoryIsOldestFirstWithIncreasingIdsAndTimes(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	chargedAndCancelled(t, db, "lg-order", domain.PaymentCard, "pi_o")
	if _, ok, _ := db.ClaimRefund(ctx, "pay-lg-order", time.Minute); !ok {
		t.Fatal("claim failed")
	}
	events, err := db.ListPaymentEvents(ctx, "lg-order")
	if err != nil || len(events) != 3 {
		t.Fatalf("%d events, err %v, want 3", len(events), err)
	}
	for i, e := range events {
		if e.PaymentID != "pay-lg-order" || e.At.IsZero() || e.ID == 0 {
			t.Errorf("event %d lost a field: %+v", i, e)
		}
		if i > 0 && (e.ID <= events[i-1].ID || e.At.Before(events[i-1].At)) {
			t.Errorf("event %d (%+v) is not after event %d (%+v)", i, e, i-1, events[i-1])
		}
	}
}

func TestTheHistoryOfOneOrderDoesNotIncludeAnothers(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "lg-a")
	paidOrder(t, db, "lg-b")
	claim(t, db, "lg-a", "pay-lg-a", time.Minute)
	claim(t, db, "lg-b", "pay-lg-b", time.Minute)
	if got := ledger(t, db, "lg-a"); len(got) != 1 {
		t.Errorf("history of lg-a = %v, want only its own claim", got)
	}
	if got := ledger(t, db, "no-such-order"); len(got) != 0 {
		t.Errorf("history of a missing order = %v, want empty", got)
	}
}

// The history cannot be rewritten, and a payment cannot be deleted from under it.
func TestTheLedgerIsAppendOnly(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "lg-immutable")
	claim(t, db, "lg-immutable", "pay-lg-immutable", time.Minute)

	_, err := db.Pool.Exec(ctx, "UPDATE payment_events SET kind = 'forged' WHERE payment_id = 'pay-lg-immutable'")
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("rewriting the history: err = %v, want it refused as append-only", err)
	}
	if _, err := db.Pool.Exec(ctx, "DELETE FROM payments WHERE id = 'pay-lg-immutable'"); err == nil {
		t.Error("a payment with a history was deleted")
	}
	wantLedger(t, db, "lg-immutable", "claimed|amount_cents=1447")
}

func TestTheHistoryFailsAsAnInternalErrorWhenTheCallerGivesUp(t *testing.T) {
	db := testAdapter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := db.ListPaymentEvents(ctx, "anything")
	if err == nil || got != nil {
		t.Errorf("got %v, %v, want an error and no rows", got, err)
	}
}
