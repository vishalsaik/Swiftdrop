package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// Dispatch, against the real database: offers are leases, and what keeps an order from two drivers (and a
// driver from two orders) is the database itself, so that is what is tested, including under many callers at
// once. Orders made here are it-dsp-, their restaurant is it-dsp-rest, and the accounts are the au- accounts of
// authDB.

const dspRest = "it-dsp-rest"

const dspLat, dspLng = 12.9719, 77.6412

func dispatchDB(t *testing.T) *DBAdapter {
	t.Helper()
	db := authDB(t)
	ctx := context.Background()
	clean := func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_offers WHERE order_id LIKE 'it-dsp-%' OR driver_id LIKE 'au-%'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE id LIKE 'it-dsp-%'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM restaurants WHERE id LIKE 'it-dsp-%'`)
	}
	clean()
	t.Cleanup(clean) // runs before authDB's, which removes the accounts these refer to
	for _, r := range []struct {
		id  string
		lat any
		lng any
	}{{dspRest, dspLat, dspLng}, {"it-dsp-nowhere", nil, nil}} {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO restaurants (id, name, active, latitude, longitude) VALUES ($1, $1, true, $2, $3)`, r.id, r.lat, r.lng); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func dspOrderAt(t *testing.T, db *DBAdapter, id, restaurant string, state domain.OrderState) {
	t.Helper()
	if _, err := db.CreateOrder(context.Background(), domain.Order{
		ID: id, PaymentType: domain.PaymentCash, CustomerID: "au-dsp-customer", RestaurantID: restaurant, State: state,
		Items: []domain.OrderItem{{SKU: "S1", Name: "Burger", Quantity: 1, UnitPriceCents: 500}},
	}); err != nil {
		t.Fatalf("CreateOrder(%s): %v", id, err)
	}
}

// dspOrder makes an order at the placed restaurant.
func dspOrder(t *testing.T, db *DBAdapter, id string, state domain.OrderState) {
	t.Helper()
	dspOrderAt(t, db, id, dspRest, state)
}

// dspDriver makes a driver who is online, km kilometres north of the restaurant.
func dspDriver(t *testing.T, db *DBAdapter, name string, km float64) domain.User {
	t.Helper()
	ctx := context.Background()
	u := driverUser(t, db, name)
	if _, err := db.SetOnline(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetLocation(ctx, u.ID, dspLat+km/111.195, dspLng); err != nil {
		t.Fatal(err)
	}
	return u
}

func mustOffer(t *testing.T, db *DBAdapter, orderID, driverID string, lease time.Duration) domain.Offer {
	t.Helper()
	o, ok, err := db.OfferOrder(context.Background(), orderID, driverID, lease)
	if err != nil || !ok {
		t.Fatalf("OfferOrder(%s, %s) = %v, %v; want an offer", orderID, driverID, ok, err)
	}
	return o
}

func offerStatuses(t *testing.T, db *DBAdapter, orderID string) string {
	t.Helper()
	offers, err := db.ListOffers(context.Background(), orderID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(offers))
	for i, o := range offers {
		out[i] = string(o.Status)
	}
	return strings.Join(out, ",")
}

func candidateIDs(t *testing.T, db *DBAdapter, orderID string, age time.Duration) []string {
	t.Helper()
	cs, err := db.ListCandidates(context.Background(), orderID, age)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, c := range cs {
		out = append(out, c.DriverID)
	}
	return out
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		seen[w]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func assignTo2(t *testing.T, db *DBAdapter, orderID, driverID string) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), `UPDATE orders SET assigned_driver_id = $2 WHERE id = $1`, orderID, driverID); err != nil {
		t.Fatal(err)
	}
}

// ---- making an offer ------------------------------------------------------------------

// A driver's countdown is the database's own count of what is left, not a time of day to compare with another clock.
func TestTheTimeLeftOnAnOfferIsTheDatabasesCount(t *testing.T) {
	db := dispatchDB(t)
	dspOrder(t, db, "it-dsp-left", domain.OrderPreparing)
	d := dspDriver(t, db, "dsp-left", 1)
	mustOffer(t, db, "it-dsp-left", d.ID, 90*time.Second)
	got, ok, err := db.OpenOffer(context.Background(), d.ID)
	if err != nil || !ok {
		t.Fatalf("OpenOffer = %v, %v", ok, err)
	}
	if got.RemainingSeconds < 89 || got.RemainingSeconds > 90 {
		t.Errorf("RemainingSeconds = %v, want just under 90", got.RemainingSeconds)
	}
}

