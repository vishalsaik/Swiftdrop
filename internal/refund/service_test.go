package refund

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// memStore is an in-memory RefundStore that follows the Postgres adapter's
// contract: a refund is due while it is "none" or its lease has expired, a claim
// is a compare-and-set that counts an attempt, and results are guarded by the
// refund being in progress. Every payment in it is owed (authorized, cancelled).
type memStore struct {
	mu       sync.Mutex
	payments map[string]*domain.Payment
	order    []string

	listErr, claimErr, completeErr, failErr error
	completeCalls, failCalls                []string
	failGiveUps                             []bool
}

func newMemStore() *memStore { return &memStore{payments: map[string]*domain.Payment{}} }

func (s *memStore) add(id string, amount int64) *domain.Payment {
	p := &domain.Payment{
		ID: id, OrderID: "o-" + id, Status: domain.PaymentAuthorized, AmountCents: amount,
		ProviderPaymentID: "pi_" + id, RefundStatus: domain.RefundNone,
	}
	s.payments[id] = p
	s.order = append(s.order, id)
	return p
}

func (s *memStore) due(p *domain.Payment) bool {
	return p.RefundStatus == domain.RefundNone ||
		(p.RefundStatus == domain.RefundInProgress && p.RefundLeaseExpiresAt != nil && !p.RefundLeaseExpiresAt.After(time.Now()))
}

// expire makes a held refund's lease run out, like waiting for it.
func (s *memStore) expire(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	past := time.Now().Add(-time.Second)
	s.payments[id].RefundLeaseExpiresAt = &past
}

func (s *memStore) get(id string) domain.Payment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.payments[id]
}

