package ports

import (
	"context"
	"errors"

	"swiftdrop/internal/domain"
)

var (
	// ErrRestaurantNotFound means there is no such restaurant.
	ErrRestaurantNotFound = errors.New("restaurant not found")
	// ErrMenuItemNotFound means there is no such menu item.
	ErrMenuItemNotFound = errors.New("menu item not found")
)

// MenuItemPatch is a change to a menu item: only the fields that are set change.
type MenuItemPatch struct {
	Name        *string
	Description *string
	PriceCents  *int64
	Available   *bool
	Veg         *bool
}

// Place is where a restaurant is and how far it delivers. Latitude and Longitude are both set or
// both nil (a restaurant that has not been placed yet is never found by a search near someone).
type Place struct {
	Cuisine          string
	Latitude         *float64
	Longitude        *float64
	DeliveryRadiusKm float64
}

// Point is a position on the earth.
type Point struct{ Lat, Lng float64 }

const (
	// MaxSearchRestaurants and MaxSearchItems bound an answer: the most restaurants returned, and
	// the most matching items shown under each.
	MaxSearchRestaurants = 30
	MaxSearchItems       = 8
)

// SearchQuery is what a customer looks for. Only open restaurants and available items are ever found.
type SearchQuery struct {
	// Text is matched against item names and descriptions and restaurant names and cuisines, with
	// some forgiveness for typing mistakes where the store can offer it. Empty means everything.
	Text string
	// Near, if set, keeps only restaurants that deliver to this point (they must have been placed,
	// and the point must be within their delivery radius) and says how far each is.
	Near *Point
	// VegOnly keeps only vegetarian items.
	VegOnly bool
	// MaxPriceCents, if above 0, keeps only items that cost no more.
	MaxPriceCents int64
}

// SearchHit is one restaurant that has something the customer looked for.
type SearchHit struct {
	Restaurant domain.Restaurant `json:"restaurant"`
	// DistanceKm is how far the restaurant is from Near; nil when the search had no location.
	DistanceKm *float64          `json:"distanceKm,omitempty"`
	Items      []domain.MenuItem `json:"items"`
}

// CatalogStore keeps the restaurants and what they sell.
type CatalogStore interface {
	// ListRestaurants returns restaurants by name. Without includeInactive, closed ones are left out.
	ListRestaurants(ctx context.Context, includeInactive bool) ([]domain.Restaurant, error)

	// GetRestaurant returns a restaurant, or ErrRestaurantNotFound.
	GetRestaurant(ctx context.Context, id string) (domain.Restaurant, error)

	// CreateRestaurant stores a new restaurant (the caller chooses the id).
	CreateRestaurant(ctx context.Context, r domain.Restaurant) (domain.Restaurant, error)

	// UpdateRestaurant changes a restaurant's name and/or whether it is open: only what is not nil.
	// ErrRestaurantNotFound if there is no such restaurant.
	UpdateRestaurant(ctx context.Context, id string, name *string, active *bool) (domain.Restaurant, error)

	// SetRestaurantPlace sets where a restaurant is, how far it delivers and its cuisine, all at
	// once. ErrRestaurantNotFound if there is no such restaurant.
	SetRestaurantPlace(ctx context.Context, id string, place Place) (domain.Restaurant, error)

	// ListMenuItems returns a restaurant's menu by name. Without includeUnavailable, what is out is left out.
	ListMenuItems(ctx context.Context, restaurantID string, includeUnavailable bool) ([]domain.MenuItem, error)

	// GetMenuItems returns the menu items with these ids, in no particular order; an id that
	// is not there is simply missing from the answer. It is how an order is priced.
	GetMenuItems(ctx context.Context, ids []string) ([]domain.MenuItem, error)

	// CreateMenuItem stores a new menu item. ErrRestaurantNotFound if its restaurant does not exist.
	CreateMenuItem(ctx context.Context, m domain.MenuItem) (domain.MenuItem, error)

	// UpdateMenuItem changes the fields of the patch that are set. ErrMenuItemNotFound if there is
	// no such item. Changing a price never changes an order already placed: an order keeps the
	// price it was placed at.
	UpdateMenuItem(ctx context.Context, id string, patch MenuItemPatch) (domain.MenuItem, error)

	// Search finds what a customer looks for, best match first, then nearest. At most
	// MaxSearchRestaurants restaurants, each with at most MaxSearchItems items.
	Search(ctx context.Context, q SearchQuery) ([]SearchHit, error)
}
