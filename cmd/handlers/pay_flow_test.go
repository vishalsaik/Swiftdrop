package handlers

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// Idempotent /pay: the payment record decides whether a request is a new
// payment or a retry, and the claim comes before the provider is called.
// ---------------------------------------------------------------------------

// seedProcessing puts an order in payment_pending with a processing payment
// whose lease runs for `lease` from now (negative = already expired).
func seedProcessing(db *fakeDB, orderID, paymentID string, amount int64, lease time.Duration) {
	seedPayable(db, orderID, domain.PaymentCard, amount)
	o := db.orders[orderID]
	o.State = domain.OrderPaymentPending
	db.orders[orderID] = o
	now := time.Now()
	db.payments[orderID] = domain.Payment{
		ID: paymentID, OrderID: orderID, Status: domain.PaymentProcessing, AmountCents: amount,
		LeaseExpiresAt: now.Add(lease), CreatedAt: now, UpdatedAt: now,
	}
}

func newPost(path string) *http.Request {
	return httptest.NewRequest(http.MethodPost, path, nil)
}

func serve(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Router.ServeHTTP(rec, req)
	return rec
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// ---- a new payment ----------------------------------------------------------

func TestNewPaymentIsRecordedAsAuthorized(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1198)

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusOK)
	if got := orderFrom(t, rec).State; got != domain.OrderPaid {
		t.Errorf("order state = %s, want paid", got)
	}

	p := db.payments["o1"]
	if p.Status != domain.PaymentAuthorized || p.ProviderPaymentID != "p1" || p.AmountCents != 1198 || p.Reason != "" {
		t.Errorf("payment = %+v, want authorized, provider id p1, 1198 cents", p)
	}
	if _, err := uuid.Parse(p.ID); err != nil {
		t.Errorf("payment id %q is not a uuid: %v", p.ID, err)
	}
	// the claim carries the 60 second lease; the fake stamps both times from
	// one clock reading, so the difference is exactly the lease it was given
	if got := p.LeaseExpiresAt.Sub(p.CreatedAt); got != paymentLease {
		t.Errorf("lease = %v, want %v", got, paymentLease)
	}
	wantWrites(t, db, domain.OrderPaymentPending, domain.OrderPaid)
}

func TestNewPaymentThatIsDeclinedIsRecordedWithItsReason(t *testing.T) {
	g := declining("Your card was declined.")
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusPaymentRequired)
	wantErrorContains(t, rec, "Your card was declined.")

	p := db.payments["o1"]
	if p.Status != domain.PaymentDeclined || p.Reason != "Your card was declined." {
		t.Errorf("payment = %+v, want declined with the reason", p)
	}
	if got := db.orders["o1"].State; got != domain.OrderCancelled {
		t.Errorf("order state = %s, want cancelled", got)
	}
}

// The payment's own id is what the provider is given as its idempotency key,
// so even if we crash and ask again, the provider will not charge twice.
func TestTheProviderIsGivenThePaymentIdAsItsIdempotencyKey(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1198)
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusOK)

	if len(g.calls) != 1 {
		t.Fatalf("gateway called %d times, want 1", len(g.calls))
	}
	if got, want := g.calls[0].IdempotencyKey, db.payments["o1"].ID; got == "" || got != want {
		t.Errorf("IdempotencyKey = %q, want the payment id %q", got, want)
	}
}

func TestAnIdempotencyKeyHeaderFromTheClientIsIgnored(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	s := NewServer(db, routerOf(g, g))
	req := newPost("/orders/o1/pay")
	req.Header.Set("Idempotency-Key", "client-chosen")
	rec := serve(s, req)
	wantStatus(t, rec, http.StatusOK)

	if got := g.calls[0].IdempotencyKey; got == "client-chosen" {
		t.Error("the client's header was used as the idempotency key; the server owns it")
	}
}

// ---- a retry of a finished payment ----------------------------------------