func (s *memStore) ListRefundable(_ context.Context, limit int) ([]domain.Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []domain.Payment
	for _, id := range s.order {
		if p := s.payments[id]; s.due(p) && len(out) < limit {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (s *memStore) ClaimRefund(_ context.Context, id string, lease time.Duration) (domain.Payment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return domain.Payment{}, false, s.claimErr
	}
	p, ok := s.payments[id]
	if !ok {
		return domain.Payment{}, false, ports.ErrPaymentNotFound
	}
	if !s.due(p) {
		return domain.Payment{}, false, nil
	}
	until := time.Now().Add(lease)
	p.RefundStatus, p.RefundAttempts, p.RefundLeaseExpiresAt = domain.RefundInProgress, p.RefundAttempts+1, &until
	return *p, true, nil
}

func (s *memStore) CompleteRefund(ctx context.Context, id, refundID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil { // like a database call on a cancelled context
		return err
	}
	s.completeCalls = append(s.completeCalls, id+":"+refundID)
	if s.completeErr != nil {
		return s.completeErr
	}
	p := s.payments[id]
	if p == nil || p.RefundStatus != domain.RefundInProgress {
		return ports.ErrRefundNotInProgress
	}
	p.RefundStatus, p.RefundID, p.RefundError, p.RefundLeaseExpiresAt = domain.RefundDone, refundID, "", nil
	return nil
}

func (s *memStore) FailRefund(ctx context.Context, id, message string, giveUp bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.failCalls = append(s.failCalls, id)
	s.failGiveUps = append(s.failGiveUps, giveUp)
	if s.failErr != nil {
		return s.failErr
	}
	p := s.payments[id]
	if p == nil || p.RefundStatus != domain.RefundInProgress {
		return ports.ErrRefundNotInProgress
	}
	p.RefundError = message
	if giveUp {
		p.RefundStatus, p.RefundLeaseExpiresAt = domain.RefundFailed, nil
	}
	return nil
}

// script is a Refunder that answers from a function and remembers its requests.
type script struct {
	mu    sync.Mutex
	calls []domain.RefundRequest
	do    func(n int, req domain.RefundRequest) (*domain.RefundResult, error)
}

func (r *script) Refund(_ context.Context, req domain.RefundRequest) (*domain.RefundResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, req)
	n := len(r.calls)
	r.mu.Unlock()
	return r.do(n, req)
}

func accepting() *script {
	return &script{do: func(_ int, req domain.RefundRequest) (*domain.RefundResult, error) {
		return &domain.RefundResult{RefundID: "re_" + req.PaymentID}, nil
	}}
}

func failing(msg string) *script {
	return &script{do: func(int, domain.RefundRequest) (*domain.RefundResult, error) { return nil, errors.New(msg) }}
}

// syncBuf is a log sink that is safe to read while the job's goroutine writes.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLog(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

func service(store *memStore, r *script) *Service { return NewService(store, r) }

// ---------------------------------------------------------------------------
// making refunds
// ---------------------------------------------------------------------------

func TestADueRefundIsMadeAndRecorded(t *testing.T) {
	store := newMemStore()
	store.add("a", 1447)
	r := accepting()

	rep, err := service(store, r).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep != (Report{Refunded: 1}) {
		t.Errorf("report = %+v, want one refund", rep)
	}
	if len(r.calls) != 1 {
		t.Fatalf("the refunder was called %d times", len(r.calls))
	}
	want := domain.RefundRequest{PaymentID: "a", ProviderPaymentID: "pi_a", AmountCents: 1447, IdempotencyKey: "refund-a"}
	if r.calls[0] != want {
		t.Errorf("request = %+v, want %+v", r.calls[0], want)
	}
	if p := store.get("a"); p.RefundStatus != domain.RefundDone || p.RefundID != "re_a" || p.RefundAttempts != 1 {
		t.Errorf("payment = %+v, want refunded as re_a after one attempt", p)
	}
}

func TestEveryDueRefundIsMadeOldestFirstAndOnlyOnce(t *testing.T) {
	store := newMemStore()
	for _, id := range []string{"a", "b", "c"} {
		store.add(id, 100)
	}
	r := accepting()
	s := service(store, r)

	rep, _ := s.RunOnce(context.Background())
	if rep.Refunded != 3 {
		t.Errorf("report = %+v, want 3 refunds", rep)
	}
	var order []string
	for _, c := range r.calls {
		order = append(order, c.PaymentID)
	}
	if !slices.Equal(order, []string{"a", "b", "c"}) {
		t.Errorf("order = %v, want a, b, c", order)
	}

	again, _ := s.RunOnce(context.Background())
	if again != (Report{}) || len(r.calls) != 3 {
		t.Errorf("a second run did %+v with %d total calls, want nothing", again, len(r.calls))
	}
}

func TestTheBatchSizeLimitsOneRunAndTheRestWaitForTheNext(t *testing.T) {
	store := newMemStore()
	for i := range 5 {
		store.add(fmt.Sprintf("p%d", i), 100)
	}
	s := service(store, accepting())
	s.BatchSize = 2

	for i, want := range []int{2, 2, 1} {
		rep, _ := s.RunOnce(context.Background())
		if rep.Refunded != want {
			t.Errorf("run %d refunded %d, want %d", i+1, rep.Refunded, want)
		}
	}
	for _, id := range []string{"p0", "p1", "p2", "p3", "p4"} {
		if store.get(id).RefundStatus != domain.RefundDone {
			t.Errorf("%s was never refunded", id)
		}
	}
}

func TestAFreeOrderNeedsNoProviderCall(t *testing.T) {
	store := newMemStore()
	store.add("free", 0)
	r := accepting()

	rep, _ := service(store, r).RunOnce(context.Background())
	if rep.Refunded != 1 || len(r.calls) != 0 {
		t.Errorf("report = %+v with %d provider calls, want it closed with none", rep, len(r.calls))
	}
	if got := store.get("free"); got.RefundStatus != domain.RefundDone || got.RefundID != "nothing-to-refund" {
		t.Errorf("payment = %+v, want it closed as nothing to refund", got)
	}
}

// ---------------------------------------------------------------------------
// failures
// ---------------------------------------------------------------------------

func TestAFailedAttemptIsRecordedAndRetriedAfterItsLease(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.add("a", 100)
	r := failing("stripe: HTTP 500")
	s := service(store, r)

	rep, _ := s.RunOnce(context.Background())
	if rep != (Report{Retrying: 1}) {
		t.Errorf("report = %+v, want one retrying", rep)
	}
	p := store.get("a")
	if p.RefundStatus != domain.RefundInProgress || !strings.Contains(p.RefundError, "HTTP 500") {
		t.Errorf("payment = %+v, want it still in progress with the error", p)
	}
	if strings.Contains(logs.String(), "NEEDS REVIEW") {
		t.Errorf("a first failure must not ask for a human:\n%s", logs)
	}

	// Still held: the next run leaves it alone.
	if rep, _ := s.RunOnce(context.Background()); rep != (Report{}) || len(r.calls) != 1 {
		t.Errorf("a run before the lease ran out did %+v with %d calls", rep, len(r.calls))
	}

	// After the lease the provider is asked again, and now it works.
	store.expire("a")
	r.do = accepting().do
	rep, _ = s.RunOnce(context.Background())
	if rep.Refunded != 1 {
		t.Errorf("report after the lease = %+v, want the retry to refund", rep)
	}
	if p := store.get("a"); p.RefundStatus != domain.RefundDone || p.RefundAttempts != 2 || p.RefundError != "" {
		t.Errorf("payment = %+v, want refunded on attempt 2 with the error cleared", p)
	}
	if r.calls[0].IdempotencyKey != r.calls[1].IdempotencyKey {
		t.Errorf("keys %q and %q differ, every attempt must use the same key", r.calls[0].IdempotencyKey, r.calls[1].IdempotencyKey)
	}
}

func TestTheJobGivesUpAfterTheMaximumAttemptsAndAsksForAPerson(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.add("a", 100)
	s := service(store, failing("no such charge"))
	s.MaxAttempts = 3

	var last Report
	for range 3 {
		last, _ = s.RunOnce(context.Background())
		store.expire("a")
	}
	if last != (Report{GaveUp: 1}) {
		t.Errorf("last report = %+v, want it to give up", last)
	}
	if got := store.get("a"); got.RefundStatus != domain.RefundFailed || got.RefundAttempts != 3 {
		t.Errorf("payment = %+v, want failed after 3 attempts", got)
	}
	if !slices.Equal(store.failGiveUps, []bool{false, false, true}) {
		t.Errorf("give-up flags = %v, want only the last attempt to give up", store.failGiveUps)
	}
	if !strings.Contains(logs.String(), "NEEDS REVIEW") {
		t.Errorf("giving up was not flagged for review:\n%s", logs)
	}

	// A payment that was given up on is not tried again.
	if rep, _ := s.RunOnce(context.Background()); rep != (Report{}) {
		t.Errorf("a run after giving up did %+v", rep)
	}
}

func TestARefunderThatReturnsNothingIsAFailureNotAPanic(t *testing.T) {
	for name, do := range map[string]func(int, domain.RefundRequest) (*domain.RefundResult, error){
		"nil, nil":     func(int, domain.RefundRequest) (*domain.RefundResult, error) { return nil, nil },
		"empty result": func(int, domain.RefundRequest) (*domain.RefundResult, error) { return &domain.RefundResult{}, nil },
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemStore()
			store.add("a", 100)
			rep, err := service(store, &script{do: do}).RunOnce(context.Background())
			if err != nil || rep != (Report{Retrying: 1}) {
				t.Errorf("report = %+v, err %v, want one retrying", rep, err)
			}
			if got := store.get("a").RefundStatus; got == domain.RefundDone {
				t.Error("a refund the provider never confirmed was recorded as done")
			}
		})
	}
}

