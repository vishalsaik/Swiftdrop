// Package catalog prices an order from the menu. A customer says which menu items and how many;
// the name and the price come from the menu, never from the request, so nobody can order a
// burger for a cent. Staff may still enter a custom line (a phone order) with a price of their own.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// Problem is something wrong with the order that the person placing it can fix. Its message
// says what, in words for them.
type Problem struct{ Message string }

func (p *Problem) Error() string { return p.Message }

func problem(format string, args ...any) error {
	return &Problem{Message: fmt.Sprintf(format, args...)}
}

// Pricer prices orders from the catalog.
type Pricer struct {
	Store ports.CatalogStore
}

// Price returns the order's lines with names and prices taken from the menu.
//
// A line with a MenuItemID is a menu line: the item must exist, be on this restaurant's menu and
// be available, and the restaurant must exist and be open. Its name, sku and price are replaced by
// the menu's, whatever the request said, and two lines for one item become one. A line without one
// is a custom line: a customer may not place it (problem), staff's is passed through as it is.
// An empty list, or an order with no restaurant, is returned unchanged for the validation rules
// to refuse in their own words. The error is a *Problem for anything the person can fix.
func (p Pricer) Price(ctx context.Context, restaurantID string, items []domain.OrderItem, staff bool) ([]domain.OrderItem, error) {
	if len(items) == 0 || strings.TrimSpace(restaurantID) == "" {
		return items, nil
	}
	if len(items) > domain.MaxOrderLines {
		return nil, problem("an order can have at most %d lines", domain.MaxOrderLines)
	}
	hasMenuLine := false
	for i, it := range items {
		switch {
		case it.MenuItemID != "":
			hasMenuLine = true
		case !staff:
			return nil, problem("items[%d]: choose an item from the menu (menuItemId is required)", i)
		}
	}
	if !hasMenuLine {
		return items, nil // staff's custom lines only: nothing to look up
	}

	restaurant, err := p.Store.GetRestaurant(ctx, restaurantID)
	switch {
	case errors.Is(err, ports.ErrRestaurantNotFound):
		return nil, problem("restaurant %q does not exist", restaurantID)
	case err != nil:
		return nil, err
	case !restaurant.Active:
		return nil, problem("%s is closed", restaurant.Name)
	}

	ids := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		if it.MenuItemID != "" && !seen[it.MenuItemID] {
			seen[it.MenuItemID] = true
			ids = append(ids, it.MenuItemID)
		}
	}
	found, err := p.Store.GetMenuItems(ctx, ids)
	if err != nil {
		return nil, err
	}
	menu := make(map[string]domain.MenuItem, len(found))
	for _, m := range found {
		menu[m.ID] = m
	}

	out := make([]domain.OrderItem, 0, len(items))
	at := map[string]int{} // where a menu item already is in out, to merge a repeat into it
	for i, it := range items {
		if it.MenuItemID == "" {
			out = append(out, it) // staff's custom line
			continue
		}
		if it.Quantity < 1 {
			return nil, problem("items[%d].quantity must be greater than 0", i)
		}
		m, ok := menu[it.MenuItemID]
		switch {
		case !ok:
			return nil, problem("items[%d]: there is no menu item %q", i, it.MenuItemID)
		case m.RestaurantID != restaurantID:
			return nil, problem("items[%d]: %s is not on the menu of %s", i, m.Name, restaurant.Name)
		case !m.Available:
			return nil, problem("items[%d]: %s is not available right now", i, m.Name)
		}
		if j, again := at[m.ID]; again {
			out[j].Quantity += it.Quantity
			if out[j].Quantity > domain.MaxLineQuantity {
				return nil, problem("at most %d of one item (%s)", domain.MaxLineQuantity, m.Name)
			}
			continue
		}
		if it.Quantity > domain.MaxLineQuantity {
			return nil, problem("at most %d of one item (%s)", domain.MaxLineQuantity, m.Name)
		}
		at[m.ID] = len(out)
		out = append(out, domain.OrderItem{MenuItemID: m.ID, SKU: m.ID, Name: m.Name, Quantity: it.Quantity, UnitPriceCents: m.PriceCents})
	}
	return out, nil
}
