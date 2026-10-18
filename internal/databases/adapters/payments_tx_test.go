package adapters

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// StartPayment and FinishPayment change a payment AND its order in one
// transaction. What these tests prove is the "and": a failure of either half
// leaves both tables exactly as they were.

func setOrderState(t *testing.T, db *DBAdapter, id string, state domain.OrderState) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), "UPDATE orders SET state = $2 WHERE id = $1", id, string(state)); err != nil {
		t.Fatalf("setOrderState: %v", err)
	}
}

func orderState(t *testing.T, db *DBAdapter, id string) domain.OrderState {
	t.Helper()
	o, err := db.GetOrderbyId(context.Background(), id)
	if err != nil {
		t.Fatalf("GetOrderbyId(%s): %v", id, err)
	}
	return o.State
}

func start(db *DBAdapter, orderID, paymentID string) (domain.Payment, bool, error) {
	return db.StartPayment(context.Background(),
		domain.Payment{ID: paymentID, OrderID: orderID, AmountCents: 2500}, time.Minute,
		string(domain.OrderCreated), string(domain.OrderPaymentPending))
}

func wantNoPayment(t *testing.T, db *DBAdapter, orderID string) {
	t.Helper()
	if p, err := db.GetPaymentByOrder(context.Background(), orderID); !errors.Is(err, ports.ErrPaymentNotFound) {
		t.Errorf("order %s has a payment %+v (err %v), want none", orderID, p, err)
	}
}

// ---------------------------------------------------------------------------
// StartPayment
// ---------------------------------------------------------------------------

func TestStartPaymentClaimsAndMovesTheOrderTogether(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "tx-start-ok")

	p, claimed, err := start(db, "tx-start-ok", "tx-pay-1")
	if err != nil {
		t.Fatal(err)
	}
	if !claimed || p.ID != "tx-pay-1" || p.Status != domain.PaymentProcessing || p.AmountCents != 2500 {
		t.Errorf("claimed=%v payment=%+v", claimed, p)
	}
	if got := orderState(t, db, "tx-start-ok"); got != domain.OrderPaymentPending {
		t.Errorf("order state = %s, want payment_pending", got)
	}
}

// The order was no longer where the caller read it, so the claim must not
// survive: no payment row, and the order untouched.
func TestStartPaymentRollsBackTheClaimWhenTheOrderMovedOn(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "tx-start-moved")
	setOrderState(t, db, "tx-start-moved", domain.OrderCancelled) // another request got there first

	_, claimed, err := start(db, "tx-start-moved", "tx-pay-moved")
	wantConflict(t, err)
	if claimed {
		t.Error("claimed a payment for an order that had moved on")
	}
	wantNoPayment(t, db, "tx-start-moved")
	if got := orderState(t, db, "tx-start-moved"); got != domain.OrderCancelled {
		t.Errorf("order state = %s, want the other request's cancelled left alone", got)
	}
}

func TestStartPaymentWhenOneAlreadyExistsWritesNothing(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "tx-start-twice")
	first, _, err := start(db, "tx-start-twice", "tx-pay-first")
	if err != nil {
		t.Fatal(err)
	}

	// the order is already payment_pending, so a second start from "created"
	// must not be treated as a lost race: it simply finds the payment
	second, claimed, err := start(db, "tx-start-twice", "tx-pay-second")
	if err != nil {
		t.Fatalf("a second start must not fail: %v", err)
	}
	if claimed || second.ID != first.ID {
		t.Errorf("claimed=%v payment=%+v, want the first payment back unclaimed", claimed, second)
	}
	if got := orderState(t, db, "tx-start-twice"); got != domain.OrderPaymentPending {
		t.Errorf("order state = %s, want payment_pending", got)
	}
}

func TestStartPaymentForAMissingOrder(t *testing.T) {
	db := testAdapter(t)
	_, claimed, err := start(db, "it-does-not-exist", "tx-pay-orphan")
	wantNotFound(t, err)
	if claimed {
		t.Error("claimed a payment for a missing order")
	}
}

func TestConcurrentStartsHaveExactlyOneOwnerAndNoDeadlock(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "tx-start-race")

	const racers = 20
	type result struct {
		p       domain.Payment
		claimed bool
		err     error
	}
	results := make([]result, racers)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-gate
			p, claimed, err := start(db, "tx-start-race", fmt.Sprintf("tx-race-%d", i))
			results[i] = result{p, claimed, err}
		})
	}
	close(gate)
	wg.Wait()

	owner := ""
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("racer %d: %v", i, r.err)
		}
		if r.claimed {
			if owner != "" {
				t.Fatalf("two owners: %s and %s", owner, r.p.ID)
			}
			owner = r.p.ID
		}
	}
	if owner == "" {
		t.Fatal("nobody claimed the payment")
	}
	for i, r := range results {
		if r.p.ID != owner {
			t.Errorf("racer %d saw payment %s, want the owner's %s", i, r.p.ID, owner)
		}
	}
	if got := orderState(t, db, "tx-start-race"); got != domain.OrderPaymentPending {
		t.Errorf("order state = %s, want payment_pending", got)
	}
}

