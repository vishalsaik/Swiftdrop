package validation

import (
	"errors"
	"strings"
	"testing"

	"swiftdrop/internal/domain"
)

// validOrder passes every rule; tests break one field at a time.
func validOrder() domain.Order {
	return domain.Order{
		ID:           "o1",
		CustomerID:   "c",
		RestaurantID: "r",
		PaymentType:  domain.PaymentCard,
		Items:        []domain.OrderItem{{SKU: "S", Name: "Burger", Quantity: 1, UnitPriceCents: 100}},
	}
}

func TestOrderAcceptsAValidOrder(t *testing.T) {
	if err := Order(validOrder()); err != nil {
		t.Fatalf("a valid order was rejected: %v", err)
	}
}

func TestEveryRule(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		mutate  func(*domain.Order)
		wantErr string // "" means the mutated order must pass
	}{
		{"id present", RequireID, func(o *domain.Order) {}, ""},
		{"id empty", RequireID, func(o *domain.Order) { o.ID = "" }, "id is required"},
		{"id blank", RequireID, func(o *domain.Order) { o.ID = " \t " }, "id is required"},

		{"customer empty", RequireCustomerID, func(o *domain.Order) { o.CustomerID = "" }, "customerId is required"},
		{"customer blank", RequireCustomerID, func(o *domain.Order) { o.CustomerID = "  " }, "customerId is required"},
		{"customer present", RequireCustomerID, func(o *domain.Order) {}, ""},

		{"restaurant empty", RequireRestaurantID, func(o *domain.Order) { o.RestaurantID = "" }, "restaurantId is required"},
		{"restaurant present", RequireRestaurantID, func(o *domain.Order) {}, ""},

		{"payment card", RequirePaymentType, func(o *domain.Order) { o.PaymentType = domain.PaymentCard }, ""},
		{"payment cash", RequirePaymentType, func(o *domain.Order) { o.PaymentType = domain.PaymentCash }, ""},
		{"payment wallet", RequirePaymentType, func(o *domain.Order) { o.PaymentType = domain.PaymentWallet }, ""},
		{"payment empty", RequirePaymentType, func(o *domain.Order) { o.PaymentType = "" }, "paymentType must be one of: card, cash, wallet"},
		{"payment unknown", RequirePaymentType, func(o *domain.Order) { o.PaymentType = "bitcoin" }, "paymentType must be one of"},
		{"payment wrong case", RequirePaymentType, func(o *domain.Order) { o.PaymentType = "Card" }, "paymentType must be one of"},

		{"items nil", RequireItems, func(o *domain.Order) { o.Items = nil }, "items must not be empty"},
		{"items empty", RequireItems, func(o *domain.Order) { o.Items = []domain.OrderItem{} }, "items must not be empty"},
		{"items present", RequireItems, func(o *domain.Order) {}, ""},

		{"quantity zero", RequirePositiveQuantities, func(o *domain.Order) { o.Items[0].Quantity = 0 }, "items[0].quantity must be greater than 0"},
		{"quantity negative", RequirePositiveQuantities, func(o *domain.Order) { o.Items[0].Quantity = -1 }, "items[0].quantity must be greater than 0"},
		{"quantity one", RequirePositiveQuantities, func(o *domain.Order) { o.Items[0].Quantity = 1 }, ""},
		{"quantity error names the right item", RequirePositiveQuantities, func(o *domain.Order) {
			o.Items = append(o.Items, domain.OrderItem{Quantity: 0})
		}, "items[1].quantity"},

		{"price negative", RequireNonNegativePrices, func(o *domain.Order) { o.Items[0].UnitPriceCents = -1 }, "items[0].unitPriceCents must not be negative"},
		{"price zero", RequireNonNegativePrices, func(o *domain.Order) { o.Items[0].UnitPriceCents = 0 }, ""},
		{"price error names the right item", RequireNonNegativePrices, func(o *domain.Order) {
			o.Items = append(o.Items, domain.OrderItem{Quantity: 1, UnitPriceCents: -5})
		}, "items[1].unitPriceCents"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := validOrder()
			tt.mutate(&o)
			err := tt.rule(o)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestChainWithNoRulesPasses(t *testing.T) {
	if err := Chain()(domain.Order{}); err != nil {
		t.Errorf("an empty chain must pass, got %v", err)
	}
}

func TestChainRunsRulesInOrderAndStopsAtTheFirstFailure(t *testing.T) {
	var ran []string
	rule := func(name string, err error) Rule {
		return func(domain.Order) error {
			ran = append(ran, name)
			return err
		}
	}
	first, second := errors.New("first failure"), errors.New("second failure")

	err := Chain(rule("a", nil), rule("b", first), rule("c", second))(domain.Order{})

	if !errors.Is(err, first) {
		t.Errorf("error = %v, want the first failure", err)
	}
	if got := strings.Join(ran, ","); got != "a,b" {
		t.Errorf("ran %q, want a,b (c must not run after b failed)", got)
	}
}

func TestChainPassesWhenEveryRulePasses(t *testing.T) {
	calls := 0
	pass := func(domain.Order) error { calls++; return nil }
	if err := Chain(pass, pass, pass)(domain.Order{}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("ran %d rules, want 3", calls)
	}
}

func TestOrderReportsOnlyTheFirstFailure(t *testing.T) {
	err := Order(domain.Order{PaymentType: "bitcoin"}) // breaks almost every rule
	if err == nil || err.Error() != "id is required" {
		t.Errorf("error = %v, want only %q", err, "id is required")
	}
}

func TestOrderChecksScalarFieldsBeforeItems(t *testing.T) {
	o := validOrder()
	o.PaymentType = ""
	o.Items = nil // both wrong; the payment type must be reported first
	if err := Order(o); err == nil || !strings.Contains(err.Error(), "paymentType") {
		t.Errorf("error = %v, want the paymentType failure first", err)
	}
}
