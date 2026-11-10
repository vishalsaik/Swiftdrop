// Command migrate creates the database tables, or brings them up to date. It is safe to run
// again and again, and to run from several places at once.
//
//	go run ./cmd/migrate
//
// The database is the one the server uses: DATABASE_URL, or the local development database.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"swiftdrop"
	"swiftdrop/internal/databases/adapters"
)

const defaultDSN = "postgres://postgres:postgres@127.0.0.1:5434/swiftdrop?sslmode=disable"

func main() {
	dsn := defaultDSN
	if v := os.Getenv("DATABASE_URL"); v != "" {
		dsn = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := adapters.NewDatabaseConnection(dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not connect to the database:", err)
		os.Exit(1)
	}
	defer db.Pool.Close()
	if err := db.Migrate(ctx, swiftdrop.Schema); err != nil {
		fmt.Fprintln(os.Stderr, err)
		db.Pool.Close()
		os.Exit(1)
	}
	fmt.Println("the database is up to date")
}
