package geocode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

// nominatim plays the service: it records what it was asked and answers with body.
func nominatim(t *testing.T, status int, body string) (*Nominatim, *http.Request) {
	t.Helper()
	asked := &http.Request{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*asked = *r.Clone(r.Context())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Nominatim{BaseURL: srv.URL, UserAgent: "SwiftDrop (test)"}, asked
}

const twoPlaces = `[
 {"display_name":"12, MG Road, Indiranagar, Bengaluru, 560038, India","name":"","lat":"12.9719","lon":"77.6412",
  "address":{"house_number":"12","road":"MG Road","suburb":"Indiranagar","city":"Bengaluru","postcode":"560038"}},
 {"display_name":"Corner Kitchen, Whitefield, India","name":"Corner Kitchen","lat":"12.9698","lon":"77.7500",
  "address":{"suburb":"Whitefield","town":"Bengaluru East","postcode":"560066"}}
]`

func TestSearchMapsWhatNominatimAnswers(t *testing.T) {
	n, asked := nominatim(t, 200, twoPlaces)
	got, err := n.Search(ctx, "12 MG Road", 5)
	if err != nil || len(got) != 2 {
		t.Fatalf("Search = %+v, %v, want 2 candidates", got, err)
	}
	first := got[0]
	want := Candidate{
		Label: "12, MG Road, Indiranagar, Bengaluru, 560038, India", Line1: "12 MG Road, Indiranagar",
		City: "Bengaluru", PostalCode: "560038", Latitude: 12.9719, Longitude: 77.6412,
	}
	if first != want {
		t.Errorf("first = %+v, want %+v", first, want)
	}
	// no street: the place's own name, and a town when there is no city
	if got[1].Line1 != "Corner Kitchen" || got[1].City != "Bengaluru East" || got[1].PostalCode != "560066" {
		t.Errorf("second = %+v", got[1])
	}
	// what was asked
	q := asked.URL.Query()
	if asked.URL.Path != "/search" || q.Get("q") != "12 MG Road" || q.Get("format") != "jsonv2" || q.Get("addressdetails") != "1" || q.Get("limit") != "5" {
		t.Errorf("asked %s", asked.URL)
	}
	if ua := asked.Header.Get("User-Agent"); ua != "SwiftDrop (test)" {
		t.Errorf("User-Agent = %q, want the program to say who it is", ua)
	}
	if q.Has("countrycodes") || q.Has("key") {
		t.Errorf("sent a country or key nobody set: %s", asked.URL)
	}
}

func TestSearchLimitsToCountriesAndSendsAKey(t *testing.T) {
	n, asked := nominatim(t, 200, `[]`)
	n.Countries, n.Key = []string{"in", "lk"}, "secret-key-123"
	if got, err := n.Search(ctx, "tea", 3); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no places = %v, %v, want an empty list, not nil", got, err)
	}
	q := asked.URL.Query()
	if q.Get("countrycodes") != "in,lk" || q.Get("key") != "secret-key-123" {
		t.Errorf("asked %s, want the countries and the key", asked.URL)
	}
}

func TestAPlaceWithoutAUsableNameOrPointIsHandledOrLeftOut(t *testing.T) {
	n, _ := nominatim(t, 200, `[
	 {"display_name":"Some Place, Somewhere, India","lat":"1","lon":"2","address":{}},
	 {"display_name":"Indiranagar","lat":"12.97","lon":"77.64","address":{"road":"Indiranagar","suburb":"Indiranagar","village":"Hamlet"}},
	 {"display_name":"","name":"","lat":"3","lon":"4","address":{"county":"Some County"}},
	 {"display_name":"bad lat","lat":"north","lon":"2","address":{}},
	 {"display_name":"bad lon","lat":"2","lon":"east","address":{}},
	 {"display_name":"lat out","lat":"91","lon":"2","address":{}},
	 {"display_name":"lng out","lat":"2","lon":"181","address":{}},
	 {"display_name":"a fine one","lat":"5","lon":"6","address":{"road":"Road","municipality":"Muni"}}
	]`)
	got, err := n.Search(ctx, "x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d candidates %+v, want the 4 with a usable point", len(got), got)
	}
	if got[0].Line1 != "Some Place" { // no street, no name: the first part of the label
		t.Errorf("line1 = %q, want the label's first part", got[0].Line1)
	}
	if got[1].Line1 != "Indiranagar" || got[1].City != "Hamlet" { // the area is not said twice
		t.Errorf("second = %+v, want the area said once and the village as the city", got[1])
	}
	if got[2].Line1 != "" || got[2].Label != "" || got[2].City != "Some County" { // nothing to name it by, and the county as the city
		t.Errorf("third = %+v", got[2])
	}
	if got[3].Line1 != "Road" || got[3].City != "Muni" {
		t.Errorf("fourth = %+v", got[3])
	}
}

