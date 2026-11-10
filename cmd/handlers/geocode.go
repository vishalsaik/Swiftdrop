package handlers

import (
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"swiftdrop/internal/domain"
	"swiftdrop/internal/geocode"
)

// Address search: typed words to places, and a place to words. The words typed are sent to the
// geocoding service the server is set up with (see GEOCODER in the README), so they go to a third
// party; nothing else about the person does. Answers are cached by the geocoder, and each signed-in
// person may ask only so often.

const (
	minGeocodeQuery   = 3
	maxGeocodeQuery   = 120
	geocodeCandidates = 5
)

// askGeocoder is the part of a geocode request they all share: there must be a geocoder, and the
// person must not have asked too often. It answers the request itself and returns false if it cannot go on.
func (s *Server) askGeocoder(w http.ResponseWriter, r *http.Request) bool {
	noStore(w)
	if s.Geocoder == nil {
		writeError(w, http.StatusServiceUnavailable, "address search is not set up on this server")
		return false
	}
	if s.GeoLimit != nil {
		if ok, wait := s.GeoLimit.Allow(principalFrom(r).User.ID); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			writeError(w, http.StatusTooManyRequests, "too many address searches; wait a moment and try again")
			return false
		}
	}
	return true
}

func geocodeFailed(w http.ResponseWriter, err error) {
	// the cause is for the log; it may carry the service's own words, which are not the customer's business
	log.Printf("geocoding: %v", err)
	if errors.Is(err, geocode.ErrUnavailable) {
		writeError(w, http.StatusBadGateway, "address search is unavailable right now; use your location or try again in a moment")
		return
	}
	writeError(w, http.StatusInternalServerError, "Failed to search for the address due to an internal server error")
}

// Geocode answers GET /geocode?q=... with {"candidates": [...]}: up to 5 places that might be what
// was typed, best first. q is 3 to 120 characters.
func (s *Server) Geocode(w http.ResponseWriter, r *http.Request) {
	if !s.askGeocoder(w, r) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if n := utf8.RuneCountInString(q); n < minGeocodeQuery || n > maxGeocodeQuery || hasControl(q) {
		writeError(w, http.StatusBadRequest, "q must be 3 to 120 characters with no control characters")
		return
	}
	found, err := s.Geocoder.Search(r.Context(), q, geocodeCandidates)
	if err != nil {
		geocodeFailed(w, err)
		return
	}
	if found == nil {
		found = []geocode.Candidate{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": found})
}

// ReverseGeocode answers GET /geocode/reverse?lat=&lng= with {"candidate": {...}} for the address at
// that point, or {"candidate": null} if there is nothing to name there.
func (s *Server) ReverseGeocode(w http.ResponseWriter, r *http.Request) {
	if !s.askGeocoder(w, r) {
		return
	}
	lat, latErr := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lng, lngErr := strconv.ParseFloat(r.URL.Query().Get("lng"), 64)
	if latErr != nil || lngErr != nil || !domain.ValidPoint(lat, lng) {
		writeError(w, http.StatusBadRequest, "lat and lng are required and must be a place on earth: lat -90 to 90, lng -180 to 180")
		return
	}
	found, err := s.Geocoder.Reverse(r.Context(), lat, lng)
	if err != nil {
		geocodeFailed(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidate": found})
}
