package adapters

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// These tests share a database with the others, so each one looks for a word nobody else uses
// (Quokka, Wombat) or searches near a place nobody else has restaurants (Reykjavik), and removes
// what it made.

func place(t *testing.T, db *DBAdapter, id, cuisine string, lat, lng, radius float64) {
	t.Helper()
	if _, err := db.SetRestaurantPlace(context.Background(), id, ports.Place{Cuisine: cuisine, Latitude: &lat, Longitude: &lng, DeliveryRadiusKm: radius}); err != nil {
		t.Fatalf("SetRestaurantPlace(%s): %v", id, err)
	}
}

func hitNames(hits []ports.SearchHit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Restaurant.Name)
	}
	return out
}

func joined(s []string) string { return strings.Join(s, " | ") }

func itemsOf(h ports.SearchHit) []string {
	var out []string
	for _, m := range h.Items {
		out = append(out, m.Name)
	}
	return out
}

// reykjavik is where the location tests put their restaurants: nobody else's data is near.
var reykjavik = ports.Point{Lat: 64.1466, Lng: -21.9426}

// north is a restaurant's latitude km north of reykjavik (a degree of latitude is 111.19 km).
func north(km float64) float64 { return reykjavik.Lat + km/111.19 }

func TestARestaurantsPlaceIsKeptAndCleared(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	made := aRestaurant(t, db, "geo-r1", "Geo Kitchen", true)
	if made.Latitude != nil || made.Longitude != nil || made.Cuisine != "" || made.DeliveryRadiusKm != domain.DefaultDeliveryRadiusKm {
		t.Errorf("a new restaurant = %+v, want no place, no cuisine and the default radius", made)
	}
	lat, lng := 12.9719, 77.6412
	r, err := db.SetRestaurantPlace(ctx, "geo-r1", ports.Place{Cuisine: "Thai", Latitude: &lat, Longitude: &lng, DeliveryRadiusKm: 7.5})
	if err != nil || r.Cuisine != "Thai" || r.Latitude == nil || *r.Latitude != lat || *r.Longitude != lng || r.DeliveryRadiusKm != 7.5 {
		t.Errorf("placed = %+v, %v", r, err)
	}
	if got, err := db.GetRestaurant(ctx, "geo-r1"); err != nil || got.Latitude == nil || *got.Latitude != lat || got.Cuisine != "Thai" || got.DeliveryRadiusKm != 7.5 {
		t.Errorf("read back = %+v, %v", got, err)
	}
	if list, _ := db.ListRestaurants(ctx, true); len(list) == 0 {
		t.Error("the list lost the restaurants")
	}
	// the name and the open flag are untouched by placing
	if r.Name != "Geo Kitchen" || !r.Active {
		t.Errorf("placing changed the name or the flag: %+v", r)
	}
	// clearing
	r, err = db.SetRestaurantPlace(ctx, "geo-r1", ports.Place{DeliveryRadiusKm: 5})
	if err != nil || r.Latitude != nil || r.Longitude != nil || r.Cuisine != "" {
		t.Errorf("cleared = %+v, %v", r, err)
	}
}

