package handlers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// The payment half of fakeDB. It follows the Postgres adapter's contract: one
// payment per order, every write guarded by the state it expects, StartPayment
// and FinishPayment all-or-nothing, and the same sentinel errors.

func paymentNotFound(orderID string) error {
	return fmt.Errorf("%w: order %s", ports.ErrPaymentNotFound, orderID)
}

// paymentByID finds a payment by its own id (the map is keyed by order id).
func (f *fakeDB) paymentByID(id string) (domain.Payment, bool) {
	for _, p := range f.payments {
		if p.ID == id {
			return p, true
		}
	}
	return domain.Payment{}, false
}

// newProcessing is a payment as a fresh claim creates it.
func newProcessing(p domain.Payment, lease time.Duration) domain.Payment {
	now := time.Now()
	p.Status = domain.PaymentProcessing
	p.ProviderPaymentID, p.Reason = "", ""
	p.LeaseExpiresAt = now.Add(lease)
	p.CreatedAt, p.UpdatedAt = now, now
	return p
}

func (f *fakeDB) ClaimPayment(_ context.Context, p domain.Payment, lease time.Duration) (domain.Payment, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	attempt := 1
	if existing, ok := f.payments[p.OrderID]; ok {
		if !closedWithoutCharge(existing) {
			return existing, false, nil
		}
		attempt = max(existing.Attempt, 1) + 1 // a declined or abandoned payment does not block a new one
	}
	if _, ok := f.orders[p.OrderID]; !ok {
		return domain.Payment{}, false, notFound(p.OrderID)
	}
	claimed := newProcessing(p, lease)
	claimed.Attempt = attempt
	f.payments[p.OrderID] = claimed
	return claimed, true, nil
}

func (f *fakeDB) GetPaymentByOrder(_ context.Context, orderID string) (domain.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payGetCalls++
	if f.payGetErr != nil && (f.payGetFailOnCall == 0 || f.payGetCalls == f.payGetFailOnCall) {
		return domain.Payment{}, f.payGetErr
	}
	p, ok := f.payments[orderID]
	if !ok {
		return domain.Payment{}, paymentNotFound(orderID)
	}
	return p, nil
}

func (f *fakeDB) GetPayment(_ context.Context, id string) (domain.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payGetCalls++
	if f.payGetErr != nil && (f.payGetFailOnCall == 0 || f.payGetCalls == f.payGetFailOnCall) {
		return domain.Payment{}, f.payGetErr
	}
	p, ok := f.paymentByID(id)
	if !ok {
		return domain.Payment{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotFound, id)
	}
	return p, nil
}

func (f *fakeDB) CompletePayment(_ context.Context, id string, status domain.PaymentStatus, providerPaymentID, reason string) (domain.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.completeErr != nil {
		return domain.Payment{}, f.completeErr
	}
	return f.completeLocked(id, status, providerPaymentID, reason)
}

func (f *fakeDB) completeLocked(id string, status domain.PaymentStatus, providerPaymentID, reason string) (domain.Payment, error) {
	if status != domain.PaymentAuthorized && status != domain.PaymentDeclined && status != domain.PaymentAbandoned {
		return domain.Payment{}, fmt.Errorf("%q is not a final status", status)
	}
	p, ok := f.paymentByID(id)
	if !ok {
		return domain.Payment{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotFound, id)
	}
	if !p.Unsettled() {
		return domain.Payment{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotProcessing, id)
	}
	p.Status, p.ProviderPaymentID, p.Reason, p.UpdatedAt = status, providerPaymentID, reason, time.Now()
	f.payments[p.OrderID] = p
	return p, nil
}

func (f *fakeDB) TakeOverPayment(_ context.Context, id string, lease time.Duration) (domain.Payment, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeTakeOver != nil {
		f.beforeTakeOver(f)
	}
	if f.takeOverErr != nil {
		return domain.Payment{}, false, f.takeOverErr
	}
	p, ok := f.paymentByID(id)
	if !ok {
		return domain.Payment{}, false, fmt.Errorf("%w: %s", ports.ErrPaymentNotFound, id)
	}
	if p.Status != domain.PaymentProcessing || p.LeaseExpiresAt.After(time.Now()) {
		return domain.Payment{}, false, nil
	}
	p.LeaseExpiresAt, p.UpdatedAt = time.Now().Add(lease), time.Now()
	f.payments[p.OrderID] = p
	return p, true, nil
}