// A start racing a cancel: whichever wins, the two tables agree. A payment
// row exists exactly when the order is payment_pending.
func TestStartRacingACancelNeverLeavesAPaymentOnACancelledOrder(t *testing.T) {
	db := testAdapter(t)
	for round := range 10 {
		id := fmt.Sprintf("tx-start-cancel-%d", round)
		paidOrder(t, db, id)

		var wg sync.WaitGroup
		gate := make(chan struct{})
		wg.Go(func() {
			<-gate
			_, _, _ = start(db, id, "tx-pc-"+id)
		})
		wg.Go(func() {
			<-gate
			_, _ = db.UpdateOrderState(context.Background(), id, string(domain.OrderCreated), string(domain.OrderCancelled))
		})
		close(gate)
		wg.Wait()

		state := orderState(t, db, id)
		_, err := db.GetPaymentByOrder(context.Background(), id)
		hasPayment := err == nil
		switch {
		case state == domain.OrderPaymentPending && !hasPayment:
			t.Errorf("round %d: order is payment_pending but has no payment", round)
		case state == domain.OrderCancelled && hasPayment:
			t.Errorf("round %d: order is cancelled but has a payment", round)
		case state != domain.OrderPaymentPending && state != domain.OrderCancelled:
			t.Errorf("round %d: unexpected state %s", round, state)
		}
	}
}

// ---------------------------------------------------------------------------
// FinishPayment
// ---------------------------------------------------------------------------

func finish(db *DBAdapter, paymentID string, status domain.PaymentStatus, providerID, reason string, to domain.OrderState) (domain.Order, error) {
	return db.FinishPayment(context.Background(), paymentID, status, providerID, reason,
		string(domain.OrderPaymentPending), string(to))
}

// startedPayment gets an order to payment_pending with a processing payment.
func startedPayment(t *testing.T, db *DBAdapter, orderID, paymentID string) {
	t.Helper()
	paidOrder(t, db, orderID)
	if _, claimed, err := start(db, orderID, paymentID); err != nil || !claimed {
		t.Fatalf("startedPayment: claimed=%v err=%v", claimed, err)
	}
}

func TestFinishPaymentRecordsTheOutcomeAndMovesTheOrderTogether(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	startedPayment(t, db, "tx-fin-ok", "tx-fin-pay-ok")
	startedPayment(t, db, "tx-fin-no", "tx-fin-pay-no")

	paid, err := finish(db, "tx-fin-pay-ok", domain.PaymentAuthorized, "pi_abc", "", domain.OrderPaid)
	if err != nil {
		t.Fatal(err)
	}
	if paid.ID != "tx-fin-ok" || paid.State != domain.OrderPaid {
		t.Errorf("returned order = %+v, want tx-fin-ok in state paid", paid)
	}
	pay, err := db.GetPaymentByOrder(ctx, "tx-fin-ok")
	if err != nil || pay.Status != domain.PaymentAuthorized || pay.ProviderPaymentID != "pi_abc" {
		t.Errorf("payment = %+v, err = %v", pay, err)
	}

	cancelled, err := finish(db, "tx-fin-pay-no", domain.PaymentDeclined, "", "Your card was declined.", domain.OrderCancelled)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != domain.OrderCancelled {
		t.Errorf("returned order state = %s, want cancelled", cancelled.State)
	}
	pay, _ = db.GetPaymentByOrder(ctx, "tx-fin-no")
	if pay.Status != domain.PaymentDeclined || pay.Reason != "Your card was declined." {
		t.Errorf("payment = %+v", pay)
	}
}

// The order moved on (for example it was cancelled) while the provider call
// was running. The outcome must not be half-recorded: the payment stays
// processing and the order stays where the other request put it.
func TestFinishPaymentRollsBackTheOutcomeWhenTheOrderMovedOn(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	startedPayment(t, db, "tx-fin-moved", "tx-fin-pay-moved")
	setOrderState(t, db, "tx-fin-moved", domain.OrderCancelled)

	_, err := finish(db, "tx-fin-pay-moved", domain.PaymentAuthorized, "pi_x", "", domain.OrderPaid)
	wantConflict(t, err)

	pay, _ := db.GetPaymentByOrder(ctx, "tx-fin-moved")
	if pay.Status != domain.PaymentProcessing || pay.ProviderPaymentID != "" {
		t.Errorf("a refused finish left the payment as %+v, want it still processing", pay)
	}
	if got := orderState(t, db, "tx-fin-moved"); got != domain.OrderCancelled {
		t.Errorf("order state = %s, want cancelled left alone", got)
	}
}

