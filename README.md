# SwiftDrop

A food-delivery and driver-dispatch backend, built from scratch in Go as a hands-on way to learn backend engineering fundamentals — HTTP APIs, persistence, and (eventually) concurrency — one feature at a time.

## Status

Early stage. Working today:

- Domain model: `Order`, `Driver`, `Payment`, `Event` types (`internal/domain`)
- HTTP API on top of [gorilla/mux](https://github.com/gorilla/mux): health check, create order, get order by ID
- Postgres-backed persistence via [pgx](https://github.com/jackc/pgx), behind a `ports.IDatabase` interface so the storage backend is swappable

Order state machine, order-lifecycle transition endpoints (ready/pickup/deliver/cancel), driver dispatch, and payment processing are not built yet.

## Tech stack

- Go 1.25
- [gorilla/mux](https://github.com/gorilla/mux) for HTTP routing
- [pgx](https://github.com/jackc/pgx) for Postgres access
- PostgreSQL 16 (via Docker for local dev)

## Project layout

```
cmd/
  api/            entry point — wires dependencies, starts the HTTP server
  handlers/       Server struct + HTTP handlers
internal/
  domain/         plain data types: Order, Driver, Payment, Event
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

## API

| Method | Path            | Description       |
|--------|-----------------|--------------------|
| GET    | `/health`       | Health check       |
| POST   | `/orders`       | Create an order    |
| GET    | `/orders/{id}`  | Get an order by ID |
