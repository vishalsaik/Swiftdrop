package adapters

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// These need the same Postgres as the other adapter tests.

// chargedAndCancelled makes an order of the given payment type whose payment was
// authorized and whose order is then cancelled: a refund that is due.
func chargedAndCancelled(t *testing.T, db *DBAdapter, id string, pt domain.PaymentType, providerID string) {
	t.Helper()
	ctx := context.Background()
	paidOrder(t, db, id)
	if _, err := db.Pool.Exec(ctx, "UPDATE orders SET payment_type = $2 WHERE id = $1", id, string(pt)); err != nil {
		t.Fatal(err)
	}
	claim(t, db, id, "pay-"+id, time.Minute)
	if _, err := db.CompletePayment(ctx, "pay-"+id, domain.PaymentAuthorized, providerID, ""); err != nil {
		t.Fatal(err)
	}
	cancelOrder(t, db, id)
}

func cancelOrder(t *testing.T, db *DBAdapter, id string) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), "UPDATE orders SET state = 'cancelled' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
}

func dueIDs(t *testing.T, db *DBAdapter) []string {
	t.Helper()
	list, err := db.ListRefundable(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range list {
		ids = append(ids, p.ID)
	}
	return ids
}

func refundOf(t *testing.T, db *DBAdapter, paymentID string) domain.Payment {
	t.Helper()
	p, err := db.GetPayment(context.Background(), paymentID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ---------------------------------------------------------------------------
// ListRefundable
// ---------------------------------------------------------------------------

func TestAChargedAndCancelledPaymentIsDue(t *testing.T) {
	db := testAdapter(t)
	chargedAndCancelled(t, db, "rf-due", domain.PaymentCard, "pi_due")

	if !slices.Contains(dueIDs(t, db), "pay-rf-due") {
		t.Error("an authorized payment on a cancelled card order should be due")
	}
	p := refundOf(t, db, "pay-rf-due")
	if p.RefundStatus != domain.RefundNone || p.RefundAttempts != 0 || p.RefundID != "" || p.RefundLeaseExpiresAt != nil {
		t.Errorf("a fresh payment has refund state %+v, want none", p)
	}
}

func TestWalletPaymentsAreDueToo(t *testing.T) {
	db := testAdapter(t)
	chargedAndCancelled(t, db, "rf-wallet", domain.PaymentWallet, "pi_wallet")
	if !slices.Contains(dueIDs(t, db), "pay-rf-wallet") {
		t.Error("a wallet payment goes through the gateway, so its refund is due")
	}
}

func TestPaymentsThatOweNothingAreNotListed(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()

	// cash: no money moved through us
	chargedAndCancelled(t, db, "rf-cash", domain.PaymentCash, "cash-uuid")
	// the order was not cancelled
	chargedAndCancelled(t, db, "rf-alive", domain.PaymentCard, "pi_alive")
	if _, err := db.Pool.Exec(ctx, "UPDATE orders SET state = 'paid' WHERE id = 'rf-alive'"); err != nil {
		t.Fatal(err)
	}
	// declined, still processing
	paidOrder(t, db, "rf-declined")
	claim(t, db, "rf-declined", "pay-rf-declined", time.Minute)
	if _, err := db.CompletePayment(ctx, "pay-rf-declined", domain.PaymentDeclined, "pi_d", "no"); err != nil {
		t.Fatal(err)
	}
	cancelOrder(t, db, "rf-declined")
	paidOrder(t, db, "rf-processing")
	claim(t, db, "rf-processing", "pay-rf-processing", time.Minute)
	cancelOrder(t, db, "rf-processing")
	// authorized but the provider id was never stored
	chargedAndCancelled(t, db, "rf-noid", domain.PaymentCard, "")
	// already refunded and given up on
	chargedAndCancelled(t, db, "rf-done", domain.PaymentCard, "pi_done")
	if _, err := db.Pool.Exec(ctx, "UPDATE payments SET refund_status = 'refunded', refund_id = 're_1' WHERE id = 'pay-rf-done'"); err != nil {
		t.Fatal(err)
	}
	chargedAndCancelled(t, db, "rf-failed", domain.PaymentCard, "pi_failed")
	if _, err := db.Pool.Exec(ctx, "UPDATE payments SET refund_status = 'failed' WHERE id = 'pay-rf-failed'"); err != nil {
		t.Fatal(err)
	}
	// held by a worker with a live lease
	chargedAndCancelled(t, db, "rf-held", domain.PaymentCard, "pi_held")
	if _, ok, err := db.ClaimRefund(ctx, "pay-rf-held", time.Hour); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	listed := dueIDs(t, db)
	for _, id := range []string{"cash", "alive", "declined", "processing", "noid", "done", "failed", "held"} {
		if slices.Contains(listed, "pay-rf-"+id) {
			t.Errorf("pay-rf-%s was listed, but it owes no refund", id)
		}
	}
}

func TestARefundWhoseWorkerDiedIsDueAgain(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	chargedAndCancelled(t, db, "rf-expired", domain.PaymentCard, "pi_exp")
	if _, ok, err := db.ClaimRefund(ctx, "pay-rf-expired", time.Hour); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if slices.Contains(dueIDs(t, db), "pay-rf-expired") {
		t.Fatal("a refund with a live lease must not be listed")
	}

	if _, err := db.Pool.Exec(ctx, "UPDATE payments SET refund_lease_expires_at = now() - interval '1 second' WHERE id = 'pay-rf-expired'"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(dueIDs(t, db), "pay-rf-expired") {
		t.Error("a refund whose lease ran out should be due again")
	}
}

func TestListRefundableIsOldestFirstAndHonoursTheLimit(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	for _, id := range []string{"rf-ord-1", "rf-ord-2", "rf-ord-3"} {
		chargedAndCancelled(t, db, id, domain.PaymentCard, "pi_"+id)
	}
	// Make the order of updated_at unmistakable: 3 is oldest, then 1, then 2.
	for id, ago := range map[string]string{"rf-ord-3": "3 hours", "rf-ord-1": "2 hours", "rf-ord-2": "1 hour"} {
		if _, err := db.Pool.Exec(ctx, "UPDATE payments SET updated_at = now() - $2::interval WHERE id = $1", "pay-"+id, ago); err != nil {
			t.Fatal(err)
		}
	}
	all := dueIDs(t, db)
	var ours []string
	for _, id := range all {
		if slices.Contains([]string{"pay-rf-ord-1", "pay-rf-ord-2", "pay-rf-ord-3"}, id) {
			ours = append(ours, id)
		}
	}
	if want := []string{"pay-rf-ord-3", "pay-rf-ord-1", "pay-rf-ord-2"}; !slices.Equal(ours, want) {
		t.Errorf("order = %v, want oldest first %v", ours, want)
	}

	one, err := db.ListRefundable(ctx, 1)
	if err != nil || len(one) != 1 {
		t.Errorf("limit 1 returned %d rows, err %v", len(one), err)
	}
}

func TestListRefundableRejectsANonPositiveLimit(t *testing.T) {
	db := testAdapter(t)
	for _, n := range []int{0, -1} {
		if _, err := db.ListRefundable(context.Background(), n); err == nil {
			t.Errorf("limit %d was accepted", n)
		}
	}
}

// ---------------------------------------------------------------------------
// ClaimRefund
// ---------------------------------------------------------------------------

func TestClaimRefundTakesADueRefundWithALeaseOnTheDatabaseClock(t *testing.T) {
	db := testAdapter(t)
	chargedAndCancelled(t, db, "rf-claim", domain.PaymentCard, "pi_claim")

	p, ok, err := db.ClaimRefund(context.Background(), "pay-rf-claim", 90*time.Second)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if p.RefundStatus != domain.RefundInProgress || p.RefundAttempts != 1 || p.RefundLeaseExpiresAt == nil {
		t.Fatalf("claimed payment = %+v", p)
	}
	if left := time.Until(*p.RefundLeaseExpiresAt); left < 80*time.Second || left > 100*time.Second {
		t.Errorf("the lease has %v left, want about 90s", left)
	}
	if p.ProviderPaymentID != "pi_claim" || p.AmountCents != 1447 {
		t.Errorf("claimed payment lost its facts: %+v", p)
	}
}

func TestClaimRefundRefusesWhileTheLeaseIsLiveAndAllowsATakeoverAfter(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	chargedAndCancelled(t, db, "rf-twice", domain.PaymentCard, "pi_twice")

	if _, ok, _ := db.ClaimRefund(ctx, "pay-rf-twice", time.Hour); !ok {
		t.Fatal("the first claim should succeed")
	}
	if _, ok, err := db.ClaimRefund(ctx, "pay-rf-twice", time.Hour); ok || err != nil {
		t.Errorf("second claim while the lease is live: ok=%v err=%v, want a plain refusal", ok, err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE payments SET refund_lease_expires_at = now() - interval '1 second' WHERE id = 'pay-rf-twice'"); err != nil {
		t.Fatal(err)
	}
	p, ok, err := db.ClaimRefund(ctx, "pay-rf-twice", time.Hour)
	if err != nil || !ok {
		t.Fatalf("takeover after expiry: ok=%v err=%v", ok, err)
	}
	if p.RefundAttempts != 2 {
		t.Errorf("attempts = %d, want 2", p.RefundAttempts)
	}
}

func TestClaimRefundRefusesWhatIsNotOwed(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()

	chargedAndCancelled(t, db, "rf-c-cash", domain.PaymentCash, "cash-uuid")
	chargedAndCancelled(t, db, "rf-c-alive", domain.PaymentCard, "pi_a")
	if _, err := db.Pool.Exec(ctx, "UPDATE orders SET state = 'delivered' WHERE id = 'rf-c-alive'"); err != nil {
		t.Fatal(err)
	}
	chargedAndCancelled(t, db, "rf-c-done", domain.PaymentCard, "pi_d")
	if _, err := db.Pool.Exec(ctx, "UPDATE payments SET refund_status = 'refunded' WHERE id = 'pay-rf-c-done'"); err != nil {
		t.Fatal(err)
	}
	paidOrder(t, db, "rf-c-processing")
	claim(t, db, "rf-c-processing", "pay-rf-c-processing", time.Minute)
	cancelOrder(t, db, "rf-c-processing")

	for _, id := range []string{"rf-c-cash", "rf-c-alive", "rf-c-done", "rf-c-processing"} {
		before := refundOf(t, db, "pay-"+id)
		_, ok, err := db.ClaimRefund(ctx, "pay-"+id, time.Minute)
		if ok || err != nil {
			t.Errorf("%s: ok=%v err=%v, want a plain refusal", id, ok, err)
		}
		if after := refundOf(t, db, "pay-"+id); after.RefundStatus != before.RefundStatus || after.RefundAttempts != before.RefundAttempts {
			t.Errorf("%s: a refused claim changed the payment from %+v to %+v", id, before, after)
		}
	}
}

func TestClaimRefundOfAMissingPaymentAndBadLease(t *testing.T) {
	db := testAdapter(t)
	_, ok, err := db.ClaimRefund(context.Background(), "no-such-payment", time.Minute)
	if ok {
		t.Error("claimed a payment that does not exist")
	}
	wantPaymentErr(t, err, ports.ErrPaymentNotFound)

	chargedAndCancelled(t, db, "rf-lease", domain.PaymentCard, "pi_l")
	for _, lease := range []time.Duration{0, -time.Second} {
		if _, ok, err := db.ClaimRefund(context.Background(), "pay-rf-lease", lease); ok || err == nil {
			t.Errorf("lease %v: ok=%v err=%v, want an error", lease, ok, err)
		}
	}
	if got := refundOf(t, db, "pay-rf-lease"); got.RefundStatus != domain.RefundNone {
		t.Errorf("a refused claim left refund status %s", got.RefundStatus)
	}
}

func TestConcurrentRefundClaimsHaveExactlyOneOwner(t *testing.T) {
	db := testAdapter(t)
	chargedAndCancelled(t, db, "rf-race", domain.PaymentCard, "pi_race")

	const n = 20
	oks := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			_, oks[i], errs[i] = db.ClaimRefund(context.Background(), "pay-rf-race", time.Minute)
		})
	}
	close(start)
	wg.Wait()

	owners := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("claim %d: %v", i, errs[i])
		}
		if oks[i] {
			owners++
		}
	}
	if owners != 1 {
		t.Errorf("%d claimers got the refund, want exactly 1", owners)
	}
	if got := refundOf(t, db, "pay-rf-race"); got.RefundAttempts != 1 {
		t.Errorf("attempts = %d, want 1", got.RefundAttempts)
	}
}

