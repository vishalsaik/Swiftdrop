package handlers

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/drivertest"
	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// A driver's offers and deliveries: what they are shown, what they may answer, and above all what they are
// never told. Everything goes through real sign-in, against scripted dispatch and job stores that follow the
// Postgres adapters' contract (the real ones are tested against Postgres).
// ---------------------------------------------------------------------------

// fakeDispatch holds the offers by id. AcceptOffer follows the adapter: the offer must be the driver's and
// open, the order must still be waiting for a driver, and then the driver is set on it.
type fakeDispatch struct {
	mu     sync.Mutex
	db     *fakeDB
	offers map[int64]domain.Offer
	// failures to inject
	openErr, acceptErr, declineErr error
}

var _ ports.DispatchStore = (*fakeDispatch)(nil)

func (f *fakeDispatch) offer(id int64, orderID, driverID string, remaining float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offers[id] = domain.Offer{ID: id, OrderID: orderID, DriverID: driverID, Status: domain.OfferOpen, RemainingSeconds: remaining}
}

func (f *fakeDispatch) status(id int64) domain.OfferStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.offers[id].Status
}

func (f *fakeDispatch) OpenOffer(_ context.Context, driverID string) (domain.Offer, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return domain.Offer{}, false, f.openErr
	}
	for _, o := range f.offers {
		if o.DriverID == driverID && o.Status == domain.OfferOpen {
			return o, true, nil
		}
	}
	return domain.Offer{}, false, nil
}

func (f *fakeDispatch) AcceptOffer(_ context.Context, id int64, driverID string) (domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acceptErr != nil {
		return domain.Order{}, f.acceptErr
	}
	o, ok := f.offers[id]
	if !ok || o.DriverID != driverID || o.Status != domain.OfferOpen {
		return domain.Order{}, ports.ErrOfferGone
	}
	f.db.mu.Lock()
	defer f.db.mu.Unlock()
	ord := f.db.orders[o.OrderID]
	if ord.State != domain.OrderPreparing || ord.AssignedDriverID != "" {
		return domain.Order{}, ports.ErrOfferGone
	}
	ord.AssignedDriverID = driverID
	f.db.orders[o.OrderID] = ord
	o.Status = domain.OfferAccepted
	f.offers[id] = o
	return ord, nil
}

func (f *fakeDispatch) DeclineOffer(_ context.Context, id int64, driverID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.declineErr != nil {
		return f.declineErr
	}
	o, ok := f.offers[id]
	if !ok || o.DriverID != driverID || o.Status != domain.OfferOpen {
		return ports.ErrOfferGone
	}
	o.Status = domain.OfferDeclined
	f.offers[id] = o
	return nil
}

func (f *fakeDispatch) ExpireOffers(context.Context) (int, error) { return 0, nil }
func (f *fakeDispatch) ListWaitingOrders(context.Context, int) ([]domain.WaitingOrder, error) {
	return nil, nil
}
func (f *fakeDispatch) ListCandidates(context.Context, string, time.Duration) ([]domain.Candidate, error) {
	return nil, nil
}
func (f *fakeDispatch) OfferOrder(context.Context, string, string, time.Duration) (domain.Offer, bool, error) {
	return domain.Offer{}, false, nil
}
func (f *fakeDispatch) ListOffers(context.Context, string) ([]domain.Offer, error) { return nil, nil }

// fakeJobs finds the driver's unfinished delivery in the fake orders, as the adapter does.
type fakeJobs struct {
	db  *fakeDB
	err error
}

var _ ports.JobStore = (*fakeJobs)(nil)

func (f *fakeJobs) CurrentJob(_ context.Context, driverID string) (domain.Order, bool, error) {
	if f.err != nil {
		return domain.Order{}, false, f.err
	}
	f.db.mu.Lock()
	defer f.db.mu.Unlock()
	for _, o := range f.db.orders {
		if o.AssignedDriverID == driverID && (o.State == domain.OrderPreparing || o.State == domain.OrderPickedUp) {
			return o, true, nil
		}
	}
	return domain.Order{}, false, nil
}

