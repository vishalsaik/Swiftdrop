package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// Finding food near someone, and placing the restaurants it is found at.
// ---------------------------------------------------------------------------

type searchBody struct {
	Located bool `json:"located"`
	Results []struct {
		Restaurant domain.Restaurant `json:"restaurant"`
		DistanceKm *float64          `json:"distanceKm"`
		Items      []domain.MenuItem `json:"items"`
	} `json:"results"`
}

// indiranagar is a point in Bengaluru; the two restaurants there are about a kilometre apart.
const indiranagarLat, indiranagarLng = 12.9719, 77.6412

func setItem(w *world, id string, change func(*domain.MenuItem)) {
	m := w.db.Items[id]
	change(&m)
	w.db.Items[id] = m
}

// foodWorld has restaurants in Bengaluru (two), Mumbai (one) and one that has not been placed.
func foodWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.db.Open("kitchen", "Corner Kitchen")
	w.db.Place("kitchen", "American", indiranagarLat, indiranagarLng, 6)
	w.db.Add("burger", "kitchen", "Smash burger", 950)
	w.db.Add("fries", "kitchen", "Fries", 400)
	setItem(w, "fries", func(m *domain.MenuItem) { m.Veg = true })

	w.db.Open("noodles", "Noodle Bar")
	w.db.Place("noodles", "Thai", 12.9784, 77.6408, 4)
	w.db.Add("padthai", "noodles", "Pad thai", 1390)
	w.db.Add("rolls", "noodles", "Spring rolls", 650)
	setItem(w, "rolls", func(m *domain.MenuItem) { m.Veg = true; m.Description = "crispy, with burger sauce" })

	w.db.Open("harbour", "Harbour Grill")
	w.db.Place("harbour", "North Indian", 19.0596, 72.8295, 8)
	w.db.Add("butter", "harbour", "Butter chicken", 1250)

	w.db.Open("cloud", "Cloud Kitchen") // open, but nobody has said where it is
	w.db.Add("cloudburger", "cloud", "Cloud burger", 500)
	return w
}

func (b *browser) search(t *testing.T, query string) (searchBody, *httptest.ResponseRecorder) {
	t.Helper()
	rec := b.do(http.MethodGet, "/search"+query, "")
	var out searchBody
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("not JSON: %v: %s", err, rec.Body)
		}
	}
	return out, rec
}

func names(out searchBody) []string {
	var got []string
	for _, r := range out.Results {
		got = append(got, r.Restaurant.Name)
	}
	return got
}

func itemNames(out searchBody, restaurant string) []string {
	var got []string
	for _, r := range out.Results {
		if r.Restaurant.Name == restaurant {
			for _, m := range r.Items {
				got = append(got, m.Name)
			}
		}
	}
	return got
}

func equal(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }

func near(lat, lng float64) string { return fmt.Sprintf("lat=%v&lng=%v", lat, lng) }

func TestSearchNeedsASignIn(t *testing.T) {
	w := foodWorld(t)
	wantStatus(t, w.browser(t).do(http.MethodGet, "/search?q=burger", ""), http.StatusUnauthorized)
}

func TestSearchFindsItemsAndNeverCachesTheAnswer(t *testing.T) {
	w := foodWorld(t)
	ann := w.customer(t, "ann@example.com")
	out, rec := ann.search(t, "?q=burger")
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if out.Located {
		t.Error("a search with no location says it was located")
	}
	// the item's own name first (Smash burger, Cloud burger: equal, so by restaurant name), then a
	// description (Spring rolls: "burger sauce"); an unplaced restaurant is found when there is no location
	if got := names(out); !equal(got, []string{"Cloud Kitchen", "Corner Kitchen", "Noodle Bar"}) {
		t.Errorf("restaurants = %v, want the name matches (Cloud Kitchen, Corner Kitchen) before the description match (Noodle Bar)", got)
	}
	for _, r := range out.Results {
		if r.DistanceKm != nil {
			t.Errorf("%s has a distance although no location was given", r.Restaurant.Name)
		}
	}
	// what the answer says about a restaurant
	first := out.Results[1].Restaurant
	if first.Cuisine != "American" || first.Latitude == nil || *first.Latitude != indiranagarLat || first.DeliveryRadiusKm != 6 {
		t.Errorf("restaurant in the answer = %+v, want its cuisine, place and radius", first)
	}
}