// ---------------------------------------------------------------------------
// CompleteRefund and FailRefund
// ---------------------------------------------------------------------------

func TestCompleteRefundRecordsTheRefund(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	chargedAndCancelled(t, db, "rf-complete", domain.PaymentCard, "pi_c")
	if _, ok, _ := db.ClaimRefund(ctx, "pay-rf-complete", time.Minute); !ok {
		t.Fatal("claim failed")
	}
	if err := db.FailRefund(ctx, "pay-rf-complete", "stripe was down", false); err != nil {
		t.Fatal(err)
	}

	if err := db.CompleteRefund(ctx, "pay-rf-complete", "re_123"); err != nil {
		t.Fatal(err)
	}
	p := refundOf(t, db, "pay-rf-complete")
	if p.RefundStatus != domain.RefundDone || p.RefundID != "re_123" || p.RefundLeaseExpiresAt != nil || p.RefundError != "" {
		t.Errorf("payment = %+v, want refunded with re_123, no lease and the old error cleared", p)
	}
	if p.Status != domain.PaymentAuthorized {
		t.Errorf("status = %s, want the payment itself left authorized", p.Status)
	}
	if slices.Contains(dueIDs(t, db), "pay-rf-complete") {
		t.Error("a refunded payment is still listed as due")
	}
}

