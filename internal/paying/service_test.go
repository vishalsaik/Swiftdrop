package paying

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/payment/providers"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// store is one order and its payments in memory, following the Postgres adapter's
// contract: every write guarded by the state it expects, and the same sentinel errors.
type store struct {
	order    domain.Order
	payments []domain.Payment // oldest attempt first

	// errors to return, by call
	getOrderErr, getPaymentErr, startErr, finishErr, completeErr, takeErr, markErr error
	// before is run inside FinishPayment, before it looks, to change things under a request
	before func(*store)
}

func (s *store) latest() (domain.Payment, bool) {
	if len(s.payments) == 0 {
		return domain.Payment{}, false
	}
	return s.payments[len(s.payments)-1], true
}

func (s *store) set(p domain.Payment) {
	s.payments[len(s.payments)-1] = p
}

func (s *store) GetOrderbyId(_ context.Context, id string) (domain.Order, error) {
	if s.getOrderErr != nil {
		return domain.Order{}, s.getOrderErr
	}
	return s.order, nil
}

func (s *store) GetPaymentByOrder(_ context.Context, orderID string) (domain.Payment, error) {
	if s.getPaymentErr != nil {
		return domain.Payment{}, s.getPaymentErr
	}
	p, ok := s.latest()
	if !ok {
		return domain.Payment{}, fmt.Errorf("%w: order %s", ports.ErrPaymentNotFound, orderID)
	}
	return p, nil
}

func (s *store) StartPayment(_ context.Context, p domain.Payment, lease time.Duration, from, to string) (domain.Payment, bool, error) {
	if s.startErr != nil {
		return domain.Payment{}, false, s.startErr
	}
	attempt := 1
	if last, ok := s.latest(); ok {
		if last.Status != domain.PaymentDeclined && last.Status != domain.PaymentAbandoned {
			return last, false, nil
		}
		attempt = last.Attempt + 1
	}
	if string(s.order.State) != from {
		return domain.Payment{}, false, fmt.Errorf("%w: %s", ports.ErrStateConflict, s.order.ID)
	}
	p.Status, p.Attempt, p.CreatedAt = domain.PaymentProcessing, attempt, time.Now()
	p.LeaseExpiresAt = time.Now().Add(lease)
	s.payments = append(s.payments, p)
	s.order.State = domain.OrderState(to)
	return p, true, nil
}

func (s *store) FinishPayment(_ context.Context, id string, status domain.PaymentStatus, providerID, reason, from, to string) (domain.Order, error) {
	if s.before != nil {
		s.before(s)
	}
	if s.finishErr != nil {
		return domain.Order{}, s.finishErr
	}
	p, _ := s.latest()
	if !p.Unsettled() {
		return domain.Order{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotProcessing, id)
	}
	if string(s.order.State) != from {
		return domain.Order{}, fmt.Errorf("%w: %s", ports.ErrStateConflict, s.order.ID)
	}
	p.Status, p.ProviderPaymentID, p.Reason = status, providerID, reason
	s.set(p)
	s.order.State = domain.OrderState(to)
	return s.order, nil
}

func (s *store) CompletePayment(_ context.Context, id string, status domain.PaymentStatus, providerID, reason string) (domain.Payment, error) {
	if s.completeErr != nil {
		return domain.Payment{}, s.completeErr
	}
	p, _ := s.latest()
	if !p.Unsettled() {
		return domain.Payment{}, fmt.Errorf("%w: %s", ports.ErrPaymentNotProcessing, id)
	}
	p.Status, p.ProviderPaymentID, p.Reason = status, providerID, reason
	s.set(p)
	return p, nil
}

func (s *store) TakeOverPayment(_ context.Context, id string, lease time.Duration) (domain.Payment, bool, error) {
	if s.takeErr != nil {
		return domain.Payment{}, false, s.takeErr
	}
	p, _ := s.latest()
	if p.Status != domain.PaymentProcessing || p.LeaseExpiresAt.After(time.Now()) {
		return domain.Payment{}, false, nil
	}
	p.LeaseExpiresAt = time.Now().Add(lease)
	s.set(p)
	return p, true, nil
}

func (s *store) MarkPaymentNeedsReview(_ context.Context, id, reason string) (domain.Payment, bool, error) {
	if s.markErr != nil {
		return domain.Payment{}, false, s.markErr
	}
	p, _ := s.latest()
	if p.Status != domain.PaymentProcessing {
		return domain.Payment{}, false, nil
	}
	p.Status, p.Reason = domain.PaymentNeedsReview, reason
	s.set(p)
	return p, true, nil
}