type offerBody struct {
	Offer *offerView `json:"offer"`
}

type jobBody struct {
	Job *jobView `json:"job"`
}

// secrets are things only the customer and staff may know: none of them may ever reach a driver.
var secrets = []string{
	"cust-secret", "Secret burger", "999", "paymentType", "card", "customerId", "unitPriceCents", "sku", "SKU-1",
	"Home", "restaurantId", "items", "payment",
}

func noSecrets(t *testing.T, what, body string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(body, s) {
			t.Errorf("%s shows a driver %q: %s", what, s, body)
		}
	}
}

// jobWorld is a driver world with dispatch and jobs set up, a placed restaurant, and a way to put an order
// on a driver's plate.
type jobWorld struct {
	*driverWorld
	dispatch *fakeDispatch
	jobs     *fakeJobs
}

func newJobWorld(t *testing.T) *jobWorld {
	t.Helper()
	w := &jobWorld{driverWorld: newDriverWorld(t)}
	w.dispatch = &fakeDispatch{db: w.db, offers: map[int64]domain.Offer{}}
	w.jobs = &fakeJobs{db: w.db}
	w.s.Dispatch, w.s.Jobs = w.dispatch, w.jobs
	w.db.Open("r1", "Corner Kitchen")
	w.db.Place("r1", "", 12.9719, 77.6412, 5)
	return w
}

// order seeds an order of a customer, with everything a driver must not see, in a state.
func (w *jobWorld) order(id string, state domain.OrderState) {
	w.db.seedWith(id, state, func(o *domain.Order) {
		o.CustomerID, o.RestaurantID, o.PaymentType = "cust-secret", "r1", domain.PaymentCard
		o.Items = []domain.OrderItem{
			{SKU: "SKU-1", Name: "Secret burger", Quantity: 2, UnitPriceCents: 999},
			{SKU: "SKU-2", Name: "Fries", Quantity: 1, UnitPriceCents: 399},
		}
		o.DeliveryAddress = &domain.Address{Label: "Home", Line1: "42 Secret Lane", Landmark: "blue gate", City: "Bengaluru", PostalCode: "560038", Latitude: 12.98, Longitude: 77.64}
	})
}

func (w *jobWorld) assign(orderID, driverID string, ready bool) {
	o := w.db.orders[orderID]
	o.AssignedDriverID = driverID
	if ready {
		now := time.Now()
		o.FoodReady = &now
	}
	w.db.orders[orderID] = o
}

func TestOnlyADriverSeesAndAnswersOffersAndSeesTheirJob(t *testing.T) {
	w := newJobWorld(t)
	staff := w.staff(t, "dee@example.com")
	customer := w.customer(t, "ada@example.com")
	anon := w.browser(t)
	for _, route := range [][2]string{
		{http.MethodGet, "/driver/offers"}, {http.MethodPost, "/driver/offers/1/accept"},
		{http.MethodPost, "/driver/offers/1/decline"}, {http.MethodGet, "/driver/job"},
	} {
		wantStatus(t, anon.do(route[0], route[1], ""), http.StatusUnauthorized)
		for who, b := range map[string]*browser{"staff": staff, "customer": customer} {
			rec := b.do(route[0], route[1], "")
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "needs a driver account") {
				t.Errorf("%s %s %s = %d %s, want 403 saying it needs a driver account", who, route[0], route[1], rec.Code, strings.TrimSpace(rec.Body.String()))
			}
		}
	}
}

