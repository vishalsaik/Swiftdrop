package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

// ---------------------------------------------------------------------------
// The payment method in the /pay body. A request the provider could never
// fulfil is refused BEFORE anything is claimed, so it cannot leave a payment
// stuck in processing and an order stuck in payment_pending.
// ---------------------------------------------------------------------------

// checkingGateway is a gateway that, like Stripe's, cannot charge without a
// payment method.
type checkingGateway struct{ *recordingGateway }

func (checkingGateway) Check(req domain.PaymentRequest) error {
	if req.PaymentMethodID == "" {
		return fmt.Errorf("%w: paymentMethodId is required", payment.ErrInvalidRequest)
	}
	return nil
}

func strict() checkingGateway { return checkingGateway{approving()} }

// payJSON posts /pay with a JSON body.
func payJSON(t *testing.T, db *fakeDB, router *payment.Router, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/orders/"+id+"/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newTestServer(db, router).Router.ServeHTTP(rec, req)
	return rec
}

func wantNothingClaimed(t *testing.T, db *fakeDB, g *recordingGateway, orderID string, state domain.OrderState) {
	t.Helper()
	if _, claimed := db.payments[orderID]; claimed {
		t.Errorf("a payment was claimed for %s", orderID)
	}
	if got := db.orders[orderID].State; got != state {
		t.Errorf("order state = %s, want it left %s", got, state)
	}
	if len(g.calls) != 0 {
		t.Errorf("the gateway was called %d times", len(g.calls))
	}
}

func TestAPaymentWithoutAMethodIsRefusedAndNothingIsClaimed(t *testing.T) {
	for name, body := range map[string]string{
		"no body":     "",
		"empty JSON":  `{}`,
		"empty value": `{"paymentMethodId":""}`,
		"other field": `{"other":"field"}`,
	} {
		t.Run(name, func(t *testing.T) {
			g := strict()
			db := newFakeDB()
			seedPayable(db, "o1", domain.PaymentCard, 1000)

			var rec *httptest.ResponseRecorder
			if body == "" {
				rec = payWith(t, db, routerOf(g, g), "o1")
			} else {
				rec = payJSON(t, db, routerOf(g, g), "o1", body)
			}
			wantStatus(t, rec, http.StatusUnprocessableEntity)
			wantErrorContains(t, rec, "paymentMethodId is required")
			wantNothingClaimed(t, db, g.recordingGateway, "o1", domain.OrderCreated)
			wantWrites(t, db)
		})
	}
}

// The refused customer can fix the request and pay: the order was never touched.
func TestARefusedPaymentCanBeRetriedWithAMethod(t *testing.T) {
	g := strict()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	router := routerOf(g, g)

	wantStatus(t, payWith(t, db, router, "o1"), http.StatusUnprocessableEntity)
	rec := payJSON(t, db, router, "o1", `{"paymentMethodId":"pm_card_visa"}`)
	wantStatus(t, rec, http.StatusOK)
	if got := orderFrom(t, rec).State; got != domain.OrderPaid {
		t.Errorf("state = %s, want paid", got)
	}
	if len(g.calls) != 1 || g.calls[0].PaymentMethodID != "pm_card_visa" {
		t.Errorf("gateway calls = %+v, want one with the method", g.calls)
	}
}

func TestTheWalletProviderAsksItsGatewayToo(t *testing.T) {
	g := strict()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentWallet, 1000)

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusUnprocessableEntity)
	wantNothingClaimed(t, db, g.recordingGateway, "o1", domain.OrderCreated)
}

// Cash never reaches a gateway, so it needs no method.
func TestCashNeedsNoMethod(t *testing.T) {
	g := strict()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCash, 1000)

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusOK)
}

// A gateway with no requirements (the fake one) accepts a body-less request.
func TestAGatewayWithNoRequirementsNeedsNoMethod(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)

	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusOK)
}

func TestExtraFieldsInThePayBodyAreIgnored(t *testing.T) {
	g := strict()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)

	wantStatus(t, payJSON(t, db, routerOf(g, g), "o1", `{"paymentMethodId":"pm_x","note":"hi"}`), http.StatusOK)
}

func TestAPayBodyThatIsNotJSONIsA400AndNothingIsClaimed(t *testing.T) {
	g := strict()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)

	rec := payJSON(t, db, routerOf(g, g), "o1", `{"paymentMethodId":`)
	wantStatus(t, rec, http.StatusBadRequest)
	wantErrorContains(t, rec, "invalid request body")
	wantNothingClaimed(t, db, g.recordingGateway, "o1", domain.OrderCreated)
}

func TestAnOversizedPayBodyIsA413AndNothingIsClaimed(t *testing.T) {
	g := strict()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)

	huge := `{"paymentMethodId":"` + strings.Repeat("a", maxBodyBytes) + `"}`
	wantStatus(t, payJSON(t, db, routerOf(g, g), "o1", huge), http.StatusRequestEntityTooLarge)
	wantNothingClaimed(t, db, g.recordingGateway, "o1", domain.OrderCreated)
}

// ---- retries ------------------------------------------------------------------

// A retry of a finished payment replays it and needs no method at all.
func TestReplayingAFinishedPaymentNeedsNoMethod(t *testing.T) {
	g := strict()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	router := routerOf(g, g)
	wantStatus(t, payJSON(t, db, router, "o1", `{"paymentMethodId":"pm_x"}`), http.StatusOK)

	wantStatus(t, payWith(t, db, router, "o1"), http.StatusOK)
	if len(g.calls) != 1 {
		t.Errorf("gateway calls = %d, want the replay to call nothing", len(g.calls))
	}
}

// A retry that would take the payment over is refused without taking it: the
// lease stays as it was, so a corrected retry can still take it over.
func TestARefusedTakeoverLeavesThePaymentUntouched(t *testing.T) {
	g := strict()
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, -time.Minute)
	before := db.payments["o1"]

	rec := payWith(t, db, routerOf(g, g), "o1")
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	if db.payments["o1"] != before {
		t.Errorf("payment changed from %+v to %+v", before, db.payments["o1"])
	}
	if len(g.calls) != 0 {
		t.Errorf("the gateway was called %d times", len(g.calls))
	}

	// The corrected retry takes over with the SAME payment id.
	rec = payJSON(t, db, routerOf(g, g), "o1", `{"paymentMethodId":"pm_x"}`)
	wantStatus(t, rec, http.StatusOK)
	if len(g.calls) != 1 || g.calls[0].IdempotencyKey != "pay-1" || g.calls[0].PaymentMethodID != "pm_x" {
		t.Errorf("gateway calls = %+v, want pay-1 with the method", g.calls)
	}
}

func TestATakeoverWithABrokenBodyIsA400AndLeavesThePaymentUntouched(t *testing.T) {
	g := strict()
	db := newFakeDB()
	seedProcessing(db, "o1", "pay-1", 1000, -time.Minute)
	before := db.payments["o1"]

	wantStatus(t, payJSON(t, db, routerOf(g, g), "o1", `nope`), http.StatusBadRequest)
	if db.payments["o1"] != before {
		t.Errorf("payment changed from %+v to %+v", before, db.payments["o1"])
	}
}
