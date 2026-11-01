package domain

// PaymentIssue is what is wrong or unfinished about an order's money, in terms a
// customer-facing screen can act on. It is derived from the order and its
// payment, never stored, so it cannot go out of date: there is nothing to keep
// in step with the facts it describes.
type PaymentIssue string

const (
	// IssueNone: nothing to tell the customer.
	IssueNone PaymentIssue = ""
	// IssueConfirming: the payment was started and has no final answer yet. The
	// customer may or may not have been charged; if they were and the order does
	// not go ahead, they are refunded.
	IssueConfirming PaymentIssue = "confirming"
	// IssueRefundPending: the customer was charged and the order is cancelled.
	// The money is on its way back.
	IssueRefundPending PaymentIssue = "refund_pending"
	// IssueRefunded: the refund was accepted by the provider.
	IssueRefunded PaymentIssue = "refunded"
	// IssueRefundFailed: the refund could not be made automatically. Support has
	// to step in.
	IssueRefundFailed PaymentIssue = "refund_failed"
	// IssueDeclined: the payment was refused and no money moved. While attempts are
	// left the order is open again (created) and the customer can try another
	// method; after the last one it is cancelled.
	IssueDeclined PaymentIssue = "declined"
	// IssueAbandoned: an attempt got no answer and the provider has no record of
	// it, so the customer was not charged. The order is open again (or cancelled
	// after the last attempt), like after a decline.
	IssueAbandoned PaymentIssue = "abandoned"
)

// IssueFor derives the PaymentIssue of an order from its payment (nil when it
// has none). Cash is never charged online, so it has nothing to refund.
func IssueFor(o Order, p *Payment) PaymentIssue {
	if p == nil || o.PaymentType == PaymentCash {
		return IssueNone
	}
	switch {
	case p.Unsettled() && (o.State == OrderPaymentPending || o.State == OrderCancelled):
		return IssueConfirming
	case p.Status == PaymentAbandoned && (o.State == OrderCreated || o.State == OrderCancelled):
		return IssueAbandoned
	case p.Status == PaymentDeclined && (o.State == OrderCreated || o.State == OrderCancelled):
		return IssueDeclined
	case p.Status == PaymentAuthorized && o.State == OrderCancelled:
		switch p.RefundStatus {
		case RefundDone:
			return IssueRefunded
		case RefundFailed:
			return IssueRefundFailed
		default:
			return IssueRefundPending
		}
	}
	return IssueNone
}
