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
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/payment/providers"
)

// ---------------------------------------------------------------------------
// fakeDB: an in-memory ports.IDatabase that behaves like the Postgres adapter
// (same "order not found: <id>" error text, persists only the columns INSERT
// writes) and can be told to fail, so handlers are tested without Postgres.
// ---------------------------------------------------------------------------

var _ ports.IDatabase = (*fakeDB)(nil)

type fakeDB struct {
	// mu makes the fake safe for the concurrent-request tests, and makes each
	// method atomic the way a single SQL statement is.
	mu     sync.Mutex
	orders map[string]domain.Order

	// beforeWrite, if set, runs inside a state write just before its
	// compare-and-set check (with mu held). Tests use it to simulate another
	// request changing the order after this request read it.
	beforeWrite func(f *fakeDB, id string)

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

// notFound and the duplicate error below use the same ports sentinels as the
// real adapter, wrapped with the id, so handlers are tested against the real
// contract instead of error text.
func notFound(id string) error { return fmt.Errorf("%w: %s", ports.ErrOrderNotFound, id) }

func (f *fakeDB) CreateOrder(_ context.Context, o domain.Order) (domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return domain.Order{}, f.createErr
	}
	if _, dup := f.orders[o.ID]; dup {
		return domain.Order{}, fmt.Errorf("%w: %s", ports.ErrOrderExists, o.ID)
	}
	stored := domain.Order{
		ID: o.ID, PaymentType: o.PaymentType, CustomerID: o.CustomerID,
		RestaurantID: o.RestaurantID, Items: o.Items, State: o.State,
	}
	f.orders[o.ID] = stored
	return stored, nil
}

func (f *fakeDB) GetOrderbyId(_ context.Context, id string) (domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return domain.Order{}, f.getErr
	}
	o, ok := f.orders[id]
	if !ok {
		return domain.Order{}, notFound(id)
	}
	return o, nil
}

// stateConflict is what the real adapter returns when the order is no longer
// in the state the caller expected.
func stateConflict(id string) error { return fmt.Errorf("%w: %s", ports.ErrStateConflict, id) }

func (f *fakeDB) UpdateOrderState(_ context.Context, id string, from, to string) (domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeWrite != nil {
		f.beforeWrite(f, id)
	}
	if f.updateErr != nil && (f.updateErrOn == "" || string(f.updateErrOn) == to) {
		return domain.Order{}, f.updateErr
	}
	o, ok := f.orders[id]
	if !ok {
		return domain.Order{}, notFound(id)
	}
	if string(o.State) != from { // the compare of compare-and-set
		return domain.Order{}, stateConflict(id)
	}
	o.State = domain.OrderState(to)
	f.orders[id] = o
	f.stateWrites = append(f.stateWrites, o.State)
	return o, nil
}