func TestSearchMatchesRestaurantNamesAndCuisinesToo(t *testing.T) {
	w := foodWorld(t)
	ann := w.customer(t, "ann@example.com")
	out, _ := ann.search(t, "?q=thai")
	if got := names(out); !equal(got, []string{"Noodle Bar"}) {
		t.Errorf("cuisine search = %v, want Noodle Bar", got)
	}
	if got := itemNames(out, "Noodle Bar"); !equal(got, []string{"Pad thai", "Spring rolls"}) {
		t.Errorf("Noodle Bar items = %v, want its whole menu for a cuisine match", got)
	}
	out, _ = ann.search(t, "?q=HARBOUR")
	if got := names(out); !equal(got, []string{"Harbour Grill"}) {
		t.Errorf("a name in capitals = %v, want Harbour Grill", got)
	}
	out, _ = ann.search(t, "?q=zzzz")
	if len(out.Results) != 0 {
		t.Errorf("nothing matches: %v", names(out))
	}
	// an empty search is everything
	out, _ = ann.search(t, "")
	if len(out.Results) != 4 {
		t.Errorf("an empty search found %v, want all four open restaurants", names(out))
	}
}

func TestSearchNearSomeoneFindsOnlyWhatDeliversThere(t *testing.T) {
	w := foodWorld(t)
	ann := w.customer(t, "ann@example.com")

	out, _ := ann.search(t, "?"+near(indiranagarLat, indiranagarLng))
	if !out.Located {
		t.Error("a search with a location does not say so")
	}
	// the two in Bengaluru, nearest first; the one in Mumbai is too far and the unplaced one cannot be known
	if got := names(out); !equal(got, []string{"Corner Kitchen", "Noodle Bar"}) {
		t.Errorf("near Indiranagar = %v, want Corner Kitchen then Noodle Bar", got)
	}
	d0, d1 := out.Results[0].DistanceKm, out.Results[1].DistanceKm
	if d0 == nil || d1 == nil || *d0 > 0.1 || *d1 < 0.5 || *d1 > 1.2 {
		t.Errorf("distances = %v, %v, want about 0 and about 0.7 km", d0, d1)
	}

	// someone in Mumbai gets the one restaurant there
	out, _ = ann.search(t, "?"+near(19.0600, 72.8300))
	if got := names(out); !equal(got, []string{"Harbour Grill"}) {
		t.Errorf("near Mumbai = %v, want Harbour Grill", got)
	}
	// someone in the middle of nowhere gets nothing, not everything
	out, rec := ann.search(t, "?"+near(0, 0))
	wantStatus(t, rec, http.StatusOK)
	if len(out.Results) != 0 || !out.Located {
		t.Errorf("at 0,0 = %v, want an empty, located answer", names(out))
	}
}

func TestTheDeliveryRadiusIsTheEdge(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r", "Edge Cafe")
	w.db.Add("tea", "r", "Tea", 100)
	// the customer is 0.1 degree of latitude (11.1 km) south of the cafe
	for _, c := range []struct {
		radius float64
		found  bool
	}{{11.0, false}, {11.2, true}, {50, true}} {
		w.db.Place("r", "", 13.0, 77.0, c.radius)
		ann := w.customer(t, fmt.Sprintf("ann%v@example.com", c.radius))
		out, _ := ann.search(t, "?"+near(12.9, 77.0))
		if (len(out.Results) == 1) != c.found {
			t.Errorf("radius %v km with the customer 11.1 km away: found = %v, want %v", c.radius, len(out.Results) == 1, c.found)
		}
	}
}

func TestAPositionIsRoundedBeforeItIsUsed(t *testing.T) {
	if coarse(12.97194) != 12.972 || coarse(-77.6412) != -77.641 || coarse(0.0004) != 0 {
		t.Errorf("coarse rounds to %v, %v, %v, want three decimals", coarse(12.97194), coarse(-77.6412), coarse(0.0004))
	}
	w := newWorld(t)
	w.db.Open("r", "Exact Cafe")
	w.db.Place("r", "", 12.972, 77.641, 5)
	w.db.Add("tea", "r", "Tea", 100)
	ann := w.customer(t, "ann@example.com")
	// sent to 5 decimals, used at 3: the distance is to the rounded point, which is exactly the cafe
	out, _ := ann.search(t, "?lat=12.97194&lng=77.64149")
	if len(out.Results) != 1 || out.Results[0].DistanceKm == nil || *out.Results[0].DistanceKm != 0 {
		t.Errorf("distance = %v, want 0: the position should have been rounded to 12.972, 77.641", out.Results)
	}
}