func TestRetryAfterSuccessReplaysWithoutChargingAgain(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	router := routerOf(g, g)
	wantStatus(t, payWith(t, db, router, "o1"), http.StatusOK)

	for i := range 3 {
		rec := payWith(t, db, router, "o1")
		wantStatus(t, rec, http.StatusOK)
		if got := orderFrom(t, rec).State; got != domain.OrderPaid {
			t.Errorf("retry %d: order state = %s, want paid", i, got)
		}
	}
	if len(g.calls) != 1 {
		t.Errorf("the provider was charged %d times, want exactly 1", len(g.calls))
	}
	wantWrites(t, db, domain.OrderPaymentPending, domain.OrderPaid)
}

// A replay shows the order as it is now, not a stale copy.
func TestReplayShowsTheOrderAsItIsNow(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	router := routerOf(g, g)
	wantStatus(t, payWith(t, db, router, "o1"), http.StatusOK)
	wantStatus(t, do(t, db, http.MethodPost, "/orders/o1/accept", ""), http.StatusOK) // now preparing

	rec := payWith(t, db, router, "o1")
	wantStatus(t, rec, http.StatusOK)
	if got := orderFrom(t, rec).State; got != domain.OrderPreparing {
		t.Errorf("replayed order state = %s, want the current preparing", got)
	}
}

func TestRetryAfterADeclineReplaysTheDecline(t *testing.T) {
	g := declining("insufficient funds")
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	router := routerOf(g, g)
	wantStatus(t, payWith(t, db, router, "o1"), http.StatusPaymentRequired)

	rec := payWith(t, db, router, "o1")
	wantStatus(t, rec, http.StatusPaymentRequired)
	wantErrorContains(t, rec, "insufficient funds")
	if len(g.calls) != 1 {
		t.Errorf("the provider was asked %d times, want 1", len(g.calls))
	}
}

func TestReplayOfADeclineWithNoStoredReasonUsesTheFallback(t *testing.T) {
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	db.payments["o1"] = domain.Payment{ID: "pay-1", OrderID: "o1", Status: domain.PaymentDeclined}
	g := approving()

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusPaymentRequired)
	if got := errorFrom(t, rec); got != "payment declined" {
		t.Errorf("error = %q, want the fallback", got)
	}
}

// ---- a retry of a payment that is still processing -----------------------

func TestRetryWhileTheLeaseIsLiveIsAnInProgressConflict(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-live", 1000, time.Hour)

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "still being processed")
	if len(g.calls) != 0 {
		t.Errorf("the provider was called %d times while another request held the payment", len(g.calls))
	}
	if got := db.payments["o1"].ID; got != "pay-live" {
		t.Errorf("payment id = %s, want it left alone", got)
	}
}

// The owner crashed. The retry takes the payment over and asks the provider
// again with the SAME id and the amount stored at the claim, never a new id.
func TestRetryAfterTheLeaseExpiredTakesOverWithTheSamePaymentId(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-orig", 777, -time.Minute)
	// the order's items now add up to something else, so only the stored
	// amount can explain a 777 reaching the provider
	o := db.orders["o1"]
	o.Items = []domain.OrderItem{{SKU: "S1", Name: "Item", Quantity: 1, UnitPriceCents: 5000}}
	db.orders["o1"] = o

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusOK)

	if len(g.calls) != 1 {
		t.Fatalf("gateway called %d times, want 1", len(g.calls))
	}
	if got := g.calls[0].IdempotencyKey; got != "pay-orig" {
		t.Errorf("idempotency key = %q, want the original payment id", got)
	}
	if got := g.calls[0].AmountCents; got != 777 {
		t.Errorf("amount = %d, want the 777 stored at the claim", got)
	}
	p := db.payments["o1"]
	if p.ID != "pay-orig" || p.Status != domain.PaymentAuthorized {
		t.Errorf("payment = %+v, want the original id, authorized", p)
	}
	// the takeover renewed the lease for a full period from now
	if renewed := time.Until(p.LeaseExpiresAt); renewed < paymentLease-5*time.Second || renewed > paymentLease {
		t.Errorf("lease renewed to %v from now, want about %v", renewed, paymentLease)
	}
	if got := db.orders["o1"].State; got != domain.OrderPaid {
		t.Errorf("order state = %s, want paid", got)
	}
}

