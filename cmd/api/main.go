package main

import (
	"fmt"
	"net/http"
	"time"

	"swiftdrop/cmd/handlers"
	"swiftdrop/internal/databases/adapters"
)

func main() {
	connStr := "postgres://postgres:postgres@127.0.0.1:5434/swiftdrop?sslmode=disable"
	dbConn, err := adapters.NewDatabaseConnection(connStr)
	if err != nil {
		panic(fmt.Sprintf("error creating database connection: %s", err))
	}
	defer dbConn.Pool.Close()
	s := handlers.NewServer(dbConn)

	srv := &http.Server{
		Addr:         "127.0.0.1:8080",
		Handler:      s.Router,
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
	}
	fmt.Printf("server started on this addr : %s \n", srv.Addr)
	if err := srv.ListenAndServe(); err != nil {
		panic(err)
	}
}