func TestSearchCombinesTextLocationAndFilters(t *testing.T) {
	w := foodWorld(t)
	ann := w.customer(t, "ann@example.com")
	here := near(indiranagarLat, indiranagarLng)

	out, _ := ann.search(t, "?veg=true&"+here)
	if got := itemNames(out, "Corner Kitchen"); !equal(got, []string{"Fries"}) {
		t.Errorf("vegetarian at Corner Kitchen = %v, want Fries", got)
	}
	if got := itemNames(out, "Noodle Bar"); !equal(got, []string{"Spring rolls"}) {
		t.Errorf("vegetarian at Noodle Bar = %v, want Spring rolls", got)
	}
	out, _ = ann.search(t, "?veg=false&"+here)
	if len(itemNames(out, "Corner Kitchen")) != 2 {
		t.Errorf("veg=false must not filter: %v", itemNames(out, "Corner Kitchen"))
	}

	out, _ = ann.search(t, "?maxPrice=700&"+here)
	if got := itemNames(out, "Corner Kitchen"); !equal(got, []string{"Fries"}) {
		t.Errorf("up to 7.00 at Corner Kitchen = %v, want Fries", got)
	}
	if got := itemNames(out, "Noodle Bar"); !equal(got, []string{"Spring rolls"}) {
		t.Errorf("up to 7.00 at Noodle Bar = %v, want Spring rolls", got)
	}
	out, _ = ann.search(t, "?maxPrice=400&veg=true&q=fries&"+here)
	if got := names(out); !equal(got, []string{"Corner Kitchen"}) || !equal(itemNames(out, "Corner Kitchen"), []string{"Fries"}) {
		t.Errorf("fries, vegetarian, up to 4.00, near = %v %v", got, itemNames(out, "Corner Kitchen"))
	}
	out, _ = ann.search(t, "?maxPrice=399&q=fries&"+here)
	if len(out.Results) != 0 {
		t.Errorf("fries cost exactly 4.00 and the limit is 3.99: %v", names(out))
	}
}

func TestSearchNeverFindsWhatIsClosedOrSoldOut(t *testing.T) {
	w := foodWorld(t)
	closed, off := false, false
	if _, err := w.db.UpdateRestaurant(ctxBackground, "noodles", nil, &closed); err != nil {
		t.Fatal(err)
	}
	setItem(w, "fries", func(m *domain.MenuItem) { m.Available = off })
	ann := w.customer(t, "ann@example.com")
	out, _ := ann.search(t, "?"+near(indiranagarLat, indiranagarLng))
	if got := names(out); !equal(got, []string{"Corner Kitchen"}) || !equal(itemNames(out, "Corner Kitchen"), []string{"Smash burger"}) {
		t.Errorf("found %v %v, want only the open restaurant's available item", got, itemNames(out, "Corner Kitchen"))
	}
	// staff are shown the same: this is what a customer would see
	staff := w.staff(t, "dispatch@example.com")
	out, _ = staff.search(t, "?q=pad")
	if len(out.Results) != 0 {
		t.Errorf("staff found a closed restaurant's item: %v", names(out))
	}
}

func TestAnAnswerIsBounded(t *testing.T) {
	w := newWorld(t)
	for i := 0; i < 35; i++ {
		id := fmt.Sprintf("r%02d", i)
		w.db.Open(id, fmt.Sprintf("Cafe %02d", i))
		w.db.Add(id+"-tea", id, "Tea", 100)
	}
	for i := 0; i < 12; i++ {
		w.db.Add(fmt.Sprintf("r00-extra%02d", i), "r00", fmt.Sprintf("Extra tea %02d", i), 100)
	}
	ann := w.customer(t, "ann@example.com")
	out, _ := ann.search(t, "?q=tea")
	if len(out.Results) != 30 {
		t.Errorf("restaurants = %d, want at most 30", len(out.Results))
	}
	for _, r := range out.Results {
		if len(r.Items) > 8 {
			t.Errorf("%s has %d items, want at most 8", r.Restaurant.Name, len(r.Items))
		}
	}
	if got := len(itemNames(out, "Cafe 00")); got != 8 {
		t.Errorf("Cafe 00 has %d matching items, want the best 8 of 13", got)
	}
}