func TestADriverIsShownTheOrderOfferedAndNothingOfTheCustomer(t *testing.T) {
	w := newJobWorld(t)
	driver := w.driver(t, "dev@example.com")
	other := w.driver(t, "other@example.com")
	w.order("o1", domain.OrderPreparing)
	w.dispatch.offer(7, "o1", driver.userID, 42.5)

	// not located yet: the offer is there, without a distance
	rec := driver.do(http.MethodGet, "/driver/offers", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("an offer must not be cached")
	}
	v := decode[offerBody](t, rec.Body).Offer
	if v == nil || v.ID != 7 || v.RemainingSeconds != 42.5 || v.Restaurant.Name != "Corner Kitchen" {
		t.Fatalf("offer = %+v", v)
	}
	if v.Restaurant.Latitude == nil || *v.Restaurant.Latitude != 12.9719 || v.Restaurant.Longitude == nil {
		t.Errorf("the restaurant's place is missing: %+v", v.Restaurant)
	}
	if v.PickupKm != nil {
		t.Errorf("pickupKm = %v for a driver who has not said where they are", *v.PickupKm)
	}
	noSecrets(t, "an offer", rec.Body.String())
	if strings.Contains(rec.Body.String(), `"o1"`) || strings.Contains(rec.Body.String(), "42 Secret Lane") {
		t.Errorf("an offer names the order or where it goes: %s", rec.Body.String())
	}

	// located two kilometres north of the restaurant: the distance to collect it is shown
	wantStatus(t, driver.do(http.MethodPost, "/driver/online", ""), http.StatusOK)
	wantStatus(t, driver.do(http.MethodPost, "/driver/location", `{"latitude":12.9899,"longitude":77.6412}`), http.StatusOK)
	v = decode[offerBody](t, driver.do(http.MethodGet, "/driver/offers", "").Body).Offer
	if v.PickupKm == nil || math.Abs(*v.PickupKm-2.0) > 0.1 {
		t.Errorf("pickupKm = %v, want about 2.0", v.PickupKm)
	}

	// nobody else sees it
	if got := decode[offerBody](t, other.do(http.MethodGet, "/driver/offers", "").Body); got.Offer != nil {
		t.Errorf("another driver sees %+v", got.Offer)
	}
	rec = other.do(http.MethodGet, "/driver/offers", "")
	if !strings.Contains(rec.Body.String(), `"offer":null`) {
		t.Errorf("no offer should read as null, got %s", rec.Body.String())
	}
}

func TestAnOfferStillWorksWhenTheRestaurantCannotBePlacedOrRead(t *testing.T) {
	w := newJobWorld(t)
	driver := w.driver(t, "dev@example.com")
	wantStatus(t, driver.do(http.MethodPost, "/driver/online", ""), http.StatusOK)
	wantStatus(t, driver.do(http.MethodPost, "/driver/location", `{"latitude":12.9899,"longitude":77.6412}`), http.StatusOK)

	// a restaurant that has no place on the map: the offer shows its name and no distance
	w.db.Open("r2", "Nowhere Diner")
	w.order("o1", domain.OrderPreparing)
	o := w.db.orders["o1"]
	o.RestaurantID = "r2"
	w.db.orders["o1"] = o
	w.dispatch.offer(1, "o1", driver.userID, 30)
	v := decode[offerBody](t, driver.do(http.MethodGet, "/driver/offers", "").Body).Offer
	if v == nil || v.Restaurant.Name != "Nowhere Diner" || v.Restaurant.Latitude != nil || v.PickupKm != nil {
		t.Errorf("offer = %+v, want the name only", v)
	}

	// a restaurant that cannot be read: the offer is still shown, without a name
	o.RestaurantID = "ghost"
	w.db.orders["o1"] = o
	v = decode[offerBody](t, driver.do(http.MethodGet, "/driver/offers", "").Body).Offer
	if v == nil || v.ID != 1 || v.Restaurant.Name != "" {
		t.Errorf("offer = %+v, want it shown without a name", v)
	}

	// the driver's own state cannot be read: no distance, still an offer
	o.RestaurantID = "r1"
	w.db.orders["o1"] = o
	w.board.Err = errors.New("driver store is down")
	v = decode[offerBody](t, driver.do(http.MethodGet, "/driver/offers", "").Body).Offer
	if v == nil || v.PickupKm != nil || v.Restaurant.Name != "Corner Kitchen" {
		t.Errorf("offer = %+v, want it shown without a distance", v)
	}
}

