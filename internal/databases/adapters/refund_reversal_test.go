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

// A refund the provider accepted can fail later. ReverseRefund moves exactly that
// refund from refunded to failed.

// refundedPayment is a charged, cancelled payment whose refund the provider accepted.
func refundedPayment(t *testing.T, db *DBAdapter, id, providerID, refundID string) {
	t.Helper()
	ctx := context.Background()
	chargedAndCancelled(t, db, id, domain.PaymentCard, providerID)
	if _, ok, err := db.ClaimRefund(ctx, "pay-"+id, time.Minute); err != nil || !ok {
		t.Fatalf("ClaimRefund: ok=%v err=%v", ok, err)
	}
	if err := db.CompleteRefund(ctx, "pay-"+id, refundID); err != nil {
		t.Fatal(err)
	}
}

func TestAReversedRefundBecomesFailedAndSaysWhy(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	refundedPayment(t, db, "rv-ok", "pi_rv_ok", "re_rv_ok")

	p, reversed, err := db.ReverseRefund(ctx, "pi_rv_ok", "re_rv_ok", "reversed: expired card")
	if err != nil || !reversed {
		t.Fatalf("ReverseRefund = %+v, %v, %v", p, reversed, err)
	}
	if p.RefundStatus != domain.RefundFailed || p.RefundError != "reversed: expired card" || p.ID != "pay-rv-ok" {
		t.Errorf("payment = %+v, want a failed refund that says why", p)
	}
	got := refundOf(t, db, "pay-rv-ok")
	if got.RefundStatus != domain.RefundFailed || got.RefundID != "re_rv_ok" || got.Status != domain.PaymentAuthorized {
		t.Errorf("stored = %+v: the refund failed, the charge stays authorized and its refund id is kept", got)
	}
	// the ledger knows through its trigger, with the reason
	wantLedger(t, db, "rv-ok",
		"claimed|amount_cents=1447", "authorized|provider_payment_id=pi_rv_ok",
		"refund_started|attempt 1", "refunded|refund_id=re_rv_ok", "refund_failed|reversed: expired card")
	// a failed refund is not due again: nothing asks the provider a second time
	for _, id := range dueIDs(t, db) {
		if id == "pay-rv-ok" {
			t.Error("a reversed refund is due again: the job would refund twice")
		}
	}
}

func TestAReversalIsRecordedOnce(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	refundedPayment(t, db, "rv-twice", "pi_rv_twice", "re_rv_twice")
	if _, reversed, err := db.ReverseRefund(ctx, "pi_rv_twice", "re_rv_twice", "first"); err != nil || !reversed {
		t.Fatalf("first: %v %v", reversed, err)
	}
	p, reversed, err := db.ReverseRefund(ctx, "pi_rv_twice", "re_rv_twice", "second")
	if err != nil || reversed {
		t.Fatalf("second: reversed=%v err=%v, want nothing changed", reversed, err)
	}
	if p.RefundStatus != domain.RefundFailed || p.RefundError != "first" {
		t.Errorf("payment = %+v, want the first reason kept", p)
	}
}

func TestOnlyTheRefundWeRecordedCanBeReversed(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	refundedPayment(t, db, "rv-other", "pi_rv_other", "re_rv_other")

	// another refund of the same payment (made by hand) failing says nothing about ours
	p, reversed, err := db.ReverseRefund(ctx, "pi_rv_other", "re_by_hand", "x")
	if err != nil || reversed || p.RefundStatus != domain.RefundDone {
		t.Errorf("ReverseRefund = %+v reversed=%v err=%v, want our refund untouched", p, reversed, err)
	}
}

func TestARefundThatIsNotDoneIsNotReversedAndSaysWhereItStands(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()

	// being made right now by the job: no id recorded yet
	chargedAndCancelled(t, db, "rv-busy", domain.PaymentCard, "pi_rv_busy")
	if _, ok, err := db.ClaimRefund(ctx, "pay-rv-busy", time.Minute); err != nil || !ok {
		t.Fatal(ok, err)
	}
	p, reversed, err := db.ReverseRefund(ctx, "pi_rv_busy", "re_x", "x")
	if err != nil || reversed || p.RefundStatus != domain.RefundInProgress {
		t.Errorf("busy: %+v reversed=%v err=%v, want it reported as in progress", p, reversed, err)
	}

	// no refund made at all
	chargedAndCancelled(t, db, "rv-none", domain.PaymentCard, "pi_rv_none")
	p, reversed, err = db.ReverseRefund(ctx, "pi_rv_none", "re_x", "x")
	if err != nil || reversed || p.RefundStatus != domain.RefundNone {
		t.Errorf("none: %+v reversed=%v err=%v, want it reported as none", p, reversed, err)
	}
}

func TestReversingAnUnknownProviderPaymentIsNotFound(t *testing.T) {
	db := testAdapter(t)
	_, reversed, err := db.ReverseRefund(context.Background(), "pi_nobody", "re_1", "x")
	wantPaymentErr(t, err, ports.ErrPaymentNotFound)
	if reversed {
		t.Error("reversed an unknown payment")
	}
}

func TestReversingNeedsBothIdsAndAWorkingDatabase(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	for _, c := range [][2]string{{"", "re_1"}, {"pi_1", ""}} {
		if _, _, err := db.ReverseRefund(ctx, c[0], c[1], "x"); err == nil || errors.Is(err, ports.ErrPaymentNotFound) {
			t.Errorf("ReverseRefund(%q, %q) = %v, want a refusal", c[0], c[1], err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := db.ReverseRefund(cancelled, "pi_1", "re_1", "x"); err == nil || errors.Is(err, ports.ErrPaymentNotFound) || !strings.Contains(err.Error(), "reverse refund") {
		t.Errorf("a cancelled context gave %v, want a plain failure", err)
	}
}

// A closed pool fails every query, so the failure is wrapped, not a driver error.
func TestReversalFailsCleanlyWhenTheDatabaseIsGone(t *testing.T) {
	db := testAdapter(t)
	db.Pool.Close()
	if _, _, err := db.ReverseRefund(context.Background(), "pi_1", "re_1", "x"); err == nil || !strings.Contains(err.Error(), "failed to reverse refund") {
		t.Errorf("error = %v, want a wrapped failure", err)
	}
}
