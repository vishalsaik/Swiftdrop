package ports

import (
	"context"
	"errors"

	"swiftdrop/internal/domain"
)

// Errors every IDatabase implementation returns (wrapped with %w and the
// order id) for these conditions, so callers match on the error with
// errors.Is instead of knowing anything about the storage driver.
var (
	ErrOrderNotFound = errors.New("order not found")
	ErrOrderExists   = errors.New("order already exists")

	// ErrStateConflict means a state write found the order in a different
	// state than the caller expected: someone else moved it between the
	// caller's read and its write. Nothing was changed.
	ErrStateConflict = errors.New("order is not in the expected state")
)

type IDatabase interface {
	CreateOrder(ctx context.Context, order domain.Order) (domain.Order, error)
	GetOrderbyId(ctx context.Context, id string) (domain.Order, error)

	// UpdateOrderState is a compare-and-set: it moves the order from `from`
	// to `to` only if it is still in `from`, and returns ErrStateConflict if
	// it is not (or ErrOrderNotFound if there is no such order). Of two
	// requests that both read the same state, exactly one write succeeds.
	UpdateOrderState(ctx context.Context, id string, from, to string) (domain.Order, error)
	SetFoodReady(ctx context.Context, id string) (domain.Order, error)

	// AcceptOrder moves the order from `from` to `to` and assigns the driver
	// in one write, with the same compare-and-set rule as UpdateOrderState.
	AcceptOrder(ctx context.Context, id string, driverID string, from, to string) (domain.Order, error)
}