func TestAcceptingAnOfferMakesItTheDriversJobAndTellsThemWhereItGoes(t *testing.T) {
	w := newJobWorld(t)
	driver := w.driver(t, "dev@example.com")
	w.order("o1", domain.OrderPreparing)
	w.dispatch.offer(7, "o1", driver.userID, 50)

	rec := driver.do(http.MethodPost, "/driver/offers/7/accept", "")
	wantStatus(t, rec, http.StatusOK)
	job := decode[jobBody](t, rec.Body).Job
	if job == nil || job.OrderID != "o1" || job.State != domain.OrderPreparing || job.FoodReady || job.ItemCount != 3 || job.Restaurant.Name != "Corner Kitchen" {
		t.Fatalf("job = %+v", job)
	}
	if d := job.DeliverTo; d == nil || d.Line1 != "42 Secret Lane" || d.Landmark != "blue gate" || d.City != "Bengaluru" || d.PostalCode != "560038" || d.Latitude != 12.98 {
		t.Errorf("deliverTo = %+v, want the address with its landmark", d)
	}
	// the address is the driver's to see now; who ordered, what, how it was paid and the customer's own name
	// for the place ("Home") are not
	noSecrets(t, "the job", rec.Body.String())
	if got := w.db.orders["o1"].AssignedDriverID; got != driver.userID {
		t.Errorf("the order's driver = %q, want the one who accepted", got)
	}
	if w.dispatch.status(7) != domain.OfferAccepted {
		t.Error("the offer was not marked accepted")
	}

	// and it is what the driver's screen finds next
	rec = driver.do(http.MethodGet, "/driver/job", "")
	wantStatus(t, rec, http.StatusOK)
	if j := decode[jobBody](t, rec.Body).Job; j == nil || j.OrderID != "o1" {
		t.Errorf("GET /driver/job = %+v", j)
	}
	if got := decode[offerBody](t, driver.do(http.MethodGet, "/driver/offers", "").Body); got.Offer != nil {
		t.Errorf("an accepted offer is still offered: %+v", got.Offer)
	}
}

func TestAnOfferThatCannotBeAnsweredIsAlwaysTheSameAnswer(t *testing.T) {
	w := newJobWorld(t)
	driver := w.driver(t, "dev@example.com")
	other := w.driver(t, "other@example.com")
	w.order("o1", domain.OrderPreparing)
	w.order("o2", domain.OrderPreparing)
	w.dispatch.offer(7, "o1", driver.userID, 50)
	w.dispatch.offer(8, "o2", other.userID, 50)

	var answers []string
	for name, path := range map[string]string{
		"an offer that does not exist": "/driver/offers/999/",
		"someone else's offer":         "/driver/offers/8/",
		"an id that is not a number":   "/driver/offers/seven/",
		"an id of zero":                "/driver/offers/0/",
		"a negative id":                "/driver/offers/-3/",
		"an id too big":                "/driver/offers/99999999999999999999/",
	} {
		for _, verb := range []string{"accept", "decline"} {
			rec := driver.do(http.MethodPost, path+verb, "")
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s (%s) = %d, want 404", name, verb, rec.Code)
			}
			answers = append(answers, rec.Body.String())
		}
	}
	for _, a := range answers {
		if a != answers[0] {
			t.Fatalf("offers that cannot be answered got different answers, so they can be told apart: %q and %q", answers[0], a)
		}
	}
	if !strings.Contains(answers[0], "no longer open") {
		t.Errorf("answer = %s", answers[0])
	}
	if w.db.orders["o2"].AssignedDriverID != "" || w.dispatch.status(8) != domain.OfferOpen {
		t.Error("another driver's offer was changed")
	}

	// answered once, gone after
	wantStatus(t, driver.do(http.MethodPost, "/driver/offers/7/accept", ""), http.StatusOK)
	wantStatus(t, driver.do(http.MethodPost, "/driver/offers/7/accept", ""), http.StatusNotFound)
	wantStatus(t, driver.do(http.MethodPost, "/driver/offers/7/decline", ""), http.StatusNotFound)
}