func TestCompleteRefundOnlyWhileItIsInProgress(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	chargedAndCancelled(t, db, "rf-comp2", domain.PaymentCard, "pi_c2")

	wantPaymentErr(t, db.CompleteRefund(ctx, "pay-rf-comp2", "re_1"), ports.ErrRefundNotInProgress) // never claimed
	if _, ok, _ := db.ClaimRefund(ctx, "pay-rf-comp2", time.Minute); !ok {
		t.Fatal("claim failed")
	}
	if err := db.CompleteRefund(ctx, "pay-rf-comp2", "re_1"); err != nil {
		t.Fatal(err)
	}
	wantPaymentErr(t, db.CompleteRefund(ctx, "pay-rf-comp2", "re_2"), ports.ErrRefundNotInProgress) // twice
	if got := refundOf(t, db, "pay-rf-comp2").RefundID; got != "re_1" {
		t.Errorf("refund id = %q, a second completion overwrote the first", got)
	}
	wantPaymentErr(t, db.CompleteRefund(ctx, "no-such-payment", "re_1"), ports.ErrPaymentNotFound)
	if err := db.CompleteRefund(ctx, "pay-rf-comp2", ""); err == nil {
		t.Error("an empty refund id was accepted")
	}
}

// A refund with no id cannot be recorded: the payment must stay in progress, so
// it is not lost as "refunded" with nothing to show for it.
func TestCompleteRefundNeedsARefundIdAndChangesNothingWithout(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	chargedAndCancelled(t, db, "rf-noid-complete", domain.PaymentCard, "pi_n")
	if _, ok, _ := db.ClaimRefund(ctx, "pay-rf-noid-complete", time.Minute); !ok {
		t.Fatal("claim failed")
	}

	err := db.CompleteRefund(ctx, "pay-rf-noid-complete", "")
	if err == nil || errors.Is(err, ports.ErrRefundNotInProgress) {
		t.Errorf("error = %v, want a complaint about the missing id, not a state error", err)
	}
	if got := refundOf(t, db, "pay-rf-noid-complete").RefundStatus; got != domain.RefundInProgress {
		t.Errorf("refund status = %s, want it left in progress", got)
	}
}

