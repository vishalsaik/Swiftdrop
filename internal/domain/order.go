package domain

import "time"

// OrderState is one node in the order lifecycle. It is a defined string type
// (not an int enum) so that logs, events, and API responses are human
// readable without a lookup table.
type OrderState string

const (
	OrderCreated        OrderState = "created"
	OrderPaymentPending OrderState = "payment_pending"
	OrderPaid           OrderState = "paid"
	OrderPreparing      OrderState = "preparing"
	OrderReadyForPickup OrderState = "ready_for_pickup"
	OrderDriverAssigned OrderState = "driver_assigned"
	OrderPickedUp       OrderState = "picked_up"
	OrderDelivered      OrderState = "delivered"
	OrderCancelled      OrderState = "cancelled"
)

// OrderItem is one line item on an order. Prices are stored in integer
// cents to avoid floating-point rounding errors in money math.
type OrderItem struct {
	SKU            string `json:"sku"`
	Name           string `json:"name"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unitPriceCents"`
}

// Order is the aggregate root for a single food-delivery order. It carries
// no behavior of its own beyond simple derived values — state transition
// rules live in internal/order/state.go so the domain type stays a plain
// data holder and the transition logic stays independently testable.
type Order struct {
	ID               string      `json:"id"`
	PaymentType      string      `json:"paymentType"`
	CustomerID       string      `json:"customerId"`
	RestaurantID     string      `json:"restaurantId"`
	Items            []OrderItem `json:"items"`
	State            OrderState  `json:"state"`
	AssignedDriverID string      `json:"assignedDriverId"`
	CreatedAt        time.Time   `json:"createdAt"`
	UpdatedAt        time.Time   `json:"updatedAt"`
}

// TotalCents sums the order's line items.
func (o Order) TotalCents() int64 {
	var total int64
	for _, item := range o.Items {
		total += item.UnitPriceCents * int64(item.Quantity)
	}
	return total
}
