// Package order holds the order lifecycle: legal state transitions.
package order

import (
	"errors"
	"fmt"
	"slices"

	"swiftdrop/internal/domain"
)

// ErrIllegalTransition is returned when a requested state change is not
// present in legalTransitions.
var ErrIllegalTransition = errors.New("illegal order state transition")

// legalTransitions maps each order state to the set of states it may move
// to directly. This table only concerns OrderState itself — it says
// nothing about FoodReady or AssignedDriverID, which are independent
// facts checked separately (e.g. by the /pickup handler) alongside
// whatever this table says about the state transition.
var legalTransitions = map[domain.OrderState][]domain.OrderState{
	domain.OrderCreated:        {domain.OrderPaymentPending, domain.OrderCancelled},
	domain.OrderPaymentPending: {domain.OrderPaid, domain.OrderCancelled},
	domain.OrderPaid:           {domain.OrderPreparing, domain.OrderCancelled},
	domain.OrderPreparing:      {domain.OrderPickedUp, domain.OrderCancelled},
	domain.OrderPickedUp:       {domain.OrderDelivered},
	domain.OrderDelivered:      nil,
	domain.OrderCancelled:      nil,
}

// CanTransition reports whether the order may move from `from` directly to `to`.
func CanTransition(from, to domain.OrderState) bool {
	return slices.Contains(legalTransitions[from], to)
}

// Apply returns `to` if the transition is legal, or the unchanged current
// state plus ErrIllegalTransition (wrapped with the attempted transition)
// if not.
func Apply(current, to domain.OrderState) (domain.OrderState, error) {
	if !CanTransition(current, to) {
		return current, fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, current, to)
	}
	return to, nil
}
