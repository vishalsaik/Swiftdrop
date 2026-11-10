package catalog

import (
	"context"
	"errors"
	"strconv"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// Deliverer decides whether a restaurant delivers to an address. The restaurant must exist, have
// said where it is, and be no farther from the address than its delivery radius.
type Deliverer struct {
	Store ports.CatalogStore
}

// km is a distance for a person: 0.7, 12.3. One decimal.
func km(x float64) string { return strconv.FormatFloat(x, 'f', 1, 64) }

// kms says a radius and a distance so that they never read as equal when one is over the other:
// "6.0 and 6.0" would be no explanation, so then they get a second decimal.
func kms(radius, away float64) (string, string) {
	if km(radius) == km(away) {
		return strconv.FormatFloat(radius, 'f', 2, 64), strconv.FormatFloat(away, 'f', 2, 64)
	}
	return km(radius), km(away)
}

// Check returns nil if the restaurant delivers to the address, and a *Problem saying why not if it
// does not: there is no such restaurant, it has not said where it is (so nobody can say how far it
// is), or it is too far. Any other error is a failure of storage. The address must already be valid.
func (d Deliverer) Check(ctx context.Context, restaurantID string, to domain.Address) error {
	r, err := d.Store.GetRestaurant(ctx, restaurantID)
	switch {
	case errors.Is(err, ports.ErrRestaurantNotFound):
		return problem("restaurant %q does not exist", restaurantID)
	case err != nil:
		return err
	case r.Latitude == nil || r.Longitude == nil:
		return problem("%s has not said where it is yet, so it cannot take delivery orders", r.Name)
	}
	away := domain.DistanceKm(*r.Latitude, *r.Longitude, to.Latitude, to.Longitude)
	if away > r.DeliveryRadiusKm {
		radius, far := kms(r.DeliveryRadiusKm, away)
		return problem("%s delivers up to %s km and that address is %s km away", r.Name, radius, far)
	}
	return nil
}
