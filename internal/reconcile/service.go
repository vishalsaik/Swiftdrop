// Package reconcile settles payments that never got an answer. It runs as a
// background job beside the HTTP server, like the refund job.
//
// A payment is claimed before the provider is called, so a crash, a timeout or a
// lost reply leaves it "processing" with nobody working on it. The job asks the
// provider what became of it, and only asks: it NEVER sends the charge again. The
// customer may have given up and walked away (a second send would charge someone
// who did not mean to pay), and once the provider forgets the idempotency key a
// second send is a brand new charge. Sending again is the customer's decision,
// made by retrying the payment themselves.
//
// What the provider says decides:
//
//	it has the payment and it succeeded or was refused -> record that, like a webhook
//	it has the payment and has not decided             -> wait
//	it has no record, and the attempt is old enough     -> abandoned: no money
//	                                                       moved, the order is open again
//	it cannot be asked, or never decides, for too long -> needs_review: a person looks
//
// "No record" is only believed after AbandonAfter, and a lookup that was cut short
// is an error, never "no record".
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/settle"
)

// Defaults for NewService.
const (
	// DefaultMinAge is how long after it was claimed a payment is left alone. The
	// payment lease is 60s, so this is past any request that is still working.
	DefaultMinAge = 2 * time.Minute
	// DefaultAbandonAfter is how old a payment must be before "the provider has no
	// record of it" is believed and the attempt is given up.
	DefaultAbandonAfter = 30 * time.Minute
	// DefaultReviewAfter is how old a payment may get without an answer before a
	// person is asked. It stays well inside the 24 hours a provider remembers an
	// idempotency key.
	DefaultReviewAfter = 20 * time.Hour
	DefaultLease       = 2 * time.Minute
	DefaultBatchSize   = 20

	// recordTimeout bounds writing a result down after the provider answered. It
	// runs on a context that is NOT cancelled when the job is told to stop.
	recordTimeout = 10 * time.Second
)

// Store is the storage the job needs.
type Store interface {
	settle.Store
	ListUnsettledPayments(ctx context.Context, minAge time.Duration, limit int) ([]domain.Payment, error)
	TakeOverPayment(ctx context.Context, id string, lease time.Duration) (domain.Payment, bool, error)
	MarkPaymentNeedsReview(ctx context.Context, id, reason string) (domain.Payment, bool, error)
}

// Service finds payments that have no answer and settles them.
type Service struct {
	Store  Store
	Looker payment.Looker
	// Alerts, if set, keeps what only a person can settle where the dashboard shows
	// it. It is logged either way.
	Alerts ports.AlertStore

	MinAge       time.Duration
	AbandonAfter time.Duration
	ReviewAfter  time.Duration
	// Lease is how long the job holds a payment while it asks the provider, so a
	// customer's retry and another server do not work on it at the same time.
	Lease     time.Duration
	BatchSize int

	// Now is the clock, for tests. The age of a payment is a matter of minutes and
	// hours, so a few seconds between this clock and the database's do not matter.
	Now func() time.Time
}

// NewService returns a Service with the default timings.
func NewService(store Store, looker payment.Looker) *Service {
	return &Service{
		Store: store, Looker: looker,
		MinAge: DefaultMinAge, AbandonAfter: DefaultAbandonAfter, ReviewAfter: DefaultReviewAfter,
		Lease: DefaultLease, BatchSize: DefaultBatchSize,
	}
}

// Report says what one run did.
type Report struct {
	Settled   int // the provider's answer was recorded
	Abandoned int // the provider has no record: given up, the order is open again
	Reviewing int // handed to a person
	Waiting   int // nothing to do yet
	Skipped   int // someone else holds it, or it was settled meanwhile
	Errors    int // a lookup or a write failed; it is left as it was
}

func (r Report) any() bool { return r != Report{} }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// RunOnce looks at every payment that waits for an answer, up to the batch size,
// one after another. It returns an error only if the list could not be read; a
// problem with one payment is logged, counted and does not stop the others.
func (s *Service) RunOnce(ctx context.Context) (Report, error) {
	var rep Report
	waiting, err := s.Store.ListUnsettledPayments(ctx, s.MinAge, s.BatchSize)
	if err != nil {
		return rep, fmt.Errorf("reconcile: listing payments without an answer: %w", err)
	}
	for _, p := range waiting {
		if ctx.Err() != nil {
			break // told to stop: whatever is left is listed again next time
		}
		s.reconcileOne(ctx, p, &rep)
	}
	return rep, nil
}

