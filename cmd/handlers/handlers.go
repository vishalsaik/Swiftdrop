package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"swiftdrop/internal/auth"
	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/order"
	"swiftdrop/internal/paying"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/validation"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Server holds everything a handler might need. Handlers are methods on
// it so they all share the same DB pool and router without any globals.
type Server struct {
	Router *mux.Router
	// DB is all of storage: orders and payments. The Postgres adapter implements
	// both, which is what lets a payment and its order change in one transaction.
	DB      ports.Store
	Payment *payment.Router
	// Webhook verifies and translates a payment provider's pushed events. It is
	// optional: while nil, the webhook route answers 404. It is a field rather
	// than a NewServer argument because only some deployments have a webhook
	// secret to configure.
	Webhook payment.EventParser
	// Lookup is where /pay first reads an order's payment. It is DB unless a cache
	// is put in front (see internal/paymentcache): the first read of a retried
	// /pay is the one that is asked most, and for a paid order its answer never
	// changes. Everything else reads DB, so a screen never sees a stale refund.
	Lookup PaymentLookup

	// Accounts signs people up and in. Without it nobody can sign in, so every
	// route that needs an account answers 401 (it fails closed).
	Accounts Accounts
	// Identify says who a request is from: the signed-in account behind its cookie,
	// with the CSRF check for requests that change something. NewServer sets it;
	// it is a field so a test can say who is signed in without a cookie.
	Identify func(r *http.Request) (auth.Principal, error)
	// SecureCookies marks the session cookie Secure, so a browser only sends it over
	// HTTPS. It is on whenever the connection itself is TLS; set it too when TLS is
	// ended by a proxy in front of the server.
	SecureCookies bool
}

// PaymentLookup reads the payment of an order.
type PaymentLookup interface {
	GetPaymentByOrder(ctx context.Context, orderID string) (domain.Payment, error)
}

// maxBodyBytes caps every request body.
const maxBodyBytes = 1 << 20 // 1 MB

// allowedOrigins is the CORS allow-list. Only these origins may read our
// responses in a browser. Extend this list when deploying to a real domain.
var allowedOrigins = []string{
	"http://localhost:3000",
	"http://localhost:5173",
}

// Handler returns the root http.Handler for the server: CORS wraps the whole
// router so that even 413/415/422 error responses carry the CORS headers, and
// so that OPTIONS preflights reach the CORS link before mux can 405 them.
func (s *Server) Handler() http.Handler {
	return CORS(allowedOrigins)(s.Router)
}

// NewServer builds the router and wires every route to its handler.
func NewServer(db ports.Store, pay *payment.Router) *Server {
	s := &Server{
		Router:  mux.NewRouter(),
		DB:      db,
		Payment: pay,
		Lookup:  db,
	}
	// The middleware chain: links run in this order for every route.
	s.Router.Use(MaxBody(maxBodyBytes))
	s.Router.Use(RequireJSON)
	s.Identify = s.identifyFromCookie
	s.routes()
	return s
}

func (s *Server) routes() {
	s.Router.HandleFunc("/health", GetHealth).Methods("GET")
	// Who may call what. A customer places orders and sees, pays for and cancels
	// their own; staff run the kitchen and the delivery and see everything. The
	// health check, the sign-in endpoints, the dashboard's files and Stripe's webhook
	// (which proves itself by its signature) need no account.
	s.Router.HandleFunc("/auth/signup", s.SignUp).Methods("POST")
	s.Router.HandleFunc("/auth/login", s.SignIn).Methods("POST")
	s.Router.HandleFunc("/auth/logout", s.signedIn(s.SignOut)).Methods("POST")
	s.Router.HandleFunc("/auth/me", s.signedIn(s.Me)).Methods("GET")
	s.Router.HandleFunc("/auth/password", s.signedIn(s.ChangePassword)).Methods("POST")

	s.Router.HandleFunc("/orders", s.signedIn(s.CreateOrder)).Methods("POST")
	s.Router.HandleFunc("/orders", s.signedIn(s.ListOrders)).Methods("GET")
	s.Router.HandleFunc("/orders/{id}", s.signedIn(s.GetOrderByID)).Methods("GET")
	s.Router.HandleFunc("/orders/{id}/cancel", s.signedIn(s.CancelOrder)).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/pay", s.signedIn(s.PayOrder)).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/ready", s.staffOnly(s.ReadyState)).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/deliver", s.staffOnly(s.DeliverOrder)).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/pickup", s.staffOnly(s.PickupOrder)).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/accept", s.staffOnly(s.AcceptOrder)).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/payment-history", s.staffOnly(s.PaymentHistory)).Methods("GET")
	s.Router.HandleFunc("/alerts", s.staffOnly(s.ListAlerts)).Methods("GET")
	s.Router.HandleFunc("/alerts/{id}/resolve", s.staffOnly(s.ResolveAlert)).Methods("POST")
	s.Router.HandleFunc("/webhooks/stripe", s.PaymentWebhook).Methods("POST")
	s.dashboardRoutes()
}

func GetHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write([]byte("OK"))
}

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError writes {"error": msg} with the given status code.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// loadOrder reads the {id} route parameter and fetches that order. If it
// can't, it has already written the 400/404/500 response and returns
// ok=false, so the caller only has to return.
func (s *Server) loadOrder(w http.ResponseWriter, r *http.Request) (domain.Order, bool) {
	orderID := mux.Vars(r)["id"]
	if orderID == "" {
		writeError(w, http.StatusBadRequest, "Missing order ID route parameter")
		return domain.Order{}, false
	}
	o, err := s.DB.GetOrderbyId(r.Context(), orderID)
	if err != nil {
		log.Printf("database query error for %s: %v", orderID, err)
		if errors.Is(err, ports.ErrOrderNotFound) {
			writeError(w, http.StatusNotFound, fmt.Sprintf("Order %s could not be found", orderID))
			return domain.Order{}, false
		}
		writeError(w, http.StatusInternalServerError, "Internal database verification step failed")
		return domain.Order{}, false
	}
	// Someone else's order does not exist as far as a customer can tell: the same
	// answer as a missing one, so ids cannot be probed.
	if !mayAccess(r, o) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("Order %s could not be found", orderID))
		return domain.Order{}, false
	}
	return o, true
}

// transition moves an order to `to` if the state machine allows it from
// its current state (409 otherwise). It covers every endpoint that is a
// pure change of OrderState. Endpoints that also check other facts (e.g.
// food ready, driver assigned) make their own checks and call saveState.
func (s *Server) transition(w http.ResponseWriter, r *http.Request, to domain.OrderState) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	if !order.CanTransition(o.State, to) {
		writeIllegalTransition(w, o, to)
		return
	}
	s.saveState(w, r, o.ID, o.State, to)
}

// writeIllegalTransition writes the 409 for a move the state machine forbids.
func writeIllegalTransition(w http.ResponseWriter, o domain.Order, to domain.OrderState) {
	writeError(w, http.StatusConflict, fmt.Sprintf("order %s cannot be %s from state %s", o.ID, to, o.State))
}

// saveState persists the move from `from` to `to` and writes the updated
// order. The caller must already have checked that the move is allowed, and
// `from` is the state it read: the write only happens if the order is still in
// it, so a request that lost a race gets a 409 instead of overwriting the
// winner.
func (s *Server) saveState(w http.ResponseWriter, r *http.Request, id string, from, to domain.OrderState) {
	updated, err := s.DB.UpdateOrderState(r.Context(), id, string(from), string(to))
	if err != nil {
		log.Printf("database update error for %s: %v", id, err)
		writeWriteError(w, id, err, "Failed to update order due to an internal server error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// writeWriteError answers a failed state write. A lost race is the caller's to
// retry (409, with the read-then-write it was part of now out of date), an
// order that vanished in between is a 404, and anything else is a 500 with the
// caller's generic message (the real cause is already in the log).
func writeWriteError(w http.ResponseWriter, id string, err error, internalMsg string) {
	switch {
	case errors.Is(err, ports.ErrStateConflict):
		writeError(w, http.StatusConflict, fmt.Sprintf("order %s changed while this request was running, read it again", id))
	case errors.Is(err, ports.ErrOrderNotFound):
		writeError(w, http.StatusNotFound, fmt.Sprintf("Order %s could not be found", id))
	default:
		writeError(w, http.StatusInternalServerError, internalMsg)
	}
}

func (s *Server) CreateOrder(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	// Decode strictly: a field we don't know about is a client mistake worth
	// telling them about, not something to silently drop.
	var orderItem domain.Order
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&orderItem); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// A customer orders for themselves, whatever the body says; staff may place an
	// order for any customer.
	if p := principalFrom(r); p.User.Role == domain.RoleCustomer {
		if orderItem.CustomerID != "" && orderItem.CustomerID != p.User.ID {
			writeError(w, http.StatusForbidden, "an order can only be placed for your own account")
			return
		}
		orderItem.CustomerID = p.User.ID
	}

	// The validator chain: first failing rule wins.
	if err := validation.Order(orderItem); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// A new order always starts as "created"; the client doesn't choose its state.
	orderItem.State = domain.OrderCreated

	savedOrder, err := s.DB.CreateOrder(r.Context(), orderItem)
	if err != nil {
		if errors.Is(err, ports.ErrOrderExists) {
			writeError(w, http.StatusConflict, fmt.Sprintf("order %s already exists", orderItem.ID))
			return
		}
		log.Printf("database insertion error: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to create order due to an internal server error")
		return
	}

	// Respond with the stored row (not the request), so the client sees what
	// the database actually holds.
	writeJSON(w, http.StatusCreated, savedOrder)
}

func (s *Server) GetOrderByID(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	if view, ok := s.loadView(w, r, o); ok {
		writeJSON(w, http.StatusOK, view)
	}
}

func (s *Server) CancelOrder(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, domain.OrderCancelled)
}

func (s *Server) DeliverOrder(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, domain.OrderDelivered)
}

