package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// memStore keeps payments and their orders in memory and follows the Postgres
// adapter's contract: an answer is recorded only for a payment that still waits
// for one, and moves its order only if the order is where it was expected.
type memStore struct {
	mu       sync.Mutex
	payments map[string]*domain.Payment
	orders   map[string]domain.OrderState // by order id

	listErr, takeErr, markErr, finishErr, completeErr error
	listedWith                                        struct {
		minAge time.Duration
		limit  int
	}
	takeOvers []string
}

func newMemStore() *memStore {
	return &memStore{payments: map[string]*domain.Payment{}, orders: map[string]domain.OrderState{}}
}

// add puts in a payment claimed `age` before t0 whose lease has run out, for an
// order waiting for it.
func (m *memStore) add(id string, status domain.PaymentStatus, age time.Duration, attempt int) *domain.Payment {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := &domain.Payment{
		ID: id, OrderID: "o-" + id, Status: status, AmountCents: 1447, Attempt: attempt,
		CreatedAt: t0.Add(-age), LeaseExpiresAt: t0.Add(-age + time.Minute),
	}
	m.payments[id] = p
	m.orders[p.OrderID] = domain.OrderPaymentPending
	return p
}

func (m *memStore) get(id string) domain.Payment {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.payments[id]
}

func (m *memStore) orderOf(id string) domain.OrderState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.orders["o-"+id]
}

func (m *memStore) ListUnsettledPayments(_ context.Context, minAge time.Duration, limit int) ([]domain.Payment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listedWith.minAge, m.listedWith.limit = minAge, limit
	if m.listErr != nil {
		return nil, m.listErr
	}
	var out []domain.Payment
	for _, p := range m.payments {
		if p.Unsettled() {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *memStore) TakeOverPayment(_ context.Context, id string, lease time.Duration) (domain.Payment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.takeOvers = append(m.takeOvers, id)
	if m.takeErr != nil {
		return domain.Payment{}, false, m.takeErr
	}
	p := m.payments[id]
	if p.Status != domain.PaymentProcessing || p.LeaseExpiresAt.After(t0) {
		return domain.Payment{}, false, nil
	}
	p.LeaseExpiresAt = t0.Add(lease)
	return *p, true, nil
}

func (m *memStore) MarkPaymentNeedsReview(ctx context.Context, id, reason string) (domain.Payment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil { // like a database call on a cancelled context
		return domain.Payment{}, false, err
	}
	if m.markErr != nil {
		return domain.Payment{}, false, m.markErr
	}
	p := m.payments[id]
	if p.Status != domain.PaymentProcessing {
		return domain.Payment{}, false, nil
	}
	p.Status, p.Reason = domain.PaymentNeedsReview, reason
	return *p, true, nil
}

func (m *memStore) CompletePayment(ctx context.Context, id string, status domain.PaymentStatus, providerPaymentID, reason string) (domain.Payment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Payment{}, err
	}
	return m.completeLocked(id, status, providerPaymentID, reason)
}

func (m *memStore) completeLocked(id string, status domain.PaymentStatus, providerPaymentID, reason string) (domain.Payment, error) {
	if m.completeErr != nil {
		return domain.Payment{}, m.completeErr
	}
	p := m.payments[id]
	if !p.Unsettled() {
		return domain.Payment{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotProcessing, id)
	}
	p.Status, p.ProviderPaymentID, p.Reason = status, providerPaymentID, reason
	return *p, nil
}

func (m *memStore) FinishPayment(ctx context.Context, id string, status domain.PaymentStatus, providerPaymentID, reason, from, to string) (domain.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	if m.finishErr != nil {
		return domain.Order{}, m.finishErr
	}
	p := m.payments[id]
	if !p.Unsettled() {
		return domain.Order{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotProcessing, id)
	}
	if string(m.orders[p.OrderID]) != from {
		return domain.Order{}, fmt.Errorf("%w: %s", ports.ErrStateConflict, p.OrderID)
	}
	// finishErr is not consulted by completeLocked's own check, so a store-wide
	// completeErr set by a test does not break a clean finish.
	saved := m.completeErr
	m.completeErr = nil
	_, err := m.completeLocked(id, status, providerPaymentID, reason)
	m.completeErr = saved
	if err != nil {
		return domain.Order{}, err
	}
	m.orders[p.OrderID] = domain.OrderState(to)
	return domain.Order{ID: p.OrderID, State: domain.OrderState(to)}, nil
}

// looker plays the provider: it answers each lookup through do, and remembers what it was asked.
type looker struct {
	mu    sync.Mutex
	asked []domain.LookupRequest
	do    func(domain.LookupRequest) (*domain.LookupResult, error)
}

func (l *looker) Lookup(_ context.Context, req domain.LookupRequest) (*domain.LookupResult, error) {
	l.mu.Lock()
	l.asked = append(l.asked, req)
	l.mu.Unlock()
	return l.do(req)
}

func (l *looker) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.asked)
}

