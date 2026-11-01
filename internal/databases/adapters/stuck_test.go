package adapters

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// Payments that never got an answer: how a reconciler finds them, the two
// statuses that settle them (abandoned, needs_review) and what each allows.

// age makes a payment look as if it was claimed d ago.
func age(t *testing.T, db *DBAdapter, paymentID string, d time.Duration) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(),
		"UPDATE payments SET created_at = now() - make_interval(secs => $2) WHERE id = $1", paymentID, d.Seconds()); err != nil {
		t.Fatalf("age: %v", err)
	}
}

// unsettledOurs lists the unsettled payments these tests made (ids start with
// pay-st-), in the order the store returned them.
func unsettledOurs(t *testing.T, db *DBAdapter, minAge time.Duration) []string {
	t.Helper()
	all, err := db.ListUnsettledPayments(context.Background(), minAge, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range all {
		if strings.HasPrefix(p.ID, "pay-st-") {
			out = append(out, p.ID)
		}
	}
	return out
}

func markForReview(t *testing.T, db *DBAdapter, id, reason string) {
	t.Helper()
	if _, ok, err := db.MarkPaymentNeedsReview(context.Background(), id, reason); err != nil || !ok {
		t.Fatalf("MarkPaymentNeedsReview(%s): ok=%v err=%v", id, ok, err)
	}
}

func TestOnlyPaymentsWithoutAnAnswerThatNobodyIsWorkingOnAreListed(t *testing.T) {
	db := testAdapter(t)
	for _, id := range []string{"st-old", "st-live", "st-young", "st-review", "st-done"} {
		paidOrder(t, db, id)
		startAttempt(t, db, id, "pay-"+id)
	}
	for _, id := range []string{"st-old", "st-young", "st-review"} {
		expire(t, db, "pay-"+id)
	}
	age(t, db, "pay-st-old", 3*time.Hour)    // old, lease out: listed
	age(t, db, "pay-st-live", 3*time.Hour)   // old, but someone holds the lease: not listed
	age(t, db, "pay-st-young", time.Minute)  // lease out, but claimed a minute ago: not yet
	age(t, db, "pay-st-review", time.Minute) // waiting for review: listed whatever its age
	markForReview(t, db, "pay-st-review", "no answer")
	if _, err := db.FinishPayment(context.Background(), "pay-st-done", domain.PaymentAuthorized, "pi_done", "",
		string(domain.OrderPaymentPending), string(domain.OrderPaid)); err != nil {
		t.Fatal(err)
	}
	expire(t, db, "pay-st-done")
	age(t, db, "pay-st-done", 3*time.Hour) // finished: never listed

	got := unsettledOurs(t, db, time.Hour)
	want := []string{"pay-st-old", "pay-st-review"} // oldest claim first
	if !slices.Equal(got, want) {
		t.Errorf("listed %v, want %v", got, want)
	}
	// with no minimum age the young one is listed too, still behind the older claim
	if got := unsettledOurs(t, db, 0); !slices.Equal(got, []string{"pay-st-old", "pay-st-young", "pay-st-review"}) {
		t.Errorf("with no minimum age listed %v", got)
	}
}

func TestTheListOfUnsettledPaymentsIsOldestFirstAndLimited(t *testing.T) {
	db := testAdapter(t)
	for i, d := range []time.Duration{1000 * 24 * time.Hour, 999 * 24 * time.Hour, 998 * 24 * time.Hour} {
		id := fmt.Sprintf("st-lim-%d", i)
		paidOrder(t, db, id)
		startAttempt(t, db, id, "pay-"+id)
		expire(t, db, "pay-"+id)
		age(t, db, "pay-"+id, d)
	}
	got, err := db.ListUnsettledPayments(context.Background(), time.Hour, 2)
	if err != nil || len(got) != 2 || got[0].ID != "pay-st-lim-0" || got[1].ID != "pay-st-lim-1" {
		t.Errorf("limit 2 = %v, %v, want the two oldest in order", got, err)
	}
}

func TestListingUnsettledPaymentsRefusesNonsenseAndFailsOnACancelledContext(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	if _, err := db.ListUnsettledPayments(ctx, time.Hour, 0); err == nil {
		t.Error("a limit of 0 was accepted")
	}
	if _, err := db.ListUnsettledPayments(ctx, -time.Second, 10); err == nil {
		t.Error("a negative minimum age was accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.ListUnsettledPayments(cancelled, time.Hour, 10); err == nil || errors.Is(err, ports.ErrPaymentNotFound) {
		t.Errorf("a cancelled context gave %v, want a plain failure", err)
	}
}

func TestMarkingAPaymentForReviewKeepsTheOrderAndSaysWhy(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "st-mark")
	startAttempt(t, db, "st-mark", "pay-st-mark")

	p, ok, err := db.MarkPaymentNeedsReview(ctx, "pay-st-mark", "no answer after 20h")
	if err != nil || !ok || p.Status != domain.PaymentNeedsReview || p.Reason != "no answer after 20h" {
		t.Fatalf("MarkPaymentNeedsReview = %+v ok=%v err=%v", p, ok, err)
	}
	if o, _ := db.GetOrderbyId(ctx, "st-mark"); o.State != domain.OrderPaymentPending {
		t.Errorf("order = %s, want it left in payment_pending: the customer may have been charged", o.State)
	}
	wantLedger(t, db, "st-mark", "claimed|amount_cents=700", "needs_review|no answer after 20h")

	// a second call changes nothing, not even the reason
	again, ok, err := db.MarkPaymentNeedsReview(ctx, "pay-st-mark", "another reason")
	if err != nil || ok || again.ID != "" {
		t.Errorf("second mark = %+v ok=%v err=%v, want ok=false", again, ok, err)
	}
	if got, _ := db.GetPayment(ctx, "pay-st-mark"); got.Reason != "no answer after 20h" {
		t.Errorf("reason = %q, the second call must not overwrite it", got.Reason)
	}
}

func TestOnlyAPaymentWithoutAnAnswerCanBeMarkedForReview(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "st-fin")
	startAttempt(t, db, "st-fin", "pay-st-fin")
	if _, err := db.FinishPayment(ctx, "pay-st-fin", domain.PaymentAuthorized, "pi_fin", "",
		string(domain.OrderPaymentPending), string(domain.OrderPaid)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.MarkPaymentNeedsReview(ctx, "pay-st-fin", "x"); err != nil || ok {
		t.Errorf("marking an authorized payment: ok=%v err=%v, want ok=false", ok, err)
	}
	if got, _ := db.GetPayment(ctx, "pay-st-fin"); got.Status != domain.PaymentAuthorized {
		t.Errorf("status = %s, the finished payment must not change", got.Status)
	}
	_, _, err := db.MarkPaymentNeedsReview(ctx, "pay-st-nope", "x")
	wantPaymentErr(t, err, ports.ErrPaymentNotFound)
}

func TestAPaymentUnderReviewBlocksANewAttempt(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "st-block")
	startAttempt(t, db, "st-block", "pay-st-block")
	markForReview(t, db, "pay-st-block", "no answer")

	got, claimed, err := db.ClaimPayment(ctx, domain.Payment{ID: "pay-st-block-2", OrderID: "st-block", AmountCents: 1}, time.Minute)
	if err != nil || claimed || got.ID != "pay-st-block" || got.Status != domain.PaymentNeedsReview {
		t.Errorf("claim over a payment under review: %+v claimed=%v err=%v, want the one under review handed back", got, claimed, err)
	}
	// it is still the order's live payment, and the database holds that line
	if _, err := db.Pool.Exec(ctx, `INSERT INTO payments (id, order_id, status, amount_cents, lease_expires_at, attempt)
		VALUES ('pay-st-block-x', 'st-block', 'processing', 1, now(), 2)`); err == nil {
		t.Error("a second live payment was inserted beside one under review")
	}
	// a retry cannot take it over either: only a processing payment can be taken
	if _, ok, err := db.TakeOverPayment(ctx, "pay-st-block", time.Minute); err != nil || ok {
		t.Errorf("takeover of a payment under review: ok=%v err=%v, want ok=false", ok, err)
	}
}

