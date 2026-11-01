// Package paying decides what a request to pay for an order means and does it.
// It is the Facade over everything /pay needs (the payment record, the provider,
// the order's state machine, alerts) so that the HTTP handler only reads the
// request and writes the answer, and this can be tested without HTTP.
//
// Pay is idempotent: calling it again for the same order never charges twice. The
// order's payment record decides what a request is.
//
//	no payment for the order  -> a NEW payment: claim it, then authorize, then record the result
//	payment authorized        -> a retry: replay the success
//	payment declined          -> a retry: replay the decline (or, if the order is open
//	                             again, a NEW attempt)
//	payment abandoned         -> the provider never saw it: a NEW payment
//	payment processing        -> a retry: conflict while its lease is live; once the
//	                             lease has expired, take it over and ask the
//	                             provider again with the SAME payment id, unless
//	                             it is so old that the provider may have forgotten
//	                             that id (then it is left for a person)
//	payment needs_review      -> conflict: a person is looking at it
//
// The claim comes BEFORE the provider call. It is what stops two simultaneous
// requests from both charging: the database lets exactly one of them in.
package paying

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/order"
	"swiftdrop/internal/payment"

	"github.com/google/uuid"
)

const (
	// Lease is how long a request that has claimed a payment may work on it before
	// a later request is allowed to take it over. It must be longer than the
	// slowest legitimate provider call (the Stripe client gives up after 30s), or a
	// second request could take over a payment that is still being processed.
	Lease = 60 * time.Second

	// TakeoverCutoff is how old a payment may be and still be sent to the provider
	// again by a retry. The provider forgets an idempotency key after a while
	// (Stripe: at least 24 hours, so this stays well inside it); a send after that
	// is a brand new charge.
	TakeoverCutoff = 20 * time.Hour
)

// Kind says what became of a request to pay.
type Kind int

const (
	// Paid: the payment is authorized (now, or earlier and replayed). Order is the order.
	Paid Kind = iota + 1
	// Declined: the payment was refused. Message is the reason.
	Declined
	// BadRequest: the request body could not be read.
	BadRequest
	// TooLarge: the request body is over the limit.
	TooLarge
	// Unprocessable: the provider could never fulfil this request, so nothing was claimed.
	Unprocessable
	// Conflict: the request cannot go ahead now (the order is in the wrong state,
	// someone else is working on the payment, a person is checking it, it moved).
	Conflict
	// NotFound: the order vanished while the request ran.
	NotFound
	// Internal: something of ours failed; the cause is in the log, not in Message.
	Internal
)

// Outcome is the answer to a request to pay.
type Outcome struct {
	Kind Kind
	// Order is the order after a Paid answer.
	Order domain.Order
	// Message is the reason of a Declined answer, and the explanation of every
	// failure.
	Message string
	// CanRetry and AttemptsLeft tell the customer, after a Declined answer,
	// whether another method may be tried and how many attempts remain.
	CanRetry     bool
	AttemptsLeft int
}

// Method reads the payment method token from the request. It is called only when
// a request is about to need it, so a replay of a finished payment never reads, or
// complains about, the body. It returns ErrBodyTooLarge or a *BadBodyError when it
// cannot read it.
type Method func() (string, error)

var (
	// ErrBodyTooLarge means the request body is over the limit.
	ErrBodyTooLarge = errors.New("request body too large")
)

// BadBodyError means the request body is not what /pay accepts.
type BadBodyError struct{ Err error }

func (e *BadBodyError) Error() string { return "invalid request body: " + e.Err.Error() }
func (e *BadBodyError) Unwrap() error { return e.Err }

// Lookup reads the payment of an order. It is the database, or a cache in front of
// it (see internal/paymentcache).
type Lookup interface {
	GetPaymentByOrder(ctx context.Context, orderID string) (domain.Payment, error)
}

