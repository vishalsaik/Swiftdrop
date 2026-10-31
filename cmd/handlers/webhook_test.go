package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/payment/gateways"
)

// ---------------------------------------------------------------------------
// The payment webhook: a provider pushes the outcome of a payment, and we record
// it with the same FinishPayment that /pay uses.
// ---------------------------------------------------------------------------

// stubParser plays an EventParser that answers what the test told it to and
// remembers what it was given.
type stubParser struct {
	mu       sync.Mutex
	event    payment.Event
	err      error
	payloads []string
}

func (p *stubParser) ParseEvent(payload []byte, _ http.Header) (payment.Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.payloads = append(p.payloads, string(payload))
	return p.event, p.err
}

func eventFor(paymentID string, status domain.PaymentStatus) *stubParser {
	e := payment.Event{ID: "evt_1", PaymentID: paymentID, ProviderPaymentID: "pi_event", Status: status}
	if status == domain.PaymentDeclined {
		e.Reason = "Your card was declined."
	}
	return &stubParser{event: e}
}

func webhookServer(db ports.Store, parser payment.EventParser) *Server {
	s := NewServer(db, testPayRouter())
	s.Webhook = parser
	return s
}

func postWebhook(s *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return serve(s, req)
}

// ack returns the "status" of a webhook answer.
func ack(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("answer is not JSON: %v (%s)", err, rec.Body)
	}
	return body["status"]
}

func wantAck(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	wantStatus(t, rec, http.StatusOK)
	if got := ack(t, rec); got != want {
		t.Errorf("answer = %q, want %q", got, want)
	}
}

// ---- recording an outcome ----------------------------------------------------

func TestAWebhookWithoutAParserIsNotFound(t *testing.T) {
	s := NewServer(newFakeDB(), testPayRouter())
	rec := postWebhook(s, `{}`)
	wantStatus(t, rec, http.StatusNotFound)
	wantErrorContains(t, rec, "not enabled")
}

func TestASucceededEventAuthorizesThePaymentAndPaysTheOrder(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)

	rec := postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized)), `{"raw":"body"}`)
	wantAck(t, rec, "recorded")
	p := db.payments["o1"]
	if p.Status != domain.PaymentAuthorized || p.ProviderPaymentID != "pi_event" {
		t.Errorf("payment = %+v, want authorized with the provider id", p)
	}
	if got := db.orders["o1"].State; got != domain.OrderPaid {
		t.Errorf("order state = %s, want paid", got)
	}
	wantWrites(t, db, domain.OrderPaid)
}

func TestADeclinedEventDeclinesThePaymentAndReopensTheOrder(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)

	rec := postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentDeclined)), `{}`)
	wantAck(t, rec, "recorded")
	p := db.payments["o1"]
	if p.Status != domain.PaymentDeclined || p.Reason != "Your card was declined." || p.ProviderPaymentID != "pi_event" {
		t.Errorf("payment = %+v, want declined with the reason", p)
	}
	if got := db.orders["o1"].State; got != domain.OrderCreated {
		t.Errorf("order state = %s, want created (open for another attempt)", got)
	}
}

// The last allowed attempt cancels the order, as it does through /pay.
func TestADeclinedEventForTheLastAttemptCancelsTheOrder(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-3", 1000, time.Minute)
	p := db.payments["o1"]
	p.Attempt = domain.MaxPaymentAttempts
	db.payments["o1"] = p

	wantAck(t, postWebhook(webhookServer(db, eventFor("pay-3", domain.PaymentDeclined)), `{}`), "recorded")
	if got := db.orders["o1"].State; got != domain.OrderCancelled {
		t.Errorf("order state = %s, want cancelled", got)
	}
}

// The parser gets the request body exactly as it was sent: a signature covers
// those bytes, so they must not be decoded and re-encoded on the way.
func TestTheParserIsGivenTheRawBody(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	parser := eventFor("pay-1", domain.PaymentAuthorized)

	raw := "{ \"id\" :\t\"evt_1\",\n  \"type\":\"x\" }"
	postWebhook(webhookServer(db, parser), raw)
	if len(parser.payloads) != 1 || parser.payloads[0] != raw {
		t.Errorf("parser saw %q, want %q", parser.payloads, raw)
	}
}

// ---- deliveries that change nothing -------------------------------------------

func TestADuplicateDeliveryChangesNothing(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	s := webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized))

	wantAck(t, postWebhook(s, `{}`), "recorded")
	wantAck(t, postWebhook(s, `{}`), "already recorded")
	wantAck(t, postWebhook(s, `{}`), "already recorded")
	wantWrites(t, db, domain.OrderPaid)
}

