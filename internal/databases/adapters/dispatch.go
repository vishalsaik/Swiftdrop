package adapters

import (
	"context"
	"errors"
	"fmt"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ ports.DispatchStore = (*DBAdapter)(nil)

const offerColumns = `id, order_id, driver_id, status, offered_at, expires_at, ended_at`

func scanOffer(row pgx.Row) (domain.Offer, error) {
	var o domain.Offer
	err := row.Scan(&o.ID, &o.OrderID, &o.DriverID, &o.Status, &o.OfferedAt, &o.ExpiresAt, &o.EndedAt)
	return o, err
}

// isUniqueViolation reports whether err is Postgres refusing a second row for a unique index.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

func (db *DBAdapter) ExpireOffers(ctx context.Context) (int, error) {
	// A lease that has run out, or an order that is no longer waiting for a driver. An accept that ran at the
	// same moment has already changed the row's status, which this update re-reads, so one of the two wins.
	tag, err := db.Pool.Exec(ctx, `
		UPDATE order_offers f SET status = 'expired', ended_at = clock_timestamp()
		WHERE f.status = 'offered' AND (
			f.expires_at <= clock_timestamp() OR NOT EXISTS (
				SELECT 1 FROM orders o WHERE o.id = f.order_id AND o.state = 'preparing' AND o.assigned_driver_id = ''))`)
	if err != nil {
		return 0, fmt.Errorf("failed to expire offers: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (db *DBAdapter) ListWaitingOrders(ctx context.Context, limit int) ([]domain.WaitingOrder, error) {
	if limit <= 0 {
		return nil, errors.New("failed to list waiting orders: limit must be positive")
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT o.id, o.restaurant_id, r.latitude, r.longitude, o.updated_at
		FROM orders o JOIN restaurants r ON r.id = o.restaurant_id
		WHERE o.state = 'preparing' AND o.assigned_driver_id = '' AND r.latitude IS NOT NULL AND r.longitude IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM order_offers f WHERE f.order_id = o.id AND f.status = 'offered')
		ORDER BY o.updated_at, o.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list waiting orders: %w", err)
	}
	defer rows.Close()
	out := []domain.WaitingOrder{}
	for rows.Next() {
		var w domain.WaitingOrder
		if err := rows.Scan(&w.OrderID, &w.RestaurantID, &w.Latitude, &w.Longitude, &w.Since); err != nil {
			return nil, fmt.Errorf("failed to list waiting orders: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (db *DBAdapter) ListCandidates(ctx context.Context, orderID string, maxLocationAge time.Duration) ([]domain.Candidate, error) {
	if maxLocationAge <= 0 {
		return nil, errors.New("failed to list candidates: the location age must be positive")
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT d.user_id, d.latitude, d.longitude, d.location_at,
		       GREATEST(d.online_since, (SELECT max(o.updated_at) FROM orders o
		                                  WHERE o.assigned_driver_id = d.user_id AND o.state IN ('delivered', 'cancelled')))
		FROM drivers d JOIN users u ON u.id = d.user_id
		WHERE d.online AND u.role = 'driver' AND u.disabled_at IS NULL
		  AND d.latitude IS NOT NULL AND d.longitude IS NOT NULL
		  AND d.location_at > now() - make_interval(secs => $2)
		  AND NOT EXISTS (SELECT 1 FROM orders j WHERE j.assigned_driver_id = d.user_id AND j.state IN ('preparing', 'picked_up'))
		  AND NOT EXISTS (SELECT 1 FROM order_offers f WHERE f.driver_id = d.user_id AND f.status = 'offered')
		  AND NOT EXISTS (SELECT 1 FROM order_offers f WHERE f.driver_id = d.user_id AND f.order_id = $1)
		ORDER BY d.user_id`, orderID, maxLocationAge.Seconds())
	if err != nil {
		return nil, fmt.Errorf("failed to list candidates: %w", err)
	}
	defer rows.Close()
	out := []domain.Candidate{}
	for rows.Next() {
		var c domain.Candidate
		if err := rows.Scan(&c.DriverID, &c.Latitude, &c.Longitude, &c.LocationAt, &c.IdleSince); err != nil {
			return nil, fmt.Errorf("failed to list candidates: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (db *DBAdapter) OfferOrder(ctx context.Context, orderID, driverID string, lease time.Duration) (domain.Offer, bool, error) {
	if lease <= 0 {
		return domain.Offer{}, false, errors.New("failed to offer order: lease must be positive")
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return domain.Offer{}, false, fmt.Errorf("failed to offer order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // a no-op once committed

	// The driver's row is locked first, and AcceptOffer takes the same lock, so an offer to a driver and
	// that driver's accepting another offer happen one after the other. Without it the two could overlap:
	// the check below that the driver holds no delivery would be read just before an accept committed, and
	// a driver who has just taken a delivery would be handed another order to hold for the whole lease. The
	// check is its own statement, so it sees everything committed before the lock was granted.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM drivers WHERE user_id = $1 FOR UPDATE`, driverID); err != nil {
		return domain.Offer{}, false, fmt.Errorf("failed to offer order: %w", err)
	}

	// One statement checks everything and makes the offer, and the unique indexes (one open offer per order
	// and per driver) decide a race between offers: of two inserts at the same moment the database lets
	// exactly one succeed. Times are the database's clock at the moment of the write (clock_timestamp), so
	// they come in the order things really happened, and servers agree on a lease's end.
	offer, err := scanOffer(tx.QueryRow(ctx, `
		INSERT INTO order_offers (order_id, driver_id, status, offered_at, expires_at)
		SELECT o.id, d.user_id, 'offered', c.t, c.t + make_interval(secs => $3)
		FROM (SELECT clock_timestamp() AS t) c, orders o, drivers d, users u
		WHERE o.id = $1 AND o.state = 'preparing' AND o.assigned_driver_id = ''
		  AND d.user_id = $2 AND u.id = d.user_id AND u.role = 'driver' AND u.disabled_at IS NULL AND d.online
		  AND NOT EXISTS (SELECT 1 FROM orders j WHERE j.assigned_driver_id = d.user_id AND j.state IN ('preparing', 'picked_up'))
		RETURNING `+offerColumns, orderID, driverID, lease.Seconds()))
	switch {
	case err == nil:
		if err := tx.Commit(ctx); err != nil {
			return domain.Offer{}, false, fmt.Errorf("failed to offer order: %w", err)
		}
		return offer, true, nil
	case errors.Is(err, pgx.ErrNoRows), isUniqueViolation(err):
		return domain.Offer{}, false, nil
	default:
		return domain.Offer{}, false, fmt.Errorf("failed to offer order: %w", err)
	}
}

func (db *DBAdapter) AcceptOffer(ctx context.Context, offerID int64, driverID string) (domain.Order, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return domain.Order{}, fmt.Errorf("failed to accept offer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // a no-op once committed

	// The driver's row is locked first: OfferOrder takes the same lock (see there for why), and two accepts
	// by one driver wait for each other.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM drivers WHERE user_id = $1 FOR UPDATE`, driverID); err != nil {
		return domain.Order{}, fmt.Errorf("failed to accept offer: %w", err)
	}

	// The offer must be the driver's, open, and inside its lease: judged at this moment, not at the start of
	// the transaction, so a lease that ran out while this waited for the lock is never completed. Two accepts
	// of one offer meet on this row: the second finds it already accepted.
	var orderID string
	err = tx.QueryRow(ctx, `
		UPDATE order_offers SET status = 'accepted', ended_at = clock_timestamp()
		WHERE id = $1 AND driver_id = $2 AND status = 'offered' AND expires_at > clock_timestamp()
		RETURNING order_id`, offerID, driverID).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, ports.ErrOfferGone
	}
	if err != nil {
		return domain.Order{}, fmt.Errorf("failed to accept offer: %w", err)
	}

	// The order's driver is set in the same transaction as the offer's outcome. If the order moved on
	// (cancelled) or someone set a driver, none of it happens; a driver who already holds a delivery is
	// refused by the unique index.
	o, err := scanOrder(tx.QueryRow(ctx, `
		UPDATE orders SET assigned_driver_id = $2, updated_at = now()
		WHERE id = $1 AND state = 'preparing' AND assigned_driver_id = ''
		RETURNING `+orderColumns, orderID, driverID))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Order{}, ports.ErrOfferGone
	case isUniqueViolation(err):
		return domain.Order{}, ports.ErrDriverBusy
	case err != nil:
		return domain.Order{}, fmt.Errorf("failed to accept offer: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Order{}, fmt.Errorf("failed to accept offer: %w", err)
	}
	return o, nil
}

func (db *DBAdapter) DeclineOffer(ctx context.Context, offerID int64, driverID string) error {
	tag, err := db.Pool.Exec(ctx, `
		UPDATE order_offers SET status = 'declined', ended_at = clock_timestamp()
		WHERE id = $1 AND driver_id = $2 AND status = 'offered' AND expires_at > clock_timestamp()`, offerID, driverID)
	if err != nil {
		return fmt.Errorf("failed to decline offer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrOfferGone
	}
	return nil
}

func (db *DBAdapter) OpenOffer(ctx context.Context, driverID string) (domain.Offer, bool, error) {
	// The time left is worked out by the database, with the clock that stamped the lease, and sent as a number
	// of seconds: the browser's clock and this server's need not agree with it, so a time of day would be wrong.
	var offer domain.Offer
	err := db.Pool.QueryRow(ctx, `
		SELECT `+offerColumns+`, EXTRACT(EPOCH FROM (expires_at - clock_timestamp()))::float8 FROM order_offers
		WHERE driver_id = $1 AND status = 'offered' AND expires_at > clock_timestamp()`, driverID).
		Scan(&offer.ID, &offer.OrderID, &offer.DriverID, &offer.Status, &offer.OfferedAt, &offer.ExpiresAt, &offer.EndedAt, &offer.RemainingSeconds)
	switch {
	case err == nil:
		return offer, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Offer{}, false, nil
	default:
		return domain.Offer{}, false, fmt.Errorf("failed to read the open offer: %w", err)
	}
}

func (db *DBAdapter) ListOffers(ctx context.Context, orderID string) ([]domain.Offer, error) {
	rows, err := db.Pool.Query(ctx, `SELECT `+offerColumns+` FROM order_offers WHERE order_id = $1 ORDER BY id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to list offers: %w", err)
	}
	defer rows.Close()
	out := []domain.Offer{}
	for rows.Next() {
		o, err := scanOffer(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to list offers: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
