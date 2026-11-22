// Package dispatch finds a driver for an order that is being prepared. It runs as a background job beside
// the HTTP server: it offers each waiting order to the nearest free driver, one at a time, and a decline or a
// lapse moves it on to the next. The choice itself (Pick) is a pure function; the offers are leases kept in
// the database (see ports.DispatchStore), so any number of servers can run the job at once.
package dispatch

import (
	"math"
	"sort"
	"time"

	"swiftdrop/internal/domain"
)

// Request is what an order asks of a driver: where the restaurant is, how far a driver may be, and when it is.
type Request struct {
	// Lat and Lng are the restaurant, where the driver has to go first.
	Lat, Lng float64
	// RadiusKm is how far from the restaurant a driver may be and still be offered the order. A driver exactly
	// that far is in.
	RadiusKm float64
	// Now is the time the choice is made, to tell a fresh point from a stale one.
	Now time.Time
	// MaxLocationAge is how old a driver's last point may be. A driver who has not been heard from for longer
	// may have gone, so they are not offered an order. Zero or less switches the check off.
	MaxLocationAge time.Duration
}

// distanceStepKm is the resolution distances are compared at: 10 metres. Two drivers closer to the restaurant
// than that are equally near, and the one who has waited longest goes first; without it a few metres of
// GPS noise would decide, which is not a reason anyone would accept for who gets an order.
const distanceStepKm = 0.01

// Pick chooses the driver to offer the order to, or reports false if none can be. The nearest to the
// restaurant (by domain.DistanceKm, to 10 metres) among those within the radius whose point is recent enough;
// equally near drivers are told apart by who has been idle longest, and then by id so the answer never
// depends on the order the candidates came in.
func Pick(req Request, candidates []domain.Candidate) (domain.Candidate, bool) {
	type ranked struct {
		c  domain.Candidate
		km int64 // distance in steps
	}
	var in []ranked
	for _, c := range candidates {
		if !domain.ValidPoint(c.Latitude, c.Longitude) {
			continue // a point that is not a place cannot be near anything
		}
		if req.MaxLocationAge > 0 && req.Now.Sub(c.LocationAt) > req.MaxLocationAge {
			continue
		}
		km := domain.DistanceKm(req.Lat, req.Lng, c.Latitude, c.Longitude)
		if km > req.RadiusKm {
			continue
		}
		in = append(in, ranked{c, int64(math.Round(km / distanceStepKm))})
	}
	if len(in) == 0 {
		return domain.Candidate{}, false
	}
	sort.Slice(in, func(i, j int) bool {
		a, b := in[i], in[j]
		switch {
		case a.km != b.km:
			return a.km < b.km
		case !a.c.IdleSince.Equal(b.c.IdleSince):
			return a.c.IdleSince.Before(b.c.IdleSince)
		default:
			return a.c.DriverID < b.c.DriverID
		}
	})
	return in[0].c, true
}
