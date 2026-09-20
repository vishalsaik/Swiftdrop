-- SwiftDrop database schema.
-- Apply with:
--   docker exec -i postgres psql -U postgres -d swiftdrop < schema.sql
-- (recreate the container first if it doesn't exist:
--   docker run --name postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=swiftdrop -p 5434:5432 -d postgres:16-alpine)

CREATE TABLE IF NOT EXISTS orders (
    id            TEXT PRIMARY KEY,
    customer_id   TEXT NOT NULL,
    restaurant_id TEXT NOT NULL,
    items         JSONB NOT NULL,
    state         TEXT NOT NULL
);
