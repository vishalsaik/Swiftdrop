package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// These need the same Postgres as database_test.go (SWIFTDROP_TEST_DSN) with
// the payments table from schema.sql applied.

// paidOrder inserts an order and removes it, and any payment on it, when the
// test ends. The payment goes first: its foreign key refuses to let the order
// go while it still references it, deliberately.
func paidOrder(t *testing.T, db *DBAdapter, orderID string) {
	t.Helper()
	newRow(t, db, orderID, domain.OrderCreated) // its cleanup runs last
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), "DELETE FROM payments WHERE order_id = $1", orderID)
	})
}

func claim(t *testing.T, db *DBAdapter, orderID, paymentID string, lease time.Duration) domain.Payment {
	t.Helper()
	p, claimed, err := db.ClaimPayment(context.Background(),
		domain.Payment{ID: paymentID, OrderID: orderID, AmountCents: 1447}, lease)
	if err != nil {
		t.Fatalf("ClaimPayment(%s): %v", paymentID, err)
	}
	if !claimed {
		t.Fatalf("ClaimPayment(%s) did not claim", paymentID)
	}
	return p
}

// expire makes the lease of a payment run out now, without waiting.
func expire(t *testing.T, db *DBAdapter, paymentID string) {
	t.Helper()
	_, err := db.Pool.Exec(context.Background(),
		"UPDATE payments SET lease_expires_at = now() - interval '1 second' WHERE id = $1", paymentID)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
}

func wantPaymentErr(t *testing.T, err, sentinel error) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want one wrapping %v", err, sentinel)
	}
	for _, leaked := range []string{"no rows", "violates", "SQLSTATE"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("driver text %q leaked through the sentinel: %v", leaked, err)
		}
	}
}

// ---------------------------------------------------------------------------
// ClaimPayment
// ---------------------------------------------------------------------------

func TestClaimPaymentCreatesAProcessingRecord(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "pay-claim-1")

	p := claim(t, db, "pay-claim-1", "pay-id-1", time.Minute)
	if p.ID != "pay-id-1" || p.OrderID != "pay-claim-1" || p.AmountCents != 1447 {
		t.Errorf("claimed payment = %+v", p)
	}
	if p.Status != domain.PaymentProcessing {
		t.Errorf("status = %s, want processing", p.Status)
	}
	if p.ProviderPaymentID != "" || p.Reason != "" {
		t.Errorf("a new claim has no provider id or reason, got %q / %q", p.ProviderPaymentID, p.Reason)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		t.Errorf("timestamps must be set by the database, got %v / %v", p.CreatedAt, p.UpdatedAt)
	}
}

// The lease deadline is computed by Postgres, not by this process.
func TestClaimLeaseUsesTheDatabaseClock(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "pay-clock")

	p := claim(t, db, "pay-clock", "pay-id-clock", time.Hour)
	var dbNow time.Time
	if err := db.Pool.QueryRow(context.Background(), "SELECT now()").Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	got := p.LeaseExpiresAt.Sub(dbNow)
	if got < 59*time.Minute || got > 61*time.Minute {
		t.Errorf("lease runs %v past the database's now, want about 1h", got)
	}
}

func TestClaimPaymentTwiceReturnsTheFirstOne(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-claim-twice")
	first := claim(t, db, "pay-claim-twice", "pay-id-first", time.Minute)

	second, claimed, err := db.ClaimPayment(ctx,
		domain.Payment{ID: "pay-id-second", OrderID: "pay-claim-twice", AmountCents: 99999}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Error("a second claim on the same order must not claim")
	}
	if second.ID != first.ID || second.AmountCents != first.AmountCents {
		t.Errorf("got %+v, want the first payment back unchanged", second)
	}

	var rows int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM payments WHERE order_id = $1", "pay-claim-twice").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("the order has %d payment rows, want exactly 1", rows)
	}
}

func TestClaimPaymentForAMissingOrderIsOrderNotFound(t *testing.T) {
	db := testAdapter(t)
	_, claimed, err := db.ClaimPayment(context.Background(),
		domain.Payment{ID: "pay-orphan", OrderID: "it-does-not-exist", AmountCents: 1}, time.Minute)
	wantPaymentErr(t, err, ports.ErrOrderNotFound)
	if claimed {
		t.Error("claimed a payment for an order that does not exist")
	}
}

