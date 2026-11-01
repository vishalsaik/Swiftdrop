package handlers

import (
	"errors"
	"io"
	"log"
	"net/http"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/settle"
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

	// One line per verified event, so a deployment can see that the provider's
	// events arrive and that they name one of our payments.
	if event.Kind == payment.EventRefundFailed {
		log.Printf("webhook %s: refund %s of provider payment %s failed (%s)", event.ID, event.RefundID, event.ProviderPaymentID, event.Reason)
		s.recordRefundFailure(w, r, event)
		return
	}
	log.Printf("webhook %s: payment %s is %s at the provider (%s)", event.ID, event.PaymentID, event.Status, event.ProviderPaymentID)
	s.recordEvent(w, r, event)
}

// recordRefundFailure applies the provider's report that a refund it accepted has
// failed. The refund job believes the money is on its way back; it is not, so the
// refund becomes failed (the customer is told to contact support, as for any
// refund we gave up on) and an alert tells a person to refund by hand. We never
// ask the provider to refund again on our own: the provider said why it failed,
// and a second refund could pay out twice.
func (s *Server) recordRefundFailure(w http.ResponseWriter, r *http.Request, event payment.Event) {
	p, reversed, err := s.DB.ReverseRefund(r.Context(), event.ProviderPaymentID, event.RefundID,
		"the provider reversed refund "+event.RefundID+": "+event.Reason)
	switch {
	case errors.Is(err, ports.ErrPaymentNotFound):
		log.Printf("webhook %s: no payment here has the provider id %s, nothing to record", event.ID, event.ProviderPaymentID)
		ackEvent(w, event, "unknown payment")
	case err != nil:
		log.Printf("webhook %s: database error reversing refund %s: %v", event.ID, event.RefundID, err)
		writeError(w, http.StatusInternalServerError, "Failed to record the refund due to an internal server error")
	case reversed:
		s.raiseAlert(r.Context(), domain.AlertRefundReversed, p.ID, p.OrderID,
			"webhook %s says refund %s of payment %s for order %s failed after the provider accepted it (%s): the customer has not got their money back",
			event.ID, event.RefundID, p.ID, p.OrderID, event.Reason)
		ackEvent(w, event, "recorded")
	case p.RefundStatus == domain.RefundInProgress:
		// Our own job is still making the refund and has not written it down.
		// Answer 5xx so the provider delivers the report again once it has.
		log.Printf("webhook %s: refund %s of payment %s failed while our refund job is still working on it; asking for a redelivery", event.ID, event.RefundID, p.ID)
		writeError(w, http.StatusServiceUnavailable, "the refund is still being recorded, try again")
	case p.RefundStatus == domain.RefundFailed:
		ackEvent(w, event, "already recorded")
	default:
		// A refund of ours that this is not (none made, or another one's id).
		log.Printf("webhook %s: refund %s failed but it is not the refund recorded for payment %s (%q, refund %q)",
			event.ID, event.RefundID, p.ID, p.RefundStatus, p.RefundID)
		ackEvent(w, event, "not our refund")
	}
}

// recordEvent applies a verified event to the payment it names.
func (s *Server) recordEvent(w http.ResponseWriter, r *http.Request, event payment.Event) {
	p, err := s.DB.GetPayment(r.Context(), event.PaymentID)
	switch {
	case err == nil:
	case errors.Is(err, ports.ErrPaymentNotFound):
		log.Printf("webhook %s: no payment %s here, nothing to record", event.ID, event.PaymentID)
		ackEvent(w, event, "unknown payment")
		return
	default:
		log.Printf("webhook %s: database error reading payment %s: %v", event.ID, event.PaymentID, err)
		writeError(w, http.StatusInternalServerError, "Failed to read the payment due to an internal server error")
		return
	}

	if !p.Unsettled() {
		// Already recorded, by PayOrder or by an earlier delivery of this event.
		if p.Status != event.Status {
			s.raiseAlert(r.Context(), domain.AlertContradictingEvent, p.ID, p.OrderID,
				"webhook %s says payment %s is %s but we recorded %s", event.ID, p.ID, event.Status, p.Status)
		}
		ackEvent(w, event, "already recorded")
		return
	}

	// The order may have moved on (been cancelled) while the payment was in
	// flight. Then the order stays as it is, but the answer is kept, so an
	// authorized payment on a cancelled order is found by the refund job. If we
	// cannot keep it, answer 5xx so the provider delivers it again.
	result, err := settle.Apply(r.Context(), s.DB, p, event.Status, event.ProviderPaymentID, event.Reason)
	var unrecorded *settle.UnrecordedError
	switch {
	case errors.As(err, &unrecorded):
		s.raiseAlert(r.Context(), domain.AlertUnrecordedCharge, p.ID, p.OrderID,
			"webhook %s says payment %s for order %s was %s after the order moved on (%v), and the answer could not be recorded: %v",
			event.ID, p.ID, p.OrderID, event.Status, unrecorded.Conflict, unrecorded.Err)
		writeError(w, http.StatusInternalServerError, "Failed to record the payment due to an internal server error")
	case err != nil:
		log.Printf("webhook %s: database error recording payment %s: %v", event.ID, p.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to record the payment due to an internal server error")
	case result == settle.Recorded:
		ackEvent(w, event, "recorded")
	case result == settle.AlreadyRecorded:
		ackEvent(w, event, "already recorded")
	default: // settle.OrderMovedOn
		if event.Status == domain.PaymentAuthorized {
			log.Printf("REFUND DUE: webhook %s says payment %s for order %s was authorized after the order moved on",
				event.ID, p.ID, p.OrderID)
		}
		ackEvent(w, event, "order moved on")
	}
}

// ackEvent answers a verified event with 200 and logs what was done with it, so the
// log shows every event that arrived and what became of it, not only the problems.
func ackEvent(w http.ResponseWriter, event payment.Event, outcome string) {
	log.Printf("webhook %s: %s", event.ID, outcome)
	writeWebhookAck(w, outcome)
}

// writeWebhookAck answers 200 so the provider stops retrying this event. The
// body is only for people reading logs and dashboards.
func writeWebhookAck(w http.ResponseWriter, outcome string) {
	writeJSON(w, http.StatusOK, map[string]string{"status": outcome})
}
