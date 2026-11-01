package domain

import "testing"

func TestAPaymentIsUnsettledUntilItHasAnAnswer(t *testing.T) {
	for status, want := range map[PaymentStatus]bool{
		PaymentProcessing:  true,
		PaymentNeedsReview: true, // a late answer can still settle it
		PaymentAuthorized:  false,
		PaymentDeclined:    false,
		PaymentAbandoned:   false,
		PaymentRefunded:    false,
	} {
		if got := (Payment{Status: status}).Unsettled(); got != want {
			t.Errorf("%s: Unsettled = %v, want %v", status, got, want)
		}
	}
}

// A payment that did not go through reopens the order for another attempt, until
// the attempts run out.
func TestAFailedPaymentReopensTheOrderUntilTheAttemptsRunOut(t *testing.T) {
	for attempt, want := range map[int]OrderState{
		0:                      OrderCreated, // an unnumbered payment is the first
		1:                      OrderCreated,
		MaxPaymentAttempts - 1: OrderCreated,
		MaxPaymentAttempts:     OrderCancelled,
		MaxPaymentAttempts + 1: OrderCancelled,
	} {
		if got := (Payment{Attempt: attempt}).OrderStateAfterFailure(); got != want {
			t.Errorf("attempt %d: order goes to %s, want %s", attempt, got, want)
		}
	}
}
