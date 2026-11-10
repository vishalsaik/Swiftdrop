package ports

import (
	"context"
	"errors"

	"swiftdrop/internal/domain"
)

var (
	// ErrAddressNotFound means the customer has no such saved address (someone else's is the same).
	ErrAddressNotFound = errors.New("address not found")
	// ErrTooManyAddresses means the customer already keeps the most they may.
	ErrTooManyAddresses = errors.New("too many saved addresses")
)

// AddressStore keeps the addresses customers save to use again. Every call names the owner, and an
// address is found only by its owner: asking for someone else's is the same as asking for one that
// is not there.
type AddressStore interface {
	// ListAddresses returns the user's addresses, oldest first.
	ListAddresses(ctx context.Context, userID string) ([]domain.SavedAddress, error)

	// GetAddress returns one of the user's addresses, or ErrAddressNotFound.
	GetAddress(ctx context.Context, userID, id string) (domain.SavedAddress, error)

	// CreateAddress stores a new address for a.UserID, or ErrTooManyAddresses if they already keep
	// domain.MaxSavedAddresses. The count and the insert are one step, so two requests at the same
	// moment cannot both take the last place.
	CreateAddress(ctx context.Context, a domain.SavedAddress) (domain.SavedAddress, error)

	// UpdateAddress replaces the words and the place of one of the user's addresses. Orders placed
	// with it are not touched: they keep their own copy.
	UpdateAddress(ctx context.Context, userID, id string, a domain.Address) (domain.SavedAddress, error)

	// DeleteAddress removes one of the user's addresses, or ErrAddressNotFound.
	DeleteAddress(ctx context.Context, userID, id string) error
}
