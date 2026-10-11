package domain

import "slices"

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

// PaymentType is how a customer chose to pay for an order. It is chosen
// when the order is created and stored on it, because /pay is a separate
// request that has to know which method to use.
type PaymentType string

const (
	PaymentCard   PaymentType = "card"
	PaymentCash   PaymentType = "cash"
	PaymentWallet PaymentType = "wallet"
)

// PaymentTypes lists every accepted payment type, in display order. It is
// the single source of truth for validation and for error messages.
var PaymentTypes = []PaymentType{PaymentCard, PaymentCash, PaymentWallet}

// Valid reports whether p is one of the accepted payment types.
func (p PaymentType) Valid() bool {
	return slices.Contains(PaymentTypes, p)
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