func answers(res domain.LookupResult) *looker {
	return &looker{do: func(domain.LookupRequest) (*domain.LookupResult, error) { r := res; return &r, nil }}
}

func fails(msg string) *looker {
	return &looker{do: func(domain.LookupRequest) (*domain.LookupResult, error) { return nil, errors.New(msg) }}
}

var (
	succeeded = domain.LookupResult{Found: true, Status: domain.PaymentAuthorized, ProviderPaymentID: "pi_found"}
	refused   = domain.LookupResult{Found: true, Status: domain.PaymentDeclined, ProviderPaymentID: "pi_no", Reason: "Your card was declined."}
	undecided = domain.LookupResult{Found: true, ProviderPaymentID: "pi_wait"}
	noRecord  = domain.LookupResult{Found: false}
)

// spyAlerts remembers the alerts it is asked to keep.
type spyAlerts struct {
	mu     sync.Mutex
	raised []domain.Alert
	err    error
}

func (s *spyAlerts) RaiseAlert(_ context.Context, kind, subject, orderID, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.raised = append(s.raised, domain.Alert{Kind: kind, Subject: subject, OrderID: orderID, Message: message})
	return nil
}

func (s *spyAlerts) ListAlerts(context.Context, bool, int) ([]domain.Alert, error) { return nil, nil }

func (s *spyAlerts) ResolveAlert(context.Context, int64, string) (domain.Alert, error) {
	return domain.Alert{}, nil
}

func (s *spyAlerts) kinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, a := range s.raised {
		out = append(out, a.Kind+"/"+a.Subject)
	}
	return out
}

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

// service is a Service whose clock stands at t0.
func service(store *memStore, l *looker) (*Service, *spyAlerts) {
	alerts := &spyAlerts{}
	s := NewService(store, l)
	s.Alerts = alerts
	s.Now = func() time.Time { return t0 }
	return s, alerts
}

func run(t *testing.T, s *Service) Report {
	t.Helper()
	rep, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	return rep
}

// ---------------------------------------------------------------------------
// the provider has an answer
// ---------------------------------------------------------------------------

func TestAPaymentTheProviderSucceededIsRecordedAndTheOrderPaid(t *testing.T) {
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 10*time.Minute, 1)
	l := answers(succeeded)
	s, alerts := service(store, l)

	rep := run(t, s)
	if rep != (Report{Settled: 1}) {
		t.Errorf("report = %+v, want one settled", rep)
	}
	if p := store.get("a"); p.Status != domain.PaymentAuthorized || p.ProviderPaymentID != "pi_found" {
		t.Errorf("payment = %+v, want authorized with the provider's id", p)
	}
	if store.orderOf("a") != domain.OrderPaid {
		t.Errorf("order = %s, want paid", store.orderOf("a"))
	}
	if len(alerts.raised) != 0 {
		t.Errorf("alerts = %v, finding a charge is not a problem", alerts.kinds())
	}
	want := domain.LookupRequest{PaymentID: "a", CreatedAt: t0.Add(-10 * time.Minute), AmountCents: 1447}
	if len(l.asked) != 1 || l.asked[0] != want {
		t.Errorf("asked %+v, want exactly %+v", l.asked, want)
	}
}

