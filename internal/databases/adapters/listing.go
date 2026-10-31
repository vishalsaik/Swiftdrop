package adapters

import (
	"context"
	"errors"
	"fmt"
	"time"

	"swiftdrop/internal/domain"
)

// nullablePayment is a payment as a LEFT JOIN returns it: every column is NULL
// when the order has no payment, so every destination is a pointer.
type nullablePayment struct {
	id, orderID, status, providerID, reason, refundStatus, refundID, refundError *string
	amount                                                                       *int64
	attempts                                                                     *int
	lease, created, updated, refundLease                                         *time.Time
}

// toPayment returns the payment, or nil if the row had none (a payment always
// has an id, so a NULL id means the join found no payment).
func (n nullablePayment) toPayment() *domain.Payment {
	if n.id == nil {
		return nil
	}
	p := &domain.Payment{
		ID: *n.id, OrderID: deref(n.orderID), Status: domain.PaymentStatus(deref(n.status)),
		AmountCents: derefInt64(n.amount), ProviderPaymentID: deref(n.providerID), Reason: deref(n.reason),
		RefundStatus: domain.RefundStatus(deref(n.refundStatus)), RefundID: deref(n.refundID),
		RefundError: deref(n.refundError), RefundLeaseExpiresAt: n.refundLease,
	}
	if n.attempts != nil {
		p.RefundAttempts = *n.attempts
	}
	if n.lease != nil {
		p.LeaseExpiresAt = *n.lease
	}
	if n.created != nil {
		p.CreatedAt = *n.created
	}
	if n.updated != nil {
		p.UpdatedAt = *n.updated
	}
	return p
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt64(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

func (db *DBAdapter) ListOrders(ctx context.Context, limit int) ([]domain.OrderWithPayment, error) {
	if limit <= 0 {
		return nil, errors.New("failed to list orders: limit must be positive")
	}
	// One LEFT JOIN, so an order and its payment come from the same snapshot.
	// created_at then id makes the order stable when two orders share a moment.
	query := `
		SELECT ` + qualify("o.", orderColumns) + `, ` + qualify("p.", paymentColumns) + `
		FROM orders o LEFT JOIN payments p ON p.order_id = o.id
		ORDER BY o.created_at DESC, o.id
		LIMIT $1`
	rows, err := db.Pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list orders: %w", err)
	}
	defer rows.Close()

	out := []domain.OrderWithPayment{}
	for rows.Next() {
		var o domain.Order
		var n nullablePayment
		err := rows.Scan(
			&o.ID, &o.PaymentType, &o.CustomerID, &o.RestaurantID, &o.Items, &o.State,
			&o.FoodReady, &o.AssignedDriverID, &o.CreatedAt, &o.UpdatedAt,
			&n.id, &n.orderID, &n.status, &n.amount, &n.providerID, &n.reason, &n.lease, &n.created, &n.updated,
			&n.refundStatus, &n.refundID, &n.attempts, &n.refundError, &n.refundLease,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to list orders: %w", err)
		}
		out = append(out, domain.OrderWithPayment{Order: o, Payment: n.toPayment()})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list orders: %w", err)
	}
	return out, nil
}