func TestClaimPaymentRejectsBadInput(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "pay-bad")
	tests := []struct {
		name  string
		p     domain.Payment
		lease time.Duration
	}{
		{"no payment id", domain.Payment{OrderID: "pay-bad", AmountCents: 1}, time.Minute},
		{"no order id", domain.Payment{ID: "x", AmountCents: 1}, time.Minute},
		{"zero lease", domain.Payment{ID: "x", OrderID: "pay-bad", AmountCents: 1}, 0},
		{"negative lease", domain.Payment{ID: "x", OrderID: "pay-bad", AmountCents: 1}, -time.Second},
		{"negative amount", domain.Payment{ID: "x", OrderID: "pay-bad", AmountCents: -5}, time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, claimed, err := db.ClaimPayment(context.Background(), tt.p, tt.lease)
			if err == nil || claimed {
				t.Errorf("claimed=%v err=%v, want a refusal", claimed, err)
			}
		})
	}
	if _, err := db.GetPaymentByOrder(context.Background(), "pay-bad"); !errors.Is(err, ports.ErrPaymentNotFound) {
		t.Errorf("a refused claim left a payment behind: %v", err)
	}
}

// Of many requests claiming the same order at once, the database picks one.
func TestConcurrentClaimsHaveExactlyOneOwner(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-claim-race")

	const racers = 20
	type result struct {
		p       domain.Payment
		claimed bool
		err     error
	}
	results := make([]result, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			p, claimed, err := db.ClaimPayment(ctx,
				domain.Payment{ID: fmt.Sprintf("pay-race-%d", i), OrderID: "pay-claim-race", AmountCents: 500}, time.Minute)
			results[i] = result{p, claimed, err}
		})
	}
	close(start)
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
}

// ---------------------------------------------------------------------------
// GetPaymentByOrder
// ---------------------------------------------------------------------------

func TestGetPaymentByOrder(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-get")

	_, err := db.GetPaymentByOrder(ctx, "pay-get")
	wantPaymentErr(t, err, ports.ErrPaymentNotFound)

	claimed := claim(t, db, "pay-get", "pay-id-get", time.Minute)
	got, err := db.GetPaymentByOrder(ctx, "pay-get")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != claimed.ID || got.Status != domain.PaymentProcessing || !got.LeaseExpiresAt.Equal(claimed.LeaseExpiresAt) {
		t.Errorf("got %+v, want the claimed payment %+v", got, claimed)
	}
}

// ---------------------------------------------------------------------------
// CompletePayment
// ---------------------------------------------------------------------------

func TestCompletePaymentRecordsTheOutcome(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-done-ok")
	paidOrder(t, db, "pay-done-no")
	claim(t, db, "pay-done-ok", "pay-id-ok", time.Minute)
	claim(t, db, "pay-done-no", "pay-id-no", time.Minute)

	ok, err := db.CompletePayment(ctx, "pay-id-ok", domain.PaymentAuthorized, "pi_abc", "")
	if err != nil {
		t.Fatal(err)
	}
	if ok.Status != domain.PaymentAuthorized || ok.ProviderPaymentID != "pi_abc" || ok.Reason != "" {
		t.Errorf("authorized payment = %+v", ok)
	}
	if ok.UpdatedAt.Before(ok.CreatedAt) {
		t.Errorf("updatedAt %v is before createdAt %v", ok.UpdatedAt, ok.CreatedAt)
	}

	no, err := db.CompletePayment(ctx, "pay-id-no", domain.PaymentDeclined, "", "Your card was declined.")
	if err != nil {
		t.Fatal(err)
	}
	if no.Status != domain.PaymentDeclined || no.Reason != "Your card was declined." {
		t.Errorf("declined payment = %+v", no)
	}

	stored, err := db.GetPaymentByOrder(ctx, "pay-done-ok")
	if err != nil || stored.Status != domain.PaymentAuthorized || stored.ProviderPaymentID != "pi_abc" {
		t.Errorf("stored = %+v, err = %v", stored, err)
	}
}

func TestSecondCompletionCannotOverwriteTheFirst(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-done-twice")
	claim(t, db, "pay-done-twice", "pay-id-twice", time.Minute)

	if _, err := db.CompletePayment(ctx, "pay-id-twice", domain.PaymentAuthorized, "pi_first", ""); err != nil {
		t.Fatal(err)
	}
	_, err := db.CompletePayment(ctx, "pay-id-twice", domain.PaymentDeclined, "", "late writer")
	wantPaymentErr(t, err, ports.ErrPaymentNotProcessing)

	stored, _ := db.GetPaymentByOrder(ctx, "pay-done-twice")
	if stored.Status != domain.PaymentAuthorized || stored.ProviderPaymentID != "pi_first" || stored.Reason != "" {
		t.Errorf("a refused completion changed the payment: %+v", stored)
	}
}

func TestCompleteMissingPayment(t *testing.T) {
	db := testAdapter(t)
	_, err := db.CompletePayment(context.Background(), "pay-id-nope", domain.PaymentAuthorized, "pi_x", "")
	wantPaymentErr(t, err, ports.ErrPaymentNotFound)
}