func TestALongProviderErrorIsTruncated(t *testing.T) {
	store := newMemStore()
	store.add("a", 100)
	service(store, failing(strings.Repeat("x", 5000))).RunOnce(context.Background())
	if n := len(store.get("a").RefundError); n != maxErrorLength {
		t.Errorf("stored error is %d bytes, want it cut to %d", n, maxErrorLength)
	}
}

// The provider has the refund but writing it down failed. It is claimed again
// after the lease; the provider treats a repeat as already refunded.
func TestARefundThatCannotBeRecordedIsFlaggedAndFinishesOnTheNextClaim(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.add("a", 100)
	store.completeErr = errors.New("connection refused")
	r := accepting()
	s := service(store, r)

	rep, _ := s.RunOnce(context.Background())
	if rep != (Report{Errors: 1}) {
		t.Errorf("report = %+v, want one error", rep)
	}
	if !strings.Contains(logs.String(), "NEEDS REVIEW") {
		t.Errorf("the unrecorded refund was not flagged:\n%s", logs)
	}

	store.completeErr = nil
	store.expire("a")
	rep, _ = s.RunOnce(context.Background())
	if rep.Refunded != 1 || len(r.calls) != 2 || r.calls[0].IdempotencyKey != r.calls[1].IdempotencyKey {
		t.Errorf("report = %+v with calls %+v, want the second claim to finish it with the same key", rep, r.calls)
	}
}