func TestAnOfferIsALeaseTakenWithTheDatabaseClock(t *testing.T) {
	db := dispatchDB(t)
	dspOrder(t, db, "it-dsp-lease", domain.OrderPreparing)
	d := dspDriver(t, db, "dsp-lease", 1)

	offer, ok, err := db.OfferOrder(context.Background(), "it-dsp-lease", d.ID, 60*time.Second)
	if err != nil || !ok {
		t.Fatalf("OfferOrder = %v, %v", ok, err)
	}
	if offer.ID == 0 || offer.OrderID != "it-dsp-lease" || offer.DriverID != d.ID || offer.Status != domain.OfferOpen || offer.EndedAt != nil {
		t.Errorf("offer = %+v", offer)
	}
	if lease := offer.ExpiresAt.Sub(offer.OfferedAt); lease != 60*time.Second {
		t.Errorf("lease = %v, want exactly 60s from the moment it was offered", lease)
	}
	// "now" is the database's, not this computer's: the two clocks need not agree, which is why a lease is
	// taken with the database's (the Docker VM's clock here is a couple of seconds from the host's)
	var dbNow time.Time
	if err := db.Pool.QueryRow(context.Background(), `SELECT now()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	if d := dbNow.Sub(offer.OfferedAt); d < 0 || d > 5*time.Second {
		t.Errorf("offered at %v, the database says it is %v now", offer.OfferedAt, dbNow)
	}
	if _, _, err := db.OfferOrder(context.Background(), "it-dsp-lease", d.ID, 0); err == nil {
		t.Error("a lease of zero was accepted")
	}
}

func TestAnOfferIsRefusedWhenSomethingHasChanged(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	d := dspDriver(t, db, "dsp-ref", 1)
	staff := staffUser(t, db, "dsp-ref-staff")
	offline := driverUser(t, db, "dsp-ref-offline") // never been online: it has no row at all
	left := dspDriver(t, db, "dsp-ref-left", 1)     // was online, and went offline: its row says so
	if _, err := db.SetOnline(context.Background(), left.ID, false); err != nil {
		t.Fatal(err)
	}
	disabled := dspDriver(t, db, "dsp-ref-disabled", 1)
	if _, err := db.Pool.Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id = $1`, disabled.ID); err != nil {
		t.Fatal(err)
	}
	busy := dspDriver(t, db, "dsp-ref-busy", 1)
	dspOrder(t, db, "it-dsp-theirs", domain.OrderPreparing)
	assignTo2(t, db, "it-dsp-theirs", busy.ID)

	for name, c := range map[string]struct {
		order  string
		state  domain.OrderState
		driver string
		driven bool // the order already has a driver
	}{
		"an order that is not being prepared": {"it-dsp-paid", domain.OrderPaid, d.ID, false},
		"an order that has a driver":          {"it-dsp-taken", domain.OrderPreparing, d.ID, true},
		"a driver who was never online":       {"it-dsp-off", domain.OrderPreparing, offline.ID, false},
		"a driver who went offline":           {"it-dsp-left", domain.OrderPreparing, left.ID, false},
		"a driver whose account is disabled":  {"it-dsp-dis", domain.OrderPreparing, disabled.ID, false},
		"a driver who is on a delivery":       {"it-dsp-busy", domain.OrderPreparing, busy.ID, false},
		"an account that is not a driver":     {"it-dsp-staff", domain.OrderPreparing, staff.ID, false},
		"nobody at all":                       {"it-dsp-nobody", domain.OrderPreparing, "au-nobody", false},
	} {
		dspOrder(t, db, c.order, c.state)
		if c.driven {
			assignTo2(t, db, c.order, "au-someone-else")
		}
		offer, ok, err := db.OfferOrder(ctx, c.order, c.driver, time.Minute)
		if err != nil || ok {
			t.Errorf("%s: OfferOrder = %+v, %v, %v; want no offer and no error", name, offer, ok, err)
		}
		if got := offerStatuses(t, db, c.order); got != "" {
			t.Errorf("%s: left offers %q", name, got)
		}
	}
	if _, ok, err := db.OfferOrder(ctx, "it-dsp-no-such-order", d.ID, time.Minute); ok || err != nil {
		t.Errorf("an order that does not exist: ok=%v err=%v, want neither", ok, err)
	}
}

func TestAnOrderHasOneOpenOfferAndADriverHasOne(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	a, b := dspDriver(t, db, "dsp-one-a", 1), dspDriver(t, db, "dsp-one-b", 2)
	dspOrder(t, db, "it-dsp-o1", domain.OrderPreparing)
	dspOrder(t, db, "it-dsp-o2", domain.OrderPreparing)

	mustOffer(t, db, "it-dsp-o1", a.ID, time.Minute)
	if _, ok, err := db.OfferOrder(ctx, "it-dsp-o1", b.ID, time.Minute); ok || err != nil {
		t.Errorf("a second driver for an order on offer: ok=%v err=%v, want it refused", ok, err)
	}
	if _, ok, err := db.OfferOrder(ctx, "it-dsp-o2", a.ID, time.Minute); ok || err != nil {
		t.Errorf("a second order for a driver with an offer: ok=%v err=%v, want it refused", ok, err)
	}
	mustOffer(t, db, "it-dsp-o2", b.ID, time.Minute) // the others are free to go on
}

// ---- who could be offered an order ------------------------------------------------------