func (f *fakeDB) SetFoodReady(_ context.Context, id string) (domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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

func (f *fakeDB) AcceptOrder(_ context.Context, id, driverID, from, to string) (domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeWrite != nil {
		f.beforeWrite(f, id)
	}
	if f.acceptErr != nil {
		return domain.Order{}, f.acceptErr
	}
	o, ok := f.orders[id]
	if !ok {
		return domain.Order{}, notFound(id)
	}
	if string(o.State) != from {
		return domain.Order{}, stateConflict(id)
	}
	o.State = domain.OrderState(to)
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
		PaymentType: domain.PaymentCash,
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

// testPayRouter returns a payment.Router wired with a MockGateway so handler
// tests don't need a real Stripe key or wallet service.
func testPayRouter() *payment.Router {
	mock := &payment.MockGateway{}
	return payment.NewRouter(
		&providers.CardProvider{Gateway: mock},
		&providers.CashProvider{},
		&providers.WalletProvider{Gateway: mock},
	)
}

// do sends a request through the real router. A request with a body is sent
// as application/json, like a well-behaved client.
func do(t *testing.T, db *fakeDB, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doWith(t, db, method, path, body, "application/json")
}

// doWith is do with an explicit Content-Type ("" sends none).
func doWith(t *testing.T, db *fakeDB, method, path, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" && contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	NewServer(db, testPayRouter()).Router.ServeHTTP(rec, req)
	return rec
}

// validBody is a create request that passes every validation rule.
const validItems = `[{"sku":"S1","name":"Burger","quantity":2,"unitPriceCents":599}]`

func validBody(id string) string {
	return `{"id":"` + id + `","customerId":"c","restaurantId":"r","paymentType":"card","items":` + validItems + `}`
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
	s := NewServer(newFakeDB(), testPayRouter())
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

// withFields appends extra JSON members to a JSON object body.
func withFields(body, extra string) string {
	return strings.TrimSuffix(body, "}") + "," + extra + "}"
}

func TestCreateOrderAlwaysStartsAsCreated(t *testing.T) {
	for _, clientState := range []string{"", "preparing", "picked_up", "delivered", "banana"} {
		t.Run("client sends "+clientState, func(t *testing.T) {
			db := newFakeDB()
			body := validBody("o1")
			if clientState != "" {
				body = withFields(body, `"state":"`+clientState+`"`)
			}

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
	body := withFields(validBody("o1"), `"assignedDriverId":"hacker","foodReady":"2020-01-01T00:00:00Z"`)
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
	body := `{"id":"o1","customerId":"cust-9","restaurantId":"rest-3","paymentType":"wallet",` +
		`"items":[{"sku":"S1","name":"Burger","quantity":2,"unitPriceCents":599}]}`
	rec := do(t, db, http.MethodPost, "/orders", body)
	wantStatus(t, rec, http.StatusCreated)

	got := db.orders["o1"]
	if got.CustomerID != "cust-9" || got.RestaurantID != "rest-3" {
		t.Errorf("wrong ids stored: %+v", got)
	}
	if got.PaymentType != domain.PaymentWallet {
		t.Errorf("paymentType stored = %q, want wallet", got.PaymentType)
	}
	if len(got.Items) != 1 || got.Items[0].Quantity != 2 || got.Items[0].UnitPriceCents != 599 {
		t.Errorf("wrong items stored: %+v", got.Items)
	}
	if resp := orderFrom(t, rec); resp.PaymentType != domain.PaymentWallet {
		t.Errorf("response paymentType = %q, want wallet", resp.PaymentType)
	}
}

func TestCreateOrderAcceptsEveryPaymentType(t *testing.T) {
	for _, pt := range domain.PaymentTypes {
		t.Run(string(pt), func(t *testing.T) {
			body := strings.Replace(validBody("o1"), `"card"`, `"`+string(pt)+`"`, 1)
			wantStatus(t, do(t, newFakeDB(), http.MethodPost, "/orders", body), http.StatusCreated)
		})
	}
}

func TestCreateOrderRejectsBadBodies(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      "not valid json",
		"truncated":     `{"id":"o1",`,
		"wrong type":    `{"id":123}`,
		"array not obj": `[]`,
		"unknown field": withFields(validBody("o1"), `"bogus":1`),
	} {
		t.Run(name, func(t *testing.T) {
			db := newFakeDB()
			rec := do(t, db, http.MethodPost, "/orders", body)
			wantStatus(t, rec, http.StatusBadRequest)
			wantContentTypeJSON(t, rec)
			wantErrorContains(t, rec, "invalid request body")
			if len(db.orders) != 0 {
				t.Errorf("a rejected request must not store anything, got %d orders", len(db.orders))
			}
		})
	}
}

func TestCreateOrderNamesTheUnknownField(t *testing.T) {
	rec := do(t, newFakeDB(), http.MethodPost, "/orders", withFields(validBody("o1"), `"bogus":1`))
	wantStatus(t, rec, http.StatusBadRequest)
	wantErrorContains(t, rec, "bogus")
}

func TestCreateOrderValidationRules(t *testing.T) {
	const ok = `"customerId":"c","restaurantId":"r","paymentType":"card"`
	item := `{"sku":"S","name":"B","quantity":1,"unitPriceCents":100}`
	tests := []struct {
		name string
		body string
		want string
	}{
		{"missing id", `{` + ok + `,"items":[` + item + `]}`, "id is required"},
		{"blank id", `{"id":"   ",` + ok + `,"items":[` + item + `]}`, "id is required"},
		{"missing customerId", `{"id":"o1","restaurantId":"r","paymentType":"card","items":[` + item + `]}`, "customerId is required"},
		{"missing restaurantId", `{"id":"o1","customerId":"c","paymentType":"card","items":[` + item + `]}`, "restaurantId is required"},
		{"missing paymentType", `{"id":"o1","customerId":"c","restaurantId":"r","items":[` + item + `]}`, "paymentType must be one of: card, cash, wallet"},
		{"unknown paymentType", `{"id":"o1","customerId":"c","restaurantId":"r","paymentType":"bitcoin","items":[` + item + `]}`, "paymentType must be one of"},
		{"no items field", `{"id":"o1",` + ok + `}`, "items must not be empty"},
		{"empty items", `{"id":"o1",` + ok + `,"items":[]}`, "items must not be empty"},
		{"zero quantity", `{"id":"o1",` + ok + `,"items":[{"sku":"S","name":"B","quantity":0,"unitPriceCents":100}]}`, "items[0].quantity must be greater than 0"},
		{"negative quantity", `{"id":"o1",` + ok + `,"items":[{"sku":"S","name":"B","quantity":-2,"unitPriceCents":100}]}`, "items[0].quantity"},
		{"negative price reports its index", `{"id":"o1",` + ok + `,"items":[` + item + `,{"sku":"S","name":"B","quantity":1,"unitPriceCents":-1}]}`, "items[1].unitPriceCents must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB()
			rec := do(t, db, http.MethodPost, "/orders", tt.body)
			wantStatus(t, rec, http.StatusUnprocessableEntity)
			wantContentTypeJSON(t, rec)
			wantErrorContains(t, rec, tt.want)
			if len(db.orders) != 0 {
				t.Errorf("an invalid order must not be stored, got %d", len(db.orders))
			}
		})
	}
}

func TestCreateOrderZeroPriceIsAllowed(t *testing.T) {
	body := `{"id":"o1","customerId":"c","restaurantId":"r","paymentType":"cash",` +
		`"items":[{"sku":"S","name":"Free sample","quantity":1,"unitPriceCents":0}]}`
	wantStatus(t, do(t, newFakeDB(), http.MethodPost, "/orders", body), http.StatusCreated)
}

func TestCreateOrderReportsOnlyTheFirstFailure(t *testing.T) {
	// Breaks every rule at once; the chain must stop at the first (id).
	rec := do(t, newFakeDB(), http.MethodPost, "/orders", `{"paymentType":"bitcoin","items":[]}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	if got := errorFrom(t, rec); got != "id is required" {
		t.Errorf("error = %q, want only the first failure (id is required)", got)
	}
}

func TestCreateOrderDuplicateID(t *testing.T) {
	db := newFakeDB()
	wantStatus(t, do(t, db, http.MethodPost, "/orders", validBody("o1")), http.StatusCreated)
	original := db.orders["o1"]

	other := strings.Replace(validBody("o1"), `"customerId":"c"`, `"customerId":"someone-else"`, 1)
	rec := do(t, db, http.MethodPost, "/orders", other)
	wantStatus(t, rec, http.StatusConflict)
	wantContentTypeJSON(t, rec)
	wantErrorContains(t, rec, "o1", "already exists")
	if db.orders["o1"].CustomerID != original.CustomerID {
		t.Error("a rejected duplicate must not overwrite the original order")
	}
}

func TestCreateOrderDatabaseFailure(t *testing.T) {
	db := newFakeDB()
	db.createErr = errors.New("disk full")
	rec := do(t, db, http.MethodPost, "/orders", validBody("o1"))
	wantStatus(t, rec, http.StatusInternalServerError)
	wantContentTypeJSON(t, rec)
	if strings.Contains(rec.Body.String(), "disk full") {
		t.Errorf("internal error text leaked to the client: %s", rec.Body.String())
	}
}

func TestCreateOrderBodyTooLarge(t *testing.T) {
	db := newFakeDB()
	body := `{"id":"o1","customerId":"` + strings.Repeat("a", maxBodyBytes+10) + `"}`
	rec := do(t, db, http.MethodPost, "/orders", body)
	wantStatus(t, rec, http.StatusRequestEntityTooLarge)
	wantContentTypeJSON(t, rec)
	wantErrorContains(t, rec, "too large")
	if len(db.orders) != 0 {
		t.Errorf("an oversized request must not store anything, got %d", len(db.orders))
	}
}

func TestCreateOrderContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        int
	}{
		{"application/json", "application/json", http.StatusCreated},
		{"with charset", "application/json; charset=utf-8", http.StatusCreated},
		{"different case", "Application/JSON", http.StatusCreated},
		{"text/plain", "text/plain", http.StatusUnsupportedMediaType},
		{"form encoded", "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"only a prefix match", "application/jsonx", http.StatusUnsupportedMediaType},
		{"missing", "", http.StatusUnsupportedMediaType},
		{"unparseable", ";;;", http.StatusUnsupportedMediaType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB()
			rec := doWith(t, db, http.MethodPost, "/orders", validBody("o1"), tt.contentType)
			wantStatus(t, rec, tt.want)
			if tt.want == http.StatusUnsupportedMediaType {
				wantContentTypeJSON(t, rec)
				wantErrorContains(t, rec, "application/json")
				if len(db.orders) != 0 {
					t.Error("a rejected request must not store anything")
				}
			}
		})
	}
}

func TestBodilessPostsSkipTheContentTypeCheck(t *testing.T) {
	// The lifecycle endpoints are POSTs with no body and no Content-Type.
	db := newFakeDB()
	db.seed("o1", domain.OrderPaid)
	rec := doWith(t, db, http.MethodPost, "/orders/o1/accept", "", "")
	wantStatus(t, rec, http.StatusOK)
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
// POST /orders/{id}/pay
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
	// Documents a known gap: the payment was authorised but saving "paid"
	// failed, so the order is left in payment_pending while the provider may
	// already hold the money. Closing it needs a payment record (the claim).
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

// ---- pay: provider outcomes -------------------------------------------------

// recordingGateway is a payment.Gateway that returns what the test gave it
// and remembers every request it was asked to charge.
type recordingGateway struct {
	mu     sync.Mutex // the concurrent-request tests call Charge from many goroutines
	result *domain.PaymentResult
	err    error
	calls  []domain.PaymentRequest
}

func (g *recordingGateway) Charge(_ context.Context, req domain.PaymentRequest) (*domain.PaymentResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, req)
	return g.result, g.err
}

func approving() *recordingGateway {
	return &recordingGateway{result: &domain.PaymentResult{PaymentID: "p1", Status: domain.PaymentAuthorized}}
}

func declining(reason string) *recordingGateway {
	return &recordingGateway{result: &domain.PaymentResult{PaymentID: "p1", Status: domain.PaymentDeclined, Reason: reason}}
}

func routerOf(card, wallet payment.Gateway) *payment.Router {
	return payment.NewRouter(
		&providers.CardProvider{Gateway: card},
		&providers.CashProvider{},
		&providers.WalletProvider{Gateway: wallet},
	)
}

// seedPayable stores a created order of the given payment type whose items add
// up to totalCents.
func seedPayable(db *fakeDB, id string, pt domain.PaymentType, totalCents int64) {
	db.seedWith(id, domain.OrderCreated, func(o *domain.Order) {
		o.PaymentType = pt
		o.Items = []domain.OrderItem{{SKU: "S1", Name: "Item", Quantity: 1, UnitPriceCents: totalCents}}
	})
}

// payWith posts /pay through a server built on the given payment router.
func payWith(t *testing.T, db *fakeDB, router *payment.Router, id string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	NewServer(db, router).Router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/orders/"+id+"/pay", nil))
	return rec
}

func wantWrites(t *testing.T, db *fakeDB, want ...domain.OrderState) {
	t.Helper()
	if !slices.Equal(db.stateWrites, want) {
		t.Errorf("state writes = %v, want %v", db.stateWrites, want)
	}
}

func TestPayUsesTheProviderOfTheOrdersPaymentType(t *testing.T) {
	tests := []struct {
		pt                   domain.PaymentType
		wantCard, wantWallet int
	}{
		{domain.PaymentCard, 1, 0},
		{domain.PaymentWallet, 0, 1},
		{domain.PaymentCash, 0, 0}, // cash never reaches a gateway
	}
	for _, tt := range tests {
		t.Run(string(tt.pt), func(t *testing.T) {
			card, wallet := approving(), approving()
			db := newFakeDB()
			seedPayable(db, "o1", tt.pt, 1000)
			wantStatus(t, payWith(t, db, routerOf(card, wallet), "o1"), http.StatusOK)
			if len(card.calls) != tt.wantCard || len(wallet.calls) != tt.wantWallet {
				t.Errorf("gateway calls: card=%d wallet=%d, want card=%d wallet=%d",
					len(card.calls), len(wallet.calls), tt.wantCard, tt.wantWallet)
			}
		})
	}
}

func TestPayCashIsPaidEvenIfEveryGatewayDeclines(t *testing.T) {
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCash, 1_000_000)
	g := declining("no")
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusOK)
	if got := db.orders["o1"].State; got != domain.OrderPaid {
		t.Errorf("state = %s, want paid", got)
	}
}

func TestPaySendsTheOrderFactsToTheGateway(t *testing.T) {
	g := approving()
	db := newFakeDB()
	db.seedWith("o1", domain.OrderCreated, func(o *domain.Order) {
		o.PaymentType = domain.PaymentCard
		o.Items = []domain.OrderItem{
			{SKU: "A", Name: "Burger", Quantity: 2, UnitPriceCents: 599},
			{SKU: "B", Name: "Fries", Quantity: 1, UnitPriceCents: 249},
		}
	})
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusOK)
	if len(g.calls) != 1 {
		t.Fatalf("gateway called %d times, want 1", len(g.calls))
	}
	got := g.calls[0]
	if got.OrderID != "o1" || got.AmountCents != 1447 || got.Currency != "USD" {
		t.Errorf("request = %+v, want order o1, 1447 cents, USD", got)
	}
}

func TestPayDeclineCancelsTheOrder(t *testing.T) {
	for _, pt := range []domain.PaymentType{domain.PaymentCard, domain.PaymentWallet} {
		t.Run(string(pt), func(t *testing.T) {
			g := declining("insufficient funds")
			db := newFakeDB()
			seedPayable(db, "o1", pt, 1198)
			router := routerOf(g, g)

			rec := payWith(t, db, router, "o1")
			wantStatus(t, rec, http.StatusPaymentRequired)
			wantErrorContains(t, rec, "insufficient funds")
			wantWrites(t, db, domain.OrderPaymentPending, domain.OrderCancelled)
			if got := db.orders["o1"].State; got != domain.OrderCancelled {
				t.Errorf("state = %s, want cancelled", got)
			}

			// cancelled is terminal: paying again is refused, not retried
			wantStatus(t, payWith(t, db, router, "o1"), http.StatusConflict)
			if len(g.calls) != 1 {
				t.Errorf("gateway called %d times, want 1", len(g.calls))
			}
		})
	}
}

func TestPayDeclineWithoutAReasonGetsAFallback(t *testing.T) {
	g := declining("")
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusPaymentRequired)
	if got := errorFrom(t, rec); got != "payment declined" {
		t.Errorf("error = %q, want the fallback %q", got, "payment declined")
	}
}

// The provider said no, but we could not record it: the client must not be
// told "declined" as if all were well, and the order must not claim cancelled.
func TestPayDeclineThatCannotBeSavedIsAServerError(t *testing.T) {
	g := declining("insufficient funds")
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	db.updateErr, db.updateErrOn = errors.New("db down"), domain.OrderCancelled

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusInternalServerError)
	wantErrorContains(t, rec, "cancelled")
	if got := db.orders["o1"].State; got != domain.OrderPaymentPending {
		t.Errorf("state = %s, want payment_pending", got)
	}
}

// An error is not a decline: we do not know what happened, so the order is
// left in payment_pending and the cause stays in the log, not the response.
func TestPayProviderErrorLeavesTheOrderPending(t *testing.T) {
	g := &recordingGateway{err: errors.New("gateway timeout to 10.0.0.7")}
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusInternalServerError)
	wantErrorContains(t, rec, "authorisation failed")
	if strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("the internal cause leaked to the client: %s", rec.Body.String())
	}
	wantWrites(t, db, domain.OrderPaymentPending)
	if got := db.orders["o1"].State; got != domain.OrderPaymentPending {
		t.Errorf("state = %s, want payment_pending", got)
	}
}

func TestPayProviderReturningNothingIsAnError(t *testing.T) {
	g := &recordingGateway{} // nil result and nil error
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusInternalServerError)
	if got := db.orders["o1"].State; got != domain.OrderPaymentPending {
		t.Errorf("state = %s, want payment_pending", got)
	}
}

// The lookup runs before any write, so an order we cannot pay for stays
// "created" instead of being stranded in payment_pending.
func TestPayUnknownPaymentTypeChangesNothing(t *testing.T) {
	for _, pt := range []domain.PaymentType{"crypto", ""} { // "" is a row from before the column existed
		t.Run("type "+string(pt), func(t *testing.T) {
			g := approving()
			db := newFakeDB()
			seedPayable(db, "o1", pt, 1000)

			rec := payWith(t, db, routerOf(g, g), "o1")
			wantStatus(t, rec, http.StatusInternalServerError)
			wantErrorContains(t, rec, "not available")
			if pt != "" && strings.Contains(rec.Body.String(), string(pt)) {
				t.Errorf("the stored type leaked to the client: %s", rec.Body.String())
			}
			wantWrites(t, db)
			if got := db.orders["o1"].State; got != domain.OrderCreated {
				t.Errorf("state = %s, want created", got)
			}
			if len(g.calls) != 0 {
				t.Errorf("gateway was called %d times for an unpayable order", len(g.calls))
			}
		})
	}
}

// Handler and FakeGateway together: the limits that main.go wires in.
func TestPayWithTheFakeGatewayLimits(t *testing.T) {
	router := routerOf(&payment.FakeGateway{LimitCents: 50_000}, &payment.FakeGateway{LimitCents: 10_000})
	tests := []struct {
		pt        domain.PaymentType
		cents     int64
		want      int
		wantState domain.OrderState
	}{
		{domain.PaymentCard, 50_000, http.StatusOK, domain.OrderPaid},
		{domain.PaymentCard, 50_001, http.StatusPaymentRequired, domain.OrderCancelled},
		{domain.PaymentWallet, 10_000, http.StatusOK, domain.OrderPaid},
		{domain.PaymentWallet, 10_001, http.StatusPaymentRequired, domain.OrderCancelled},
		{domain.PaymentCash, 999_999_999, http.StatusOK, domain.OrderPaid},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s %d", tt.pt, tt.cents), func(t *testing.T) {
			db := newFakeDB()
			seedPayable(db, "o1", tt.pt, tt.cents)
			wantStatus(t, payWith(t, db, router, "o1"), tt.want)
			if got := db.orders["o1"].State; got != tt.wantState {
				t.Errorf("state = %s, want %s", got, tt.wantState)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// the whole journey
// ---------------------------------------------------------------------------

func TestFullLifecycle(t *testing.T) {
	db := newFakeDB()
	wantStatus(t, do(t, db, http.MethodPost, "/orders", validBody("o1")), http.StatusCreated)

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
