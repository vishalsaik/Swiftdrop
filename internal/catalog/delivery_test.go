package catalog

import (
	"errors"
	"testing"

	"swiftdrop/internal/catalog/catalogtest"
	"swiftdrop/internal/domain"
)

func at(lat, lng float64) domain.Address {
	return domain.Address{Line1: "x", Latitude: lat, Longitude: lng}
}

// north is a latitude km kilometres north of 12.9719, 77.6412 (a degree of latitude is 111.19 km).
func north(km float64) float64 { return 12.9719 + km/111.19 }

func deliverer(t *testing.T) (Deliverer, *catalogtest.Catalog) {
	t.Helper()
	c := catalogtest.New()
	c.Open("r1", "Corner Kitchen")
	c.Place("r1", "American", 12.9719, 77.6412, 6)
	return Deliverer{Store: c}, c
}

func TestARestaurantDeliversWithinItsRadius(t *testing.T) {
	d, _ := deliverer(t)
	for _, km := range []float64{0, 0.1, 3, 5.9} {
		if err := d.Check(ctx, "r1", at(north(km), 77.6412)); err != nil {
			t.Errorf("%.1f km away: %v, want it delivered", km, err)
		}
	}
}

func TestARestaurantDoesNotDeliverBeyondItsRadiusAndSaysHowFar(t *testing.T) {
	d, _ := deliverer(t)
	err := d.Check(ctx, "r1", at(north(7.4), 77.6412))
	wantProblem(t, err, "Corner Kitchen delivers up to 6.0 km", "7.4 km away")
	// a little over the edge reads as two different numbers, not "6.0 and 6.0"
	err = d.Check(ctx, "r1", at(north(6.02), 77.6412))
	wantProblem(t, err, "up to 6.00 km", "6.02 km away")
	// and a smaller radius is the one said
	_, c := deliverer(t)
	c.Place("r1", "", 12.9719, 77.6412, 1.5)
	wantProblem(t, Deliverer{Store: c}.Check(ctx, "r1", at(north(2), 77.6412)), "up to 1.5 km")
}

func TestARestaurantNotPlacedDeliversNowhere(t *testing.T) {
	c := catalogtest.New()
	c.Open("r1", "Nowhere Cafe")
	wantProblem(t, Deliverer{Store: c}.Check(ctx, "r1", at(12.9719, 77.6412)), "Nowhere Cafe has not said where it is", "cannot take delivery orders")
}

func TestARestaurantThatIsNotThereDeliversNowhere(t *testing.T) {
	d, _ := deliverer(t)
	wantProblem(t, d.Check(ctx, "ghost", at(12.9719, 77.6412)), `"ghost" does not exist`)
}

func TestAStorageFailureIsNotAProblemTheCustomerCanFixInDelivery(t *testing.T) {
	d, c := deliverer(t)
	c.Err = errors.New("connection refused")
	err := d.Check(ctx, "r1", at(12.9719, 77.6412))
	var p *Problem
	if err == nil || errors.As(err, &p) {
		t.Errorf("err = %v, want a plain storage error", err)
	}
}
