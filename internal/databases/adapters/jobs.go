package adapters

import (
	"context"
	"errors"
	"fmt"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
)

var _ ports.JobStore = (*DBAdapter)(nil)

func (db *DBAdapter) CurrentJob(ctx context.Context, driverID string) (domain.Order, bool, error) {
	// Orders that have no driver have assigned_driver_id = '', so a lost id must never reach the query.
	if driverID == "" {
		return domain.Order{}, false, errors.New("failed to read the current job: driver id is required")
	}
	o, err := scanOrder(db.Pool.QueryRow(ctx, `
		SELECT `+orderColumns+` FROM orders
		WHERE assigned_driver_id = $1 AND state IN ('preparing', 'picked_up')`, driverID))
	switch {
	case err == nil:
		return o, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Order{}, false, nil
	default:
		return domain.Order{}, false, fmt.Errorf("failed to read the current job: %w", err)
	}
}
