package adapters

import (
	"context"
	"errors"
	"testing"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// aRestaurant makes a restaurant and removes it, and its menu, when the test ends.
func aRestaurant(t *testing.T, db *DBAdapter, id, name string, active bool) domain.Restaurant {
	t.Helper()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), "DELETE FROM menu_items WHERE restaurant_id = $1", id)
		_, _ = db.Pool.Exec(context.Background(), "DELETE FROM restaurants WHERE id = $1", id)
	})
	r, err := db.CreateRestaurant(context.Background(), domain.Restaurant{ID: id, Name: name, Active: active})
	if err != nil {
		t.Fatalf("CreateRestaurant(%s): %v", id, err)
	}
	return r
}

func anItem(t *testing.T, db *DBAdapter, id, restaurant, name string, cents int64, available bool) domain.MenuItem {
	t.Helper()
	m, err := db.CreateMenuItem(context.Background(), domain.MenuItem{
		ID: id, RestaurantID: restaurant, Name: name, Description: name + " description", PriceCents: cents, Available: available,
	})
	if err != nil {
		t.Fatalf("CreateMenuItem(%s): %v", id, err)
	}
	return m
}

func TestARestaurantIsKeptAndReadBack(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	made := aRestaurant(t, db, "cat-r1", "Catalog Test Kitchen", true)
	if made.ID != "cat-r1" || made.Name != "Catalog Test Kitchen" || !made.Active || made.CreatedAt.IsZero() {
		t.Errorf("created = %+v, want it back with a creation time", made)
	}
	got, err := db.GetRestaurant(ctx, "cat-r1")
	if err != nil || got != made {
		t.Errorf("GetRestaurant = %+v, %v, want what was created %+v", got, err, made)
	}
	if _, err := db.GetRestaurant(ctx, "cat-nope"); !errors.Is(err, ports.ErrRestaurantNotFound) {
		t.Errorf("a missing restaurant = %v, want ErrRestaurantNotFound", err)
	}
	if _, err := db.CreateRestaurant(ctx, domain.Restaurant{ID: "cat-r1", Name: "Again", Active: true}); err == nil {
		t.Error("the same id twice was accepted")
	}
	for name, r := range map[string]domain.Restaurant{"no id": {Name: "x"}, "no name": {ID: "cat-x"}} {
		if _, err := db.CreateRestaurant(ctx, r); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestRestaurantsAreListedByNameAndClosedOnesOnlyWhenAsked(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	aRestaurant(t, db, "cat-b", "Catalog B", true)
	aRestaurant(t, db, "cat-a", "Catalog A", true)
	aRestaurant(t, db, "cat-c", "Catalog C closed", false)

	ours := func(includeClosed bool) []string {
		list, err := db.ListRestaurants(ctx, includeClosed)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range list {
			if len(r.ID) > 4 && r.ID[:4] == "cat-" {
				ids = append(ids, r.ID)
			}
		}
		return ids
	}
	if got := ours(false); len(got) != 2 || got[0] != "cat-a" || got[1] != "cat-b" {
		t.Errorf("open restaurants = %v, want cat-a, cat-b in name order", got)
	}
	if got := ours(true); len(got) != 3 || got[0] != "cat-a" || got[1] != "cat-b" || got[2] != "cat-c" {
		t.Errorf("all restaurants = %v, want all three in name order", got)
	}
}

func TestAnUpdateChangesOnlyWhatIsSet(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	aRestaurant(t, db, "cat-r1", "Before", true)
	name, off := "After", false

	r, err := db.UpdateRestaurant(ctx, "cat-r1", &name, nil)
	if err != nil || r.Name != "After" || !r.Active {
		t.Errorf("rename = %+v, %v, want the name changed and still open", r, err)
	}
	r, err = db.UpdateRestaurant(ctx, "cat-r1", nil, &off)
	if err != nil || r.Name != "After" || r.Active {
		t.Errorf("close = %+v, %v, want closed with the name kept", r, err)
	}
	if _, err := db.UpdateRestaurant(ctx, "cat-nope", &name, nil); !errors.Is(err, ports.ErrRestaurantNotFound) {
		t.Errorf("updating a missing restaurant = %v, want ErrRestaurantNotFound", err)
	}
	empty := ""
	if _, err := db.UpdateRestaurant(ctx, "cat-r1", &empty, nil); err == nil {
		t.Error("an empty name was accepted")
	}
}

func TestMenuItemsAreKeptListedAndFiltered(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	aRestaurant(t, db, "cat-r1", "Catalog Kitchen", true)
	aRestaurant(t, db, "cat-r2", "Catalog Other", true)
	made := anItem(t, db, "cat-m-burger", "cat-r1", "Burger", 950, true)
	anItem(t, db, "cat-m-fries", "cat-r1", "Fries", 400, true)
	anItem(t, db, "cat-m-gone", "cat-r1", "Aardvark pie", 700, false)
	anItem(t, db, "cat-m-noodles", "cat-r2", "Pad thai", 1390, true)

	if made.PriceCents != 950 || made.RestaurantID != "cat-r1" || made.Description != "Burger description" || !made.Available || made.CreatedAt.IsZero() {
		t.Errorf("created = %+v", made)
	}
	names := func(items []domain.MenuItem) []string {
		var out []string
		for _, m := range items {
			out = append(out, m.Name)
		}
		return out
	}
	open, err := db.ListMenuItems(ctx, "cat-r1", false)
	if err != nil || len(open) != 2 || open[0].Name != "Burger" || open[1].Name != "Fries" {
		t.Errorf("available menu = %v, %v, want Burger, Fries in name order", names(open), err)
	}
	all, err := db.ListMenuItems(ctx, "cat-r1", true)
	if err != nil || len(all) != 3 || all[0].Name != "Aardvark pie" {
		t.Errorf("whole menu = %v, %v, want three, name order", names(all), err)
	}
	none, err := db.ListMenuItems(ctx, "cat-nope", true)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("a restaurant with no menu = %v, %v, want an empty list, not nil (it is sent as JSON)", none, err)
	}

	// GetMenuItems answers what it finds, across restaurants, and nothing for ids it does not know
	found, err := db.GetMenuItems(ctx, []string{"cat-m-burger", "cat-m-noodles", "cat-m-ghost"})
	if err != nil || len(found) != 2 {
		t.Errorf("GetMenuItems = %v, %v, want the two that exist", names(found), err)
	}
	if none, err := db.GetMenuItems(ctx, nil); err != nil || none == nil || len(none) != 0 {
		t.Errorf("GetMenuItems(nil) = %v, %v, want an empty list", none, err)
	}
}

func TestAMenuItemNeedsItsRestaurantAndAPriceInRange(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	aRestaurant(t, db, "cat-r1", "Catalog Kitchen", true)

	_, err := db.CreateMenuItem(ctx, domain.MenuItem{ID: "cat-m-orphan", RestaurantID: "cat-nope", Name: "Orphan", PriceCents: 100})
	if !errors.Is(err, ports.ErrRestaurantNotFound) {
		t.Errorf("an item for a missing restaurant = %v, want ErrRestaurantNotFound", err)
	}
	for name, m := range map[string]domain.MenuItem{
		"no id":          {RestaurantID: "cat-r1", Name: "x", PriceCents: 1},
		"no restaurant":  {ID: "cat-m-x", Name: "x", PriceCents: 1},
		"no name":        {ID: "cat-m-x", RestaurantID: "cat-r1", PriceCents: 1},
		"negative price": {ID: "cat-m-x", RestaurantID: "cat-r1", Name: "x", PriceCents: -1},
		"huge price":     {ID: "cat-m-x", RestaurantID: "cat-r1", Name: "x", PriceCents: domain.MaxMenuPriceCents + 1},
	} {
		if _, err := db.CreateMenuItem(ctx, m); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// a free item and the top price are both fine
	anItem(t, db, "cat-m-free", "cat-r1", "Water", 0, true)
	anItem(t, db, "cat-m-feast", "cat-r1", "Feast", domain.MaxMenuPriceCents, true)
	if _, err := db.CreateMenuItem(ctx, domain.MenuItem{ID: "cat-m-free", RestaurantID: "cat-r1", Name: "Again", PriceCents: 1}); err == nil {
		t.Error("the same id twice was accepted")
	}

	// the database itself refuses what the code would not send
	_, err = db.Pool.Exec(ctx, `INSERT INTO menu_items (id, restaurant_id, name, price_cents) VALUES ('cat-m-bad', 'cat-r1', 'Bad', -5)`)
	if err == nil {
		t.Error("the table accepted a negative price")
	}
	_, err = db.Pool.Exec(ctx, `INSERT INTO menu_items (id, restaurant_id, name, price_cents) VALUES ('cat-m-bad', 'cat-r1', '', 5)`)
	if err == nil {
		t.Error("the table accepted an empty name")
	}
}

func TestAMenuItemPatchChangesOnlyWhatIsSet(t *testing.T) {
	db := testAdapter(t)
	ctx := context.Background()
	aRestaurant(t, db, "cat-r1", "Catalog Kitchen", true)
	anItem(t, db, "cat-m-burger", "cat-r1", "Burger", 950, true)
	price, off, on := int64(1100), false, true
	name, desc := "Double burger", "two patties"

	m, err := db.UpdateMenuItem(ctx, "cat-m-burger", ports.MenuItemPatch{PriceCents: &price})
	if err != nil || m.PriceCents != 1100 || m.Name != "Burger" || m.Description != "Burger description" || !m.Available {
		t.Errorf("price only = %+v, %v", m, err)
	}
	m, err = db.UpdateMenuItem(ctx, "cat-m-burger", ports.MenuItemPatch{Available: &off})
	if err != nil || m.Available || m.PriceCents != 1100 {
		t.Errorf("availability only = %+v, %v", m, err)
	}
	m, err = db.UpdateMenuItem(ctx, "cat-m-burger", ports.MenuItemPatch{Name: &name, Description: &desc, Available: &on})
	if err != nil || m.Name != "Double burger" || m.Description != "two patties" || !m.Available || m.PriceCents != 1100 {
		t.Errorf("name, description and availability = %+v, %v", m, err)
	}
	emptyDesc := ""
	m, err = db.UpdateMenuItem(ctx, "cat-m-burger", ports.MenuItemPatch{Description: &emptyDesc})
	if err != nil || m.Description != "" {
		t.Errorf("clearing the description = %+v, %v, want it cleared (an empty string is a value)", m, err)
	}
	m, err = db.UpdateMenuItem(ctx, "cat-m-burger", ports.MenuItemPatch{})
	if err != nil || m.Name != "Double burger" || m.PriceCents != 1100 {
		t.Errorf("an empty patch = %+v, %v, want the item unchanged", m, err)
	}

	if _, err := db.UpdateMenuItem(ctx, "cat-m-ghost", ports.MenuItemPatch{PriceCents: &price}); !errors.Is(err, ports.ErrMenuItemNotFound) {
		t.Errorf("a missing item = %v, want ErrMenuItemNotFound", err)
	}
	empty, neg, huge := "", int64(-1), int64(domain.MaxMenuPriceCents+1)
	for name, p := range map[string]ports.MenuItemPatch{"empty name": {Name: &empty}, "negative": {PriceCents: &neg}, "huge": {PriceCents: &huge}} {
		if _, err := db.UpdateMenuItem(ctx, "cat-m-burger", p); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if got, _ := db.GetMenuItems(ctx, []string{"cat-m-burger"}); len(got) != 1 || got[0].PriceCents != 1100 || got[0].Name != "Double burger" {
		t.Errorf("a refused patch altered the item: %+v", got)
	}
}

// A closed pool: every call reports a plain storage error, none is mistaken for "not found".
func TestAStorageFailureIsNotMistakenForNotFound(t *testing.T) {
	db := testAdapter(t)
	db.Pool.Close()
	ctx := context.Background()
	price, on, name := int64(1), true, "x"
	calls := map[string]func() error{
		"ListRestaurants":  func() error { _, err := db.ListRestaurants(ctx, true); return err },
		"GetRestaurant":    func() error { _, err := db.GetRestaurant(ctx, "a"); return err },
		"CreateRestaurant": func() error { _, err := db.CreateRestaurant(ctx, domain.Restaurant{ID: "a", Name: "a"}); return err },
		"UpdateRestaurant": func() error { _, err := db.UpdateRestaurant(ctx, "a", &name, &on); return err },
		"ListMenuItems":    func() error { _, err := db.ListMenuItems(ctx, "a", true); return err },
		"GetMenuItems":     func() error { _, err := db.GetMenuItems(ctx, []string{"a"}); return err },
		"CreateMenuItem": func() error {
			_, err := db.CreateMenuItem(ctx, domain.MenuItem{ID: "a", RestaurantID: "a", Name: "a"})
			return err
		},
		"UpdateMenuItem": func() error {
			_, err := db.UpdateMenuItem(ctx, "a", ports.MenuItemPatch{PriceCents: &price})
			return err
		},
	}
	for name, call := range calls {
		err := call()
		if err == nil || errors.Is(err, ports.ErrRestaurantNotFound) || errors.Is(err, ports.ErrMenuItemNotFound) {
			t.Errorf("%s on a closed pool = %v, want a plain storage error", name, err)
		}
	}
}
