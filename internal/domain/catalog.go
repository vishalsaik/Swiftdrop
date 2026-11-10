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

	// A restaurant's cuisine is a short label ("North Indian"). It delivers up to MaxDeliveryRadiusKm
	// from where it is, DefaultDeliveryRadiusKm until someone says otherwise.
	MaxCuisineLength        = 40
	DefaultDeliveryRadiusKm = 5.0
	MaxDeliveryRadiusKm     = 100.0
	// MaxSearchLength is the longest thing a customer may search for.
	MaxSearchLength = 80
)

// Restaurant is a place customers can order from. An inactive restaurant is closed: it is not
// offered to customers and takes no orders from the menu.
type Restaurant struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Cuisine string `json:"cuisine,omitempty"`
	// Latitude and Longitude say where it is; both nil until staff place it. A restaurant that is
	// not placed is never found by a search near someone, because nobody can say how far it is.
	Latitude         *float64  `json:"latitude,omitempty"`
	Longitude        *float64  `json:"longitude,omitempty"`
	DeliveryRadiusKm float64   `json:"deliveryRadiusKm"`
	CreatedAt        time.Time `json:"createdAt"`
}

// MenuItem is something a restaurant sells, at the price the restaurant set. This is where an
// order's prices come from: a customer never says what an item costs.
type MenuItem struct {
	ID           string `json:"id"`
	RestaurantID string `json:"restaurantId"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	PriceCents   int64  `json:"priceCents"`
	// Available is false for something the kitchen has run out of; it stays on the menu list for staff.
	Available bool `json:"available"`
	// Veg is true for a vegetarian item.
	Veg       bool      `json:"veg"`
	CreatedAt time.Time `json:"createdAt"`
}
