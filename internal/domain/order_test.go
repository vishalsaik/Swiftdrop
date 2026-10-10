package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTotalCents(t *testing.T) {
	tests := []struct {
		name  string
		items []OrderItem
		want  int64
	}{
		{"no items", nil, 0},
		{"empty slice", []OrderItem{}, 0},
		{"single item", []OrderItem{{Quantity: 1, UnitPriceCents: 599}}, 599},
		{"quantity multiplies", []OrderItem{{Quantity: 3, UnitPriceCents: 250}}, 750},
		{"items add up", []OrderItem{
			{Quantity: 2, UnitPriceCents: 599},
			{Quantity: 1, UnitPriceCents: 249},
		}, 1447},
		{"zero quantity contributes nothing", []OrderItem{{Quantity: 0, UnitPriceCents: 999}}, 0},
		{"large amounts stay exact", []OrderItem{{Quantity: 1000, UnitPriceCents: 3_000_000}}, 3_000_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Order{Items: tt.items}.TotalCents()
			if got != tt.want {
				t.Errorf("TotalCents() = %d, want %d", got, tt.want)
			}
		})
	}
}

// The state strings are stored in Postgres and exposed over the API, so
// changing one is a breaking change; this pins them.
func TestOrderStateValues(t *testing.T) {
	tests := map[OrderState]string{
		OrderCreated:        "created",
		OrderPaymentPending: "payment_pending",
		OrderPaid:           "paid",
		OrderPreparing:      "preparing",
		OrderPickedUp:       "picked_up",
		OrderDelivered:      "delivered",
		OrderCancelled:      "cancelled",
	}
	for state, want := range tests {
		if string(state) != want {
			t.Errorf("state = %q, want %q", state, want)
		}
	}
}

func TestOrderJSONUsesCamelCaseKeys(t *testing.T) {
	o := Order{
		ID:               "o-1",
		PaymentType:      "card",
		CustomerID:       "c-1",
		RestaurantID:     "r-1",
		Items:            []OrderItem{{SKU: "S1", Name: "Burger", Quantity: 2, UnitPriceCents: 599}},
		State:            OrderPreparing,
		AssignedDriverID: "d-1",
	}
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"id", "paymentType", "customerId", "restaurantId", "items",
		"state", "assignedDriverId", "createdAt", "updatedAt",
	} {
		if _, ok := generic[key]; !ok {
			t.Errorf("JSON is missing key %q (got %s)", key, raw)
		}
	}
	if generic["state"] != "preparing" {
		t.Errorf(`state = %v, want "preparing"`, generic["state"])
	}
	item := generic["items"].([]any)[0].(map[string]any)
	for _, key := range []string{"sku", "name", "quantity", "unitPriceCents"} {
		if _, ok := item[key]; !ok {
			t.Errorf("item JSON is missing key %q", key)
		}
	}
}

func TestFoodReadyIsOmittedUntilSet(t *testing.T) {
	notReady, err := json.Marshal(Order{ID: "o-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(notReady), "foodReady") {
		t.Errorf("foodReady must be omitted while nil, got %s", notReady)
	}

	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ready, err := json.Marshal(Order{ID: "o-1", FoodReady: &at})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ready), `"foodReady":"2026-10-10T12:00:00Z"`) {
		t.Errorf("foodReady should be present once set, got %s", ready)
	}
}

func TestOrderJSONRoundTrip(t *testing.T) {
	body := `{"id":"o-9","customerId":"c","restaurantId":"r","state":"paid",` +
		`"items":[{"sku":"S","name":"Fries","quantity":1,"unitPriceCents":249}]}`
	var o Order
	if err := json.Unmarshal([]byte(body), &o); err != nil {
		t.Fatal(err)
	}
	if o.ID != "o-9" || o.CustomerID != "c" || o.RestaurantID != "r" || o.State != OrderPaid {
		t.Errorf("decoded order has wrong scalar fields: %+v", o)
	}
	if len(o.Items) != 1 || o.Items[0].SKU != "S" || o.Items[0].UnitPriceCents != 249 {
		t.Errorf("decoded order has wrong items: %+v", o.Items)
	}
	if o.FoodReady != nil || o.AssignedDriverID != "" {
		t.Errorf("fields absent from the body should stay zero, got %+v", o)
	}
}
