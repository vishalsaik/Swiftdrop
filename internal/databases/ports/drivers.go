package ports

import (
	"context"
	"errors"

	"swiftdrop/internal/domain"
)

var (
	// ErrDriverNotFound means there is no driver account with that id: no account at all, or one that is
	// not a driver.
	ErrDriverNotFound = errors.New("driver not found")
	// ErrDriverOffline means a driver tried something only an online driver may do (send a location).
	ErrDriverOffline = errors.New("driver is offline")
)

// DriverStore is what the driver routes need from storage: a driver's working state, and the places they
// say they are. A driver is a user with the role 'driver'; a driver who has never been online simply reads
// as offline.
type DriverStore interface {
	// GetDriver returns the driver's state. ErrDriverNotFound if the account does not exist or is not a driver.
	GetDriver(ctx context.Context, userID string) (domain.Driver, error)

	// SetOnline says whether the driver is taking deliveries, and answers their state. Going online starts
	// the clock their idle time is measured from; going offline forgets the last point they sent, so a
	// location is kept only while the driver is online. Saying what is already true changes nothing
	// (going online twice keeps the first time). ErrDriverNotFound if the account is not a driver.
	SetOnline(ctx context.Context, userID string, online bool) (domain.Driver, error)

	// SetLocation replaces the driver's last point (never keeps a trail) and answers their state.
	// ErrDriverOffline if they are not online, ErrDriverNotFound if the account is not a driver. The caller
	// has checked the point is a place that exists.
	SetLocation(ctx context.Context, userID string, lat, lng float64) (domain.Driver, error)

	// ListDrivers returns every driver whose account is not disabled: online first, then by name.
	ListDrivers(ctx context.Context) ([]domain.Driver, error)

	// DriverNames returns the names of the given accounts, keyed by id, for showing a driver on an order.
	// An id that is not an account is simply missing from the answer.
	DriverNames(ctx context.Context, ids []string) (map[string]string, error)
}
