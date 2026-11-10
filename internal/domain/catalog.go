package domain

import "time"

// Limits on what a menu may hold. They keep a typo from becoming a ten-thousand-dollar burger and
// an order from becoming a thousand lines.
const (
	MaxMenuPriceCents    = 1_000_000 // $10,000 for one item
	MaxNameLength        = 80
	MaxDescriptionLength = 300
	// MaxLineQuantity is the most of one menu item on an order, and MaxOrderLines the most lines.
	MaxLineQuantity = 50
	MaxOrderLines   = 30
)

// Restaurant is a place customers can order from. An inactive restaurant is closed: it is not
// offered to customers and takes no orders from the menu.
type Restaurant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"createdAt"`
}

// MenuItem is something a restaurant sells, at the price the restaurant set. This is where an
// order's prices come from: a customer never says what an item costs.
type MenuItem struct {
	ID           string    `json:"id"`
	RestaurantID string    `json:"restaurantId"`
	Name         string    `json:"name"`
	Description  string    `json:"description,omitempty"`
	PriceCents   int64     `json:"priceCents"`
	// Available is false for something the kitchen has run out of; it stays on the menu list for staff.
	Available bool      `json:"available"`
	CreatedAt time.Time `json:"createdAt"`
}