func TestAnOfferForAnOrderThatWasCancelledIsGone(t *testing.T) {
	w := newJobWorld(t)
	driver := w.driver(t, "dev@example.com")
	w.order("o1", domain.OrderCancelled)
	w.dispatch.offer(7, "o1", driver.userID, 50)
	wantStatus(t, driver.do(http.MethodPost, "/driver/offers/7/accept", ""), http.StatusNotFound)
	if w.db.orders["o1"].AssignedDriverID != "" {
		t.Error("a cancelled order was given a driver")
	}
}

func TestADriverCanDeclineAndABusyDriverCannotAccept(t *testing.T) {
	w := newJobWorld(t)
	driver := w.driver(t, "dev@example.com")
	w.order("o1", domain.OrderPreparing)
	w.dispatch.offer(7, "o1", driver.userID, 50)

	rec := driver.do(http.MethodPost, "/driver/offers/7/decline", "")
	wantStatus(t, rec, http.StatusNoContent)
	if rec.Body.Len() != 0 {
		t.Errorf("a decline answered with a body: %s", rec.Body.String())
	}
	if w.dispatch.status(7) != domain.OfferDeclined || w.db.orders["o1"].AssignedDriverID != "" {
		t.Error("declining changed the wrong thing")
	}

	w.dispatch.offer(8, "o1", driver.userID, 50)
	w.dispatch.acceptErr = ports.ErrDriverBusy
	rec = driver.do(http.MethodPost, "/driver/offers/8/accept", "")
	wantStatus(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "finish the delivery you have") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestADriverIsToldNothingOfAFailingStore(t *testing.T) {
	w := newJobWorld(t)
	driver := w.driver(t, "dev@example.com")
	w.order("o1", domain.OrderPreparing)
	w.dispatch.offer(7, "o1", driver.userID, 50)

	w.dispatch.openErr = errors.New("secret open failure")
	w.dispatch.acceptErr = errors.New("secret accept failure")
	w.dispatch.declineErr = errors.New("secret decline failure")
	w.jobs.err = errors.New("secret job failure")
	for _, route := range [][2]string{
		{http.MethodGet, "/driver/offers"}, {http.MethodPost, "/driver/offers/7/accept"},
		{http.MethodPost, "/driver/offers/7/decline"}, {http.MethodGet, "/driver/job"},
	} {
		rec := driver.do(route[0], route[1], "")
		wantStatus(t, rec, http.StatusInternalServerError)
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s leaked the cause: %s", route[1], rec.Body.String())
		}
	}

	// an offer whose order cannot be read
	w.dispatch.openErr = nil
	w.db.getErr = errors.New("secret order failure")
	rec := driver.do(http.MethodGet, "/driver/offers", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("leaked the cause: %s", rec.Body.String())
	}
}

func TestTheOfferAndJobRoutesSayWhenTheyAreNotSetUp(t *testing.T) {
	w := newJobWorld(t)
	driver := w.driver(t, "dev@example.com")
	w.s.Dispatch, w.s.Jobs = nil, nil
	for _, route := range [][2]string{
		{http.MethodGet, "/driver/offers"}, {http.MethodPost, "/driver/offers/7/accept"},
		{http.MethodPost, "/driver/offers/7/decline"}, {http.MethodGet, "/driver/job"},
	} {
		wantStatus(t, driver.do(route[0], route[1], ""), http.StatusServiceUnavailable)
	}
}