func TestFailRefundKeepsItInProgressUntilTheLeaseRunsOut(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	chargedAndCancelled(t, db, "rf-fail", domain.PaymentCard, "pi_f")
	claimed, ok, _ := db.ClaimRefund(ctx, "pay-rf-fail", time.Hour)
	if !ok {
		t.Fatal("claim failed")
	}

	if err := db.FailRefund(ctx, "pay-rf-fail", "stripe: HTTP 500", false); err != nil {
		t.Fatal(err)
	}
	p := refundOf(t, db, "pay-rf-fail")
	if p.RefundStatus != domain.RefundInProgress || p.RefundError != "stripe: HTTP 500" {
		t.Errorf("payment = %+v, want still in progress with the error", p)
	}
	if p.RefundLeaseExpiresAt == nil || !p.RefundLeaseExpiresAt.Equal(*claimed.RefundLeaseExpiresAt) {
		t.Errorf("lease changed from %v to %v, want it kept as the wait before the next attempt", claimed.RefundLeaseExpiresAt, p.RefundLeaseExpiresAt)
	}
	if slices.Contains(dueIDs(t, db), "pay-rf-fail") {
		t.Error("a failed attempt must wait for its lease before it is due again")
	}
}

func TestFailRefundCanGiveUp(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	chargedAndCancelled(t, db, "rf-giveup", domain.PaymentCard, "pi_g")
	if _, ok, _ := db.ClaimRefund(ctx, "pay-rf-giveup", time.Minute); !ok {
		t.Fatal("claim failed")
	}

	if err := db.FailRefund(ctx, "pay-rf-giveup", "no such charge", true); err != nil {
		t.Fatal(err)
	}
	p := refundOf(t, db, "pay-rf-giveup")
	if p.RefundStatus != domain.RefundFailed || p.RefundError != "no such charge" || p.RefundLeaseExpiresAt != nil {
		t.Errorf("payment = %+v, want failed with the reason and no lease", p)
	}
	if slices.Contains(dueIDs(t, db), "pay-rf-giveup") {
		t.Error("a payment that was given up on is still listed")
	}
	if _, ok, _ := db.ClaimRefund(ctx, "pay-rf-giveup", time.Minute); ok {
		t.Error("a failed refund was claimed again")
	}
}