func TestSearchRefusesWhatItCannotUnderstand(t *testing.T) {
	w := foodWorld(t)
	ann := w.customer(t, "ann@example.com")
	for name, query := range map[string]string{
		"a long search":      "?q=" + strings.Repeat("x", 81),
		"control characters": "?q=" + url.QueryEscape("a\x07b"),
		"lat alone":          "?lat=12.9",
		"lng alone":          "?lng=77.6",
		"lat not a number":   "?lat=north&lng=77.6",
		"lng not a number":   "?lat=12.9&lng=east",
		"lat out of range":   "?lat=91&lng=77.6",
		"lng out of range":   "?lat=12.9&lng=181",
		"not a number":       "?lat=NaN&lng=77.6",
		"infinite":           "?lat=Inf&lng=77.6",
		"veg maybe":          "?veg=maybe",
		"price not a number": "?maxPrice=cheap",
		"price negative":     "?maxPrice=-1",
		"price fractional":   "?maxPrice=9.5",
	} {
		rec := ann.do(http.MethodGet, "/search"+query, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s (%s): status %d, want 400: %s", name, query, rec.Code, rec.Body)
		}
	}
	// the limits themselves are fine
	for _, ok := range []string{"?q=" + strings.Repeat("x", 80), "?lat=90&lng=180", "?lat=-90&lng=-180", "?maxPrice=0"} {
		if rec := ann.do(http.MethodGet, "/search"+ok, ""); rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200: %s", ok, rec.Code, rec.Body)
		}
	}
}

