package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

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
}

func GetHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write([]byte("OK"))
}

func (s *Server) CreateOrder(w http.ResponseWriter, r *http.Request) {
	var orderItem domain.Order
	err := json.NewDecoder(r.Body).Decode(&orderItem)
	if err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
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
	w.Header().Set("Content-Type", "application/json")

	vars := mux.Vars(r)
	orderID := vars["id"]
	if orderID == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Missing order ID route parameter"})
		return
	}
	order, err := s.DB.GetOrderbyId(r.Context(), orderID)
	if err != nil {
		log.Printf("database query error for %s: %v", orderID, err)

		// 404 Condition checked via the adapter error format match
		if strings.Contains(err.Error(), "not found") {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Order %s could not be found", orderID)})
			return
		}

		// Return 500 database access error as a structural JSON object
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Internal database verification step failed"})
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(order)
}
