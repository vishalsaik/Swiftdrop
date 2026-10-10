package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/order"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Server holds everything a handler might need. Handlers are methods on
// it so they all share the same DB pool and router without any globals.
type Server struct {
	Router *mux.Router
	DB     ports.IDatabase
}

// NewServer builds the router and wires every route to its handler.
func NewServer(db ports.IDatabase) *Server {
	s := &Server{
		Router: mux.NewRouter(),
		DB:     db,
	}
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
		// 404 is decided by matching the adapter's error text.
		if strings.Contains(err.Error(), "not found") {
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
	var orderItem domain.Order
	err := json.NewDecoder(r.Body).Decode(&orderItem)
	if err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	// A new order always starts as "created"; the client doesn't choose its state.
	orderItem.State = domain.OrderCreated
	// 2. Call DB with request context and handle the database outcome
	// Replaced context.Background() with r.Context()
	savedOrder, err := s.DB.CreateOrder(r.Context(), orderItem)
	if err != nil {
		log.Printf("database insertion error: %v", err)

		// Return 500 error as a structured JSON object
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to create order due to an internal server error"})
		return
	}

	// 3. Write success header and encode the SAVED order returned from DB
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	// Use savedOrder here since it contains modifications made by the DB (like serial IDs or default values)
	json.NewEncoder(w).Encode(savedOrder)
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

// PayOrder is a MOCK payment: it moves created -> payment_pending -> paid
// without calling any provider. The real flow will authorize the payment
// between those two moves (and carry an idempotency key); the hop that
// makes this a two-step move is deliberately kept so that code slots in
// between them.
func (s *Server) PayOrder(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	// Only a freshly created order can start paying.
	if !order.CanTransition(o.State, domain.OrderPaymentPending) {
		writeIllegalTransition(w, o, domain.OrderPaid)
		return
	}
	if _, err := s.DB.UpdateOrderState(r.Context(), o.ID, string(domain.OrderPaymentPending)); err != nil {
		log.Printf("database update error for %s: %v", o.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to start payment due to an internal server error")
		return
	}
	// <-- the real payment provider call goes here.
	s.saveState(w, r, o.ID, domain.OrderPaid)
}