func TestCompletePaymentRejectsANonFinalStatus(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-done-bad")
	claim(t, db, "pay-done-bad", "pay-id-bad", time.Minute)

	for _, status := range []domain.PaymentStatus{domain.PaymentProcessing, domain.PaymentRefunded, "", "weird"} {
		if _, err := db.CompletePayment(ctx, "pay-id-bad", status, "pi_x", ""); err == nil {
			t.Errorf("status %q was accepted as a completion", status)
		}
	}
	stored, _ := db.GetPaymentByOrder(ctx, "pay-done-bad")
	if stored.Status != domain.PaymentProcessing {
		t.Errorf("a refused completion changed the status to %s", stored.Status)
	}
}

func TestConcurrentCompletionsHaveExactlyOneWinner(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-done-race")
	claim(t, db, "pay-done-race", "pay-id-race", time.Minute)

	const racers = 20
	errs := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			_, errs[i] = db.CompletePayment(ctx, "pay-id-race", domain.PaymentAuthorized, fmt.Sprintf("pi_%d", i), "")
		})
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, err := range errs {
		switch {
		case err == nil:
			if winner != -1 {
				t.Fatalf("two completions won: %d and %d", winner, i)
			}
			winner = i
		case !errors.Is(err, ports.ErrPaymentNotProcessing):
			t.Errorf("racer %d: unexpected error %v", i, err)
		}
	}
	if winner == -1 {
		t.Fatal("no completion won")
	}
	stored, _ := db.GetPaymentByOrder(ctx, "pay-done-race")
	if want := fmt.Sprintf("pi_%d", winner); stored.ProviderPaymentID != want {
		t.Errorf("stored provider id %q, want the winner's %q", stored.ProviderPaymentID, want)
	}
}

// ---------------------------------------------------------------------------
// TakeOverPayment
// ---------------------------------------------------------------------------

func TestTakeOverRefusesWhileTheLeaseIsAlive(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-take-live")
	before := claim(t, db, "pay-take-live", "pay-id-live", time.Minute)

	_, ok, err := db.TakeOverPayment(ctx, "pay-id-live", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("took over a payment whose lease is still alive")
	}
	after, _ := db.GetPaymentByOrder(ctx, "pay-take-live")
	if !after.LeaseExpiresAt.Equal(before.LeaseExpiresAt) {
		t.Errorf("a refused takeover moved the lease: %v -> %v", before.LeaseExpiresAt, after.LeaseExpiresAt)
	}
}

func TestTakeOverAnExpiredLeaseExtendsIt(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-take-expired")
	before := claim(t, db, "pay-take-expired", "pay-id-expired", time.Minute)
	expire(t, db, "pay-id-expired")

	got, ok, err := db.TakeOverPayment(ctx, "pay-id-expired", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("did not take over an expired lease")
	}
	if got.ID != before.ID || got.Status != domain.PaymentProcessing {
		t.Errorf("a takeover must keep the same payment id and status processing, got %+v", got)
	}
	if !got.LeaseExpiresAt.After(time.Now().Add(time.Minute)) {
		t.Errorf("lease %v was not extended into the future", got.LeaseExpiresAt)
	}

	// the new lease is live, so a second taker is turned away
	if _, ok, _ := db.TakeOverPayment(ctx, "pay-id-expired", time.Minute); ok {
		t.Error("a second takeover succeeded against the fresh lease")
	}
}

// Not a SQL shortcut: let a very short lease really run out.
func TestTakeOverAfterTheLeaseReallyExpires(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "pay-take-real")
	claim(t, db, "pay-take-real", "pay-id-real", 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	_, ok, err := db.TakeOverPayment(context.Background(), "pay-id-real", time.Minute)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want a takeover after the lease ran out", ok, err)
	}
}

func TestTakeOverNeverTouchesAFinishedPayment(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-take-done")
	claim(t, db, "pay-take-done", "pay-id-done", time.Minute)
	if _, err := db.CompletePayment(ctx, "pay-id-done", domain.PaymentAuthorized, "pi_done", ""); err != nil {
		t.Fatal(err)
	}
	expire(t, db, "pay-id-done") // even with an expired lease

	_, ok, err := db.TakeOverPayment(ctx, "pay-id-done", time.Minute)
	if err != nil || ok {
		t.Errorf("ok=%v err=%v, want a refusal for a finished payment", ok, err)
	}
	stored, _ := db.GetPaymentByOrder(ctx, "pay-take-done")
	if stored.Status != domain.PaymentAuthorized {
		t.Errorf("status = %s, want authorized left alone", stored.Status)
	}
}

func TestTakeOverMissingPayment(t *testing.T) {
	db := testAdapter(t)
	_, _, err := db.TakeOverPayment(context.Background(), "pay-id-nope", time.Minute)
	wantPaymentErr(t, err, ports.ErrPaymentNotFound)
}

