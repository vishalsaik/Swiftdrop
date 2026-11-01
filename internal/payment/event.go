package payment

import (
	"errors"
	"net/http"

	"swiftdrop/internal/domain"
)

// Event is a provider's own report that one of our payments reached a final
// outcome, after it has been verified and translated into our terms. It is the
// webhook counterpart of PaymentResult: the same fact, delivered to us instead
// of returned from a call, so it still arrives when the reply to the call was
// lost.
type Event struct {
	// Kind says what the event reports. The zero value is a payment outcome.
	Kind EventKind
	// ID is the provider's id for the event, for the log.
	ID string
	// PaymentID is OUR payment id: the idempotency key we sent with the charge.
	PaymentID string
	// ProviderPaymentID is the provider's own id for the payment.
	ProviderPaymentID string
	// Status is PaymentAuthorized or PaymentDeclined.
	Status domain.PaymentStatus
	// Reason is the decline reason; empty unless Status is PaymentDeclined, and for
	// a refund failure why the provider says the refund failed.
	Reason string
	// RefundID is the provider's id for the refund, for a refund failure.
	RefundID string
}

// EventKind is what an Event reports.
type EventKind int

const (
	// EventPayment: a payment reached a final outcome (Status says which).
	EventPayment EventKind = iota
	// EventRefundFailed: a refund the provider had accepted failed or was
	// cancelled afterwards, so the money is not on its way back after all. It is
	// found through ProviderPaymentID, not PaymentID: the provider's report of an
	// asynchronous change is not tied to a request of ours.
	EventRefundFailed
)

var (
	// ErrInvalidEvent means the request is not a genuine, readable event:
	// a bad or missing signature, a stale timestamp, or a body that is not an
	// event. The caller rejects it.
	ErrInvalidEvent = errors.New("invalid webhook event")

	// ErrEventIgnored means the event is genuine but not one we act on: another
	// event type, or an event that none of our payment requests caused. The
	// caller acknowledges it so the provider stops retrying.
	ErrEventIgnored = errors.New("webhook event ignored")
)

// EventParser verifies a webhook request and translates it into an Event.
// Each provider implements it (Adapter): the handler never sees a provider's
// signature scheme or event format.
type EventParser interface {
	// ParseEvent takes the raw request body, exactly as received, and the
	// request headers. It returns an error wrapping ErrInvalidEvent or
	// ErrEventIgnored when there is no Event to act on.
	ParseEvent(payload []byte, header http.Header) (Event, error)
}
