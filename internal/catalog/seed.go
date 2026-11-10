package catalog

import (
	"context"
	"errors"
	"fmt"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

type demoItem struct {
	id, name, description string
	priceCents            int64
}

type demoRestaurant struct {
	id, name string
	items    []demoItem
}

// A small menu to try the app with. The ids are fixed, so seeding twice adds nothing.
var demo = []demoRestaurant{
	{"demo-corner-kitchen", "Corner Kitchen", []demoItem{
		{"demo-smash-burger", "Smash burger", "Two patties, cheese, onions", 950},
		{"demo-fries", "Fries", "Skin-on, salted", 400},
		{"demo-milkshake", "Vanilla milkshake", "", 550},
	}},
	{"demo-noodle-bar", "Noodle Bar", []demoItem{
		{"demo-pad-thai", "Pad thai", "Rice noodles, tamarind, peanuts", 1390},
		{"demo-spring-rolls", "Spring rolls", "Four, with sweet chilli", 650},
	}},
}

// Seed adds the demo restaurants and their menu items that are not there yet and says how many
// of each it added. It never changes or removes anything, so it is safe to run against a
// database people are already using.
func Seed(ctx context.Context, store ports.CatalogStore) (restaurants, items int, err error) {
	for _, r := range demo {
		_, err := store.GetRestaurant(ctx, r.id)
		switch {
		case errors.Is(err, ports.ErrRestaurantNotFound):
			if _, err := store.CreateRestaurant(ctx, domain.Restaurant{ID: r.id, Name: r.name, Active: true}); err != nil {
				return restaurants, items, fmt.Errorf("adding %s: %w", r.name, err)
			}
			restaurants++
		case err != nil:
			return restaurants, items, fmt.Errorf("looking for %s: %w", r.name, err)
		}
		ids := make([]string, len(r.items))
		for i, m := range r.items {
			ids[i] = m.id
		}
		have, err := store.GetMenuItems(ctx, ids)
		if err != nil {
			return restaurants, items, fmt.Errorf("looking at the menu of %s: %w", r.name, err)
		}
		onTheMenu := map[string]bool{}
		for _, m := range have {
			onTheMenu[m.ID] = true
		}
		for _, m := range r.items {
			if onTheMenu[m.id] {
				continue
			}
			if _, err := store.CreateMenuItem(ctx, domain.MenuItem{
				ID: m.id, RestaurantID: r.id, Name: m.name, Description: m.description, PriceCents: m.priceCents, Available: true,
			}); err != nil {
				return restaurants, items, fmt.Errorf("adding %s: %w", m.name, err)
			}
			items++
		}
	}
	return restaurants, items, nil
}