// gateway answers every charge as told and remembers what it was asked. It also
// checks requests the way Stripe's gateway does: a payment method is required
// when needsMethod is set.
type gateway struct {
	mu          sync.Mutex
	result      *domain.PaymentResult
	err         error
	calls       []domain.PaymentRequest
	needsMethod bool
}

func (g *gateway) Charge(_ context.Context, req domain.PaymentRequest) (*domain.PaymentResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, req)
	return g.result, g.err
}

func (g *gateway) Check(req domain.PaymentRequest) error {
	if g.needsMethod && req.PaymentMethodID == "" {
		return fmt.Errorf("%w: paymentMethodId is required", payment.ErrInvalidRequest)
	}
	return nil
}

func approves() *gateway {
	return &gateway{result: &domain.PaymentResult{PaymentID: "pi_1", Status: domain.PaymentAuthorized}}
}

func declines(reason string) *gateway {
	return &gateway{result: &domain.PaymentResult{PaymentID: "pi_1", Status: domain.PaymentDeclined, Reason: reason}}
}

// alerts remembers the alerts it is asked to raise.
type alerts struct {
	kinds []string
	texts []string
}

func (a *alerts) raise(_ context.Context, kind, subject, orderID, format string, args ...any) {
	a.kinds = append(a.kinds, kind+"/"+subject+"/"+orderID)
	a.texts = append(a.texts, fmt.Sprintf(format, args...))
}

type rig struct {
	svc    *Service
	store  *store
	gw     *gateway
	alerts *alerts
}

func newRig(g *gateway) *rig {
	st := &store{order: domain.Order{
		ID: "o1", State: domain.OrderCreated, PaymentType: domain.PaymentCard,
		Items: []domain.OrderItem{{SKU: "S", Name: "Meal", Quantity: 2, UnitPriceCents: 500}},
	}}
	a := &alerts{}
	return &rig{
		svc: &Service{
			DB: st, Lookup: st,
			Payment: payment.NewRouter(&providers.CardProvider{Gateway: g}, &providers.CashProvider{}, &providers.WalletProvider{Gateway: g}),
			Alert:   a.raise,
		},
		store: st, gw: g, alerts: a,
	}
}

func (r *rig) pay(method Method) Outcome {
	return r.svc.Pay(context.Background(), r.store.order, method)
}

// noBody is a request with no payment method.
func noBody() (string, error) { return "", nil }

func withMethod(id string) Method { return func() (string, error) { return id, nil } }

