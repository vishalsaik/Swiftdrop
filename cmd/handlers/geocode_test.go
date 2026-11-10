package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"swiftdrop/internal/geocode"
)

// ---------------------------------------------------------------------------
// Address search: words to places, and a place to words.
// ---------------------------------------------------------------------------

// stubGeocoder answers what it is told to and remembers what it was asked.
type stubGeocoder struct {
	found   []geocode.Candidate
	one     *geocode.Candidate
	err     error
	queries []string
	limits  []int
	points  [][2]float64
}

func (g *stubGeocoder) Search(_ context.Context, q string, limit int) ([]geocode.Candidate, error) {
	g.queries, g.limits = append(g.queries, q), append(g.limits, limit)
	return g.found, g.err
}

func (g *stubGeocoder) Reverse(_ context.Context, lat, lng float64) (*geocode.Candidate, error) {
	g.points = append(g.points, [2]float64{lat, lng})
	return g.one, g.err
}

type candidatesBody struct {
	Candidates []geocode.Candidate `json:"candidates"`
}

type candidateBody struct {
	Candidate *geocode.Candidate `json:"candidate"`
}

var mgRoad = geocode.Candidate{Label: "12 MG Road, Indiranagar, Bengaluru 560038", Line1: "12 MG Road, Indiranagar", City: "Bengaluru", PostalCode: "560038", Latitude: 12.9719, Longitude: 77.6412}

func searchWorld(t *testing.T) (*world, *stubGeocoder) {
	t.Helper()
	w := newWorld(t)
	g := &stubGeocoder{found: []geocode.Candidate{mgRoad}, one: &mgRoad}
	w.s.Geocoder = g
	return w, g
}

func TestAddressSearchNeedsASignIn(t *testing.T) {
	w, _ := searchWorld(t)
	anon := w.browser(t)
	wantStatus(t, anon.do(http.MethodGet, "/geocode?q=mg+road", ""), http.StatusUnauthorized)
	wantStatus(t, anon.do(http.MethodGet, "/geocode/reverse?lat=1&lng=2", ""), http.StatusUnauthorized)
}

func TestAddressSearchIsOffUntilTheServerIsSetUpForIt(t *testing.T) {
	w := newWorld(t) // no geocoder
	ann := w.customer(t, "ann@example.com")
	for _, path := range []string{"/geocode?q=mg+road", "/geocode/reverse?lat=1&lng=2"} {
		rec := ann.do(http.MethodGet, path, "")
		wantStatus(t, rec, http.StatusServiceUnavailable)
		wantErrorContains(t, rec, "not set up")
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q", path, got)
		}
	}
}

func TestTypedWordsAreSearchedAndTheCandidatesAreAnswered(t *testing.T) {
	w, g := searchWorld(t)
	ann := w.customer(t, "ann@example.com")
	rec := ann.do(http.MethodGet, "/geocode?q="+"++12+MG+Road++", "")
	wantStatus(t, rec, http.StatusOK)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	got := decode[candidatesBody](t, rec.Body).Candidates
	if len(got) != 1 || got[0] != mgRoad {
		t.Errorf("candidates = %+v", got)
	}
	if len(g.queries) != 1 || g.queries[0] != "12 MG Road" || g.limits[0] != 5 {
		t.Errorf("the geocoder was asked %q with limit %v, want the trimmed words and 5", g.queries, g.limits)
	}
	// nothing found is an empty list (it is sent as JSON), not null
	g.found = nil
	rec = ann.do(http.MethodGet, "/geocode?q=zzzz", "")
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"candidates":[]`) {
		t.Errorf("nothing found = %s, want an empty list", rec.Body)
	}
}

func TestTheWordsSearchedForMustBeReasonable(t *testing.T) {
	w, g := searchWorld(t)
	ann := w.customer(t, "ann@example.com")
	for name, q := range map[string]string{
		"nothing":            "",
		"two characters":     "ab",
		"blanks":             "+++++",
		"over the limit":     strings.Repeat("x", 121),
		"control characters": "ab%07cd",
	} {
		wantStatus(t, ann.do(http.MethodGet, "/geocode?q="+q, ""), http.StatusBadRequest)
		_ = name
	}
	wantStatus(t, ann.do(http.MethodGet, "/geocode", ""), http.StatusBadRequest)
	if len(g.queries) != 0 {
		t.Errorf("the geocoder was asked %q for a request that was refused", g.queries)
	}
	for _, ok := range []string{"abc", strings.Repeat("é", 120)} {
		wantStatus(t, ann.do(http.MethodGet, "/geocode?q="+ok, ""), http.StatusOK)
	}
}

func TestAPlaceIsNamed(t *testing.T) {
	w, g := searchWorld(t)
	ann := w.customer(t, "ann@example.com")
	rec := ann.do(http.MethodGet, "/geocode/reverse?lat=12.9719&lng=77.6412", "")
	wantStatus(t, rec, http.StatusOK)
	if got := decode[candidateBody](t, rec.Body).Candidate; got == nil || *got != mgRoad {
		t.Errorf("candidate = %+v", got)
	}
	if len(g.points) != 1 || g.points[0] != [2]float64{12.9719, 77.6412} {
		t.Errorf("the geocoder was asked about %v", g.points)
	}
	g.one = nil // nothing to name there
	rec = ann.do(http.MethodGet, "/geocode/reverse?lat=64.1&lng=-21.9", "")
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"candidate":null`) {
		t.Errorf("nothing there = %s, want a null candidate", rec.Body)
	}
}

