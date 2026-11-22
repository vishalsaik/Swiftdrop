package adapters

import (
	"context"
	"errors"
	"fmt"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
)

var _ ports.DriverStore = (*DBAdapter)(nil)

// activeJob is what makes a driver busy: an order assigned to them that is not finished. It is read from
// the orders themselves, so a driver's busy state can never disagree with them.
const activeJob = `o.assigned_driver_id = u.id AND o.state IN ('preparing', 'picked_up')`

// driverColumns and driverFrom are the one shape every driver query returns; they must stay in step with
// scanDriver. IdleSince is when an online driver last became free: when they went online or when their last
// finished (or cancelled) delivery was last touched, whichever is later.
const driverColumns = `
	u.id, u.name,
	COALESCE(d.online, false), d.online_since, d.latitude, d.longitude, d.location_at,
	EXISTS (SELECT 1 FROM orders o WHERE ` + activeJob + `),
	CASE WHEN COALESCE(d.online, false) THEN GREATEST(d.online_since,
		(SELECT max(o.updated_at) FROM orders o WHERE o.assigned_driver_id = u.id AND o.state IN ('delivered', 'cancelled'))) END,
	COALESCE(d.updated_at, u.created_at)`

const driverFrom = ` FROM users u LEFT JOIN drivers d ON d.user_id = u.id `

func scanDriver(row pgx.Row) (domain.Driver, error) {
	var d domain.Driver
	err := row.Scan(&d.UserID, &d.Name, &d.Online, &d.OnlineSince, &d.Latitude, &d.Longitude, &d.LocationAt,
		&d.Busy, &d.IdleSince, &d.UpdatedAt)
	return d, err
}

func (db *DBAdapter) GetDriver(ctx context.Context, userID string) (domain.Driver, error) {
	d, err := scanDriver(db.Pool.QueryRow(ctx,
		`SELECT `+driverColumns+driverFrom+`WHERE u.id = $1 AND u.role = 'driver'`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Driver{}, fmt.Errorf("%w: %s", ports.ErrDriverNotFound, userID)
	}
	if err != nil {
		return domain.Driver{}, fmt.Errorf("failed to read driver: %w", err)
	}
	return d, nil
}

func (db *DBAdapter) SetOnline(ctx context.Context, userID string, online bool) (domain.Driver, error) {
	if online {
		// A driver's row is made the first time they go online. Going online twice keeps the first
		// time, so the idle clock is not reset by a repeated tap. An account that is not a driver
		// inserts nothing, and the read at the end says so.
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO drivers (user_id, online, online_since, updated_at)
			SELECT id, true, now(), now() FROM users WHERE id = $1 AND role = 'driver'
			ON CONFLICT (user_id) DO UPDATE
			SET online = true,
			    online_since = CASE WHEN drivers.online THEN drivers.online_since ELSE now() END,
			    updated_at = now()`, userID); err != nil {
			return domain.Driver{}, fmt.Errorf("failed to set driver online: %w", err)
		}
	} else {
		// Going offline forgets where they were: a location is kept only while they are online. It also
		// ends an offer they hold, so the order is offered to someone else at the next round instead of
		// waiting out the lease for a driver who has left. Saying "offline" to an offline driver changes
		// nothing and is not an error.
		if err := db.goOffline(ctx, userID); err != nil {
			return domain.Driver{}, err
		}
	}
	return db.GetDriver(ctx, userID)
}

// goOffline makes the driver offline, forgets their point and ends the offer they hold, in one transaction.
func (db *DBAdapter) goOffline(ctx context.Context, userID string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to set driver offline: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE drivers SET online = false, online_since = NULL, latitude = NULL, longitude = NULL,
		       location_at = NULL, updated_at = now()
		WHERE user_id = $1 AND online`, userID); err != nil {
		return fmt.Errorf("failed to set driver offline: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE order_offers SET status = 'expired', ended_at = clock_timestamp()
		WHERE driver_id = $1 AND status = 'offered'`, userID); err != nil {
		return fmt.Errorf("failed to set driver offline: %w", err)
	}
	return tx.Commit(ctx)
}

func (db *DBAdapter) SetLocation(ctx context.Context, userID string, lat, lng float64) (domain.Driver, error) {
	res, err := db.Pool.Exec(ctx, `
		UPDATE drivers SET latitude = $2, longitude = $3, location_at = now(), updated_at = now()
		WHERE user_id = $1 AND online`, userID, lat, lng)
	if err != nil {
		return domain.Driver{}, fmt.Errorf("failed to set driver location: %w", err)
	}
	if res.RowsAffected() == 0 {
		// Either they are not online, or there is no such driver: say which.
		if _, err := db.GetDriver(ctx, userID); err != nil {
			return domain.Driver{}, err
		}
		return domain.Driver{}, fmt.Errorf("%w: %s", ports.ErrDriverOffline, userID)
	}
	return db.GetDriver(ctx, userID)
}

func (db *DBAdapter) ListDrivers(ctx context.Context) ([]domain.Driver, error) {
	rows, err := db.Pool.Query(ctx, `SELECT `+driverColumns+driverFrom+`
		WHERE u.role = 'driver' AND u.disabled_at IS NULL
		ORDER BY COALESCE(d.online, false) DESC, lower(u.name), u.id`)
	if err != nil {
		return nil, fmt.Errorf("failed to list drivers: %w", err)
	}
	defer rows.Close()
	out := []domain.Driver{}
	for rows.Next() {
		d, err := scanDriver(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to list drivers: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (db *DBAdapter) DriverNames(ctx context.Context, ids []string) (map[string]string, error) {
	names := map[string]string{}
	if len(ids) == 0 {
		return names, nil
	}
	rows, err := db.Pool.Query(ctx, `SELECT id, name FROM users WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to read driver names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("failed to read driver names: %w", err)
		}
		names[id] = name
	}
	return names, rows.Err()
}