// existing puts in a payment of the given status, claimed `age` ago with its lease run out.
func (r *rig) existing(status domain.PaymentStatus, orderState domain.OrderState, age time.Duration, attempt int) {
	r.store.order.State = orderState
	r.store.payments = append(r.store.payments, domain.Payment{
		ID: fmt.Sprintf("pay-%d", attempt), OrderID: "o1", Status: status, AmountCents: 1000, Attempt: attempt,
		CreatedAt: time.Now().Add(-age), LeaseExpiresAt: time.Now().Add(-time.Minute),
	})
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

func want(t *testing.T, got Outcome, kind Kind, msgPart string) {
	t.Helper()
	if got.Kind != kind || !strings.Contains(got.Message, msgPart) {
		t.Errorf("outcome = %+v, want kind %d with %q in the message", got, kind, msgPart)
	}
}

// ---------------------------------------------------------------------------
// a new payment
// ---------------------------------------------------------------------------

func TestANewPaymentIsClaimedChargedUnderItsOwnIdAndRecorded(t *testing.T) {
	r := newRig(approves())
	out := r.pay(withMethod("pm_x"))
	if out.Kind != Paid || out.Order.State != domain.OrderPaid {
		t.Fatalf("outcome = %+v, want Paid with the order paid", out)
	}
	p, _ := r.store.latest()
	if p.Status != domain.PaymentAuthorized || p.ProviderPaymentID != "pi_1" || p.Attempt != 1 {
		t.Errorf("payment = %+v", p)
	}
	if len(r.gw.calls) != 1 {
		t.Fatalf("charges = %d, want 1", len(r.gw.calls))
	}
	c := r.gw.calls[0]
	if c.IdempotencyKey != p.ID || c.AmountCents != 1000 || c.Currency != "USD" || c.PaymentMethodID != "pm_x" || c.OrderID != "o1" {
		t.Errorf("charge = %+v, want the payment's own id as the key and the stored amount", c)
	}
}

func TestADeclineSaysWhetherAndHowOftenTheCustomerMayTryAgain(t *testing.T) {
	r := newRig(declines("Your card was declined."))
	out := r.pay(noBody)
	if !reflect.DeepEqual(out, Outcome{Kind: Declined, Message: "Your card was declined.", CanRetry: true, AttemptsLeft: domain.MaxPaymentAttempts - 1}) {
		t.Errorf("outcome = %+v", out)
	}
	if r.store.order.State != domain.OrderCreated {
		t.Errorf("order = %s, want it open again", r.store.order.State)
	}

	// the last attempt cancels the order instead
	last := newRig(declines(""))
	last.existing(domain.PaymentDeclined, domain.OrderCreated, time.Hour, domain.MaxPaymentAttempts-1)
	out = last.pay(noBody)
	if !reflect.DeepEqual(out, Outcome{Kind: Declined, Message: "payment declined", CanRetry: false, AttemptsLeft: 0}) {
		t.Errorf("last attempt = %+v, want no retry and the fallback reason", out)
	}
	if last.store.order.State != domain.OrderCancelled {
		t.Errorf("order = %s, want cancelled", last.store.order.State)
	}
}

func TestAnOrderNotOpenForPaymentIsRefusedBeforeAnythingHappens(t *testing.T) {
	r := newRig(approves())
	r.store.order.State = domain.OrderCancelled
	want(t, r.pay(noBody), Conflict, "order o1 cannot be paid from state cancelled")
	if len(r.gw.calls) != 0 || len(r.store.payments) != 0 {
		t.Errorf("charges = %d, payments = %d, want nothing", len(r.gw.calls), len(r.store.payments))
	}
}

func TestAnOrderWithAPaymentTypeWeCannotHandleIsAnInternalError(t *testing.T) {
	logs := captureLog(t)
	r := newRig(approves())
	r.store.order.PaymentType = "bitcoin"
	want(t, r.pay(noBody), Internal, "Payment is not available")
	if len(r.store.payments) != 0 || !strings.Contains(logs.String(), "no payment provider") {
		t.Errorf("payments = %d, log:\n%s", len(r.store.payments), logs)
	}
}

// ---------------------------------------------------------------------------
// the request body
// ---------------------------------------------------------------------------

func TestABodyThatCannotBeReadRefusesTheRequestBeforeAnythingIsClaimed(t *testing.T) {
	logs := captureLog(t)
	for _, c := range []struct {
		name   string
		err    error
		kind   Kind
		inText string
	}{
		{"too large", ErrBodyTooLarge, TooLarge, "request body too large"},
		{"not JSON", &BadBodyError{Err: errors.New("unexpected end of JSON input")}, BadRequest, "invalid request body: unexpected end of JSON input"},
		{"some other failure", errors.New("disk on fire"), Internal, "Failed to read the request"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(approves())
			want(t, r.pay(func() (string, error) { return "", c.err }), c.kind, c.inText)
			if len(r.store.payments) != 0 || len(r.gw.calls) != 0 {
				t.Errorf("payments = %d, charges = %d, want nothing claimed or sent", len(r.store.payments), len(r.gw.calls))
			}
		})
	}
	if !strings.Contains(logs.String(), "disk on fire") {
		t.Errorf("the unexpected failure was not logged:\n%s", logs)
	}
}

func TestARequestTheProviderCouldNeverFulfilIsRefusedBeforeAnythingIsClaimed(t *testing.T) {
	g := approves()
	g.needsMethod = true
	r := newRig(g)
	want(t, r.pay(noBody), Unprocessable, "paymentMethodId is required")
	if len(r.store.payments) != 0 || len(g.calls) != 0 || r.store.order.State != domain.OrderCreated {
		t.Errorf("payments = %d, charges = %d, order = %s", len(r.store.payments), len(g.calls), r.store.order.State)
	}
	if out := r.pay(withMethod("pm_ok")); out.Kind != Paid {
		t.Errorf("with a method: %+v", out)
	}
}

// A retry of a finished payment never reads the body, so a bad one cannot spoil it.
func TestAReplayNeverReadsTheBody(t *testing.T) {
	r := newRig(approves())
	r.pay(noBody)
	read := false
	out := r.pay(func() (string, error) { read = true; return "", &BadBodyError{Err: errors.New("garbage")} })
	if out.Kind != Paid || read {
		t.Errorf("outcome = %+v, body read = %v, want the success replayed without reading", out, read)
	}
}

