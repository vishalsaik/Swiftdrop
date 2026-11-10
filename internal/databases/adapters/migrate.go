package adapters

import (
	"context"
	"fmt"
)

// migrateLockKey is the advisory lock that lets only one migration run at a time. Starting
// several copies of a service together (or a deploy that overlaps the last) is then safe: the
// others wait, and find the work done.
const migrateLockKey int64 = 0x5377696674 // "Swift"

// Migrate applies the schema (SQL that is safe to apply again) under a lock, on one
// connection, so two callers never interleave their statements.
func (db *DBAdapter) Migrate(ctx context.Context, schema string) error {
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to migrate: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLockKey); err != nil {
		return fmt.Errorf("failed to migrate: taking the lock: %w", err)
	}
	// The lock belongs to the session, so release it explicitly before the connection goes
	// back to the pool; the context may be cancelled by then, hence a fresh one.
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLockKey) }()
	// No arguments, so this goes by the simple protocol, which accepts many statements.
	if _, err := conn.Exec(ctx, schema); err != nil {
		return fmt.Errorf("failed to migrate: %w", err)
	}
	return nil
}
