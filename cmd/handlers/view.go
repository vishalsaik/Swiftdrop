package handlers

import (
	"errors"
	"log"
	"net/http"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// orderView is an order as a screen needs it: the order itself (its fields stay
// at the top level) plus a summary of its payment and what, if anything, is
// wrong or unfinished about the money. The lifecycle endpoints keep answering
// with the plain order; a screen reads this when it shows an order.
type orderView struct {
	domain.Order
	Payment *paymentView `json:"payment,omitempty"`
	// PaymentIssue is derived from the order and its payment on every read (see
	// domain.IssueFor), so it cannot go out of date. Absent when there is
	// nothing to tell the customer.
	PaymentIssue domain.PaymentIssue `json:"paymentIssue,omitempty"`
}

// paymentView is the part of a payment a customer-facing screen may see. It
// leaves out the provider's ids and error text on purpose.
type paymentView struct {
	ID           string               `json:"id"`
	Status       domain.PaymentStatus `json:"status"`
	AmountCents  int64                `json:"amountCents"`
	Reason       string               `json:"reason,omitempty"` // the decline reason
	RefundStatus domain.RefundStatus  `json:"refundStatus"`
	UpdatedAt    time.Time            `json:"updatedAt"`
}

// viewOf builds the view of an order from its payment (nil if it has none).
func viewOf(o domain.Order, p *domain.Payment) orderView {
	v := orderView{Order: o, PaymentIssue: domain.IssueFor(o, p)}
	if p != nil {
		refund := p.RefundStatus
		if refund == "" {
			refund = domain.RefundNone
		}
		v.Payment = &paymentView{
			ID: p.ID, Status: p.Status, AmountCents: p.AmountCents, Reason: p.Reason,
			RefundStatus: refund, UpdatedAt: p.UpdatedAt,
		}
	}
	return v
}

// loadView reads an order's payment and builds its view. If it cannot, it has
// already written the 500 and returns ok = false.
func (s *Server) loadView(w http.ResponseWriter, r *http.Request, o domain.Order) (orderView, bool) {
	p, err := s.DB.GetPaymentByOrder(r.Context(), o.ID)
	switch {
	case err == nil:
		return viewOf(o, &p), true
	case errors.Is(err, ports.ErrPaymentNotFound):
		return viewOf(o, nil), true
	default:
		log.Printf("database query error for the payment of %s: %v", o.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to look up the payment due to an internal server error")
		return orderView{}, false
	}
}
