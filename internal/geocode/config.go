package geocode

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// FromEnv builds the geocoder the environment asks for, wrapped in a cache, and a line that says what
// it is (never holding a key). getenv is os.Getenv; it is a parameter so this can be tested.
//
//	GEOCODER          off (the default): no address search; demo: a built-in list of streets near the
//	                  demo restaurants, no network; nominatim: OpenStreetMap's Nominatim or a service
//	                  that speaks the same protocol.
//	GEOCODER_URL      for nominatim: where it is (default DefaultNominatimURL).
//	GEOCODER_KEY      for nominatim: a key, if the service wants one. A secret: set it in your own shell.
//	GEOCODER_COUNTRY  for nominatim: limit searches to these countries, two-letter codes, "in" or "in,lk".
//	GEOCODER_CONTACT  for nominatim: who to contact about this program's traffic (an email address or a
//	                  web address); Nominatim's policy asks for it, and it goes in the User-Agent.
//
// A nil Geocoder with a nil error means address search is off.
func FromEnv(getenv func(string) string) (Geocoder, string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(getenv("GEOCODER"))); mode {
	case "", "off":
		return nil, "GEOCODER not set: address search is off (customers can still use their location or a saved address)", nil
	case "demo":
		return Cached(Demo{}, time.Hour, 2000), "GEOCODER=demo: address search uses a built-in list of streets in Bengaluru and Mumbai (for trying the product)", nil
	case "nominatim":
		base := strings.TrimRight(strings.TrimSpace(getenv("GEOCODER_URL")), "/")
		if base == "" {
			base = DefaultNominatimURL
		}
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, "", fmt.Errorf("GEOCODER_URL must be an http or https address, got %q", base)
		}
		n := &Nominatim{BaseURL: base, UserAgent: "SwiftDrop"}
		if contact := strings.TrimSpace(getenv("GEOCODER_CONTACT")); contact != "" {
			n.UserAgent += " (" + contact + ")"
		}
		n.Key = strings.TrimSpace(getenv("GEOCODER_KEY"))
		if raw := strings.TrimSpace(getenv("GEOCODER_COUNTRY")); raw != "" {
			for _, code := range strings.Split(raw, ",") {
				code = strings.ToLower(strings.TrimSpace(code))
				if len(code) != 2 || code[0] < 'a' || code[0] > 'z' || code[1] < 'a' || code[1] > 'z' {
					return nil, "", fmt.Errorf("GEOCODER_COUNTRY must be two-letter country codes such as in or in,lk, got %q", raw)
				}
				n.Countries = append(n.Countries, code)
			}
		}
		said := "GEOCODER=nominatim: address search asks " + u.Host
		if len(n.Countries) > 0 {
			said += " (" + strings.Join(n.Countries, ", ") + " only)"
		}
		if base == DefaultNominatimURL && getenv("GEOCODER_CONTACT") == "" {
			said += ". The public Nominatim's policy asks for light use and a contact: set GEOCODER_CONTACT, and use your own service for real traffic"
		}
		return Cached(n, time.Hour, 2000), said, nil
	default:
		return nil, "", fmt.Errorf("GEOCODER must be off, demo or nominatim, got %q", mode)
	}
}
