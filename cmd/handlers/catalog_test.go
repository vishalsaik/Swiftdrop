package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// Restaurants and menus: anyone signed in reads, only staff change, and the
// prices here are the only prices an order can have.
// ---------------------------------------------------------------------------

type restaurantsBody struct {
	Restaurants []domain.Restaurant `json:"restaurants"`
}

type menuBody struct {
	Restaurant domain.Restaurant `json:"restaurant"`
	Items      []domain.MenuItem `json:"items"`
}

type fieldsBody struct {
	Fields map[string]string `json:"fields"`
}

func decode[T any](t *testing.T, rec interface{ Bytes() []byte }) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Bytes(), &v); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return v
}

// menuUnreadable finds the restaurant but cannot read its items.
type menuUnreadable struct{ *fakeDB }

func (menuUnreadable) ListMenuItems(context.Context, string, bool) ([]domain.MenuItem, error) {
	return nil, errors.New("menu locked")
}

func TestTheCatalogNeedsASignIn(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	anon := w.browser(t)
	for _, route := range [][2]string{
		{http.MethodGet, "/restaurants"},
		{http.MethodGet, "/restaurants/r1/menu"},
		{http.MethodPost, "/restaurants"},
		{http.MethodPatch, "/restaurants/r1"},
		{http.MethodPost, "/restaurants/r1/menu"},
		{http.MethodPatch, "/menu/x"},
	} {
		wantStatus(t, anon.do(route[0], route[1], `{}`), http.StatusUnauthorized)
	}
}

func TestOnlyStaffChangeTheCatalog(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	w.db.Add("meal", "r1", "Meal", 500)
	ann := w.customer(t, "ann@example.com")
	for _, route := range [][3]string{
		{http.MethodPost, "/restaurants", `{"name":"New place"}`},
		{http.MethodPatch, "/restaurants/r1", `{"active":false}`},
		{http.MethodPost, "/restaurants/r1/menu", `{"name":"Soup","priceCents":300}`},
		{http.MethodPatch, "/menu/meal", `{"priceCents":1}`},
	} {
		wantStatus(t, ann.do(route[0], route[1], route[2]), http.StatusForbidden)
	}
	if len(w.db.Restaurants) != 1 || !w.db.Restaurants["r1"].Active || w.db.Items["meal"].PriceCents != 500 || len(w.db.Items) != 1 {
		t.Errorf("a customer changed the catalog: %+v %+v", w.db.Restaurants, w.db.Items)
	}
}

func TestChangingTheCatalogNeedsTheCSRFToken(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dispatch@example.com")
	staff.noCSRF = true
	rec := staff.do(http.MethodPost, "/restaurants", `{"name":"New place"}`)
	wantStatus(t, rec, http.StatusForbidden)
	wantErrorContains(t, rec, "CSRF")
	if len(w.db.Restaurants) != 0 {
		t.Error("a restaurant was made without the token")
	}
}

func TestACustomerSeesOnlyWhatIsOpenAndAvailable(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	w.db.Open("r2", "Closed Cafe")
	closed := false
	if _, err := w.db.UpdateRestaurant(ctxBackground, "r2", nil, &closed); err != nil {
		t.Fatal(err)
	}
	w.db.Add("meal", "r1", "Meal", 500)
	w.db.Add("gone", "r1", "Sold out", 700)
	w.db.Items["gone"] = func() domain.MenuItem { m := w.db.Items["gone"]; m.Available = false; return m }()
	ann := w.customer(t, "ann@example.com")

	rec := ann.do(http.MethodGet, "/restaurants", "")
	wantStatus(t, rec, http.StatusOK)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: a menu changes under a stale page", got)
	}
	if list := decode[restaurantsBody](t, rec.Body); len(list.Restaurants) != 1 || list.Restaurants[0].ID != "r1" {
		t.Errorf("a customer's restaurants = %s, want only the open one", rec.Body)
	}
	// ?all=true is staff's: for a customer it changes nothing
	rec = ann.do(http.MethodGet, "/restaurants?all=true", "")
	if list := decode[restaurantsBody](t, rec.Body); len(list.Restaurants) != 1 {
		t.Errorf("a customer asked for all and got %s", rec.Body)
	}

	rec = ann.do(http.MethodGet, "/restaurants/r1/menu?all=true", "")
	wantStatus(t, rec, http.StatusOK)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("a menu's Cache-Control = %q, want no-store: a price or a sold-out item changes under a stale page", got)
	}
	if menu := decode[menuBody](t, rec.Body); menu.Restaurant.Name != "Corner Kitchen" || len(menu.Items) != 1 || menu.Items[0].ID != "meal" {
		t.Errorf("a customer's menu = %s, want only the available item", rec.Body)
	}
	// a closed restaurant is not found, the same as one that is not there
	wantStatus(t, ann.do(http.MethodGet, "/restaurants/r2/menu", ""), http.StatusNotFound)
	wantStatus(t, ann.do(http.MethodGet, "/restaurants/nope/menu", ""), http.StatusNotFound)
}