// ReadyState marks the food ready. It doesn't change OrderState (food
// readiness is an independent fact), so it can't use transition.
func (s *Server) ReadyState(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	if o.State != domain.OrderPreparing {
		writeError(w, http.StatusConflict, fmt.Sprintf("order %s cannot be marked ready from state %s", o.ID, o.State))
		return
	}
	updated, err := s.DB.SetFoodReady(r.Context(), o.ID, string(o.State))
	if err != nil {
		log.Printf("database update error for %s: %v", o.ID, err)
		writeWriteError(w, o.ID, err, "Failed to set food ready due to an internal server error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) PickupOrder(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	// 1. the state machine must allow preparing -> picked_up
	if !order.CanTransition(o.State, domain.OrderPickedUp) {
		writeIllegalTransition(w, o, domain.OrderPickedUp)
		return
	}
	// 2. the kitchen has marked the food ready
	if o.FoodReady == nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("Food is not ready yet for %s", o.ID))
		return
	}
	// 3. a driver has been assigned
	if o.AssignedDriverID == "" {
		writeError(w, http.StatusConflict, fmt.Sprintf("No driver was assigned to this order %s", o.ID))
		return
	}
	s.saveState(w, r, o.ID, o.State, domain.OrderPickedUp)
}
func (s *Server) AcceptOrder(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	if !order.CanTransition(o.State, domain.OrderPreparing) {
		writeIllegalTransition(w, o, domain.OrderPreparing)
		return
	}
	// TODO: stand-in for real dispatch. Replace once the driver/dispatch feature exists.
	driverID := uuid.New().String()
	updated, err := s.DB.AcceptOrder(r.Context(), o.ID, driverID, string(o.State), string(domain.OrderPreparing))
	if err != nil {
		log.Printf("database update error for %s: %v", o.ID, err)
		writeWriteError(w, o.ID, err, "Failed to accept order due to an internal server error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// PayOrder pays for an order, and is idempotent: calling it again for the same
// order never charges twice. What a request means, and what it does, is decided by
// internal/paying; this reads the request and writes the answer.
func (s *Server) PayOrder(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	writePayOutcome(w, s.payService().Pay(r.Context(), o, func() (string, error) { return payMethod(r) }))
}

// payService builds the service from the server's current parts, so what a test
// or main swaps in (the payment lookup, the router) is what it uses.
func (s *Server) payService() *paying.Service {
	return &paying.Service{DB: s.DB, Lookup: s.Lookup, Payment: s.Payment, Alert: s.raiseAlert}
}

// payBody is what /pay may carry: the payment method token the client got from
// the provider's own library (Stripe.js). Cash and the fake gateway need none.
type payBody struct {
	PaymentMethodID string `json:"paymentMethodId"`
}

// payMethod reads the payment method from the request body. A body that is not
// JSON is a paying.BadBodyError, an oversized one paying.ErrBodyTooLarge, and no
// body at all is fine: it is just an empty method. The service calls it only when
// it needs the method, so a replay never reads the body.
func payMethod(r *http.Request) (string, error) {
	var body payBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return "", paying.ErrBodyTooLarge
		}
		return "", &paying.BadBodyError{Err: err}
	}
	return body.PaymentMethodID, nil
}

// writePayOutcome turns what became of a request to pay into the HTTP answer.
func writePayOutcome(w http.ResponseWriter, out paying.Outcome) {
	switch out.Kind {
	case paying.Paid:
		writeJSON(w, http.StatusOK, out.Order)
	case paying.Declined:
		writeDecline(w, out.Message, out.CanRetry, out.AttemptsLeft)
	case paying.BadRequest:
		writeError(w, http.StatusBadRequest, out.Message)
	case paying.TooLarge:
		writeError(w, http.StatusRequestEntityTooLarge, out.Message)
	case paying.Unprocessable:
		writeError(w, http.StatusUnprocessableEntity, out.Message)
	case paying.Conflict:
		writeError(w, http.StatusConflict, out.Message)
	case paying.NotFound:
		writeError(w, http.StatusNotFound, out.Message)
	default: // paying.Internal
		writeError(w, http.StatusInternalServerError, out.Message)
	}
}

// writeDecline answers a payment that was declined: 402 with the reason, and
// whether the customer can try again and how many attempts are left.
func writeDecline(w http.ResponseWriter, reason string, canRetry bool, attemptsLeft int) {
	writeJSON(w, http.StatusPaymentRequired, map[string]any{
		"error": reason, "canRetry": canRetry, "attemptsLeft": attemptsLeft,
	})
}
