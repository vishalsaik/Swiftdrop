package domain

import "time"

// Alert kinds: each is a situation the system cannot settle by itself.
const (
	// AlertUnrecordedCharge: the provider says money moved and we could not write it down.
	AlertUnrecordedCharge = "unrecorded_charge"
	// AlertContradictingEvent: the provider's report contradicts what we recorded.
	AlertContradictingEvent = "contradicting_event"
	// AlertRefundGivenUp: the refund job tried the maximum number of times and failed.
	AlertRefundGivenUp = "refund_given_up"
	// AlertRefundUnrecorded: the provider refunded but we could not write it down.
	AlertRefundUnrecorded = "refund_unrecorded"
	// AlertPaymentNeedsReview: a payment has had no answer for so long that the
	// system stopped asking, and the customer may have been charged.
	AlertPaymentNeedsReview = "payment_needs_review"
)

// Alert is a problem a person has to look at. It stays open until someone
// resolves it; the same problem raised again only counts another occurrence.
type Alert struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	Subject     string     `json:"subject"` // what it is about, usually a payment id
	OrderID     string     `json:"orderId,omitempty"`
	Message     string     `json:"message"`
	Occurrences int        `json:"occurrences"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastSeenAt  time.Time  `json:"lastSeenAt"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`
	// ResolvedNote is what the person who closed it wrote.
	ResolvedNote string `json:"resolvedNote,omitempty"`
}

// Open reports whether nobody has resolved the alert yet.
func (a Alert) Open() bool { return a.ResolvedAt == nil }
