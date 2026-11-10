// Command seed adds a small demo menu (two restaurants) so there is something to order. It
// only adds what is missing and never changes what is there, so it is safe to run again.
//
//	go run ./cmd/seed
//
// The database is the one the server uses: DATABASE_URL, or the local development database.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"swiftdrop/internal/catalog"
	"swiftdrop/internal/databases/adapters"
)

const defaultDSN = "postgres://postgres:postgres@127.0.0.1:5434/swiftdrop?sslmode=disable"

func main() {
	dsn := defaultDSN
	if v := os.Getenv("DATABASE_URL"); v != "" {
		dsn = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := adapters.NewDatabaseConnection(dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not connect to the database:", err)
		os.Exit(1)
	}
	defer db.Pool.Close()
	restaurants, items, err := catalog.Seed(ctx, db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not seed the menu:", err)
		db.Pool.Close()
		os.Exit(1)
	}
	fmt.Printf("added %d restaurants and %d menu items (what was already there is untouched)\n", restaurants, items)
}