func TestAPlaceThatCannotBeIsRefused(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	aRestaurant(t, db, "geo-r1", "Geo Kitchen", true)
	one, big, bad := 1.0, 181.0, 91.0
	for name, p := range map[string]ports.Place{
		"latitude alone":    {Latitude: &one, DeliveryRadiusKm: 5},
		"longitude alone":   {Longitude: &one, DeliveryRadiusKm: 5},
		"latitude too big":  {Latitude: &bad, Longitude: &one, DeliveryRadiusKm: 5},
		"longitude too big": {Latitude: &one, Longitude: &big, DeliveryRadiusKm: 5},
		"no radius":         {DeliveryRadiusKm: 0},
		"negative radius":   {DeliveryRadiusKm: -1},
		"radius too big":    {DeliveryRadiusKm: domain.MaxDeliveryRadiusKm + 1},
		"not a number":      {DeliveryRadiusKm: math.NaN()},
	} {
		if _, err := db.SetRestaurantPlace(ctx, "geo-r1", p); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := db.SetRestaurantPlace(ctx, "geo-nope", ports.Place{DeliveryRadiusKm: 5}); !errors.Is(err, ports.ErrRestaurantNotFound) {
		t.Errorf("a missing restaurant = %v, want ErrRestaurantNotFound", err)
	}
	// the table refuses by itself what the code would not send
	for name, sql := range map[string]string{
		"half a place":        `UPDATE restaurants SET latitude = 10 WHERE id = 'geo-r1'`,
		"latitude out":        `UPDATE restaurants SET latitude = 95, longitude = 10 WHERE id = 'geo-r1'`,
		"longitude out":       `UPDATE restaurants SET latitude = 10, longitude = 190 WHERE id = 'geo-r1'`,
		"a radius of nothing": `UPDATE restaurants SET delivery_radius_km = 0 WHERE id = 'geo-r1'`,
		"a radius too big":    `UPDATE restaurants SET delivery_radius_km = 101 WHERE id = 'geo-r1'`,
	} {
		if _, err := db.Pool.Exec(ctx, sql); err == nil {
			t.Errorf("the table accepted %s", name)
		}
	}
}

func TestAVegetarianFlagIsKeptAndPatched(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	aRestaurant(t, db, "geo-r1", "Geo Kitchen", true)
	m, err := db.CreateMenuItem(ctx, domain.MenuItem{ID: "geo-m1", RestaurantID: "geo-r1", Name: "Paneer", PriceCents: 500, Available: true, Veg: true})
	if err != nil || !m.Veg {
		t.Fatalf("created = %+v, %v, want vegetarian", m, err)
	}
	other, _ := db.CreateMenuItem(ctx, domain.MenuItem{ID: "geo-m2", RestaurantID: "geo-r1", Name: "Chicken", PriceCents: 600, Available: true})
	if other.Veg {
		t.Error("an item created with no flag is vegetarian")
	}
	off, on := false, true
	if m, err = db.UpdateMenuItem(ctx, "geo-m1", ports.MenuItemPatch{Veg: &off}); err != nil || m.Veg || m.PriceCents != 500 || m.Name != "Paneer" {
		t.Errorf("veg off = %+v, %v, want only the flag changed", m, err)
	}
	if m, err = db.UpdateMenuItem(ctx, "geo-m1", ports.MenuItemPatch{Veg: &on}); err != nil || !m.Veg {
		t.Errorf("veg on = %+v, %v", m, err)
	}
	// a patch that does not mention it leaves it
	price := int64(550)
	if m, err = db.UpdateMenuItem(ctx, "geo-m1", ports.MenuItemPatch{PriceCents: &price}); err != nil || !m.Veg {
		t.Errorf("a price change = %+v, %v, want it still vegetarian", m, err)
	}
	got, _ := db.GetMenuItems(ctx, []string{"geo-m1"})
	list, _ := db.ListMenuItems(ctx, "geo-r1", true)
	if len(got) != 1 || !got[0].Veg || len(list) != 2 {
		t.Errorf("read back = %+v, listed %d", got, len(list))
	}
}

// ---- searching -----------------------------------------------------------------

func TestSearchRanksTheItemsOwnNameAboveItsRestaurantAboveItsDescription(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	aRestaurant(t, db, "geo-a", "Alpha Place", true)
	anItem(t, db, "geo-a1", "geo-a", "Quokka burger", 900, true)
	aRestaurant(t, db, "geo-b", "Quokka Diner", true)
	anItem(t, db, "geo-b1", "geo-b", "Plain rice", 300, true)
	aRestaurant(t, db, "geo-c", "Charlie Place", true)
	if _, err := db.CreateMenuItem(ctx, domain.MenuItem{ID: "geo-c1", RestaurantID: "geo-c", Name: "House special", Description: "with quokka sauce", PriceCents: 700, Available: true}); err != nil {
		t.Fatal(err)
	}
	aRestaurant(t, db, "geo-d", "Delta Place", true)
	place(t, db, "geo-d", "Quokka fusion", 1, 1, 5)
	anItem(t, db, "geo-d1", "geo-d", "Noodles", 400, true)

	hits, err := db.Search(ctx, ports.SearchQuery{Text: "quokka"})
	if err != nil {
		t.Fatal(err)
	}
	// the item's own name (1.0), then the restaurants whose name or cuisine match (0.8, by name), then the description (0.6)
	if got := joined(hitNames(hits)); got != "Alpha Place | Delta Place | Quokka Diner | Charlie Place" {
		t.Errorf("order = %s, want the item name first, then name/cuisine matches, then the description", got)
	}
	if hits[0].DistanceKm != nil {
		t.Error("a search with no location has a distance")
	}
	if got := itemsOf(hits[1]); len(got) != 1 || got[0] != "Noodles" {
		t.Errorf("a cuisine match shows the restaurant's items: %v", got)
	}
	// capitals and surrounding spaces do not matter
	hits, _ = db.Search(ctx, ports.SearchQuery{Text: "  QUOKKA  "})
	if len(hits) != 4 {
		t.Errorf("QUOKKA found %v", hitNames(hits))
	}
}

func TestSearchForgivesATypingMistake(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	aRestaurant(t, db, "geo-a", "Alpha Place", true)
	anItem(t, db, "geo-a1", "geo-a", "Quokkabiryani", 900, true)
	for _, typed := range []string{"quokkabiryni", "quokabiryani", "quokkabiryanii"} {
		hits, err := db.Search(ctx, ports.SearchQuery{Text: typed})
		if err != nil || len(hits) != 1 || hits[0].Restaurant.Name != "Alpha Place" {
			t.Errorf("%q found %v, %v, want Quokkabiryani", typed, hitNames(hits), err)
		}
	}
	if hits, _ := db.Search(ctx, ports.SearchQuery{Text: "zzzzqqqq"}); len(hits) != 0 {
		t.Errorf("a word that is nothing like anything found %v", hitNames(hits))
	}
}

func TestLikePatternEscapesWhatLikeWouldTakeForAWildcard(t *testing.T) {
	for text, want := range map[string]string{
		"tea":    "%tea%",
		"50%":    `%50\%%`,
		"a_b":    `%a\_b%`,
		`a\b`:    `%a\\b%`,
		`%_\`:    `%\%\_\\%`,
		"":       "%%",
		"naïve ": "%naïve %",
	} {
		if got := likePattern(text); got != want {
			t.Errorf("likePattern(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestSearchNearSomeoneUsesTheDeliveryRadiusAndSortsByDistance(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	// 2 km, 4 km and 9 km north of the customer; the radii are 3, 5 and 8 km, so the third is out of reach
	for i, c := range []struct {
		id     string
		km     float64
		radius float64
	}{{"geo-near", 2, 3}, {"geo-mid", 4, 5}, {"geo-far", 9, 8}} {
		aRestaurant(t, db, c.id, fmt.Sprintf("Geo %d %s", i, c.id), true)
		place(t, db, c.id, "", north(c.km), reykjavik.Lng, c.radius)
		anItem(t, db, c.id+"-i", c.id, "Fish "+c.id, 800, true)
	}
	aRestaurant(t, db, "geo-unplaced", "Geo unplaced", true)
	anItem(t, db, "geo-unplaced-i", "geo-unplaced", "Fish unplaced", 800, true)

	hits, err := db.Search(ctx, ports.SearchQuery{Near: &reykjavik})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Restaurant.ID != "geo-near" || hits[1].Restaurant.ID != "geo-mid" {
		t.Fatalf("near Reykjavik = %v, want the 2 km one then the 4 km one (the 9 km one is beyond its 8 km, the unplaced one cannot be known)", hitNames(hits))
	}
	if d := hits[0].DistanceKm; d == nil || math.Abs(*d-2) > 0.05 {
		t.Errorf("the nearest is %v km away, want about 2", d)
	}
	if d := hits[1].DistanceKm; d == nil || math.Abs(*d-4) > 0.05 {
		t.Errorf("the next is %v km away, want about 4", d)
	}
	// without a location they are all found, the unplaced one too
	hits, _ = db.Search(ctx, ports.SearchQuery{Text: "fish"})
	if len(hits) < 4 {
		t.Errorf("without a location found %v, want all four", hitNames(hits))
	}
}

func TestTheRadiusIsInclusiveAtItsEdgeAndNearestWinsAmongEqualMatches(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	aRestaurant(t, db, "geo-edge", "Geo Edge", true)
	anItem(t, db, "geo-edge-i", "geo-edge", "Fish edge", 800, true)
	place(t, db, "geo-edge", "", north(10), reykjavik.Lng, 9.9)
	if hits, _ := db.Search(ctx, ports.SearchQuery{Near: &reykjavik}); len(hits) != 0 {
		t.Errorf("10 km away with a 9.9 km radius was found: %v", hitNames(hits))
	}
	place(t, db, "geo-edge", "", north(10), reykjavik.Lng, 10.1)
	if hits, _ := db.Search(ctx, ports.SearchQuery{Near: &reykjavik}); len(hits) != 1 {
		t.Errorf("10 km away with a 10.1 km radius was not found: %v", hitNames(hits))
	}

	// a worse match that is nearer does not beat a better match that is farther
	aRestaurant(t, db, "geo-close", "Geo Close", true)
	if _, err := db.CreateMenuItem(ctx, domain.MenuItem{ID: "geo-close-i", RestaurantID: "geo-close", Name: "Plain", Description: "wombat flavour", PriceCents: 100, Available: true}); err != nil {
		t.Fatal(err)
	}
	place(t, db, "geo-close", "", north(1), reykjavik.Lng, 5)
	anItem(t, db, "geo-edge-w", "geo-edge", "Wombat stew", 100, true)
	hits, _ := db.Search(ctx, ports.SearchQuery{Text: "wombat", Near: &reykjavik})
	if got := joined(hitNames(hits)); got != "Geo Edge | Geo Close" {
		t.Errorf("order = %s, want the name match (10 km) before the description match (1 km)", got)
	}
}

func TestSearchFiltersByVegetarianPriceOpenAndAvailable(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	aRestaurant(t, db, "geo-a", "Geo Open", true)
	aRestaurant(t, db, "geo-b", "Geo Closed", false)
	place(t, db, "geo-a", "", north(1), reykjavik.Lng, 5)
	place(t, db, "geo-b", "", north(1), reykjavik.Lng, 5)
	mk := func(id, rest, name string, cents int64, available, veg bool) {
		if _, err := db.CreateMenuItem(ctx, domain.MenuItem{ID: id, RestaurantID: rest, Name: name, PriceCents: cents, Available: available, Veg: veg}); err != nil {
			t.Fatal(err)
		}
	}
	mk("geo-a1", "geo-a", "Wombat salad", 500, true, true)
	mk("geo-a2", "geo-a", "Wombat steak", 1500, true, false)
	mk("geo-a3", "geo-a", "Wombat soup", 400, false, true) // sold out
	mk("geo-b1", "geo-b", "Wombat pie", 300, true, true)   // the restaurant is closed

	find := func(q ports.SearchQuery) string {
		q.Text = "wombat"
		hits, err := db.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var all []string
		for _, h := range hits {
			all = append(all, itemsOf(h)...)
		}
		return joined(all)
	}
	if got := find(ports.SearchQuery{}); got != "Wombat salad | Wombat steak" {
		t.Errorf("everything = %s, want the open restaurant's available items only", got)
	}
	if got := find(ports.SearchQuery{VegOnly: true}); got != "Wombat salad" {
		t.Errorf("vegetarian = %s", got)
	}
	if got := find(ports.SearchQuery{MaxPriceCents: 1000}); got != "Wombat salad" {
		t.Errorf("up to 10.00 = %s", got)
	}
	if got := find(ports.SearchQuery{MaxPriceCents: 500}); got != "Wombat salad" {
		t.Errorf("up to exactly 5.00 = %s, want the 5.00 item included", got)
	}
	if got := find(ports.SearchQuery{MaxPriceCents: 499}); got != "" {
		t.Errorf("up to 4.99 = %s, want nothing", got)
	}
	if got := find(ports.SearchQuery{MaxPriceCents: 0}); got != "Wombat salad | Wombat steak" {
		t.Errorf("a limit of 0 means no limit: %s", got)
	}
	if got := find(ports.SearchQuery{VegOnly: true, MaxPriceCents: 400, Near: &reykjavik}); got != "" {
		t.Errorf("vegetarian up to 4.00, near = %s, want nothing", got)
	}
	if got := find(ports.SearchQuery{Near: &reykjavik}); got != "Wombat salad | Wombat steak" {
		t.Errorf("near = %s", got)
	}
}

func TestAnAnswerIsCutToThirtyRestaurantsOfEightItems(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	for i := 0; i < 32; i++ {
		id := fmt.Sprintf("geo-%02d", i)
		aRestaurant(t, db, id, fmt.Sprintf("Geo Cafe %02d", i), true)
		anItem(t, db, id+"-tea", id, "Wombat tea", 100, true)
	}
	for i := 0; i < 11; i++ {
		anItem(t, db, fmt.Sprintf("geo-00-x%02d", i), "geo-00", fmt.Sprintf("Wombat extra %02d", i), 100, true)
	}
	hits, err := db.Search(ctx, ports.SearchQuery{Text: "wombat"})
	if err != nil || len(hits) != ports.MaxSearchRestaurants {
		t.Fatalf("restaurants = %d, %v, want %d", len(hits), err, ports.MaxSearchRestaurants)
	}
	for _, h := range hits {
		if len(h.Items) > ports.MaxSearchItems {
			t.Errorf("%s has %d items, want at most %d", h.Restaurant.Name, len(h.Items), ports.MaxSearchItems)
		}
	}
	if len(hits[0].Items) != ports.MaxSearchItems {
		t.Errorf("Geo Cafe 00 has %d of its 12 matching items shown, want %d", len(hits[0].Items), ports.MaxSearchItems)
	}
}

func TestSearchAnswersAnEmptyListNotNull(t *testing.T) {
	db := testAdapter(t)
	hits, err := db.Search(context.Background(), ports.SearchQuery{Text: "zzzzqqqqxxxx"})
	if err != nil || hits == nil || len(hits) != 0 {
		t.Errorf("nothing found = %v, %v, want an empty list (it is sent as JSON)", hits, err)
	}
}

func TestSearchAndPlacingNeedAConnection(t *testing.T) {
	db := testAdapter(t)
	db.Pool.Close()
	ctx := context.Background()
	if _, err := db.Search(ctx, ports.SearchQuery{Text: "x"}); err == nil || errors.Is(err, ports.ErrRestaurantNotFound) {
		t.Errorf("a closed pool searching = %v, want a plain storage error", err)
	}
	if _, err := db.SetRestaurantPlace(ctx, "a", ports.Place{DeliveryRadiusKm: 5}); err == nil || errors.Is(err, ports.ErrRestaurantNotFound) {
		t.Errorf("a closed pool placing = %v, want a plain storage error", err)
	}
}
