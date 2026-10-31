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

// translateStateWrite is translate for a compare-and-set write. Zero rows
// back means one of two things the caller must tell apart: there is no such
// order (ErrOrderNotFound), or there is one but it was no longer in the state
// the caller expected (ErrStateConflict). A second query separates them; the
// write itself already decided the race, so this is only for the error.
func (db *DBAdapter) translateStateWrite(ctx context.Context, err error, id, doing string) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return translate(err, id, doing)
	}
	var exists bool
	if qerr := db.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM orders WHERE id = $1)`, id).Scan(&exists); qerr != nil {
		return fmt.Errorf("%s: %w", doing, qerr)
	}
	if !exists {
		return fmt.Errorf("%w: %s", ports.ErrOrderNotFound, id)
	}
	return fmt.Errorf("%w: %s", ports.ErrStateConflict, id)
}

func (db *DBAdapter) UpdateOrderState(ctx context.Context, id string, from, to string) (domain.Order, error) {
	// Compare-and-set: the row only matches while it is still in `from`, so
	// when two requests race, the database lets exactly one of them through.
	query := `
		UPDATE orders
		SET state = $1
		WHERE id = $2 AND state = $3
		RETURNING ` + orderColumns
	o, err := scanOrder(db.Pool.QueryRow(ctx, query, to, id, from))
	if err != nil {
		return domain.Order{}, db.translateStateWrite(ctx, err, id, "failed to update order state")
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

func (db *DBAdapter) AcceptOrder(ctx context.Context, id string, driverID string, from, to string) (domain.Order, error) {
	// The state and the driver are written together, and only if the order is
	// still in `from`: a request that lost the race assigns no driver at all.
	query := `
		UPDATE orders
		SET state = $1, assigned_driver_id = $2
		WHERE id = $3 AND state = $4
		RETURNING ` + orderColumns
	o, err := scanOrder(db.Pool.QueryRow(ctx, query, to, driverID, id, from))
	if err != nil {
		return domain.Order{}, db.translateStateWrite(ctx, err, id, "failed to accept order")
	}
	return o, nil
}
