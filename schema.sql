-- SwiftDrop database schema.
-- Apply with:
--   docker exec -i postgres psql -U postgres -d swiftdrop < schema.sql
-- (recreate the container first if it doesn't exist:
--   docker run --name postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=swiftdrop -p 5434:5432 -d postgres:16-alpine)
--
-- Safe to re-run: CREATE TABLE IF NOT EXISTS plus idempotent ALTERs below,
-- so an existing database is upgraded in place.

CREATE TABLE IF NOT EXISTS orders (
    id                 TEXT PRIMARY KEY,
    payment_type       TEXT NOT NULL DEFAULT '',   -- card | cash | wallet, chosen at creation
    customer_id        TEXT NOT NULL,
    restaurant_id      TEXT NOT NULL,
    items              JSONB NOT NULL,
    state              TEXT NOT NULL,
    food_ready         TIMESTAMPTZ,                -- NULL until the kitchen (or its stand-in) marks it ready
    assigned_driver_id TEXT NOT NULL DEFAULT ''
);

-- Upgrades for databases created before a column existed.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS payment_type TEXT NOT NULL DEFAULT '';