func TestAProviderErrorLeavesThePaymentProcessingAndALaterRetryReconciles(t *testing.T) {
	g := &recordingGateway{err: errors.New("gateway timeout")}
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	router := routerOf(g, g)

	wantStatus(t, payWith(t, db, router, "o1"), http.StatusInternalServerError)
	if got := db.payments["o1"].Status; got != domain.PaymentProcessing {
		t.Fatalf("payment status = %s, want processing after an error", got)
	}
	firstID := db.payments["o1"].ID

	// an immediate retry finds a live lease
	wantStatus(t, payWith(t, db, router, "o1"), http.StatusConflict)
	if len(g.calls) != 1 {
		t.Fatalf("the provider was called %d times, want 1 so far", len(g.calls))
	}

	// the lease runs out and the provider has recovered
	p := db.payments["o1"]
	p.LeaseExpiresAt = time.Now().Add(-time.Second)
	db.payments["o1"] = p
	g.err, g.result = nil, &domain.PaymentResult{PaymentID: "pi_ok", Status: domain.PaymentAuthorized}

	wantStatus(t, payWith(t, db, router, "o1"), http.StatusOK)
	if len(g.calls) != 2 {
		t.Fatalf("the provider was called %d times, want 2", len(g.calls))
	}
	if g.calls[0].IdempotencyKey != firstID || g.calls[1].IdempotencyKey != firstID {
		t.Errorf("keys %q and %q, want both to be the first payment id %q", g.calls[0].IdempotencyKey, g.calls[1].IdempotencyKey, firstID)
	}
	if got := db.payments["o1"]; got.Status != domain.PaymentAuthorized || got.ID != firstID {
		t.Errorf("payment = %+v, want the same id, authorized", got)
	}
}

func TestFiftySimultaneousTakeoversOfAnExpiredPaymentCallTheProviderOnce(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-orig", 1000, -time.Minute)
	s := NewServer(db, routerOf(g, g))

	codes := make([]int, racers)
	fireTogether(racers, func(i int) { codes[i] = post(s, "/orders/o1/pay") })

	ok, conflict, other := tally(codes)
	if other != 0 || ok < 1 || ok+conflict != racers {
		t.Errorf("200s=%d 409s=%d other=%d, want at least one 200 and nothing but 200s and 409s", ok, conflict, other)
	}
	if len(g.calls) != 1 {
		t.Errorf("the provider was called %d times, want exactly 1", len(g.calls))
	}
	if db.payments["o1"].Status != domain.PaymentAuthorized {
		t.Errorf("payment status = %s, want authorized", db.payments["o1"].Status)
	}
}

func TestFiftySimultaneousRetriesAfterSuccessAllReplay(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	s := NewServer(db, routerOf(g, g))
	if post(s, "/orders/o1/pay") != http.StatusOK {
		t.Fatal("the first payment failed")
	}

	codes := make([]int, racers)
	fireTogether(racers, func(i int) { codes[i] = post(s, "/orders/o1/pay") })

	if ok, _, _ := tally(codes); ok != racers {
		t.Errorf("%d of %d retries replayed with a 200", ok, racers)
	}
	if len(g.calls) != 1 {
		t.Errorf("the provider was charged %d times, want exactly 1", len(g.calls))
	}
}

// ---- races inside the flow --------------------------------------------------

