package handlers

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/gorilla/mux"
)

// Finding food, and placing the restaurants it is found at.

// coarse rounds a coordinate to three decimals, about 110 metres: enough to say which restaurants
// deliver to someone, and not more of where they are than the question needs.
func coarse(x float64) float64 { return math.Round(x*1000) / 1000 }

// SearchFood answers GET /search?q=&lat=&lng=&veg=&maxPrice= with {"results": [...], "located": bool}.
//
// q is what to look for (item names and descriptions, restaurant names and cuisines; empty means
// everything). lat and lng, both or neither, are where the customer is: only restaurants that
// deliver there are found, each with its distance. veg=true keeps vegetarian items; maxPrice is in
// cents. Only open restaurants and available items are ever found. The position is used for this
// answer and kept nowhere, and rounded to about 110 metres first.
func (s *Server) SearchFood(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	params := r.URL.Query()
	q := ports.SearchQuery{Text: strings.TrimSpace(params.Get("q"))}
	if utf8.RuneCountInString(q.Text) > domain.MaxSearchLength || hasControl(q.Text) {
		writeError(w, http.StatusBadRequest, "q must be at most 80 characters and have no control characters")
		return
	}

	latText, lngText := params.Get("lat"), params.Get("lng")
	if latText != "" || lngText != "" {
		lat, latErr := strconv.ParseFloat(latText, 64)
		lng, lngErr := strconv.ParseFloat(lngText, 64)
		if latText == "" || lngText == "" || latErr != nil || lngErr != nil {
			writeError(w, http.StatusBadRequest, "lat and lng go together and must be numbers")
			return
		}
		if !domain.ValidPoint(lat, lng) {
			writeError(w, http.StatusBadRequest, "lat and lng must be a place on earth: lat -90 to 90, lng -180 to 180")
			return
		}
		q.Near = &ports.Point{Lat: coarse(lat), Lng: coarse(lng)}
	}

	switch params.Get("veg") {
	case "", "false":
	case "true":
		q.VegOnly = true
	default:
		writeError(w, http.StatusBadRequest, "veg must be true or false")
		return
	}
	if v := params.Get("maxPrice"); v != "" {
		cents, err := strconv.ParseInt(v, 10, 64)
		if err != nil || cents < 0 {
			writeError(w, http.StatusBadRequest, "maxPrice must be a number of cents, 0 or more")
			return
		}
		q.MaxPriceCents = cents
	}

	hits, err := s.DB.Search(r.Context(), q)
	if err != nil {
		catalogError(w, "search", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": hits, "located": q.Near != nil})
}

// SetRestaurantPlace says where a restaurant is, how far it delivers and what cuisine it serves:
// {"cuisine"?, "latitude"?, "longitude"?, "deliveryRadiusKm"?}. It replaces all four, so what is left
// out is cleared (no place, no cuisine) or reset (the radius goes back to 5 km); send latitude and
// longitude together or neither. Staff only.
func (s *Server) SetRestaurantPlace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Cuisine          string   `json:"cuisine"`
		Latitude         *float64 `json:"latitude"`
		Longitude        *float64 `json:"longitude"`
		DeliveryRadiusKm *float64 `json:"deliveryRadiusKm"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	place := ports.Place{Cuisine: strings.TrimSpace(in.Cuisine), Latitude: in.Latitude, Longitude: in.Longitude, DeliveryRadiusKm: domain.DefaultDeliveryRadiusKm}
	problems := map[string]string{}
	if utf8.RuneCountInString(place.Cuisine) > domain.MaxCuisineLength {
		problems["cuisine"] = "must be at most 40 characters"
	} else if hasControl(place.Cuisine) {
		problems["cuisine"] = "must not contain control characters"
	}
	switch {
	case (in.Latitude == nil) != (in.Longitude == nil):
		problems["latitude"] = "and longitude go together: send both or neither"
	case in.Latitude != nil && !domain.ValidPoint(*in.Latitude, *in.Longitude):
		problems["latitude"] = "and longitude must be a place on earth: latitude -90 to 90, longitude -180 to 180"
	}
	if in.DeliveryRadiusKm != nil {
		if !(*in.DeliveryRadiusKm > 0 && *in.DeliveryRadiusKm <= domain.MaxDeliveryRadiusKm) {
			problems["deliveryRadiusKm"] = "must be above 0 and at most 100"
		}
		place.DeliveryRadiusKm = *in.DeliveryRadiusKm
	}
	if len(problems) > 0 {
		writeFieldProblems(w, problems)
		return
	}
	updated, err := s.DB.SetRestaurantPlace(r.Context(), mux.Vars(r)["id"], place)
	if err != nil {
		catalogError(w, "set the restaurant's place", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restaurant": updated})
}
