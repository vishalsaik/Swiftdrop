// Package catalogtest is an in-memory ports.CatalogStore for tests. It follows the Postgres
// adapter's contract (restaurants and items by name, a patch changes only what is set, a menu
// item needs its restaurant), so a test against it is a test of the real behaviour.
package catalogtest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

var _ ports.CatalogStore = (*Catalog)(nil)

// Catalog keeps restaurants and menu items in memory. Err makes every call fail, to test what
// callers do when storage breaks.
type Catalog struct {
	mu          sync.Mutex
	Restaurants map[string]domain.Restaurant
	Items       map[string]domain.MenuItem
	Err         error
}

// New returns an empty Catalog.
func New() *Catalog {
	return &Catalog{Restaurants: map[string]domain.Restaurant{}, Items: map[string]domain.MenuItem{}}
}

// Open adds an open restaurant and returns it.
func (c *Catalog) Open(id, name string) domain.Restaurant {
	r := domain.Restaurant{ID: id, Name: name, Active: true, CreatedAt: time.Now()}
	c.mu.Lock()
	c.Restaurants[id] = r
	c.mu.Unlock()
	return r
}

// Add adds an available menu item to a restaurant and returns it.
func (c *Catalog) Add(id, restaurantID, name string, priceCents int64) domain.MenuItem {
	m := domain.MenuItem{ID: id, RestaurantID: restaurantID, Name: name, PriceCents: priceCents, Available: true, CreatedAt: time.Now()}
	c.mu.Lock()
	c.Items[id] = m
	c.mu.Unlock()
	return m
}

func (c *Catalog) ListRestaurants(_ context.Context, includeInactive bool) ([]domain.Restaurant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return nil, c.Err
	}
	out := []domain.Restaurant{}
	for _, r := range c.Restaurants {
		if includeInactive || r.Active {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (c *Catalog) GetRestaurant(_ context.Context, id string) (domain.Restaurant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return domain.Restaurant{}, c.Err
	}
	r, ok := c.Restaurants[id]
	if !ok {
		return domain.Restaurant{}, fmt.Errorf("%w: %s", ports.ErrRestaurantNotFound, id)
	}
	return r, nil
}

func (c *Catalog) CreateRestaurant(_ context.Context, r domain.Restaurant) (domain.Restaurant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return domain.Restaurant{}, c.Err
	}
	if r.ID == "" || r.Name == "" {
		return domain.Restaurant{}, errors.New("id and name are required")
	}
	r.CreatedAt = time.Now()
	c.Restaurants[r.ID] = r
	return r, nil
}

func (c *Catalog) UpdateRestaurant(_ context.Context, id string, name *string, active *bool) (domain.Restaurant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return domain.Restaurant{}, c.Err
	}
	r, ok := c.Restaurants[id]
	if !ok {
		return domain.Restaurant{}, fmt.Errorf("%w: %s", ports.ErrRestaurantNotFound, id)
	}
	if name != nil {
		if *name == "" {
			return domain.Restaurant{}, errors.New("the name cannot be empty")
		}
		r.Name = *name
	}
	if active != nil {
		r.Active = *active
	}
	c.Restaurants[id] = r
	return r, nil
}

func (c *Catalog) ListMenuItems(_ context.Context, restaurantID string, includeUnavailable bool) ([]domain.MenuItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return nil, c.Err
	}
	out := []domain.MenuItem{}
	for _, m := range c.Items {
		if m.RestaurantID == restaurantID && (includeUnavailable || m.Available) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (c *Catalog) GetMenuItems(_ context.Context, ids []string) ([]domain.MenuItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return nil, c.Err
	}
	out := []domain.MenuItem{}
	for _, id := range ids {
		if m, ok := c.Items[id]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (c *Catalog) CreateMenuItem(_ context.Context, m domain.MenuItem) (domain.MenuItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return domain.MenuItem{}, c.Err
	}
	if m.ID == "" || m.Name == "" || m.PriceCents < 0 || m.PriceCents > domain.MaxMenuPriceCents {
		return domain.MenuItem{}, errors.New("id and name are required and the price must be in range")
	}
	if _, ok := c.Restaurants[m.RestaurantID]; !ok {
		return domain.MenuItem{}, fmt.Errorf("%w: %s", ports.ErrRestaurantNotFound, m.RestaurantID)
	}
	m.CreatedAt = time.Now()
	c.Items[m.ID] = m
	return m, nil
}

func (c *Catalog) UpdateMenuItem(_ context.Context, id string, patch ports.MenuItemPatch) (domain.MenuItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return domain.MenuItem{}, c.Err
	}
	m, ok := c.Items[id]
	if !ok {
		return domain.MenuItem{}, fmt.Errorf("%w: %s", ports.ErrMenuItemNotFound, id)
	}
	if patch.Name != nil {
		if *patch.Name == "" {
			return domain.MenuItem{}, errors.New("the name cannot be empty")
		}
		m.Name = *patch.Name
	}
	if patch.Description != nil {
		m.Description = *patch.Description
	}
	if patch.PriceCents != nil {
		if *patch.PriceCents < 0 || *patch.PriceCents > domain.MaxMenuPriceCents {
			return domain.MenuItem{}, errors.New("the price must be in range")
		}
		m.PriceCents = *patch.PriceCents
	}
	if patch.Available != nil {
		m.Available = *patch.Available
	}
	c.Items[id] = m
	return m, nil
}
