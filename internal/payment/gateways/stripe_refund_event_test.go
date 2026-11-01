package gateways

import (
	"errors"
	"testing"

	"swiftdrop/internal/payment"
)

// A refund Stripe accepted can fail later; Stripe tells us with an event about the
// Refund object. These events are matched on the payment intent, because Stripe
// changes a refund on its own and no request of ours caused the event.

const noRequest = `null`

func refundObject(id, status, reason, intent string) string {
	fields := `"id":"` + id + `","object":"refund","status":"` + status + `"`
	if reason != "" {
		fields += `,"failure_reason":"` + reason + `"`
	}
	if intent != "" {
		fields += `,"payment_intent":"` + intent + `"`
	} else {
		fields += `,"payment_intent":null`
	}
	return `{` + fields + `}`
}

func TestARefundThatFailedBecomesARefundFailedEventWithItsReason(t *testing.T) {
	got, err := parse(t, event("refund.failed", noRequest, refundObject("re_1", "failed", "expired_or_canceled_card", "pi_9")))
	if err != nil {
		t.Fatal(err)
	}
	want := payment.Event{
		Kind: payment.EventRefundFailed, ID: "evt_1", ProviderPaymentID: "pi_9", RefundID: "re_1", Reason: "expired_or_canceled_card",
	}
	if got != want {
		t.Errorf("event = %+v, want %+v", got, want)
	}
	if got.PaymentID != "" {
		t.Errorf("PaymentID = %q: the refund event is not tied to a request of ours", got.PaymentID)
	}
}

// refund.failed is Stripe saying the refund failed; the object's own status is not
// needed to believe it.
func TestTheRefundFailedEventIsBelievedWhateverTheObjectSays(t *testing.T) {
	got, err := parse(t, event("refund.failed", noRequest, refundObject("re_1", "pending", "declined", "pi_9")))
	if err != nil || got.Kind != payment.EventRefundFailed {
		t.Errorf("event = %+v, %v, want a refund failure", got, err)
	}
}

func TestAFailureWithoutAReasonSaysUnknown(t *testing.T) {
	got, err := parse(t, event("refund.failed", noRequest, refundObject("re_1", "failed", "", "pi_9")))
	if err != nil || got.Reason != "unknown" {
		t.Errorf("event = %+v, %v, want the reason unknown", got, err)
	}
}

func TestARefundUpdateThatEndedInFailureCountsAndOtherUpdatesDoNot(t *testing.T) {
	for _, c := range []struct {
		status string
		fails  bool
	}{
		{"failed", true}, {"canceled", true}, {"cancelled", true},
		{"pending", false}, {"succeeded", false}, {"requires_action", false}, {"", false},
	} {
		t.Run(c.status, func(t *testing.T) {
			got, err := parse(t, event("refund.updated", noRequest, refundObject("re_1", c.status, "declined", "pi_9")))
			if c.fails {
				if err != nil || got.Kind != payment.EventRefundFailed || got.RefundID != "re_1" || got.Reason != "declined" {
					t.Errorf("event = %+v, %v, want a refund failure", got, err)
				}
				return
			}
			if !errors.Is(err, payment.ErrEventIgnored) {
				t.Errorf("error = %v, want the event ignored: a refund that is not failing is not news", err)
			}
		})
	}
}

func TestARefundEventWithoutAnIdOrAPaymentIntentCannotBeActedOn(t *testing.T) {
	for name, object := range map[string]string{
		"no payment intent (a refund of a charge)": refundObject("re_1", "failed", "declined", ""),
		"no id": `{"object":"refund","status":"failed","payment_intent":"pi_9"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(t, event("refund.failed", noRequest, object)); !errors.Is(err, payment.ErrEventIgnored) {
				t.Errorf("error = %v, want the event ignored", err)
			}
		})
	}
}

func TestARefundEventThatIsNotAnObjectIsInvalid(t *testing.T) {
	if _, err := parse(t, event("refund.failed", noRequest, `"re_1"`)); !errors.Is(err, payment.ErrInvalidEvent) {
		t.Errorf("error = %v, want an invalid event", err)
	}
}

func TestOnlyASignedRefundEventIsBelieved(t *testing.T) {
	payload := event("refund.failed", noRequest, refundObject("re_1", "failed", "declined", "pi_9"))
	if _, err := webhook().ParseEvent([]byte(payload), header("whsec_other", whNow, payload)); !errors.Is(err, payment.ErrInvalidEvent) {
		t.Errorf("error = %v, want a refund event signed with another secret refused", err)
	}
}

func TestOtherRefundRelatedEventsAreIgnored(t *testing.T) {
	for _, typ := range []string{"refund.created", "charge.refunded", "charge.refund.updated"} {
		if _, err := parse(t, event(typ, noRequest, refundObject("re_1", "failed", "declined", "pi_9"))); !errors.Is(err, payment.ErrEventIgnored) {
			t.Errorf("%s: error = %v, want it ignored", typ, err)
		}
	}
}

// Reading the event's data as raw JSON must not change how payment events are read.
func TestAPaymentEventWhoseObjectIsNotAnObjectIsInvalid(t *testing.T) {
	for _, typ := range []string{"payment_intent.succeeded", "payment_intent.payment_failed"} {
		if _, err := parse(t, event(typ, ourRequest, `"pi_1"`)); !errors.Is(err, payment.ErrInvalidEvent) {
			t.Errorf("%s: error = %v, want an invalid event", typ, err)
		}
	}
}

func TestAnUnrelatedEventWithAnyDataIsStillJustIgnored(t *testing.T) {
	if _, err := parse(t, event("customer.created", noRequest, `"not an object"`)); !errors.Is(err, payment.ErrEventIgnored) {
		t.Errorf("error = %v, want it ignored, not rejected", err)
	}
}