func TestStaffSeeEverythingWhenTheyAsk(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	w.db.Open("r2", "Closed Cafe")
	closed := false
	if _, err := w.db.UpdateRestaurant(ctxBackground, "r2", nil, &closed); err != nil {
		t.Fatal(err)
	}
	w.db.Add("meal", "r1", "Meal", 500)
	w.db.Add("gone", "r2", "Sold out", 700)
	off := false
	if _, err := w.db.UpdateMenuItem(ctxBackground, "gone", ports.MenuItemPatch{Available: &off}); err != nil {
		t.Fatal(err)
	}
	staff := w.staff(t, "dispatch@example.com")

	if list := decode[restaurantsBody](t, staff.do(http.MethodGet, "/restaurants", "").Body); len(list.Restaurants) != 1 {
		t.Errorf("staff without all=true see %+v, want the open one", list)
	}
	if list := decode[restaurantsBody](t, staff.do(http.MethodGet, "/restaurants?all=true", "").Body); len(list.Restaurants) != 2 {
		t.Errorf("staff with all=true see %+v, want both", list)
	}
	rec := staff.do(http.MethodGet, "/restaurants/r2/menu?all=true", "")
	wantStatus(t, rec, http.StatusOK)
	if menu := decode[menuBody](t, rec.Body); menu.Restaurant.Active || len(menu.Items) != 1 {
		t.Errorf("staff menu of a closed restaurant = %s, want it with its unavailable item", rec.Body)
	}
	// without all=true a closed restaurant's unavailable items stay out of staff's view too
	rec = staff.do(http.MethodGet, "/restaurants/r2/menu", "")
	wantStatus(t, rec, http.StatusOK)
	if menu := decode[menuBody](t, rec.Body); len(menu.Items) != 0 {
		t.Errorf("staff without all=true see %s", rec.Body)
	}
}

func TestAStorageFailureReadingTheCatalogIsAnInternalError(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	ann := w.customer(t, "ann@example.com")
	w.db.Catalog.Err = errors.New("connection refused")
	for _, path := range []string{"/restaurants", "/restaurants/r1/menu"} {
		rec := ann.do(http.MethodGet, path, "")
		wantStatus(t, rec, http.StatusInternalServerError)
		if strings.Contains(rec.Body.String(), "connection refused") {
			t.Errorf("%s leaked the cause: %s", path, rec.Body)
		}
	}
	// the restaurant is found but its items cannot be read
	w.db.Catalog.Err = nil
	w.s.DB = menuUnreadable{w.db}
	rec := ann.do(http.MethodGet, "/restaurants/r1/menu", "")
	wantStatus(t, rec, http.StatusInternalServerError)
}

