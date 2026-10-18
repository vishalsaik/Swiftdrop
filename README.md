# SwiftDrop

A food-delivery and driver-dispatch backend, built from scratch in Go as a hands-on way to learn backend engineering fundamentals — HTTP APIs, persistence, and (eventually) concurrency — one feature at a time.

## Status

Early stage. Working today:

- Domain model: `Order`, `Driver`, `Payment`, `Event` types (`internal/domain`)
- HTTP API on top of [gorilla/mux](https://github.com/gorilla/mux): health check, create order, get order by ID, and the full order lifecycle: pay → accept → ready → pickup → deliver, plus cancel
- Postgres-backed persistence via [pgx](https://github.com/jackc/pgx), behind a `ports.IDatabase` interface so the storage backend is swappable
- Order state machine (`internal/order`) — a `legalTransitions` table + `CanTransition`/`Apply` that decide which order-state changes are legal. `Order.State` is a short linear chain (`Created → PaymentPending → Paid → Preparing → PickedUp → Delivered`, plus `Cancelled` from any pre-`PickedUp` state); food-ready and driver-assignment are tracked as independent fields (`FoodReady`, `AssignedDriverID`) rather than states, since either can become true before the other

- Request validation on `POST /orders` as two Chain-of-Responsibility pipelines: an HTTP middleware chain (body size limit, `Content-Type` check) and a validator chain of field rules (`internal/validation`) that stops at the first failure. Orders always start as `created`, and the payment type (`card`, `cash`, `wallet`) is required and stored
- CORS (`CORS` in `cmd/handlers/middleware.go`) as the outermost link: it wraps the whole router via `Server.Handler()` so preflights and error responses are covered. Allowed origins are an explicit list (`allowedOrigins` in `handlers.go`); methods `GET, POST, OPTIONS`, header `Content-Type`, `Vary: Origin` on every response. Browsers enforce it, so it protects a user's logged-in session from other sites, not the API from direct callers
- Storage errors cross the `IDatabase` boundary as sentinels (`ports.ErrOrderNotFound`, `ports.ErrOrderExists`), so handlers never see driver-specific errors
- Payments (`internal/payment`) as Strategy + Adapter + Factory: a `PaymentProvider` per payment type (card, wallet, cash in `providers/`), a `Gateway` seam that card and wallet delegate to (`StripeGateway`, `FakeGateway`, and a test-only `MockGateway`), and a `Router` that is the factory (payment type in, provider out). `Server` receives the router through `NewServer`, so the handler asks per order instead of being wired to one provider. A decline is a definite no (`payment_pending → cancelled`, `402`); a provider error means the outcome is unknown, so the order stays `payment_pending` (`500`) and the cause goes to the log, not the client
- `StripeGateway` tells a decline from an error by reading the HTTP status first: only a `402` card error (or a recorded decline on the PaymentIntent) is a decline; a wrong key, a rate limit, a bad request, an outage, or a payment still `processing` is an error
- `main.go` picks the gateway once at startup: Stripe when `STRIPE_KEY` is set, otherwise `FakeGateway`, which declines card amounts over 50,000 cents and wallet amounts over 10,000, so a decline is reproducible with no network
- Every order state write is a compare-and-set (`UPDATE orders SET state = $to WHERE id = $id AND state = $from`), so of two requests that read the same state exactly one write lands; the loser gets a `409` and changes nothing (`ports.ErrStateConflict`)
- `/pay` is idempotent, built on a `payments` table (one row per order, `UNIQUE (order_id)`) and a `ports.PaymentStore`. The claim comes **before** the provider call: `StartPayment` inserts the payment row and moves the order to `payment_pending` in one transaction, so of any number of simultaneous requests exactly one reaches the provider. The payment's own uuid is the provider's idempotency key, and `FinishPayment` records the outcome and moves the order to `paid` or `cancelled` in one transaction. A retry replays a finished outcome without charging again; while another request holds the payment (a 60 second lease, timed by the database clock) it is a `409`; after the lease expires a retry takes the payment over and asks the provider again with the **same** payment id
- Concurrency is tested for real: 50 goroutines paying, cancelling or accepting one order, and 20 goroutines racing every `PaymentStore` operation against Postgres, all under `go test -race`

Stand-ins for now: wallet payments go to the same Stripe gateway as cards, `PaymentMethodID` is read from the `/pay` body without validation, and `/accept` assigns a random UUID as the driver. Not built yet: a way out for a payment stuck in `processing` forever (it should eventually be flagged for review); refunds, so a cancel that lands while a payment is in flight can leave an order that was charged but cancelled (the log says `NEEDS REVIEW` when a finished payment cannot be recorded); `SetFoodReady` is still check-then-write; a cache in front of the payment lookup; a real driver registry and dispatch; and authentication.

## Tech stack

- Go 1.25
- [gorilla/mux](https://github.com/gorilla/mux) for HTTP routing
- [pgx](https://github.com/jackc/pgx) for Postgres access
- [google/uuid](https://github.com/google/uuid) for the stand-in driver IDs and the fake payment IDs
- PostgreSQL 16 (via Docker for local dev)

## Project layout

```
cmd/
  api/            entry point — wires dependencies, starts the HTTP server
  handlers/       Server struct, HTTP handlers, and the middleware chain
internal/
  domain/         plain data types: Order, Driver, Payment, Event
  order/          legal order-state transitions (CanTransition/Apply)
  payment/        PaymentProvider (strategy), Gateway (adapter seam), Router (factory), FakeGateway, MockGateway
    providers/    card, wallet and cash providers
    gateways/     StripeGateway, the real HTTP adapter to Stripe
  validation/     the field rules for a new order and the chain that runs them
  databases/
    ports/        IDatabase — the storage interface handlers depend on
    adapters/     Postgres implementation of IDatabase
schema.sql        database schema
curls.txt         example curl commands for manual API testing
```

## Getting started

**Prerequisites:** Go 1.25+, Docker.

1. Start Postgres:

   ```bash
   docker run --name postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=swiftdrop -p 5434:5432 -d postgres:16-alpine
   ```

2. Apply the schema:

   ```bash
   docker exec -i postgres psql -U postgres -d swiftdrop < schema.sql
   ```

   Re-run it whenever you pull changes: it is safe to repeat and upgrades an existing database in place (for example it adds the `payments` table). Do the same for the test database below.

3. Run the API:

   ```bash
   go run ./cmd/api
   ```

   The server listens on `127.0.0.1:8080`. With no `STRIPE_KEY` set it uses the fake payment gateway and never touches the network. To try real Stripe, set `STRIPE_KEY` to a Stripe **test-mode** secret key and send a test payment method in the `/pay` body (`{"paymentMethodId": "pm_..."}`). Never use a live key while developing, and never commit a key.

4. Try it — see [curls.txt](curls.txt) for ready-to-use examples, or:

   ```bash
   curl http://127.0.0.1:8080/health
   ```

## Tests

```bash
go test ./...
```

The state-machine, domain, payment and HTTP handler tests need nothing but Go: the handlers are driven through the real router against an in-memory fake of `ports.IDatabase`, and `StripeGateway` is tested against a local `httptest` server that plays Stripe, so no key or network is involved.

The Postgres adapter tests need a database and are skipped unless `SWIFTDROP_TEST_DSN` is set. Use a separate database so your dev data is never touched:

```bash
docker exec postgres psql -U postgres -c "CREATE DATABASE swiftdrop_test;"
docker exec -i postgres psql -U postgres -d swiftdrop_test < schema.sql
```

```bash
SWIFTDROP_TEST_DSN="postgres://postgres:postgres@127.0.0.1:5434/swiftdrop_test?sslmode=disable" go test ./... -cover
```

(PowerShell: `$env:SWIFTDROP_TEST_DSN = "postgres://postgres:postgres@127.0.0.1:5434/swiftdrop_test?sslmode=disable"` first, then `go test ./... -cover`.)

## API

| Method | Path                   | Description                                                                              |
|--------|------------------------|--------------------------------------------------------------------------------------------|
| GET    | `/health`              | Health check                                                                               |
| POST   | `/orders`              | Create an order (always starts `created`); `422` rule failure, `400` bad body, `415` wrong Content-Type, `413` over 1 MB, `409` duplicate id |
| GET    | `/orders/{id}`         | Get an order by ID (`404` if missing)                                                      |
| POST   | `/orders/{id}/pay`     | Idempotent. A new payment is claimed (`created → payment_pending` plus a `payments` row), authorized through the order's provider, then recorded: `paid`, or `cancelled` with a `402` and the reason on a decline. A retry replays the finished outcome (`200` with the order, or the same `402`) without charging again; `409` "still being processed" while another request holds the payment, or if the order moved on meanwhile; `409` for an order that cannot be paid from its state; `500` if the provider errors (the payment stays `processing`; a retry after its 60 s lease takes it over) or the stored payment type has no provider |
| POST   | `/orders/{id}/accept`  | Restaurant accepts: `paid → preparing` and a stub driver is assigned; `409` otherwise       |
| POST   | `/orders/{id}/cancel`  | Cancel an order (`409` if its current state can't be cancelled)                            |
| POST   | `/orders/{id}/ready`   | Mark the food ready; only while `preparing`, otherwise `409`. State stays `preparing`      |
| POST   | `/orders/{id}/pickup`  | `preparing → picked_up`; `409` unless food is ready **and** a driver is assigned           |
| POST   | `/orders/{id}/deliver` | `picked_up → delivered`; `409` from any other state                                        |

Every lifecycle endpoint answers `404` for an unknown order and `409` for a move the order's current state doesn't allow. Errors are always `{"error": "..."}`.

A create request looks like this (`paymentType` is one of `card`, `cash`, `wallet`; unknown fields are rejected, and the server owns `state`, `foodReady` and `assignedDriverId`):

```json
{
  "id": "order-1",
  "customerId": "cust-1",
  "restaurantId": "rest-1",
  "paymentType": "card",
  "items": [{ "sku": "SKU1", "name": "Burger", "quantity": 2, "unitPriceCents": 599 }]
}
```
