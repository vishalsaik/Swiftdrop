package adapters

import (
	"context"
	"errors"
	"fmt"
	"log"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ ports.IDatabase = (*DBAdapter)(nil)

// uniqueViolation is Postgres' SQLSTATE for a duplicate primary/unique key.
const uniqueViolation = "23505"

// orderColumns is the one column list every query returns. It must stay in
// the same order as the Scan in scanOrder.
const orderColumns = `id, payment_type, customer_id, restaurant_id, items, state, food_ready, assigned_driver_id`

// scanOrder reads one row shaped like orderColumns into an Order.
func scanOrder(row pgx.Row) (domain.Order, error) {
	var o domain.Order
	err := row.Scan(
		&o.ID,
		&o.PaymentType,
		&o.CustomerID,
		&o.RestaurantID,
		&o.Items,
		&o.State,
		&o.FoodReady,
		&o.AssignedDriverID,
	)
	return o, err
}

// translate turns a driver error into the errors callers are meant to see:
// the ports sentinels for "missing" and "duplicate", otherwise the original
// error wrapped with what we were doing.
func translate(err error, id, doing string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ports.ErrOrderNotFound, id)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return fmt.Errorf("%w: %s", ports.ErrOrderExists, id)
	}
	return fmt.Errorf("%s: %w", doing, err)
}

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
		INSERT INTO orders (id, payment_type, customer_id, restaurant_id, items, state)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING ` + orderColumns
	// pgx natively serializes order.Items slice directly into a JSONB column block
	o, err := scanOrder(db.Pool.QueryRow(ctx, query,
		order.ID,
		order.PaymentType,
		order.CustomerID,
		order.RestaurantID,
		order.Items,
		order.State,
	))
	if err != nil {
		return domain.Order{}, translate(err, order.ID, "failed to insert order")
	}
	return o, nil
}

func (db *DBAdapter) GetOrderbyId(ctx context.Context, id string) (domain.Order, error) {
	query := `SELECT ` + orderColumns + ` FROM orders WHERE id = $1`
	o, err := scanOrder(db.Pool.QueryRow(ctx, query, id))
	if err != nil {
		return domain.Order{}, translate(err, id, "failed to fetch order")
	}
	return o, nil
}

func (db *DBAdapter) UpdateOrderState(ctx context.Context, id string, state string) (domain.Order, error) {
	query := `
		UPDATE orders
		SET state = $1
		WHERE id = $2
		RETURNING ` + orderColumns
	o, err := scanOrder(db.Pool.QueryRow(ctx, query, state, id))
	if err != nil {
		return domain.Order{}, translate(err, id, "failed to update order state")
	}
	return o, nil
}

func (db *DBAdapter) SetFoodReady(ctx context.Context, id string) (domain.Order, error) {
	query := `
		UPDATE orders
		SET food_ready = COALESCE(food_ready, now())
		WHERE id = $1
		RETURNING ` + orderColumns
	o, err := scanOrder(db.Pool.QueryRow(ctx, query, id))
	if err != nil {
		return domain.Order{}, translate(err, id, "failed to set food ready")
	}
	return o, nil
}

func (db *DBAdapter) AcceptOrder(ctx context.Context, id string, driverID string, state string) (domain.Order, error) {
	query := `
		UPDATE orders
		SET state = $1, assigned_driver_id = $2
		WHERE id = $3
		RETURNING ` + orderColumns
	o, err := scanOrder(db.Pool.QueryRow(ctx, query, state, driverID, id))
	if err != nil {
		return domain.Order{}, translate(err, id, "failed to accept order")
	}
	return o, nil
}
