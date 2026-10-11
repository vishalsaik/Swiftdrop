package adapters

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

func nowPtr() *time.Time {
	n := time.Now()
	return &n
}

// The adapter talks to real Postgres, so these tests need a database with
// schema.sql applied. They are skipped unless SWIFTDROP_TEST_DSN points at
// one (use a separate database, not your dev data), for example:
//
//	postgres://postgres:postgres@127.0.0.1:5434/swiftdrop_test?sslmode=disable

func testAdapter(t *testing.T) *DBAdapter {
	t.Helper()
	dsn := os.Getenv("SWIFTDROP_TEST_DSN")
	if dsn == "" {
		t.Skip("SWIFTDROP_TEST_DSN not set; skipping Postgres integration test")
	}
	db, err := NewDatabaseConnection(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Pool.Close)
	return db
}

// newRow inserts an order in the given state and removes it when the test ends.
func newRow(t *testing.T, db *DBAdapter, id string, state domain.OrderState) domain.Order {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), "DELETE FROM orders WHERE id = $1", id)
	})
	o, err := db.CreateOrder(ctx, domain.Order{
		ID: id, PaymentType: domain.PaymentWallet, CustomerID: "cust", RestaurantID: "rest", State: state,
		Items: []domain.OrderItem{{SKU: "S1", Name: "Burger", Quantity: 2, UnitPriceCents: 599}},
	})
	if err != nil {
		t.Fatalf("CreateOrder(%s): %v", id, err)
	}
	return o
}

// wantNotFound checks the contract the HTTP layer relies on: a missing order
// is reported as ports.ErrOrderNotFound, not as driver-specific text.
func wantNotFound(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ports.ErrOrderNotFound) {
		t.Fatalf("error = %v, want one wrapping ports.ErrOrderNotFound", err)
	}
	if strings.Contains(err.Error(), "no rows") {
		t.Errorf("driver text leaked through the sentinel: %v", err)
	}
}

func TestNewDatabaseConnectionRejectsBadDSN(t *testing.T) {
	// Neither case needs a running database.
	for name, dsn := range map[string]string{
		"unparseable":   "this is not a dsn",
		"nothing there": "postgres://postgres:postgres@127.0.0.1:1/swiftdrop?sslmode=disable&connect_timeout=2",
	} {
		t.Run(name, func(t *testing.T) {
			db, err := NewDatabaseConnection(dsn)
			if err == nil {
				db.Pool.Close()
				t.Fatal("expected an error")
			}
			if db != nil {
				t.Error("a failed connection must not return an adapter")
			}
		})
	}
}

func TestNewDatabaseConnection(t *testing.T) {
	db := testAdapter(t)
	if err := db.Pool.Ping(context.Background()); err != nil {
		t.Errorf("ping: %v", err)
	}
}

func TestCreateAndGetRoundTrip(t *testing.T) {
	db := testAdapter(t)
	created := newRow(t, db, "it-roundtrip", domain.OrderCreated)

	got, err := db.GetOrderbyId(context.Background(), "it-roundtrip")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "it-roundtrip" || got.CustomerID != "cust" || got.RestaurantID != "rest" {
		t.Errorf("wrong scalar fields: %+v", got)
	}
	if got.State != domain.OrderCreated {
		t.Errorf("state = %s, want created", got.State)
	}
	if len(got.Items) != 1 || got.Items[0].SKU != "S1" || got.Items[0].Quantity != 2 || got.Items[0].UnitPriceCents != 599 {
		t.Errorf("items did not survive the JSONB round trip: %+v", got.Items)
	}
	if got.FoodReady != nil {
		t.Errorf("foodReady = %v, want nil on a new order", got.FoodReady)
	}
	if got.AssignedDriverID != "" {
		t.Errorf("assignedDriverId = %q, want empty on a new order", got.AssignedDriverID)
	}
	if created.ID != got.ID {
		t.Errorf("CreateOrder returned id %q, stored %q", created.ID, got.ID)
	}
}