func TestAPaymentTheProviderRefusedReopensTheOrderUnlessItWasTheLastAttempt(t *testing.T) {
	for _, c := range []struct {
		attempt int
		want    domain.OrderState
	}{{1, domain.OrderCreated}, {domain.MaxPaymentAttempts, domain.OrderCancelled}} {
		t.Run(fmt.Sprintf("attempt %d", c.attempt), func(t *testing.T) {
			store := newMemStore()
			store.add("a", domain.PaymentProcessing, 10*time.Minute, c.attempt)
			s, _ := service(store, answers(refused))
			rep := run(t, s)
			if rep != (Report{Settled: 1}) {
				t.Errorf("report = %+v", rep)
			}
			if p := store.get("a"); p.Status != domain.PaymentDeclined || p.Reason != "Your card was declined." {
				t.Errorf("payment = %+v, want declined with the reason", p)
			}
			if store.orderOf("a") != c.want {
				t.Errorf("order = %s, want %s", store.orderOf("a"), c.want)
			}
		})
	}
}

func TestAnAnswerForAnOrderThatWasCancelledIsKeptAndTheOrderLeftAlone(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 10*time.Minute, 1)
	store.orders["o-a"] = domain.OrderCancelled
	s, _ := service(store, answers(succeeded))

	if rep := run(t, s); rep != (Report{Settled: 1}) {
		t.Errorf("report = %+v", rep)
	}
	if p := store.get("a"); p.Status != domain.PaymentAuthorized {
		t.Errorf("payment = %s, want the charge kept: the refund job looks for it", p.Status)
	}
	if store.orderOf("a") != domain.OrderCancelled {
		t.Errorf("order = %s, want it left cancelled", store.orderOf("a"))
	}
	if !strings.Contains(logs.String(), "REFUND DUE") {
		t.Errorf("a charge on a cancelled order must say a refund is due:\n%s", logs)
	}
}

// A refusal moves no money, so a cancelled order with one has nothing to refund.
func TestARefusalForACancelledOrderDoesNotSayARefundIsDue(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 10*time.Minute, 1)
	store.orders["o-a"] = domain.OrderCancelled
	s, _ := service(store, answers(refused))
	if rep := run(t, s); rep != (Report{Settled: 1}) || store.get("a").Status != domain.PaymentDeclined {
		t.Errorf("report = %+v, payment = %s", rep, store.get("a").Status)
	}
	if strings.Contains(logs.String(), "REFUND DUE") {
		t.Errorf("a refusal was logged as a refund due:\n%s", logs)
	}
}

func TestAnAnswerThatCannotBeKeptForACancelledOrderRaisesAnAlertAndIsTriedAgain(t *testing.T) {
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 10*time.Minute, 1)
	store.orders["o-a"] = domain.OrderCancelled
	store.completeErr = errors.New("connection refused")
	s, alerts := service(store, answers(succeeded))

	if rep := run(t, s); rep != (Report{Errors: 1}) {
		t.Errorf("report = %+v, want one error", rep)
	}
	if got := alerts.kinds(); len(got) != 1 || got[0] != domain.AlertUnrecordedCharge+"/a" {
		t.Errorf("alerts = %v, want an unrecorded charge for a", got)
	}
	if store.get("a").Status != domain.PaymentProcessing {
		t.Error("the payment must stay as it was so the next run asks again")
	}
	// a refusal that could not be kept moved no money, so it is only logged
	store2 := newMemStore()
	store2.add("b", domain.PaymentProcessing, 10*time.Minute, 1)
	store2.orders["o-b"] = domain.OrderCancelled
	store2.completeErr = errors.New("connection refused")
	s2, alerts2 := service(store2, answers(refused))
	if rep := run(t, s2); rep != (Report{Errors: 1}) || len(alerts2.raised) != 0 {
		t.Errorf("report = %+v, alerts = %v, want an error and no alert", rep, alerts2.kinds())
	}
}

