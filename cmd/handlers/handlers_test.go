package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// fakeDB: an in-memory ports.IDatabase that behaves like the Postgres adapter
// (same "order not found: <id>" error text, persists only the columns INSERT
// writes) and can be told to fail, so handlers are tested without Postgres.
// ---------------------------------------------------------------------------

var _ ports.IDatabase = (*fakeDB)(nil)

type fakeDB struct {
	orders map[string]domain.Order

	getErr    error
	createErr error
	readyErr  error
	acceptErr error
	updateErr error
	// updateErrOn restricts updateErr to moves into this state; "" means every move.
	updateErrOn domain.OrderState

	stateWrites     []domain.OrderState
	acceptDriverIDs []string
}

func newFakeDB() *fakeDB { return &fakeDB{orders: map[string]domain.Order{}} }

func notFound(id string) error { return fmt.Errorf("order not found: %s", id) }

func (f *fakeDB) CreateOrder(_ context.Context, o domain.Order) (domain.Order, error) {
	if f.createErr != nil {
		return domain.Order{}, f.createErr
	}
	if _, dup := f.orders[o.ID]; dup {
		return domain.Order{}, fmt.Errorf("duplicate key: %s", o.ID)
	}
	stored := domain.Order{
		ID: o.ID, CustomerID: o.CustomerID, RestaurantID: o.RestaurantID,
		Items: o.Items, State: o.State,
	}
	f.orders[o.ID] = stored
	return stored, nil
}

func (f *fakeDB) GetOrderbyId(_ context.Context, id string) (domain.Order, error) {
	if f.getErr != nil {
		return domain.Order{}, f.getErr
	}
	o, ok := f.orders[id]
	if !ok {
		return domain.Order{}, notFound(id)
	}
	return o, nil
}

func (f *fakeDB) UpdateOrderState(_ context.Context, id string, state string) (domain.Order, error) {
	if f.updateErr != nil && (f.updateErrOn == "" || string(f.updateErrOn) == state) {
		return domain.Order{}, f.updateErr
	}
	o, ok := f.orders[id]
	if !ok {
		return domain.Order{}, notFound(id)
	}
	o.State = domain.OrderState(state)
	f.orders[id] = o
	f.stateWrites = append(f.stateWrites, o.State)
	return o, nil
}

func (f *fakeDB) SetFoodReady(_ context.Context, id string) (domain.Order, error) {
	if f.readyErr != nil {
		return domain.Order{}, f.readyErr
	}
	o, ok := f.orders[id]
	if !ok {
		return domain.Order{}, notFound(id)
	}
	if o.FoodReady == nil { // COALESCE(food_ready, now())
		now := time.Now()
		o.FoodReady = &now
	}
	f.orders[id] = o
	return o, nil
}

func (f *fakeDB) AcceptOrder(_ context.Context, id, driverID, state string) (domain.Order, error) {
	if f.acceptErr != nil {
		return domain.Order{}, f.acceptErr
	}
	o, ok := f.orders[id]
	if !ok {
		return domain.Order{}, notFound(id)
	}
	o.State = domain.OrderState(state)
	o.AssignedDriverID = driverID
	f.orders[id] = o
	f.acceptDriverIDs = append(f.acceptDriverIDs, driverID)
	return o, nil
}

// seed stores an order in the given state and returns it.
func (f *fakeDB) seed(id string, state domain.OrderState) domain.Order {
	o := domain.Order{
		ID: id, CustomerID: "cust", RestaurantID: "rest",
		Items: []domain.OrderItem{}, State: state,
	}
	f.orders[id] = o
	return o
}

// seedWith stores an order and then lets the caller adjust it.
func (f *fakeDB) seedWith(id string, state domain.OrderState, adjust func(*domain.Order)) {
	o := f.seed(id, state)
	adjust(&o)
	f.orders[id] = o
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

var allStates = []domain.OrderState{
	domain.OrderCreated,
	domain.OrderPaymentPending,
	domain.OrderPaid,
	domain.OrderPreparing,
	domain.OrderPickedUp,
	domain.OrderDelivered,
	domain.OrderCancelled,
}

func do(t *testing.T, db *fakeDB, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	NewServer(db).Router.ServeHTTP(rec, httptest.NewRequest(method, path, rdr))
	return rec
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, want, strings.TrimSpace(rec.Body.String()))
	}
}