// ---------------------------------------------------------------------------
// a retry
// ---------------------------------------------------------------------------

func TestARetryOfAPaidOrderReplaysTheSuccessWithTheCurrentOrderAndDoesNotCharge(t *testing.T) {
	r := newRig(approves())
	r.pay(noBody)
	r.store.order.State = domain.OrderPreparing // it moved on since
	out := r.pay(noBody)
	if out.Kind != Paid || out.Order.State != domain.OrderPreparing {
		t.Errorf("outcome = %+v, want the current order", out)
	}
	if len(r.gw.calls) != 1 {
		t.Errorf("charges = %d, want 1", len(r.gw.calls))
	}
}

func TestAReplayWhoseOrderCannotBeReadIsAnInternalOrNotFoundAnswer(t *testing.T) {
	logs := captureLog(t)
	r := newRig(approves())
	r.pay(noBody)
	r.store.getOrderErr = errors.New("connection refused")
	want(t, r.pay(noBody), Internal, "Internal database verification step failed")
	r.store.getOrderErr = fmt.Errorf("%w: o1", ports.ErrOrderNotFound)
	want(t, r.pay(noBody), NotFound, "Order o1 could not be found")
	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("the cause was not logged:\n%s", logs)
	}
}

func TestADeclineOnAClosedOrderIsReplayedWithoutRetry(t *testing.T) {
	r := newRig(approves())
	r.existing(domain.PaymentDeclined, domain.OrderCancelled, time.Hour, domain.MaxPaymentAttempts)
	r.store.payments[0].Reason = "Insufficient funds."
	out := r.pay(noBody)
	if !reflect.DeepEqual(out, Outcome{Kind: Declined, Message: "Insufficient funds."}) {
		t.Errorf("outcome = %+v", out)
	}
	if len(r.gw.calls) != 0 {
		t.Errorf("charges = %d, want none", len(r.gw.calls))
	}
}

func TestADeclineOnAnOpenOrderStartsANewAttemptWithItsOwnKey(t *testing.T) {
	r := newRig(approves())
	r.existing(domain.PaymentDeclined, domain.OrderCreated, time.Hour, 1)
	if out := r.pay(noBody); out.Kind != Paid {
		t.Fatalf("outcome = %+v", out)
	}
	p, _ := r.store.latest()
	if p.Attempt != 2 || p.ID == "pay-1" || r.gw.calls[0].IdempotencyKey != p.ID {
		t.Errorf("payment = %+v, charge = %+v, want a new attempt under a new key", p, r.gw.calls[0])
	}
}

func TestAnAbandonedAttemptStartsANewOneUnlessTheOrderIsClosed(t *testing.T) {
	open := newRig(approves())
	open.existing(domain.PaymentAbandoned, domain.OrderCreated, time.Hour, 1)
	if out := open.pay(noBody); out.Kind != Paid {
		t.Errorf("open order: %+v", out)
	}
	closed := newRig(approves())
	closed.existing(domain.PaymentAbandoned, domain.OrderCancelled, time.Hour, domain.MaxPaymentAttempts)
	want(t, closed.pay(noBody), Conflict, "cannot be paid from state cancelled")
	if len(closed.gw.calls) != 0 {
		t.Errorf("charges = %d, want none", len(closed.gw.calls))
	}
}

func TestAPaymentUnderReviewRefusesAnotherAttempt(t *testing.T) {
	r := newRig(approves())
	r.existing(domain.PaymentNeedsReview, domain.OrderPaymentPending, 22*time.Hour, 1)
	want(t, r.pay(noBody), Conflict, "being checked")
	if len(r.gw.calls) != 0 {
		t.Errorf("charges = %d, want none", len(r.gw.calls))
	}
}

func TestAPaymentWithAnUnknownStatusIsAnInternalError(t *testing.T) {
	logs := captureLog(t)
	r := newRig(approves())
	r.existing("mystery", domain.OrderPaymentPending, time.Hour, 1)
	want(t, r.pay(noBody), Internal, "Failed to read the payment")
	if !strings.Contains(logs.String(), "unknown status") {
		t.Errorf("not logged:\n%s", logs)
	}
	// replaying such a payment is refused the same way
	if out := r.svc.replay(context.Background(), domain.Payment{ID: "p", OrderID: "o1", Status: "mystery"}); out.Kind != Internal {
		t.Errorf("replay = %+v", out)
	}
}

