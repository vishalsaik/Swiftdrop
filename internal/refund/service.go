// Package refund gives money back for orders that were charged and then
// cancelled. It runs as a background job beside the HTTP server.
package refund

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

// Defaults for NewService.
const (
	DefaultLease       = 2 * time.Minute
	DefaultMaxAttempts = 5
	DefaultBatchSize   = 20

	// recordTimeout bounds writing a result down after the provider answered. It
	// runs on a context that is NOT cancelled when the job is told to stop: a
	// refund that went through must be recorded even during shutdown.
	recordTimeout = 10 * time.Second

	// maxErrorLength keeps a provider's error text a reasonable size to store.
	maxErrorLength = 500
)

// Service finds refunds that are due and makes them. A refund is due when a
// payment was authorized, its order is cancelled and it has not been refunded
// (see ports.RefundStore). Each one is claimed with a lease before the provider
// is called, so any number of servers can run the job at once and a payment is
// still refunded once; a worker that dies leaves a lease that expires, and the
// refund becomes due again.
type Service struct {
	Store    ports.RefundStore
	Refunder payment.Refunder
	// Alerts, if set, keeps the problems only a person can fix (a refund given
	// up on, a refund that could not be recorded) where the dashboard shows them.
	// They are logged either way.
	Alerts ports.AlertStore

	// Lease is how long a claimed refund is held. It is also the wait before a
	// failed attempt is tried again, so it must be longer than the provider call.
	Lease time.Duration
	// MaxAttempts is how many times a refund is tried before the job gives up
	// and leaves it for a person (status failed).
	MaxAttempts int
	// BatchSize is how many due refunds one run takes on.
	BatchSize int
}

// NewService returns a Service with the default lease, attempts and batch size.
func NewService(store ports.RefundStore, refunder payment.Refunder) *Service {
	return &Service{
		Store: store, Refunder: refunder,
		Lease: DefaultLease, MaxAttempts: DefaultMaxAttempts, BatchSize: DefaultBatchSize,
	}
}

// Report says what one run did.
type Report struct {
	Refunded int // the provider accepted the refund and it was recorded
	Retrying int // the attempt failed; it will be tried again after its lease
	GaveUp   int // the attempt failed for the last time; a person must look
	Skipped  int // someone else holds it, or it is no longer owed
	Errors   int // storage failures; the payment is left as it was
}

func (r Report) any() bool { return r != Report{} }

// RunOnce makes every refund that is due now, up to the batch size, one after
// another. It returns an error only if the list of due refunds could not be
// read; a problem with one payment is logged, counted in the Report and does
// not stop the others.
func (s *Service) RunOnce(ctx context.Context) (Report, error) {
	var rep Report
	due, err := s.Store.ListRefundable(ctx, s.BatchSize)
	if err != nil {
		return rep, fmt.Errorf("refund: listing due refunds: %w", err)
	}
	for _, d := range due {
		if ctx.Err() != nil {
			break // told to stop: whatever is left is due again next time
		}
		s.refundOne(ctx, d, &rep)
	}
	return rep, nil
}

func (s *Service) refundOne(ctx context.Context, due domain.Payment, rep *Report) {
	p, ok, err := s.Store.ClaimRefund(ctx, due.ID, s.Lease)
	switch {
	case err != nil:
		log.Printf("refund: claiming payment %s: %v", due.ID, err)
		rep.Errors++
		return
	case !ok:
		rep.Skipped++ // another worker got it, or it is no longer owed
		return
	}

	// record writes a result down even if the job has been told to stop.
	record := func(write func(ctx context.Context) error) error {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
		defer cancel()
		return write(rctx)
	}

	if p.AmountCents <= 0 {
		// A free order was charged nothing: there is nothing to give back.
		s.complete(record, p, "nothing-to-refund", rep)
		return
	}

	result, err := s.Refunder.Refund(ctx, domain.RefundRequest{
		PaymentID:         p.ID,
		ProviderPaymentID: p.ProviderPaymentID,
		AmountCents:       p.AmountCents,
		IdempotencyKey:    "refund-" + p.ID,
	})
	if err == nil && (result == nil || result.RefundID == "") {
		err = errors.New("the refunder returned neither a refund nor an error")
	}
	if err != nil {
		if ctx.Err() != nil {
			// The job was stopped mid-call; the call did not fail on its own, so
			// do not count it against the payment. The lease will expire.
			return
		}
		s.failed(record, p, err, rep)
		return
	}
	s.complete(record, p, result.RefundID, rep)
}

func (s *Service) complete(record func(func(context.Context) error) error, p domain.Payment, refundID string, rep *Report) {
	if err := record(func(ctx context.Context) error { return s.Store.CompleteRefund(ctx, p.ID, refundID) }); err != nil {
		// The provider has the refund but we could not write it down. When the
		// lease expires it is claimed again and the provider (which treats a
		// repeat as already refunded) lets it finish.
		s.alert(domain.AlertRefundUnrecorded, p,
			"payment %s was refunded as %s but this could not be recorded: %v", p.ID, refundID, err)
		rep.Errors++
		return
	}
	log.Printf("refund: payment %s for order %s refunded (%s)", p.ID, p.OrderID, refundID)
	rep.Refunded++
}

func (s *Service) failed(record func(func(context.Context) error) error, p domain.Payment, cause error, rep *Report) {
	msg := cause.Error()
	if len(msg) > maxErrorLength {
		msg = msg[:maxErrorLength]
	}
	giveUp := p.RefundAttempts >= s.MaxAttempts
	if err := record(func(ctx context.Context) error { return s.Store.FailRefund(ctx, p.ID, msg, giveUp) }); err != nil {
		log.Printf("refund: payment %s: the refund failed (%s) and that could not be recorded: %v", p.ID, msg, err)
		rep.Errors++
		return
	}
	if giveUp {
		s.alert(domain.AlertRefundGivenUp, p,
			"payment %s for order %s could not be refunded after %d attempts: %s", p.ID, p.OrderID, p.RefundAttempts, msg)
		rep.GaveUp++
		return
	}
	log.Printf("refund: payment %s attempt %d of %d failed, will retry: %s", p.ID, p.RefundAttempts, s.MaxAttempts, msg)
	rep.Retrying++
}

// alert logs a problem for a person and, when the service has somewhere to keep
// it, keeps it as an alert. It uses a context that is not cancelled with the job,
// like the results it records.
func (s *Service) alert(kind string, p domain.Payment, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	log.Printf("refund: NEEDS REVIEW: %s", message)
	if s.Alerts == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), recordTimeout)
	defer cancel()
	if err := s.Alerts.RaiseAlert(ctx, kind, p.ID, p.OrderID, message); err != nil {
		log.Printf("refund: could not keep the alert for payment %s: %v", p.ID, err)
	}
}

// Run makes the due refunds now and then every interval until ctx is cancelled.
// It blocks, so start it in its own goroutine.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		log.Printf("refund: not running, the interval must be positive (got %v)", interval)
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
			log.Printf("refund: run done: %d refunded, %d retrying, %d given up, %d skipped, %d errors",
				rep.Refunded, rep.Retrying, rep.GaveUp, rep.Skipped, rep.Errors)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
