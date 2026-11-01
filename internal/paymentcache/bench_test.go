package paymentcache_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"swiftdrop/internal/databases/adapters"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/paymentcache"
)

// What the cache is worth: the read that a retried /pay does first, against real
// Postgres. It needs the same database as the adapter tests and is skipped without
// one:
//
//	go test ./internal/paymentcache -bench . -benchmem
//
// "database" asks Postgres every time, "cached" is the same read through the cache.
// Both ask for the payment of an order that is already paid, which is the only
// case the cache serves.

func paidOrder(b *testing.B) (*adapters.DBAdapter, string) {
	b.Helper()
	dsn := os.Getenv("SWIFTDROP_TEST_DSN")
	if dsn == "" {
		b.Skip("SWIFTDROP_TEST_DSN not set; skipping the benchmark that needs Postgres")
	}
	db, err := adapters.NewDatabaseConnection(dsn)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	id := fmt.Sprintf("bench-%d", time.Now().UnixNano())
	b.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, "DELETE FROM payment_events WHERE payment_id = $1", "pay-"+id)
		_, _ = db.Pool.Exec(ctx, "DELETE FROM payments WHERE order_id = $1", id)
		_, _ = db.Pool.Exec(ctx, "DELETE FROM orders WHERE id = $1", id)
		db.Pool.Close()
	})
	if _, err := db.CreateOrder(ctx, domain.Order{
		ID: id, PaymentType: domain.PaymentCard, CustomerID: "c", RestaurantID: "r", State: domain.OrderCreated,
		Items: []domain.OrderItem{{SKU: "S", Name: "Meal", Quantity: 1, UnitPriceCents: 1000}},
	}); err != nil {
		b.Fatal(err)
	}
	if _, _, err := db.StartPayment(ctx, domain.Payment{ID: "pay-" + id, OrderID: id, AmountCents: 1000}, time.Minute,
		string(domain.OrderCreated), string(domain.OrderPaymentPending)); err != nil {
		b.Fatal(err)
	}
	if _, err := db.FinishPayment(ctx, "pay-"+id, domain.PaymentAuthorized, "pi_bench", "",
		string(domain.OrderPaymentPending), string(domain.OrderPaid)); err != nil {
		b.Fatal(err)
	}
	return db, id
}

func BenchmarkLookupOfAPaidOrder(b *testing.B) {
	db, id := paidOrder(b)
	ctx := context.Background()

	b.Run("database", func(b *testing.B) {
		for b.Loop() {
			if _, err := db.GetPaymentByOrder(ctx, id); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached", func(b *testing.B) {
		c := paymentcache.New(db, 1000)
		for b.Loop() {
			if _, err := c.GetPaymentByOrder(ctx, id); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("database, parallel", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := db.GetPaymentByOrder(ctx, id); err != nil {
					b.Fatal(err)
				}
			}
		})
	})
	b.Run("cached, parallel", func(b *testing.B) {
		c := paymentcache.New(db, 1000)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := c.GetPaymentByOrder(ctx, id); err != nil {
					b.Fatal(err)
				}
			}
		})
	})
}
