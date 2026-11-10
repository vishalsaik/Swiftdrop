package adapters

import (
	"context"
	"errors"
	"fmt"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/jackc/pgx/v5"
)

var _ ports.AddressStore = (*DBAdapter)(nil)

const addressColumns = `id, user_id, label, line1, landmark, city, postal_code, latitude, longitude, created_at`

func scanAddress(row pgx.Row) (domain.SavedAddress, error) {
	var a domain.SavedAddress
	err := row.Scan(&a.ID, &a.UserID, &a.Label, &a.Line1, &a.Landmark, &a.City, &a.PostalCode, &a.Latitude, &a.Longitude, &a.CreatedAt)
	return a, err
}

func (db *DBAdapter) ListAddresses(ctx context.Context, userID string) ([]domain.SavedAddress, error) {
	rows, err := db.Pool.Query(ctx, `SELECT `+addressColumns+` FROM addresses WHERE user_id = $1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list addresses: %w", err)
	}
	defer rows.Close()
	out := []domain.SavedAddress{}
	for rows.Next() {
		a, err := scanAddress(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to list addresses: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list addresses: %w", err)
	}
	return out, nil
}

func (db *DBAdapter) GetAddress(ctx context.Context, userID, id string) (domain.SavedAddress, error) {
	a, err := scanAddress(db.Pool.QueryRow(ctx, `SELECT `+addressColumns+` FROM addresses WHERE id = $1 AND user_id = $2`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SavedAddress{}, fmt.Errorf("%w: %s", ports.ErrAddressNotFound, id)
	}
	if err != nil {
		return domain.SavedAddress{}, fmt.Errorf("failed to fetch address: %w", err)
	}
	return a, nil
}

func (db *DBAdapter) CreateAddress(ctx context.Context, a domain.SavedAddress) (domain.SavedAddress, error) {
	if a.ID == "" || a.UserID == "" || a.Line1 == "" {
		return domain.SavedAddress{}, errors.New("failed to create address: id, owner and street address are required")
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return domain.SavedAddress{}, fmt.Errorf("failed to create address: %w", err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck // rolling back a finished transaction is a no-op

	// One person's inserts take turns, so counting and then inserting cannot be raced into an 11th.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "addresses:"+a.UserID); err != nil {
		return domain.SavedAddress{}, fmt.Errorf("failed to create address: %w", err)
	}
	var have int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM addresses WHERE user_id = $1`, a.UserID).Scan(&have); err != nil {
		return domain.SavedAddress{}, fmt.Errorf("failed to create address: %w", err)
	}
	if have >= domain.MaxSavedAddresses {
		return domain.SavedAddress{}, ports.ErrTooManyAddresses
	}
	created, err := scanAddress(tx.QueryRow(ctx, `
		INSERT INTO addresses (id, user_id, label, line1, landmark, city, postal_code, latitude, longitude)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+addressColumns,
		a.ID, a.UserID, a.Label, a.Line1, a.Landmark, a.City, a.PostalCode, a.Latitude, a.Longitude))
	if err != nil {
		return domain.SavedAddress{}, fmt.Errorf("failed to create address: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SavedAddress{}, fmt.Errorf("failed to create address: %w", err)
	}
	return created, nil
}

func (db *DBAdapter) UpdateAddress(ctx context.Context, userID, id string, a domain.Address) (domain.SavedAddress, error) {
	if a.Line1 == "" {
		return domain.SavedAddress{}, errors.New("failed to update address: the street address is required")
	}
	saved, err := scanAddress(db.Pool.QueryRow(ctx, `
		UPDATE addresses SET label = $3, line1 = $4, landmark = $5, city = $6, postal_code = $7, latitude = $8, longitude = $9
		WHERE id = $1 AND user_id = $2 RETURNING `+addressColumns,
		id, userID, a.Label, a.Line1, a.Landmark, a.City, a.PostalCode, a.Latitude, a.Longitude))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SavedAddress{}, fmt.Errorf("%w: %s", ports.ErrAddressNotFound, id)
	}
	if err != nil {
		return domain.SavedAddress{}, fmt.Errorf("failed to update address: %w", err)
	}
	return saved, nil
}

func (db *DBAdapter) DeleteAddress(ctx context.Context, userID, id string) error {
	tag, err := db.Pool.Exec(ctx, `DELETE FROM addresses WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("failed to delete address: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ports.ErrAddressNotFound, id)
	}
	return nil
}