func TestAnEventForAPaymentThatPayOrderAlreadyRecordedChangesNothing(t *testing.T) {
	db := newFakeDB()
	g := approving()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusOK)
	id := db.payments["o1"].ID
	before := db.payments["o1"]

	wantAck(t, postWebhook(webhookServer(db, eventFor(id, domain.PaymentAuthorized)), `{}`), "already recorded")
	if db.payments["o1"] != before {
		t.Errorf("payment changed from %+v to %+v", before, db.payments["o1"])
	}
	wantWrites(t, db, domain.OrderPaymentPending, domain.OrderPaid) // only /pay's own two
}

// The provider and our record disagree about money. That needs a person.
func TestAnEventThatContradictsTheRecordedOutcomeIsFlaggedAndChangesNothing(t *testing.T) {
	logs := captureLog(t)
	db := newFakeDB()
	g := declining("no funds")
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusPaymentRequired)
	id := db.payments["o1"].ID

	wantAck(t, postWebhook(webhookServer(db, eventFor(id, domain.PaymentAuthorized)), `{}`), "already recorded")
	if got := db.payments["o1"].Status; got != domain.PaymentDeclined {
		t.Errorf("payment status = %s, want it left declined", got)
	}
	if !strings.Contains(logs.String(), "NEEDS REVIEW") {
		t.Errorf("the contradiction was not flagged:\n%s", logs)
	}
}

func TestAnEventForAPaymentWeDoNotKnowIsAcknowledged(t *testing.T) {
	db := newFakeDB()
	db.seed("o1", domain.OrderPaymentPending)

	wantAck(t, postWebhook(webhookServer(db, eventFor("no-such-payment", domain.PaymentAuthorized)), `{}`), "unknown payment")
	wantWrites(t, db)
}

func TestAnIgnoredEventIsAcknowledgedAndTouchesNothing(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	parser := &stubParser{err: fmt.Errorf("%w: charge.succeeded", payment.ErrEventIgnored)}

	wantAck(t, postWebhook(webhookServer(db, parser), `{}`), "ignored")
	if db.payGetCalls != 0 {
		t.Errorf("the store was read %d times for an event we ignore", db.payGetCalls)
	}
	wantWrites(t, db)
}

// ---- requests that are not genuine ---------------------------------------------

func TestAnEventThatFailsVerificationIsRejectedAndTouchesNothing(t *testing.T) {
	logs := captureLog(t)
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	parser := &stubParser{err: fmt.Errorf("%w: signature does not match", payment.ErrInvalidEvent)}

	rec := postWebhook(webhookServer(db, parser), `{}`)
	wantStatus(t, rec, http.StatusBadRequest)
	if strings.Contains(rec.Body.String(), "signature does not match") {
		t.Errorf("the verification detail leaked to the caller: %s", rec.Body)
	}
	if !strings.Contains(logs.String(), "signature does not match") {
		t.Errorf("the cause is not in the log:\n%s", logs)
	}
	if db.payGetCalls != 0 || len(db.stateWrites) != 0 {
		t.Errorf("a rejected event touched the store (reads %d, writes %v)", db.payGetCalls, db.stateWrites)
	}
}

func TestAnUnknownParserErrorIsAlsoRejected(t *testing.T) {
	parser := &stubParser{err: errors.New("something else")}
	wantStatus(t, postWebhook(webhookServer(newFakeDB(), parser), `{}`), http.StatusBadRequest)
}

func TestAWebhookBodyThatIsTooLargeIs413(t *testing.T) {
	parser := &stubParser{}
	rec := postWebhook(webhookServer(newFakeDB(), parser), strings.Repeat("x", maxBodyBytes+1))
	wantStatus(t, rec, http.StatusRequestEntityTooLarge)
	if len(parser.payloads) != 0 {
		t.Error("an oversized body reached the parser")
	}
}

func TestAWebhookBodyThatCannotBeReadIs400(t *testing.T) {
	parser := &stubParser{}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", iotest.ErrReader(errors.New("connection reset")))
	req.Header.Set("Content-Type", "application/json")
	rec := serve(webhookServer(newFakeDB(), parser), req)
	wantStatus(t, rec, http.StatusBadRequest)
	if len(parser.payloads) != 0 {
		t.Error("an unreadable body reached the parser")
	}
}

// ---- failures we want the provider to retry -------------------------------------

func TestAStoreFailureReadingThePaymentIsA500SoTheProviderRetries(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	db.payGetErr = errors.New("connection refused")

	rec := postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized)), `{}`)
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked to the caller: %s", rec.Body)
	}
}

