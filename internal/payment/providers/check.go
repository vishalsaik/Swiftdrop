package providers

import (
	"swiftdrop/internal/domain"
	"swiftdrop/internal/payment"
)

// checkWith asks the gateway to check the request if it can. Most gateways have
// no requirements beyond what Charge itself reports, so no answer means fine.
func checkWith(g payment.Gateway, req domain.PaymentRequest) error {
	if checker, ok := g.(payment.RequestChecker); ok {
		return checker.Check(req)
	}
	return nil
}