func TestAStorageFailureRecordingAnAnswerIsCountedAndTheNextPaymentStillSettles(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 10*time.Minute, 1)
	store.add("b", domain.PaymentProcessing, 10*time.Minute, 1)
	calls := 0
	store.finishErr = errors.New("connection refused")
	l := &looker{do: func(domain.LookupRequest) (*domain.LookupResult, error) {
		calls++
		if calls == 2 {
			store.mu.Lock()
			store.finishErr = nil // the store comes back
			store.mu.Unlock()
		}
		r := succeeded
		return &r, nil
	}}
	s, _ := service(store, l)
	rep := run(t, s)
	if rep != (Report{Settled: 1, Errors: 1}) {
		t.Errorf("report = %+v, want one error and one settled", rep)
	}
	if store.get("a").Status != domain.PaymentProcessing || store.get("b").Status != domain.PaymentAuthorized {
		t.Errorf("a = %s, b = %s", store.get("a").Status, store.get("b").Status)
	}
	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("the failure was not logged:\n%s", logs)
	}
}

func TestAnAnswerAnotherWriterRecordedFirstIsSkipped(t *testing.T) {
	store := newMemStore()
	p := store.add("a", domain.PaymentProcessing, 10*time.Minute, 1)
	l := &looker{do: func(domain.LookupRequest) (*domain.LookupResult, error) {
		store.mu.Lock()
		p.Status = domain.PaymentAuthorized // the webhook got there while we asked
		store.mu.Unlock()
		r := succeeded
		return &r, nil
	}}
	s, _ := service(store, l)
	if rep := run(t, s); rep != (Report{Skipped: 1}) {
		t.Errorf("report = %+v, want it skipped", rep)
	}
}

// ---------------------------------------------------------------------------
// the provider has no record
// ---------------------------------------------------------------------------

func TestAPaymentTheProviderNeverSawIsAbandonedOnceItIsOldEnough(t *testing.T) {
	logs := captureLog(t)
	for _, c := range []struct {
		name    string
		age     time.Duration
		attempt int
		abandon bool
		order   domain.OrderState
	}{
		{"too young", 29 * time.Minute, 1, false, domain.OrderPaymentPending},
		{"just old enough", 30 * time.Minute, 1, true, domain.OrderCreated},
		{"old", 3 * time.Hour, 2, true, domain.OrderCreated},
		{"old, last attempt", 3 * time.Hour, domain.MaxPaymentAttempts, true, domain.OrderCancelled},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := newMemStore()
			store.add("a", domain.PaymentProcessing, c.age, c.attempt)
			s, alerts := service(store, answers(noRecord))
			rep := run(t, s)
			if c.abandon {
				if rep != (Report{Abandoned: 1}) || store.get("a").Status != domain.PaymentAbandoned {
					t.Errorf("report = %+v, payment = %s, want abandoned", rep, store.get("a").Status)
				}
				if !strings.Contains(store.get("a").Reason, "no record at the provider after") {
					t.Errorf("reason = %q, a person reading the history must learn why", store.get("a").Reason)
				}
			} else if rep != (Report{Waiting: 1}) || store.get("a").Status != domain.PaymentProcessing {
				t.Errorf("report = %+v, payment = %s, want it left waiting", rep, store.get("a").Status)
			}
			if store.orderOf("a") != c.order {
				t.Errorf("order = %s, want %s", store.orderOf("a"), c.order)
			}
			if len(alerts.raised) != 0 {
				t.Errorf("alerts = %v: nobody is asked about a payment that never happened", alerts.kinds())
			}
		})
	}
	if !strings.Contains(logs.String(), "abandoned") {
		t.Errorf("an abandonment must be logged:\n%s", logs)
	}
}

