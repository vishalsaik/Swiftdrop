package gateways

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

func TestStripeCheckAcceptsAPaymentMethodId(t *testing.T) {
	g := NewStripeGateway(testKey)
	for _, id := range []string{"pm_card_visa", "pm_1NG8Du2eZvKYlo2CYzzldNr7", "pm_card_visa_chargeDeclined"} {
		if err := g.Check(domain.PaymentRequest{PaymentMethodID: id}); err != nil {
			t.Errorf("Check(%q) = %v, want accepted", id, err)
		}
	}
}

func TestStripeCheckRefusesWhatStripeCouldNeverCharge(t *testing.T) {
	g := NewStripeGateway(testKey)
	cases := map[string]string{
		"empty":                "",
		"a card token":         "tok_visa",
		"a customer id":        "cus_123",
		"no prefix":            "card_visa",
		"prefix only inside":   "x_pm_card",
		"upper case prefix":    "PM_card_visa",
		"a space inside":       "pm_card visa",
		"a newline":            "pm_card\nvisa",
		"a tab":                "pm_card\tvisa",
		"longer than any real": "pm_" + strings.Repeat("a", 253),
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			err := g.Check(domain.PaymentRequest{PaymentMethodID: id})
			if !errors.Is(err, payment.ErrInvalidRequest) {
				t.Errorf("Check(%q) = %v, want one wrapping ErrInvalidRequest", id, err)
			}
		})
	}
}

// The longest accepted id is exactly the limit.
func TestStripeCheckLengthBoundary(t *testing.T) {
	g := NewStripeGateway(testKey)
	atLimit := "pm_" + strings.Repeat("a", maxPaymentMethodIDLength-3)
	if err := g.Check(domain.PaymentRequest{PaymentMethodID: atLimit}); err != nil {
		t.Errorf("an id of %d characters was refused: %v", len(atLimit), err)
	}
	over := atLimit + "a"
	if err := g.Check(domain.PaymentRequest{PaymentMethodID: over}); err == nil {
		t.Errorf("an id of %d characters was accepted", len(over))
	}
}

func TestStripeCheckSaysWhatIsWrong(t *testing.T) {
	g := NewStripeGateway(testKey)
	missing := g.Check(domain.PaymentRequest{})
	if missing == nil || !strings.Contains(missing.Error(), "required") {
		t.Errorf("a missing id: error = %v, want it to say it is required", missing)
	}
	wrong := g.Check(domain.PaymentRequest{PaymentMethodID: "tok_visa"})
	if wrong == nil || !strings.Contains(wrong.Error(), "pm_") {
		t.Errorf("a wrong id: error = %v, want it to mention pm_", wrong)
	}
}

// Check is a local check: it must never touch the network.
func TestStripeCheckMakesNoRequest(t *testing.T) {
	called := false
	g := stripeStub(t, func(_ http.ResponseWriter, _ *http.Request) { called = true })
	_ = g.Check(domain.PaymentRequest{PaymentMethodID: "pm_card_visa"})
	_ = g.Check(domain.PaymentRequest{})
	if called {
		t.Error("Check called Stripe")
	}
}

var _ payment.RequestChecker = (*StripeGateway)(nil)
