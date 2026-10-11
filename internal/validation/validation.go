// Package validation holds the rules a new order must satisfy and the chain
// that runs them. It is a Chain of Responsibility: each rule checks one thing
// and the first failure ends the chain.
package validation

import (
	"errors"
	"fmt"
	"strings"

	"swiftdrop/internal/domain"
)

// Rule checks one thing about an order; it returns an error describing the
// problem, or nil.
type Rule func(domain.Order) error

// Chain returns a Rule that runs each rule in order and stops at the first
// failure. If all rules pass, it returns nil.
func Chain(rules ...Rule) Rule {
	return func(o domain.Order) error {
		for _, rule := range rules {
			if err := rule(o); err != nil {
				return err
			}
		}
		return nil
	}
}

// Order validates an order submitted by a client. Scalar fields come first,
// then the items.
var Order = Chain(
	RequireID,
	RequireCustomerID,
	RequireRestaurantID,
	RequirePaymentType,
	RequireItems,
	RequirePositiveQuantities,
	RequireNonNegativePrices,
)

func blank(s string) bool { return strings.TrimSpace(s) == "" }

// RequireID rejects orders without an id.
func RequireID(o domain.Order) error {
	if blank(o.ID) {
		return errors.New("id is required")
	}
	return nil
}

// RequireCustomerID rejects orders that have no customer.
func RequireCustomerID(o domain.Order) error {
	if blank(o.CustomerID) {
		return errors.New("customerId is required")
	}
	return nil
}

// RequireRestaurantID rejects orders placed at no restaurant.
func RequireRestaurantID(o domain.Order) error {
	if blank(o.RestaurantID) {
		return errors.New("restaurantId is required")
	}
	return nil
}

// RequirePaymentType rejects a missing or unknown payment type.
func RequirePaymentType(o domain.Order) error {
	if !o.PaymentType.Valid() {
		names := make([]string, len(domain.PaymentTypes))
		for i, t := range domain.PaymentTypes {
			names[i] = string(t)
		}
		return fmt.Errorf("paymentType must be one of: %s", strings.Join(names, ", "))
	}
	return nil
}

// RequireItems rejects empty carts.
func RequireItems(o domain.Order) error {
	if len(o.Items) == 0 {
		return errors.New("items must not be empty")
	}
	return nil
}

// RequirePositiveQuantities rejects line items with zero or negative quantity.
func RequirePositiveQuantities(o domain.Order) error {
	for i, item := range o.Items {
		if item.Quantity <= 0 {
			return fmt.Errorf("items[%d].quantity must be greater than 0", i)
		}
	}
	return nil
}

// RequireNonNegativePrices rejects line items with a negative unit price.
func RequireNonNegativePrices(o domain.Order) error {
	for i, item := range o.Items {
		if item.UnitPriceCents < 0 {
			return fmt.Errorf("items[%d].unitPriceCents must not be negative", i)
		}
	}
	return nil
}