// "No record" is only believed for a payment this job holds: one a person is
// looking at is never given up on by a machine.
func TestAPaymentWaitingForAPersonIsNeverAbandoned(t *testing.T) {
	store := newMemStore()
	store.add("a", domain.PaymentNeedsReview, 30*time.Hour, 1)
	s, alerts := service(store, answers(noRecord))
	rep := run(t, s)
	if rep != (Report{Waiting: 1}) || store.get("a").Status != domain.PaymentNeedsReview {
		t.Errorf("report = %+v, payment = %s, want it left for the person", rep, store.get("a").Status)
	}
	if len(alerts.raised) != 0 {
		t.Errorf("alerts = %v, the person was already asked", alerts.kinds())
	}
}

// ---------------------------------------------------------------------------
// the provider has it but has not decided, or cannot be asked
// ---------------------------------------------------------------------------

func TestAPaymentTheProviderHasNotDecidedWaitsUntilItIsTooOld(t *testing.T) {
	young := newMemStore()
	young.add("a", domain.PaymentProcessing, time.Hour, 1)
	s, alerts := service(young, answers(undecided))
	if rep := run(t, s); rep != (Report{Waiting: 1}) || young.get("a").Status != domain.PaymentProcessing || len(alerts.raised) != 0 {
		t.Errorf("young: payment %s, alerts %v", young.get("a").Status, alerts.kinds())
	}

	old := newMemStore()
	old.add("b", domain.PaymentProcessing, 20*time.Hour, 1)
	s, alerts = service(old, answers(undecided))
	rep := run(t, s)
	if rep != (Report{Waiting: 1, Reviewing: 1}) || old.get("b").Status != domain.PaymentNeedsReview {
		t.Errorf("old: report %+v, payment %s, want it handed to a person", rep, old.get("b").Status)
	}
	if got := alerts.kinds(); len(got) != 1 || got[0] != domain.AlertPaymentNeedsReview+"/b" {
		t.Errorf("alerts = %v, want one payment_needs_review for b", got)
	}
	if !strings.Contains(old.get("b").Reason, "has not decided") {
		t.Errorf("reason = %q", old.get("b").Reason)
	}
	if old.orderOf("b") != domain.OrderPaymentPending {
		t.Errorf("order = %s, want it left pending: the customer may have been charged", old.orderOf("b"))
	}
}

func TestAProviderThatCannotBeAskedIsRetriedUntilThePaymentIsTooOld(t *testing.T) {
	logs := captureLog(t)
	young := newMemStore()
	young.add("a", domain.PaymentProcessing, 2*time.Hour, 1)
	s, alerts := service(young, fails("stripe: HTTP 503"))
	if rep := run(t, s); rep != (Report{Errors: 1}) || young.get("a").Status != domain.PaymentProcessing || len(alerts.raised) != 0 {
		t.Errorf("young: payment %s, alerts %v", young.get("a").Status, alerts.kinds())
	}
	if !strings.Contains(logs.String(), "HTTP 503") {
		t.Errorf("the failure was not logged:\n%s", logs)
	}

	old := newMemStore()
	old.add("b", domain.PaymentProcessing, 21*time.Hour, 1)
	s, alerts = service(old, fails("stripe: HTTP 503"))
	rep := run(t, s)
	if rep != (Report{Errors: 1, Reviewing: 1}) || old.get("b").Status != domain.PaymentNeedsReview {
		t.Errorf("old: report %+v, payment %s", rep, old.get("b").Status)
	}
	if got := alerts.raised; len(got) != 1 || !strings.Contains(got[0].Message, "HTTP 503") || got[0].OrderID != "o-b" {
		t.Errorf("alerts = %+v, want the failure named for a person", got)
	}
}

