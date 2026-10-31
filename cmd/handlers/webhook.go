package handlers

import (
	"errors"
	"io"
	"log"
	"net/http"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

// PaymentWebhook receives a payment provider's report of a payment outcome. It
// is the second way an outcome reaches us: PayOrder gets it from the reply to
// its call, and this gets it pushed by the provider, so a charge whose reply was
// lost is still recorded once the provider manages to deliver the event.
//
// It records the outcome with the same FinishPayment PayOrder uses, so whichever
// of the two arrives first wins and the other finds the payment already
// finished and changes nothing. That also makes a duplicate delivery harmless.
//
// The status code is how we talk to the provider's retry logic: a 2xx means
// "stop sending this", so it is used for everything a retry cannot improve
// (an event we do not act on, a payment we do not know, one already recorded).
// A 5xx is kept for when our own storage failed, because a later delivery can
// succeed. A request that fails verification is a 400.
func (s *Server) PaymentWebhook(w http.ResponseWriter, r *http.Request) {
	if s.Webhook == nil {
		writeError(w, http.StatusNotFound, "webhooks are not enabled")
		return
	}
	defer r.Body.Close()

	// The signature covers the exact bytes sent, so read them raw.
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return
	}

	event, err := s.Webhook.ParseEvent(payload, r.Header)
	switch {
	case err == nil:
	case errors.Is(err, payment.ErrEventIgnored):
		log.Printf("webhook: %v", err)
		writeWebhookAck(w, "ignored")
		return
	default:
		log.Printf("webhook rejected: %v", err)
		writeError(w, http.StatusBadRequest, "webhook could not be verified")
		return
	}

	s.recordEvent(w, r, event)
}

// recordEvent applies a verified event to the payment it names.
func (s *Server) recordEvent(w http.ResponseWriter, r *http.Request, event payment.Event) {
	p, err := s.DB.GetPayment(r.Context(), event.PaymentID)
	switch {
	case err == nil:
	case errors.Is(err, ports.ErrPaymentNotFound):
		log.Printf("webhook %s: no payment %s here, nothing to record", event.ID, event.PaymentID)
		writeWebhookAck(w, "unknown payment")
		return
	default:
		log.Printf("webhook %s: database error reading payment %s: %v", event.ID, event.PaymentID, err)
		writeError(w, http.StatusInternalServerError, "Failed to read the payment due to an internal server error")
		return
	}

	if p.Status != domain.PaymentProcessing {
		// Already recorded, by PayOrder or by an earlier delivery of this event.
		if p.Status != event.Status {
			log.Printf("NEEDS REVIEW: webhook %s says payment %s is %s but we recorded %s", event.ID, p.ID, event.Status, p.Status)
		}
		writeWebhookAck(w, "already recorded")
		return
	}

	to := domain.OrderPaid
	if event.Status == domain.PaymentDeclined {
		to = domain.OrderCancelled
	}
	_, err = s.DB.FinishPayment(r.Context(), p.ID, event.Status, event.ProviderPaymentID, event.Reason,
		string(domain.OrderPaymentPending), string(to))
	switch {
	case err == nil:
		writeWebhookAck(w, "recorded")
	case errors.Is(err, ports.ErrPaymentNotProcessing), errors.Is(err, ports.ErrPaymentNotFound):
		// Another writer finished it between our read and our write.
		writeWebhookAck(w, "already recorded")
	case errors.Is(err, ports.ErrStateConflict):
		// The order moved on (it was cancelled) while the payment was in flight.
		// The order stays as it is, but the answer is kept, so an authorized
		// payment on a cancelled order is found by the refund job. If we cannot
		// keep it, answer 5xx so the provider delivers it again.
		if keepErr := s.recordOrphanedOutcome(r.Context(), p, event.Status, event.ProviderPaymentID, event.Reason); keepErr != nil {
			log.Printf("NEEDS REVIEW: webhook %s says payment %s for order %s was %s after the order moved on (%v), and the answer could not be recorded: %v",
				event.ID, p.ID, p.OrderID, event.Status, err, keepErr)
			writeError(w, http.StatusInternalServerError, "Failed to record the payment due to an internal server error")
			return
		}
		if event.Status == domain.PaymentAuthorized {
			log.Printf("REFUND DUE: webhook %s says payment %s for order %s was authorized after the order moved on: %v",
				event.ID, p.ID, p.OrderID, err)
		}
		writeWebhookAck(w, "order moved on")
	default:
		log.Printf("webhook %s: database error recording payment %s: %v", event.ID, p.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to record the payment due to an internal server error")
	}
}

// writeWebhookAck answers 200 so the provider stops retrying this event. The
// body is only for people reading logs and dashboards.
func writeWebhookAck(w http.ResponseWriter, outcome string) {
	writeJSON(w, http.StatusOK, map[string]string{"status": outcome})
}
