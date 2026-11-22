package dispatch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// stub is a scripted ports.DispatchStore: it answers what the test says and records what it was asked, so
// each way a round can go is a few lines. The real store's guarantees are tested against Postgres.
type stub struct {
	mu sync.Mutex

	expired    int
	expireErr  error
	waiting    []domain.WaitingOrder
	waitingErr error
	candidates map[string][]domain.Candidate
	candErr    map[string]error
	declined   map[string]bool  // OfferOrder answers "not made" for these orders
	offerErr   map[string]error // OfferOrder fails for these orders

	// what it was asked
	calls                      []string
	limit                      int
	maxAge, lease              time.Duration
	offered                    [][2]string // order, driver
	afterOffer                 func(orderID string)
	expiredCalls, waitingCalls int
	candidateCalls, offerCalls int
}

func (s *stub) record(call string) { s.calls = append(s.calls, call) }

func (s *stub) ExpireOffers(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expiredCalls++
	s.record("expire")
	return s.expired, s.expireErr
}

func (s *stub) ListWaitingOrders(_ context.Context, limit int) ([]domain.WaitingOrder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waitingCalls++
	s.record("waiting")
	s.limit = limit
	return s.waiting, s.waitingErr
}

func (s *stub) ListCandidates(_ context.Context, orderID string, maxAge time.Duration) ([]domain.Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.candidateCalls++
	s.record("candidates " + orderID)
	s.maxAge = maxAge
	return s.candidates[orderID], s.candErr[orderID]
}

func (s *stub) OfferOrder(_ context.Context, orderID, driverID string, lease time.Duration) (domain.Offer, bool, error) {
	s.mu.Lock()
	s.offerCalls++
	s.record("offer " + orderID + " " + driverID)
	s.lease = lease
	err, declined := s.offerErr[orderID], s.declined[orderID]
	if err == nil && !declined {
		s.offered = append(s.offered, [2]string{orderID, driverID})
	}
	after := s.afterOffer
	s.mu.Unlock()
	if after != nil {
		after(orderID)
	}
	if err != nil {
		return domain.Offer{}, false, err
	}
	if declined {
		return domain.Offer{}, false, nil
	}
	return domain.Offer{OrderID: orderID, DriverID: driverID, Status: domain.OfferOpen, ExpiresAt: t0.Add(lease)}, true, nil
}

func (s *stub) AcceptOffer(context.Context, int64, string) (domain.Order, error) {
	panic("dispatch never accepts an offer: the driver does")
}
func (s *stub) DeclineOffer(context.Context, int64, string) error {
	panic("dispatch never declines an offer: the driver does")
}
func (s *stub) OpenOffer(context.Context, string) (domain.Offer, bool, error) {
	panic("dispatch never reads a driver's offer")
}
func (s *stub) ListOffers(context.Context, string) ([]domain.Offer, error) {
	panic("dispatch never reads the history")
}

var _ ports.DispatchStore = (*stub)(nil)

func waiting(ids ...string) []domain.WaitingOrder {
	out := make([]domain.WaitingOrder, len(ids))
	for i, id := range ids {
		out[i] = domain.WaitingOrder{OrderID: id, RestaurantID: "r", Latitude: restLat, Longitude: restLng}
	}
	return out
}

func newStub() *stub {
	return &stub{candidates: map[string][]domain.Candidate{}, candErr: map[string]error{}, declined: map[string]bool{}, offerErr: map[string]error{}}
}

func service(s *stub) *Service {
	svc := NewService(s)
	svc.Now = func() time.Time { return t0 }
	return svc
}

func TestNewServiceHasTheDocumentedDefaults(t *testing.T) {
	s := NewService(newStub())
	if s.Lease != 60*time.Second || s.RadiusKm != 8 || s.MaxLocationAge != 3*time.Minute || s.BatchSize != 20 || s.Now == nil {
		t.Errorf("defaults = lease %v, radius %v, age %v, batch %d", s.Lease, s.RadiusKm, s.MaxLocationAge, s.BatchSize)
	}
}

func TestARoundEndsLapsedOffersThenOffersEachWaitingOrderToTheNearestDriver(t *testing.T) {
	st := newStub()
	st.expired = 2
	st.waiting = waiting("o1", "o2")
	st.candidates["o1"] = []domain.Candidate{driver("far", 6, t0), driver("near", 1, t0)}
	st.candidates["o2"] = []domain.Candidate{driver("far", 6, t0)}
	svc := service(st)
	svc.Lease, svc.BatchSize, svc.MaxLocationAge = 45*time.Second, 7, 90*time.Second

	rep, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep != (Report{Expired: 2, Offered: 2}) {
		t.Errorf("report = %+v", rep)
	}
	want := []string{"expire", "waiting", "candidates o1", "offer o1 near", "candidates o2", "offer o2 far"}
	if !slices.Equal(st.calls, want) {
		t.Errorf("calls = %v, want %v: lapsed offers first, then each order in turn", st.calls, want)
	}
	if st.limit != 7 || st.maxAge != 90*time.Second || st.lease != 45*time.Second {
		t.Errorf("the store was given limit %d, age %v, lease %v; want the service's own settings", st.limit, st.maxAge, st.lease)
	}
}