func TestAFailureReadingThePaymentIsAnInternalError(t *testing.T) {
	logs := captureLog(t)
	r := newRig(approves())
	r.store.getPaymentErr = errors.New("connection refused")
	want(t, r.pay(noBody), Internal, "Failed to look up the payment")
	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("not logged:\n%s", logs)
	}
}

// ---------------------------------------------------------------------------
// a payment that is still processing
// ---------------------------------------------------------------------------

func TestARetryWhileSomeoneIsWorkingOnThePaymentIsToldToWait(t *testing.T) {
	r := newRig(approves())
	r.existing(domain.PaymentProcessing, domain.OrderPaymentPending, time.Minute, 1)
	r.store.payments[0].LeaseExpiresAt = time.Now().Add(time.Hour)
	want(t, r.pay(noBody), Conflict, "still being processed")
	if len(r.gw.calls) != 0 {
		t.Errorf("charges = %d, want none", len(r.gw.calls))
	}
}

func TestARetryAfterTheLeaseRanOutTakesOverAndAsksTheProviderWithTheSameKey(t *testing.T) {
	r := newRig(approves())
	r.existing(domain.PaymentProcessing, domain.OrderPaymentPending, 10*time.Minute, 1)
	out := r.pay(withMethod("pm_x"))
	if out.Kind != Paid || len(r.gw.calls) != 1 || r.gw.calls[0].IdempotencyKey != "pay-1" || r.gw.calls[0].AmountCents != 1000 {
		t.Errorf("outcome = %+v, charges = %+v, want the original key and amount", out, r.gw.calls)
	}
	p, _ := r.store.latest()
	if p.LeaseExpiresAt.Sub(time.Now()) < Lease-5*time.Second {
		t.Errorf("lease = %v from now, want about %v", time.Until(p.LeaseExpiresAt), Lease)
	}
}

func TestARetryThatLosesTheTakeoverReadsWhatBecameOfThePayment(t *testing.T) {
	r := newRig(approves())
	r.existing(domain.PaymentProcessing, domain.OrderPaymentPending, 10*time.Minute, 1)
	r.store.payments[0].LeaseExpiresAt = time.Now().Add(time.Hour) // someone else took it
	want(t, r.pay(noBody), Conflict, "still being processed")

	gone := newRig(approves())
	gone.existing(domain.PaymentProcessing, domain.OrderPaymentPending, 10*time.Minute, 1)
	gone.store.getPaymentErr = nil
	gone.svc.DB = &flippingStore{store: gone.store}
	if out := gone.pay(noBody); out.Kind != Paid {
		t.Errorf("a payment answered while we looked: %+v, want its success replayed", out)
	}
}

// flippingStore answers the takeover with "no" and settles the payment meanwhile.
type flippingStore struct{ *store }

func (f *flippingStore) TakeOverPayment(ctx context.Context, id string, lease time.Duration) (domain.Payment, bool, error) {
	p, _ := f.latest()
	p.Status = domain.PaymentAuthorized
	f.set(p)
	f.order.State = domain.OrderPaid
	return domain.Payment{}, false, nil
}

func TestAFailureTakingTheLeaseIsAnInternalError(t *testing.T) {
	logs := captureLog(t)
	r := newRig(approves())
	r.existing(domain.PaymentProcessing, domain.OrderPaymentPending, 10*time.Minute, 1)
	r.store.takeErr = errors.New("connection refused")
	want(t, r.pay(noBody), Internal, "Failed to read the payment")
	if len(r.gw.calls) != 0 || !strings.Contains(logs.String(), "taking over payment") {
		t.Errorf("charges = %d, log:\n%s", len(r.gw.calls), logs)
	}
}

func TestARetryRefusedBeforeTheTakeoverDoesNotHoldTheLease(t *testing.T) {
	g := approves()
	g.needsMethod = true
	r := newRig(g)
	r.existing(domain.PaymentProcessing, domain.OrderPaymentPending, 10*time.Minute, 1)
	before := r.store.payments[0].LeaseExpiresAt
	want(t, r.pay(noBody), Unprocessable, "paymentMethodId is required")
	if !r.store.payments[0].LeaseExpiresAt.Equal(before) {
		t.Error("a refused retry took the lease")
	}

	bad := newRig(approves())
	bad.existing(domain.PaymentProcessing, domain.OrderPaymentPending, 10*time.Minute, 1)
	bad.store.order.PaymentType = "bitcoin"
	want(t, bad.pay(noBody), Internal, "Payment is not available")
}

