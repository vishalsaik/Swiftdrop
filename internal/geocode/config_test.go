package geocode

import (
	"strings"
	"testing"
	"time"
)

func env(pairs ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestAddressSearchIsOffUnlessAskedFor(t *testing.T) {
	for _, mode := range []string{"", "off", "OFF", "  "} {
		g, said, err := FromEnv(env("GEOCODER", mode))
		if g != nil || err != nil || !strings.Contains(said, "off") {
			t.Errorf("GEOCODER=%q: %v, %q, %v, want off", mode, g, said, err)
		}
	}
}

func TestTheDemoListIsChosenByName(t *testing.T) {
	g, said, err := FromEnv(env("GEOCODER", "Demo"))
	if err != nil || g == nil || !strings.Contains(said, "built-in") {
		t.Fatalf("%v, %q, %v", g, said, err)
	}
	if got, _ := g.Search(ctx, "indiranagar", 5); len(got) == 0 {
		t.Error("the demo geocoder found nothing")
	}
	if _, cached := g.(*cached); !cached {
		t.Error("the demo geocoder is not behind the cache")
	}
}

func TestNominatimIsConfiguredFromTheEnvironment(t *testing.T) {
	g, said, err := FromEnv(env("GEOCODER", "nominatim", "GEOCODER_URL", "https://nominatim.example.org/", "GEOCODER_KEY", "secret-key-123",
		"GEOCODER_COUNTRY", " IN , lk ", "GEOCODER_CONTACT", "ops@example.org"))
	if err != nil {
		t.Fatal(err)
	}
	n := g.(*cached).inner.(*Nominatim)
	if n.BaseURL != "https://nominatim.example.org" || n.Key != "secret-key-123" || strings.Join(n.Countries, ",") != "in,lk" || n.UserAgent != "SwiftDrop (ops@example.org)" {
		t.Errorf("configured %+v", n)
	}
	if g.(*cached).ttl != time.Hour {
		t.Errorf("answers are kept for %v, want an hour", g.(*cached).ttl)
	}
	if strings.Contains(said, "secret-key-123") || !strings.Contains(said, "nominatim.example.org") || !strings.Contains(said, "in, lk") {
		t.Errorf("said %q: it should name the host and countries and never the key", said)
	}
	if strings.Contains(said, "public Nominatim") {
		t.Errorf("a service of your own got the warning about the public one: %q", said)
	}
}

func TestNominatimDefaultsToTheOpenStreetMapServiceAndSaysToBeCareful(t *testing.T) {
	g, said, err := FromEnv(env("GEOCODER", "nominatim"))
	if err != nil {
		t.Fatal(err)
	}
	n := g.(*cached).inner.(*Nominatim)
	if n.BaseURL != DefaultNominatimURL || n.UserAgent != "SwiftDrop" || len(n.Countries) != 0 || n.Key != "" {
		t.Errorf("defaults = %+v", n)
	}
	if !strings.Contains(said, "GEOCODER_CONTACT") || !strings.Contains(said, "own service") {
		t.Errorf("said %q, want the public service's policy mentioned", said)
	}
	// with a contact the warning about it goes away, but not the host
	_, said, _ = FromEnv(env("GEOCODER", "nominatim", "GEOCODER_CONTACT", "me@example.org"))
	if strings.Contains(said, "GEOCODER_CONTACT") || !strings.Contains(said, "nominatim.openstreetmap.org") {
		t.Errorf("said %q", said)
	}
}

func TestABadConfigurationIsRefusedAtStartup(t *testing.T) {
	for name, e := range map[string]func(string) string{
		"an unknown mode":           env("GEOCODER", "google"),
		"a URL that is not one":     env("GEOCODER", "nominatim", "GEOCODER_URL", "nominatim.example.org"),
		"a URL of the wrong kind":   env("GEOCODER", "nominatim", "GEOCODER_URL", "ftp://example.org"),
		"a URL with no host":        env("GEOCODER", "nominatim", "GEOCODER_URL", "https://"),
		"a country of one letter":   env("GEOCODER", "nominatim", "GEOCODER_COUNTRY", "i"),
		"a country of digits":       env("GEOCODER", "nominatim", "GEOCODER_COUNTRY", "12"),
		"a country list with a gap": env("GEOCODER", "nominatim", "GEOCODER_COUNTRY", "in,,lk"),
	} {
		g, _, err := FromEnv(e)
		if err == nil || g != nil {
			t.Errorf("%s was accepted: %v, %v", name, g, err)
		}
	}
}
