package adapters

import (
	"context"
	"testing"
	"time"

	"swiftdrop/internal/domain"
)

// Every order records when it was created and when a write last changed it.

func TestANewOrderIsStampedWithTheTimeItWasCreated(t *testing.T) {
	db := testAdapter(t)
	before := time.Now().Add(-time.Minute)
	created := newRow(t, db, "it-times-new", domain.OrderCreated)

	if created.CreatedAt.Before(before) || created.CreatedAt.After(time.Now().Add(time.Minute)) {
		t.Errorf("createdAt = %v, want about now", created.CreatedAt)
	}
	if !created.UpdatedAt.Equal(created.CreatedAt) {
		t.Errorf("updatedAt = %v, want it equal to createdAt %v on a new order", created.UpdatedAt, created.CreatedAt)
	}
	got, err := db.GetOrderbyId(context.Background(), "it-times-new")
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(created.CreatedAt) || !got.UpdatedAt.Equal(created.UpdatedAt) {
		t.Errorf("a read returned %v/%v, want the stored %v/%v", got.CreatedAt, got.UpdatedAt, created.CreatedAt, created.UpdatedAt)
	}
}

// A write moves updatedAt forward and never touches createdAt. A failed
// (conflicting) write changes neither.
func TestEveryOrderWriteMovesUpdatedAtAndKeepsCreatedAt(t *testing.T) {
	ctx := context.Background()
	writes := map[string]struct {
		state domain.OrderState
		write func(db *DBAdapter, id string) (domain.Order, error)
	}{
		"UpdateOrderState": {domain.OrderCreated, func(db *DBAdapter, id string) (domain.Order, error) {
			return db.UpdateOrderState(ctx, id, string(domain.OrderCreated), string(domain.OrderPaid))
		}},
		"SetFoodReady": {domain.OrderPreparing, func(db *DBAdapter, id string) (domain.Order, error) {
			return db.SetFoodReady(ctx, id, string(domain.OrderPreparing))
		}},
		"AcceptOrder": {domain.OrderPaid, func(db *DBAdapter, id string) (domain.Order, error) {
			return db.AcceptOrder(ctx, id, "d1", string(domain.OrderPaid), string(domain.OrderPreparing))
		}},
		"StartPayment": {domain.OrderCreated, func(db *DBAdapter, id string) (domain.Order, error) {
			_, _, err := db.StartPayment(ctx, domain.Payment{ID: "pay-" + id, OrderID: id, AmountCents: 1}, time.Minute,
				string(domain.OrderCreated), string(domain.OrderPaymentPending))
			if err != nil {
				return domain.Order{}, err
			}
			return db.GetOrderbyId(ctx, id)
		}},
		"FinishPayment": {domain.OrderPaymentPending, func(db *DBAdapter, id string) (domain.Order, error) {
			if _, _, err := db.ClaimPayment(ctx, domain.Payment{ID: "pay-" + id, OrderID: id, AmountCents: 1}, time.Minute); err != nil {
				return domain.Order{}, err
			}
			return db.FinishPayment(ctx, "pay-"+id, domain.PaymentAuthorized, "pi_x", "",
				string(domain.OrderPaymentPending), string(domain.OrderPaid))
		}},
	}
	for name, w := range writes {
		t.Run(name, func(t *testing.T) {
			db := testAdapter(t)
			id := "it-times-" + name
			paidOrder(t, db, id) // also removes any payment the write creates
			if _, err := db.Pool.Exec(ctx, "UPDATE orders SET state = $2 WHERE id = $1", id, string(w.state)); err != nil {
				t.Fatal(err)
			}
			before, err := db.GetOrderbyId(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)

			after, err := w.write(db, id)
			if err != nil {
				t.Fatal(err)
			}
			if !after.CreatedAt.Equal(before.CreatedAt) {
				t.Errorf("createdAt moved from %v to %v", before.CreatedAt, after.CreatedAt)
			}
			if !after.UpdatedAt.After(before.UpdatedAt) {
				t.Errorf("updatedAt = %v, want it after %v", after.UpdatedAt, before.UpdatedAt)
			}
		})
	}
}

func TestAConflictingWriteLeavesUpdatedAtAlone(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	before := newRow(t, db, "it-times-conflict", domain.OrderCreated)
	time.Sleep(20 * time.Millisecond)

	if _, err := db.UpdateOrderState(ctx, "it-times-conflict", string(domain.OrderPaid), string(domain.OrderPreparing)); err == nil {
		t.Fatal("the write should have conflicted")
	}
	after, err := db.GetOrderbyId(ctx, "it-times-conflict")
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("updatedAt moved from %v to %v on a write that changed nothing", before.UpdatedAt, after.UpdatedAt)
	}
}