// ---------------------------------------------------------------------------
// a payment too old to send again
// ---------------------------------------------------------------------------

func TestAPaymentTooOldForTheProviderToRememberIsNeverSentAgain(t *testing.T) {
	r := newRig(approves())
	r.existing(domain.PaymentProcessing, domain.OrderPaymentPending, TakeoverCutoff+time.Hour, 1)
	want(t, r.pay(noBody), Conflict, "being checked")
	if len(r.gw.calls) != 0 {
		t.Fatalf("charges = %d: a key that old may be forgotten, and a send would charge twice", len(r.gw.calls))
	}
	p, _ := r.store.latest()
	if p.Status != domain.PaymentNeedsReview || !strings.Contains(p.Reason, "20h") {
		t.Errorf("payment = %+v", p)
	}
	if len(r.alerts.kinds) != 1 || r.alerts.kinds[0] != domain.AlertPaymentNeedsReview+"/pay-1/o1" || !strings.Contains(r.alerts.texts[0], "not sent again") {
		t.Errorf("alerts = %v %v", r.alerts.kinds, r.alerts.texts)
	}
	r.pay(noBody) // asking again alerts nobody a second time
	if len(r.alerts.kinds) != 1 {
		t.Errorf("alerts = %v, want still one", r.alerts.kinds)
	}
}

func TestAPaymentJustInsideTheCutoffIsStillSentWithItsKey(t *testing.T) {
	r := newRig(approves())
	r.existing(domain.PaymentProcessing, domain.OrderPaymentPending, TakeoverCutoff-time.Hour, 1)
	if out := r.pay(noBody); out.Kind != Paid || len(r.gw.calls) != 1 || len(r.alerts.kinds) != 0 {
		t.Errorf("outcome = %+v, charges = %d, alerts = %v", out, len(r.gw.calls), r.alerts.kinds)
	}
}

func TestAFailureHandingAnOldPaymentToAPersonIsAnInternalError(t *testing.T) {
	logs := captureLog(t)
	r := newRig(approves())
	r.existing(domain.PaymentProcessing, domain.OrderPaymentPending, TakeoverCutoff+time.Hour, 1)
	r.store.markErr = errors.New("connection refused")
	want(t, r.pay(noBody), Internal, "Failed to read the payment")
	if len(r.gw.calls) != 0 || !strings.Contains(logs.String(), "marking payment pay-1 for review") {
		t.Errorf("charges = %d, log:\n%s", len(r.gw.calls), logs)
	}
}

// ---------------------------------------------------------------------------
// claiming
// ---------------------------------------------------------------------------

func TestAFailureStartingThePaymentIsAnswerAccordingToItsCause(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		kind Kind
		text string
	}{
		{"a lost race", fmt.Errorf("%w: o1", ports.ErrStateConflict), Conflict, "changed while this request was running"},
		{"the order vanished", fmt.Errorf("%w: o1", ports.ErrOrderNotFound), NotFound, "Order o1 could not be found"},
		{"the database is down", errors.New("connection refused"), Internal, "Failed to start payment"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(approves())
			r.store.startErr = c.err
			want(t, r.pay(noBody), c.kind, c.text)
			if len(r.gw.calls) != 0 {
				t.Errorf("charges = %d, want none", len(r.gw.calls))
			}
		})
	}
}

// Another request claimed the payment between our lookup and our claim.
func TestARequestThatLosesTheClaimIsARetryAfterAll(t *testing.T) {
	r := newRig(approves())
	other := &claimedFirst{store: r.store}
	r.svc.DB = other
	want(t, r.pay(noBody), Conflict, "still being processed")
	if len(r.gw.calls) != 0 {
		t.Errorf("charges = %d: the loser must never reach the provider", len(r.gw.calls))
	}
}

// claimedFirst plays a request that claimed the payment just before ours: our
// lookup found nothing, then our claim finds theirs.
type claimedFirst struct{ *store }

func (c *claimedFirst) StartPayment(_ context.Context, p domain.Payment, lease time.Duration, from, to string) (domain.Payment, bool, error) {
	theirs := domain.Payment{ID: "theirs", OrderID: p.OrderID, Status: domain.PaymentProcessing, Attempt: 1, LeaseExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now()}
	c.payments = append(c.payments, theirs)
	return theirs, false, nil
}

// ---------------------------------------------------------------------------
// the provider's answer
// ---------------------------------------------------------------------------