func TestAFailureThatCannotBeRecordedIsCountedAndTheOthersContinue(t *testing.T) {
	store := newMemStore()
	store.add("a", 100)
	store.add("b", 100)
	store.failErr = errors.New("connection refused")
	r := &script{do: func(n int, req domain.RefundRequest) (*domain.RefundResult, error) {
		if req.PaymentID == "a" {
			return nil, errors.New("stripe down")
		}
		return &domain.RefundResult{RefundID: "re_b"}, nil
	}}

	rep, _ := service(store, r).RunOnce(context.Background())
	if rep != (Report{Refunded: 1, Errors: 1}) {
		t.Errorf("report = %+v, want a counted error and b refunded", rep)
	}
}

// ---------------------------------------------------------------------------
// claims
// ---------------------------------------------------------------------------

func TestAnUnclaimableRefundIsSkippedWithoutCallingTheProvider(t *testing.T) {
	store := newMemStore()
	store.add("a", 100)
	s := service(store, accepting())
	// Another worker's claim lands between our list and our claim.
	listing := &racingStore{memStore: store, beforeClaim: func() {
		until := time.Now().Add(time.Hour)
		store.payments["a"].RefundStatus, store.payments["a"].RefundLeaseExpiresAt = domain.RefundInProgress, &until
	}}
	s.Store = listing
	r := accepting()
	s.Refunder = r

	rep, _ := s.RunOnce(context.Background())
	if rep != (Report{Skipped: 1}) || len(r.calls) != 0 {
		t.Errorf("report = %+v with %d calls, want it skipped", rep, len(r.calls))
	}
}

// racingStore lets a test change the store between ListRefundable and ClaimRefund.
type racingStore struct {
	*memStore
	beforeClaim func()
}

func (s *racingStore) ClaimRefund(ctx context.Context, id string, lease time.Duration) (domain.Payment, bool, error) {
	if s.beforeClaim != nil {
		s.mu.Lock()
		s.beforeClaim()
		s.mu.Unlock()
	}
	return s.memStore.ClaimRefund(ctx, id, lease)
}

func TestAClaimErrorIsCountedAndTheOthersContinue(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.add("a", 100)
	store.claimErr = errors.New("connection refused")
	r := accepting()

	rep, _ := service(store, r).RunOnce(context.Background())
	if rep != (Report{Errors: 1}) || len(r.calls) != 0 {
		t.Errorf("report = %+v with %d calls, want one error and no refund", rep, len(r.calls))
	}
	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("the cause is not in the log:\n%s", logs)
	}
}

func TestAnErrorListingDueRefundsFailsTheRun(t *testing.T) {
	store := newMemStore()
	store.listErr = errors.New("connection refused")
	_, err := service(store, accepting()).RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error = %v, want the listing failure", err)
	}
}

// Several servers run the job at once: each payment is still refunded once.
func TestManyWorkersRefundEachPaymentExactlyOnce(t *testing.T) {
	store := newMemStore()
	const payments, workers = 30, 8
	for i := range payments {
		store.add(fmt.Sprintf("p%02d", i), 100)
	}
	var calls sync.Map
	var total atomic.Int32
	r := &script{do: func(_ int, req domain.RefundRequest) (*domain.RefundResult, error) {
		if _, dup := calls.LoadOrStore(req.PaymentID, true); dup {
			t.Errorf("payment %s was refunded twice", req.PaymentID)
		}
		total.Add(1)
		return &domain.RefundResult{RefundID: "re_" + req.PaymentID}, nil
	}}
	s := service(store, r)
	s.BatchSize = payments

	var wg sync.WaitGroup
	start := make(chan struct{})
	for range workers {
		wg.Go(func() {
			<-start
			_, _ = s.RunOnce(context.Background())
		})
	}
	close(start)
	wg.Wait()

	if total.Load() != payments {
		t.Errorf("%d refunds were made, want %d", total.Load(), payments)
	}
	for i := range payments {
		if got := store.get(fmt.Sprintf("p%02d", i)); got.RefundStatus != domain.RefundDone || got.RefundAttempts != 1 {
			t.Errorf("p%02d = %+v", i, got)
		}
	}
}

// ---------------------------------------------------------------------------
// shutdown
// ---------------------------------------------------------------------------

// The job is told to stop while the provider call is in flight. The refund that
// went through is still written down, on a context that was not cancelled.
func TestARefundThatCompletesWhileShuttingDownIsStillRecorded(t *testing.T) {
	store := newMemStore()
	store.add("a", 100)
	ctx, cancel := context.WithCancel(context.Background())
	r := &script{do: func(_ int, req domain.RefundRequest) (*domain.RefundResult, error) {
		cancel() // shutdown arrives mid-call
		return &domain.RefundResult{RefundID: "re_a"}, nil
	}}

	rep, _ := service(store, r).RunOnce(ctx)
	if rep.Refunded != 1 {
		t.Errorf("report = %+v, want the refund recorded", rep)
	}
	if got := store.get("a").RefundStatus; got != domain.RefundDone {
		t.Errorf("refund status = %s, want done", got)
	}
}

