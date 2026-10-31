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
    assigned_driver_id TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()   -- set again by every write
);

-- Upgrades for databases created before a column existed. (Orders that already
-- exist get the time of the upgrade, the best we can know.)
ALTER TABLE orders ADD COLUMN IF NOT EXISTS payment_type TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE orders ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

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

-- The refund of a payment, a second dimension next to status: an authorized
-- payment stays authorized forever (the money was taken) and refund_status says
-- whether it has been given back. An authorized payment on a cancelled order
-- whose refund_status is 'none' is a refund that is DUE; the refund job finds
-- those and claims them with a lease, the same way a charge is claimed.
ALTER TABLE payments ADD COLUMN IF NOT EXISTS refund_status TEXT NOT NULL DEFAULT 'none'
    CHECK (refund_status IN ('none', 'refunding', 'refunded', 'failed'));
ALTER TABLE payments ADD COLUMN IF NOT EXISTS refund_id TEXT NOT NULL DEFAULT '';
ALTER TABLE payments ADD COLUMN IF NOT EXISTS refund_attempts INTEGER NOT NULL DEFAULT 0
    CHECK (refund_attempts >= 0);
ALTER TABLE payments ADD COLUMN IF NOT EXISTS refund_error TEXT NOT NULL DEFAULT '';
ALTER TABLE payments ADD COLUMN IF NOT EXISTS refund_lease_expires_at TIMESTAMPTZ;   -- NULL unless refunding

-- Keeps "which refunds are due" cheap: only authorized payments not yet finished.
CREATE INDEX IF NOT EXISTS payments_refund_due_idx ON payments (updated_at)
    WHERE status = 'authorized' AND refund_status IN ('none', 'refunding');