func TestAProviderErrorLeavesThePaymentProcessingForARetry(t *testing.T) {
	logs := captureLog(t)
	g := &gateway{err: errors.New("stripe: HTTP 503")}
	r := newRig(g)
	want(t, r.pay(noBody), Internal, "Payment authorisation failed")
	p, _ := r.store.latest()
	if p.Status != domain.PaymentProcessing || r.store.order.State != domain.OrderPaymentPending {
		t.Errorf("payment = %s, order = %s: nothing may be decided on an error", p.Status, r.store.order.State)
	}
	if !strings.Contains(logs.String(), "HTTP 503") {
		t.Errorf("not logged:\n%s", logs)
	}
}

func TestAProviderThatBreaksItsContractIsNotTakenForADecline(t *testing.T) {
	logs := captureLog(t)
	for name, g := range map[string]*gateway{
		"no result and no error":   {},
		"a status that is neither": {result: &domain.PaymentResult{Status: domain.PaymentProcessing}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(g)
			want(t, r.pay(noBody), Internal, "Payment authorisation failed")
			if p, _ := r.store.latest(); p.Status != domain.PaymentProcessing {
				t.Errorf("payment = %s, want it left processing", p.Status)
			}
		})
	}
	if !strings.Contains(logs.String(), "no result and no error") || !strings.Contains(logs.String(), "neither authorized nor declined") {
		t.Errorf("not logged:\n%s", logs)
	}
}

// ---------------------------------------------------------------------------
// recording the answer
// ---------------------------------------------------------------------------

// Another request recorded the outcome first (a retry that took the payment over).
func TestARequestWhoseAnswerWasRecordedByAnotherIsToldTheirs(t *testing.T) {
	r := newRig(approves())
	r.store.before = func(s *store) {
		p, _ := s.latest()
		p.Status = domain.PaymentDeclined
		p.Reason = "Your card was declined."
		s.set(p)
	}
	out := r.pay(noBody)
	if !reflect.DeepEqual(out, Outcome{Kind: Declined, Message: "Your card was declined."}) {
		t.Errorf("outcome = %+v, want the other request's decline", out)
	}

	broken := newRig(approves())
	broken.store.before = func(s *store) {
		p, _ := s.latest()
		p.Status = domain.PaymentAuthorized
		s.set(p)
		s.getPaymentErr = errors.New("connection refused")
	}
	want(t, broken.pay(noBody), Internal, "Failed to read the payment")
}

func TestAnAnswerForAnOrderThatMovedOnIsKeptAndTheOrderLeftAlone(t *testing.T) {
	logs := captureLog(t)
	for _, c := range []struct {
		name       string
		orderState domain.OrderState
		wantText   string
	}{
		{"cancelled", domain.OrderCancelled, "was cancelled while the payment was being processed; the payment has been recorded and will be refunded"},
		{"otherwise", domain.OrderPreparing, "changed while the payment was being processed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(approves())
			r.store.before = func(s *store) { s.order.State = c.orderState }
			want(t, r.pay(noBody), Conflict, c.wantText)
			p, _ := r.store.latest()
			if p.Status != domain.PaymentAuthorized || p.ProviderPaymentID != "pi_1" {
				t.Errorf("payment = %+v, want the charge kept: the refund job looks for it", p)
			}
			if r.store.order.State != c.orderState {
				t.Errorf("order = %s, want it left alone", r.store.order.State)
			}
		})
	}
	if !strings.Contains(logs.String(), "REFUND DUE") {
		t.Errorf("a charge on an order that moved on must say a refund is due:\n%s", logs)
	}

	// the order cannot even be read to word the answer: still kept, still a conflict
	unread := newRig(approves())
	unread.store.before = func(s *store) {
		s.order.State = domain.OrderCancelled
		s.getOrderErr = errors.New("connection refused")
	}
	want(t, unread.pay(noBody), Conflict, "changed while the payment was being processed")
}

func TestADeclineForAnOrderThatMovedOnIsKeptAndNoRefundIsDue(t *testing.T) {
	logs := captureLog(t)
	r := newRig(declines("no funds"))
	r.store.before = func(s *store) { s.order.State = domain.OrderCancelled }
	want(t, r.pay(noBody), Conflict, "changed while this request was running")
	if p, _ := r.store.latest(); p.Status != domain.PaymentDeclined {
		t.Errorf("payment = %s, want the decline kept", p.Status)
	}
	if strings.Contains(logs.String(), "REFUND DUE") {
		t.Errorf("a decline moved no money:\n%s", logs)
	}
}

