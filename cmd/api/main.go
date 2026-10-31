package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"swiftdrop/cmd/handlers"
	"swiftdrop/internal/databases/adapters"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/payment/gateways"
	"swiftdrop/internal/payment/providers"
	"swiftdrop/internal/refund"
)

// defaultRefundInterval is how often the refund job looks for refunds that are
// due. Set REFUND_INTERVAL (for example "5s") to change it.
const defaultRefundInterval = 30 * time.Second

func main() {
	connStr := "postgres://postgres:postgres@127.0.0.1:5434/swiftdrop?sslmode=disable"
	dbConn, err := adapters.NewDatabaseConnection(connStr)
	if err != nil {
		panic(fmt.Sprintf("error creating database connection: %s", err))
	}
	defer dbConn.Pool.Close()

	refundInterval := defaultRefundInterval
	if v := os.Getenv("REFUND_INTERVAL"); v != "" {
		refundInterval, err = time.ParseDuration(v)
		if err != nil || refundInterval <= 0 {
			panic(fmt.Sprintf("REFUND_INTERVAL must be a positive duration such as 30s, got %q", v))
		}
	}

	// Gateways are chosen once, here. All three providers are always built and
	// every one that needs a gateway always gets one, so no payment type can
	// reach a nil gateway. Without a Stripe key the app uses the fake gateway
	// and never touches the network. The same gateway that takes the money gives
	// it back.
	var cardGateway, walletGateway payment.Gateway
	var refunder payment.Refunder
	if key := os.Getenv("STRIPE_KEY"); key != "" {
		stripeGateway := gateways.NewStripeGateway(key)
		cardGateway = stripeGateway
		walletGateway = stripeGateway // TODO: replace with WalletServiceGateway
		refunder = stripeGateway
	} else {
		fmt.Println("STRIPE_KEY not set: using FakeGateway (card declines over 50000 cents, wallet over 10000)")
		cardGateway = &payment.FakeGateway{LimitCents: 50_000}
		walletGateway = &payment.FakeGateway{LimitCents: 10_000}
		refunder = &payment.FakeGateway{}
	}

	payRouter := payment.NewRouter(
		&providers.CardProvider{Gateway: cardGateway},
		&providers.CashProvider{},
		&providers.WalletProvider{Gateway: walletGateway},
	)
	s := handlers.NewServer(dbConn, payRouter)

	// Stripe pushes payment outcomes to POST /webhooks/stripe and signs every
	// request with the endpoint's secret (the whsec_ value from the Stripe
	// dashboard, or from `stripe listen` in development). Without it the route
	// answers 404, because an unverified webhook must never be trusted.
	if secret := os.Getenv("STRIPE_WEBHOOK_SECRET"); secret != "" {
		s.Webhook = gateways.NewStripeWebhook(secret)
	} else {
		fmt.Println("STRIPE_WEBHOOK_SECRET not set: POST /webhooks/stripe is disabled")
	}

	// Ctrl+C or SIGTERM cancels ctx. The refund job and the server both stop on
	// it, so a refund that is mid-call is recorded before the process exits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	jobDone := make(chan struct{})
	go func() {
		defer close(jobDone)
		refund.NewService(dbConn, refunder).Run(ctx, refundInterval)
	}()
	fmt.Printf("refund job running every %s\n", refundInterval)

	srv := &http.Server{
		Addr:         "127.0.0.1:8080",
		Handler:      s.Handler(),
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	fmt.Printf("server started on this addr : %s \n", srv.Addr)

	select {
	case err := <-serveErr:
		stop() // the server died on its own: stop the job too
		<-jobDone
		panic(err)
	case <-ctx.Done():
	}

	fmt.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Println("server shutdown:", err)
	}
	<-jobDone
}
