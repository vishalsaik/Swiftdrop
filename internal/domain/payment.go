package domain

// PaymentRequest is what the order service hands to a PaymentProvider.
// IdempotencyKey is mandatory input, not an afterthought: Core invariant #4
// requires "a payment operation is idempotent for a given order and payment
// key," so the key travels with the request from the start rather than
// being bolted on by a decorator later.
type PaymentRequest struct {
	OrderID        string `json:"orderId"`
	IdempotencyKey string `json:"idempotencyKey"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
}

type PaymentStatus string

const (
	PaymentAuthorized PaymentStatus = "authorized"
	PaymentDeclined   PaymentStatus = "declined"
	PaymentRefunded   PaymentStatus = "refunded"
)

type PaymentResult struct {
	PaymentID string        `json:"paymentId"`
	Status    PaymentStatus `json:"status"`
	Reason    string        `json:"reason"` // set when Status is PaymentDeclined
}
