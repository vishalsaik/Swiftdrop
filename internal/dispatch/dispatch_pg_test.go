package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"swiftdrop"
	"swiftdrop/internal/databases/adapters"
	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/dispatch"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
)

// The dispatch service against a real database, as several servers would run it. Dispatch looks at every
// waiting order and every online driver, so these tests make a database of their own (and drop it after):
// other tests that share the test database, running at the same moment in other packages, must neither be
// handed these drivers nor hand these tests theirs. They are skipped unless SWIFTDROP_TEST_DSN is set.

const restLat, restLng = 12.9719, 77.6412

func isolatedDB(t *testing.T) *adapters.DBAdapter {
	t.Helper()
	dsn := os.Getenv("SWIFTDROP_TEST_DSN")
	if dsn == "" {
		t.Skip("SWIFTDROP_TEST_DSN not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	name := fmt.Sprintf("sd_dispatch_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close(ctx)
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := adapters.NewDatabaseConnection(u.String())
	if err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}
	t.Cleanup(db.Pool.Close) // runs before the drop above
	if err := db.Migrate(ctx, swiftdrop.Schema); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO restaurants (id, name, active, latitude, longitude) VALUES ('rest', 'Rest', true, $1, $2)`, restLat, restLng); err != nil {
		t.Fatal(err)
	}
	return db
}

func addDriver(t *testing.T, db *adapters.DBAdapter, id string, km float64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.CreateUser(ctx, domain.User{ID: id, Email: id + "@example.test", Name: "Driver " + id, Role: domain.RoleDriver}, "hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetOnline(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetLocation(ctx, id, restLat+km/111.195, restLng); err != nil {
		t.Fatal(err)
	}
}

func addOrder(t *testing.T, db *adapters.DBAdapter, id string, state domain.OrderState) {
	t.Helper()
	if _, err := db.CreateOrder(context.Background(), domain.Order{
		ID: id, PaymentType: domain.PaymentCash, CustomerID: "cust", RestaurantID: "rest", State: state,
		Items: []domain.OrderItem{{SKU: "S", Name: "Burger", Quantity: 1, UnitPriceCents: 500}},
	}); err != nil {
		t.Fatal(err)
	}
}

func newService(db *adapters.DBAdapter, lease time.Duration) *dispatch.Service {
	s := dispatch.NewService(db)
	s.Lease = lease
	return s
}

func round(t *testing.T, s *dispatch.Service) dispatch.Report {
	t.Helper()
	rep, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func openOffer(t *testing.T, db *adapters.DBAdapter, driver string) (domain.Offer, bool) {
	t.Helper()
	o, ok, err := db.OpenOffer(context.Background(), driver)
	if err != nil {
		t.Fatal(err)
	}
	return o, ok
}

func history(t *testing.T, db *adapters.DBAdapter, order string) string {
	t.Helper()
	offers, err := db.ListOffers(context.Background(), order)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, o := range offers {
		out = append(out, o.DriverID+":"+string(o.Status))
	}
	return strings.Join(out, " ")
}

func TestAnOrderWalksDownTheDriversNearestFirstAndNewcomersGetTheirTurn(t *testing.T) {
	db := isolatedDB(t)
	ctx := context.Background()
	addDriver(t, db, "near", 1)
	addDriver(t, db, "mid", 3)
	addDriver(t, db, "far", 5)
	addOrder(t, db, "o1", domain.OrderPreparing)
	svc := newService(db, 600*time.Millisecond)

	if rep := round(t, svc); rep.Offered != 1 {
		t.Fatalf("round 1 = %+v, want the order offered", rep)
	}
	if o, ok := openOffer(t, db, "near"); !ok || o.OrderID != "o1" {
		t.Fatalf("the nearest driver has %+v, %v; want the offer", o, ok)
	}
	for _, other := range []string{"mid", "far"} {
		if _, ok := openOffer(t, db, other); ok {
			t.Errorf("%s was offered the order too", other)
		}
	}
	if rep := round(t, svc); rep.Offered != 0 || rep.NoDriver != 0 {
		t.Errorf("a second round while the offer is open = %+v, want nothing to do", rep)
	}

	// the nearest says no: the next nearest is asked
	o, _ := openOffer(t, db, "near")
	if err := db.DeclineOffer(ctx, o.ID, "near"); err != nil {
		t.Fatal(err)
	}
	if rep := round(t, svc); rep.Offered != 1 {
		t.Fatalf("after a decline = %+v", rep)
	}
	if _, ok := openOffer(t, db, "mid"); !ok {
		t.Fatal("the next nearest driver was not offered the order after a decline")
	}

	// the next nearest does not answer: the lease runs out and the one after is asked
	time.Sleep(800 * time.Millisecond)
	rep := round(t, svc)
	if rep.Expired != 1 || rep.Offered != 1 {
		t.Fatalf("after a lapse = %+v, want one lapsed and one offered", rep)
	}
	if _, ok := openOffer(t, db, "far"); !ok {
		t.Fatal("the third driver was not offered the order after the lapse")
	}

	// the last says no: nobody is left, and the order waits
	o, _ = openOffer(t, db, "far")
	if err := db.DeclineOffer(ctx, o.ID, "far"); err != nil {
		t.Fatal(err)
	}
	if rep := round(t, svc); rep.NoDriver != 1 || rep.Offered != 0 {
		t.Errorf("with every driver asked = %+v, want the order left waiting", rep)
	}

	// a driver who comes online later is asked at once
	addDriver(t, db, "late", 2)
	if rep := round(t, svc); rep.Offered != 1 {
		t.Fatalf("with a newcomer = %+v", rep)
	}
	o, ok := openOffer(t, db, "late")
	if !ok {
		t.Fatal("the newcomer was not offered the waiting order")
	}
	got, err := db.AcceptOffer(ctx, o.ID, "late")
	if err != nil || got.AssignedDriverID != "late" {
		t.Fatalf("accept = %+v, %v", got, err)
	}
	if rep := round(t, svc); rep != (dispatch.Report{}) {
		t.Errorf("a round after the order was taken = %+v, want nothing", rep)
	}
	want := "near:declined mid:expired far:declined late:accepted"
	if got := history(t, db, "o1"); got != want {
		t.Errorf("history = %q, want %q", got, want)
	}
}

func TestADriverOutsideTheRadiusIsNotAsked(t *testing.T) {
	db := isolatedDB(t)
	addDriver(t, db, "distant", 9)
	addOrder(t, db, "o1", domain.OrderPreparing)
	svc := newService(db, time.Minute)

	if rep := round(t, svc); rep.NoDriver != 1 || rep.Offered != 0 {
		t.Errorf("round = %+v, want no driver within 8 km", rep)
	}
	svc.RadiusKm = 10
	if rep := round(t, svc); rep.Offered != 1 {
		t.Errorf("with a radius of 10 km = %+v, want the order offered", rep)
	}
}

func TestOrdersAreOfferedOldestFirstAndADriverHoldsOneAtATime(t *testing.T) {
	db := isolatedDB(t)
	ctx := context.Background()
	addDriver(t, db, "solo", 1)
	addOrder(t, db, "old", domain.OrderPreparing)
	time.Sleep(20 * time.Millisecond)
	addOrder(t, db, "new", domain.OrderPreparing)
	svc := newService(db, time.Minute)

	if rep := round(t, svc); rep.Offered != 1 || rep.NoDriver != 1 {
		t.Fatalf("round = %+v, want one offered and one left waiting", rep)
	}
	o, ok := openOffer(t, db, "solo")
	if !ok || o.OrderID != "old" {
		t.Fatalf("the driver was offered %+v, want the order that has waited longest", o)
	}
	if _, err := db.AcceptOffer(ctx, o.ID, "solo"); err != nil {
		t.Fatal(err)
	}
	if rep := round(t, svc); rep.Offered != 0 {
		t.Errorf("a driver on a delivery was offered another: %+v", rep)
	}
	// finishing the delivery frees them
	for _, step := range [][2]domain.OrderState{{domain.OrderPreparing, domain.OrderPickedUp}, {domain.OrderPickedUp, domain.OrderDelivered}} {
		if _, err := db.UpdateOrderState(ctx, "old", string(step[0]), string(step[1])); err != nil {
			t.Fatal(err)
		}
	}
	if rep := round(t, svc); rep.Offered != 1 {
		t.Fatalf("after the delivery = %+v, want the waiting order offered", rep)
	}
	if o, ok := openOffer(t, db, "solo"); !ok || o.OrderID != "new" {
		t.Errorf("the driver was offered %+v, want the order that was waiting", o)
	}
}

func TestADriverWhoLeavesWithAnOfferHoldsNothingAndACancelledOrderIsLetGo(t *testing.T) {
	db := isolatedDB(t)
	ctx := context.Background()
	addDriver(t, db, "leaver", 1)
	addDriver(t, db, "stayer", 2)
	addOrder(t, db, "o1", domain.OrderPreparing)
	addOrder(t, db, "o2", domain.OrderPreparing)
	svc := newService(db, time.Minute)

	round(t, svc)
	if _, ok := openOffer(t, db, "leaver"); !ok {
		t.Fatal("setup: the nearest driver was not offered an order")
	}
	if _, err := db.SetOnline(ctx, "leaver", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := openOffer(t, db, "leaver"); ok {
		t.Fatal("a driver who went offline still holds an offer")
	}
	round(t, svc) // the order goes to the driver who stayed (they hold the other order's offer, so it waits)

	// cancel what the stayer was offered: the next round lets it go and offers them the other order
	staying, _ := openOffer(t, db, "stayer")
	if _, err := db.UpdateOrderState(ctx, staying.OrderID, string(domain.OrderPreparing), string(domain.OrderCancelled)); err != nil {
		t.Fatal(err)
	}
	rep := round(t, svc)
	if rep.Expired < 1 {
		t.Errorf("round = %+v, want the offer for the cancelled order ended", rep)
	}
	if o, ok := openOffer(t, db, "stayer"); !ok || o.OrderID == staying.OrderID {
		t.Errorf("the stayer has %+v, %v; want the other order", o, ok)
	}
}

// Several servers run dispatch at once, a crowd of orders needs drivers, and the drivers answer and deliver:
// every order is delivered exactly once, no order was ever offered to two drivers at the same time, no driver
// was ever offered two orders at the same time, and every delivery has the driver who accepted it.
func TestSeveralServersManyOrdersAndManyDriversNeverDoubleBook(t *testing.T) {
	db := isolatedDB(t)
	const orders, drivers, servers = 14, 5, 3
	for i := range orders {
		addOrder(t, db, fmt.Sprintf("o%02d", i), domain.OrderPreparing)
	}
	for i := range drivers {
		addDriver(t, db, fmt.Sprintf("d%d", i), 0.5+float64(i)*0.4)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var problems sync.Mutex
	var found []string
	problem := func(format string, args ...any) {
		problems.Lock()
		defer problems.Unlock()
		found = append(found, fmt.Sprintf(format, args...))
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range servers {
		svc := newService(db, 4*time.Second)
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := svc.RunOnce(ctx); err != nil && ctx.Err() == nil {
					problem("a round failed: %v", err)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}

	var delivered atomic.Int64
	for i := range drivers {
		id := fmt.Sprintf("d%d", i)
		declines := 0
		if i < 2 {
			declines = 1 // the first two drivers refuse the first thing they are offered
		}
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				offer, ok, err := db.OpenOffer(ctx, id)
				if err != nil && ctx.Err() == nil {
					problem("%s: reading the offer: %v", id, err)
				}
				if !ok {
					time.Sleep(5 * time.Millisecond)
					continue
				}
				if declines > 0 {
					declines--
					if err := db.DeclineOffer(ctx, offer.ID, id); err != nil && !errors.Is(err, ports.ErrOfferGone) {
						problem("%s: declining: %v", id, err)
					}
					continue
				}
				if _, err := db.AcceptOffer(ctx, offer.ID, id); err != nil {
					if !errors.Is(err, ports.ErrOfferGone) {
						problem("%s: accepting offer %d for %s: %v", id, offer.ID, offer.OrderID, err)
					}
					continue
				}
				for _, step := range [][2]domain.OrderState{{domain.OrderPreparing, domain.OrderPickedUp}, {domain.OrderPickedUp, domain.OrderDelivered}} {
					if _, err := db.UpdateOrderState(ctx, offer.OrderID, string(step[0]), string(step[1])); err != nil {
						problem("%s: moving %s from %s: %v", id, offer.OrderID, step[0], err)
					}
				}
				delivered.Add(1)
			}
		})
	}

	deadline := time.Now().Add(60 * time.Second)
	for delivered.Load() < orders && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	wg.Wait()

	for _, p := range found {
		t.Error(p)
	}
	if n := delivered.Load(); n != orders {
		t.Fatalf("%d of %d orders were delivered", n, orders)
	}

	type span struct {
		order, driver, status string
		from, to              time.Time
	}
	var all []span
	for i := range orders {
		id := fmt.Sprintf("o%02d", i)
		o, err := db.GetOrderbyId(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		offers, err := db.ListOffers(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		accepted := ""
		acceptedN := 0
		for _, of := range offers {
			if of.EndedAt == nil {
				t.Errorf("%s: an offer to %s is still open after every order was delivered", id, of.DriverID)
				continue
			}
			all = append(all, span{id, of.DriverID, string(of.Status), of.OfferedAt, *of.EndedAt})
			if of.Status == domain.OfferAccepted {
				accepted, acceptedN = of.DriverID, acceptedN+1
			}
		}
		if o.State != domain.OrderDelivered || acceptedN != 1 || o.AssignedDriverID != accepted {
			t.Errorf("%s: state %s, driver %q, %d accepted offers (%v); want delivered by the one driver who accepted it", id, o.State, o.AssignedDriverID, acceptedN, history(t, db, id))
		}
	}

	// no two offers for one order, or to one driver, were ever open at the same time
	overlap := func(key func(span) string, what string) {
		by := map[string][]span{}
		for _, s := range all {
			by[key(s)] = append(by[key(s)], s)
		}
		for k, spans := range by {
			sort.Slice(spans, func(i, j int) bool { return spans[i].from.Before(spans[j].from) })
			for i := 1; i < len(spans); i++ {
				if spans[i].from.Before(spans[i-1].to) {
					t.Errorf("%s %s: offers %+v and %+v were open at the same time", what, k, spans[i-1], spans[i])
				}
			}
		}
	}
	overlap(func(s span) string { return s.order }, "order")
	overlap(func(s span) string { return s.driver }, "driver")
}