func TestWhoCouldBeOfferedAnOrder(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()

	stale := dspDriver(t, db, "cand-stale", 1)
	time.Sleep(1300 * time.Millisecond) // the others are heard from after this
	good := dspDriver(t, db, "cand-good", 2)
	driverUser(t, db, "cand-offline")
	online := driverUser(t, db, "cand-nowhere") // online but has never sent a point
	if _, err := db.SetOnline(ctx, online.ID, true); err != nil {
		t.Fatal(err)
	}
	busy := dspDriver(t, db, "cand-busy", 1)
	dspOrder(t, db, "it-dsp-job", domain.OrderPreparing)
	assignTo2(t, db, "it-dsp-job", busy.ID)
	disabled := dspDriver(t, db, "cand-disabled", 1)
	if _, err := db.Pool.Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id = $1`, disabled.ID); err != nil {
		t.Fatal(err)
	}
	asked := dspDriver(t, db, "cand-asked", 1) // has an open offer for another order
	dspOrder(t, db, "it-dsp-other", domain.OrderPreparing)
	mustOffer(t, db, "it-dsp-other", asked.ID, time.Minute)
	refused := dspDriver(t, db, "cand-refused", 1) // declined this very order before
	dspOrder(t, db, "it-dsp-main", domain.OrderPreparing)
	offer := mustOffer(t, db, "it-dsp-main", refused.ID, time.Minute)
	if err := db.DeclineOffer(ctx, offer.ID, refused.ID); err != nil {
		t.Fatal(err)
	}
	staffUser(t, db, "cand-staff")
	_ = stale

	got := candidateIDs(t, db, "it-dsp-main", time.Second)
	if !sameSet(got, []string{good.ID}) {
		t.Errorf("candidates = %v, want only %v (not the stale, offline, unlocated, busy, disabled, already asked or already refused)", got, good.ID)
	}

	// the driver who declined this order is a candidate for the next one; and a driver heard from a while ago is with a longer limit
	dspOrder(t, db, "it-dsp-next", domain.OrderPreparing)
	got = candidateIDs(t, db, "it-dsp-next", time.Minute)
	if !sameSet(got, []string{good.ID, refused.ID, stale.ID}) {
		t.Errorf("candidates for another order = %v, want the good, the one who declined the other, and the not so recent", got)
	}

	cs, _ := db.ListCandidates(ctx, "it-dsp-next", time.Minute)
	for _, c := range cs {
		if c.IdleSince.IsZero() || c.LocationAt.IsZero() || c.Latitude == 0 {
			t.Errorf("candidate %+v is missing what dispatch needs to rank it", c)
		}
	}
	if _, err := db.ListCandidates(ctx, "it-dsp-next", 0); err == nil {
		t.Error("an age limit of zero was accepted")
	}
}

func TestWhichOrdersNeedADriver(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	d := dspDriver(t, db, "wait-a", 1)

	dspOrder(t, db, "it-dsp-w1", domain.OrderPreparing)
	time.Sleep(15 * time.Millisecond)
	dspOrder(t, db, "it-dsp-w2", domain.OrderPreparing)
	dspOrder(t, db, "it-dsp-assigned", domain.OrderPreparing)
	assignTo2(t, db, "it-dsp-assigned", "au-someone")
	dspOrder(t, db, "it-dsp-paid", domain.OrderPaid)
	dspOrder(t, db, "it-dsp-picked", domain.OrderPickedUp)
	dspOrderAt(t, db, "it-dsp-unplaced", "it-dsp-nowhere", domain.OrderPreparing)
	dspOrder(t, db, "it-dsp-offered", domain.OrderPreparing)
	mustOffer(t, db, "it-dsp-offered", d.ID, time.Minute)

	ours := func(limit int) []string {
		ws, err := db.ListWaitingOrders(ctx, limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, w := range ws {
			if strings.HasPrefix(w.OrderID, "it-dsp-") {
				out = append(out, w.OrderID)
				if w.RestaurantID != dspRest || w.Latitude != dspLat || w.Longitude != dspLng || w.Since.IsZero() {
					t.Errorf("waiting order %+v is missing where the restaurant is", w)
				}
			}
		}
		return out
	}
	if got := ours(1000); fmt.Sprint(got) != "[it-dsp-w1 it-dsp-w2]" {
		t.Errorf("waiting = %v, want only the two that are being prepared with no driver and no offer, oldest first", got)
	}
	if got := ours(1); len(got) > 1 {
		t.Errorf("a limit of 1 gave %v", got)
	}
	if _, err := db.ListWaitingOrders(ctx, 0); err == nil {
		t.Error("a limit of zero was accepted")
	}
}

// ---- answering an offer -------------------------------------------------------------------

func TestAcceptingAnOfferSetsTheDriverAndTheOutcomeTogether(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	dspOrder(t, db, "it-dsp-acc", domain.OrderPreparing)
	d := dspDriver(t, db, "dsp-acc", 1)
	offer := mustOffer(t, db, "it-dsp-acc", d.ID, time.Minute)

	o, err := db.AcceptOffer(ctx, offer.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.ID != "it-dsp-acc" || o.AssignedDriverID != d.ID || o.State != domain.OrderPreparing {
		t.Errorf("order = %+v, want the driver set and the order still being prepared", o)
	}
	stored, _ := db.GetOrderbyId(ctx, "it-dsp-acc")
	if stored.AssignedDriverID != d.ID || !stored.UpdatedAt.After(stored.CreatedAt) {
		t.Errorf("stored %+v, want the driver set and the order marked as changed", stored)
	}
	offers, _ := db.ListOffers(ctx, "it-dsp-acc")
	if len(offers) != 1 || offers[0].Status != domain.OfferAccepted || offers[0].EndedAt == nil {
		t.Errorf("offers = %+v, want one accepted offer with an end time", offers)
	}
	if _, ok, _ := db.OpenOffer(ctx, d.ID); ok {
		t.Error("the driver still has an open offer after accepting it")
	}
	got, _ := db.GetDriver(ctx, d.ID)
	if !got.Busy {
		t.Error("the driver is not busy with the delivery they accepted")
	}
}

func TestAnOfferThatIsNotThereToAnswerIsGone(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	d := dspDriver(t, db, "gone-a", 1)
	other := dspDriver(t, db, "gone-b", 2)

	gone := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ports.ErrOfferGone) {
			t.Errorf("%s: error = %v, want ErrOfferGone", what, err)
		}
	}
	_, err := db.AcceptOffer(ctx, 987654321, d.ID)
	gone("an offer that never existed", err)
	gone("declining an offer that never existed", db.DeclineOffer(ctx, 987654321, d.ID))

	// someone else's offer: told exactly what a missing one is told
	dspOrder(t, db, "it-dsp-g1", domain.OrderPreparing)
	offer := mustOffer(t, db, "it-dsp-g1", d.ID, time.Minute)
	_, err = db.AcceptOffer(ctx, offer.ID, other.ID)
	gone("accepting another driver's offer", err)
	gone("declining another driver's offer", db.DeclineOffer(ctx, offer.ID, other.ID))

	// already answered
	if _, err := db.AcceptOffer(ctx, offer.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	_, err = db.AcceptOffer(ctx, offer.ID, d.ID)
	gone("accepting twice", err)
	gone("declining after accepting", db.DeclineOffer(ctx, offer.ID, d.ID))

	dspOrder(t, db, "it-dsp-g2", domain.OrderPreparing)
	offer2 := mustOffer(t, db, "it-dsp-g2", other.ID, time.Minute)
	if err := db.DeclineOffer(ctx, offer2.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	_, err = db.AcceptOffer(ctx, offer2.ID, other.ID)
	gone("accepting after declining", err)
	gone("declining twice", db.DeclineOffer(ctx, offer2.ID, other.ID))
}

func TestALostLeaseIsNeverCompletedByTheDriverWhoHeldIt(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	dspOrder(t, db, "it-dsp-lost", domain.OrderPreparing)
	slow := dspDriver(t, db, "lost-slow", 1)
	next := dspDriver(t, db, "lost-next", 2)

	offer := mustOffer(t, db, "it-dsp-lost", slow.ID, 300*time.Millisecond)
	time.Sleep(450 * time.Millisecond)

	// the lease has run out, though nothing has ended the offer yet: the driver still cannot take it
	if _, err := db.AcceptOffer(ctx, offer.ID, slow.ID); !errors.Is(err, ports.ErrOfferGone) {
		t.Fatalf("accepting after the lease ran out: error = %v, want ErrOfferGone", err)
	}
	if err := db.DeclineOffer(ctx, offer.ID, slow.ID); !errors.Is(err, ports.ErrOfferGone) {
		t.Errorf("declining after the lease ran out: error = %v, want ErrOfferGone", err)
	}
	if _, ok, _ := db.OpenOffer(ctx, slow.ID); ok {
		t.Error("a lapsed offer is still shown to the driver")
	}
	// ...and the order cannot be offered to anyone else until the lapse is recorded
	if _, ok, _ := db.OfferOrder(ctx, "it-dsp-lost", next.ID, time.Minute); ok {
		t.Error("the order was offered again before the lapsed offer was ended")
	}

	if n, err := db.ExpireOffers(ctx); err != nil || n != 1 {
		t.Fatalf("ExpireOffers = %d, %v, want the one lapsed offer", n, err)
	}
	if got := offerStatuses(t, db, "it-dsp-lost"); got != "expired" {
		t.Errorf("history = %q, want expired", got)
	}
	mustOffer(t, db, "it-dsp-lost", next.ID, time.Minute) // now it is free to go to the next driver
	if _, err := db.AcceptOffer(ctx, offer.ID, slow.ID); !errors.Is(err, ports.ErrOfferGone) {
		t.Errorf("the old holder accepting after the order moved on: error = %v", err)
	}
	o, err := db.AcceptOffer(ctx, mustOpenOfferID(t, db, next.ID), next.ID)
	if err != nil || o.AssignedDriverID != next.ID {
		t.Fatalf("the next driver accepting: %+v, %v", o, err)
	}
	if got := offerStatuses(t, db, "it-dsp-lost"); got != "expired,accepted" {
		t.Errorf("history = %q, want expired,accepted", got)
	}
}

func mustOpenOfferID(t *testing.T, db *DBAdapter, driverID string) int64 {
	t.Helper()
	o, ok, err := db.OpenOffer(context.Background(), driverID)
	if err != nil || !ok {
		t.Fatalf("OpenOffer(%s) = %v, %v; want one", driverID, ok, err)
	}
	return o.ID
}

func TestAcceptingAnOrderThatWasCancelledSetsNoDriver(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	dspOrder(t, db, "it-dsp-cx", domain.OrderPreparing)
	d := dspDriver(t, db, "dsp-cx", 1)
	offer := mustOffer(t, db, "it-dsp-cx", d.ID, time.Minute)
	if _, err := db.UpdateOrderState(ctx, "it-dsp-cx", string(domain.OrderPreparing), string(domain.OrderCancelled)); err != nil {
		t.Fatal(err)
	}

	if _, err := db.AcceptOffer(ctx, offer.ID, d.ID); !errors.Is(err, ports.ErrOfferGone) {
		t.Fatalf("error = %v, want ErrOfferGone", err)
	}
	stored, _ := db.GetOrderbyId(ctx, "it-dsp-cx")
	if stored.AssignedDriverID != "" || stored.State != domain.OrderCancelled {
		t.Errorf("a refused accept changed the order: %+v", stored)
	}
	// nothing of the accept was kept: the offer was not marked accepted, so the next round ends it as lapsed
	if got := offerStatuses(t, db, "it-dsp-cx"); got != "offered" {
		t.Errorf("history = %q, want the offer left open (the accept was rolled back whole)", got)
	}
	if n, _ := db.ExpireOffers(ctx); n != 1 {
		t.Errorf("ExpireOffers ended %d offers, want the one for the cancelled order", n)
	}
	if got := offerStatuses(t, db, "it-dsp-cx"); got != "expired" {
		t.Errorf("history = %q, want expired", got)
	}
}

func TestADriverWhoAlreadyHoldsADeliveryCannotAcceptAnother(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	dspOrder(t, db, "it-dsp-first", domain.OrderPreparing)
	dspOrder(t, db, "it-dsp-second", domain.OrderPreparing)
	d := dspDriver(t, db, "dsp-twice", 1)
	offer := mustOffer(t, db, "it-dsp-second", d.ID, time.Minute) // free at the time
	assignTo2(t, db, "it-dsp-first", d.ID)                        // then given another order some other way

	if _, err := db.AcceptOffer(ctx, offer.ID, d.ID); !errors.Is(err, ports.ErrDriverBusy) {
		t.Fatalf("error = %v, want ErrDriverBusy", err)
	}
	stored, _ := db.GetOrderbyId(ctx, "it-dsp-second")
	if stored.AssignedDriverID != "" {
		t.Errorf("the second order was given to a driver who is busy: %+v", stored)
	}
	if got := offerStatuses(t, db, "it-dsp-second"); got != "offered" {
		t.Errorf("history = %q, want the offer left as it was", got)
	}
}

func TestDecliningLetsTheOrderGoToSomeoneElse(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	dspOrder(t, db, "it-dsp-dec", domain.OrderPreparing)
	a, b := dspDriver(t, db, "dec-a", 1), dspDriver(t, db, "dec-b", 2)
	offer := mustOffer(t, db, "it-dsp-dec", a.ID, time.Minute)

	if err := db.DeclineOffer(ctx, offer.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if got := candidateIDs(t, db, "it-dsp-dec", time.Minute); !sameSet(got, []string{b.ID}) {
		t.Errorf("candidates = %v, want only the other driver: one who said no is not asked again", got)
	}
	mustOffer(t, db, "it-dsp-dec", b.ID, time.Minute)
	if got := offerStatuses(t, db, "it-dsp-dec"); got != "declined,offered" {
		t.Errorf("history = %q, want declined,offered", got)
	}
	// the driver who declined is free for other orders at once
	dspOrder(t, db, "it-dsp-dec2", domain.OrderPreparing)
	mustOffer(t, db, "it-dsp-dec2", a.ID, time.Minute)
}

func TestTheOpenOfferAndTheHistory(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	dspOrder(t, db, "it-dsp-hist", domain.OrderPreparing)
	a, b := dspDriver(t, db, "hist-a", 1), dspDriver(t, db, "hist-b", 2)

	if _, ok, err := db.OpenOffer(ctx, a.ID); ok || err != nil {
		t.Errorf("OpenOffer with none = %v, %v", ok, err)
	}
	offer := mustOffer(t, db, "it-dsp-hist", a.ID, 200*time.Millisecond)
	if got, ok, err := db.OpenOffer(ctx, a.ID); err != nil || !ok || got.ID != offer.ID {
		t.Errorf("OpenOffer = %+v, %v, %v; want the offer", got, ok, err)
	} else if got.RemainingSeconds <= 0 || got.RemainingSeconds > 0.2 {
		t.Errorf("RemainingSeconds = %v, want what is left of a lease of 0.2 s", got.RemainingSeconds)
	}
	if _, ok, _ := db.OpenOffer(ctx, b.ID); ok {
		t.Error("another driver sees the offer")
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := db.ExpireOffers(ctx); err != nil {
		t.Fatal(err)
	}
	if err := func() error {
		o := mustOffer(t, db, "it-dsp-hist", b.ID, time.Minute)
		return db.DeclineOffer(ctx, o.ID, b.ID)
	}(); err != nil {
		t.Fatal(err)
	}
	offers, err := db.ListOffers(ctx, "it-dsp-hist")
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 2 || offers[0].DriverID != a.ID || offers[0].Status != domain.OfferExpired || offers[1].DriverID != b.ID || offers[1].Status != domain.OfferDeclined {
		t.Fatalf("history = %+v, want a's offer expired then b's declined, oldest first", offers)
	}
	for _, o := range offers {
		if o.EndedAt == nil || o.EndedAt.Before(o.OfferedAt) {
			t.Errorf("offer %+v has no sensible end time", o)
		}
	}
	if none, err := db.ListOffers(ctx, "it-dsp-no-offers"); err != nil || len(none) != 0 {
		t.Errorf("an order with no offers = %v, %v", none, err)
	}
}

// ---- ending offers ------------------------------------------------------------------------

func TestOffersEndWhenTheirOrderNoLongerNeedsADriverOrTheDriverLeaves(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	cancelled, taken, left, fine := dspDriver(t, db, "end-cx", 1), dspDriver(t, db, "end-taken", 1), dspDriver(t, db, "end-left", 1), dspDriver(t, db, "end-fine", 1)
	for _, id := range []string{"it-dsp-e1", "it-dsp-e2", "it-dsp-e3", "it-dsp-e4"} {
		dspOrder(t, db, id, domain.OrderPreparing)
	}
	mustOffer(t, db, "it-dsp-e1", cancelled.ID, time.Hour)
	mustOffer(t, db, "it-dsp-e2", taken.ID, time.Hour)
	mustOffer(t, db, "it-dsp-e3", left.ID, time.Hour)
	mustOffer(t, db, "it-dsp-e4", fine.ID, time.Hour)

	if _, err := db.UpdateOrderState(ctx, "it-dsp-e1", string(domain.OrderPreparing), string(domain.OrderCancelled)); err != nil {
		t.Fatal(err)
	}
	assignTo2(t, db, "it-dsp-e2", "au-staff-chose")
	if n, err := db.ExpireOffers(ctx); err != nil || n != 2 {
		t.Fatalf("ExpireOffers = %d, %v, want the cancelled order's and the already-assigned order's offers", n, err)
	}
	for id, want := range map[string]string{"it-dsp-e1": "expired", "it-dsp-e2": "expired", "it-dsp-e4": "offered"} {
		if got := offerStatuses(t, db, id); got != want {
			t.Errorf("%s: history %q, want %q", id, got, want)
		}
	}

	// a driver who goes offline gives up the offer they hold, and their point is forgotten with it
	d, err := db.SetOnline(ctx, left.ID, false)
	if err != nil || d.Online || d.Located() {
		t.Fatalf("SetOnline(false) = %+v, %v", d, err)
	}
	if got := offerStatuses(t, db, "it-dsp-e3"); got != "expired" {
		t.Errorf("the offer of a driver who went offline is %q, want expired", got)
	}
	for _, id := range candidateIDs(t, db, "it-dsp-e3", time.Hour) {
		if id == left.ID {
			t.Error("a driver who went offline can still be offered orders")
		}
	}
	if n, err := db.ExpireOffers(ctx); err != nil || n != 0 {
		t.Errorf("a second ExpireOffers ended %d, %v; want nothing left to end", n, err)
	}
}

func TestAnOfferIsAHistoryThatCanOnlyBeEndedOnce(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	dspOrder(t, db, "it-dsp-h1", domain.OrderPreparing)
	d, other := dspDriver(t, db, "hist-d", 1), dspDriver(t, db, "hist-o", 2)
	offer := mustOffer(t, db, "it-dsp-h1", d.ID, time.Minute)

	for name, sql := range map[string]string{
		"extending the lease":          `UPDATE order_offers SET expires_at = expires_at + interval '1 hour' WHERE id = $1`,
		"moving it to another driver":  `UPDATE order_offers SET driver_id = '` + other.ID + `' WHERE id = $1`,
		"moving it to another order":   `UPDATE order_offers SET order_id = 'it-dsp-h1-other' WHERE id = $1`,
		"ending it without saying so":  `UPDATE order_offers SET ended_at = now() WHERE id = $1`,
		"opening it again":             `UPDATE order_offers SET status = 'offered' WHERE id = $1`,
		"a status that does not exist": `UPDATE order_offers SET status = 'maybe', ended_at = now() WHERE id = $1`,
		"changing when it was offered": `UPDATE order_offers SET offered_at = offered_at - interval '1 day', status = 'declined', ended_at = now() WHERE id = $1`,
	} {
		if _, err := db.Pool.Exec(ctx, sql, offer.ID); err == nil {
			t.Errorf("%s was allowed", name)
		}
	}
	if got := offerStatuses(t, db, "it-dsp-h1"); got != "offered" {
		t.Errorf("history = %q after the refused changes, want it untouched", got)
	}

	// it can be ended exactly once, and then never touched again
	if _, err := db.Pool.Exec(ctx, `UPDATE order_offers SET status = 'declined', ended_at = now() WHERE id = $1`, offer.ID); err != nil {
		t.Fatalf("ending an open offer: %v", err)
	}
	for name, sql := range map[string]string{
		"ending it again":   `UPDATE order_offers SET status = 'accepted', ended_at = now() WHERE id = $1`,
		"reopening it":      `UPDATE order_offers SET status = 'offered', ended_at = NULL WHERE id = $1`,
		"changing its time": `UPDATE order_offers SET ended_at = now() + interval '1 day' WHERE id = $1`,
	} {
		if _, err := db.Pool.Exec(ctx, sql, offer.ID); err == nil {
			t.Errorf("%s was allowed on an offer that had ended", name)
		}
	}
}

func TestTheDatabaseItselfKeepsOneOpenOfferPerOrderAndPerDriverAndOneJobPerDriver(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	a, b := dspDriver(t, db, "uniq-a", 1), dspDriver(t, db, "uniq-b", 2)
	dspOrder(t, db, "it-dsp-u1", domain.OrderPreparing)
	dspOrder(t, db, "it-dsp-u2", domain.OrderPreparing)
	mustOffer(t, db, "it-dsp-u1", a.ID, time.Minute)

	insert := `INSERT INTO order_offers (order_id, driver_id, status, expires_at) VALUES ($1, $2, 'offered', now() + interval '1 minute')`
	if _, err := db.Pool.Exec(ctx, insert, "it-dsp-u1", b.ID); err == nil {
		t.Error("two open offers for one order were allowed")
	}
	if _, err := db.Pool.Exec(ctx, insert, "it-dsp-u2", a.ID); err == nil {
		t.Error("two open offers for one driver were allowed")
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO order_offers (order_id, driver_id, status, expires_at) VALUES ('it-dsp-u2', $1, 'declined', now())`, b.ID); err == nil {
		t.Error("an ended offer with no end time was allowed")
	}

	assignTo2(t, db, "it-dsp-u1", b.ID)
	if _, err := db.Pool.Exec(ctx, `UPDATE orders SET assigned_driver_id = $2 WHERE id = $1`, "it-dsp-u2", b.ID); err == nil {
		t.Error("one driver was given two unfinished deliveries")
	}
	// a finished delivery does not count against them
	if _, err := db.Pool.Exec(ctx, `UPDATE orders SET state = 'delivered' WHERE id = 'it-dsp-u1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE orders SET assigned_driver_id = $2 WHERE id = $1`, "it-dsp-u2", b.ID); err != nil {
		t.Errorf("a driver with only a finished delivery was refused another: %v", err)
	}
}

