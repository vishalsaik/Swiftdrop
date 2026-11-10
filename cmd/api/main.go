package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"swiftdrop/cmd/handlers"
	"swiftdrop/internal/alertnotify"
	"swiftdrop/internal/auth"
	"swiftdrop/internal/databases/adapters"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/payment/gateways"
	"swiftdrop/internal/payment/providers"
	"swiftdrop/internal/paymentcache"
	"swiftdrop/internal/reconcile"
	"swiftdrop/internal/refund"
)

// defaultRefundInterval is how often the refund job looks for refunds that are
// due. Set REFUND_INTERVAL (for example "5s") to change it.
const defaultRefundInterval = 30 * time.Second

// defaultReconcileInterval is how often the reconciler looks for payments that
// never got an answer. Set RECONCILE_INTERVAL (for example "10s") to change it.
const defaultReconcileInterval = time.Minute

// defaultAlertNotifyInterval is how often the job that sends alerts out looks for ones
// that are due. Set ALERT_NOTIFY_INTERVAL (for example "5s") to change it.
const defaultAlertNotifyInterval = 15 * time.Second

// defaultPaymentCacheSize is how many paid orders' payments are kept in memory.
// Set PAYMENT_CACHE_SIZE to change it; 0 switches the cache off.
const defaultPaymentCacheSize = 10_000

func main() {
	// The local development database, unless DATABASE_URL names another (for
	// example a throwaway one to demo the dashboard without the test orders).
	connStr := "postgres://postgres:postgres@127.0.0.1:5434/swiftdrop?sslmode=disable"
	if v := os.Getenv("DATABASE_URL"); v != "" {
		connStr = v
	}
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

	reconcileInterval := defaultReconcileInterval
	if v := os.Getenv("RECONCILE_INTERVAL"); v != "" {
		reconcileInterval, err = time.ParseDuration(v)
		if err != nil || reconcileInterval <= 0 {
			panic(fmt.Sprintf("RECONCILE_INTERVAL must be a positive duration such as 1m, got %q", v))
		}
	}

	alertInterval := defaultAlertNotifyInterval
	if v := os.Getenv("ALERT_NOTIFY_INTERVAL"); v != "" {
		alertInterval, err = time.ParseDuration(v)
		if err != nil || alertInterval <= 0 {
			panic(fmt.Sprintf("ALERT_NOTIFY_INTERVAL must be a positive duration such as 15s, got %q", v))
		}
	}
	alertReminder := alertnotify.DefaultReminder
	if v := os.Getenv("ALERT_REMINDER"); v != "" {
		alertReminder, err = time.ParseDuration(v)
		if err != nil || alertReminder < 0 {
			panic(fmt.Sprintf("ALERT_REMINDER must be a duration such as 24h, or 0 to switch reminders off, got %q", v))
		}
	}

	// Gateways are chosen once, here. All three providers are always built and
	// every one that needs a gateway always gets one, so no payment type can
	// reach a nil gateway. Without a Stripe key the app uses the fake gateway
	// and never touches the network. The same gateway that takes the money gives
	// it back.
	var cardGateway, walletGateway payment.Gateway
	var refunder payment.Refunder
	var looker payment.Looker
	if key := os.Getenv("STRIPE_KEY"); key != "" {
		stripeGateway := gateways.NewStripeGateway(key)
		cardGateway = stripeGateway
		walletGateway = stripeGateway // TODO: replace with WalletServiceGateway
		refunder = stripeGateway
		looker = stripeGateway
	} else {
		fmt.Println("STRIPE_KEY not set: using FakeGateway (card declines over 50000 cents, wallet over 10000)")
		cardGateway = &payment.FakeGateway{LimitCents: 50_000}
		walletGateway = &payment.FakeGateway{LimitCents: 10_000}
		refunder = &payment.FakeGateway{}
		looker = &payment.FakeGateway{}
	}

	payRouter := payment.NewRouter(
		&providers.CardProvider{Gateway: cardGateway},
		&providers.CashProvider{},
		&providers.WalletProvider{Gateway: walletGateway},
	)
	s := handlers.NewServer(dbConn, payRouter)

	// Accounts and sign-ins. COOKIE_SECURE=true marks the session cookie Secure, for
	// when TLS is ended by a proxy in front of the server (a direct TLS connection
	// is detected by itself).
	authService := auth.New(dbConn)
	s.Accounts = authService
	s.SecureCookies = os.Getenv("COOKIE_SECURE") == "true"
	fmt.Println("sign-in is required. Make the first staff account with: go run ./cmd/user -email you@example.com -name \"Your Name\"")

	// A retried /pay on a paid order is answered from memory: an authorized payment
	// never changes, so it is the one kind of payment worth keeping.
	cacheSize := defaultPaymentCacheSize
	if v := os.Getenv("PAYMENT_CACHE_SIZE"); v != "" {
		cacheSize, err = strconv.Atoi(v)
		if err != nil || cacheSize < 0 {
			panic(fmt.Sprintf("PAYMENT_CACHE_SIZE must be a whole number, 0 or more, got %q", v))
		}
	}
	if cacheSize > 0 {
		s.Lookup = paymentcache.New(dbConn, cacheSize)
	}

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
		refundService := refund.NewService(dbConn, refunder)
		refundService.Alerts = dbConn
		refundService.Run(ctx, refundInterval)
	}()
	fmt.Printf("refund job running every %s\n", refundInterval)

	// Old sessions (ended or unused for a day) are removed once an hour.
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		authService.RunCleanup(ctx, time.Hour)
	}()

	// The reconciler settles payments that never got an answer by asking the
	// provider what became of them. It never sends a charge again.
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		reconciler := reconcile.NewService(dbConn, looker)
		reconciler.Alerts = dbConn
		reconciler.Run(ctx, reconcileInterval)
	}()
	fmt.Printf("payment reconciler running every %s\n", reconcileInterval)

	// Alerts are sent to a webhook (Slack-compatible) so they do not wait for someone to
	// open the dashboard. The address is a secret and is never printed, only its host.
	notifyDone := make(chan struct{})
	if raw := os.Getenv("ALERT_WEBHOOK_URL"); raw != "" {
		hook, err := alertnotify.NewWebhook(raw)
		if err != nil {
			panic(fmt.Sprintf("ALERT_WEBHOOK_URL: %v", err))
		}
		hook.Dashboard = os.Getenv("DASHBOARD_URL")
		go func() {
			defer close(notifyDone)
			notifier := alertnotify.NewService(dbConn, hook)
			notifier.Reminder = alertReminder
			notifier.Run(ctx, alertInterval)
		}()
		fmt.Printf("alerts are sent to %s every %s (reminder after %s)\n", hook.Host(), alertInterval, alertReminder)
	} else {
		close(notifyDone)
		fmt.Println("ALERT_WEBHOOK_URL not set: alerts are only shown on the dashboard")
	}

	// Only this computer, unless LISTEN_ADDR says otherwise (a container listens on 0.0.0.0:8080,
	// and what reaches it is decided by how the port is published).
	listenAddr := "127.0.0.1:8080"
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		listenAddr = v
	}
	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      s.Handler(),
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	fmt.Printf("server started on this addr : %s \n", srv.Addr)

	select {
	case err := <-serveErr:
		stop() // the server died on its own: stop the jobs too
		<-jobDone
		<-reconcileDone
		<-cleanupDone
		<-notifyDone
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
	<-reconcileDone
	<-cleanupDone
	<-notifyDone
}
