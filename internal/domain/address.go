package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits on an address, and on how many a customer may keep. They stop a typo, or a script, from
// filling the database.
const (
	MaxAddressLine    = 200
	MaxAddressLabel   = 40
	MaxAddressCity    = 80
	MaxAddressPostal  = 20
	MaxSavedAddresses = 10
)

// Address is where an order is going: words a driver can read and the point on the map they stand
// for. An order keeps its own copy of the address it was placed with, so editing or deleting a
// saved address never changes an order that exists.
type Address struct {
	// Label is the customer's own name for it ("Home"); optional.
	Label string `json:"label,omitempty"`
	// Line1 is the street address: house, street and area.
	Line1 string `json:"line1"`
	// Landmark is anything that helps find the door: a landmark, a floor, "ring twice".
	Landmark   string  `json:"landmark,omitempty"`
	City       string  `json:"city,omitempty"`
	PostalCode string  `json:"postalCode,omitempty"`
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
}

// SavedAddress is an address a customer keeps to use again.
type SavedAddress struct {
	ID     string `json:"id"`
	UserID string `json:"userId"`
	Address
	CreatedAt time.Time `json:"createdAt"`
}

func hasControlRune(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// Tidy trims the words of an address. It changes nothing else.
func (a Address) Tidy() Address {
	a.Label, a.Line1, a.Landmark = strings.TrimSpace(a.Label), strings.TrimSpace(a.Line1), strings.TrimSpace(a.Landmark)
	a.City, a.PostalCode = strings.TrimSpace(a.City), strings.TrimSpace(a.PostalCode)
	return a
}

// Problems says what is wrong with an address, by field name, or returns nil if nothing is. Call it
// on a Tidy address. The coordinates must be a place on earth: a street address that is not
// anywhere cannot be delivered to.
func (a Address) Problems() map[string]string {
	problems := map[string]string{}
	check := func(field, text string, max int, required bool) {
		switch {
		case text == "" && required:
			problems[field] = "is required"
		case utf8.RuneCountInString(text) > max:
			problems[field] = "must be at most " + strconv.Itoa(max) + " characters"
		case hasControlRune(text):
			problems[field] = "must not contain control characters"
		}
	}
	check("label", a.Label, MaxAddressLabel, false)
	check("line1", a.Line1, MaxAddressLine, true)
	check("landmark", a.Landmark, MaxAddressLine, false)
	check("city", a.City, MaxAddressCity, false)
	check("postalCode", a.PostalCode, MaxAddressPostal, false)
	if !ValidPoint(a.Latitude, a.Longitude) {
		problems["latitude"] = "and longitude must be a place on earth: latitude -90 to 90, longitude -180 to 180"
	}
	if len(problems) == 0 {
		return nil
	}
	return problems
}