// ---- one driver, one thing at a time -----------------------------------------------------

// lockDriver holds the driver's row the way an accept in progress does, until the returned function commits.
func lockDriver(t *testing.T, db *DBAdapter, driverID string) (release func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// If the test fails before it releases the lock, the lock must still go: the cleanup that follows deletes
	// this driver, and would wait on it for ever.
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, `SELECT 1 FROM drivers WHERE user_id = $1 FOR UPDATE`, driverID); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := tx.Commit(ctx); err != nil {
			t.Error(err)
		}
	}
}

// Found by the system test in dispatch_pg_test.go: an offer to a driver read "holds no delivery" just before
// that driver's accept committed, and a driver on a delivery was handed another order to hold for the whole
// lease. Offering and accepting for one driver now take turns, and the check that they are free is made after
// the turn is granted. This holds an accept "in progress" so the order of events is the same every time.
func TestAnOfferWaitsForADriverWhoIsAcceptingAndThenSeesThemBusy(t *testing.T) {
	db := dispatchDB(t)
	dspOrder(t, db, "it-dsp-first", domain.OrderPreparing)
	dspOrder(t, db, "it-dsp-second", domain.OrderPreparing)
	d := dspDriver(t, db, "dsp-turns", 1)

	release := lockDriver(t, db, d.ID) // the driver is in the middle of accepting the first order
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, ok, err := db.OfferOrder(context.Background(), "it-dsp-second", d.ID, time.Minute)
		done <- result{ok, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("the offer did not wait for the driver's accept: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	assignTo2(t, db, "it-dsp-first", d.ID) // the accept's work...
	release()                              // ...is committed

	select {
	case r := <-done:
		if r.err != nil || r.ok {
			t.Errorf("OfferOrder = %v, %v; want it refused, the driver now holds a delivery", r.ok, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the offer never finished")
	}
	if got := offerStatuses(t, db, "it-dsp-second"); got != "" {
		t.Errorf("a busy driver was offered an order: %q", got)
	}
}

// A lease is judged when the accept happens, not when it began waiting its turn: a driver whose lease ran out
// while they waited cannot complete it.
func TestALeaseThatRunsOutWhileAnAcceptWaitsItsTurnIsNotCompleted(t *testing.T) {
	db := dispatchDB(t)
	dspOrder(t, db, "it-dsp-wait", domain.OrderPreparing)
	d := dspDriver(t, db, "dsp-wait", 1)
	offer := mustOffer(t, db, "it-dsp-wait", d.ID, 300*time.Millisecond)

	release := lockDriver(t, db, d.ID)
	done := make(chan error, 1)
	go func() {
		_, err := db.AcceptOffer(context.Background(), offer.ID, d.ID)
		done <- err
	}()
	time.Sleep(600 * time.Millisecond) // the lease runs out while the accept is waiting
	release()

	select {
	case err := <-done:
		if !errors.Is(err, ports.ErrOfferGone) {
			t.Errorf("accepting a lease that ran out while waiting: error = %v, want ErrOfferGone", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the accept never finished")
	}
	stored, _ := db.GetOrderbyId(context.Background(), "it-dsp-wait")
	if stored.AssignedDriverID != "" {
		t.Errorf("the order was given to a driver whose lease had run out: %+v", stored)
	}
}

// ---- many at once -------------------------------------------------------------------------

// A crowd of drivers offered the same order at the same moment: exactly one offer is made.
func TestManyDriversOfferedOneOrderAtOnceGetExactlyOneOffer(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	dspOrder(t, db, "it-dsp-race1", domain.OrderPreparing)
	const racers = 20
	drivers := make([]domain.User, racers)
	for i := range racers {
		drivers[i] = dspDriver(t, db, fmt.Sprintf("race1-%02d", i), float64(i)/10+0.1)
	}

	made := make([]bool, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			_, made[i], errs[i] = db.OfferOrder(ctx, "it-dsp-race1", drivers[i].ID, time.Minute)
		})
	}
	close(start)
	wg.Wait()

	n := 0
	for i := range racers {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if made[i] {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d offers were made for one order, want exactly 1", n)
	}
	if got := offerStatuses(t, db, "it-dsp-race1"); got != "offered" {
		t.Errorf("history = %q, want one open offer", got)
	}
}

// A crowd of orders offered to the same driver at the same moment: exactly one is taken.
func TestManyOrdersOfferedToOneDriverAtOnceGetExactlyOneOffer(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	d := dspDriver(t, db, "race2", 1)
	const racers = 20
	for i := range racers {
		dspOrder(t, db, fmt.Sprintf("it-dsp-race2-%02d", i), domain.OrderPreparing)
	}
	made := make([]bool, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			_, made[i], errs[i] = db.OfferOrder(ctx, fmt.Sprintf("it-dsp-race2-%02d", i), d.ID, time.Minute)
		})
	}
	close(start)
	wg.Wait()

	n := 0
	for i := range racers {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if made[i] {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d orders were offered to one driver, want exactly 1", n)
	}
}

// The same offer accepted by many requests at once (a double tap, a retry): one wins, the rest are told it is gone.
func TestManyAcceptsOfOneOfferHaveExactlyOneWinner(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	dspOrder(t, db, "it-dsp-race3", domain.OrderPreparing)
	d := dspDriver(t, db, "race3", 1)
	offer := mustOffer(t, db, "it-dsp-race3", d.ID, time.Minute)

	const racers = 20
	errs := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			_, errs[i] = db.AcceptOffer(ctx, offer.ID, d.ID)
		})
	}
	close(start)
	wg.Wait()

	wins, gone := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ports.ErrOfferGone):
			gone++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 || gone != racers-1 {
		t.Errorf("wins=%d gone=%d, want exactly 1 and %d", wins, gone, racers-1)
	}
	if got := offerStatuses(t, db, "it-dsp-race3"); got != "accepted" {
		t.Errorf("history = %q", got)
	}
}