func (s *Service) reconcileOne(ctx context.Context, p domain.Payment, rep *Report) {
	age := s.now().Sub(p.CreatedAt)

	// A payment still processing is held while we ask, so a customer's retry or
	// another server cannot work on it at the same time. One waiting for review
	// is only ever read, so it needs no hold.
	if p.Status == domain.PaymentProcessing {
		_, ok, err := s.Store.TakeOverPayment(ctx, p.ID, s.Lease)
		switch {
		case err != nil:
			log.Printf("reconcile: taking payment %s: %v", p.ID, err)
			rep.Errors++
			return
		case !ok:
			rep.Skipped++ // a retry or another server is working on it
			return
		}
	}

	res, err := s.Looker.Lookup(ctx, domain.LookupRequest{PaymentID: p.ID, CreatedAt: p.CreatedAt, AmountCents: p.AmountCents})
	if err == nil && res == nil {
		err = errors.New("the provider returned neither an answer nor an error")
	}
	if err != nil {
		if ctx.Err() != nil {
			return // told to stop mid-call: not the payment's fault
		}
		log.Printf("reconcile: asking about payment %s (%s old): %v", p.ID, age.Round(time.Second), err)
		rep.Errors++
		s.escalate(ctx, p, age, fmt.Sprintf("the provider could not be asked: %v", err), rep)
		return
	}

	switch {
	case res.Found && (res.Status == domain.PaymentAuthorized || res.Status == domain.PaymentDeclined):
		s.record(ctx, p, res.Status, res.ProviderPaymentID, res.Reason, rep)
	case res.Found:
		// The provider has it and has not decided (still processing, or waiting for
		// the customer to authenticate).
		rep.Waiting++
		s.escalate(ctx, p, age, "the provider has the payment but has not decided in all that time", rep)
	case p.Status == domain.PaymentProcessing && age >= s.AbandonAfter:
		s.record(ctx, p, domain.PaymentAbandoned, "", fmt.Sprintf("no record at the provider after %s", age.Round(time.Minute)), rep)
	default:
		// Too young for "no record" to be believed, or waiting for a person.
		rep.Waiting++
	}
}

// record writes the answer down, even if the job has been told to stop, and says
// what it found.
func (s *Service) record(ctx context.Context, p domain.Payment, status domain.PaymentStatus, providerPaymentID, reason string, rep *Report) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	result, err := settle.Apply(rctx, s.Store, p, status, providerPaymentID, reason)
	var unrecorded *settle.UnrecordedError
	switch {
	case errors.As(err, &unrecorded):
		rep.Errors++
		if status == domain.PaymentAuthorized {
			s.alert(domain.AlertUnrecordedCharge, p,
				"the provider says payment %s for order %s was authorized after the order moved on (%v), and the answer could not be recorded: %v",
				p.ID, p.OrderID, unrecorded.Conflict, unrecorded.Err)
		} else {
			log.Printf("reconcile: payment %s: %s could not be recorded, will try again: %v", p.ID, status, err)
		}
	case err != nil:
		log.Printf("reconcile: recording %s for payment %s: %v", status, p.ID, err)
		rep.Errors++
	case result == settle.AlreadyRecorded:
		rep.Skipped++
	default:
		if status == domain.PaymentAbandoned {
			rep.Abandoned++
			log.Printf("reconcile: payment %s for order %s abandoned: %s", p.ID, p.OrderID, reason)
		} else {
			rep.Settled++
			log.Printf("reconcile: payment %s for order %s found %s at the provider", p.ID, p.OrderID, status)
		}
		if result == settle.OrderMovedOn && status == domain.PaymentAuthorized {
			log.Printf("REFUND DUE: payment %s for order %s was authorized after the order moved on", p.ID, p.OrderID)
		}
	}
}

// escalate hands a payment that has waited too long to a person, once. A payment
// already waiting for review stays as it is.
func (s *Service) escalate(ctx context.Context, p domain.Payment, age time.Duration, why string, rep *Report) {
	if p.Status != domain.PaymentProcessing || age < s.ReviewAfter {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	_, marked, err := s.Store.MarkPaymentNeedsReview(rctx, p.ID, why)
	switch {
	case err != nil:
		log.Printf("reconcile: marking payment %s for review: %v", p.ID, err)
		rep.Errors++
	case marked:
		rep.Reviewing++
		s.alert(domain.AlertPaymentNeedsReview, p,
			"payment %s for order %s has had no answer for %s and was not sent again: %s",
			p.ID, p.OrderID, age.Round(time.Minute), why)
	}
}

// alert logs a problem for a person and, when the service has somewhere to keep
// it, keeps it as an alert, on a context that is not cancelled with the job.
func (s *Service) alert(kind string, p domain.Payment, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	log.Printf("reconcile: NEEDS REVIEW: %s", message)
	if s.Alerts == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), recordTimeout)
	defer cancel()
	if err := s.Alerts.RaiseAlert(ctx, kind, p.ID, p.OrderID, message); err != nil {
		log.Printf("reconcile: could not keep the alert for payment %s: %v", p.ID, err)
	}
}

// Run settles what waits for an answer now and then every interval until ctx is
// cancelled. It blocks, so start it in its own goroutine.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		log.Printf("reconcile: not running, the interval must be positive (got %v)", interval)
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		rep, err := s.RunOnce(ctx)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				log.Printf("%v", err)
			}
		case rep.any():
			log.Printf("reconcile: run done: %d settled, %d abandoned, %d for review, %d waiting, %d skipped, %d errors",
				rep.Settled, rep.Abandoned, rep.Reviewing, rep.Waiting, rep.Skipped, rep.Errors)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
