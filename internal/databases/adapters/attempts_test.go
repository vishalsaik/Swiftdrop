package adapters

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// An order can be paid again after a decline: it then has several payments, but
// only one that is not declined at a time (a partial unique index).

// declined records a claimed payment as declined, leaving the order created again
// the way a decline does.
func decline(t *testing.T, db *DBAdapter, orderID, paymentID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.FinishPayment(ctx, paymentID, domain.PaymentDeclined, "pi_"+paymentID, "no",
		string(domain.OrderPaymentPending), string(domain.OrderCreated)); err != nil {
		t.Fatalf("decline %s: %v", paymentID, err)
	}
}

// startAttempt claims a payment for the order the way the handler does.
func startAttempt(t *testing.T, db *DBAdapter, orderID, paymentID string) domain.Payment {
	t.Helper()
	p, claimed, err := db.StartPayment(context.Background(),
		domain.Payment{ID: paymentID, OrderID: orderID, AmountCents: 700}, time.Minute,
		string(domain.OrderCreated), string(domain.OrderPaymentPending))
	if err != nil || !claimed {
		t.Fatalf("StartPayment(%s): claimed=%v err=%v", paymentID, claimed, err)
	}
	return p
}

func TestADeclinedPaymentDoesNotBlockANewOne(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "at-retry")

	first := startAttempt(t, db, "at-retry", "pay-at-retry-1")
	if first.Attempt != 1 {
		t.Errorf("first attempt = %d, want 1", first.Attempt)
	}
	decline(t, db, "at-retry", "pay-at-retry-1")
	if o, _ := db.GetOrderbyId(context.Background(), "at-retry"); o.State != domain.OrderCreated {
		t.Fatalf("order state = %s, want created after a decline", o.State)
	}

	second := startAttempt(t, db, "at-retry", "pay-at-retry-2")
	if second.Attempt != 2 || second.ID != "pay-at-retry-2" {
		t.Errorf("second payment = %+v, want attempt 2", second)
	}
	decline(t, db, "at-retry", "pay-at-retry-2")
	third := startAttempt(t, db, "at-retry", "pay-at-retry-3")
	if third.Attempt != 3 {
		t.Errorf("third attempt = %d, want 3", third.Attempt)
	}
}

func TestOnlyOnePaymentThatIsNotDeclinedCanExistForAnOrder(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "at-one")
	startAttempt(t, db, "at-one", "pay-at-one-1")

	// a second claim while the first is live is handed the first, never inserted
	got, claimed, err := db.ClaimPayment(ctx, domain.Payment{ID: "pay-at-one-2", OrderID: "at-one", AmountCents: 1}, time.Minute)
	if err != nil || claimed || got.ID != "pay-at-one-1" {
		t.Errorf("claim over a live payment: got %+v claimed=%v err=%v, want the live one handed back", got, claimed, err)
	}
	// and the database itself refuses a second live row
	_, err = db.Pool.Exec(ctx, `INSERT INTO payments (id, order_id, status, amount_cents, lease_expires_at, attempt)
		VALUES ('pay-at-one-x', 'at-one', 'authorized', 1, now(), 2)`)
	if err == nil {
		t.Error("a second payment that is not declined was inserted")
	}
	// a declined row beside the live one is fine
	if _, err := db.Pool.Exec(ctx, `INSERT INTO payments (id, order_id, status, amount_cents, lease_expires_at, attempt)
		VALUES ('pay-at-one-d', 'at-one', 'declined', 1, now(), 2)`); err != nil {
		t.Errorf("a declined payment beside a live one was refused: %v", err)
	}
}

func TestThePaymentOfAnOrderIsItsLatestAttempt(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "at-latest")
	startAttempt(t, db, "at-latest", "pay-at-latest-1")
	decline(t, db, "at-latest", "pay-at-latest-1")
	startAttempt(t, db, "at-latest", "pay-at-latest-2")

	got, err := db.GetPaymentByOrder(ctx, "at-latest")
	if err != nil || got.ID != "pay-at-latest-2" || got.Attempt != 2 {
		t.Errorf("GetPaymentByOrder = %+v, %v, want attempt 2", got, err)
	}
	// both can still be read by their own id
	first, err := db.GetPayment(ctx, "pay-at-latest-1")
	if err != nil || first.Status != domain.PaymentDeclined || first.Attempt != 1 {
		t.Errorf("GetPayment(first) = %+v, %v, want the declined attempt 1", first, err)
	}
}

