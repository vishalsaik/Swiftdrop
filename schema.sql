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

-- One row per payment attempt. The UNIQUE constraint on order_id is what makes
-- claiming a payment atomic: of two requests inserting at once, the database
-- lets exactly one succeed. (Retrying a declined payment, a later feature, will
-- relax this to "one active payment per order".)
--
-- No ON DELETE CASCADE on purpose: a payment is a financial record and must
-- not disappear because its order row was removed. status 'processing' means
-- claimed and not yet answered; lease_expires_at is the deadline of that claim.
CREATE TABLE IF NOT EXISTS payments (
    id                  TEXT PRIMARY KEY,
    order_id            TEXT NOT NULL UNIQUE REFERENCES orders (id),
    status              TEXT NOT NULL CHECK (status IN ('processing', 'authorized', 'declined')),
    amount_cents        BIGINT NOT NULL CHECK (amount_cents >= 0),
    provider_payment_id TEXT NOT NULL DEFAULT '',   -- the provider's id, set on completion
    reason              TEXT NOT NULL DEFAULT '',   -- decline reason, empty otherwise
    lease_expires_at    TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