// Store is the storage a payment needs: the order, its payment, and the writes
// that move both together. ports.Store (the Postgres adapter) satisfies it.
type Store interface {
	GetOrderbyId(ctx context.Context, id string) (domain.Order, error)
	GetPaymentByOrder(ctx context.Context, orderID string) (domain.Payment, error)
	StartPayment(ctx context.Context, p domain.Payment, lease time.Duration, orderFrom, orderTo string) (domain.Payment, bool, error)
	FinishPayment(ctx context.Context, paymentID string, status domain.PaymentStatus, providerPaymentID, reason, orderFrom, orderTo string) (domain.Order, error)
	CompletePayment(ctx context.Context, id string, status domain.PaymentStatus, providerPaymentID, reason string) (domain.Payment, error)
	TakeOverPayment(ctx context.Context, id string, lease time.Duration) (domain.Payment, bool, error)
	MarkPaymentNeedsReview(ctx context.Context, id, reason string) (domain.Payment, bool, error)
}

// Service pays for orders.
type Service struct {
	DB      Store
	Lookup  Lookup
	Payment *payment.Router
	// Alert makes a problem only a person can fix visible (see the handlers'
	// raiseAlert): it logs and keeps an alert.
	Alert func(ctx context.Context, kind, subject, orderID, format string, args ...any)
}

