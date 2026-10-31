package handlers

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// Lost races: another request changes the order between this request's read
// and its write. The write is compare-and-set, so it must change nothing and
// answer 409, never overwrite the winner.
// ---------------------------------------------------------------------------

// flipTo makes the order look like another request already moved it to state.
func flipTo(state domain.OrderState) func(*fakeDB, string) {
	return func(f *fakeDB, id string) {
		o := f.orders[id]
		o.State = state
		f.orders[id] = o
	}
}

func TestStateWriteThatLosesTheRaceAnswers409AndChangesNothing(t *testing.T) {
	tests := []struct {
		name string
		seed func(*fakeDB)
		path string
		// winner is the state the other request moved the order to
		winner domain.OrderState
	}{
		{"cancel", func(db *fakeDB) { db.seed("o1", domain.OrderCreated) }, "/orders/o1/cancel", domain.OrderPaymentPending},
		{"deliver", func(db *fakeDB) { db.seed("o1", domain.OrderPickedUp) }, "/orders/o1/deliver", domain.OrderDelivered},
		{"pickup", func(db *fakeDB) {
			db.seedWith("o1", domain.OrderPreparing, func(o *domain.Order) {
				now := time.Now()
				o.FoodReady, o.AssignedDriverID = &now, "d1"
			})
		}, "/orders/o1/pickup", domain.OrderCancelled},
		{"accept", func(db *fakeDB) { db.seed("o1", domain.OrderPaid) }, "/orders/o1/accept", domain.OrderCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB()
			tt.seed(db)
			db.beforeWrite = flipTo(tt.winner)

			rec := do(t, db, http.MethodPost, tt.path, "")
			wantStatus(t, rec, http.StatusConflict)
			wantErrorContains(t, rec, "changed while this request was running")
			if got := db.orders["o1"].State; got != tt.winner {
				t.Errorf("state = %s, want the winner's %s left alone", got, tt.winner)
			}
			wantWrites(t, db)
			if len(db.acceptDriverIDs) != 0 {
				t.Errorf("a lost race must not assign a driver, got %v", db.acceptDriverIDs)
			}
			if tt.name == "accept" && db.orders["o1"].AssignedDriverID != "" {
				t.Errorf("the losing accept left driver %q on the order", db.orders["o1"].AssignedDriverID)
			}
		})
	}
}

func TestPayThatLosesTheClaimNeverReachesTheProvider(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	db.beforeWrite = flipTo(domain.OrderCancelled) // someone else got there first

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusConflict)
	if len(g.calls) != 0 {
		t.Errorf("the provider was charged %d times by a request that lost the claim", len(g.calls))
	}
	wantWrites(t, db)
}

// The claim succeeded and the provider answered, but the order moved on in the
// meantime, so the final write must not overwrite it.
func TestPayFinalWriteThatLosesTheRaceDoesNotOverwrite(t *testing.T) {
	tests := []struct {
		name    string
		gateway *recordingGateway
	}{
		{"approved", approving()},
		{"declined", declining("no")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB()
			seedPayable(db, "o1", domain.PaymentCard, 1000)
			writes := 0
			db.beforeWrite = func(f *fakeDB, id string) {
				writes++
				if writes == 2 { // the write after the provider answered
					flipTo(domain.OrderDelivered)(f, id)
				}
			}

			rec := payWith(t, db, routerOf(tt.gateway, tt.gateway), "o1")
			wantStatus(t, rec, http.StatusConflict)
			if got := db.orders["o1"].State; got != domain.OrderDelivered {
				t.Errorf("state = %s, want the other request's delivered left alone", got)
			}
			wantWrites(t, db, domain.OrderPaymentPending)
		})
	}
}

func TestOrderThatVanishesBetweenReadAndWriteIsA404(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderCreated)
	db.beforeWrite = func(f *fakeDB, id string) { delete(f.orders, id) }

	rec := do(t, db, http.MethodPost, "/orders/o1/cancel", "")
	wantStatus(t, rec, http.StatusNotFound)
	wantErrorContains(t, rec, "could not be found")
}

// ---------------------------------------------------------------------------
// Real races: many goroutines hit the same order at the same moment. Run with
// -race. Exactly one request may win, the rest get a 409.
// ---------------------------------------------------------------------------