func TestCreateOrderIgnoresServerOwnedFields(t *testing.T) {
	db := testAdapter(t)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), "DELETE FROM orders WHERE id = 'it-owned'")
	})
	now := nowPtr()
	got, err := db.CreateOrder(context.Background(), domain.Order{
		ID: "it-owned", CustomerID: "c", RestaurantID: "r", State: domain.OrderCreated,
		Items: []domain.OrderItem{}, AssignedDriverID: "hacker", FoodReady: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.AssignedDriverID != "" || got.FoodReady != nil {
		t.Errorf("INSERT must not write server-owned columns, got %+v", got)
	}
}

func TestCreateDuplicateIDIsReportedAsErrOrderExists(t *testing.T) {
	db := testAdapter(t)
	newRow(t, db, "it-dup", domain.OrderCreated)
	_, err := db.CreateOrder(context.Background(), domain.Order{
		ID: "it-dup", CustomerID: "other", RestaurantID: "r", State: domain.OrderCreated, Items: []domain.OrderItem{},
	})
	if !errors.Is(err, ports.ErrOrderExists) {
		t.Fatalf("error = %v, want one wrapping ports.ErrOrderExists", err)
	}
	if !strings.Contains(err.Error(), "it-dup") {
		t.Errorf("the error should name the order id: %v", err)
	}

	// The original must be untouched by the rejected insert.
	stored, err := db.GetOrderbyId(context.Background(), "it-dup")
	if err != nil {
		t.Fatal(err)
	}
	if stored.CustomerID != "cust" {
		t.Errorf("duplicate insert overwrote the original: %+v", stored)
	}
}

func TestPaymentTypeIsStoredAndReturnedByEveryQuery(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	created := newRow(t, db, "it-payment", domain.OrderPaid)
	if created.PaymentType != domain.PaymentWallet {
		t.Fatalf("CreateOrder returned paymentType %q, want wallet", created.PaymentType)
	}

	got, err := db.GetOrderbyId(ctx, "it-payment")
	if err != nil || got.PaymentType != domain.PaymentWallet {
		t.Errorf("GetOrderbyId: paymentType = %q, err = %v", got.PaymentType, err)
	}
	updated, err := db.UpdateOrderState(ctx, "it-payment", string(domain.OrderPreparing))
	if err != nil || updated.PaymentType != domain.PaymentWallet {
		t.Errorf("UpdateOrderState: paymentType = %q, err = %v", updated.PaymentType, err)
	}
	ready, err := db.SetFoodReady(ctx, "it-payment")
	if err != nil || ready.PaymentType != domain.PaymentWallet {
		t.Errorf("SetFoodReady: paymentType = %q, err = %v", ready.PaymentType, err)
	}
	accepted, err := db.AcceptOrder(ctx, "it-payment", "d1", string(domain.OrderPreparing))
	if err != nil || accepted.PaymentType != domain.PaymentWallet {
		t.Errorf("AcceptOrder: paymentType = %q, err = %v", accepted.PaymentType, err)
	}
}

func TestGetMissingOrder(t *testing.T) {
	db := testAdapter(t)
	_, err := db.GetOrderbyId(context.Background(), "it-does-not-exist")
	wantNotFound(t, err)
}

func TestUpdateOrderState(t *testing.T) {
	db := testAdapter(t)
	before := newRow(t, db, "it-update", domain.OrderCreated)

	got, err := db.UpdateOrderState(context.Background(), "it-update", string(domain.OrderPaid))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.OrderPaid {
		t.Errorf("returned state = %s, want paid", got.State)
	}
	if got.CustomerID != before.CustomerID || len(got.Items) != len(before.Items) {
		t.Errorf("only state should change, got %+v", got)
	}

	stored, err := db.GetOrderbyId(context.Background(), "it-update")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != domain.OrderPaid {
		t.Errorf("stored state = %s, want paid", stored.State)
	}
}

func TestUpdateOrderStateMissingOrder(t *testing.T) {
	db := testAdapter(t)
	_, err := db.UpdateOrderState(context.Background(), "it-does-not-exist", string(domain.OrderPaid))
	wantNotFound(t, err)
}

func TestSetFoodReady(t *testing.T) {
	db := testAdapter(t)
	newRow(t, db, "it-ready", domain.OrderPreparing)

	first, err := db.SetFoodReady(context.Background(), "it-ready")
	if err != nil {
		t.Fatal(err)
	}
	if first.FoodReady == nil {
		t.Fatal("foodReady should be set")
	}
	if first.State != domain.OrderPreparing {
		t.Errorf("SetFoodReady must not change state, got %s", first.State)
	}

	second, err := db.SetFoodReady(context.Background(), "it-ready")
	if err != nil {
		t.Fatal(err)
	}
	if second.FoodReady == nil || !second.FoodReady.Equal(*first.FoodReady) {
		t.Errorf("a second call must keep the original timestamp: %v -> %v", first.FoodReady, second.FoodReady)
	}
}

func TestSetFoodReadyMissingOrder(t *testing.T) {
	db := testAdapter(t)
	_, err := db.SetFoodReady(context.Background(), "it-does-not-exist")
	wantNotFound(t, err)
}

func TestAcceptOrderWritesStateAndDriverTogether(t *testing.T) {
	db := testAdapter(t)
	newRow(t, db, "it-accept", domain.OrderPaid)

	got, err := db.AcceptOrder(context.Background(), "it-accept", "driver-42", string(domain.OrderPreparing))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.OrderPreparing || got.AssignedDriverID != "driver-42" {
		t.Errorf("returned order = %+v", got)
	}

	stored, err := db.GetOrderbyId(context.Background(), "it-accept")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != domain.OrderPreparing || stored.AssignedDriverID != "driver-42" {
		t.Errorf("stored order = %+v", stored)
	}
	if stored.FoodReady != nil {
		t.Error("accepting must not mark the food ready")
	}
}

func TestAcceptOrderMissingOrder(t *testing.T) {
	db := testAdapter(t)
	_, err := db.AcceptOrder(context.Background(), "it-does-not-exist", "d", string(domain.OrderPreparing))
	wantNotFound(t, err)
}
