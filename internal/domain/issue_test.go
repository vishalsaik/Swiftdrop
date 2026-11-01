package domain

import "testing"

func TestIssueFor(t *testing.T) {
	card := func(state OrderState) Order { return Order{State: state, PaymentType: PaymentCard} }
	pay := func(status PaymentStatus, refund RefundStatus) *Payment {
		return &Payment{Status: status, RefundStatus: refund}
	}
	tests := []struct {
		name string
		o    Order
		p    *Payment
		want PaymentIssue
	}{
		{"no payment yet", card(OrderCreated), nil, IssueNone},
		{"being processed", card(OrderPaymentPending), pay(PaymentProcessing, ""), IssueConfirming},
		{"cancelled while being processed", card(OrderCancelled), pay(PaymentProcessing, ""), IssueConfirming},
		{"paid", card(OrderPaid), pay(PaymentAuthorized, ""), IssueNone},
		{"delivered", card(OrderDelivered), pay(PaymentAuthorized, RefundNone), IssueNone},
		{"declined, open for another try", card(OrderCreated), pay(PaymentDeclined, ""), IssueDeclined},
		{"declined, attempts used up", card(OrderCancelled), pay(PaymentDeclined, ""), IssueDeclined},
		{"an old decline on an order a later attempt paid", card(OrderPaid), pay(PaymentDeclined, ""), IssueNone},
		{"charged and cancelled, refund not started", card(OrderCancelled), pay(PaymentAuthorized, RefundNone), IssueRefundPending},
		{"charged and cancelled, status unset", card(OrderCancelled), pay(PaymentAuthorized, ""), IssueRefundPending},
		{"charged and cancelled, refund running", card(OrderCancelled), pay(PaymentAuthorized, RefundInProgress), IssueRefundPending},
		{"refunded", card(OrderCancelled), pay(PaymentAuthorized, RefundDone), IssueRefunded},
		{"refund failed", card(OrderCancelled), pay(PaymentAuthorized, RefundFailed), IssueRefundFailed},
		{"a processing payment on an order that moved to paid", card(OrderPaid), pay(PaymentProcessing, ""), IssueNone},
		{"cash cancelled", Order{State: OrderCancelled, PaymentType: PaymentCash}, pay(PaymentAuthorized, ""), IssueNone},
		{"cash being processed", Order{State: OrderPaymentPending, PaymentType: PaymentCash}, pay(PaymentProcessing, ""), IssueNone},
		{"wallet charged and cancelled", Order{State: OrderCancelled, PaymentType: PaymentWallet}, pay(PaymentAuthorized, ""), IssueRefundPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IssueFor(tt.o, tt.p); got != tt.want {
				t.Errorf("IssueFor = %q, want %q", got, tt.want)
			}
		})
	}
}
