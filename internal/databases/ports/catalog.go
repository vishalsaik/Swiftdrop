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
}
