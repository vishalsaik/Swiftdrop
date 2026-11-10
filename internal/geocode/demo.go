package geocode

import (
	"context"
	"sort"
	"strings"

	"swiftdrop/internal/domain"
)

// Demo is a geocoder that needs no network: a short built-in list of streets near the demo
// restaurants. It is for trying the product, and for tests and CI, where asking a real service would
// make them slow, flaky and a nuisance to its operators. It knows nothing outside the list.
type Demo struct{}

type demoPlace struct {
	line1, area, city, postal string
	lat, lng                  float64
}

// Streets in Indiranagar (Bengaluru) near the demo restaurants there, and in Bandra (Mumbai) near
// Harbour Grill. The coordinates are real places; the house numbers are made up.
var demoPlaces = []demoPlace{
	{"12 100 Feet Road", "Indiranagar", "Bengaluru", "560038", 12.9784, 77.6408},
	{"45 CMH Road", "Indiranagar", "Bengaluru", "560038", 12.9719, 77.6412},
	{"7 12th Main Road", "Indiranagar", "Bengaluru", "560008", 12.9750, 77.6410},
	{"88 Old Airport Road", "Domlur", "Bengaluru", "560071", 12.9610, 77.6387},
	{"3 Church Street", "MG Road", "Bengaluru", "560001", 12.9753, 77.6054},
	{"21 Hill Road", "Bandra West", "Mumbai", "400050", 19.0596, 72.8295},
	{"9 Linking Road", "Bandra West", "Mumbai", "400050", 19.0607, 72.8362},
	{"60 Carter Road", "Bandra West", "Mumbai", "400050", 19.0689, 72.8236},
}

func (p demoPlace) candidate() Candidate {
	return Candidate{
		Label: p.line1 + ", " + p.area + ", " + p.city + " " + p.postal,
		Line1: p.line1 + ", " + p.area, City: p.city, PostalCode: p.postal, Latitude: p.lat, Longitude: p.lng,
	}
}

// Search returns the places that contain every word typed, in the order of the list.
func (Demo) Search(_ context.Context, query string, limit int) ([]Candidate, error) {
	words := strings.Fields(strings.ToLower(query))
	out := []Candidate{}
	if len(words) == 0 {
		return out, nil
	}
	for _, p := range demoPlaces {
		hay := strings.ToLower(p.candidate().Label)
		all := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				all = false
				break
			}
		}
		if all && len(out) < limit {
			out = append(out, p.candidate())
		}
	}
	return out, nil
}

// maxDemoReverseKm is how far from a listed street a point may be and still be named by it.
const maxDemoReverseKm = 2.0

// Reverse names the nearest listed street within two kilometres, and nothing farther away.
func (Demo) Reverse(_ context.Context, lat, lng float64) (*Candidate, error) {
	near := make([]demoPlace, 0, len(demoPlaces))
	for _, p := range demoPlaces {
		if domain.DistanceKm(lat, lng, p.lat, p.lng) <= maxDemoReverseKm {
			near = append(near, p)
		}
	}
	if len(near) == 0 {
		return nil, nil
	}
	sort.Slice(near, func(i, j int) bool {
		return domain.DistanceKm(lat, lng, near[i].lat, near[i].lng) < domain.DistanceKm(lat, lng, near[j].lat, near[j].lng)
	})
	c := near[0].candidate()
	// the words are the street's, but the point is where the person is
	c.Latitude, c.Longitude = lat, lng
	return &c, nil
}