func TestSecondFinishChangesNeitherTheOrderNorThePayment(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	startedPayment(t, db, "tx-fin-twice", "tx-fin-pay-twice")
	if _, err := finish(db, "tx-fin-pay-twice", domain.PaymentAuthorized, "pi_first", "", domain.OrderPaid); err != nil {
		t.Fatal(err)
	}

	_, err := finish(db, "tx-fin-pay-twice", domain.PaymentDeclined, "", "late", domain.OrderCancelled)
	wantPaymentErr(t, err, ports.ErrPaymentNotProcessing)

	pay, _ := db.GetPaymentByOrder(ctx, "tx-fin-twice")
	if pay.Status != domain.PaymentAuthorized || pay.ProviderPaymentID != "pi_first" || pay.Reason != "" {
		t.Errorf("payment = %+v, want the first outcome kept", pay)
	}
	if got := orderState(t, db, "tx-fin-twice"); got != domain.OrderPaid {
		t.Errorf("order state = %s, want paid", got)
	}
}

func TestFinishPaymentMissingAndNonFinalStatus(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()

	_, err := finish(db, "tx-fin-nope", domain.PaymentAuthorized, "pi_x", "", domain.OrderPaid)
	wantPaymentErr(t, err, ports.ErrPaymentNotFound)

	startedPayment(t, db, "tx-fin-badstatus", "tx-fin-pay-badstatus")
	for _, status := range []domain.PaymentStatus{domain.PaymentProcessing, domain.PaymentRefunded, "", "weird"} {
		if _, err := finish(db, "tx-fin-pay-badstatus", status, "pi_x", "", domain.OrderPaid); err == nil {
			t.Errorf("status %q was accepted as a completion", status)
		}
	}
	pay, _ := db.GetPaymentByOrder(ctx, "tx-fin-badstatus")
	if pay.Status != domain.PaymentProcessing || orderState(t, db, "tx-fin-badstatus") != domain.OrderPaymentPending {
		t.Errorf("a refused finish changed something: payment %+v", pay)
	}
}

func TestConcurrentFinishesHaveExactlyOneWinner(t *testing.T) {
	db := testAdapter(t)
	startedPayment(t, db, "tx-fin-race", "tx-fin-pay-race")

	const racers = 20
	errs := make([]error, racers)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-gate
			_, errs[i] = finish(db, "tx-fin-pay-race", domain.PaymentAuthorized, fmt.Sprintf("pi_%d", i), "", domain.OrderPaid)
		})
	}
	close(gate)
	wg.Wait()

	winner := -1
	for i, err := range errs {
		switch {
		case err == nil:
			if winner != -1 {
				t.Fatalf("two finishes won: %d and %d", winner, i)
			}
			winner = i
		case !errors.Is(err, ports.ErrPaymentNotProcessing):
			t.Errorf("racer %d: unexpected error %v", i, err)
		}
	}
	if winner == -1 {
		t.Fatal("no finish won")
	}
	pay, _ := db.GetPaymentByOrder(context.Background(), "tx-fin-race")
	if want := fmt.Sprintf("pi_%d", winner); pay.ProviderPaymentID != want {
		t.Errorf("stored provider id %q, want the winner's %q", pay.ProviderPaymentID, want)
	}
	if got := orderState(t, db, "tx-fin-race"); got != domain.OrderPaid {
		t.Errorf("order state = %s, want paid", got)
	}
}

// ---------------------------------------------------------------------------
// Failures that are neither a conflict nor a missing row
// ---------------------------------------------------------------------------

func TestTransactionalPaymentFailuresAreInternalErrorsAndChangeNothing(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "tx-cancelled-start")
	startedPayment(t, db, "tx-cancelled-fin", "tx-cancelled-fin-pay")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	plain := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: want an error, got nil", name)
			return
		}
		for _, s := range []error{ports.ErrStateConflict, ports.ErrOrderNotFound, ports.ErrPaymentNotFound, ports.ErrPaymentNotProcessing} {
			if errors.Is(err, s) {
				t.Errorf("%s: error %v must not wrap %v", name, err, s)
			}
		}
	}
	_, claimed, err := db.StartPayment(ctx, domain.Payment{ID: "x", OrderID: "tx-cancelled-start", AmountCents: 1}, time.Minute,
		string(domain.OrderCreated), string(domain.OrderPaymentPending))
	plain("StartPayment", err)
	if claimed {
		t.Error("StartPayment claimed with a cancelled context")
	}
	_, err = db.FinishPayment(ctx, "tx-cancelled-fin-pay", domain.PaymentAuthorized, "pi_x", "",
		string(domain.OrderPaymentPending), string(domain.OrderPaid))
	plain("FinishPayment", err)

	wantNoPayment(t, db, "tx-cancelled-start")
	if got := orderState(t, db, "tx-cancelled-start"); got != domain.OrderCreated {
		t.Errorf("a cancelled start moved the order to %s", got)
	}
	pay, _ := db.GetPaymentByOrder(context.Background(), "tx-cancelled-fin")
	if pay.Status != domain.PaymentProcessing || orderState(t, db, "tx-cancelled-fin") != domain.OrderPaymentPending {
		t.Errorf("a cancelled finish changed something: payment %+v", pay)
	}
}
