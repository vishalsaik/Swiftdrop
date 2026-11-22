package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
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
	// DriverName is who is delivering it, once a driver has taken it: the whole name for staff, and for a
	// customer only the first, the way a delivery person introduces themselves. A customer is never sent the
	// driver's account id (assignedDriverId is empty for them), only that there is a driver.
	DriverName string `json:"driverName,omitempty"`
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
	// Attempt is which try this payment is (1, 2, 3 ...) of MaxAttempts: a
	// declined payment can be followed by another until the attempts run out.
	Attempt     int `json:"attempt"`
	MaxAttempts int `json:"maxAttempts"`
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
			ID: p.ID, Status: p.Status, AmountCents: p.AmountCents,
			RefundStatus: refund, UpdatedAt: p.UpdatedAt,
			Attempt: max(p.Attempt, 1), MaxAttempts: domain.MaxPaymentAttempts,
		}
		// Only a decline's reason is written for the customer; the reason on a
		// payment waiting for review or abandoned is for the people running the service.
		if p.Status == domain.PaymentDeclined {
			v.Payment.Reason = p.Reason
		}
	}
	return v
}

// unknownDriver is what a customer is told when an order has a driver whose name cannot be read.
const unknownDriver = "Your driver"

// nameDrivers fills in who is delivering each order, and keeps the driver's account id for staff: a customer
// is told only the first name, and an order with a driver whose name cannot be read still says it has one. A
// name that cannot be looked up is not a reason to fail the answer, only to leave it out.
func (s *Server) nameDrivers(ctx context.Context, role domain.Role, views ...*orderView) {
	var ids []string
	seen := map[string]bool{}
	for _, v := range views {
		if id := v.AssignedDriverID; id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	var names map[string]string
	if s.Drivers != nil {
		var err error
		if names, err = s.Drivers.DriverNames(ctx, ids); err != nil {
			log.Printf("reading the names of drivers: %v", err)
		}
	}
	for _, v := range views {
		id := v.AssignedDriverID
		if id == "" {
			continue
		}
		if role == domain.RoleStaff {
			v.DriverName = names[id]
			continue
		}
		v.AssignedDriverID = ""
		v.DriverName = unknownDriver
		if first := strings.Fields(names[id]); len(first) > 0 {
			v.DriverName = first[0]
		}
	}
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
