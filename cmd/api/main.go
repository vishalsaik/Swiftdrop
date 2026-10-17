package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"swiftdrop/cmd/handlers"
	"swiftdrop/internal/databases/adapters"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/payment/gateways"
	"swiftdrop/internal/payment/providers"
)

func main() {
	connStr := "postgres://postgres:postgres@127.0.0.1:5434/swiftdrop?sslmode=disable"
	dbConn, err := adapters.NewDatabaseConnection(connStr)
	if err != nil {
		panic(fmt.Sprintf("error creating database connection: %s", err))
	}
	defer dbConn.Pool.Close()

	// Gateways are chosen once, here. All three providers are always built and
	// every one that needs a gateway always gets one, so no payment type can
	// reach a nil gateway. Without a Stripe key the app uses the fake gateway
	// and never touches the network.
	var cardGateway, walletGateway payment.Gateway
	if key := os.Getenv("STRIPE_KEY"); key != "" {
		stripeGateway := gateways.NewStripeGateway(key)
		cardGateway = stripeGateway
		walletGateway = stripeGateway // TODO: replace with WalletServiceGateway
	} else {
		fmt.Println("STRIPE_KEY not set: using FakeGateway (card declines over 50000 cents, wallet over 10000)")
		cardGateway = &payment.FakeGateway{LimitCents: 50_000}
		walletGateway = &payment.FakeGateway{LimitCents: 10_000}
	}

	payRouter := payment.NewRouter(
		&providers.CardProvider{Gateway: cardGateway},
		&providers.CashProvider{},
		&providers.WalletProvider{Gateway: walletGateway},
	)
	s := handlers.NewServer(dbConn, payRouter)

	srv := &http.Server{
		Addr:         "127.0.0.1:8080",
		Handler:      s.Handler(),
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
	}
	fmt.Printf("server started on this addr : %s \n", srv.Addr)
	if err := srv.ListenAndServe(); err != nil {
		panic(err)
	}
}
