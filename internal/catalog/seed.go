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
	veg                   bool
}

type demoRestaurant struct {
	id, name, cuisine string
	lat, lng, radius  float64
	items             []demoItem
}

// A small menu to try the app with. The ids are fixed, so seeding twice adds nothing. Two
// restaurants are in Bengaluru (Indiranagar), a kilometre apart, and one is in Mumbai, so a search
// near Bengaluru finds two and one near Mumbai finds one.
var demo = []demoRestaurant{
	{"demo-corner-kitchen", "Corner Kitchen", "American", 12.9719, 77.6412, 6, []demoItem{
		{"demo-smash-burger", "Smash burger", "Two patties, cheese, onions", 950, false},
		{"demo-fries", "Fries", "Skin-on, salted", 400, true},
		{"demo-milkshake", "Vanilla milkshake", "", 550, true},
	}},
	{"demo-noodle-bar", "Noodle Bar", "Thai", 12.9784, 77.6408, 4, []demoItem{
		{"demo-pad-thai", "Pad thai", "Rice noodles, tamarind, peanuts", 1390, false},
		{"demo-spring-rolls", "Spring rolls", "Four, with sweet chilli", 650, true},
	}},
	{"demo-harbour-grill", "Harbour Grill", "North Indian", 19.0596, 72.8295, 8, []demoItem{
		{"demo-butter-chicken", "Butter chicken", "Creamy tomato gravy, with naan", 1250, false},
		{"demo-paneer-tikka", "Paneer tikka", "Charred cottage cheese, mint chutney", 980, true},
	}},
}

// Seed adds the demo restaurants and their menu items that are not there yet and says how many
// of each it added. A demo restaurant that has no place yet (one seeded before restaurants had
// places) is given its place, and nothing else that is there is changed or removed, so it is
// safe to run against a database people are already using.
func Seed(ctx context.Context, store ports.CatalogStore) (restaurants, items int, err error) {
	for _, r := range demo {
		have, err := store.GetRestaurant(ctx, r.id)
		switch {
		case errors.Is(err, ports.ErrRestaurantNotFound):
			have, err = store.CreateRestaurant(ctx, domain.Restaurant{ID: r.id, Name: r.name, Active: true})
			if err != nil {
				return restaurants, items, fmt.Errorf("adding %s: %w", r.name, err)
			}
			restaurants++
		case err != nil:
			return restaurants, items, fmt.Errorf("looking for %s: %w", r.name, err)
		}
		if have.Latitude == nil {
			lat, lng := r.lat, r.lng
			if _, err := store.SetRestaurantPlace(ctx, r.id, ports.Place{Cuisine: r.cuisine, Latitude: &lat, Longitude: &lng, DeliveryRadiusKm: r.radius}); err != nil {
				return restaurants, items, fmt.Errorf("placing %s: %w", r.name, err)
			}
		}
		ids := make([]string, len(r.items))
		for i, m := range r.items {
			ids[i] = m.id
		}
		found, err := store.GetMenuItems(ctx, ids)
		if err != nil {
			return restaurants, items, fmt.Errorf("looking at the menu of %s: %w", r.name, err)
		}
		onTheMenu := map[string]bool{}
		for _, m := range found {
			onTheMenu[m.ID] = true
		}
		for _, m := range r.items {
			if onTheMenu[m.id] {
				continue
			}
			if _, err := store.CreateMenuItem(ctx, domain.MenuItem{
				ID: m.id, RestaurantID: r.id, Name: m.name, Description: m.description, PriceCents: m.priceCents, Available: true, Veg: m.veg,
			}); err != nil {
				return restaurants, items, fmt.Errorf("adding %s: %w", m.name, err)
			}
			items++
		}
	}
	return restaurants, items, nil
}