func TestAPlaceToNameMustBeAPlace(t *testing.T) {
	w, g := searchWorld(t)
	ann := w.customer(t, "ann@example.com")
	for _, q := range []string{"", "?lat=1", "?lng=1", "?lat=north&lng=1", "?lat=1&lng=east", "?lat=91&lng=1", "?lat=1&lng=181", "?lat=NaN&lng=1", "?lat=Inf&lng=1"} {
		wantStatus(t, ann.do(http.MethodGet, "/geocode/reverse"+q, ""), http.StatusBadRequest)
	}
	if len(g.points) != 0 {
		t.Errorf("the geocoder was asked about %v for refused requests", g.points)
	}
	wantStatus(t, ann.do(http.MethodGet, "/geocode/reverse?lat=90&lng=-180", ""), http.StatusOK)
}

func TestWhenTheServiceIsDownTheCustomerIsToldSoAndNothingLeaks(t *testing.T) {
	w, g := searchWorld(t)
	ann := w.customer(t, "ann@example.com")
	g.err = errors.New("geocoding service unavailable: the service answered 503 for https://example.org/search?key=secret-key-123")
	g.err = errors.Join(geocode.ErrUnavailable, g.err)
	for _, path := range []string{"/geocode?q=mg+road", "/geocode/reverse?lat=1&lng=2"} {
		rec := ann.do(http.MethodGet, path, "")
		wantStatus(t, rec, http.StatusBadGateway)
		wantErrorContains(t, rec, "unavailable right now", "use your location")
		if strings.Contains(rec.Body.String(), "secret-key-123") || strings.Contains(rec.Body.String(), "example.org") {
			t.Errorf("%s leaked the service's own words: %s", path, rec.Body)
		}
	}
	g.err = errors.New("something odd")
	for _, path := range []string{"/geocode?q=mg+road", "/geocode/reverse?lat=1&lng=2"} {
		rec := ann.do(http.MethodGet, path, "")
		wantStatus(t, rec, http.StatusInternalServerError)
		if strings.Contains(rec.Body.String(), "something odd") {
			t.Errorf("%s leaked the cause: %s", path, rec.Body)
		}
	}
}

func TestEachPersonMayAskOnlySoOften(t *testing.T) {
	w, g := searchWorld(t)
	ann, bea := w.customer(t, "ann@example.com"), w.customer(t, "bea@example.com")
	for i := 0; i < 30; i++ { // searches and place lookups share one budget
		path := "/geocode?q=mg+road"
		if i%2 == 1 {
			path = "/geocode/reverse?lat=1&lng=2"
		}
		wantStatus(t, ann.do(http.MethodGet, path, ""), http.StatusOK)
	}
	rec := ann.do(http.MethodGet, "/geocode?q=mg+road", "")
	wantStatus(t, rec, http.StatusTooManyRequests)
	wantErrorContains(t, rec, "too many address searches")
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 || secs > 60 {
		t.Errorf("Retry-After = %q, want a number of seconds up to a minute", rec.Header().Get("Retry-After"))
	}
	if len(g.queries)+len(g.points) != 30 {
		t.Errorf("the geocoder was asked %d times, want 30: the refused request must not reach it", len(g.queries)+len(g.points))
	}
	wantStatus(t, bea.do(http.MethodGet, "/geocode?q=mg+road", ""), http.StatusOK) // bea's budget is her own
}

func TestWithNoLimiterTheServerStillAnswers(t *testing.T) {
	w, _ := searchWorld(t)
	w.s.GeoLimit = nil
	ann := w.customer(t, "ann@example.com")
	for i := 0; i < 40; i++ {
		wantStatus(t, ann.do(http.MethodGet, "/geocode?q=mg+road", ""), http.StatusOK)
	}
}