// Another request claims the payment between our lookup and our claim: we are
// a retry after all, and must not call the provider.
func TestLosingTheClaimToAConcurrentRequestIsARetry(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	db.beforeWrite = func(f *fakeDB, orderID string) {
		o := f.orders[orderID]
		o.State = domain.OrderPaymentPending
		f.orders[orderID] = o
		now := time.Now()
		f.payments[orderID] = domain.Payment{ID: "pay-other", OrderID: orderID, Status: domain.PaymentProcessing,
			LeaseExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
		f.beforeWrite = nil
	}

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "still being processed")
	if len(g.calls) != 0 {
		t.Errorf("the provider was called %d times by a request that lost the claim", len(g.calls))
	}
}

// The payment finished between our lookup of it and our attempt to take it
// over: answer with the finished outcome, do not call the provider.
func TestAPaymentThatFinishesWhileWeTryToTakeItOverIsReplayed(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Hour)
	db.beforeTakeOver = func(f *fakeDB) {
		p := f.payments["o1"]
		p.Status = domain.PaymentAuthorized
		f.payments["o1"] = p
		o := f.orders["o1"]
		o.State = domain.OrderPaid
		f.orders["o1"] = o
	}

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusOK)
	if len(g.calls) != 0 {
		t.Errorf("the provider was called %d times for a payment that had finished", len(g.calls))
	}
}

// Our outcome lost to another writer's: answer with theirs.
func TestFinishingAfterAnotherRequestAlreadyRecordedTheOutcomeReplaysIt(t *testing.T) {
	g := declining("late")
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	db.beforeWrite = func(f *fakeDB, orderID string) {
		p := f.payments[orderID]
		if p.ID == "" || p.Status != domain.PaymentProcessing {
			return // the claim write, not the finish
		}
		p.Status, p.ProviderPaymentID = domain.PaymentAuthorized, "pi_theirs"
		f.payments[orderID] = p
		o := f.orders[orderID]
		o.State = domain.OrderPaid
		f.orders[orderID] = o
	}

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusOK) // the other request's success wins
	if got := db.payments["o1"].ProviderPaymentID; got != "pi_theirs" {
		t.Errorf("provider id = %q, want the other writer's", got)
	}
}

// The provider answered, but the order moved on (for example it was cancelled)
// while it was working. Nothing may be half-recorded, and a human is told.
func TestOrderThatMovedOnWhileTheProviderWorkedNeedsReview(t *testing.T) {
	logs := captureLog(t)
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	db.beforeWrite = func(f *fakeDB, orderID string) {
		p := f.payments[orderID]
		if p.ID == "" {
			return // still the claim
		}
		o := f.orders[orderID]
		o.State = domain.OrderCancelled
		f.orders[orderID] = o
	}

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusConflict)
	if got := db.payments["o1"].Status; got != domain.PaymentProcessing {
		t.Errorf("payment status = %s, want it left processing", got)
	}
	if got := db.orders["o1"].State; got != domain.OrderCancelled {
		t.Errorf("order state = %s, want the cancel left alone", got)
	}
	if !strings.Contains(logs.String(), "NEEDS REVIEW") {
		t.Errorf("the log does not flag the charged-but-cancelled order:\n%s", logs)
	}
}

// ---- failures ----------------------------------------------------------------

func TestPaymentLookupFailureIsA500AndCallsNothing(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	db.payGetErr = errors.New("db down")

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusInternalServerError)
	if len(g.calls) != 0 {
		t.Errorf("the provider was called %d times", len(g.calls))
	}
	wantWrites(t, db)
}

func TestStartPaymentFailureIsA500AndLeavesNoPayment(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	db.updateErr, db.updateErrOn = errors.New("db down"), domain.OrderPaymentPending

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusInternalServerError)
	if _, ok := db.payments["o1"]; ok {
		t.Error("a failed start left a payment behind")
	}
	if len(g.calls) != 0 {
		t.Errorf("the provider was called %d times", len(g.calls))
	}
}

func TestAPaymentWithAnUnknownStatusIsA500(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	db.payments["o1"] = domain.Payment{ID: "pay-1", OrderID: "o1", Status: "weird"}

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusInternalServerError)
	if len(g.calls) != 0 {
		t.Errorf("the provider was called %d times", len(g.calls))
	}
}