// Twenty requests pay the same open order at once, after a decline: exactly one
// starts the next attempt.
func TestConcurrentNewAttemptsHaveExactlyOneWinner(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "at-race")
	startAttempt(t, db, "at-race", "pay-at-race-0")
	decline(t, db, "at-race", "pay-at-race-0")

	const n = 20
	claimed := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			_, claimed[i], errs[i] = db.StartPayment(context.Background(),
				domain.Payment{ID: fmt.Sprintf("pay-at-race-%d", i+1), OrderID: "at-race", AmountCents: 700}, time.Minute,
				string(domain.OrderCreated), string(domain.OrderPaymentPending))
		})
	}
	close(start)
	wg.Wait()

	winners := 0
	for i := range n {
		if claimed[i] {
			winners++
		} else if errs[i] != nil && !errors.Is(errs[i], ports.ErrStateConflict) {
			t.Errorf("request %d: unexpected error %v", i, errs[i])
		}
	}
	if winners != 1 {
		t.Errorf("%d requests started the new attempt, want exactly 1", winners)
	}
	var rows int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM payments WHERE order_id = 'at-race'").Scan(&rows); err != nil || rows != 2 {
		t.Errorf("%d payments for the order (err %v), want the declined one and the winner", rows, err)
	}
}

func TestEveryAttemptsHistoryIsInTheOrdersLedger(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "at-ledger")
	startAttempt(t, db, "at-ledger", "pay-at-ledger-1")
	decline(t, db, "at-ledger", "pay-at-ledger-1")
	startAttempt(t, db, "at-ledger", "pay-at-ledger-2")
	if _, err := db.FinishPayment(context.Background(), "pay-at-ledger-2", domain.PaymentAuthorized, "pi_ok", "",
		string(domain.OrderPaymentPending), string(domain.OrderPaid)); err != nil {
		t.Fatal(err)
	}
	wantLedger(t, db, "at-ledger",
		"claimed|amount_cents=700", "declined|no",
		"claimed|amount_cents=700", "authorized|provider_payment_id=pi_ok")
}

func TestAnOrderWithSeveralAttemptsIsListedOnceWithItsLatest(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "ls-attempts")
	startAttempt(t, db, "ls-attempts", "pay-ls-attempts-1")
	decline(t, db, "ls-attempts", "pay-ls-attempts-1")
	startAttempt(t, db, "ls-attempts", "pay-ls-attempts-2")

	var found []domain.OrderWithPayment
	for _, ow := range listed(t, db) {
		if ow.Order.ID == "ls-attempts" {
			found = append(found, ow)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the order was listed %d times, want once", len(found))
	}
	if p := found[0].Payment; p == nil || p.ID != "pay-ls-attempts-2" || p.Attempt != 2 {
		t.Errorf("listed payment = %+v, want attempt 2", p)
	}
}

// A declined attempt moved no money, so it never makes a refund due; the authorized
// one that follows does once its order is cancelled.
func TestOnlyTheAuthorizedAttemptCanBeRefunded(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "at-refund")
	startAttempt(t, db, "at-refund", "pay-at-refund-1")
	decline(t, db, "at-refund", "pay-at-refund-1")
	startAttempt(t, db, "at-refund", "pay-at-refund-2")
	if _, err := db.FinishPayment(ctx, "pay-at-refund-2", domain.PaymentAuthorized, "pi_ok", "",
		string(domain.OrderPaymentPending), string(domain.OrderPaid)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE orders SET state = 'cancelled' WHERE id = 'at-refund'"); err != nil {
		t.Fatal(err)
	}

	ours := []string{}
	for _, id := range dueIDs(t, db) {
		if id == "pay-at-refund-1" || id == "pay-at-refund-2" {
			ours = append(ours, id)
		}
	}
	if !slices.Equal(ours, []string{"pay-at-refund-2"}) {
		t.Errorf("due refunds = %v, want only the authorized attempt", ours)
	}
	if _, ok, _ := db.ClaimRefund(ctx, "pay-at-refund-1", time.Minute); ok {
		t.Error("the declined attempt was claimed for a refund")
	}
}

func TestADeclineReopensTheOrderInTheSameTransaction(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "at-tx")
	startAttempt(t, db, "at-tx", "pay-at-tx-1")
	// the order is cancelled meanwhile: the decline cannot reopen it, and is rolled back
	if _, err := db.Pool.Exec(ctx, "UPDATE orders SET state = 'cancelled' WHERE id = 'at-tx'"); err != nil {
		t.Fatal(err)
	}
	_, err := db.FinishPayment(ctx, "pay-at-tx-1", domain.PaymentDeclined, "pi_x", "no",
		string(domain.OrderPaymentPending), string(domain.OrderCreated))
	if !errors.Is(err, ports.ErrStateConflict) {
		t.Fatalf("error = %v, want a state conflict", err)
	}
	if p, _ := db.GetPayment(ctx, "pay-at-tx-1"); p.Status != domain.PaymentProcessing {
		t.Errorf("payment status = %s, want it left processing", p.Status)
	}
	if o, _ := db.GetOrderbyId(ctx, "at-tx"); o.State != domain.OrderCancelled {
		t.Errorf("order state = %s, want the cancel left alone", o.State)
	}
}

func TestTheAttemptNumberIsConstrained(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "at-check")
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO payments (id, order_id, status, amount_cents, lease_expires_at, attempt)
		VALUES ('pay-at-check', 'at-check', 'declined', 1, now(), 0)`); err == nil {
		t.Error("an attempt number of 0 was accepted")
	}
}
