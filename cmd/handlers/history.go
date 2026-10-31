package handlers

import (
	"log"
	"net/http"

	"swiftdrop/internal/domain"
)

// PaymentHistory answers with everything that happened to an order's payments,
// oldest first: when each was started, taken over, answered, and every refund
// attempt. It is read from the ledger the database keeps, so it is the record of
// what happened rather than a summary of where things stand. It is for the people
// running the service, not for a customer: it names provider ids and errors.
func (s *Server) PaymentHistory(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	events, err := s.DB.ListPaymentEvents(r.Context(), o.ID)
	if err != nil {
		log.Printf("database query error for the payment history of %s: %v", o.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to read the payment history due to an internal server error")
		return
	}
	if events == nil {
		events = []domain.PaymentEvent{} // an empty list, never null
	}
	writeJSON(w, http.StatusOK, map[string][]domain.PaymentEvent{"events": events})
}
