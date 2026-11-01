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

-- One row per payment attempt. An order may be paid again after a decline, so it
-- can have several payments, but only one that is not declined (the partial unique
-- index below). That index is what makes claiming a payment atomic: of two
-- requests inserting at once, the database lets exactly one succeed.
--
-- No ON DELETE CASCADE on purpose: a payment is a financial record and must
-- not disappear because its order row was removed. status 'processing' means
-- claimed and not yet answered; lease_expires_at is the deadline of that claim.
CREATE TABLE IF NOT EXISTS payments (
    id                  TEXT PRIMARY KEY,
    order_id            TEXT NOT NULL REFERENCES orders (id),
    status              TEXT NOT NULL CHECK (status IN ('processing', 'authorized', 'declined')),
    amount_cents        BIGINT NOT NULL CHECK (amount_cents >= 0),
    provider_payment_id TEXT NOT NULL DEFAULT '',   -- the provider's id, set on completion
    reason              TEXT NOT NULL DEFAULT '',   -- decline reason, empty otherwise
    lease_expires_at    TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Retrying a declined payment. attempt numbers an order's payments 1, 2, 3 ... (it
-- is information, set when the payment is claimed). Databases created before this
-- had UNIQUE (order_id) on the column, which is dropped in favour of the index.
ALTER TABLE payments ADD COLUMN IF NOT EXISTS attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt >= 1);
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_order_id_key;
CREATE UNIQUE INDEX IF NOT EXISTS payments_one_live_per_order ON payments (order_id) WHERE status <> 'declined';

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

-- The ledger: an append-only history of everything that happens to a payment.
-- `payments` holds only the latest state of each payment; this table keeps how it
-- got there (claimed, taken over, authorized or declined, every refund attempt).
--
-- It is written by a trigger on `payments`, not by application code, so it is part
-- of the same transaction as the change it describes: a change that is rolled back
-- leaves no trace, and no code path can forget to record one. Rows can never be
-- updated. The foreign key has no cascade on purpose: a payment cannot be deleted
-- while it has a history.
CREATE TABLE IF NOT EXISTS payment_events (
    id         BIGSERIAL PRIMARY KEY,
    payment_id TEXT NOT NULL REFERENCES payments (id),
    at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind       TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS payment_events_payment_idx ON payment_events (payment_id, id);

CREATE OR REPLACE FUNCTION payment_ledger() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO payment_events (payment_id, kind, detail)
        VALUES (NEW.id, 'claimed', 'amount_cents=' || NEW.amount_cents);
        RETURN NEW;
    END IF;

    -- the payment itself
    IF OLD.status IS DISTINCT FROM NEW.status THEN
        INSERT INTO payment_events (payment_id, kind, detail)
        VALUES (NEW.id, NEW.status,
                CASE WHEN NEW.status = 'authorized' THEN 'provider_payment_id=' || NEW.provider_payment_id
                     ELSE NEW.reason END);
    ELSIF NEW.status = 'processing' AND NEW.lease_expires_at > OLD.lease_expires_at THEN
        -- a takeover moves the deadline forward; nothing else does
        INSERT INTO payment_events (payment_id, kind, detail) VALUES (NEW.id, 'taken_over', '');
    END IF;

    -- its refund
    IF OLD.refund_status IS DISTINCT FROM NEW.refund_status THEN
        INSERT INTO payment_events (payment_id, kind, detail)
        VALUES (NEW.id,
                CASE NEW.refund_status WHEN 'refunding' THEN 'refund_started'
                                       WHEN 'refunded'  THEN 'refunded'
                                       WHEN 'failed'    THEN 'refund_failed'
                                       ELSE 'refund_' || NEW.refund_status END,
                CASE NEW.refund_status WHEN 'refunding' THEN 'attempt ' || NEW.refund_attempts
                                       WHEN 'refunded'  THEN 'refund_id=' || NEW.refund_id
                                       ELSE NEW.refund_error END);
    ELSIF NEW.refund_status = 'refunding' AND NEW.refund_attempts > OLD.refund_attempts THEN
        INSERT INTO payment_events (payment_id, kind, detail)
        VALUES (NEW.id, 'refund_started', 'attempt ' || NEW.refund_attempts);
    ELSIF NEW.refund_status = 'refunding' AND OLD.refund_error IS DISTINCT FROM NEW.refund_error AND NEW.refund_error <> '' THEN
        INSERT INTO payment_events (payment_id, kind, detail)
        VALUES (NEW.id, 'refund_attempt_failed', NEW.refund_error);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS payments_ledger ON payments;
CREATE TRIGGER payments_ledger AFTER INSERT OR UPDATE ON payments
    FOR EACH ROW EXECUTE FUNCTION payment_ledger();

CREATE OR REPLACE FUNCTION payment_events_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'payment_events is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS payment_events_no_update ON payment_events;
CREATE TRIGGER payment_events_no_update BEFORE UPDATE ON payment_events
    FOR EACH ROW EXECUTE FUNCTION payment_events_append_only();

-- Things a person has to look at. Until now they were log lines (NEEDS REVIEW);
-- an alert is the same signal where the dashboard can show it and someone can mark
-- it done. An open alert is unique per (kind, subject): the same problem raised
-- again (a webhook delivered ten times) bumps `occurrences` instead of piling up.
CREATE TABLE IF NOT EXISTS alerts (
    id            BIGSERIAL PRIMARY KEY,
    kind          TEXT NOT NULL,                 -- what sort of problem
    subject       TEXT NOT NULL,                 -- what it is about, usually a payment id
    order_id      TEXT NOT NULL DEFAULT '',      -- the order it concerns, to open it from the dashboard
    message       TEXT NOT NULL,
    occurrences   INTEGER NOT NULL DEFAULT 1 CHECK (occurrences >= 1),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at   TIMESTAMPTZ,
    resolved_note TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS alerts_one_open_per_subject ON alerts (kind, subject) WHERE resolved_at IS NULL;
