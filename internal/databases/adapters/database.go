package adapters

import (
	"context"
	"errors"
	"fmt"
	"log"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This struct holds your actual pgx connection pool
type DBAdapter struct {
	Pool *pgxpool.Pool
}

func NewDatabaseConnection(connStr string) (*DBAdapter, error) {
	ctx := context.Background()
	dbPool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		return nil, fmt.Errorf("unable to create connection pool: %w", err)
	}
	if err := dbPool.Ping(ctx); err != nil {
		dbPool.Close()
		return nil, fmt.Errorf("database connection failed: %w", err)
	}
	log.Println("Connected to PostgreSQL successfully.")
	return &DBAdapter{Pool: dbPool}, nil
}
func (db *DBAdapter) CreateOrder(ctx context.Context, order domain.Order) (domain.Order, error) {
	query := `
		INSERT INTO orders (id, customer_id, restaurant_id, items, state) 
		VALUES ($1, $2, $3, $4, $5) 
		RETURNING id, customer_id, restaurant_id, items, state`
	// pgx natively serializes order.Items slice directly into a JSONB column block
	err := db.Pool.QueryRow(ctx, query,
		order.ID,
		order.CustomerID,
		order.RestaurantID,
		order.Items,
		order.State,
	).Scan(
		&order.ID,
		&order.CustomerID,
		&order.RestaurantID,
		&order.Items,
		&order.State,
	)
	if err != nil {
		return domain.Order{}, fmt.Errorf("failed to insert order payload: %w", err)
	}

	return order, nil
}
func (db *DBAdapter) GetOrderbyId(ctx context.Context, id string) (domain.Order, error) {
	query := `
		SELECT id, customer_id, restaurant_id, items, state 
		FROM orders 
		WHERE id = $1;
	`

	var order domain.Order

	err := db.Pool.QueryRow(ctx, query, id).Scan(
		&order.ID,
		&order.CustomerID,
		&order.RestaurantID,
		&order.Items,
		&order.State,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Order{}, fmt.Errorf("order not found: %s", id)
		}
		return domain.Order{}, fmt.Errorf("failed to fetch order context: %w", err)
	}

	return order, nil
}
