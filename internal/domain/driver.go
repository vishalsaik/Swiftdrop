package domain

import "time"

// Driver is a delivery driver's working state: whether they are taking deliveries, where they last said
// they were, and whether they are busy. The person (name, email, sign-in) is a User with RoleDriver; a
// Driver is what dispatch needs to know about them.
//
// Where a driver is stays on the server only as long as it is useful: one point, replaced by the next,
// never a trail. It is shown to staff (the Drivers list) and used to pick the nearest driver, and to no
// one else.
type Driver struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	// Online is true while the driver says they are taking deliveries.
	Online bool `json:"online"`
	// Latitude and Longitude are the last point the driver sent, nil until they send one (both or neither).
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	// LocationAt is when that point was sent.
	LocationAt *time.Time `json:"locationAt,omitempty"`
	// OnlineSince is when the driver last went online; nil while offline.
	OnlineSince *time.Time `json:"onlineSince,omitempty"`
	// Busy is true while the driver holds a delivery that is not finished (it is read from the orders, so it
	// can never disagree with them). Free is Online and not Busy.
	Busy bool `json:"busy"`
	// IdleSince is when the driver last became free: going online, or finishing a delivery. Dispatch breaks a
	// tie between two equally near drivers in favour of the one idle longest.
	IdleSince *time.Time `json:"idleSince,omitempty"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// Located reports whether the driver has sent a point.
func (d Driver) Located() bool { return d.Latitude != nil && d.Longitude != nil }