func failure(kind Kind, format string, args ...any) Outcome {
	return Outcome{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

func internal(msg string) Outcome { return Outcome{Kind: Internal, Message: msg} }

// writeFailure is the answer to a failed state write. A lost race is the caller's
// to retry (the read-then-write it was part of is now out of date), an order that
// vanished in between is a NotFound, and anything else is Internal with the
// caller's generic message (the real cause is already in the log).
func writeFailure(id string, err error, internalMsg string) Outcome {
	switch {
	case errors.Is(err, ports.ErrStateConflict):
		return failure(Conflict, "order %s changed while this request was running, read it again", id)
	case errors.Is(err, ports.ErrOrderNotFound):
		return failure(NotFound, "Order %s could not be found", id)
	default:
		return internal(internalMsg)
	}
}

// Pay pays for o, or says why not.
func (s *Service) Pay(ctx context.Context, o domain.Order, method Method) Outcome {
	p, err := s.Lookup.GetPaymentByOrder(ctx, o.ID)
	switch {
	case err == nil && p.Status == domain.PaymentDeclined && o.State == domain.OrderCreated:
		// The last attempt was declined and the order is open again (a decline
		// returns it to created while attempts are left), so this request is a NEW
		// attempt with a new payment, not a retry of the declined one.
		return s.start(ctx, o, method)
	case err == nil && p.Status == domain.PaymentAbandoned:
		// The provider never saw the last attempt, so nothing was charged and a new
		// payment is the right answer. If the order is no longer open (it was
		// cancelled), start refuses it like any other payment on that order.
		return s.start(ctx, o, method)
	case err == nil:
		return s.continuePayment(ctx, o, p, method)
	case errors.Is(err, ports.ErrPaymentNotFound):
		return s.start(ctx, o, method)
	default:
		log.Printf("database query error for the payment of %s: %v", o.ID, err)
		return internal("Failed to look up the payment due to an internal server error")
	}
}

// providerFor picks the payment provider (Strategy, via the Factory) for the
// order's payment type.
func (s *Service) providerFor(o domain.Order) (payment.PaymentProvider, *Outcome) {
	provider, err := s.Payment.For(o.PaymentType)
	if err != nil {
		// Our stored data, not this request, named a type we can't handle
		// (e.g. an order created before the payment_type column existed), so
		// this is an internal error, and the real cause goes to the log.
		log.Printf("no payment provider for order %s (payment type %q): %v", o.ID, o.PaymentType, err)
		out := internal("Payment is not available for this order")
		return nil, &out
	}
	return provider, nil
}

// start handles an order that has no payment yet.
func (s *Service) start(ctx context.Context, o domain.Order, method Method) Outcome {
	if !order.CanTransition(o.State, domain.OrderPaymentPending) {
		return failure(Conflict, "order %s cannot be %s from state %s", o.ID, domain.OrderPaid, o.State)
	}
	// The provider lookup can fail, so it runs before anything is saved: a
	// failure leaves the order untouched (still "created"), not stranded in
	// payment_pending with a payment nobody will finish.
	provider, out := s.providerFor(o)
	if out != nil {
		return *out
	}
	// Refuse a request the provider could never fulfil before claiming anything:
	// a claim that is then abandoned would leave the payment stuck in processing.
	methodID, out := s.paymentMethod(o, provider, method)
	if out != nil {
		return *out
	}
	// The claim. The payment row and the order's move to payment_pending are
	// written in one transaction, and only if the order has no payment yet and
	// is still in the state we read. Of any number of simultaneous requests,
	// exactly one gets claimed = true; the rest never reach the provider.
	claim := domain.Payment{ID: uuid.New().String(), OrderID: o.ID, AmountCents: o.TotalCents()}
	p, claimed, err := s.DB.StartPayment(ctx, claim, Lease, string(o.State), string(domain.OrderPaymentPending))
	if err != nil {
		log.Printf("database error starting the payment of %s: %v", o.ID, err)
		return writeFailure(o.ID, err, "Failed to start payment due to an internal server error")
	}
	if !claimed {
		// Another request claimed it between our lookup and our claim, so this
		// one is a retry after all.
		return s.continuePayment(ctx, o, p, method)
	}
	return s.authorize(ctx, p, provider, methodID)
}

// paymentMethod reads the payment method from the request and lets the provider
// refuse the request if it could never succeed (Unprocessable), so that nothing is
// claimed for it. A body that cannot be read is a BadRequest or TooLarge, and no
// body at all is fine: it is just an empty method.
func (s *Service) paymentMethod(o domain.Order, provider payment.PaymentProvider, method Method) (string, *Outcome) {
	id, err := method()
	var bad *BadBodyError
	switch {
	case errors.Is(err, ErrBodyTooLarge):
		out := failure(TooLarge, "%s", ErrBodyTooLarge)
		return "", &out
	case errors.As(err, &bad):
		out := failure(BadRequest, "%s", bad)
		return "", &out
	case err != nil:
		log.Printf("reading the payment method of %s: %v", o.ID, err)
		out := internal("Failed to read the request due to an internal server error")
		return "", &out
	}
	if checker, ok := provider.(payment.RequestChecker); ok {
		req := domain.PaymentRequest{
			OrderID: o.ID, AmountCents: o.TotalCents(), Currency: "USD", PaymentMethodID: id,
		}
		if err := checker.Check(req); err != nil {
			out := failure(Unprocessable, "%s", err)
			return "", &out
		}
	}
	return id, nil
}

// continuePayment handles an order that already has a payment, so this request is
// a retry.
func (s *Service) continuePayment(ctx context.Context, o domain.Order, p domain.Payment, method Method) Outcome {
	switch p.Status {
	case domain.PaymentAuthorized, domain.PaymentDeclined:
		return s.replay(ctx, p)
	case domain.PaymentProcessing:
		return s.takeOver(ctx, o, p, method)
	case domain.PaymentNeedsReview, domain.PaymentAbandoned:
		return s.answerUnsettled(ctx, o)
	default:
		log.Printf("payment %s for order %s has an unknown status %q", p.ID, p.OrderID, p.Status)
		return internal("Failed to read the payment due to an internal server error")
	}
}

// replay answers a retry of a payment that is not ours to send again: a finished
// one with its original outcome, without calling the provider again, and one that
// is still being worked on, or checked by a person, with a Conflict.
func (s *Service) replay(ctx context.Context, p domain.Payment) Outcome {
	switch p.Status {
	case domain.PaymentAuthorized:
		current, err := s.DB.GetOrderbyId(ctx, p.OrderID)
		if err != nil {
			log.Printf("database query error for %s: %v", p.OrderID, err)
			return writeFailure(p.OrderID, err, "Internal database verification step failed")
		}
		return Outcome{Kind: Paid, Order: current}
	case domain.PaymentDeclined:
		// Only a payment on an order that is no longer open is replayed (a
		// declined payment on an open order starts a new attempt instead), so
		// there is nothing left to retry.
		return Outcome{Kind: Declined, Message: declineReason(p.Reason)}
	case domain.PaymentProcessing:
		return failure(Conflict, "the payment for order %s is still being processed, try again shortly", p.OrderID)
	case domain.PaymentNeedsReview:
		return failure(Conflict, "the payment for order %s could not be confirmed automatically and is being checked, so it cannot be tried again yet", p.OrderID)
	case domain.PaymentAbandoned:
		return failure(Conflict, "the earlier payment attempt for order %s ended without a charge, try again", p.OrderID)
	default:
		log.Printf("payment %s for order %s cannot be replayed from status %q", p.ID, p.OrderID, p.Status)
		return internal("Failed to read the payment due to an internal server error")
	}
}

// takeOver handles a retry of a payment that is still processing. If its lease is
// live, someone is working on it: say so. If the lease has run out, its owner
// probably died, so take it over and ask the provider again with the SAME payment
// id (the provider returns the saved answer, or runs it once), never with a new
// one.
func (s *Service) takeOver(ctx context.Context, o domain.Order, p domain.Payment, method Method) Outcome {
	// Everything that can refuse this request comes before the takeover, so a
	// refused retry does not take the lease and leave the payment held.
	provider, out := s.providerFor(o)
	if out != nil {
		return *out
	}
	methodID, out := s.paymentMethod(o, provider, method)
	if out != nil {
		return *out
	}
	// The provider keeps the answer to an idempotency key for a limited time
	// (Stripe: at least 24 hours). Asking again after that would be a NEW charge,
	// so a payment this old is never sent again: it is left for the reconciler to
	// look up, and for a person if that does not settle it.
	if time.Since(p.CreatedAt) >= TakeoverCutoff {
		_, marked, err := s.DB.MarkPaymentNeedsReview(ctx, p.ID, "no answer after "+TakeoverCutoff.String())
		if err != nil {
			log.Printf("database error marking payment %s for review: %v", p.ID, err)
			return internal("Failed to read the payment due to an internal server error")
		}
		if marked {
			s.Alert(ctx, domain.AlertPaymentNeedsReview, p.ID, p.OrderID,
				"payment %s for order %s has had no answer for %s, so it was not sent again (the provider may have forgotten its key, and a second send could charge twice)",
				p.ID, p.OrderID, time.Since(p.CreatedAt).Round(time.Minute))
		}
		return s.answerUnsettled(ctx, o)
	}
	taken, ok, err := s.DB.TakeOverPayment(ctx, p.ID, Lease)
	if err != nil {
		log.Printf("database error taking over payment %s: %v", p.ID, err)
		return internal("Failed to read the payment due to an internal server error")
	}
	if !ok {
		// A live lease, or it finished while we were looking. Read it again.
		return s.answerUnsettled(ctx, o)
	}
	return s.authorize(ctx, taken, provider, methodID)
}

// answerUnsettled answers a retry that cannot go on to the provider: it reads the
// order's payment again (from the database, never a cache) and says what stands in
// the way, or replays the answer if there is one by now.
func (s *Service) answerUnsettled(ctx context.Context, o domain.Order) Outcome {
	latest, err := s.DB.GetPaymentByOrder(ctx, o.ID)
	if err != nil {
		log.Printf("database query error for the payment of %s: %v", o.ID, err)
		return internal("Failed to read the payment due to an internal server error")
	}
	return s.replay(ctx, latest)
}

// authorize asks the provider and records the answer. p is a payment this request
// holds the claim on. The payment's own id is the provider's idempotency key, and
// the amount is the one stored when it was claimed, so a retry or a takeover asks
// the provider the very same question.
func (s *Service) authorize(ctx context.Context, p domain.Payment, provider payment.PaymentProvider, method string) Outcome {
	req := domain.PaymentRequest{
		OrderID:         p.OrderID,
		IdempotencyKey:  p.ID,
		AmountCents:     p.AmountCents,
		Currency:        "USD",
		PaymentMethodID: method,
	}
	result, err := provider.Authorize(ctx, req)
	if err != nil {
		// We do not know what happened. The payment stays processing: once its
		// lease runs out, a retry asks the provider again with the same id.
		log.Printf("payment authorisation error for %s: %v", p.OrderID, err)
		return internal("Payment authorisation failed")
	}
	// A provider that returns neither a result nor an error broke its contract.
	// We don't know what happened, so treat it like an error, not a decline.
	if result == nil {
		log.Printf("payment provider returned no result and no error for %s", p.OrderID)
		return internal("Payment authorisation failed")
	}
	switch result.Status {
	case domain.PaymentAuthorized:
		return s.finish(ctx, p, domain.PaymentAuthorized, result.PaymentID, "", domain.OrderPaid)
	case domain.PaymentDeclined:
		// A decline is a normal answer, not an error. The order goes back to
		// created so the customer can try another method, until the attempts run
		// out and it is cancelled. The client gets the reason either way.
		return s.finish(ctx, p, domain.PaymentDeclined, result.PaymentID, declineReason(result.Reason), p.OrderStateAfterFailure())
	default:
		log.Printf("payment provider answered %q for %s, which is neither authorized nor declined", result.Status, p.OrderID)
		return internal("Payment authorisation failed")
	}
}

// finish records the outcome and moves the order, in one transaction, and says
// what became of the request.
func (s *Service) finish(ctx context.Context, p domain.Payment, status domain.PaymentStatus, providerPaymentID, reason string, to domain.OrderState) Outcome {
	updated, err := s.DB.FinishPayment(ctx, p.ID, status, providerPaymentID, reason,
		string(domain.OrderPaymentPending), string(to))
	switch {
	case err == nil:
		if status == domain.PaymentDeclined {
			return Outcome{Kind: Declined, Message: reason, CanRetry: to == domain.OrderCreated, AttemptsLeft: max(domain.MaxPaymentAttempts-p.Attempt, 0)}
		}
		return Outcome{Kind: Paid, Order: updated}
	case errors.Is(err, ports.ErrPaymentNotProcessing):
		// Another request recorded the outcome first (for example a retry that
		// took the payment over). Answer with theirs.
		latest, getErr := s.DB.GetPaymentByOrder(ctx, p.OrderID)
		if getErr != nil {
			log.Printf("database query error for the payment of %s: %v", p.OrderID, getErr)
			return internal("Failed to read the payment due to an internal server error")
		}
		return s.replay(ctx, latest)
	case errors.Is(err, ports.ErrStateConflict):
		// The provider has answered but the order moved on while it was working
		// (it was cancelled). The order stays as it is, but the provider's answer
		// is a fact about money, so it is kept: an authorized payment on a
		// cancelled order is what the refund job looks for.
		return s.keep(ctx, p, status, providerPaymentID, reason, err)
	default:
		log.Printf("database error recording payment %s for %s: %v", p.ID, p.OrderID, err)
		return writeFailure(p.OrderID, err, "Failed to record the payment due to an internal server error")
	}
}

// keep records the provider's answer for a payment whose order moved on, without
// touching the order, and says what became of the request. cause is the state
// conflict that revealed the order had moved.
//
// A provider's answer is a fact about money, and the order is a fact about the
// kitchen. Usually they change together (FinishPayment). But an order can be
// cancelled while a payment is in flight; then the answer arrives for an order
// that no longer waits for it. The order must stay cancelled, and the answer must
// still be kept: an authorized payment on a cancelled order is exactly what the
// refund job looks for. Dropping it would leave money taken with nothing to say so.
func (s *Service) keep(ctx context.Context, p domain.Payment, status domain.PaymentStatus, providerPaymentID, reason string, cause error) Outcome {
	// Guarded by the payment still being processing, so a late or repeated writer
	// cannot overwrite an outcome; finding it already recorded is fine.
	if _, err := s.DB.CompletePayment(ctx, p.ID, status, providerPaymentID, reason); err != nil && !errors.Is(err, ports.ErrPaymentNotProcessing) {
		// The money may have moved and we could not write it down.
		s.Alert(ctx, domain.AlertUnrecordedCharge, p.ID, p.OrderID,
			"payment %s for order %s was answered %q after the order moved on (%v), and the answer could not be recorded: %v",
			p.ID, p.OrderID, status, cause, err)
		return internal("Failed to record the payment due to an internal server error")
	}
	if status != domain.PaymentAuthorized {
		// A decline moved no money, so there is nothing more to do: the caller
		// just learns the order changed.
		return writeFailure(p.OrderID, cause, "Failed to record the payment due to an internal server error")
	}
	log.Printf("REFUND DUE: payment %s for order %s was authorized after the order moved on: %v", p.ID, p.OrderID, cause)
	msg := fmt.Sprintf("order %s changed while the payment was being processed", p.OrderID)
	if latest, err := s.DB.GetOrderbyId(ctx, p.OrderID); err == nil && latest.State == domain.OrderCancelled {
		msg = fmt.Sprintf("order %s was cancelled while the payment was being processed; the payment has been recorded and will be refunded", p.OrderID)
	}
	return Outcome{Kind: Conflict, Message: msg}
}

// declineReason is what the client is told when a payment was declined and the
// provider gave no reason.
func declineReason(reason string) string {
	if reason == "" {
		return "payment declined"
	}
	return reason
}