// An accept and the end of the lease at the same moment: whichever the database saw first wins, and the order
// never ends up with both a driver and an expired offer.
func TestAnAcceptRacingTheEndOfTheLeaseNeverLeavesBoth(t *testing.T) {
	db := dispatchDB(t)
	ctx := context.Background()
	for round := range 15 {
		order := fmt.Sprintf("it-dsp-race4-%02d", round)
		dspOrder(t, db, order, domain.OrderPreparing)
		d := dspDriver(t, db, fmt.Sprintf("race4-%02d", round), 1)
		offer := mustOffer(t, db, order, d.ID, 40*time.Millisecond)
		time.Sleep(time.Duration(round%5) * 10 * time.Millisecond) // sometimes before the lease ends, sometimes after

		var accepted error
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Go(func() { <-start; _, accepted = db.AcceptOffer(ctx, offer.ID, d.ID) })
		wg.Go(func() { <-start; _, _ = db.ExpireOffers(ctx) })
		close(start)
		wg.Wait()

		stored, _ := db.GetOrderbyId(ctx, order)
		status := offerStatuses(t, db, order)
		switch {
		case accepted == nil && (stored.AssignedDriverID != d.ID || status != "accepted"):
			t.Fatalf("round %d: the accept won but the order is %+v with offers %q", round, stored, status)
		case accepted != nil && !errors.Is(accepted, ports.ErrOfferGone):
			t.Fatalf("round %d: unexpected error %v", round, accepted)
		case accepted != nil && stored.AssignedDriverID != "":
			t.Fatalf("round %d: the accept failed but the order has driver %q (offers %q)", round, stored.AssignedDriverID, status)
		case accepted != nil && status == "accepted":
			t.Fatalf("round %d: the accept failed but the offer says accepted", round)
		}
	}
}
