package domain

import "time"

// DriverStatus tracks whether a driver can receive new delivery offers.
type DriverStatus string

const (
	DriverOffline    DriverStatus = "offline"
	DriverAvailable  DriverStatus = "available"
	DriverOnDelivery DriverStatus = "on_delivery"
)

// Location is a point on the map. Kept as plain floats rather than a
// third-party geo type so the domain package has zero external
// dependencies (Global Constraint: "first milestone with only the Go
// standard library").
type Location struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Driver is a delivery driver's identity, availability, and last known
// position. Capacity exists now so Phase 4+ can reason about drivers who
// can legitimately hold more than one active delivery, even though v1
// dispatch will treat every driver as capacity 1.
type Driver struct {
	ID                string       `json:"id"`
	Status            DriverStatus `json:"status"`
	Location          Location     `json:"location"`
	LocationUpdatedAt time.Time    `json:"locationUpdatedAt"`
	Capacity          int          `json:"capacity"`
	ActiveOrderID     string       `json:"activeOrderId"` // empty when the driver holds no reservation
}