func orderFrom(t *testing.T, rec *httptest.ResponseRecorder) domain.Order {
	t.Helper()
	var o domain.Order
	if err := json.NewDecoder(rec.Body).Decode(&o); err != nil {
		t.Fatalf("response is not an order: %v (body: %s)", err, rec.Body.String())
	}
	return o
}

func errorFrom(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("response is not a JSON error: %v (body: %s)", err, rec.Body.String())
	}
	msg, ok := e["error"]
	if !ok {
		t.Fatalf(`response has no "error" key: %s`, rec.Body.String())
	}
	return msg
}

func wantContentTypeJSON(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func wantErrorContains(t *testing.T, rec *httptest.ResponseRecorder, parts ...string) {
	t.Helper()
	msg := errorFrom(t, rec)
	for _, p := range parts {
		if !strings.Contains(msg, p) {
			t.Errorf("error %q should contain %q", msg, p)
		}
	}
}

// ---------------------------------------------------------------------------
// response helpers
// ---------------------------------------------------------------------------

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusCreated, map[string]int{"n": 1})
	wantStatus(t, rec, http.StatusCreated)
	wantContentTypeJSON(t, rec)
	if got := strings.TrimSpace(rec.Body.String()); got != `{"n":1}` {
		t.Errorf("body = %s", got)
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusTeapot, "boom")
	wantStatus(t, rec, http.StatusTeapot)
	wantContentTypeJSON(t, rec)
	if got := errorFrom(t, rec); got != "boom" {
		t.Errorf("error = %q, want boom", got)
	}
}

func TestWriteIllegalTransition(t *testing.T) {
	rec := httptest.NewRecorder()
	writeIllegalTransition(rec, domain.Order{ID: "o1", State: domain.OrderDelivered}, domain.OrderCancelled)
	wantStatus(t, rec, http.StatusConflict)
	if got, want := errorFrom(t, rec), "order o1 cannot be cancelled from state delivered"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// health, routing
// ---------------------------------------------------------------------------

func TestHealth(t *testing.T) {
	rec := do(t, newFakeDB(), http.MethodGet, "/health", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.String() != "OK" {
		t.Errorf("body = %q, want OK", rec.Body.String())
	}
}

func TestRouting(t *testing.T) {
	tests := []struct {
		name         string
		method, path string
		want         int
	}{
		{"unknown path", http.MethodGet, "/nope", http.StatusNotFound},
		{"lifecycle verbs are POST only", http.MethodGet, "/orders/x/pay", http.StatusMethodNotAllowed},
		{"order is not listable", http.MethodGet, "/orders", http.StatusMethodNotAllowed},
		{"health is GET only", http.MethodPost, "/health", http.StatusMethodNotAllowed},
		{"get is not POST", http.MethodPost, "/orders/x", http.StatusMethodNotAllowed},
		{"old route shape is gone", http.MethodPost, "/x/ready", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantStatus(t, do(t, newFakeDB(), tt.method, tt.path, ""), tt.want)
		})
	}
}

// ---------------------------------------------------------------------------
// loadOrder
// ---------------------------------------------------------------------------

func TestLoadOrderMissingIDParam(t *testing.T) {
	s := NewServer(newFakeDB())
	rec := httptest.NewRecorder()
	// Not routed through mux, so there is no {id} variable at all.
	_, ok := s.loadOrder(rec, httptest.NewRequest(http.MethodGet, "/orders/", nil))
	if ok {
		t.Fatal("loadOrder reported ok without an id")
	}
	wantStatus(t, rec, http.StatusBadRequest)
}

func TestLoadOrderOutcomes(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		db := newFakeDB()
		db.seed("o1", domain.OrderPaid)
		wantStatus(t, do(t, db, http.MethodGet, "/orders/o1", ""), http.StatusOK)
	})
	t.Run("not found", func(t *testing.T) {
		rec := do(t, newFakeDB(), http.MethodGet, "/orders/ghost", "")
		wantStatus(t, rec, http.StatusNotFound)
		wantErrorContains(t, rec, "ghost")
	})
	t.Run("database failure is a 500 and does not leak the cause", func(t *testing.T) {
		db := newFakeDB()
		db.getErr = errors.New("connection reset by peer")
		rec := do(t, db, http.MethodGet, "/orders/o1", "")
		wantStatus(t, rec, http.StatusInternalServerError)
		if strings.Contains(rec.Body.String(), "connection reset") {
			t.Errorf("internal error text leaked to the client: %s", rec.Body.String())
		}
	})
}