func TestAStoreFailureRecordingTheOutcomeIsA500AndLeavesThePaymentProcessing(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	db.updateErr = errors.New("connection refused")

	rec := postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized)), `{}`)
	wantStatus(t, rec, http.StatusInternalServerError)
	if got := db.payments["o1"].Status; got != domain.PaymentProcessing {
		t.Errorf("payment status = %s, want it left processing for the retry", got)
	}

	// The provider retries, the store is back, and this time it is recorded.
	db.updateErr = nil
	wantAck(t, postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized)), `{}`), "recorded")
}

// ---- the other writer won, or the order moved on ---------------------------------

func TestLosingTheRaceToAnotherWriterIsAnAcknowledgement(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	db.beforeWrite = func(f *fakeDB, orderID string) {
		p := f.payments[orderID]
		p.Status, p.ProviderPaymentID = domain.PaymentAuthorized, "pi_theirs"
		f.payments[orderID] = p
	}

	wantAck(t, postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized)), `{}`), "already recorded")
	if got := db.payments["o1"].ProviderPaymentID; got != "pi_theirs" {
		t.Errorf("provider id = %q, want the other writer's left alone", got)
	}
}

// failingFinish makes FinishPayment fail the way the test says.
type failingFinish struct {
	*fakeDB
	err error
}

func (f failingFinish) FinishPayment(context.Context, string, domain.PaymentStatus, string, string, string, string) (domain.Order, error) {
	return domain.Order{}, f.err
}

func TestAPaymentThatDisappearsBeforeItIsRecordedIsAnAcknowledgement(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	store := failingFinish{db, fmt.Errorf("%w: pay-1", ports.ErrPaymentNotFound)}

	wantAck(t, postWebhook(webhookServer(store, eventFor("pay-1", domain.PaymentAuthorized)), `{}`), "already recorded")
}

// Money moved but the order was cancelled meanwhile. The order stays cancelled,
// the charge is kept on record (authorized, with the provider's id) so the
// refund job can find it, and the delivery is acknowledged.
func TestAnAuthorizedPaymentForAnOrderThatMovedOnIsKeptForARefund(t *testing.T) {
	logs := captureLog(t)
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	o := db.orders["o1"]
	o.State = domain.OrderCancelled
	db.orders["o1"] = o

	wantAck(t, postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized)), `{}`), "order moved on")
	if p := db.payments["o1"]; p.Status != domain.PaymentAuthorized || p.ProviderPaymentID != "pi_event" {
		t.Errorf("payment = %+v, want authorized with the provider's id kept", p)
	}
	if got := db.orders["o1"].State; got != domain.OrderCancelled {
		t.Errorf("order state = %s, want the cancel left alone", got)
	}
	wantWrites(t, db)
	if !strings.Contains(logs.String(), "REFUND DUE") {
		t.Errorf("the log does not say a refund is due:\n%s", logs)
	}
	if strings.Contains(logs.String(), "NEEDS REVIEW") {
		t.Errorf("a recorded charge must not ask for a human:\n%s", logs)
	}

	// Stripe delivers it again: nothing changes.
	wantAck(t, postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized)), `{}`), "already recorded")
}

// No money moved: the decline is recorded and nothing is due.
func TestADeclinedPaymentForAnOrderThatMovedOnIsRecordedWithoutARefund(t *testing.T) {
	logs := captureLog(t)
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	o := db.orders["o1"]
	o.State = domain.OrderCancelled
	db.orders["o1"] = o

	wantAck(t, postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentDeclined)), `{}`), "order moved on")
	if p := db.payments["o1"]; p.Status != domain.PaymentDeclined || p.Reason != "Your card was declined." {
		t.Errorf("payment = %+v, want the decline recorded", p)
	}
	if strings.Contains(logs.String(), "REFUND DUE") || strings.Contains(logs.String(), "NEEDS REVIEW") {
		t.Errorf("a decline raised a refund or a review:\n%s", logs)
	}
}

// If the charge cannot be written down the delivery is NOT acknowledged, so
// Stripe sends it again, and a person is told in the meantime.
func TestAChargeForAnOrderThatMovedOnThatCannotBeRecordedIsRetriedByTheProvider(t *testing.T) {
	logs := captureLog(t)
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	o := db.orders["o1"]
	o.State = domain.OrderCancelled
	db.orders["o1"] = o
	db.completeErr = errors.New("connection refused")

	rec := postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized)), `{}`)
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked to the caller: %s", rec.Body)
	}
	if !strings.Contains(logs.String(), "NEEDS REVIEW") {
		t.Errorf("the unrecorded charge was not flagged:\n%s", logs)
	}

	// The store is back; the redelivery is recorded.
	db.completeErr = nil
	wantAck(t, postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized)), `{}`), "order moved on")
	if got := db.payments["o1"].Status; got != domain.PaymentAuthorized {
		t.Errorf("payment status = %s, want the charge kept on the redelivery", got)
	}
}

