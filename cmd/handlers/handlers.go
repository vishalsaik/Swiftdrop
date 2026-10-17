package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/order"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/validation"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Server holds everything a handler might need. Handlers are methods on
// it so they all share the same DB pool and router without any globals.
type Server struct {
	Router  *mux.Router
	DB      ports.IDatabase
	Payment *payment.Router
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
func NewServer(db ports.IDatabase, pay *payment.Router) *Server {
	s := &Server{
		Router:  mux.NewRouter(),
		DB:      db,
		Payment: pay,
	}
	// The middleware chain: links run in this order for every route.
	s.Router.Use(MaxBody(maxBodyBytes))
	s.Router.Use(RequireJSON)
	s.routes()
	return s
}

func (s *Server) routes() {
	s.Router.HandleFunc("/health", GetHealth).Methods("GET")
	s.Router.HandleFunc("/orders", s.CreateOrder).Methods("POST")
	s.Router.HandleFunc("/orders/{id}", s.GetOrderByID).Methods("GET")
	s.Router.HandleFunc("/orders/{id}/cancel", s.CancelOrder).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/ready", s.ReadyState).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/deliver", s.DeliverOrder).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/pickup", s.PickupOrder).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/accept", s.AcceptOrder).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/pay", s.PayOrder).Methods("POST")
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
	s.saveState(w, r, o.ID, to)
}

// writeIllegalTransition writes the 409 for a move the state machine forbids.
func writeIllegalTransition(w http.ResponseWriter, o domain.Order, to domain.OrderState) {
	writeError(w, http.StatusConflict, fmt.Sprintf("order %s cannot be %s from state %s", o.ID, to, o.State))
}

// saveState persists the new state and writes the updated order. The
// caller must already have checked that the move is allowed.
func (s *Server) saveState(w http.ResponseWriter, r *http.Request, id string, to domain.OrderState) {
	updated, err := s.DB.UpdateOrderState(r.Context(), id, string(to))
	if err != nil {
		log.Printf("database update error for %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "Failed to update order due to an internal server error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
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
	if o, ok := s.loadOrder(w, r); ok {
		writeJSON(w, http.StatusOK, o)
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
	updated, err := s.DB.SetFoodReady(r.Context(), o.ID)
	if err != nil {
		log.Printf("database update error for %s: %v", o.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to set food ready due to an internal server error")
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
	s.saveState(w, r, o.ID, domain.OrderPickedUp)
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
	updated, err := s.DB.AcceptOrder(r.Context(), o.ID, driverID, string(domain.OrderPreparing))
	if err != nil {
		log.Printf("database update error for %s: %v", o.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to accept order due to an internal server error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) PayOrder(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	if !order.CanTransition(o.State, domain.OrderPaymentPending) {
		writeIllegalTransition(w, o, domain.OrderPaid)
		return
	}
	// Strategy pattern: pick the provider from the order's payment type, then
	// delegate; the handler never knows which gateway runs. This lookup can
	// fail, so it runs before any state is saved: a failure leaves the order
	// untouched (still "created") instead of stranding it in payment_pending.
	provider, err := s.Payment.For(o.PaymentType)
	if err != nil {
		// Our stored data, not this request, named a type we can't handle
		// (e.g. an order created before the payment_type column existed), so
		// this is a 500, and the real cause goes to the log, not the client.
		log.Printf("no payment provider for order %s (payment type %q): %v", o.ID, o.PaymentType, err)
		writeError(w, http.StatusInternalServerError, "Payment is not available for this order")
		return
	}
	if _, err := s.DB.UpdateOrderState(r.Context(), o.ID, string(domain.OrderPaymentPending)); err != nil {
		log.Printf("database update error for %s: %v", o.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to start payment due to an internal server error")
		return
	}

	var body struct {
		PaymentMethodID string `json:"paymentMethodId"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	req := domain.PaymentRequest{
		OrderID:         o.ID,
		IdempotencyKey:  r.Header.Get("Idempotency-Key"),
		AmountCents:     o.TotalCents(),
		Currency:        "USD",
		PaymentMethodID: body.PaymentMethodID,
	}
	result, err := provider.Authorize(r.Context(), req)
	if err != nil {
		log.Printf("payment authorisation error for %s: %v", o.ID, err)
		writeError(w, http.StatusInternalServerError, "Payment authorisation failed")
		return
	}
	// A provider that returns neither a result nor an error broke its contract.
	// We don't know what happened, so treat it like an error, not a decline.
	if result == nil {
		log.Printf("payment provider returned no result and no error for %s", o.ID)
		writeError(w, http.StatusInternalServerError, "Payment authorisation failed")
		return
	}
	// A decline is a normal answer, not an error: the order ends in a final
	// state (cancelled) and the client gets the reason.
	if result.Status == domain.PaymentDeclined {
		if _, err := s.DB.UpdateOrderState(r.Context(), o.ID, string(domain.OrderCancelled)); err != nil {
			log.Printf("database update error for %s: %v", o.ID, err)
			writeError(w, http.StatusInternalServerError, "Failed to update payment state to cancelled due to an internal server error")
			return
		}
		reason := result.Reason
		if reason == "" {
			reason = "payment declined"
		}
		writeError(w, http.StatusPaymentRequired, reason)
		return
	}

	s.saveState(w, r, o.ID, domain.OrderPaid)
}