// ---------------------------------------------------------------------------
// POST /orders
// ---------------------------------------------------------------------------

func TestCreateOrderAlwaysStartsAsCreated(t *testing.T) {
	for _, clientState := range []string{"", "preparing", "picked_up", "delivered", "banana"} {
		t.Run("client sends "+clientState, func(t *testing.T) {
			db := newFakeDB()
			body := `{"id":"o1","customerId":"c","restaurantId":"r","items":[]`
			if clientState != "" {
				body += `,"state":"` + clientState + `"`
			}
			body += `}`

			rec := do(t, db, http.MethodPost, "/orders", body)
			wantStatus(t, rec, http.StatusCreated)
			wantContentTypeJSON(t, rec)
			if got := orderFrom(t, rec).State; got != domain.OrderCreated {
				t.Errorf("response state = %q, want created", got)
			}
			if got := db.orders["o1"].State; got != domain.OrderCreated {
				t.Errorf("stored state = %q, want created", got)
			}
		})
	}
}

func TestCreateOrderIgnoresServerOwnedFields(t *testing.T) {
	db := newFakeDB()
	body := `{"id":"o1","customerId":"c","restaurantId":"r","items":[],` +
		`"assignedDriverId":"hacker","foodReady":"2020-01-01T00:00:00Z"}`
	rec := do(t, db, http.MethodPost, "/orders", body)
	wantStatus(t, rec, http.StatusCreated)

	got := orderFrom(t, rec)
	if got.AssignedDriverID != "" {
		t.Errorf("assignedDriverId = %q, want empty", got.AssignedDriverID)
	}
	if got.FoodReady != nil {
		t.Errorf("foodReady = %v, want nil", got.FoodReady)
	}
	if stored := db.orders["o1"]; stored.AssignedDriverID != "" || stored.FoodReady != nil {
		t.Errorf("server-owned fields were persisted: %+v", stored)
	}
}

func TestCreateOrderPersistsFields(t *testing.T) {
	db := newFakeDB()
	body := `{"id":"o1","customerId":"cust-9","restaurantId":"rest-3",` +
		`"items":[{"sku":"S1","name":"Burger","quantity":2,"unitPriceCents":599}]}`
	wantStatus(t, do(t, db, http.MethodPost, "/orders", body), http.StatusCreated)

	got := db.orders["o1"]
	if got.CustomerID != "cust-9" || got.RestaurantID != "rest-3" {
		t.Errorf("wrong ids stored: %+v", got)
	}
	if len(got.Items) != 1 || got.Items[0].Quantity != 2 || got.Items[0].UnitPriceCents != 599 {
		t.Errorf("wrong items stored: %+v", got.Items)
	}
}

