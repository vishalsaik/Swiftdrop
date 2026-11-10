package adapters

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"swiftdrop"
	"swiftdrop/internal/domain"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("SWIFTDROP_TEST_DSN")
	if dsn == "" {
		t.Skip("SWIFTDROP_TEST_DSN not set; skipping Postgres integration test")
	}
	return dsn
}

func restaurantFor(id string) domain.Restaurant {
	return domain.Restaurant{ID: id, Name: "Migrated Kitchen", Active: true}
}

// aFreshDatabase makes an empty database on the test server and drops it when the test ends,
// so a migration is tried from nothing, as a new installation would.
func aFreshDatabase(t *testing.T) *DBAdapter {
	t.Helper()
	admin := testAdapter(t)
	name := fmt.Sprintf("swiftdrop_migrate_%d", time.Now().UnixNano())
	ctx := context.Background()
	if _, err := admin.Pool.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Pool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
	u, err := url.Parse(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := NewDatabaseConnection(u.String())
	if err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}
	t.Cleanup(db.Pool.Close)
	return db
}

func tablesIn(t *testing.T, db *DBAdapter) map[string]bool {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(), `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	return out
}

func TestMigratingAnEmptyDatabaseMakesEveryTable(t *testing.T) {
	db := aFreshDatabase(t)
	if err := db.Migrate(context.Background(), swiftdrop.Schema); err != nil {
		t.Fatal(err)
	}
	got := tablesIn(t, db)
	for _, want := range []string{"orders", "payments", "users", "sessions", "restaurants", "menu_items", "user_events"} {
		if !got[want] {
			t.Errorf("table %q was not made (have %v)", want, got)
		}
	}
	// and the database is usable: the catalog works on it
	aRestaurantIn(t, db)
}

func aRestaurantIn(t *testing.T, db *DBAdapter) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.CreateRestaurant(ctx, restaurantFor("m-r1")); err != nil {
		t.Fatalf("a migrated database cannot take a restaurant: %v", err)
	}
}

func TestMigratingAgainChangesNothingAndKeepsTheData(t *testing.T) {
	db := aFreshDatabase(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, swiftdrop.Schema); err != nil {
		t.Fatal(err)
	}
	aRestaurantIn(t, db)
	before := len(tablesIn(t, db))
	if err := db.Migrate(ctx, swiftdrop.Schema); err != nil {
		t.Fatalf("the second migration: %v", err)
	}
	if after := len(tablesIn(t, db)); after != before {
		t.Errorf("tables = %d after, %d before", after, before)
	}
	if got, err := db.GetRestaurant(ctx, "m-r1"); err != nil || got.Name == "" {
		t.Errorf("the data did not survive a second migration: %+v, %v", got, err)
	}
}

func TestMigrationsRunningAtOnceTakeTurns(t *testing.T) {
	db := aFreshDatabase(t)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.Migrate(context.Background(), swiftdrop.Schema)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a concurrent migration failed: %v", err)
		}
	}
	if !tablesIn(t, db)["menu_items"] {
		t.Error("the tables are not all there")
	}
}

func TestABrokenMigrationIsReportedAndLeavesTheLockFree(t *testing.T) {
	db := aFreshDatabase(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, `THIS IS NOT SQL`); err == nil {
		t.Error("broken SQL was accepted")
	}
	// the lock was let go: a good migration still goes through, at once
	done := make(chan error, 1)
	go func() { done <- db.Migrate(ctx, swiftdrop.Schema) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("migrating after a failure: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the lock was never released")
	}
}

func TestMigratingNeedsAConnection(t *testing.T) {
	db := aFreshDatabase(t)
	db.Pool.Close()
	if err := db.Migrate(context.Background(), swiftdrop.Schema); err == nil {
		t.Error("a closed pool was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	other := aFreshDatabase(t)
	if err := other.Migrate(ctx, swiftdrop.Schema); err == nil {
		t.Error("a cancelled context was accepted")
	}
}