func TestSearchReturnsNoMoreThanItWasToldTo(t *testing.T) {
	n, _ := nominatim(t, 200, twoPlaces)
	got, err := n.Search(ctx, "x", 1)
	if err != nil || len(got) != 1 {
		t.Errorf("limit 1 = %+v, %v", got, err)
	}
}

func TestAServiceThatIsNotWellIsUnavailableAndNeverLeaksTheKey(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"a server error":     {500, `oops`},
		"a refusal":          {403, `{"error":"blocked"}`},
		"too many":           {429, ``},
		"not found":          {404, ``},
		"not JSON":           {200, `<html>maintenance</html>`},
		"the wrong shape":    {200, `{"this":"is not a list"}`},
		"a list for a place": {200, `[1, 2]`},
	} {
		n, _ := nominatim(t, c.status, c.body)
		n.Key = "secret-key-123"
		_, err := n.Search(ctx, "x", 5)
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v, want ErrUnavailable", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-key-123") {
			t.Errorf("%s: the error holds the key: %v", name, err)
		}
		// an object with no point is a fair answer to a reverse lookup (nothing there); a list is not
		if _, err := n.Reverse(ctx, 1, 2); !errors.Is(err, ErrUnavailable) && name != "the wrong shape" {
			t.Errorf("%s: reverse %v, want ErrUnavailable", name, err)
		}
	}
}

func TestAServiceThatCannotBeReachedIsUnavailableAndNeverLeaksTheKey(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens there now
	n := &Nominatim{BaseURL: url, UserAgent: "SwiftDrop", Key: "secret-key-123"}
	_, err := n.Search(ctx, "x", 5)
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secret-key-123") || strings.Contains(err.Error(), url) {
		t.Errorf("err = %v, want ErrUnavailable with neither the key nor the address in it", err)
	}
	if _, err := (&Nominatim{BaseURL: "://bad", UserAgent: "x"}).Search(ctx, "x", 5); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a bad address = %v, want ErrUnavailable", err)
	}
}

func TestASlowServiceIsGivenUpOn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	n := &Nominatim{BaseURL: srv.URL, UserAgent: "x", Client: &http.Client{Timeout: 50 * time.Millisecond}}
	start := time.Now()
	if _, err := n.Search(ctx, "x", 5); !errors.Is(err, ErrUnavailable) || time.Since(start) > time.Second {
		t.Errorf("a slow service: %v after %v, want ErrUnavailable quickly", err, time.Since(start))
	}
	if got := (&Nominatim{}).client().Timeout; got != 3*time.Second {
		t.Errorf("the default client waits %v, want 3s", got)
	}
}

func TestReverseNamesAPlace(t *testing.T) {
	n, asked := nominatim(t, 200, `{"display_name":"45, CMH Road, Indiranagar, Bengaluru, 560038, India","lat":"12.9719","lon":"77.6412",
		"address":{"house_number":"45","road":"CMH Road","suburb":"Indiranagar","city":"Bengaluru","postcode":"560038"}}`)
	got, err := n.Reverse(ctx, 12.97191, 77.64121)
	if err != nil || got == nil || got.Line1 != "45 CMH Road, Indiranagar" || got.City != "Bengaluru" {
		t.Fatalf("Reverse = %+v, %v", got, err)
	}
	q := asked.URL.Query()
	if asked.URL.Path != "/reverse" || q.Get("lat") != "12.971910" || q.Get("lon") != "77.641210" || q.Get("format") != "jsonv2" {
		t.Errorf("asked %s", asked.URL)
	}
}

func TestReverseHasNothingToSayWhenThereIsNothingThere(t *testing.T) {
	for name, body := range map[string]string{
		"an error from the service": `{"error":"Unable to geocode"}`,
		"a point out of range":      `{"display_name":"x","lat":"95","lon":"1","address":{}}`,
		"no point":                  `{"display_name":"x","address":{}}`,
	} {
		n, _ := nominatim(t, 200, body)
		got, err := n.Reverse(ctx, 1, 2)
		if got != nil || err != nil {
			t.Errorf("%s: %+v, %v, want nothing and no error", name, got, err)
		}
	}
}

func TestOnlyTheKindOfAFailureIsKeptNotTheAddressItWasAskedAt(t *testing.T) {
	plain := errors.New("something else")
	if unwrapURLError(plain) != plain {
		t.Error("an error that is not about an address was changed")
	}
}
