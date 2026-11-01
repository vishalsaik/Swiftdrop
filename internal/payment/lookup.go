package payment

import (
	"context"

	"swiftdrop/internal/domain"
)

// Looker asks an external payment processor what became of a payment WITHOUT
// charging again. It is the read-only way to settle a payment whose answer we
// never received: a lost reply, a crash after the charge. Sending the charge
// again would be wrong twice over: the customer may have given up and walked
// away, and after the provider forgets the idempotency key (Stripe keeps it at
// least 24 hours) it would be a second charge.
//
// Like Refunder it is a separate, small interface: a gateway that can only
// charge, like the test Mock, stays valid.
//
// Lookup returns an error for anything that is not an answer ("I could not
// find out"), and the caller asks again later. A result with Found = false is an
// answer: the provider has no such payment, so no money moved. An implementation
// must never report that on a guess, such as a search that was cut short.
type Looker interface {
	Lookup(ctx context.Context, req domain.LookupRequest) (*domain.LookupResult, error)
}