func TestAStorageFailureSearchingIsAnInternalError(t *testing.T) {
	w := foodWorld(t)
	ann := w.customer(t, "ann@example.com")
	w.db.Catalog.Err = errors.New("connection refused")
	rec := ann.do(http.MethodGet, "/search?q=burger", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
}

// ---- placing a restaurant ---------------------------------------------------------

func TestStaffPlaceARestaurant(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	staff := w.staff(t, "dispatch@example.com")

	rec := staff.do(http.MethodPut, "/restaurants/r1/place", `{"cuisine":"  South Indian ","latitude":12.9719,"longitude":77.6412,"deliveryRadiusKm":7.5}`)
	wantStatus(t, rec, http.StatusOK)
	got := w.db.Restaurants["r1"]
	if got.Cuisine != "South Indian" || got.Latitude == nil || *got.Latitude != 12.9719 || *got.Longitude != 77.6412 || got.DeliveryRadiusKm != 7.5 {
		t.Errorf("restaurant = %+v, want the place as sent, the cuisine trimmed", got)
	}
	if !strings.Contains(rec.Body.String(), `"deliveryRadiusKm":7.5`) {
		t.Errorf("the answer lacks the radius: %s", rec.Body)
	}

	// a customer can now find it near that place
	ann := w.customer(t, "ann@example.com")
	w.db.Add("dosa", "r1", "Masala dosa", 350)
	if out, _ := ann.search(t, "?"+near(12.9719, 77.6412)); len(out.Results) != 1 {
		t.Errorf("a placed restaurant was not found near its place: %v", names(out))
	}

	// it replaces everything: what is left out is cleared, and the radius goes back to 5
	wantStatus(t, staff.do(http.MethodPut, "/restaurants/r1/place", `{}`), http.StatusOK)
	got = w.db.Restaurants["r1"]
	if got.Cuisine != "" || got.Latitude != nil || got.Longitude != nil || got.DeliveryRadiusKm != 5 {
		t.Errorf("after an empty place = %+v, want no cuisine, no place, 5 km", got)
	}
	// and an unplaced restaurant is not found near anyone
	if out, _ := ann.search(t, "?"+near(12.9719, 77.6412)); len(out.Results) != 0 {
		t.Errorf("an unplaced restaurant was found near someone: %v", names(out))
	}
	wantStatus(t, staff.do(http.MethodPut, "/restaurants/nope/place", `{}`), http.StatusNotFound)
}

func TestOnlyStaffPlaceARestaurant(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	wantStatus(t, w.browser(t).do(http.MethodPut, "/restaurants/r1/place", `{}`), http.StatusUnauthorized)
	ann := w.customer(t, "ann@example.com")
	wantStatus(t, ann.do(http.MethodPut, "/restaurants/r1/place", `{"latitude":1,"longitude":1}`), http.StatusForbidden)
	if w.db.Restaurants["r1"].Latitude != nil {
		t.Error("a customer placed a restaurant")
	}
	staff := w.staff(t, "dispatch@example.com")
	staff.noCSRF = true
	rec := staff.do(http.MethodPut, "/restaurants/r1/place", `{}`)
	wantStatus(t, rec, http.StatusForbidden)
	wantErrorContains(t, rec, "CSRF")
}

func TestAPlaceNamesWhatIsWrongWithIt(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	staff := w.staff(t, "dispatch@example.com")
	for name, c := range map[string]struct{ body, field string }{
		"latitude alone":       {`{"latitude":12.9}`, "latitude"},
		"longitude alone":      {`{"longitude":77.6}`, "latitude"},
		"latitude too big":     {`{"latitude":90.5,"longitude":0}`, "latitude"},
		"longitude too big":    {`{"latitude":0,"longitude":-180.5}`, "latitude"},
		"no radius":            {`{"deliveryRadiusKm":0}`, "deliveryRadiusKm"},
		"negative radius":      {`{"deliveryRadiusKm":-2}`, "deliveryRadiusKm"},
		"radius over the max":  {`{"deliveryRadiusKm":100.5}`, "deliveryRadiusKm"},
		"cuisine too long":     {`{"cuisine":"` + strings.Repeat("x", 41) + `"}`, "cuisine"},
		"control in a cuisine": {`{"cuisine":"a\u0007b"}`, "cuisine"},
	} {
		rec := staff.do(http.MethodPut, "/restaurants/r1/place", c.body)
		wantStatus(t, rec, http.StatusUnprocessableEntity)
		if decode[fieldsBody](t, rec.Body).Fields[c.field] == "" {
			t.Errorf("%s: %q not named: %s", name, c.field, rec.Body)
		}
	}
	wantStatus(t, staff.do(http.MethodPut, "/restaurants/r1/place", `{"colour":"red"}`), http.StatusBadRequest)
	// the edges are allowed
	for _, ok := range []string{`{"latitude":90,"longitude":180,"deliveryRadiusKm":100}`, `{"latitude":-90,"longitude":-180,"deliveryRadiusKm":0.1}`, `{"cuisine":"` + strings.Repeat("é", 40) + `"}`} {
		wantStatus(t, staff.do(http.MethodPut, "/restaurants/r1/place", ok), http.StatusOK)
	}
}

func TestAStorageFailurePlacingIsAnInternalError(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	staff := w.staff(t, "dispatch@example.com")
	w.db.Catalog.Err = errors.New("connection refused")
	rec := staff.do(http.MethodPut, "/restaurants/r1/place", `{}`)
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
}

// ---- vegetarian items --------------------------------------------------------------

func TestStaffMarkItemsVegetarian(t *testing.T) {
	w := newWorld(t)
	w.db.Open("r1", "Corner Kitchen")
	w.db.Add("burger", "r1", "Smash burger", 950)
	staff := w.staff(t, "dispatch@example.com")

	rec := staff.do(http.MethodPost, "/restaurants/r1/menu", `{"name":"Fries","priceCents":400,"veg":true}`)
	wantStatus(t, rec, http.StatusCreated)
	if !strings.Contains(rec.Body.String(), `"veg":true`) {
		t.Errorf("the new item is not vegetarian: %s", rec.Body)
	}
	rec = staff.do(http.MethodPost, "/restaurants/r1/menu", `{"name":"Wings","priceCents":600}`)
	wantStatus(t, rec, http.StatusCreated)
	if !strings.Contains(rec.Body.String(), `"veg":false`) {
		t.Errorf("an item with no veg flag should not be vegetarian: %s", rec.Body)
	}

	wantStatus(t, staff.do(http.MethodPatch, "/menu/burger", `{"veg":true}`), http.StatusOK)
	if !w.db.Items["burger"].Veg || w.db.Items["burger"].PriceCents != 950 {
		t.Errorf("burger = %+v, want vegetarian and otherwise unchanged", w.db.Items["burger"])
	}
	wantStatus(t, staff.do(http.MethodPatch, "/menu/burger", `{"veg":false}`), http.StatusOK)
	if w.db.Items["burger"].Veg {
		t.Error("veg=false did not clear the flag")
	}
	rec = staff.do(http.MethodPatch, "/menu/burger", `{}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	if !strings.Contains(rec.Body.String(), "veg") {
		t.Errorf("an empty change should say veg is one of the things to change: %s", rec.Body)
	}
}