func TestAPaymentAlreadyUnderReviewIsNotHandedToAPersonAgain(t *testing.T) {
	store := newMemStore()
	store.add("a", domain.PaymentNeedsReview, 30*time.Hour, 1)
	s, alerts := service(store, fails("stripe: HTTP 503"))
	if rep := run(t, s); rep != (Report{Errors: 1}) || len(alerts.raised) != 0 {
		t.Errorf("report = %+v, alerts = %v, want no second alert", rep, alerts.kinds())
	}
	s2, alerts2 := service(store, answers(undecided))
	if rep := run(t, s2); rep != (Report{Waiting: 1}) || len(alerts2.raised) != 0 {
		t.Errorf("report = %+v, alerts = %v, want no second alert", rep, alerts2.kinds())
	}
	if len(store.takeOvers) != 0 {
		t.Errorf("takeovers = %v: a payment waiting for review is only read, never held", store.takeOvers)
	}
}

func TestALateAnswerSettlesAPaymentThatWasHandedToAPerson(t *testing.T) {
	store := newMemStore()
	store.add("a", domain.PaymentNeedsReview, 30*time.Hour, 1)
	s, _ := service(store, answers(succeeded))
	if rep := run(t, s); rep != (Report{Settled: 1}) {
		t.Errorf("report = %+v", rep)
	}
	if store.get("a").Status != domain.PaymentAuthorized || store.orderOf("a") != domain.OrderPaid {
		t.Errorf("payment %s, order %s, want the charge recorded and the order paid", store.get("a").Status, store.orderOf("a"))
	}
}

func TestAnEmptyAnswerFromTheProviderIsAFailureNotAnAnswer(t *testing.T) {
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 2*time.Hour, 1)
	l := &looker{do: func(domain.LookupRequest) (*domain.LookupResult, error) { return nil, nil }}
	s, _ := service(store, l)
	if rep := run(t, s); rep != (Report{Errors: 1}) || store.get("a").Status != domain.PaymentProcessing {
		t.Errorf("report = %+v, payment = %s, want an error and no decision", rep, store.get("a").Status)
	}
}

func TestFailingToHandAPaymentToAPersonIsCountedAndTheNextRunTriesAgain(t *testing.T) {
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 25*time.Hour, 1)
	store.markErr = errors.New("connection refused")
	s, alerts := service(store, answers(undecided))
	if rep := run(t, s); rep != (Report{Waiting: 1, Errors: 1}) || len(alerts.raised) != 0 {
		t.Errorf("report = %+v, alerts = %v", rep, alerts.kinds())
	}
}

// A payment that was settled by someone else while we were deciding is not marked.
func TestAPaymentSettledMeanwhileIsNotHandedToAPerson(t *testing.T) {
	store := newMemStore()
	p := store.add("a", domain.PaymentProcessing, 25*time.Hour, 1)
	l := &looker{do: func(domain.LookupRequest) (*domain.LookupResult, error) {
		store.mu.Lock()
		p.Status = domain.PaymentAuthorized
		store.mu.Unlock()
		r := undecided
		return &r, nil
	}}
	s, alerts := service(store, l)
	if rep := run(t, s); rep != (Report{Waiting: 1}) || len(alerts.raised) != 0 {
		t.Errorf("report = %+v, alerts = %v, want no alert for a payment that is settled", rep, alerts.kinds())
	}
}

// ---------------------------------------------------------------------------
// holding the payment while we ask
// ---------------------------------------------------------------------------

func TestAPaymentSomeoneElseIsWorkingOnIsLeftAloneAndNotAsked(t *testing.T) {
	store := newMemStore()
	p := store.add("a", domain.PaymentProcessing, 10*time.Minute, 1)
	p.LeaseExpiresAt = t0.Add(time.Minute) // a retry holds it
	l := answers(succeeded)
	s, _ := service(store, l)
	if rep := run(t, s); rep != (Report{Skipped: 1}) || l.calls() != 0 {
		t.Errorf("report = %+v, lookups = %d, want it skipped without asking", rep, l.calls())
	}
}

