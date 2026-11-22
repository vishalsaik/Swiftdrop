package dispatch

import (
	"context"
	"fmt"
	"log"
	"time"

	"swiftdrop/internal/databases/ports"
)

// Defaults for NewService.
const (
	// DefaultLease is how long an offer holds an order for one driver before it goes to the next.
	DefaultLease = 60 * time.Second
	// DefaultRadiusKm is how far from the restaurant a driver may be and still be offered its orders.
	DefaultRadiusKm = 8.0
	// DefaultMaxLocationAge is how old a driver's last point may be. A driver's app sends one every 30
	// seconds while online, so three minutes means six missed in a row: they have probably gone.
	DefaultMaxLocationAge = 3 * time.Minute
	// DefaultBatchSize is how many waiting orders one round takes on.
	DefaultBatchSize = 20
)

// Service finds a driver for every order that is being prepared and has none. Each round it first ends the
// offers that have lapsed, then offers each waiting order, oldest first, to the nearest free driver (see
// Pick). The offer is a lease taken in the database, so several servers can run the job together and an
// order is still never offered to two drivers, nor a driver two orders; a server that dies simply leaves
// leases that run out.
type Service struct {
	Store ports.DispatchStore

	// Lease is how long each offer holds an order for its driver.
	Lease time.Duration
	// RadiusKm is how far from the restaurant a driver may be.
	RadiusKm float64
	// MaxLocationAge is how old a driver's last point may be. The store applies it with the database's clock,
	// which is the clock that stamped the point; this server's clock is never compared with it.
	MaxLocationAge time.Duration
	// BatchSize is how many waiting orders one round takes on.
	BatchSize int
	// Now is the clock (a test sets it).
	Now func() time.Time
}

// NewService returns a Service with the default lease, radius, point age and batch size.
func NewService(store ports.DispatchStore) *Service {
	return &Service{
		Store: store, Lease: DefaultLease, RadiusKm: DefaultRadiusKm,
		MaxLocationAge: DefaultMaxLocationAge, BatchSize: DefaultBatchSize, Now: time.Now,
	}
}

// Report says what one round did.
type Report struct {
	Expired  int // offers ended because their lease ran out, or their order no longer needed a driver
	Offered  int // orders offered to a driver
	NoDriver int // orders for which no driver could be offered now (they wait for the next round)
	Skipped  int // the offer was not made because something changed since the lists were read
	Errors   int // storage failures; the order is left as it was
}

// noteworthy is whether the round did something worth a line in the log. Orders that merely keep waiting for
// a driver are not: they would fill the log every round.
func (r Report) noteworthy() bool { return r.Expired+r.Offered+r.Skipped+r.Errors > 0 }

// RunOnce makes one round. It returns an error only if the first reads fail; a problem with one order is
// logged, counted in the Report and does not stop the others.
func (s *Service) RunOnce(ctx context.Context) (Report, error) {
	var rep Report
	expired, err := s.Store.ExpireOffers(ctx)
	if err != nil {
		return rep, fmt.Errorf("dispatch: ending lapsed offers: %w", err)
	}
	rep.Expired = expired
	waiting, err := s.Store.ListWaitingOrders(ctx, s.BatchSize)
	if err != nil {
		return rep, fmt.Errorf("dispatch: listing orders that need a driver: %w", err)
	}
	for _, w := range waiting {
		if ctx.Err() != nil {
			break // told to stop: whatever is left waits for the next round
		}
		s.offerOne(ctx, w.OrderID, w.Latitude, w.Longitude, &rep)
	}
	return rep, nil
}

func (s *Service) offerOne(ctx context.Context, orderID string, lat, lng float64, rep *Report) {
	candidates, err := s.Store.ListCandidates(ctx, orderID, s.MaxLocationAge)
	if err != nil {
		log.Printf("dispatch: listing drivers for order %s: %v", orderID, err)
		rep.Errors++
		return
	}
	best, ok := Pick(Request{Lat: lat, Lng: lng, RadiusKm: s.RadiusKm, Now: s.Now()}, candidates)
	if !ok {
		rep.NoDriver++
		return
	}
	offer, made, err := s.Store.OfferOrder(ctx, orderID, best.DriverID, s.Lease)
	switch {
	case err != nil:
		log.Printf("dispatch: offering order %s to driver %s: %v", orderID, best.DriverID, err)
		rep.Errors++
	case !made:
		rep.Skipped++ // someone else offered the order or took the driver first
	default:
		log.Printf("dispatch: order %s offered to driver %s until %s", orderID, best.DriverID, offer.ExpiresAt.Format(time.RFC3339))
		rep.Offered++
	}
}

// Run makes a round now and then every interval until ctx is cancelled. It blocks, so start it in its own
// goroutine.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		log.Printf("dispatch: not running, the interval must be positive (got %v)", interval)
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		rep, err := s.RunOnce(ctx)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				log.Printf("%v", err)
			}
		case rep.noteworthy():
			log.Printf("dispatch: round done: %d lapsed, %d offered, %d without a driver, %d skipped, %d errors",
				rep.Expired, rep.Offered, rep.NoDriver, rep.Skipped, rep.Errors)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
