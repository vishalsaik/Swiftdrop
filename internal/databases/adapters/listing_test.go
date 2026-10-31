package adapters

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/domain"
)

// ListOrders needs the same Postgres as the other adapter tests. Other tests
// leave their own rows around while they run, so these look only at rows whose id
// starts with "ls-".

func listed(t *testing.T, db *DBAdapter) []domain.OrderWithPayment {
	t.Helper()
	all, err := db.ListOrders(context.Background(), 10000)
	if err != nil {
		t.Fatal(err)
	}
	var ours []domain.OrderWithPayment
	for _, ow := range all {
		if strings.HasPrefix(ow.Order.ID, "ls-") {
			ours = append(ours, ow)
		}
	}
	return ours
}

func ids(list []domain.OrderWithPayment) []string {
	var out []string
	for _, ow := range list {
		out = append(out, ow.Order.ID)
	}
	return out
}

func setCreated(t *testing.T, db *DBAdapter, id, ago string) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), "UPDATE orders SET created_at = now() - $2::interval WHERE id = $1", id, ago); err != nil {
		t.Fatal(err)
	}
}

func TestListOrdersIsNewestFirstWithEachOrdersPayment(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	newRow(t, db, "ls-old", domain.OrderCreated) // no payment
	paidOrder(t, db, "ls-mid")                   // authorized, then refunded
	paidOrder(t, db, "ls-new")                   // declined
	setCreated(t, db, "ls-old", "3 hours")
	setCreated(t, db, "ls-mid", "2 hours")
	setCreated(t, db, "ls-new", "1 hour")

	claim(t, db, "ls-mid", "pay-ls-mid", time.Minute)
	if _, err := db.CompletePayment(ctx, "pay-ls-mid", domain.PaymentAuthorized, "pi_mid", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE orders SET state = 'cancelled' WHERE id = 'ls-mid'"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.ClaimRefund(ctx, "pay-ls-mid", time.Hour); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := db.CompleteRefund(ctx, "pay-ls-mid", "re_mid"); err != nil {
		t.Fatal(err)
	}
	claim(t, db, "ls-new", "pay-ls-new", time.Minute)
	if _, err := db.CompletePayment(ctx, "pay-ls-new", domain.PaymentDeclined, "pi_new", "Your card was declined."); err != nil {
		t.Fatal(err)
	}

	got := listed(t, db)
	if want := []string{"ls-new", "ls-mid", "ls-old"}; !slices.Equal(ids(got), want) {
		t.Fatalf("order = %v, want newest first %v", ids(got), want)
	}

	declined, refunded, none := got[0], got[1], got[2]
	if none.Payment != nil {
		t.Errorf("an order with no payment has %+v, want nil", none.Payment)
	}
	if p := declined.Payment; p == nil || p.ID != "pay-ls-new" || p.Status != domain.PaymentDeclined || p.Reason != "Your card was declined." || p.AmountCents != 1447 {
		t.Errorf("declined payment = %+v", p)
	}
	p := refunded.Payment
	if p == nil || p.Status != domain.PaymentAuthorized || p.ProviderPaymentID != "pi_mid" ||
		p.RefundStatus != domain.RefundDone || p.RefundID != "re_mid" || p.RefundAttempts != 1 || p.RefundLeaseExpiresAt != nil {
		t.Errorf("refunded payment = %+v", p)
	}
	if p != nil && (p.OrderID != "ls-mid" || p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() || p.LeaseExpiresAt.IsZero()) {
		t.Errorf("payment times or order id missing: %+v", p)
	}
}

// What a list returns for an order is what a single read returns.
func TestListOrdersMatchesAnIndividualRead(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	paidOrder(t, db, "ls-same")
	claim(t, db, "ls-same", "pay-ls-same", time.Minute)
	if _, err := db.SetFoodReady(ctx, "ls-same", string(domain.OrderCreated)); err != nil {
		t.Fatal(err)
	}

	var row domain.OrderWithPayment
	for _, ow := range listed(t, db) {
		if ow.Order.ID == "ls-same" {
			row = ow
		}
	}
	single, err := db.GetOrderbyId(ctx, "ls-same")
	if err != nil {
		t.Fatal(err)
	}
	if row.Order.ID == "" {
		t.Fatal("the order was not listed")
	}
	if row.Order.State != single.State || row.Order.PaymentType != single.PaymentType || row.Order.CustomerID != single.CustomerID ||
		len(row.Order.Items) != len(single.Items) || row.Order.Items[0] != single.Items[0] ||
		row.Order.FoodReady == nil || !row.Order.FoodReady.Equal(*single.FoodReady) ||
		!row.Order.CreatedAt.Equal(single.CreatedAt) || !row.Order.UpdatedAt.Equal(single.UpdatedAt) {
		t.Errorf("list row %+v differs from the single read %+v", row.Order, single)
	}
	payment, err := db.GetPaymentByOrder(ctx, "ls-same")
	if err != nil {
		t.Fatal(err)
	}
	if row.Payment == nil || row.Payment.ID != payment.ID || row.Payment.Status != payment.Status || !row.Payment.LeaseExpiresAt.Equal(payment.LeaseExpiresAt) {
		t.Errorf("list payment %+v differs from the single read %+v", row.Payment, payment)
	}
}

func TestListOrdersBreaksTiesByIdSoThePageIsStable(t *testing.T) {
	db := testAdapter(t)
	for _, id := range []string{"ls-tie-b", "ls-tie-a", "ls-tie-c"} {
		newRow(t, db, id, domain.OrderCreated)
		setCreated(t, db, id, "5 hours")
	}
	// the three share a moment only if their created_at is equal: force it
	if _, err := db.Pool.Exec(context.Background(), "UPDATE orders SET created_at = '2026-01-01T00:00:00Z' WHERE id LIKE 'ls-tie-%'"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"ls-tie-a", "ls-tie-b", "ls-tie-c"}; !slices.Equal(ids(listed(t, db)), want) {
		t.Errorf("order = %v, want %v", ids(listed(t, db)), want)
	}
}

func TestListOrdersHonoursTheLimit(t *testing.T) {
	db := testAdapter(t)
	for _, id := range []string{"ls-lim-1", "ls-lim-2", "ls-lim-3"} {
		newRow(t, db, id, domain.OrderCreated)
	}
	for _, n := range []int{1, 2} {
		got, err := db.ListOrders(context.Background(), n)
		if err != nil || len(got) != n {
			t.Errorf("limit %d returned %d rows, err %v", n, len(got), err)
		}
	}
}

func TestListOrdersRejectsANonPositiveLimit(t *testing.T) {
	db := testAdapter(t)
	for _, n := range []int{0, -3} {
		if _, err := db.ListOrders(context.Background(), n); err == nil {
			t.Errorf("limit %d was accepted", n)
		}
	}
}

func TestListOrdersFailsAsAnInternalErrorWhenTheCallerGivesUp(t *testing.T) {
	db := testAdapter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := db.ListOrders(ctx, 10)
	if err == nil || got != nil {
		t.Errorf("got %v, %v, want an error and no rows", got, err)
	}
}
