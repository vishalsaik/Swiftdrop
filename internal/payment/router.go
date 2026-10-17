package payment

import (
	"fmt"

	"swiftdrop/internal/domain"
)

// Router resolves a PaymentType to its PaymentProvider strategy.
// Providers are registered at construction time; callers never
// type-switch on PaymentType themselves.
type Router struct {
	providers map[domain.PaymentType]PaymentProvider
}

// NewRouter returns a Router wired with the three built-in strategies.
func NewRouter(card, cash, wallet PaymentProvider) *Router {
	return &Router{
		providers: map[domain.PaymentType]PaymentProvider{
			domain.PaymentCard:   card,
			domain.PaymentCash:   cash,
			domain.PaymentWallet: wallet,
		},
	}
}

// For returns the PaymentProvider registered for t, or an error if t
// is unknown (guards against future PaymentType additions that forget
// to register a strategy).
func (r *Router) For(t domain.PaymentType) (PaymentProvider, error) {
	p, ok := r.providers[t]
	if !ok {
		return nil, fmt.Errorf("no payment provider registered for type %q", t)
	}
	return p, nil
}