// ---- the story this exists for ---------------------------------------------------

// The provider takes the charge but the reply is lost: /pay fails and the
// payment is left processing. The webhook then delivers the outcome, and a
// retry of /pay replays it without charging a second time.
func TestALostReplyIsRecoveredByTheWebhookAndTheRetryReplaysIt(t *testing.T) {
	db := newFakeDB()
	lost := &recordingGateway{err: errors.New("connection reset by peer")}
	seedPayable(db, "o1", domain.PaymentCard, 1000)

	wantStatus(t, payWith(t, db, routerOf(lost, lost), "o1"), http.StatusInternalServerError)
	if got := db.payments["o1"].Status; got != domain.PaymentProcessing {
		t.Fatalf("payment status = %s, want processing after the lost reply", got)
	}
	id := db.payments["o1"].ID
	if len(lost.calls) != 1 || lost.calls[0].IdempotencyKey != id {
		t.Fatalf("the charge was sent with %+v, want the payment id as its key", lost.calls)
	}

	wantAck(t, postWebhook(webhookServer(db, eventFor(id, domain.PaymentAuthorized)), `{}`), "recorded")
	if got := db.orders["o1"].State; got != domain.OrderPaid {
		t.Fatalf("order state = %s, want paid", got)
	}

	retry := payWith(t, db, routerOf(lost, lost), "o1")
	wantStatus(t, retry, http.StatusOK)
	if got := orderFrom(t, retry).State; got != domain.OrderPaid {
		t.Errorf("the retry shows state %s, want paid", got)
	}
	if len(lost.calls) != 1 {
		t.Errorf("the provider was called %d times, want the retry to replay without charging", len(lost.calls))
	}
}

func TestALostReplyThenADeclineWebhookLeavesTheOrderOpenForANewAttempt(t *testing.T) {
	db := newFakeDB()
	lost := &recordingGateway{err: errors.New("connection reset by peer")}
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	wantStatus(t, payWith(t, db, routerOf(lost, lost), "o1"), http.StatusInternalServerError)
	id := db.payments["o1"].ID

	wantAck(t, postWebhook(webhookServer(db, eventFor(id, domain.PaymentDeclined)), `{}`), "recorded")
	if got := db.orders["o1"].State; got != domain.OrderCreated {
		t.Fatalf("order state = %s, want created", got)
	}

	// The customer tries another card: a NEW payment, with its own key.
	ok := approving()
	retry := payWith(t, db, routerOf(ok, ok), "o1")
	wantStatus(t, retry, http.StatusOK)
	if p := db.payments["o1"]; p.ID == id || p.Attempt != 2 || p.Status != domain.PaymentAuthorized {
		t.Errorf("payment = %+v, want a new authorized payment on attempt 2", p)
	}
	if len(lost.calls) != 1 || len(ok.calls) != 1 || ok.calls[0].IdempotencyKey == id {
		t.Errorf("lost calls %d, ok calls %+v, want one each and a different key", len(lost.calls), ok.calls)
	}
}

// ---- concurrency -----------------------------------------------------------------

func TestFiftySimultaneousDeliveriesRecordTheOutcomeOnce(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	s := webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized))

	answers := make([]string, racers)
	fireTogether(racers, func(i int) {
		rec := postWebhook(s, `{}`)
		if rec.Code != http.StatusOK {
			answers[i] = fmt.Sprintf("HTTP %d", rec.Code)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(rec.Body).Decode(&body)
		answers[i] = body["status"]
	})

	recorded, already := 0, 0
	for _, a := range answers {
		switch a {
		case "recorded":
			recorded++
		case "already recorded":
			already++
		default:
			t.Errorf("unexpected answer %q", a)
		}
	}
	if recorded != 1 || already != racers-1 {
		t.Errorf("recorded = %d, already recorded = %d, want 1 and %d", recorded, already, racers-1)
	}
	wantWrites(t, db, domain.OrderPaid)
}