// StartPayment claims the payment and moves the order in one atomic step: if
// any part cannot be done nothing is written.
func (f *fakeDB) StartPayment(_ context.Context, p domain.Payment, lease time.Duration, orderFrom, orderTo string) (domain.Payment, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeWrite != nil {
		f.beforeWrite(f, p.OrderID)
	}
	if f.updateErr != nil && (f.updateErrOn == "" || string(f.updateErrOn) == orderTo) {
		return domain.Payment{}, false, f.updateErr
	}
	attempt := 1
	if existing, ok := f.payments[p.OrderID]; ok {
		if !closedWithoutCharge(existing) {
			return existing, false, nil // already has a live payment: nothing is written
		}
		attempt = max(existing.Attempt, 1) + 1 // a declined or abandoned payment does not block a new one
	}
	o, ok := f.orders[p.OrderID]
	if !ok {
		return domain.Payment{}, false, notFound(p.OrderID)
	}
	if string(o.State) != orderFrom {
		return domain.Payment{}, false, stateConflict(p.OrderID)
	}
	claimed := newProcessing(p, lease)
	claimed.Attempt = attempt
	f.payments[p.OrderID] = claimed
	o.State = domain.OrderState(orderTo)
	f.orders[p.OrderID] = o
	f.stateWrites = append(f.stateWrites, o.State)
	return claimed, true, nil
}

// FinishPayment records the outcome and moves the order in one atomic step.
func (f *fakeDB) FinishPayment(_ context.Context, paymentID string, status domain.PaymentStatus, providerPaymentID, reason, orderFrom, orderTo string) (domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.paymentByID(paymentID)
	if !ok {
		return domain.Order{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotFound, paymentID)
	}
	if f.beforeWrite != nil {
		f.beforeWrite(f, p.OrderID)
		p, _ = f.paymentByID(paymentID) // the hook may have changed it
	}
	if f.updateErr != nil && (f.updateErrOn == "" || string(f.updateErrOn) == orderTo) {
		return domain.Order{}, f.updateErr
	}
	if status != domain.PaymentAuthorized && status != domain.PaymentDeclined && status != domain.PaymentAbandoned {
		return domain.Order{}, errors.New("not a final status")
	}
	if !p.Unsettled() {
		return domain.Order{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotProcessing, paymentID)
	}
	o := f.orders[p.OrderID]
	if string(o.State) != orderFrom {
		return domain.Order{}, stateConflict(p.OrderID)
	}
	if _, err := f.completeLocked(paymentID, status, providerPaymentID, reason); err != nil {
		return domain.Order{}, err
	}
	o.State = domain.OrderState(orderTo)
	f.orders[p.OrderID] = o
	f.stateWrites = append(f.stateWrites, o.State)
	return o, nil
}

// closedWithoutCharge reports whether a payment ended with no money taken, so the
// order may start another one (the Postgres index leaves such payments out).
func closedWithoutCharge(p domain.Payment) bool {
	return p.Status == domain.PaymentDeclined || p.Status == domain.PaymentAbandoned
}

// ReverseRefund follows the adapter: only the refund recorded as done, and only
// that very refund, moves to failed; otherwise the payment is handed back as it is.
func (f *fakeDB) ReverseRefund(_ context.Context, providerPaymentID, refundID, reason string) (domain.Payment, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reverseErr != nil {
		return domain.Payment{}, false, f.reverseErr
	}
	for orderID, p := range f.payments {
		if p.ProviderPaymentID != providerPaymentID {
			continue
		}
		if p.RefundStatus == domain.RefundDone && p.RefundID == refundID {
			p.RefundStatus, p.RefundError, p.UpdatedAt = domain.RefundFailed, reason, time.Now()
			f.payments[orderID] = p
			return p, true, nil
		}
		return p, false, nil
	}
	return domain.Payment{}, false, fmt.Errorf("%w: provider payment %s", ports.ErrPaymentNotFound, providerPaymentID)
}

// ListUnsettledPayments follows the adapter: payments needing review, and those
// processing whose lease ran out and that are at least minAge old.
func (f *fakeDB) ListUnsettledPayments(_ context.Context, minAge time.Duration, limit int) ([]domain.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unsettledErr != nil {
		return nil, f.unsettledErr
	}
	out := []domain.Payment{}
	now := time.Now()
	for _, p := range f.payments {
		stuck := p.Status == domain.PaymentProcessing && !p.LeaseExpiresAt.After(now) && !p.CreatedAt.After(now.Add(-minAge))
		if p.Status == domain.PaymentNeedsReview || stuck {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MarkPaymentNeedsReview follows the adapter: only a processing payment moves.
func (f *fakeDB) MarkPaymentNeedsReview(_ context.Context, id, reason string) (domain.Payment, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeMark != nil {
		f.beforeMark(f)
	}
	if f.markErr != nil {
		return domain.Payment{}, false, f.markErr
	}
	p, ok := f.paymentByID(id)
	if !ok {
		return domain.Payment{}, false, fmt.Errorf("%w: %s", ports.ErrPaymentNotFound, id)
	}
	if p.Status != domain.PaymentProcessing {
		return domain.Payment{}, false, nil
	}
	p.Status, p.Reason, p.UpdatedAt = domain.PaymentNeedsReview, reason, time.Now()
	f.payments[p.OrderID] = p
	return p, true, nil
}

// ListPaymentEvents returns what a test put in f.events for the order (the
// real history is written by the database's trigger, which is tested in the adapter).
func (f *fakeDB) ListPaymentEvents(_ context.Context, orderID string) ([]domain.PaymentEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	return f.events[orderID], nil
}
