// Package alertnotify sends alerts out of the system, so that a problem only a person
// can fix does not wait for someone to open the dashboard. It runs as a background job
// beside the HTTP server, like the refund job.
package alertnotify

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// Defaults for NewService.
const (
	// DefaultLease is how long a claimed alert is held while it is being sent. It must
	// be longer than a send (the webhook gives up after 10 seconds).
	DefaultLease = time.Minute
	// DefaultReminder is how long an alert may stay open before it is sent again.
	DefaultReminder = 24 * time.Hour
	// DefaultBatchSize is how many alerts one run takes on.
	DefaultBatchSize = 20
	// DefaultBackoffBase and DefaultBackoffMax bound the wait after a failed send: it
	// doubles with every failure in a row, from the base up to the maximum.
	DefaultBackoffBase = 30 * time.Second
	DefaultBackoffMax  = 15 * time.Minute

	// recordTimeout bounds writing down that an alert was sent. It runs on a context that
	// is NOT cancelled when the job is told to stop: a message that went out must be
	// recorded even during shutdown, or it would be sent again at the next start.
	recordTimeout = 10 * time.Second
)

// Message is what is sent: the alert, and whether this is a reminder of one sent before.
type Message struct {
	Alert    domain.Alert
	Reminder bool
}

// Sink delivers a message somewhere a person will see it. An error means it was not
// delivered and will be tried again. It must not put anything secret (the address it
// posts to) in the error, because the job logs it.
type Sink interface {
	Send(ctx context.Context, m Message) error
}

// Service sends each open alert once, and again as a reminder while it stays open. An
// alert is claimed with a lease before it is sent, so any number of servers can run the
// job and an alert goes out once; a server that dies leaves a lease that ends, and the
// alert becomes due again.
//
// Delivery is at least once: if the message went out and writing that down failed, it
// is sent again after the lease. A duplicate message is the better mistake than none.
type Service struct {
	Store ports.AlertNotifyStore
	Sink  Sink

	Lease       time.Duration
	Reminder    time.Duration // 0 switches reminders off
	BatchSize   int
	BackoffBase time.Duration
	BackoffMax  time.Duration
}

// NewService returns a Service with the default lease, reminder, batch size and backoff.
func NewService(store ports.AlertNotifyStore, sink Sink) *Service {
	return &Service{
		Store: store, Sink: sink,
		Lease: DefaultLease, Reminder: DefaultReminder, BatchSize: DefaultBatchSize,
		BackoffBase: DefaultBackoffBase, BackoffMax: DefaultBackoffMax,
	}
}

// Report says what one run did.
type Report struct {
	Sent   int // delivered and recorded
	Failed int // not delivered; it will be tried again after a wait
	Errors int // delivered or not, but the outcome could not be recorded
}

func (r Report) any() bool { return r != Report{} }

// RunOnce sends every alert that is due now, up to the batch size, one after another. It
// returns an error only if the due alerts could not be read; a problem with one alert is
// logged, counted in the Report and does not stop the others.
func (s *Service) RunOnce(ctx context.Context) (Report, error) {
	var rep Report
	claims, err := s.Store.ClaimAlertsToNotify(ctx, s.BatchSize, s.Lease, s.Reminder)
	if err != nil {
		return rep, fmt.Errorf("alertnotify: reading the alerts that are due: %w", err)
	}
	for _, c := range claims {
		if ctx.Err() != nil {
			break // the ones not yet sent are held by a lease that ends, and then become due again
		}
		sendErr := s.Sink.Send(ctx, Message{Alert: c.Alert, Reminder: c.Reminder})
		if sendErr != nil && ctx.Err() != nil && errors.Is(sendErr, ctx.Err()) {
			break // stopped in the middle: not a failure of the alert, and not counted as one
		}

		rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
		if sendErr != nil {
			wait := s.backoff(c.Failures + 1)
			log.Printf("alertnotify: could not send alert %d (%s), attempt %d: %v; trying again in %s",
				c.Alert.ID, c.Alert.Kind, c.Failures+1, sendErr, wait)
			if err := s.Store.MarkAlertNotifyFailed(rec, c.Alert.ID, wait); err != nil {
				log.Printf("alertnotify: %v", err)
				rep.Errors++
			} else {
				rep.Failed++
			}
		} else if err := s.Store.MarkAlertNotified(rec, c.Alert.ID); err != nil {
			log.Printf("alertnotify: alert %d went out but %v; it will go out again", c.Alert.ID, err)
			rep.Errors++
		} else {
			rep.Sent++
		}
		cancel()
	}
	return rep, nil
}

// backoff is the wait after the nth failure in a row: the base, doubled each time, up to
// the maximum.
func (s *Service) backoff(failures int) time.Duration {
	wait := s.BackoffBase
	for i := 1; i < failures && wait < s.BackoffMax; i++ {
		wait *= 2
	}
	if wait > s.BackoffMax {
		wait = s.BackoffMax
	}
	return wait
}

// Run sends the due alerts now and then every interval until ctx is cancelled. It blocks,
// so start it in its own goroutine.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		log.Printf("alertnotify: not running, the interval must be positive (got %v)", interval)
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
			log.Printf("alertnotify: run done: %d sent, %d failed, %d errors", rep.Sent, rep.Failed, rep.Errors)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
