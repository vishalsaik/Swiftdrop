package adapters

import (
	"context"
	"errors"
	"fmt"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ ports.CatalogStore = (*DBAdapter)(nil)

const restaurantColumns = `id, name, active, cuisine, latitude, longitude, delivery_radius_km, created_at`
const menuItemColumns = `id, restaurant_id, name, description, price_cents, available, veg, created_at`

func scanRestaurant(row pgx.Row) (domain.Restaurant, error) {
	var r domain.Restaurant
	err := row.Scan(&r.ID, &r.Name, &r.Active, &r.Cuisine, &r.Latitude, &r.Longitude, &r.DeliveryRadiusKm, &r.CreatedAt)
	return r, err
}

func scanMenuItem(row pgx.Row) (domain.MenuItem, error) {
	var m domain.MenuItem
	err := row.Scan(&m.ID, &m.RestaurantID, &m.Name, &m.Description, &m.PriceCents, &m.Available, &m.Veg, &m.CreatedAt)
	return m, err
}

func (db *DBAdapter) ListRestaurants(ctx context.Context, includeInactive bool) ([]domain.Restaurant, error) {
	rows, err := db.Pool.Query(ctx, `SELECT `+restaurantColumns+` FROM restaurants WHERE ($1 OR active) ORDER BY name, id`, includeInactive)
	if err != nil {
		return nil, fmt.Errorf("failed to list restaurants: %w", err)
	}
	defer rows.Close()
	out := []domain.Restaurant{}
	for rows.Next() {
		r, err := scanRestaurant(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to list restaurants: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list restaurants: %w", err)
	}
	return out, nil
}

func (db *DBAdapter) GetRestaurant(ctx context.Context, id string) (domain.Restaurant, error) {
	r, err := scanRestaurant(db.Pool.QueryRow(ctx, `SELECT `+restaurantColumns+` FROM restaurants WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Restaurant{}, fmt.Errorf("%w: %s", ports.ErrRestaurantNotFound, id)
	}
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("failed to fetch restaurant: %w", err)
	}
	return r, nil
}

func (db *DBAdapter) CreateRestaurant(ctx context.Context, r domain.Restaurant) (domain.Restaurant, error) {
	if r.ID == "" || r.Name == "" {
		return domain.Restaurant{}, errors.New("failed to create restaurant: id and name are required")
	}
	created, err := scanRestaurant(db.Pool.QueryRow(ctx, `
		INSERT INTO restaurants (id, name, active) VALUES ($1, $2, $3) RETURNING `+restaurantColumns, r.ID, r.Name, r.Active))
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("failed to create restaurant: %w", err)
	}
	return created, nil
}

func (db *DBAdapter) UpdateRestaurant(ctx context.Context, id string, name *string, active *bool) (domain.Restaurant, error) {
	if name != nil && *name == "" {
		return domain.Restaurant{}, errors.New("failed to update restaurant: the name cannot be empty")
	}
	r, err := scanRestaurant(db.Pool.QueryRow(ctx, `
		UPDATE restaurants SET name = COALESCE($2, name), active = COALESCE($3, active)
		WHERE id = $1 RETURNING `+restaurantColumns, id, name, active))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Restaurant{}, fmt.Errorf("%w: %s", ports.ErrRestaurantNotFound, id)
	}
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("failed to update restaurant: %w", err)
	}
	return r, nil
}

func (db *DBAdapter) ListMenuItems(ctx context.Context, restaurantID string, includeUnavailable bool) ([]domain.MenuItem, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT `+menuItemColumns+` FROM menu_items WHERE restaurant_id = $1 AND ($2 OR available) ORDER BY name, id`,
		restaurantID, includeUnavailable)
	if err != nil {
		return nil, fmt.Errorf("failed to list the menu: %w", err)
	}
	defer rows.Close()
	out := []domain.MenuItem{}
	for rows.Next() {
		m, err := scanMenuItem(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to list the menu: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list the menu: %w", err)
	}
	return out, nil
}

func (db *DBAdapter) GetMenuItems(ctx context.Context, ids []string) ([]domain.MenuItem, error) {
	out := []domain.MenuItem{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.Pool.Query(ctx, `SELECT `+menuItemColumns+` FROM menu_items WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch menu items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanMenuItem(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch menu items: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to fetch menu items: %w", err)
	}
	return out, nil
}

func (db *DBAdapter) CreateMenuItem(ctx context.Context, m domain.MenuItem) (domain.MenuItem, error) {
	if m.ID == "" || m.RestaurantID == "" || m.Name == "" || m.PriceCents < 0 || m.PriceCents > domain.MaxMenuPriceCents {
		return domain.MenuItem{}, errors.New("failed to create menu item: id, restaurant and name are required and the price must be between 0 and the maximum")
	}
	created, err := scanMenuItem(db.Pool.QueryRow(ctx, `
		INSERT INTO menu_items (id, restaurant_id, name, description, price_cents, available, veg) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+menuItemColumns, m.ID, m.RestaurantID, m.Name, m.Description, m.PriceCents, m.Available, m.Veg))
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return created, nil
	case errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation:
		return domain.MenuItem{}, fmt.Errorf("%w: %s", ports.ErrRestaurantNotFound, m.RestaurantID)
	default:
		return domain.MenuItem{}, fmt.Errorf("failed to create menu item: %w", err)
	}
}

func (db *DBAdapter) UpdateMenuItem(ctx context.Context, id string, patch ports.MenuItemPatch) (domain.MenuItem, error) {
	if patch.Name != nil && *patch.Name == "" {
		return domain.MenuItem{}, errors.New("failed to update menu item: the name cannot be empty")
	}
	if patch.PriceCents != nil && (*patch.PriceCents < 0 || *patch.PriceCents > domain.MaxMenuPriceCents) {
		return domain.MenuItem{}, errors.New("failed to update menu item: the price must be between 0 and the maximum")
	}
	m, err := scanMenuItem(db.Pool.QueryRow(ctx, `
		UPDATE menu_items SET name = COALESCE($2, name), description = COALESCE($3, description),
		                      price_cents = COALESCE($4, price_cents), available = COALESCE($5, available), veg = COALESCE($6, veg)
		WHERE id = $1 RETURNING `+menuItemColumns, id, patch.Name, patch.Description, patch.PriceCents, patch.Available, patch.Veg))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.MenuItem{}, fmt.Errorf("%w: %s", ports.ErrMenuItemNotFound, id)
	}
	if err != nil {
		return domain.MenuItem{}, fmt.Errorf("failed to update menu item: %w", err)
	}
	return m, nil
}

func (db *DBAdapter) SetRestaurantPlace(ctx context.Context, id string, place ports.Place) (domain.Restaurant, error) {
	if (place.Latitude == nil) != (place.Longitude == nil) {
		return domain.Restaurant{}, errors.New("failed to set restaurant place: latitude and longitude go together")
	}
	if place.Latitude != nil && !domain.ValidPoint(*place.Latitude, *place.Longitude) {
		return domain.Restaurant{}, errors.New("failed to set restaurant place: that is not a place on earth")
	}
	if !(place.DeliveryRadiusKm > 0 && place.DeliveryRadiusKm <= domain.MaxDeliveryRadiusKm) {
		return domain.Restaurant{}, errors.New("failed to set restaurant place: the delivery radius must be above 0 and at most the maximum")
	}
	r, err := scanRestaurant(db.Pool.QueryRow(ctx, `
		UPDATE restaurants SET cuisine = $2, latitude = $3, longitude = $4, delivery_radius_km = $5
		WHERE id = $1 RETURNING `+restaurantColumns, id, place.Cuisine, place.Latitude, place.Longitude, place.DeliveryRadiusKm))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Restaurant{}, fmt.Errorf("%w: %s", ports.ErrRestaurantNotFound, id)
	}
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("failed to set restaurant place: %w", err)
	}
	return r, nil
}
