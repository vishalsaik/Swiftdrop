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
- A Stripe webhook (`POST /webhooks/stripe`) is the second way a payment outcome reaches us. If Stripe charges the card but the reply to our call is lost, the payment would stay `processing`; Stripe instead pushes `payment_intent.succeeded` or `payment_intent.payment_failed` to us on its own, and the handler records it with the same `FinishPayment` that `/pay` uses. Whichever of the two arrives first wins and the other finds the payment finished and changes nothing, so a duplicate delivery is harmless. `StripeWebhook` (`internal/payment/gateways`) verifies the `Stripe-Signature` header (HMAC-SHA256 over the timestamp and the raw body, 5 minute tolerance) and finds our payment through `event.request.idempotency_key`, which Stripe copies from our charge's `Idempotency-Key` header (our payment id), so no metadata is sent. It answers `2xx` for anything a retry cannot improve, `400` for a request that fails verification, and `5xx` only when our storage fails so Stripe retries. It is switched on by `STRIPE_WEBHOOK_SECRET`; without it the route answers `404`
- A charged order that is cancelled is refunded, whichever way it got there: cancelled after paying, or cancelled while the payment was in flight (the provider's answer is kept even when the order has moved on, as an authorized payment on a cancelled order). A refund is **due** when a payment is authorized, its order is cancelled, it was charged online (cash moves no money through us) and its refund is not done; that is derived from the data, so cancelling needs no extra step. A background job (`internal/refund`, started by `main`, every `REFUND_INTERVAL`, default `30s`) claims each due refund with a lease (the same pattern as claiming a charge), asks the provider through the small `payment.Refunder` interface (`StripeGateway` and `FakeGateway` implement it, with the idempotency key `refund-<payment id>`), and records the result. A failed attempt waits for its lease and is retried; after 5 attempts the refund is marked `failed` and logged `NEEDS REVIEW`. Any number of servers can run the job and a payment is still refunded once. A refund that goes through is recorded even while the server is shutting down
- `GET /orders/{id}` and `GET /orders` answer with the order, a `payment` summary (no provider ids or error text) and a `paymentIssue` derived on every read from the order and its payment: `confirming`, `refund_pending`, `refunded` or `refund_failed`. It is the message a customer-facing screen shows ("your money is on its way back")
- A dispatch dashboard at `/` (served by the API itself, compiled into the binary, no build step): a board of kitchen tickets by stage, an order drawer with the journey, the money and the actions that make sense, a new-order form, rubber-stamp markers for payment and refund states, and the pop-up message a customer would see when their money situation changes. Night and daylight themes. Its rules (`web/static/logic.js`) are plain functions tested with `node --test`
- Small guarantees added on the way: `SetFoodReady` is a compare-and-set like every other write; a card or wallet payment without a valid payment method is refused with a `422` **before** anything is claimed (so it cannot leave a payment stuck in `processing`); a body that is not JSON is a `400`; orders record `createdAt` and `updatedAt`

Stand-ins for now: wallet payments go to the same Stripe gateway as cards, `/accept` assigns a random UUID as the driver. Not built yet: a way out for a payment stuck in `processing` when no webhook event ever arrives (the request never reached Stripe, or the event was lost after Stripe's retries ended; see [KNOWN_ISSUES.md](KNOWN_ISSUES.md)); tracking a refund that Stripe accepts and later fails (the job only knows it was accepted); a cache in front of the payment lookup; a real driver registry and dispatch; and authentication.

## Tech stack

- Go 1.25
- [gorilla/mux](https://github.com/gorilla/mux) for HTTP routing
- [pgx](https://github.com/jackc/pgx) for Postgres access
- [google/uuid](https://github.com/google/uuid) for the stand-in driver IDs and the fake payment IDs
- PostgreSQL 16 (via Docker for local dev)
- The dashboard is plain HTML, CSS and JavaScript (ES modules, no framework, no build step). Node 20+ is only needed to run its tests

## Project layout

```
cmd/
  api/            entry point — wires dependencies, starts the HTTP server
  handlers/       Server struct, HTTP handlers, and the middleware chain
internal/
  domain/         plain data types: Order, Driver, Payment, Event
  order/          legal order-state transitions (CanTransition/Apply)
  payment/        PaymentProvider (strategy), Gateway (adapter seam), Router (factory), EventParser (webhook seam), FakeGateway, MockGateway
    providers/    card, wallet and cash providers
    gateways/     StripeGateway (the real HTTP adapter to Stripe) and StripeWebhook (verifies and translates Stripe's events)
  validation/     the field rules for a new order and the chain that runs them
  refund/         the background job that refunds charged orders that were cancelled
  databases/
    ports/        IDatabase — the storage interface handlers depend on
    adapters/     Postgres implementation of IDatabase
web/              the dashboard (static files compiled into the binary) and its tests
schema.sql        database schema
curls.txt         example curl commands for manual API testing
KNOWN_ISSUES.md   what we chose not to build yet, and the production problems that leaves
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

   To receive Stripe's webhook as well, set `STRIPE_WEBHOOK_SECRET` to the endpoint's signing secret (the `whsec_` value). In development run `stripe listen --forward-to localhost:8080/webhooks/stripe` with the [Stripe CLI](https://docs.stripe.com/stripe-cli); it prints the secret to use. It works with the fake gateway too, so you can try it with events you sign yourself (see [curls.txt](curls.txt)).

   Open <http://127.0.0.1:8080/> for the dashboard. The refund job looks for due refunds every 30 seconds; set `REFUND_INTERVAL` (for example `5s`) to watch a refund happen faster.

4. Try it — see [curls.txt](curls.txt) for ready-to-use examples, or:

   ```bash
   curl http://127.0.0.1:8080/health
   ```

## Tests

```bash
go test ./...
```

The state-machine, domain, payment and HTTP handler tests need nothing but Go: the handlers are driven through the real router against an in-memory fake of `ports.IDatabase`, and `StripeGateway` is tested against a local `httptest` server that plays Stripe, so no key or network is involved. The webhook is tested the same way: the tests sign their own events with a test secret.

The Postgres adapter tests need a database and are skipped unless `SWIFTDROP_TEST_DSN` is set. Use a separate database so your dev data is never touched:

```bash
docker exec postgres psql -U postgres -c "CREATE DATABASE swiftdrop_test;"
docker exec -i postgres psql -U postgres -d swiftdrop_test < schema.sql
```

```bash
SWIFTDROP_TEST_DSN="postgres://postgres:postgres@127.0.0.1:5434/swiftdrop_test?sslmode=disable" go test ./... -cover
```

(PowerShell: `$env:SWIFTDROP_TEST_DSN = "postgres://postgres:postgres@127.0.0.1:5434/swiftdrop_test?sslmode=disable"` first, then `go test ./... -cover`.)

The dashboard's rules (actions, notices, money, form validation) run without a browser:

```bash
node --test web/test/logic.test.mjs
```

Its drawing code (`web/static/app.js`) is checked by hand in a browser, not by automated tests.

## API

| Method | Path                   | Description                                                                              |
|--------|------------------------|--------------------------------------------------------------------------------------------|
| GET    | `/health`              | Health check                                                                               |
| POST   | `/orders`              | Create an order (always starts `created`); `422` rule failure, `400` bad body, `415` wrong Content-Type, `413` over 1 MB, `409` duplicate id |
| GET    | `/orders`              | The newest orders, `{"orders": [...]}`, each as in `GET /orders/{id}`. `?limit=N`, default 50, at most 200 (`400` otherwise). Read in one query, so an order and its payment are from the same moment |
| GET    | `/orders/{id}`         | Get an order by ID (`404` if missing), with a `payment` summary and a `paymentIssue` when there is something to tell the customer                                                      |
| POST   | `/orders/{id}/pay`     | Idempotent. A new payment is claimed (`created → payment_pending` plus a `payments` row), authorized through the order's provider, then recorded: `paid`, or `cancelled` with a `402` and the reason on a decline. A retry replays the finished outcome (`200` with the order, or the same `402`) without charging again; `409` "still being processed" while another request holds the payment, or if the order moved on meanwhile; `409` for an order that cannot be paid from its state; `422` if the payment method is missing or not a Stripe id (nothing is claimed), `400` for a body that is not JSON; `409` with a refund promise if the order was cancelled while the payment was being processed; `500` if the provider errors (the payment stays `processing`; the webhook records the outcome if Stripe took the charge, or a retry after its 60 s lease takes it over) or the stored payment type has no provider |
| POST   | `/webhooks/stripe`     | Stripe's push of a payment outcome (`payment_intent.succeeded` or `.payment_failed`), signed with `STRIPE_WEBHOOK_SECRET`. Not for browsers or clients. `200` with `recorded`, `already recorded`, `order moved on`, `unknown payment` or `ignored`; `400` if the signature, timestamp or body fails verification; `404` while no secret is configured; `500` only if our storage fails, so Stripe retries |
| POST   | `/orders/{id}/accept`  | Restaurant accepts: `paid → preparing` and a stub driver is assigned; `409` otherwise       |
| POST   | `/orders/{id}/cancel`  | Cancel an order (`409` if its current state can't be cancelled)                            |
| POST   | `/orders/{id}/ready`   | Mark the food ready; only while `preparing`, otherwise `409` (also if the order changed while the request ran). State stays `preparing`      |
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