func TestCreateOrderRejectsBadBodies(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      "not valid json",
		"truncated":     `{"id":"o1",`,
		"wrong type":    `{"id":123}`,
		"array not obj": `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			db := newFakeDB()
			wantStatus(t, do(t, db, http.MethodPost, "/orders", body), http.StatusBadRequest)
			if len(db.orders) != 0 {
				t.Errorf("a rejected request must not store anything, got %d orders", len(db.orders))
			}
		})
	}
}

func TestCreateOrderDatabaseFailure(t *testing.T) {
	db := newFakeDB()
	db.createErr = errors.New("disk full")
	rec := do(t, db, http.MethodPost, "/orders", `{"id":"o1","customerId":"c","restaurantId":"r","items":[]}`)
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "disk full") {
		t.Errorf("internal error text leaked to the client: %s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GET /orders/{id}
// ---------------------------------------------------------------------------

func TestGetOrderByID(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderPaid)
	rec := do(t, db, http.MethodGet, "/orders/o1", "")
	wantStatus(t, rec, http.StatusOK)
	wantContentTypeJSON(t, rec)
	if got := orderFrom(t, rec); got.ID != "o1" || got.State != domain.OrderPaid {
		t.Errorf("wrong order returned: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// transition: /cancel and /deliver are pure state moves
// ---------------------------------------------------------------------------

func TestPureStateMoves(t *testing.T) {
	tests := []struct {
		name      string
		action    string
		to        domain.OrderState
		legalFrom []domain.OrderState
	}{
		{
			name: "cancel", action: "cancel", to: domain.OrderCancelled,
			legalFrom: []domain.OrderState{
				domain.OrderCreated, domain.OrderPaymentPending, domain.OrderPaid, domain.OrderPreparing,
			},
		},
		{
			name: "deliver", action: "deliver", to: domain.OrderDelivered,
			legalFrom: []domain.OrderState{domain.OrderPickedUp},
		},
	}
	for _, tt := range tests {
		for _, from := range allStates {
			t.Run(fmt.Sprintf("%s from %s", tt.name, from), func(t *testing.T) {
				db := newFakeDB()
				db.seed("o1", from)
				rec := do(t, db, http.MethodPost, "/orders/o1/"+tt.action, "")

				if slices.Contains(tt.legalFrom, from) {
					wantStatus(t, rec, http.StatusOK)
					if got := orderFrom(t, rec).State; got != tt.to {
						t.Errorf("response state = %s, want %s", got, tt.to)
					}
					if got := db.orders["o1"].State; got != tt.to {
						t.Errorf("stored state = %s, want %s", got, tt.to)
					}
					return
				}
				wantStatus(t, rec, http.StatusConflict)
				wantErrorContains(t, rec, "o1", string(from))
				if got := db.orders["o1"].State; got != from {
					t.Errorf("a refused move must not change state, got %s", got)
				}
				if len(db.stateWrites) != 0 {
					t.Errorf("a refused move must not write, got %v", db.stateWrites)
				}
			})
		}
	}
}

func TestPureStateMovesMissingAndFailing(t *testing.T) {
	for _, action := range []string{"cancel", "deliver"} {
		t.Run(action+" unknown order", func(t *testing.T) {
			wantStatus(t, do(t, newFakeDB(), http.MethodPost, "/orders/ghost/"+action, ""), http.StatusNotFound)
		})
	}
	t.Run("update failure is a 500 and state is unchanged", func(t *testing.T) {
		db := newFakeDB()
		db.seed("o1", domain.OrderCreated)
		db.updateErr = errors.New("deadlock detected")
		rec := do(t, db, http.MethodPost, "/orders/o1/cancel", "")
		wantStatus(t, rec, http.StatusInternalServerError)
		if db.orders["o1"].State != domain.OrderCreated {
			t.Errorf("state changed despite the failed update: %s", db.orders["o1"].State)
		}
	})
}

// ---------------------------------------------------------------------------
// POST /orders/{id}/ready
// ---------------------------------------------------------------------------

func TestReady(t *testing.T) {
	for _, from := range allStates {
		t.Run("from "+string(from), func(t *testing.T) {
			db := newFakeDB()
			db.seed("o1", from)
			rec := do(t, db, http.MethodPost, "/orders/o1/ready", "")

			if from != domain.OrderPreparing {
				wantStatus(t, rec, http.StatusConflict)
				wantErrorContains(t, rec, "o1", string(from))
				if db.orders["o1"].FoodReady != nil {
					t.Error("a refused call must not set foodReady")
				}
				return
			}
			wantStatus(t, rec, http.StatusOK)
			got := orderFrom(t, rec)
			if got.FoodReady == nil {
				t.Error("foodReady should be set")
			}
			if got.State != domain.OrderPreparing {
				t.Errorf("ready must not change state, got %s", got.State)
			}
			if len(db.stateWrites) != 0 {
				t.Errorf("ready must not write state, got %v", db.stateWrites)
			}
		})
	}
}

func TestReadyIsIdempotent(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderPreparing)

	first := orderFrom(t, do(t, db, http.MethodPost, "/orders/o1/ready", ""))
	second := orderFrom(t, do(t, db, http.MethodPost, "/orders/o1/ready", ""))

	if first.FoodReady == nil || second.FoodReady == nil {
		t.Fatal("foodReady missing")
	}
	if !first.FoodReady.Equal(*second.FoodReady) {
		t.Errorf("a second /ready moved the timestamp: %v -> %v", first.FoodReady, second.FoodReady)
	}
}

func TestReadyMissingAndFailing(t *testing.T) {
	wantStatus(t, do(t, newFakeDB(), http.MethodPost, "/orders/ghost/ready", ""), http.StatusNotFound)

	db := newFakeDB()
	db.seed("o1", domain.OrderPreparing)
	db.readyErr = errors.New("boom")
	wantStatus(t, do(t, db, http.MethodPost, "/orders/o1/ready", ""), http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// POST /orders/{id}/pickup
// ---------------------------------------------------------------------------

func TestPickupRequiresFoodAndDriver(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name       string
		food       *time.Time
		driver     string
		wantStatus int
		wantMsg    string
	}{
		{"food ready and driver assigned", &now, "driver-1", http.StatusOK, ""},
		{"food not ready, driver assigned", nil, "driver-1", http.StatusConflict, "Food is not ready"},
		{"food ready, no driver", &now, "", http.StatusConflict, "No driver"},
		{"neither (food is reported first)", nil, "", http.StatusConflict, "Food is not ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB()
			db.seedWith("o1", domain.OrderPreparing, func(o *domain.Order) {
				o.FoodReady, o.AssignedDriverID = tt.food, tt.driver
			})
			rec := do(t, db, http.MethodPost, "/orders/o1/pickup", "")
			wantStatus(t, rec, tt.wantStatus)

			if tt.wantStatus == http.StatusOK {
				if got := orderFrom(t, rec).State; got != domain.OrderPickedUp {
					t.Errorf("state = %s, want picked_up", got)
				}
				return
			}
			wantErrorContains(t, rec, tt.wantMsg)
			if got := db.orders["o1"].State; got != domain.OrderPreparing {
				t.Errorf("a refused pickup must not change state, got %s", got)
			}
		})
	}
}

func TestPickupOnlyFromPreparing(t *testing.T) {
	now := time.Now()
	for _, from := range allStates {
		if from == domain.OrderPreparing {
			continue
		}
		t.Run("from "+string(from), func(t *testing.T) {
			db := newFakeDB()
			// Both facts are true, so only the state can be the reason for refusing.
			db.seedWith("o1", from, func(o *domain.Order) {
				o.FoodReady, o.AssignedDriverID = &now, "driver-1"
			})
			rec := do(t, db, http.MethodPost, "/orders/o1/pickup", "")
			wantStatus(t, rec, http.StatusConflict)
			wantErrorContains(t, rec, string(from))
			if got := db.orders["o1"].State; got != from {
				t.Errorf("state changed to %s", got)
			}
		})
	}
}

func TestPickupChecksStateBeforeFacts(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderCreated) // no food, no driver, and wrong state
	rec := do(t, db, http.MethodPost, "/orders/o1/pickup", "")
	wantStatus(t, rec, http.StatusConflict)
	if msg := errorFrom(t, rec); strings.Contains(msg, "Food is not ready") {
		t.Errorf("the state problem should be reported, not the food: %q", msg)
	}
}

func TestPickupMissingAndFailing(t *testing.T) {
	wantStatus(t, do(t, newFakeDB(), http.MethodPost, "/orders/ghost/pickup", ""), http.StatusNotFound)

	now := time.Now()
	db := newFakeDB()
	db.seedWith("o1", domain.OrderPreparing, func(o *domain.Order) {
		o.FoodReady, o.AssignedDriverID = &now, "driver-1"
	})
	db.updateErr = errors.New("boom")
	wantStatus(t, do(t, db, http.MethodPost, "/orders/o1/pickup", ""), http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// POST /orders/{id}/accept
// ---------------------------------------------------------------------------

func TestAcceptOnlyFromPaid(t *testing.T) {
	for _, from := range allStates {
		t.Run("from "+string(from), func(t *testing.T) {
			db := newFakeDB()
			db.seed("o1", from)
			rec := do(t, db, http.MethodPost, "/orders/o1/accept", "")

			if from != domain.OrderPaid {
				wantStatus(t, rec, http.StatusConflict)
				if len(db.acceptDriverIDs) != 0 {
					t.Error("a refused accept must not assign a driver")
				}
				if got := db.orders["o1"].AssignedDriverID; got != "" {
					t.Errorf("driver was set to %q", got)
				}
				return
			}
			wantStatus(t, rec, http.StatusOK)
			got := orderFrom(t, rec)
			if got.State != domain.OrderPreparing {
				t.Errorf("state = %s, want preparing", got.State)
			}
			if _, err := uuid.Parse(got.AssignedDriverID); err != nil {
				t.Errorf("assignedDriverId %q is not a UUID: %v", got.AssignedDriverID, err)
			}
			if got.FoodReady != nil {
				t.Error("accepting must not mark the food ready")
			}
		})
	}
}

func TestAcceptAssignsDriverAndStateTogether(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderPaid)
	wantStatus(t, do(t, db, http.MethodPost, "/orders/o1/accept", ""), http.StatusOK)

	if len(db.acceptDriverIDs) != 1 {
		t.Fatalf("AcceptOrder called %d times, want 1", len(db.acceptDriverIDs))
	}
	if len(db.stateWrites) != 0 {
		t.Errorf("accept must be one write via AcceptOrder, not a separate state write: %v", db.stateWrites)
	}
	stored := db.orders["o1"]
	if stored.State != domain.OrderPreparing || stored.AssignedDriverID != db.acceptDriverIDs[0] {
		t.Errorf("stored order is inconsistent: %+v", stored)
	}
}

func TestAcceptGivesEachOrderItsOwnDriver(t *testing.T) {
	db := newFakeDB()
	db.seed("a", domain.OrderPaid)
	db.seed("b", domain.OrderPaid)
	wantStatus(t, do(t, db, http.MethodPost, "/orders/a/accept", ""), http.StatusOK)
	wantStatus(t, do(t, db, http.MethodPost, "/orders/b/accept", ""), http.StatusOK)

	if db.orders["a"].AssignedDriverID == db.orders["b"].AssignedDriverID {
		t.Error("two orders were given the same driver ID")
	}
}

func TestAcceptMissingAndFailing(t *testing.T) {
	wantStatus(t, do(t, newFakeDB(), http.MethodPost, "/orders/ghost/accept", ""), http.StatusNotFound)

	db := newFakeDB()
	db.seed("o1", domain.OrderPaid)
	db.acceptErr = errors.New("boom")
	wantStatus(t, do(t, db, http.MethodPost, "/orders/o1/accept", ""), http.StatusInternalServerError)
	if db.orders["o1"].State != domain.OrderPaid {
		t.Errorf("a failed accept changed state to %s", db.orders["o1"].State)
	}
}

// ---------------------------------------------------------------------------
// POST /orders/{id}/pay (mock)
// ---------------------------------------------------------------------------

func TestPayOnlyFromCreated(t *testing.T) {
	for _, from := range allStates {
		t.Run("from "+string(from), func(t *testing.T) {
			db := newFakeDB()
			db.seed("o1", from)
			rec := do(t, db, http.MethodPost, "/orders/o1/pay", "")

			if from != domain.OrderCreated {
				wantStatus(t, rec, http.StatusConflict)
				wantErrorContains(t, rec, "cannot be paid", string(from))
				if len(db.stateWrites) != 0 {
					t.Errorf("a refused payment must not write, got %v", db.stateWrites)
				}
				return
			}
			wantStatus(t, rec, http.StatusOK)
			if got := orderFrom(t, rec).State; got != domain.OrderPaid {
				t.Errorf("state = %s, want paid", got)
			}
		})
	}
}

func TestPayGoesThroughPaymentPending(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderCreated)
	wantStatus(t, do(t, db, http.MethodPost, "/orders/o1/pay", ""), http.StatusOK)

	want := []domain.OrderState{domain.OrderPaymentPending, domain.OrderPaid}
	if !slices.Equal(db.stateWrites, want) {
		t.Errorf("state writes = %v, want %v", db.stateWrites, want)
	}
}

func TestPayFailureModes(t *testing.T) {
	t.Run("unknown order", func(t *testing.T) {
		wantStatus(t, do(t, newFakeDB(), http.MethodPost, "/orders/ghost/pay", ""), http.StatusNotFound)
	})
	t.Run("first hop fails: nothing changes", func(t *testing.T) {
		db := newFakeDB()
		db.seed("o1", domain.OrderCreated)
		db.updateErr, db.updateErrOn = errors.New("boom"), domain.OrderPaymentPending
		wantStatus(t, do(t, db, http.MethodPost, "/orders/o1/pay", ""), http.StatusInternalServerError)
		if got := db.orders["o1"].State; got != domain.OrderCreated {
			t.Errorf("state = %s, want created", got)
		}
	})
	// Documents a known limitation of the mock: the two hops are separate
	// writes, so a failure in the second leaves the order in payment_pending.
	t.Run("second hop fails: order is left in payment_pending", func(t *testing.T) {
		db := newFakeDB()
		db.seed("o1", domain.OrderCreated)
		db.updateErr, db.updateErrOn = errors.New("boom"), domain.OrderPaid
		wantStatus(t, do(t, db, http.MethodPost, "/orders/o1/pay", ""), http.StatusInternalServerError)
		if got := db.orders["o1"].State; got != domain.OrderPaymentPending {
			t.Errorf("state = %s, want payment_pending", got)
		}
	})
}

// ---------------------------------------------------------------------------
// the whole journey
// ---------------------------------------------------------------------------

func TestFullLifecycle(t *testing.T) {
	db := newFakeDB()
	wantStatus(t, do(t, db, http.MethodPost, "/orders",
		`{"id":"o1","customerId":"c","restaurantId":"r","items":[]}`), http.StatusCreated)

	steps := []struct {
		path      string
		want      int
		wantState domain.OrderState
	}{
		{"/orders/o1/accept", http.StatusConflict, domain.OrderCreated}, // cannot skip paying
		{"/orders/o1/pay", http.StatusOK, domain.OrderPaid},
		{"/orders/o1/pay", http.StatusConflict, domain.OrderPaid}, // cannot pay twice
		{"/orders/o1/accept", http.StatusOK, domain.OrderPreparing},
		{"/orders/o1/pickup", http.StatusConflict, domain.OrderPreparing}, // food not ready
		{"/orders/o1/ready", http.StatusOK, domain.OrderPreparing},
		{"/orders/o1/pickup", http.StatusOK, domain.OrderPickedUp},
		{"/orders/o1/cancel", http.StatusConflict, domain.OrderPickedUp}, // too late to cancel
		{"/orders/o1/deliver", http.StatusOK, domain.OrderDelivered},
		{"/orders/o1/deliver", http.StatusConflict, domain.OrderDelivered}, // terminal
		{"/orders/o1/cancel", http.StatusConflict, domain.OrderDelivered},
	}
	for _, step := range steps {
		rec := do(t, db, http.MethodPost, step.path, "")
		if rec.Code != step.want {
			t.Fatalf("POST %s = %d, want %d (body: %s)", step.path, rec.Code, step.want, rec.Body.String())
		}
		if got := db.orders["o1"].State; got != step.wantState {
			t.Fatalf("after POST %s state = %s, want %s", step.path, got, step.wantState)
		}
	}

	final := orderFrom(t, do(t, db, http.MethodGet, "/orders/o1", ""))
	if final.State != domain.OrderDelivered || final.FoodReady == nil || final.AssignedDriverID == "" {
		t.Errorf("final order is missing facts the pickup relied on: %+v", final)
	}
}
