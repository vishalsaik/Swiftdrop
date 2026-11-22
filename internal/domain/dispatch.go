package domain

import "time"

// OfferStatus is where an offer of an order to a driver stands.
type OfferStatus string

const (
	OfferOpen     OfferStatus = "offered"  // the order is held for the driver until ExpiresAt
	OfferAccepted OfferStatus = "accepted" // the driver took it: it is now their delivery
	OfferDeclined OfferStatus = "declined" // the driver said no
	OfferExpired  OfferStatus = "expired"  // the driver did not answer in time, went offline, or the order no longer needed a driver
)

// Offer is one order offered to one driver. It is a lease: while it is open the order is held for that
// driver and for nobody else, and it ends once, as accepted, declined or expired. The ended offers are a
// history of who was asked, when, and what came of it.
type Offer struct {
	ID        int64       `json:"id"`
	OrderID   string      `json:"orderId"`
	DriverID  string      `json:"driverId"`
	Status    OfferStatus `json:"status"`
	OfferedAt time.Time   `json:"offeredAt"`
	// ExpiresAt is when the lease runs out, by the database's clock.
	ExpiresAt time.Time  `json:"expiresAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	// RemainingSeconds is how long the lease has left, by the database's clock at the moment it was read. It is
	// set only when a driver's open offer is read, and it is what a countdown should count down from: the
	// browser's clock, this server's and the database's need not agree, so ExpiresAt is no good for that.
	RemainingSeconds float64 `json:"remainingSeconds,omitempty"`
}

// WaitingOrder is an order that needs a driver: it is being prepared, has none and is not on offer. It
// carries the one fact dispatch needs about it, where the restaurant is.
type WaitingOrder struct {
	OrderID      string
	RestaurantID string
	Latitude     float64
	Longitude    float64
	// Since is when the order last changed (when it started to wait), so the longest waiting is offered first.
	Since time.Time
}

// Candidate is a driver who could be offered an order now: online, free (no unfinished delivery, no open
// offer), not yet offered this order, and with a point they sent recently.
type Candidate struct {
	DriverID   string
	Latitude   float64
	Longitude  float64
	LocationAt time.Time
	// IdleSince is when they last became free: going online or finishing a delivery.
	IdleSince time.Time
}