func TestTheJobHoldsAPaymentForItsLeaseWhileItAsks(t *testing.T) {
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 10*time.Minute, 1)
	var held time.Time
	l := &looker{do: func(domain.LookupRequest) (*domain.LookupResult, error) {
		held = store.get("a").LeaseExpiresAt
		r := undecided
		return &r, nil
	}}
	s, _ := service(store, l)
	s.Lease = 3 * time.Minute
	run(t, s)
	if !held.Equal(t0.Add(3 * time.Minute)) {
		t.Errorf("lease while asking = %v, want %v", held, t0.Add(3*time.Minute))
	}
}

func TestAFailureTakingAPaymentIsCountedAndNothingIsAsked(t *testing.T) {
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 10*time.Minute, 1)
	store.takeErr = errors.New("connection refused")
	l := answers(succeeded)
	s, _ := service(store, l)
	if rep := run(t, s); rep != (Report{Errors: 1}) || l.calls() != 0 {
		t.Errorf("report = %+v, lookups = %d", rep, l.calls())
	}
}

// ---------------------------------------------------------------------------
// the run
// ---------------------------------------------------------------------------

func TestARunTakesAtMostABatchAndPassesTheMinimumAge(t *testing.T) {
	store := newMemStore()
	s, _ := service(store, answers(undecided))
	s.BatchSize, s.MinAge = 7, 5*time.Minute
	run(t, s)
	if store.listedWith.limit != 7 || store.listedWith.minAge != 5*time.Minute {
		t.Errorf("listed with %+v, want limit 7 and minimum age 5m", store.listedWith)
	}
}

func TestAListingFailureIsTheOnlyErrorARunReturns(t *testing.T) {
	store := newMemStore()
	store.listErr = errors.New("connection refused")
	s, _ := service(store, answers(undecided))
	if _, err := s.RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error = %v, want the listing failure", err)
	}
}

func TestAJobToldToStopLeavesTheRestAndStillRecordsWhatItAlreadyKnows(t *testing.T) {
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 10*time.Minute, 1)
	store.add("b", domain.PaymentProcessing, 10*time.Minute, 1)
	ctx, cancel := context.WithCancel(context.Background())
	l := &looker{do: func(domain.LookupRequest) (*domain.LookupResult, error) {
		cancel() // shutdown arrives while the first answer is in hand
		r := succeeded
		return &r, nil
	}}
	s, _ := service(store, l)
	rep, err := s.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep != (Report{Settled: 1}) || store.get("a").Status != domain.PaymentAuthorized {
		t.Errorf("report = %+v, a = %s: an answer in hand must be written down even during shutdown", rep, store.get("a").Status)
	}
	if store.get("b").Status != domain.PaymentProcessing || l.calls() != 1 {
		t.Errorf("b = %s after %d lookups, want it left for the next run", store.get("b").Status, l.calls())
	}
}

func TestALookupCutShortByShutdownIsNotHeldAgainstThePayment(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 25*time.Hour, 1)
	ctx, cancel := context.WithCancel(context.Background())
	l := &looker{do: func(domain.LookupRequest) (*domain.LookupResult, error) {
		cancel()
		return nil, context.Canceled
	}}
	s, alerts := service(store, l)
	rep, _ := s.RunOnce(ctx)
	if rep != (Report{}) || store.get("a").Status != domain.PaymentProcessing || len(alerts.raised) != 0 {
		t.Errorf("report = %+v, payment %s, alerts %v: a lookup stopped by shutdown says nothing about the payment", rep, store.get("a").Status, alerts.kinds())
	}
	if strings.Contains(logs.String(), "asking about payment") {
		t.Errorf("shutdown was logged as a problem:\n%s", logs)
	}
}

func TestWithoutAnAlertStoreTheProblemIsStillLogged(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 25*time.Hour, 1)
	s := NewService(store, answers(undecided))
	s.Now = func() time.Time { return t0 }
	if rep := run(t, s); rep.Reviewing != 1 {
		t.Errorf("report = %+v", rep)
	}
	if !strings.Contains(logs.String(), "NEEDS REVIEW") {
		t.Errorf("the problem was not logged:\n%s", logs)
	}
}

