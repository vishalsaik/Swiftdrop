# SwiftDrop

A food-delivery and driver-dispatch backend, built from scratch in Go as a hands-on way to learn backend engineering fundamentals — HTTP APIs, persistence, and (eventually) concurrency — one feature at a time.

## Status

Early stage. Working today:

- Domain model: `Order`, `Driver`, `Payment`, `Event` types (`internal/domain`)
- HTTP API on top of [gorilla/mux](https://github.com/gorilla/mux): health check, create order, get order by ID, and the full order lifecycle: pay (mock) → accept → ready → pickup → deliver, plus cancel
- Postgres-backed persistence via [pgx](https://github.com/jackc/pgx), behind a `ports.IDatabase` interface so the storage backend is swappable
- Order state machine (`internal/order`) — a `legalTransitions` table + `CanTransition`/`Apply` that decide which order-state changes are legal. `Order.State` is a short linear chain (`Created → PaymentPending → Paid → Preparing → PickedUp → Delivered`, plus `Cancelled` from any pre-`PickedUp` state); food-ready and driver-assignment are tracked as independent fields (`FoodReady`, `AssignedDriverID`) rather than states, since either can become true before the other

- Request validation on `POST /orders` as two Chain-of-Responsibility pipelines: an HTTP middleware chain (body size limit, `Content-Type` check) and a validator chain of field rules (`internal/validation`) that stops at the first failure. Orders always start as `created`, and the payment type (`card`, `cash`, `wallet`) is required and stored
- Storage errors cross the `IDatabase` boundary as sentinels (`ports.ErrOrderNotFound`, `ports.ErrOrderExists`), so handlers never see driver-specific errors

Stand-ins for now: `/pay` is a mock (no payment provider, no idempotency key yet) and `/accept` assigns a random UUID as the driver. Not built yet: real payment processing, a real driver registry and dispatch, CORS, and compare-and-set on state writes.

## Tech stack

- Go 1.25
- [gorilla/mux](https://github.com/gorilla/mux) for HTTP routing
- [pgx](https://github.com/jackc/pgx) for Postgres access
- [google/uuid](https://github.com/google/uuid) for the stand-in driver IDs
- PostgreSQL 16 (via Docker for local dev)

## Project layout

```
cmd/
  api/            entry point — wires dependencies, starts the HTTP server
  handlers/       Server struct, HTTP handlers, and the middleware chain
internal/
  domain/         plain data types: Order, Driver, Payment, Event
  order/          legal order-state transitions (CanTransition/Apply)
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

3. Run the API:

   ```bash
   go run ./cmd/api
   ```

   The server listens on `127.0.0.1:8080`.

4. Try it — see [curls.txt](curls.txt) for ready-to-use examples, or:

   ```bash
   curl http://127.0.0.1:8080/health
   ```

## Tests

```bash
go test ./...
```

The state-machine, domain and HTTP handler tests need nothing but Go: the handlers are driven through the real router against an in-memory fake of `ports.IDatabase`.

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
| POST   | `/orders/{id}/pay`     | Mock payment: `created → payment_pending → paid`; `409` from any other state              |
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
