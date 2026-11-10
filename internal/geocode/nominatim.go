package geocode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultNominatimURL is OpenStreetMap's public Nominatim. Its usage policy allows light use only:
// at most one request a second, no search-as-you-type, an identifying User-Agent, and caching. For
// real traffic point GEOCODER_URL at a Nominatim of your own or at a service that speaks the same
// protocol. Results come from OpenStreetMap, so the dashboard credits "OpenStreetMap contributors".
const DefaultNominatimURL = "https://nominatim.openstreetmap.org"

// maxAnswer is the most we read of an answer; a real one is a few kilobytes.
const maxAnswer = 1 << 20

// Nominatim asks a Nominatim-compatible service.
type Nominatim struct {
	// BaseURL is the service's address with no trailing slash, such as DefaultNominatimURL.
	BaseURL string
	// UserAgent identifies this program to the service. Nominatim's policy asks for one that says
	// who to contact; GEOCODER_CONTACT adds that.
	UserAgent string
	// Countries, if set, are two-letter country codes ("in") that searches are limited to.
	Countries []string
	// Key, if set, is sent as the "key" parameter, for the paid services that speak this protocol.
	// It is a secret and is never logged.
	Key string
	// Client is the HTTP client to use; nil means one with a 3 second timeout.
	Client *http.Client
}

func (n *Nominatim) client() *http.Client {
	if n.Client != nil {
		return n.Client
	}
	return &http.Client{Timeout: 3 * time.Second}
}

// get asks for path with params and decodes the JSON answer into out. Any failure to get a good
// answer is ErrUnavailable; the cause is wrapped for the log, and carries no key.
func (n *Nominatim) get(ctx context.Context, path string, params url.Values, out any) error {
	if n.Key != "" {
		params.Set("key", n.Key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(n.BaseURL, "/")+path+"?"+params.Encode(), nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("User-Agent", n.UserAgent)
	req.Header.Set("Accept", "application/json")
	res, err := n.client().Do(req)
	if err != nil {
		// the error names the address asked, which holds the key: keep only its kind
		return fmt.Errorf("%w: the request failed (%T)", ErrUnavailable, unwrapURLError(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: the service answered %d", ErrUnavailable, res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxAnswer)).Decode(out); err != nil {
		return fmt.Errorf("%w: the answer is not JSON: %v", ErrUnavailable, err)
	}
	return nil
}

func unwrapURLError(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err
	}
	return err
}

type nominatimPlace struct {
	DisplayName string  `json:"display_name"`
	Name        string  `json:"name"`
	Lat         string  `json:"lat"`
	Lon         string  `json:"lon"`
	Address     address `json:"address"`
}

type address struct {
	HouseNumber   string `json:"house_number"`
	Road          string `json:"road"`
	Neighbourhood string `json:"neighbourhood"`
	Suburb        string `json:"suburb"`
	City          string `json:"city"`
	Town          string `json:"town"`
	Village       string `json:"village"`
	Municipality  string `json:"municipality"`
	County        string `json:"county"`
	Postcode      string `json:"postcode"`
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// candidate turns one Nominatim place into a Candidate; ok is false for one with no usable point.
func (p nominatimPlace) candidate() (Candidate, bool) {
	lat, errLat := strconv.ParseFloat(p.Lat, 64)
	lng, errLng := strconv.ParseFloat(p.Lon, 64)
	if errLat != nil || errLng != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return Candidate{}, false
	}
	a := p.Address
	street := strings.TrimSpace(a.HouseNumber + " " + a.Road)
	line1 := first(street, p.Name, strings.TrimSpace(strings.SplitN(p.DisplayName, ",", 2)[0]))
	// the area, if the street alone would not say where: "MG Road, Indiranagar"
	if area := first(a.Neighbourhood, a.Suburb); area != "" && street != "" && area != line1 {
		line1 += ", " + area
	}
	label := p.DisplayName
	if label == "" {
		label = line1
	}
	return Candidate{
		Label: label, Line1: line1,
		City:       first(a.City, a.Town, a.Village, a.Municipality, a.County),
		PostalCode: a.Postcode, Latitude: lat, Longitude: lng,
	}, true
}

// Search asks Nominatim's /search.
func (n *Nominatim) Search(ctx context.Context, query string, limit int) ([]Candidate, error) {
	params := url.Values{"q": {query}, "format": {"jsonv2"}, "addressdetails": {"1"}, "limit": {strconv.Itoa(limit)}}
	if len(n.Countries) > 0 {
		params.Set("countrycodes", strings.Join(n.Countries, ","))
	}
	var places []nominatimPlace
	if err := n.get(ctx, "/search", params, &places); err != nil {
		return nil, err
	}
	out := []Candidate{}
	for _, p := range places {
		if c, ok := p.candidate(); ok && len(out) < limit {
			out = append(out, c)
		}
	}
	return out, nil
}

// Reverse asks Nominatim's /reverse. A point with nothing to name there, which Nominatim answers
// with an "error" field, is nil and no error.
func (n *Nominatim) Reverse(ctx context.Context, lat, lng float64) (*Candidate, error) {
	params := url.Values{
		"lat": {strconv.FormatFloat(lat, 'f', 6, 64)}, "lon": {strconv.FormatFloat(lng, 'f', 6, 64)},
		"format": {"jsonv2"}, "addressdetails": {"1"},
	}
	var answer struct {
		nominatimPlace
		Error string `json:"error"`
	}
	if err := n.get(ctx, "/reverse", params, &answer); err != nil {
		return nil, err
	}
	if answer.Error != "" {
		return nil, nil
	}
	c, ok := answer.candidate()
	if !ok {
		return nil, nil
	}
	return &c, nil
}