func TestFailRefundOnlyWhileItIsInProgress(t *testing.T) {
	db := testAdapter(t)
	chargedAndCancelled(t, db, "rf-fail2", domain.PaymentCard, "pi_f2")
	wantPaymentErr(t, db.FailRefund(context.Background(), "pay-rf-fail2", "x", false), ports.ErrRefundNotInProgress)
	wantPaymentErr(t, db.FailRefund(context.Background(), "pay-rf-fail2", "x", true), ports.ErrRefundNotInProgress)
	wantPaymentErr(t, db.FailRefund(context.Background(), "no-such-payment", "x", true), ports.ErrPaymentNotFound)
	if got := refundOf(t, db, "pay-rf-fail2").RefundStatus; got != domain.RefundNone {
		t.Errorf("refund status = %s, a refused write changed it", got)
	}
}

// A caller that gives up must see an internal error, never a false statement
// about the refund.
func TestRefundStoreFailuresAreInternalErrors(t *testing.T) {
	db := testAdapter(t)
	chargedAndCancelled(t, db, "rf-ctx", domain.PaymentCard, "pi_x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	plain := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: want an error", name)
			return
		}
		for _, s := range []error{ports.ErrPaymentNotFound, ports.ErrRefundNotInProgress} {
			if errors.Is(err, s) {
				t.Errorf("%s: error %v must not wrap %v", name, err, s)
			}
		}
	}
	_, err := db.ListRefundable(ctx, 10)
	plain("ListRefundable", err)
	_, ok, err := db.ClaimRefund(ctx, "pay-rf-ctx", time.Minute)
	plain("ClaimRefund", err)
	if ok {
		t.Error("ClaimRefund claimed with a cancelled context")
	}
	plain("CompleteRefund", db.CompleteRefund(ctx, "pay-rf-ctx", "re_1"))
	plain("FailRefund", db.FailRefund(ctx, "pay-rf-ctx", "x", false))
	if got := refundOf(t, db, "pay-rf-ctx").RefundStatus; got != domain.RefundNone {
		t.Errorf("refund status = %s, a cancelled call changed it", got)
	}
}

// The schema refuses nonsense whatever the code does.
func TestRefundColumnsAreConstrained(t *testing.T) {
	db := testAdapter(t)
	chargedAndCancelled(t, db, "rf-schema", domain.PaymentCard, "pi_s")
	ctx := context.Background()
	for name, stmt := range map[string]string{
		"an unknown refund status": "UPDATE payments SET refund_status = 'maybe' WHERE id = 'pay-rf-schema'",
		"negative attempts":        "UPDATE payments SET refund_attempts = -1 WHERE id = 'pay-rf-schema'",
	} {
		if _, err := db.Pool.Exec(ctx, stmt); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
