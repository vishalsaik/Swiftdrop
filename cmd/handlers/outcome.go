package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// A provider's answer is a fact about money, and the order is a fact about the
// kitchen. Usually they change together (FinishPayment). But an order can be
// cancelled while a payment is in flight; then the answer arrives for an order
// that no longer waits for it. The order must stay cancelled, and the answer
// must still be kept: an authorized payment on a cancelled order is exactly what
// the refund job looks for. Dropping it would leave money taken with nothing to
// say so.

// recordOrphanedOutcome keeps the provider's answer for a payment whose order
// moved on, without touching the order. It is guarded by the payment still being
// processing, so a late or repeated writer cannot overwrite an outcome; finding
// it already recorded is fine.
func (s *Server) recordOrphanedOutcome(ctx context.Context, p domain.Payment, status domain.PaymentStatus, providerPaymentID, reason string) error {
	_, err := s.DB.CompletePayment(ctx, p.ID, status, providerPaymentID, reason)
	if errors.Is(err, ports.ErrPaymentNotProcessing) {
		return nil
	}
	return err
}

// keepOutcome is that for the /pay request that just got the provider's answer,
// and answers it. cause is the state conflict that revealed the order had moved.
func (s *Server) keepOutcome(w http.ResponseWriter, r *http.Request, p domain.Payment, status domain.PaymentStatus, providerPaymentID, reason string, cause error) {
	if err := s.recordOrphanedOutcome(r.Context(), p, status, providerPaymentID, reason); err != nil {
		// The money may have moved and we could not write it down.
		s.raiseAlert(r.Context(), domain.AlertUnrecordedCharge, p.ID, p.OrderID,
			"payment %s for order %s was answered %q after the order moved on (%v), and the answer could not be recorded: %v",
			p.ID, p.OrderID, status, cause, err)
		writeError(w, http.StatusInternalServerError, "Failed to record the payment due to an internal server error")
		return
	}
	if status != domain.PaymentAuthorized {
		// A decline moved no money, so there is nothing more to do: the caller
		// just learns the order changed.
		writeWriteError(w, p.OrderID, cause, "Failed to record the payment due to an internal server error")
		return
	}
	log.Printf("REFUND DUE: payment %s for order %s was authorized after the order moved on: %v", p.ID, p.OrderID, cause)
	msg := fmt.Sprintf("order %s changed while the payment was being processed", p.OrderID)
	if latest, err := s.DB.GetOrderbyId(r.Context(), p.OrderID); err == nil && latest.State == domain.OrderCancelled {
		msg = fmt.Sprintf("order %s was cancelled while the payment was being processed; the payment has been recorded and will be refunded", p.OrderID)
	}
	writeError(w, http.StatusConflict, msg)
}