const racers = 50

// fireTogether starts n goroutines and releases them all at once.
func fireTogether(n int, fn func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
}

func post(s *Server, path string) int {
	rec := httptest.NewRecorder()
	s.Router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	return rec.Code
}

func tally(codes []int) (ok, conflict, other int) {
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			other++
		}
	}
	return
}

func TestFiftySimultaneousPaysChargeExactlyOnce(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 2000)
	s := NewServer(db, routerOf(g, g))

	codes := make([]int, racers)
	fireTogether(racers, func(i int) { codes[i] = post(s, "/orders/o1/pay") })

	// A request that arrives while the payment is still processing gets a 409
	// ("in progress"); one that arrives after it finished gets a replay (a 200
	// with the order). How the 50 split depends on timing, so the test pins
	// what must hold whatever the timing: one charge, no failures, one winner.
	ok, conflict, other := tally(codes)
	if other != 0 || ok < 1 || ok+conflict != racers {
		t.Errorf("200s=%d 409s=%d other=%d, want at least one 200 and nothing but 200s and 409s", ok, conflict, other)
	}
	if len(g.calls) != 1 {
		t.Errorf("the provider was charged %d times, want exactly 1", len(g.calls))
	}
	if got := db.orders["o1"].State; got != domain.OrderPaid {
		t.Errorf("state = %s, want paid", got)
	}
	if got := db.payments["o1"].Status; got != domain.PaymentAuthorized {
		t.Errorf("payment status = %s, want authorized", got)
	}
	wantWrites(t, db, domain.OrderPaymentPending, domain.OrderPaid)
}

func TestFiftySimultaneousCancelsCancelOnce(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderCreated)
	s := NewServer(db, testPayRouter())

	codes := make([]int, racers)
	fireTogether(racers, func(i int) { codes[i] = post(s, "/orders/o1/cancel") })

	ok, conflict, other := tally(codes)
	if ok != 1 || conflict != racers-1 || other != 0 {
		t.Errorf("200s=%d 409s=%d other=%d, want exactly one 200 and %d 409s", ok, conflict, other, racers-1)
	}
	wantWrites(t, db, domain.OrderCancelled)
}

func TestFiftySimultaneousAcceptsAssignOneDriver(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderPaid)
	s := NewServer(db, testPayRouter())

	codes := make([]int, racers)
	fireTogether(racers, func(i int) { codes[i] = post(s, "/orders/o1/accept") })

	ok, conflict, other := tally(codes)
	if ok != 1 || conflict != racers-1 || other != 0 {
		t.Errorf("200s=%d 409s=%d other=%d, want exactly one 200 and %d 409s", ok, conflict, other, racers-1)
	}
	if len(db.acceptDriverIDs) != 1 {
		t.Fatalf("%d drivers were assigned, want exactly 1", len(db.acceptDriverIDs))
	}
	if got := db.orders["o1"].AssignedDriverID; got != db.acceptDriverIDs[0] {
		t.Errorf("stored driver %q is not the one that won, %q", got, db.acceptDriverIDs[0])
	}
}

// Pay and cancel racing on one order: whatever interleaving happens, the
// customer is charged at most once and the order ends in a single final state.
//
// Known gap this deliberately does not assert away: a cancel that lands between
// the claim and the provider's answer can leave an order that was charged but is
// cancelled. Closing that needs the payment record (the claim), not this write.
func TestPayAndCancelRacingChargeAtMostOnce(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 2000)
	s := NewServer(db, routerOf(g, g))

	codes := make([]int, racers)
	fireTogether(racers, func(i int) {
		if i%2 == 0 {
			codes[i] = post(s, "/orders/o1/pay")
		} else {
			codes[i] = post(s, "/orders/o1/cancel")
		}
	})

	for i, c := range codes {
		if c != http.StatusOK && c != http.StatusConflict {
			t.Errorf("request %d answered %d, want 200 or 409", i, c)
		}
	}
	if len(g.calls) > 1 {
		t.Errorf("the provider was charged %d times, want at most 1", len(g.calls))
	}
	if got := db.orders["o1"].State; !slices.Contains([]domain.OrderState{domain.OrderPaid, domain.OrderCancelled}, got) {
		t.Errorf("final state = %s, want paid or cancelled", got)
	}
}
