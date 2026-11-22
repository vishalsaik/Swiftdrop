package dispatch

import (
	"math"
	"slices"
	"testing"
	"time"

	"swiftdrop/internal/domain"
)

// The restaurant, and a way to put a driver a known distance north of it (one degree of latitude is
// 111.195 km on the sphere DistanceKm uses).
const restLat, restLng = 12.9719, 77.6412

var t0 = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

func north(km float64) (float64, float64) { return restLat + km/111.195, restLng }

// driver is a candidate km north of the restaurant, heard from just now and idle since idle.
func driver(id string, km float64, idle time.Time) domain.Candidate {
	lat, lng := north(km)
	return domain.Candidate{DriverID: id, Latitude: lat, Longitude: lng, LocationAt: t0, IdleSince: idle}
}

func request() Request {
	return Request{Lat: restLat, Lng: restLng, RadiusKm: 8, Now: t0, MaxLocationAge: 3 * time.Minute}
}

func ids(cs []domain.Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.DriverID
	}
	return out
}

func TestNoCandidatesPickNobody(t *testing.T) {
	if c, ok := Pick(request(), nil); ok {
		t.Errorf("picked %+v from nobody", c)
	}
	if _, ok := Pick(request(), []domain.Candidate{}); ok {
		t.Error("picked from an empty list")
	}
}

func TestTheNearestDriverToTheRestaurantIsPickedWhateverTheOrder(t *testing.T) {
	cands := []domain.Candidate{
		driver("far", 6, t0), driver("near", 1, t0), driver("middle", 3, t0), driver("close", 0.4, t0),
	}
	// every rotation of the list gives the same answer: the order candidates arrive in decides nothing
	for range len(cands) {
		got, ok := Pick(request(), cands)
		if !ok || got.DriverID != "close" {
			t.Fatalf("picked %q (%v) from %v, want close", got.DriverID, ok, ids(cands))
		}
		cands = append(cands[1:], cands[0])
	}
}

func TestADriverBeyondTheRadiusIsNotOfferedTheOrder(t *testing.T) {
	cands := []domain.Candidate{driver("too-far", 8.5, t0)}
	if c, ok := Pick(request(), cands); ok {
		t.Errorf("picked %q at 8.5 km with a radius of 8", c.DriverID)
	}
	// a nearer one is picked even when a farther one waited longer
	cands = append(cands, driver("ok", 7.9, t0.Add(time.Hour)))
	if got, ok := Pick(request(), cands); !ok || got.DriverID != "ok" {
		t.Errorf("picked %q (%v), want ok", got.DriverID, ok)
	}
}

func TestADriverExactlyAtTheRadiusIsIn(t *testing.T) {
	c := driver("edge", 8, t0)
	req := request()
	req.RadiusKm = domain.DistanceKm(restLat, restLng, c.Latitude, c.Longitude) // exactly that far
	if got, ok := Pick(req, []domain.Candidate{c}); !ok || got.DriverID != "edge" {
		t.Errorf("a driver exactly %.4f km away was not picked at a radius of %.4f km", req.RadiusKm, req.RadiusKm)
	}
	req.RadiusKm = math.Nextafter(req.RadiusKm, 0) // a hair less
	if _, ok := Pick(req, []domain.Candidate{c}); ok {
		t.Error("a driver just beyond the radius was picked")
	}
}

func TestADriverWhoHasNotBeenHeardFromRecentlyIsNotOffered(t *testing.T) {
	fresh, stale := driver("fresh", 5, t0), driver("stale", 1, t0)
	stale.LocationAt = t0.Add(-3*time.Minute - time.Second)
	got, ok := Pick(request(), []domain.Candidate{stale, fresh})
	if !ok || got.DriverID != "fresh" {
		t.Errorf("picked %q (%v), want fresh: the nearer driver's point is too old", got.DriverID, ok)
	}

	exactly := driver("exactly", 1, t0)
	exactly.LocationAt = t0.Add(-3 * time.Minute)
	if got, ok := Pick(request(), []domain.Candidate{exactly}); !ok || got.DriverID != "exactly" {
		t.Error("a point exactly as old as the limit was refused")
	}

	// the check can be switched off
	req := request()
	req.MaxLocationAge = 0
	if got, ok := Pick(req, []domain.Candidate{stale}); !ok || got.DriverID != "stale" {
		t.Error("with no age limit a stale point was still refused")
	}
}

func TestADriverWhoseLocationIsNotAPlaceIsNeverPicked(t *testing.T) {
	for name, c := range map[string]domain.Candidate{
		"not a number": {DriverID: "nan", Latitude: math.NaN(), Longitude: restLng, LocationAt: t0},
		"latitude 91":  {DriverID: "lat", Latitude: 91, Longitude: restLng, LocationAt: t0},
		"longitude":    {DriverID: "lng", Latitude: restLat, Longitude: 181, LocationAt: t0},
	} {
		if got, ok := Pick(request(), []domain.Candidate{c}); ok {
			t.Errorf("%s: picked %q", name, got.DriverID)
		}
	}
}

func TestDriversWithinTenMetresAreEquallyNearAndTheOneIdleLongestGoesFirst(t *testing.T) {
	long, short := t0.Add(-30*time.Minute), t0.Add(-2*time.Minute)
	a, b := driver("nearer-but-busy-lately", 2.000, short), driver("a-few-metres-further", 2.004, long)
	// the premise: they are different distances, but the same to the nearest 10 metres
	da := domain.DistanceKm(restLat, restLng, a.Latitude, a.Longitude)
	db := domain.DistanceKm(restLat, restLng, b.Latitude, b.Longitude)
	if da >= db || math.Round(da/distanceStepKm) != math.Round(db/distanceStepKm) {
		t.Fatalf("setup: %.4f and %.4f km should differ but round to the same 10 m", da, db)
	}
	for _, cands := range [][]domain.Candidate{{a, b}, {b, a}} {
		if got, _ := Pick(request(), cands); got.DriverID != b.DriverID {
			t.Errorf("picked %q from %v, want %q: equally near, and idle longest", got.DriverID, ids(cands), b.DriverID)
		}
	}
}

func TestADriverMoreThanTenMetresNearerWinsWhateverTheIdleTime(t *testing.T) {
	long, short := t0.Add(-time.Hour), t0.Add(-time.Minute)
	nearer, further := driver("nearer", 2.000, short), driver("further", 2.025, long)
	for _, cands := range [][]domain.Candidate{{nearer, further}, {further, nearer}} {
		if got, _ := Pick(request(), cands); got.DriverID != "nearer" {
			t.Errorf("picked %q from %v, want nearer", got.DriverID, ids(cands))
		}
	}
}

func TestEquallyNearAndEquallyIdleIsDecidedByIdSoTheAnswerIsStable(t *testing.T) {
	a, b, c := driver("a", 2, t0), driver("b", 2, t0), driver("c", 2, t0)
	orders := [][]domain.Candidate{{a, b, c}, {c, b, a}, {b, c, a}, {c, a, b}}
	for _, cands := range orders {
		if got, _ := Pick(request(), slices.Clone(cands)); got.DriverID != "a" {
			t.Errorf("picked %q from %v, want a", got.DriverID, ids(cands))
		}
	}
}

func TestPickDoesNotChangeWhatItIsGiven(t *testing.T) {
	cands := []domain.Candidate{driver("z", 5, t0), driver("y", 1, t0), driver("x", 3, t0)}
	before := slices.Clone(cands)
	Pick(request(), cands)
	if !slices.Equal(ids(cands), ids(before)) {
		t.Errorf("the list was reordered: %v -> %v", ids(before), ids(cands))
	}
}
