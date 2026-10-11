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
)

type IDatabase interface {
	CreateOrder(ctx context.Context, order domain.Order) (domain.Order, error)
	GetOrderbyId(ctx context.Context, id string) (domain.Order, error)
	UpdateOrderState(ctx context.Context, id string, state string) (domain.Order, error)
	SetFoodReady(ctx context.Context, id string) (domain.Order, error)
	AcceptOrder(ctx context.Context, id string, driverID string, state string) (domain.Order, error)
}