func TestFailingToKeepAnAlertIsLoggedAndTheJobGoesOn(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.add("a", domain.PaymentProcessing, 25*time.Hour, 1)
	store.add("b", domain.PaymentProcessing, 10*time.Minute, 1)
	s, alerts := service(store, answers(undecided))
	alerts.err = errors.New("connection refused")
	rep := run(t, s)
	if rep.Reviewing != 1 || rep.Waiting != 2 {
		t.Errorf("report = %+v", rep)
	}
	if !strings.Contains(logs.String(), "NEEDS REVIEW") || !strings.Contains(logs.String(), "could not keep the alert") {
		t.Errorf("the problem and the failure to keep it must both be logged:\n%s", logs)
	}
}

func TestTheClockDefaultsToTheRealOne(t *testing.T) {
	s := NewService(newMemStore(), answers(undecided))
	if d := time.Since(s.now()); d < 0 || d > time.Minute {
		t.Errorf("now() is %v away from the real time", d)
	}
}

func TestNewServiceSetsSensibleDefaults(t *testing.T) {
	s := NewService(newMemStore(), answers(undecided))
	if s.MinAge != DefaultMinAge || s.AbandonAfter != DefaultAbandonAfter || s.ReviewAfter != DefaultReviewAfter ||
		s.Lease != DefaultLease || s.BatchSize != DefaultBatchSize {
		t.Errorf("defaults = %+v", s)
	}
	// the timings only make sense in this order, and the review limit must sit
	// inside the 24 hours Stripe remembers an idempotency key
	if !(60*time.Second < s.MinAge && s.MinAge < s.AbandonAfter && s.AbandonAfter < s.ReviewAfter && s.ReviewAfter < 24*time.Hour) {
		t.Errorf("timings out of order: min age %v, abandon %v, review %v", s.MinAge, s.AbandonAfter, s.ReviewAfter)
	}
}

// ---------------------------------------------------------------------------
// Run: the loop
// ---------------------------------------------------------------------------

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

func TestRunWorksNowAndOnEveryTickUntilStopped(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.add("first", domain.PaymentProcessing, 10*time.Minute, 1)
	s, _ := service(store, answers(succeeded))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx, 5*time.Millisecond); close(done) }()
	waitFor(t, "the first payment (an immediate run)", func() bool { return store.get("first").Status == domain.PaymentAuthorized })

	store.add("second", domain.PaymentProcessing, 10*time.Minute, 1) // appears later; a later tick finds it
	waitFor(t, "the second payment (a later tick)", func() bool { return store.get("second").Status == domain.PaymentAuthorized })

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
	s, _ := service(store, answers(succeeded))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx, 5*time.Millisecond); close(done) }()
	waitFor(t, "the failure to be logged", func() bool { return strings.Contains(logs.String(), "connection refused") })

	store.mu.Lock()
	store.listErr = nil
	store.mu.Unlock()
	store.add("a", domain.PaymentProcessing, 10*time.Minute, 1)
	waitFor(t, "the payment once the store is back", func() bool { return store.get("a").Status == domain.PaymentAuthorized })
	cancel()
	<-done
}

func TestRunDoesNotLogTheListingErrorOfAStoppedRun(t *testing.T) {
	logs := captureLog(t)
	store := newMemStore()
	store.listErr = errors.New("context canceled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, _ := service(store, answers(succeeded))
	s.Run(ctx, time.Hour)
	if strings.Contains(logs.String(), "listing payments") {
		t.Errorf("a failure caused by shutting down was logged as a problem:\n%s", logs)
	}
}

func TestRunRefusesANonPositiveInterval(t *testing.T) {
	logs := captureLog(t)
	s, _ := service(newMemStore(), answers(succeeded))
	done := make(chan struct{})
	go func() { s.Run(context.Background(), 0); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run with a zero interval did not return")
	}
	if !strings.Contains(logs.String(), "interval must be positive") {
		t.Errorf("the refusal was not explained:\n%s", logs)
	}
}
