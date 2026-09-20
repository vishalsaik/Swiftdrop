package domain

import "time"

// EventType names the kind of domain occurrence being published.
type EventType string

const (
	EventOrderCreated      EventType = "order.created"
	EventOrderStateChanged EventType = "order.state_changed"
	EventDriverAssigned    EventType = "driver.assigned"
	EventDriverLocation    EventType = "driver.location_updated"
	EventOrderCancelled    EventType = "order.cancelled"
)

// Event is one entry in an order's event stream. Sequence is assigned by
// internal/events (Phase 3), not here — the domain type only describes the
// shape of an event, it doesn't know how ordering is enforced.
type Event struct {
	ID         string    `json:"id"`
	OrderID    string    `json:"orderId"`
	Type       EventType `json:"type"`
	Sequence   uint64    `json:"sequence"`
	OccurredAt time.Time `json:"occurredAt"`
	Payload    any       `json:"payload"`
}