func TestTakeOverFailureIsA500(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, -time.Minute)
	db.takeOverErr = errors.New("db down")

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusInternalServerError)
	if len(g.calls) != 0 {
		t.Errorf("the provider was called %d times", len(g.calls))
	}
}

// Reading the payment again after a refused takeover fails.
func TestReadingThePaymentAgainAfterARefusedTakeoverCanFail(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Hour)
	db.payGetErr, db.payGetFailOnCall = errors.New("db down"), 2 // the first read is PayOrder's own

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusInternalServerError)
}

// After a refused takeover the payment turns out to have an unreadable status.
func TestAReplayOfAnUnreplayableStatusIsA500(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, time.Hour)
	db.beforeTakeOver = func(f *fakeDB) {
		p := f.payments["o1"]
		p.Status = "weird"
		f.payments["o1"] = p
	}

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusInternalServerError)
}

func TestTakeOverOfAnUnpayableOrderIsA500AndCallsNothing(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, -time.Minute)
	o := db.orders["o1"]
	o.PaymentType = "crypto"
	db.orders["o1"] = o

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusInternalServerError)
	wantErrorContains(t, rec, "not available")
	if len(g.calls) != 0 {
		t.Errorf("the provider was called %d times", len(g.calls))
	}
}

func TestReplayOfASuccessWhenTheOrderCannotBeReadIsAServerError(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	router := routerOf(g, g)
	wantStatus(t, payWith(t, db, router, "o1"), http.StatusOK)

	db.getCalls = 0
	db.getFailOnCall = 2 // call 1 is loadOrder, call 2 is the replay's read
	wantStatus(t, payWith(t, db, router, "o1"), http.StatusInternalServerError)
}

func TestFinishFailureAfterTheProviderAnsweredIsA500(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	db.updateErr, db.updateErrOn = errors.New("db down"), domain.OrderPaid

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusInternalServerError)
	if got := db.payments["o1"].Status; got != domain.PaymentProcessing {
		t.Errorf("payment status = %s, want processing so a retry can finish it", got)
	}
	if got := db.orders["o1"].State; got != domain.OrderPaymentPending {
		t.Errorf("order state = %s, want payment_pending", got)
	}
}

// Reading the winner's outcome after losing the finish fails.
func TestReadingTheWinnersOutcomeCanFail(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	db.beforeWrite = func(f *fakeDB, orderID string) {
		p := f.payments[orderID]
		if p.ID == "" || p.Status != domain.PaymentProcessing {
			return
		}
		p.Status = domain.PaymentAuthorized
		f.payments[orderID] = p
		f.payGetErr, f.payGetFailOnCall = errors.New("db down"), f.payGetCalls+1
	}

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusInternalServerError)
}

// A provider answering anything but authorized or declined is an error, not a
// payment that happened.
func TestAProviderAnswerThatIsNeitherAuthorizedNorDeclinedIsAnError(t *testing.T) {
	g := &recordingGateway{result: &domain.PaymentResult{PaymentID: "p1", Status: domain.PaymentRefunded}}
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusInternalServerError)
	if got := db.payments["o1"].Status; got != domain.PaymentProcessing {
		t.Errorf("payment status = %s, want it left processing", got)
	}
}

// ---- unpayable orders leave no trace ---------------------------------------

func TestUnpayableOrderLeavesNoPayment(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", "crypto", 1000)

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusInternalServerError)
	if _, ok := db.payments["o1"]; ok {
		t.Error("an unpayable order got a payment record")
	}
}

func TestOrderThatIsNotCreatedCannotStartAPayment(t *testing.T) {
	g := approving()
	db := newFakeDB()
	db.seed("o1", domain.OrderPreparing)

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "cannot be paid")
	if _, ok := db.payments["o1"]; ok {
		t.Error("a refused payment left a record")
	}
}
