package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

const (
	maxRefundReferenceLength = 200
	maxRefundNoteLength      = 500
)

// RecordRefund records that a person refunded an order's customer by hand, outside the
// system (in the provider's dashboard, by a bank transfer), after the system gave up on
// the refund or the provider reversed it. It is the answer to the alerts that say "refund
// the customer yourself": without it the money would stay "failed" for ever and the
// customer would keep being told to contact support.
//
// It records; it never moves money, and it never causes another refund. It works only on
// a payment whose refund FAILED. Staff only.
func (s *Server) RecordRefund(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	var in struct {
		Reference string `json:"reference"`
		Note      string `json:"note"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	reference, note := strings.TrimSpace(in.Reference), strings.TrimSpace(in.Note)
	if problems := refundFieldProblems(reference, note); len(problems) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "some fields are not acceptable", "fields": problems})
		return
	}

	by := principalFrom(r).User.Email
	p, recorded, err := s.DB.RecordManualRefund(r.Context(), o.ID, reference, by, note)
	switch {
	case errors.Is(err, ports.ErrPaymentNotFound):
		writeError(w, http.StatusConflict, "this order has no payment, so there is nothing to refund")
		return
	case err != nil:
		log.Printf("database error recording a manual refund for %s: %v", o.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to record the refund due to an internal server error")
		return
	case !recorded:
		writeError(w, http.StatusConflict, whyNotRecorded(p))
		return
	}
	log.Printf("manual refund recorded: order %s, payment %s, reference %q, by %s", o.ID, p.ID, reference, by)
	writeJSON(w, http.StatusOK, viewOf(o, &p))
}

// refundFieldProblems says what is wrong with what was typed, keyed by field.
func refundFieldProblems(reference, note string) map[string]string {
	problems := map[string]string{}
	switch {
	case reference == "":
		problems["reference"] = "is required: the provider's refund id, or whatever finds the transfer"
	case utf8.RuneCountInString(reference) > maxRefundReferenceLength:
		problems["reference"] = "must be at most 200 characters"
	case hasControl(reference):
		problems["reference"] = "must not contain control characters"
	}
	switch {
	case utf8.RuneCountInString(note) > maxRefundNoteLength:
		problems["note"] = "must be at most 500 characters"
	case hasControl(note):
		problems["note"] = "must not contain control characters"
	}
	return problems
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// whyNotRecorded explains a refusal from how the payment stands.
func whyNotRecorded(p domain.Payment) string {
	if p.Status != domain.PaymentAuthorized {
		return "this order was never charged, so there is nothing to refund"
	}
	switch p.RefundStatus {
	case domain.RefundDone:
		return "the refund of this order is already recorded as done"
	case domain.RefundInProgress:
		return "the refund job is still working on this refund; wait for it to finish or to give up"
	case domain.RefundFailed:
		// the update did not apply although the refund had failed: it changed in between
		return "the refund changed while this was being recorded; look at the order again"
	default:
		return "no refund has been tried for this order yet: the refund job makes it when the order is cancelled"
	}
}
