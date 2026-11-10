package adapters

import (
	"context"
	"fmt"
	"strings"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// likePattern is text as a pattern for ILIKE that matches it anywhere: the characters that mean
// something to LIKE (%, _ and the escape \) are escaped, so searching for "50%" finds "50%".
func likePattern(text string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(text) + "%"
}

// searchRows is the most item rows read for one search; they are then grouped by restaurant and
// cut to MaxSearchRestaurants and MaxSearchItems.
const searchRows = 600

// Search finds what a customer looks for. Matching is by substring (item name, description,
// restaurant name, cuisine) and, for an item's name, by trigram similarity too, so "biryni" finds
// "Chicken Biryani". A match on the item's own name ranks above one on its restaurant or its
// description. With a location, only restaurants that have been placed and deliver that far are
// found, and the nearest come first among equal matches.
func (db *DBAdapter) Search(ctx context.Context, q ports.SearchQuery) ([]ports.SearchHit, error) {
	text := strings.TrimSpace(q.Text)
	var lat, lng *float64
	if q.Near != nil {
		lat, lng = &q.Near.Lat, &q.Near.Lng
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT r.id, r.name, r.active, r.cuisine, r.latitude, r.longitude, r.delivery_radius_km, r.created_at,
		       d.km,
		       m.id, m.restaurant_id, m.name, m.description, m.price_cents, m.available, m.veg, m.created_at
		FROM restaurants r
		JOIN menu_items m ON m.restaurant_id = r.id
		CROSS JOIN LATERAL (
			SELECT CASE WHEN $2::float8 IS NULL OR r.latitude IS NULL THEN NULL
			       ELSE 2 * 6371 * asin(sqrt(least(1,
			            power(sin(radians((r.latitude - $2::float8) / 2)), 2)
			          + cos(radians($2::float8)) * cos(radians(r.latitude)) * power(sin(radians((r.longitude - $3::float8) / 2)), 2)))) END AS km
		) d
		CROSS JOIN LATERAL (
			SELECT CASE
			         WHEN $1 = '' THEN 0::float8
			         WHEN m.name ILIKE $7 THEN 1.0
			         WHEN r.name ILIKE $7 OR r.cuisine ILIKE $7 THEN 0.8
			         WHEN m.description ILIKE $7 THEN 0.6
			         ELSE word_similarity($1, m.name)::float8
			       END AS score
		) s
		WHERE r.active AND m.available
		  AND ($2::float8 IS NULL OR (r.latitude IS NOT NULL AND d.km <= r.delivery_radius_km))
		  AND (NOT $4 OR m.veg)
		  AND ($5::bigint <= 0 OR m.price_cents <= $5::bigint)
		  AND ($1 = '' OR s.score >= 0.45)
		ORDER BY s.score DESC, d.km NULLS LAST, r.name, r.id, m.name, m.id
		LIMIT $6`,
		text, lat, lng, q.VegOnly, q.MaxPriceCents, searchRows, likePattern(text))
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}
	defer rows.Close()

	hits := []ports.SearchHit{}
	at := map[string]int{} // restaurant id -> its place in hits, to keep the order of best match first
	for rows.Next() {
		var r domain.Restaurant
		var m domain.MenuItem
		var km *float64
		if err := rows.Scan(&r.ID, &r.Name, &r.Active, &r.Cuisine, &r.Latitude, &r.Longitude, &r.DeliveryRadiusKm, &r.CreatedAt,
			&km,
			&m.ID, &m.RestaurantID, &m.Name, &m.Description, &m.PriceCents, &m.Available, &m.Veg, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to search: %w", err)
		}
		i, seen := at[r.ID]
		if !seen {
			if len(hits) >= ports.MaxSearchRestaurants {
				continue
			}
			i = len(hits)
			at[r.ID] = i
			hits = append(hits, ports.SearchHit{Restaurant: r, DistanceKm: km, Items: []domain.MenuItem{}})
		}
		if len(hits[i].Items) < ports.MaxSearchItems {
			hits[i].Items = append(hits[i].Items, m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}
	return hits, nil
}
