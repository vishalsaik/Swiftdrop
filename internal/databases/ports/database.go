package ports

import (
	"context"

	"swiftdrop/internal/domain"
)

type IDatabase interface {
	CreateOrder(ctx context.Context, order domain.Order) (domain.Order, error)
	GetOrderbyId(ctx context.Context, id string) (domain.Order, error)
	UpdateOrderState(ctx context.Context, id string, state string) (domain.Order, error)
	SetFoodReady(ctx context.Context, id string) (domain.Order, error)
	AcceptOrder(ctx context.Context, id string, driverID string, state string) (domain.Order, error)
}