// Contradictory events for one payment cannot leave it half one way and half
// the other: the payment and its order end up telling the same story.
func TestContradictoryEventsLeaveThePaymentAndOrderConsistent(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	succeeded := webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized))
	failed := webhookServer(db, eventFor("pay-1", domain.PaymentDeclined))

	fireTogether(racers, func(i int) {
		if i%2 == 0 {
			postWebhook(succeeded, `{}`)
		} else {
			postWebhook(failed, `{}`)
		}
	})

	p, o := db.payments["o1"], db.orders["o1"]
	switch {
	case p.Status == domain.PaymentAuthorized && o.State == domain.OrderPaid:
	case p.Status == domain.PaymentDeclined && o.State == domain.OrderCreated:
	default:
		t.Errorf("payment %s with order %s is not a consistent outcome", p.Status, o.State)
	}
	if len(db.stateWrites) != 1 {
		t.Errorf("state writes = %v, want exactly one", db.stateWrites)
	}
}

// ---- through the real Stripe parser ----------------------------------------------

func signedStripeRequest(secret, body string, at time.Time) *http.Request {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", at.Unix(), body)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
	// Stripe sends a charset parameter; the JSON check must accept it.
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", at.Unix(), hex.EncodeToString(mac.Sum(nil))))
	return req
}

const stripeSecret = "whsec_integration"

func stripeBody(typ, key string) string {
	return fmt.Sprintf(`{"id":"evt_9","type":%q,"data":{"object":{"id":"pi_9"}},"request":{"id":"req_9","idempotency_key":%q}}`, typ, key)
}

func TestARealSignedStripeEventIsRecordedEndToEnd(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	s := webhookServer(db, gateways.NewStripeWebhook(stripeSecret))

	rec := serve(s, signedStripeRequest(stripeSecret, stripeBody("payment_intent.succeeded", "pay-1"), time.Now()))
	wantAck(t, rec, "recorded")
	if p := db.payments["o1"]; p.Status != domain.PaymentAuthorized || p.ProviderPaymentID != "pi_9" {
		t.Errorf("payment = %+v, want authorized with pi_9", p)
	}
	if got := db.orders["o1"].State; got != domain.OrderPaid {
		t.Errorf("order state = %s, want paid", got)
	}
}

func TestAForgedStripeEventChangesNothing(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	s := webhookServer(db, gateways.NewStripeWebhook(stripeSecret))

	forged := serve(s, signedStripeRequest("whsec_attacker", stripeBody("payment_intent.succeeded", "pay-1"), time.Now()))
	wantStatus(t, forged, http.StatusBadRequest)

	replayed := serve(s, signedStripeRequest(stripeSecret, stripeBody("payment_intent.succeeded", "pay-1"), time.Now().Add(-time.Hour)))
	wantStatus(t, replayed, http.StatusBadRequest)

	unsigned := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(stripeBody("payment_intent.succeeded", "pay-1")))
	unsigned.Header.Set("Content-Type", "application/json")
	wantStatus(t, serve(s, unsigned), http.StatusBadRequest)

	if got := db.payments["o1"].Status; got != domain.PaymentProcessing {
		t.Errorf("payment status = %s, want it untouched", got)
	}
	wantWrites(t, db)
}

func TestAStripeEventNoRequestOfOursCausedIsAcknowledged(t *testing.T) {
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	s := webhookServer(db, gateways.NewStripeWebhook(stripeSecret))
	body := `{"id":"evt_9","type":"payment_intent.succeeded","data":{"object":{"id":"pi_9"}},"request":{"id":null,"idempotency_key":null}}`

	wantAck(t, serve(s, signedStripeRequest(stripeSecret, body, time.Now())), "ignored")
	wantWrites(t, db)
}

// Only POST reaches the webhook.
func TestTheWebhookRouteOnlyAcceptsPost(t *testing.T) {
	s := webhookServer(newFakeDB(), &stubParser{})
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/webhooks/stripe", nil)
		if got := serve(s, req).Code; got != http.StatusMethodNotAllowed {
			t.Errorf("%s /webhooks/stripe = %d, want 405", method, got)
		}
	}
}

// Someone else recorded the same outcome between our read and our write: the
// delivery is still acknowledged, and nobody is asked to review anything.
func TestAChargeForAnOrderThatMovedOnThatSomeoneElseRecordedIsAcknowledged(t *testing.T) {
	logs := captureLog(t)
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Minute)
	o := db.orders["o1"]
	o.State = domain.OrderCancelled
	db.orders["o1"] = o
	db.completeErr = fmt.Errorf("%w: pay-1", ports.ErrPaymentNotProcessing)

	wantAck(t, postWebhook(webhookServer(db, eventFor("pay-1", domain.PaymentAuthorized)), `{}`), "order moved on")
	if strings.Contains(logs.String(), "NEEDS REVIEW") {
		t.Errorf("an outcome someone else recorded was flagged for review:\n%s", logs)
	}
}
