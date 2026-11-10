package domain

import (
	"math"
	"testing"
)

func TestDistanceBetweenKnownPlaces(t *testing.T) {
	// Bengaluru (Indiranagar) to Mumbai (Bandra) is about 840 km as the crow flies.
	got := DistanceKm(12.9719, 77.6412, 19.0596, 72.8295)
	if got < 820 || got > 860 {
		t.Errorf("Bengaluru to Mumbai = %.1f km, want about 840", got)
	}
	// One degree of latitude is about 111 km.
	if d := DistanceKm(0, 0, 1, 0); math.Abs(d-111.2) > 0.5 {
		t.Errorf("one degree of latitude = %.2f km, want about 111.2", d)
	}
}

func TestDistanceIsZeroToItselfAndSymmetric(t *testing.T) {
	if d := DistanceKm(12.97, 77.64, 12.97, 77.64); d != 0 {
		t.Errorf("a place to itself = %v, want 0", d)
	}
	a, b := DistanceKm(12.97, 77.64, 13.05, 77.55), DistanceKm(13.05, 77.55, 12.97, 77.64)
	if math.Abs(a-b) > 1e-9 {
		t.Errorf("there = %v, back = %v, want the same", a, b)
	}
}

func TestDistanceAcrossTheAntipodesAndTheDateLine(t *testing.T) {
	// opposite sides of the earth: half the circumference, about 20,015 km (and no NaN from rounding)
	if d := DistanceKm(0, 0, 0, 180); math.IsNaN(d) || math.Abs(d-20015) > 5 {
		t.Errorf("antipodes = %v, want about 20015", d)
	}
	// across the date line the short way is a few km, not the long way round
	if d := DistanceKm(0, 179.99, 0, -179.99); d > 3 {
		t.Errorf("across the date line = %.2f km, want about 2", d)
	}
}

func TestValidPoint(t *testing.T) {
	for _, ok := range [][2]float64{{0, 0}, {90, 180}, {-90, -180}, {12.97, 77.64}} {
		if !ValidPoint(ok[0], ok[1]) {
			t.Errorf("%v should be a place", ok)
		}
	}
	for _, bad := range [][2]float64{{91, 0}, {-91, 0}, {0, 181}, {0, -181}, {math.NaN(), 0}, {0, math.NaN()}, {math.Inf(1), 0}} {
		if ValidPoint(bad[0], bad[1]) {
			t.Errorf("%v should not be a place", bad)
		}
	}
}
