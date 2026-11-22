package ports

import (
	"context"

	"swiftdrop/internal/domain"
)

// JobStore is what a driver's own screen needs to know about their delivery.
type JobStore interface {
	// CurrentJob returns the driver's unfinished delivery: the order assigned to them that is being prepared
	// or is on its way. ok = false if they have none. A driver holds at most one (the database sees to it),
	// and an empty driver id is an error, never "every order that has no driver".
	CurrentJob(ctx context.Context, driverID string) (order domain.Order, ok bool, err error)
}