func TestAnOrderWithNoDriverInRangeWaitsForTheNextRound(t *testing.T) {
	st := newStub()
	st.waiting = waiting("none", "tooFar")
	st.candidates["tooFar"] = []domain.Candidate{driver("d", 20, t0)} // beyond the radius
	rep, err := service(st).RunOnce(context.Background())
	if err != nil || rep != (Report{NoDriver: 2}) {
		t.Errorf("report = %+v, %v", rep, err)
	}
	if st.offerCalls != 0 {
		t.Error("an offer was made with nobody to offer to")
	}
}

func TestAnOfferThatCouldNotBeMadeIsSkippedNotRetriedAndNotAnError(t *testing.T) {
	st := newStub()
	st.waiting = waiting("o1", "o2")
	st.candidates["o1"] = []domain.Candidate{driver("d1", 1, t0)}
	st.candidates["o2"] = []domain.Candidate{driver("d2", 1, t0)}
	st.declined["o1"] = true // another server got there first
	rep, _ := service(st).RunOnce(context.Background())
	if rep != (Report{Skipped: 1, Offered: 1}) {
		t.Errorf("report = %+v, want one skipped and one offered", rep)
	}
	if st.offerCalls != 2 {
		t.Errorf("%d offers were tried, want one for each order and no retry within the round", st.offerCalls)
	}
}

func TestAStorageFailureForOneOrderDoesNotStopTheOthers(t *testing.T) {
	st := newStub()
	st.waiting = waiting("bad-list", "bad-offer", "good")
	st.candErr["bad-list"] = errors.New("boom")
	st.candidates["bad-offer"] = []domain.Candidate{driver("d1", 1, t0)}
	st.offerErr["bad-offer"] = errors.New("boom")
	st.candidates["good"] = []domain.Candidate{driver("d2", 1, t0)}
	rep, err := service(st).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep != (Report{Errors: 2, Offered: 1}) {
		t.Errorf("report = %+v, want two errors and the good order offered", rep)
	}
	if len(st.offered) != 1 || st.offered[0] != [2]string{"good", "d2"} {
		t.Errorf("offered = %v, want only the good order", st.offered)
	}
}

func TestARoundThatCannotReadItsListsReturnsTheErrorAndDoesNothingElse(t *testing.T) {
	st := newStub()
	st.expireErr = errors.New("expire is down")
	if _, err := service(st).RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "expire is down") {
		t.Errorf("error = %v, want the cause", err)
	}
	if st.waitingCalls != 0 {
		t.Error("it went on to list orders after failing to end lapsed offers")
	}

	st = newStub()
	st.waitingErr = errors.New("list is down")
	if _, err := service(st).RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "list is down") {
		t.Errorf("error = %v, want the cause", err)
	}
}

func TestARoundToldToStopLeavesTheRestWaiting(t *testing.T) {
	st := newStub()
	st.waiting = waiting("o1", "o2", "o3")
	for _, id := range []string{"o1", "o2", "o3"} {
		st.candidates[id] = []domain.Candidate{driver("d-"+id, 1, t0)}
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.afterOffer = func(string) { cancel() } // told to stop while the first offer is being made
	rep, err := service(st).RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Offered != 1 || st.offerCalls != 1 {
		t.Errorf("report %+v after %d offers, want it to stop after the first", rep, st.offerCalls)
	}
}

func TestRunMakesRoundsUntilToldToStop(t *testing.T) {
	st := newStub()
	st.waiting = waiting("o1")
	st.candidates["o1"] = []domain.Candidate{driver("d1", 1, t0)}
	ctx, cancel := context.WithCancel(context.Background())
	st.afterOffer = func(string) {} // the first round offers
	done := make(chan struct{})
	go func() {
		defer close(done)
		service(st).Run(ctx, time.Millisecond)
	}()
	deadline := time.After(5 * time.Second)
	for {
		st.mu.Lock()
		n := st.expiredCalls
		st.mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Run did not make rounds")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop when told to")
	}
}

func TestRunKeepsGoingAfterAFailedRoundAndStaysQuietWhenToldToStop(t *testing.T) {
	st := newStub()
	st.expireErr = errors.New("database is down")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		service(st).Run(ctx, time.Millisecond)
	}()
	for range 5000 {
		st.mu.Lock()
		n := st.expiredCalls
		st.mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if st.expiredCalls < 2 {
		t.Errorf("only %d rounds were tried: a failed round must not end the job", st.expiredCalls)
	}

	// a round that fails because it was cancelled is not logged as a failure; it just returns
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		service(st).Run(ctx2, time.Hour)
	}()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return for a context that was already cancelled")
	}
}

func TestRunWithoutAPositiveIntervalDoesNotRun(t *testing.T) {
	st := newStub()
	service(st).Run(context.Background(), 0)
	service(st).Run(context.Background(), -time.Second)
	if st.expiredCalls != 0 {
		t.Error("a round was made with no interval")
	}
}

func TestOnlyARoundThatDidSomethingIsWorthALogLine(t *testing.T) {
	for r, want := range map[Report]bool{
		{}:                        false,
		{NoDriver: 5}:             false, // orders that keep waiting would fill the log every round
		{Expired: 1}:              true,
		{Offered: 1}:              true,
		{Skipped: 1}:              true,
		{Errors: 1}:               true,
		{NoDriver: 5, Offered: 1}: true,
	} {
		if got := r.noteworthy(); got != want {
			t.Errorf("%+v noteworthy = %v, want %v", r, got, want)
		}
	}
}