func TestADriverSeesTheirCurrentDeliveryAndOnlyThat(t *testing.T) {
	w := newJobWorld(t)
	driver := w.driver(t, "dev@example.com")
	other := w.driver(t, "other@example.com")

	rec := driver.do(http.MethodGet, "/driver/job", "")
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"job":null`) {
		t.Errorf("no job should read as null, got %s", rec.Body.String())
	}

	w.order("o1", domain.OrderPreparing)
	w.assign("o1", driver.userID, false)
	w.order("done", domain.OrderDelivered)
	w.assign("done", driver.userID, true)
	rec = driver.do(http.MethodGet, "/driver/job", "")
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("a job must not be cached")
	}
	job := decode[jobBody](t, rec.Body).Job
	if job == nil || job.OrderID != "o1" || job.FoodReady {
		t.Fatalf("job = %+v, want the unfinished delivery, not the one already delivered", job)
	}
	noSecrets(t, "a job", rec.Body.String())
	if got := decode[jobBody](t, other.do(http.MethodGet, "/driver/job", "").Body); got.Job != nil {
		t.Errorf("another driver sees %+v", got.Job)
	}

	// food ready is shown, and a delivery with no address on it still reads
	w.assign("o1", driver.userID, true)
	o := w.db.orders["o1"]
	o.DeliveryAddress = nil
	w.db.orders["o1"] = o
	job = decode[jobBody](t, driver.do(http.MethodGet, "/driver/job", "").Body).Job
	if job == nil || !job.FoodReady || job.DeliverTo != nil {
		t.Errorf("job = %+v, want food ready and no address", job)
	}
}

func TestADriverPicksUpAndDeliversOnlyTheirOwnDeliveryAndIsToldOnlyTheJob(t *testing.T) {
	w := newJobWorld(t)
	staff := w.staff(t, "dee@example.com")
	customer := w.customer(t, "ada@example.com")
	driver := w.driver(t, "dev@example.com")
	other := w.driver(t, "other@example.com")
	w.order("o1", domain.OrderPreparing)
	w.assign("o1", driver.userID, false)

	// the food is not ready: the same refusal staff get
	rec := driver.do(http.MethodPost, "/orders/o1/pickup", "")
	wantStatus(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "not ready") {
		t.Errorf("body = %s", rec.Body.String())
	}
	// not before picking it up
	wantStatus(t, driver.do(http.MethodPost, "/orders/o1/deliver", ""), http.StatusConflict)

	// not someone else's, not one that does not exist, and not one nobody has: all the same "not found"
	w.assign("o1", driver.userID, true)
	w.order("theirs", domain.OrderPreparing)
	w.assign("theirs", other.userID, true)
	w.order("unassigned", domain.OrderPreparing)
	var notFound []string
	for _, id := range []string{"theirs", "ghost", "unassigned"} {
		for _, action := range []string{"pickup", "deliver"} {
			rec := driver.do(http.MethodPost, "/orders/"+id+"/"+action, "")
			wantStatus(t, rec, http.StatusNotFound)
			notFound = append(notFound, strings.ReplaceAll(rec.Body.String(), id, "ID"))
		}
	}
	for _, a := range notFound {
		if a != notFound[0] {
			t.Errorf("a driver can tell orders apart by the answer: %q and %q", notFound[0], a)
		}
	}
	if w.db.orders["theirs"].State != domain.OrderPreparing {
		t.Error("a driver moved another driver's order")
	}

	// theirs, with the food ready: picked up, and the answer is the job, not the order
	rec = driver.do(http.MethodPost, "/orders/o1/pickup", "")
	wantStatus(t, rec, http.StatusOK)
	job := decode[jobBody](t, rec.Body).Job
	if job == nil || job.State != domain.OrderPickedUp || job.OrderID != "o1" {
		t.Fatalf("job = %+v", job)
	}
	noSecrets(t, "the answer to a pickup", rec.Body.String())
	if w.db.orders["o1"].State != domain.OrderPickedUp {
		t.Error("the order was not picked up")
	}

	rec = driver.do(http.MethodPost, "/orders/o1/deliver", "")
	wantStatus(t, rec, http.StatusOK)
	if job := decode[jobBody](t, rec.Body).Job; job == nil || job.State != domain.OrderDelivered {
		t.Errorf("job = %+v, want delivered", job)
	}
	// finished: it is no longer theirs to touch, and no longer their job
	wantStatus(t, driver.do(http.MethodPost, "/orders/o1/deliver", ""), http.StatusNotFound)
	if j := decode[jobBody](t, driver.do(http.MethodGet, "/driver/job", "").Body).Job; j != nil {
		t.Errorf("a delivered order is still the job: %+v", j)
	}

	// customers and signed-out visitors may not; staff are answered with the whole order as before
	w.order("o2", domain.OrderPreparing)
	w.assign("o2", other.userID, true)
	wantStatus(t, customer.do(http.MethodPost, "/orders/o2/pickup", ""), http.StatusForbidden)
	wantStatus(t, w.browser(t).do(http.MethodPost, "/orders/o2/pickup", ""), http.StatusUnauthorized)
	rec = staff.do(http.MethodPost, "/orders/o2/pickup", "")
	wantStatus(t, rec, http.StatusOK)
	if got := orderFrom(t, rec); got.ID != "o2" || got.CustomerID != "cust-secret" || got.State != domain.OrderPickedUp {
		t.Errorf("staff were answered %+v, want the whole order", got)
	}
}

func TestADriverStillCannotRunTheKitchenOrCancel(t *testing.T) {
	w := newJobWorld(t)
	driver := w.driver(t, "dev@example.com")
	w.order("o1", domain.OrderPreparing)
	w.assign("o1", driver.userID, true)
	for _, path := range []string{"/orders/o1/ready", "/orders/o1/accept", "/orders/o1/cancel", "/orders/o1/refund"} {
		if rec := driver.do(http.MethodPost, path, ""); rec.Code != http.StatusForbidden {
			t.Errorf("a driver on %s got %d, want 403", path, rec.Code)
		}
	}
	if got := w.db.orders["o1"].State; got != domain.OrderPreparing {
		t.Errorf("a refused call changed the order to %s", got)
	}
}

type ordersBody struct {
	Orders []orderView `json:"orders"`
}

func TestStaffSeeTheDriversWholeNameAndACustomerOnlyTheFirstAndNeverTheirId(t *testing.T) {
	w := newJobWorld(t)
	staff := w.staff(t, "dee@example.com")
	customer := w.customer(t, "ada@example.com")
	driver := w.driver(t, "dev@example.com")
	w.order("o1", domain.OrderPreparing)
	w.assign("o1", driver.userID, false)
	w.db.orders["o1"] = withCustomer(w.db.orders["o1"], customer.userID)
	w.order("o2", domain.OrderPaid) // no driver yet
	w.db.orders["o2"] = withCustomer(w.db.orders["o2"], customer.userID)

	// staff: the whole name, and the id they act on
	one := decode[orderView](t, staff.do(http.MethodGet, "/orders/o1", "").Body)
	if one.DriverName != "Dev Driver" || one.AssignedDriverID != driver.userID {
		t.Errorf("staff see driverName %q and id %q, want the whole name and the id", one.DriverName, one.AssignedDriverID)
	}
	byID := map[string]orderView{}
	for _, v := range decode[ordersBody](t, staff.do(http.MethodGet, "/orders", "").Body).Orders {
		byID[v.ID] = v
	}
	if byID["o1"].DriverName != "Dev Driver" || byID["o2"].DriverName != "" {
		t.Errorf("staff's list: o1 driverName %q, o2 driverName %q; want a name only where there is a driver", byID["o1"].DriverName, byID["o2"].DriverName)
	}

	// the customer: the first name, and not the account id, in the order, the list, and what they do to it
	rec := customer.do(http.MethodGet, "/orders/o1", "")
	one = decode[orderView](t, rec.Body)
	if one.DriverName != "Dev" || one.AssignedDriverID != "" || strings.Contains(rec.Body.String(), driver.userID) {
		t.Errorf("the customer is shown driverName %q, id %q: %s", one.DriverName, one.AssignedDriverID, rec.Body.String())
	}
	rec = customer.do(http.MethodGet, "/orders", "")
	if strings.Contains(rec.Body.String(), driver.userID) || strings.Contains(rec.Body.String(), "Dev Driver") {
		t.Errorf("the customer's list shows more of the driver than a first name: %s", rec.Body.String())
	}
	for _, v := range decode[ordersBody](t, rec.Body).Orders {
		if v.ID == "o1" && v.DriverName != "Dev" || v.ID == "o2" && v.DriverName != "" {
			t.Errorf("the customer's list: %s has driverName %q", v.ID, v.DriverName)
		}
	}
	rec = customer.do(http.MethodPost, "/orders/o1/cancel", "")
	wantStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), driver.userID) {
		t.Errorf("cancelling answered with the driver's account id: %s", rec.Body.String())
	}
}

func TestAnOrderWithADriverWhoseNameCannotBeReadStillSaysItHasOne(t *testing.T) {
	w := newJobWorld(t)
	staff := w.staff(t, "dee@example.com")
	customer := w.customer(t, "ada@example.com")
	driver := w.driver(t, "dev@example.com")
	w.order("o1", domain.OrderPreparing)
	w.assign("o1", driver.userID, false)
	w.db.orders["o1"] = withCustomer(w.db.orders["o1"], customer.userID)

	for how, undo := range map[string]func(){
		"the lookup fails":        func() { w.board.Err = errors.New("secret") },
		"drivers are not set up":  func() { w.s.Drivers = nil },
		"the name is not on file": func() { w.board.Err = nil; w.s.Drivers = drivertest.New() }, // knows no one
	} {
		undo()
		got := decode[orderView](t, customer.do(http.MethodGet, "/orders/o1", "").Body)
		if got.DriverName != unknownDriver || got.AssignedDriverID != "" {
			t.Errorf("%s: the customer is shown driverName %q, id %q; want %q and no id", how, got.DriverName, got.AssignedDriverID, unknownDriver)
		}
		rec := staff.do(http.MethodGet, "/orders/o1", "")
		wantStatus(t, rec, http.StatusOK)
		if v := decode[orderView](t, rec.Body); v.DriverName != "" || v.AssignedDriverID != driver.userID {
			t.Errorf("%s: staff are shown driverName %q, id %q; want no name and the id", how, v.DriverName, v.AssignedDriverID)
		}
		w.s.Drivers, w.board.Err = w.board, nil
	}
}

func withCustomer(o domain.Order, customerID string) domain.Order {
	o.CustomerID = customerID
	return o
}

// The whole of it, in the order it happens: the kitchen accepts, dispatch offers, the driver takes it, the
// kitchen finishes, the driver collects and delivers, and staff see it delivered.
func TestFromTheKitchenToTheDoor(t *testing.T) {
	w := newJobWorld(t)
	staff := w.staff(t, "dee@example.com")
	driver := w.driver(t, "dev@example.com")
	w.order("o1", domain.OrderPaid)

	wantStatus(t, staff.do(http.MethodPost, "/orders/o1/accept", ""), http.StatusOK)
	if w.db.orders["o1"].AssignedDriverID != "" {
		t.Fatal("the kitchen accepting an order chose a driver")
	}
	w.dispatch.offer(1, "o1", driver.userID, 60) // what the dispatch job does next
	if v := decode[offerBody](t, driver.do(http.MethodGet, "/driver/offers", "").Body).Offer; v == nil || v.ID != 1 {
		t.Fatalf("the driver was not shown the offer: %+v", v)
	}
	wantStatus(t, driver.do(http.MethodPost, "/driver/offers/1/accept", ""), http.StatusOK)
	wantStatus(t, driver.do(http.MethodPost, "/orders/o1/pickup", ""), http.StatusConflict) // the food is not ready
	wantStatus(t, staff.do(http.MethodPost, "/orders/o1/ready", ""), http.StatusOK)
	wantStatus(t, driver.do(http.MethodPost, "/orders/o1/pickup", ""), http.StatusOK)
	wantStatus(t, driver.do(http.MethodPost, "/orders/o1/deliver", ""), http.StatusOK)

	got := orderFrom(t, staff.do(http.MethodGet, "/orders/o1", ""))
	if got.State != domain.OrderDelivered || got.AssignedDriverID != driver.userID {
		t.Errorf("order = %+v, want delivered by the driver who accepted it", got)
	}
}