// The provider's answer can still arrive after a person was asked to look, and it
// settles the payment like any other.
func TestALateAnswerSettlesAPaymentUnderReview(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "st-late")
	startAttempt(t, db, "st-late", "pay-st-late")
	markForReview(t, db, "pay-st-late", "no answer")

	o, err := db.FinishPayment(ctx, "pay-st-late", domain.PaymentAuthorized, "pi_late", "",
		string(domain.OrderPaymentPending), string(domain.OrderPaid))
	if err != nil || o.State != domain.OrderPaid {
		t.Fatalf("FinishPayment = %+v, %v, want the order paid", o, err)
	}
	got, _ := db.GetPayment(ctx, "pay-st-late")
	if got.Status != domain.PaymentAuthorized || got.ProviderPaymentID != "pi_late" || got.Reason != "" {
		t.Errorf("payment = %+v, want authorized with the provider's id and the review reason gone", got)
	}
	wantLedger(t, db, "st-late", "claimed|amount_cents=700", "needs_review|no answer", "authorized|provider_payment_id=pi_late")
	_, err = db.FinishPayment(ctx, "pay-st-late", domain.PaymentDeclined, "", "late",
		string(domain.OrderPaymentPending), string(domain.OrderCreated))
	wantPaymentErr(t, err, ports.ErrPaymentNotProcessing)
}

