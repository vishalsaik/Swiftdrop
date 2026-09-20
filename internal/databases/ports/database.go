package ports

import (
	"context"

	"swiftdrop/internal/domain"
)

type IDatabase interface {
	CreateOrder(ctx context.Context, order domain.Order) (domain.Order, error)
	GetOrderbyId(ctx context.Context, id string) (domain.Order, error)
}