// A call cut short by shutdown did not fail on its own: it is not counted
// against the payment, and the lease makes it due again.
func TestAnAttemptCutShortByShutdownIsNotRecordedAsAFailure(t *testing.T) {
	store := newMemStore()
	store.add("a", 100)
	ctx, cancel := context.WithCancel(context.Background())
	r := &script{do: func(int, domain.RefundRequest) (*domain.RefundResult, error) {
		cancel()
		return nil, context.Canceled
	}}

	rep, _ := service(store, r).RunOnce(ctx)
	if rep != (Report{}) || len(store.failCalls) != 0 {
		t.Errorf("report = %+v with fail calls %v, want nothing recorded", rep, store.failCalls)
	}
}

func TestARunStopsStartingNewRefundsOnceToldToStop(t *testing.T) {
	store := newMemStore()
	for _, id := range []string{"a", "b", "c"} {
		store.add(id, 100)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &script{do: func(n int, req domain.RefundRequest) (*domain.RefundResult, error) {
		if n == 1 {
			cancel()
		}
		return &domain.RefundResult{RefundID: "re_" + req.PaymentID}, nil
	}}

	service(store, r).RunOnce(ctx)
	if len(r.calls) != 1 {
		t.Errorf("%d refunds were started, want the run to stop after the first", len(r.calls))
	}
	if store.get("b").RefundStatus != domain.RefundNone {
		t.Error("b was touched after the stop")
	}
}

// ---------------------------------------------------------------------------
// Run: the loop
// ---------------------------------------------------------------------------

func TestRunWorksNowAndOnEveryTickUntilStopped(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.add("first", 100)
	r := accepting()
	s := service(store, r)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx, 5*time.Millisecond); close(done) }()

	waitFor(t, "the first refund (an immediate run)", func() bool { return store.get("first").RefundStatus == domain.RefundDone })

	store.mu.Lock()
	store.add("second", 100) // appears later; the next tick should find it
	store.mu.Unlock()
	waitFor(t, "the second refund (a later tick)", func() bool { return store.get("second").RefundStatus == domain.RefundDone })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
	if !strings.Contains(logs.String(), "run done") {
		t.Errorf("a run that did something was not logged:\n%s", logs)
	}
}

func TestRunLogsAListingFailureAndKeepsGoing(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.listErr = errors.New("connection refused")
	s := service(store, accepting())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx, 5*time.Millisecond); close(done) }()
	waitFor(t, "the failure to be logged", func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return strings.Contains(logs.String(), "connection refused")
	})

	store.mu.Lock()
	store.listErr = nil
	store.add("a", 100)
	store.mu.Unlock()
	waitFor(t, "the refund once the store is back", func() bool { return store.get("a").RefundStatus == domain.RefundDone })
	cancel()
	<-done
}

func TestRunDoesNotLogTheListingErrorOfAStoppedRun(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.listErr = errors.New("context canceled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service(store, accepting()).Run(ctx, time.Hour)
	if strings.Contains(logs.String(), "listing due refunds") {
		t.Errorf("a failure caused by shutting down was logged as a problem:\n%s", logs)
	}
}

func TestRunRefusesANonPositiveInterval(t *testing.T) {
	logs := captureLog(t)
	done := make(chan struct{})
	go func() { service(newMemStore(), accepting()).Run(context.Background(), 0); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run with a zero interval did not return")
	}
	if !strings.Contains(logs.String(), "interval must be positive") {
		t.Errorf("the refusal was not explained:\n%s", logs)
	}
}

func TestNewServiceSetsSensibleDefaults(t *testing.T) {
	s := NewService(newMemStore(), accepting())
	if s.Lease != DefaultLease || s.MaxAttempts != DefaultMaxAttempts || s.BatchSize != DefaultBatchSize {
		t.Errorf("defaults = lease %v, attempts %d, batch %d", s.Lease, s.MaxAttempts, s.BatchSize)
	}
	if s.Lease < 30*time.Second {
		t.Errorf("a lease of %v is shorter than a slow provider call", s.Lease)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