func TestAnAbandonedPaymentReopensTheOrderAndAllowsANewAttempt(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "st-aband")
	startAttempt(t, db, "st-aband", "pay-st-aband-1")

	o, err := db.FinishPayment(ctx, "pay-st-aband-1", domain.PaymentAbandoned, "", "no record at the provider after 30m",
		string(domain.OrderPaymentPending), string(domain.OrderCreated))
	if err != nil || o.State != domain.OrderCreated {
		t.Fatalf("FinishPayment(abandoned) = %+v, %v, want the order open again", o, err)
	}
	if got, _ := db.GetPayment(ctx, "pay-st-aband-1"); got.Status != domain.PaymentAbandoned || got.Reason != "no record at the provider after 30m" {
		t.Errorf("payment = %+v", got)
	}

	second := startAttempt(t, db, "st-aband", "pay-st-aband-2")
	if second.Attempt != 2 {
		t.Errorf("second attempt = %d, want 2", second.Attempt)
	}
	wantLedger(t, db, "st-aband",
		"claimed|amount_cents=700", "abandoned|no record at the provider after 30m", "claimed|amount_cents=700")
	// an abandoned row beside the live one is fine, a second live one is not
	if _, err := db.Pool.Exec(ctx, `INSERT INTO payments (id, order_id, status, amount_cents, lease_expires_at, attempt)
		VALUES ('pay-st-aband-x', 'st-aband', 'abandoned', 1, now(), 3)`); err != nil {
		t.Errorf("an abandoned payment beside a live one was refused: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO payments (id, order_id, status, amount_cents, lease_expires_at, attempt)
		VALUES ('pay-st-aband-y', 'st-aband', 'processing', 1, now(), 4)`); err == nil {
		t.Error("a second live payment was inserted")
	}
}

func TestAnAbandonedPaymentIsFinalAndNeedsReviewIsNotAnAnswer(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "st-final")
	startAttempt(t, db, "st-final", "pay-st-final")

	if _, err := db.CompletePayment(ctx, "pay-st-final", domain.PaymentNeedsReview, "", "x"); err == nil || !strings.Contains(err.Error(), "not a final status") {
		t.Errorf("completing with needs_review: %v, want it refused as not final", err)
	}
	if _, err := db.CompletePayment(ctx, "pay-st-final", domain.PaymentProcessing, "", ""); err == nil {
		t.Error("completing with processing was accepted")
	}
	if _, err := db.CompletePayment(ctx, "pay-st-final", domain.PaymentAbandoned, "", "gone"); err != nil {
		t.Fatal(err)
	}
	_, err := db.CompletePayment(ctx, "pay-st-final", domain.PaymentAuthorized, "pi_x", "")
	wantPaymentErr(t, err, ports.ErrPaymentNotProcessing)
}

func TestTheDatabaseRefusesAnUnknownPaymentStatus(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "st-bogus")
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO payments (id, order_id, status, amount_cents, lease_expires_at)
		VALUES ('pay-st-bogus', 'st-bogus', 'bogus', 1, now())`); err == nil {
		t.Error("a payment with an unknown status was inserted")
	}
}

// Whoever answers first wins: a provider's success, a decline and an abandonment
// arriving together settle a payment under review exactly once.
func TestOnlyOneOfSeveralAnswersSettlesAPaymentUnderReview(t *testing.T) {
	db := testAdapter(t)
	paidOrder(t, db, "st-race")
	startAttempt(t, db, "st-race", "pay-st-race")
	markForReview(t, db, "pay-st-race", "no answer")

	type answer struct {
		status domain.PaymentStatus
		to     domain.OrderState
	}
	answers := []answer{
		{domain.PaymentAuthorized, domain.OrderPaid}, {domain.PaymentDeclined, domain.OrderCreated}, {domain.PaymentAbandoned, domain.OrderCreated},
	}
	const n = 12
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			a := answers[i%len(answers)]
			_, errs[i] = db.FinishPayment(context.Background(), "pay-st-race", a.status, "pi_race", "r",
				string(domain.OrderPaymentPending), string(a.to))
		})
	}
	close(start)
	wg.Wait()

	wins := 0
	for i, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, ports.ErrPaymentNotProcessing):
			t.Errorf("answer %d: unexpected error %v", i, err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d answers were recorded, want exactly 1", wins)
	}
	p, _ := db.GetPayment(context.Background(), "pay-st-race")
	o, _ := db.GetOrderbyId(context.Background(), "st-race")
	wantState := domain.OrderCreated
	if p.Status == domain.PaymentAuthorized {
		wantState = domain.OrderPaid
	}
	if o.State != wantState {
		t.Errorf("payment %s but order %s: the two must change together", p.Status, o.State)
	}
}