// A takeover with no lease would hand over the payment and leave it
// immediately expired again, so it is refused up front. The payment here is
// real and expired, so the lease check is the only thing that can refuse it.
func TestTakeOverWithANonPositiveLeaseIsRefused(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-take-badlease")
	claim(t, db, "pay-take-badlease", "pay-id-badlease", time.Minute)
	expire(t, db, "pay-id-badlease")

	for _, lease := range []time.Duration{0, -time.Second} {
		if _, ok, err := db.TakeOverPayment(ctx, "pay-id-badlease", lease); err == nil || ok {
			t.Errorf("lease %v: ok=%v err=%v, want a refusal", lease, ok, err)
		}
	}
	// still takeable with a proper lease: the refused calls changed nothing
	if _, ok, err := db.TakeOverPayment(ctx, "pay-id-badlease", time.Minute); err != nil || !ok {
		t.Errorf("ok=%v err=%v, want the untouched expired payment to be takeable", ok, err)
	}
}

func TestConcurrentTakeOversHaveExactlyOneTaker(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-take-race")
	claim(t, db, "pay-take-race", "pay-id-takerace", time.Minute)
	expire(t, db, "pay-id-takerace")

	const racers = 20
	oks := make([]bool, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			_, oks[i], errs[i] = db.TakeOverPayment(ctx, "pay-id-takerace", time.Minute)
		})
	}
	close(start)
	wg.Wait()

	takers := 0
	for i := range racers {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if oks[i] {
			takers++
		}
	}
	if takers != 1 {
		t.Errorf("%d requests took the payment over, want exactly 1", takers)
	}
}

// A failure that is not "no rows" (here, the caller gave up) is an internal
// error: never a missing payment or a lost race, or a caller would act on a
// false statement about the payment.
func TestPaymentStoreFailuresAreInternalErrors(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "pay-cancelled")
	claim(t, db, "pay-cancelled", "pay-id-cancelled", time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	plain := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: want an error, got nil", name)
			return
		}
		for _, s := range []error{ports.ErrPaymentNotFound, ports.ErrPaymentNotProcessing, ports.ErrOrderNotFound} {
			if errors.Is(err, s) {
				t.Errorf("%s: error %v must not wrap %v", name, err, s)
			}
		}
	}
	_, claimed, err := db.ClaimPayment(ctx, domain.Payment{ID: "x", OrderID: "pay-cancelled"}, time.Minute)
	plain("ClaimPayment", err)
	if claimed {
		t.Error("ClaimPayment claimed with a cancelled context")
	}
	_, err = db.GetPaymentByOrder(ctx, "pay-cancelled")
	plain("GetPaymentByOrder", err)
	_, err = db.CompletePayment(ctx, "pay-id-cancelled", domain.PaymentAuthorized, "pi_x", "")
	plain("CompletePayment", err)
	_, ok, err := db.TakeOverPayment(ctx, "pay-id-cancelled", time.Minute)
	plain("TakeOverPayment", err)
	if ok {
		t.Error("TakeOverPayment took over with a cancelled context")
	}

	stored, _ := db.GetPaymentByOrder(context.Background(), "pay-cancelled")
	if stored.Status != domain.PaymentProcessing {
		t.Errorf("a cancelled call changed the payment to %s", stored.Status)
	}
}

// ---------------------------------------------------------------------------
// The schema itself: constraints the database enforces whatever the code does.
// ---------------------------------------------------------------------------

func TestPaymentsSchemaEnforcesItsRules(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "pay-schema")

	insert := func(id, orderID, status string, amount int64) error {
		_, err := db.Pool.Exec(ctx, `
			INSERT INTO payments (id, order_id, status, amount_cents, lease_expires_at)
			VALUES ($1, $2, $3, $4, now() + interval '1 minute')`, id, orderID, status, amount)
		return err
	}

	if err := insert("schema-bad-status", "pay-schema", "weird", 1); err == nil {
		t.Error("a payment with status 'weird' was accepted")
	}
	if err := insert("schema-neg", "pay-schema", "processing", -1); err == nil {
		t.Error("a negative amount was accepted")
	}
	if err := insert("schema-orphan", "it-does-not-exist", "processing", 1); err == nil {
		t.Error("a payment for a missing order was accepted")
	}
	if err := insert("schema-ok", "pay-schema", "processing", 1); err != nil {
		t.Fatalf("a valid payment was refused: %v", err)
	}
	if err := insert("schema-second", "pay-schema", "processing", 1); err == nil {
		t.Error("a second payment for the same order was accepted")
	}

	// a payment is a financial record: its order cannot be deleted from under it
	if _, err := db.Pool.Exec(ctx, "DELETE FROM orders WHERE id = $1", "pay-schema"); err == nil {
		t.Error("an order with a payment was deleted")
	}
}