func TestAnAnswerThatCannotBeKeptRaisesAnAlertAndIsAnInternalError(t *testing.T) {
	r := newRig(approves())
	r.store.before = func(s *store) {
		s.order.State = domain.OrderCancelled
		s.completeErr = errors.New("connection refused")
	}
	want(t, r.pay(noBody), Internal, "Failed to record the payment")
	if len(r.alerts.kinds) != 1 || !strings.HasPrefix(r.alerts.kinds[0], domain.AlertUnrecordedCharge+"/") {
		t.Fatalf("alerts = %v, want one unrecorded_charge", r.alerts.kinds)
	}
	if !strings.Contains(r.alerts.texts[0], "could not be recorded") || !strings.Contains(r.alerts.texts[0], "connection refused") {
		t.Errorf("alert text = %q", r.alerts.texts[0])
	}
}

func TestAnAnswerAnotherWriterAlreadyKeptIsNotAProblem(t *testing.T) {
	r := newRig(approves())
	r.store.before = func(s *store) {
		s.order.State = domain.OrderCancelled
		s.completeErr = fmt.Errorf("%w: pay", ports.ErrPaymentNotProcessing)
	}
	want(t, r.pay(noBody), Conflict, "cancelled while the payment was being processed")
	if len(r.alerts.kinds) != 0 {
		t.Errorf("alerts = %v, want none", r.alerts.kinds)
	}
}

func TestAStorageFailureRecordingTheAnswerIsAnInternalError(t *testing.T) {
	logs := captureLog(t)
	r := newRig(approves())
	r.store.finishErr = errors.New("connection refused")
	want(t, r.pay(noBody), Internal, "Failed to record the payment")
	if !strings.Contains(logs.String(), "database error recording payment") {
		t.Errorf("not logged:\n%s", logs)
	}
}

func TestTheOutcomeOfAFailureNamesTheOrderWhenItVanishedMidWrite(t *testing.T) {
	r := newRig(approves())
	r.store.finishErr = fmt.Errorf("%w: o1", ports.ErrOrderNotFound)
	want(t, r.pay(noBody), NotFound, "Order o1 could not be found")
}

// The reconciler gave up on the payment while a retry was deciding to take it over.
func TestARetryThatLosesTheTakeoverToAnAbandonmentIsToldTheAttemptEndedWithoutACharge(t *testing.T) {
	r := newRig(approves())
	r.existing(domain.PaymentProcessing, domain.OrderPaymentPending, 10*time.Minute, 1)
	r.svc.DB = &abandonedMeanwhile{store: r.store}
	want(t, r.pay(noBody), Conflict, "ended without a charge")
	if len(r.gw.calls) != 0 {
		t.Errorf("charges = %d, want none", len(r.gw.calls))
	}
}

// abandonedMeanwhile answers the takeover with "no" and abandons the payment meanwhile.
type abandonedMeanwhile struct{ *store }

func (a *abandonedMeanwhile) TakeOverPayment(context.Context, string, time.Duration) (domain.Payment, bool, error) {
	p, _ := a.latest()
	p.Status = domain.PaymentAbandoned
	a.set(p)
	a.order.State = domain.OrderCreated
	return domain.Payment{}, false, nil
}

// The payment is read again from the database, never the cache, and that can fail.
func TestAFailureReadingThePaymentAgainIsAnInternalError(t *testing.T) {
	logs := captureLog(t)
	r := newRig(approves())
	r.existing(domain.PaymentNeedsReview, domain.OrderPaymentPending, 22*time.Hour, 1)
	cached := &store{payments: r.store.payments, order: r.store.order} // what the lookup sees
	r.svc.Lookup = cached
	r.store.getPaymentErr = errors.New("connection refused")
	want(t, r.pay(noBody), Internal, "Failed to read the payment")
	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("not logged:\n%s", logs)
	}
}

// The two timings only make sense in a range: a lease shorter than a slow provider
// call lets a second request take over a payment that is still being processed,
// and a cutoff past the provider's memory of an idempotency key lets a retry
// charge twice.
func TestTheTimingsAreInsideTheRangeTheyNeed(t *testing.T) {
	if Lease <= 30*time.Second {
		t.Errorf("lease = %v, must outlast the 30s the Stripe client waits", Lease)
	}
	if TakeoverCutoff < time.Hour || TakeoverCutoff >= 24*time.Hour {
		t.Errorf("cutoff = %v, must be well inside the 24 hours Stripe remembers a key", TakeoverCutoff)
	}
}
