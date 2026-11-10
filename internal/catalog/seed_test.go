package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"

	"swiftdrop/internal/catalog/catalogtest"
	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

func TestSeedingAddsTheDemoMenuOnce(t *testing.T) {
	c := catalogtest.New()
	restaurants, items, err := Seed(ctx, c)
	if err != nil || restaurants != 3 || items != 7 {
		t.Fatalf("first seed = %d restaurants, %d items, %v, want 3, 7", restaurants, items, err)
	}
	open, _ := c.ListRestaurants(ctx, false)
	if len(open) != 3 {
		t.Errorf("open restaurants = %+v, want both open", open)
	}
	menu, _ := c.ListMenuItems(ctx, "demo-corner-kitchen", false)
	if len(menu) != 3 {
		t.Errorf("Corner Kitchen has %d items, want 3", len(menu))
	}
	for _, m := range c.Items {
		if m.PriceCents <= 0 || m.Name == "" || !m.Available {
			t.Errorf("a demo item is not orderable: %+v", m)
		}
	}

	restaurants, items, err = Seed(ctx, c)
	if err != nil || restaurants != 0 || items != 0 {
		t.Errorf("second seed = %d, %d, %v, want nothing added", restaurants, items, err)
	}
}

func TestSeedingChangesNothingThatIsThere(t *testing.T) {
	c := catalogtest.New()
	if _, _, err := Seed(ctx, c); err != nil {
		t.Fatal(err)
	}
	// someone renamed a restaurant, closed another, repriced an item and removed one
	renamed, closed, price := "Our Kitchen", false, int64(1)
	if _, err := c.UpdateRestaurant(ctx, "demo-corner-kitchen", &renamed, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateRestaurant(ctx, "demo-noodle-bar", nil, &closed); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateMenuItem(ctx, "demo-fries", ports.MenuItemPatch{PriceCents: &price}); err != nil {
		t.Fatal(err)
	}
	delete(c.Items, "demo-milkshake")

	restaurants, items, err := Seed(ctx, c)
	if err != nil || restaurants != 0 || items != 1 {
		t.Fatalf("seed = %d, %d, %v, want only the missing item back", restaurants, items, err)
	}
	if c.Restaurants["demo-corner-kitchen"].Name != "Our Kitchen" || c.Restaurants["demo-noodle-bar"].Active || c.Items["demo-fries"].PriceCents != 1 {
		t.Error("seeding changed something that people had changed")
	}
}

// ---- when storage fails ------------------------------------------------------------

type createsFail struct {
	*catalogtest.Catalog
	restaurant, item, place bool
}

func (c createsFail) SetRestaurantPlace(ctx context.Context, id string, p ports.Place) (domain.Restaurant, error) {
	if c.place {
		return domain.Restaurant{}, errors.New("disk full")
	}
	return c.Catalog.SetRestaurantPlace(ctx, id, p)
}

func (c createsFail) CreateRestaurant(ctx context.Context, r domain.Restaurant) (domain.Restaurant, error) {
	if c.restaurant {
		return domain.Restaurant{}, errors.New("disk full")
	}
	return c.Catalog.CreateRestaurant(ctx, r)
}

func (c createsFail) CreateMenuItem(ctx context.Context, m domain.MenuItem) (domain.MenuItem, error) {
	if c.item {
		return domain.MenuItem{}, errors.New("disk full")
	}
	return c.Catalog.CreateMenuItem(ctx, m)
}

func TestSeedingReportsEachKindOfStorageFailure(t *testing.T) {
	broken := catalogtest.New()
	broken.Err = errors.New("connection refused")
	for name, c := range map[string]struct {
		store ports.CatalogStore
		said  string // what the error says it was doing
	}{
		"looking for a restaurant": {broken, "looking for Corner Kitchen"},
		"adding a restaurant":      {createsFail{Catalog: catalogtest.New(), restaurant: true}, "adding Corner Kitchen"},
		"reading a menu":           {menuFails{catalogtest.New()}, "looking at the menu of Corner Kitchen"},
		"adding an item":           {createsFail{Catalog: catalogtest.New(), item: true}, "adding Smash burger"},
		"placing a restaurant":     {createsFail{Catalog: catalogtest.New(), place: true}, "placing Corner Kitchen"},
	} {
		if _, _, err := Seed(ctx, c.store); err == nil || !strings.Contains(err.Error(), c.said) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, c.said)
		}
	}
}

// ---- places and vegetarian items ---------------------------------------------------

func TestSeededRestaurantsAreInPlaceWithTheirCuisineAndVegetarianItems(t *testing.T) {
	c := catalogtest.New()
	if _, _, err := Seed(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, r := range c.Restaurants {
		if r.Latitude == nil || r.Longitude == nil || r.Cuisine == "" || r.DeliveryRadiusKm <= 0 || !domain.ValidPoint(*r.Latitude, *r.Longitude) {
			t.Errorf("%s is not placed: %+v", r.Name, r)
		}
	}
	if !c.Items["demo-fries"].Veg || c.Items["demo-smash-burger"].Veg || !c.Items["demo-paneer-tikka"].Veg {
		t.Error("the demo items' vegetarian flags are not as seeded")
	}
	// a search near Bengaluru finds the two there and not the one in Mumbai, and the other way round
	bengaluru := &ports.Point{Lat: 12.9750, Lng: 77.6410}
	hits, _ := c.Search(ctx, ports.SearchQuery{Near: bengaluru})
	if len(hits) != 2 || hits[0].Restaurant.ID != "demo-corner-kitchen" || hits[1].Restaurant.ID != "demo-noodle-bar" {
		t.Errorf("near Bengaluru = %+v, want Corner Kitchen and Noodle Bar", hits)
	}
	hits, _ = c.Search(ctx, ports.SearchQuery{Near: &ports.Point{Lat: 19.0760, Lng: 72.8777}})
	if len(hits) != 1 || hits[0].Restaurant.ID != "demo-harbour-grill" {
		t.Errorf("near Mumbai = %+v, want Harbour Grill", hits)
	}
}

func TestSeedingPlacesADemoRestaurantThatHasNoPlaceAndLeavesOthersAlone(t *testing.T) {
	c := catalogtest.New()
	// seeded by an older version: there, but not placed
	c.Open("demo-corner-kitchen", "Corner Kitchen")
	// seeded and then moved by staff
	c.Open("demo-noodle-bar", "Noodle Bar")
	c.Place("demo-noodle-bar", "Pan-Asian", 1.5, 2.5, 3)

	if _, _, err := Seed(ctx, c); err != nil {
		t.Fatal(err)
	}
	if r := c.Restaurants["demo-corner-kitchen"]; r.Latitude == nil || r.Cuisine != "American" {
		t.Errorf("the unplaced demo restaurant = %+v, want it placed", r)
	}
	r := c.Restaurants["demo-noodle-bar"]
	if r.Cuisine != "Pan-Asian" || r.Latitude == nil || *r.Latitude != 1.5 || r.DeliveryRadiusKm != 3 {
		t.Errorf("a restaurant that staff placed was changed: %+v", r)
	}
}