func TestStaffOpenAndRenameARestaurant(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dispatch@example.com")
	rec := staff.do(http.MethodPost, "/restaurants", `{"name":"  Noodle Bar  "}`)
	wantStatus(t, rec, http.StatusCreated)
	var made struct {
		Restaurant domain.Restaurant `json:"restaurant"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	if made.Restaurant.ID == "" || made.Restaurant.Name != "Noodle Bar" || !made.Restaurant.Active {
		t.Fatalf("made = %+v, want an open, trimmed, identified restaurant", made.Restaurant)
	}
	id := made.Restaurant.ID

	rec = staff.do(http.MethodPatch, "/restaurants/"+id, `{"name":" Noodle House "}`)
	wantStatus(t, rec, http.StatusOK)
	if got := w.db.Restaurants[id]; got.Name != "Noodle House" || !got.Active {
		t.Errorf("after a rename = %+v, want the new name and still open", got)
	}
	wantStatus(t, staff.do(http.MethodPatch, "/restaurants/"+id, `{"active":false}`), http.StatusOK)
	if got := w.db.Restaurants[id]; got.Name != "Noodle House" || got.Active {
		t.Errorf("after closing = %+v, want closed with the name kept", got)
	}
	wantStatus(t, staff.do(http.MethodPatch, "/restaurants/nope", `{"active":false}`), http.StatusNotFound)
}

func TestARestaurantNeedsAGoodName(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dispatch@example.com")
	w.db.Open("r1", "Corner Kitchen")
	long := strings.Repeat("x", 81)
	for name, body := range map[string]string{
		"missing":       `{}`,
		"blank":         `{"name":"   "}`,
		"too long":      `{"name":"` + long + `"}`,
		"control chars": `{"name":"a\u0007b"}`,
	} {
		rec := staff.do(http.MethodPost, "/restaurants", body)
		wantStatus(t, rec, http.StatusUnprocessableEntity)
		if decode[fieldsBody](t, rec.Body).Fields["name"] == "" {
			t.Errorf("%s: name not named: %s", name, rec.Body)
		}
		rec = staff.do(http.MethodPatch, "/restaurants/r1", strings.Replace(body, "{}", `{"name":""}`, 1))
		wantStatus(t, rec, http.StatusUnprocessableEntity)
	}
	// 80 characters is fine, counted as characters and not bytes
	ok := strings.Repeat("é", 80)
	wantStatus(t, staff.do(http.MethodPost, "/restaurants", `{"name":"`+ok+`"}`), http.StatusCreated)
	// nothing to change
	rec := staff.do(http.MethodPatch, "/restaurants/r1", `{}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	// an unknown field is refused, not ignored
	wantStatus(t, staff.do(http.MethodPost, "/restaurants", `{"name":"A","price":1}`), http.StatusBadRequest)
	if len(w.db.Restaurants) != 2 {
		t.Errorf("restaurants = %d, want just r1 and the one good one", len(w.db.Restaurants))
	}
}

func TestAStorageFailureChangingARestaurantIsAnInternalError(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dispatch@example.com")
	w.db.Open("r1", "Corner Kitchen")
	w.db.Catalog.Err = errors.New("connection refused")
	for _, c := range [][3]string{
		{http.MethodPost, "/restaurants", `{"name":"A"}`},
		{http.MethodPatch, "/restaurants/r1", `{"active":false}`},
		{http.MethodPost, "/restaurants/r1/menu", `{"name":"Soup","priceCents":300}`},
		{http.MethodPatch, "/menu/x", `{"priceCents":300}`},
	} {
		rec := staff.do(c[0], c[1], c[2])
		wantStatus(t, rec, http.StatusInternalServerError)
		if strings.Contains(rec.Body.String(), "connection refused") {
			t.Errorf("%s leaked the cause: %s", c[1], rec.Body)
		}
	}
}

func TestStaffAddAndChangeMenuItems(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	staff := w.staff(t, "dispatch@example.com")

	rec := staff.do(http.MethodPost, "/restaurants/r1/menu", `{"name":" Smash burger ","description":" with cheese ","priceCents":950}`)
	wantStatus(t, rec, http.StatusCreated)
	var made struct {
		Item domain.MenuItem `json:"item"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	it := made.Item
	if it.ID == "" || it.RestaurantID != "r1" || it.Name != "Smash burger" || it.Description != "with cheese" || it.PriceCents != 950 || !it.Available {
		t.Fatalf("item = %+v, want trimmed, priced, available and in r1", it)
	}
	// a free item is a price too
	wantStatus(t, staff.do(http.MethodPost, "/restaurants/r1/menu", `{"name":"Tap water","priceCents":0}`), http.StatusCreated)

	// a patch changes only what it names
	wantStatus(t, staff.do(http.MethodPatch, "/menu/"+it.ID, `{"priceCents":1000}`), http.StatusOK)
	if got := w.db.Items[it.ID]; got.PriceCents != 1000 || got.Name != "Smash burger" || !got.Available {
		t.Errorf("after a price change = %+v", got)
	}
	wantStatus(t, staff.do(http.MethodPatch, "/menu/"+it.ID, `{"available":false,"name":" Double smash ","description":""}`), http.StatusOK)
	if got := w.db.Items[it.ID]; got.Available || got.Name != "Double smash" || got.Description != "" || got.PriceCents != 1000 {
		t.Errorf("after a second change = %+v", got)
	}
	wantStatus(t, staff.do(http.MethodPatch, "/menu/nope", `{"priceCents":1}`), http.StatusNotFound)
	// a menu item for a restaurant that is not there
	wantStatus(t, staff.do(http.MethodPost, "/restaurants/nope/menu", `{"name":"Soup","priceCents":300}`), http.StatusNotFound)
}

func TestAMenuItemNeedsAGoodNameDescriptionAndPrice(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	w.db.Add("meal", "r1", "Meal", 500)
	staff := w.staff(t, "dispatch@example.com")
	long := strings.Repeat("x", 301)
	cases := map[string]struct{ body, field string }{
		"no name":            {`{"priceCents":100}`, "name"},
		"control in name":    {`{"name":"a\u0000b","priceCents":100}`, "name"},
		"long description":   {`{"name":"A","description":"` + long + `","priceCents":100}`, "description"},
		"control in descrip": {`{"name":"A","description":"a\u0007","priceCents":100}`, "description"},
		"no price":           {`{"name":"A"}`, "priceCents"},
		"negative price":     {`{"name":"A","priceCents":-1}`, "priceCents"},
		"huge price":         {`{"name":"A","priceCents":1000001}`, "priceCents"},
	}
	for name, c := range cases {
		rec := staff.do(http.MethodPost, "/restaurants/r1/menu", c.body)
		wantStatus(t, rec, http.StatusUnprocessableEntity)
		if decode[fieldsBody](t, rec.Body).Fields[c.field] == "" {
			t.Errorf("%s: %q not named: %s", name, c.field, rec.Body)
		}
	}
	// the top price is allowed
	wantStatus(t, staff.do(http.MethodPost, "/restaurants/r1/menu", `{"name":"Feast","priceCents":1000000}`), http.StatusCreated)

	// the same rules on a change, all named at once
	rec := staff.do(http.MethodPatch, "/menu/meal", `{"name":"","description":"`+long+`","priceCents":-5}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	fields := decode[fieldsBody](t, rec.Body).Fields
	if fields["name"] == "" || fields["description"] == "" || fields["priceCents"] == "" {
		t.Errorf("fields = %v, want all three named", fields)
	}
	wantStatus(t, staff.do(http.MethodPatch, "/menu/meal", `{}`), http.StatusUnprocessableEntity)
	wantStatus(t, staff.do(http.MethodPatch, "/menu/meal", `{"colour":"red"}`), http.StatusBadRequest)
	wantStatus(t, staff.do(http.MethodPost, "/restaurants/r1/menu", `{"name":"A","priceCents":1,"colour":"red"}`), http.StatusBadRequest)
	wantStatus(t, staff.do(http.MethodPatch, "/restaurants/r1", `{"colour":"red"}`), http.StatusBadRequest)
	if got := w.db.Items["meal"]; got.Name != "Meal" || got.PriceCents != 500 {
		t.Errorf("a refused change altered the item: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Ordering: the server prices from the menu.
// ---------------------------------------------------------------------------

func menuWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	w.db.Place("r1", "", testLat, testLng, 5)
	w.db.Add("burger", "r1", "Smash burger", 950)
	w.db.Add("fries", "r1", "Fries", 400)
	w.db.Open("r2", "Noodle Bar")
	w.db.Place("r2", "", testLat, testLng, 5)
	w.db.Add("noodles", "r2", "Pad thai", 1390)
	return w
}

func orderBody(id, restaurant, items string) string {
	return `{"id":"` + id + `","restaurantId":"` + restaurant + `","paymentType":"card","deliveryAddress":` + testAddress + `,"items":` + items + `}`
}

func TestACustomerPaysTheMenuPriceWhateverTheyAsk(t *testing.T) {
	w := menuWorld(t)
	ann := w.customer(t, "ann@example.com")
	rec := ann.do(http.MethodPost, "/orders", orderBody("o1", "r1",
		`[{"menuItemId":"burger","quantity":2,"unitPriceCents":1,"name":"Free burger","sku":"FREE"},{"menuItemId":"fries","quantity":1}]`))
	wantStatus(t, rec, http.StatusCreated)
	got := w.db.orders["o1"]
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.Items[0].UnitPriceCents != 950 || got.Items[0].Name != "Smash burger" || got.Items[0].SKU != "burger" || got.Items[0].MenuItemID != "burger" {
		t.Errorf("first line = %+v, want the menu's name, sku and price", got.Items[0])
	}
	if total := got.TotalCents(); total != 2*950+400 {
		t.Errorf("total = %d, want %d", total, 2*950+400)
	}
}

func TestACustomerCannotOrderAnythingButMenuItems(t *testing.T) {
	w := menuWorld(t)
	ann := w.customer(t, "ann@example.com")
	rec := ann.do(http.MethodPost, "/orders", orderBody("o1", "r1", `[{"sku":"X","name":"Free lunch","quantity":1,"unitPriceCents":0}]`))
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	wantErrorContains(t, rec, "menuItemId is required")
	if len(w.db.orders) != 0 {
		t.Error("an order was placed from a custom line")
	}
}

func TestAnOrderThatDoesNotFitTheMenuIsRefusedInWords(t *testing.T) {
	w := menuWorld(t)
	closed, off := false, false
	w.db.Open("r3", "Closed Cafe")
	w.db.Add("latte", "r3", "Latte", 500)
	if _, err := w.db.UpdateRestaurant(ctxBackground, "r3", nil, &closed); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.UpdateMenuItem(ctxBackground, "fries", ports.MenuItemPatch{Available: &off}); err != nil {
		t.Fatal(err)
	}
	ann := w.customer(t, "ann@example.com")
	for name, c := range map[string]struct{ restaurant, items, want string }{
		"no such restaurant": {"ghost", `[{"menuItemId":"burger","quantity":1}]`, "does not exist"},
		"closed restaurant":  {"r3", `[{"menuItemId":"latte","quantity":1}]`, "Closed Cafe is closed"},
		"no such item":       {"r1", `[{"menuItemId":"nope","quantity":1}]`, "no menu item"},
		"another's item":     {"r1", `[{"menuItemId":"noodles","quantity":1}]`, "not on the menu of Corner Kitchen"},
		"unavailable item":   {"r1", `[{"menuItemId":"fries","quantity":1}]`, "Fries is not available"},
		"zero quantity":      {"r1", `[{"menuItemId":"burger","quantity":0}]`, "quantity must be greater than 0"},
		"too many of one":    {"r1", `[{"menuItemId":"burger","quantity":51}]`, "at most 50"},
	} {
		rec := ann.do(http.MethodPost, "/orders", orderBody("o-"+strings.ReplaceAll(name, " ", "-"), c.restaurant, c.items))
		wantStatus(t, rec, http.StatusUnprocessableEntity)
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %s lacks %q", name, rec.Body, c.want)
		}
	}
	if len(w.db.orders) != 0 {
		t.Errorf("orders were placed anyway: %v", w.db.orders)
	}
}

func TestTheSameItemTwiceIsOneLineOnTheOrder(t *testing.T) {
	w := menuWorld(t)
	ann := w.customer(t, "ann@example.com")
	wantStatus(t, ann.do(http.MethodPost, "/orders", orderBody("o1", "r1",
		`[{"menuItemId":"burger","quantity":2},{"menuItemId":"burger","quantity":3}]`)), http.StatusCreated)
	if items := w.db.orders["o1"].Items; len(items) != 1 || items[0].Quantity != 5 {
		t.Errorf("items = %+v, want one line of 5", items)
	}
}

// staff place orders for a named customer
func staffOrder(id, restaurant, items string) string {
	return `{"id":"` + id + `","customerId":"walk-in-1","restaurantId":"` + restaurant + `","paymentType":"card","deliveryAddress":` + testAddress + `,"items":` + items + `}`
}

func TestStaffMayEnterACustomLineAndMixItWithMenuLines(t *testing.T) {
	w := menuWorld(t)
	staff := w.staff(t, "dispatch@example.com")
	// custom lines only: no menu lookups are needed, the restaurant need not even be on the menu
	wantStatus(t, staff.do(http.MethodPost, "/orders", staffOrder("o1", "r1",
		`[{"sku":"CAKE","name":"Birthday cake","quantity":1,"unitPriceCents":4200}]`)), http.StatusCreated)
	if items := w.db.orders["o1"].Items; len(items) != 1 || items[0].UnitPriceCents != 4200 || items[0].Name != "Birthday cake" {
		t.Errorf("custom line = %+v, want it as typed", items)
	}
	// mixed: the menu line is priced by the menu, the custom one by staff
	wantStatus(t, staff.do(http.MethodPost, "/orders", staffOrder("o2", "r1",
		`[{"menuItemId":"burger","quantity":1,"unitPriceCents":1},{"sku":"CAKE","name":"Birthday cake","quantity":1,"unitPriceCents":4200}]`)), http.StatusCreated)
	items := w.db.orders["o2"].Items
	if len(items) != 2 || items[0].UnitPriceCents != 950 || items[1].UnitPriceCents != 4200 {
		t.Errorf("mixed lines = %+v", items)
	}
}

func TestAStorageFailurePricingAnOrderIsAnInternalError(t *testing.T) {
	w := menuWorld(t)
	ann := w.customer(t, "ann@example.com")
	w.db.Catalog.Err = errors.New("connection refused")
	rec := ann.do(http.MethodPost, "/orders", orderBody("o1", "r1", `[{"menuItemId":"burger","quantity":1}]`))
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
	if len(w.db.orders) != 0 {
		t.Error("an order was placed that could not be priced")
	}
}
