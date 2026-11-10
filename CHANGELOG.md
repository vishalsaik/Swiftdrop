# Changelog

## v1.0.0

The first complete version: the whole journey from signing up to a delivered order, runnable with one command.

### New

- **Menus, and orders priced from them.** Staff keep restaurants and menu items (**Menu** in the dashboard, or `/restaurants` and `/menu` by API). A customer orders by menu item id and count; the server takes the name and price from the menu, so nobody can order a burger for a cent. A customer cannot place a line that is not on the menu; staff can (a phone order), with a price of their own. Closed restaurants, sold-out items, items of another restaurant, more than 50 of one item and more than 30 lines are refused with a message that says which. An order keeps the name and price it was placed at, so a menu change never rewrites an order.
- **The order sheet picks from the menu.** A restaurant picker, the menu with steppers and a live total, and for staff a customer picker (or a walk-in) and an optional custom line.
- **`docker compose up --build`** starts Postgres (not published), creates the tables and starts the server on `127.0.0.1:8080`. The image is a small static build that runs as a non-root user.
- **`cmd/migrate`** creates or upgrades the tables (the schema is embedded in the binary, applied under a database lock so several can start at once, and safe to repeat). **`cmd/seed`** adds a small demo menu and never changes anything that is there.
- **`LISTEN_ADDR`** sets where the server listens (default `127.0.0.1:8080`; the image uses `0.0.0.0:8080`).
- **CI** (`.github/workflows/ci.yml`): gofmt, vet and every test with the race detector against Postgres; the dashboard's type check and tests, and a check that the committed build in `web/static` is what the source builds; then the whole Docker stack with the end-to-end test.
- **An end-to-end test** (`e2e/`) that walks the product over HTTP against a running stack, with no mocks.

### Changed

- `POST /orders` from a customer now takes `menuItemId` lines instead of names and prices (a customer's old request with its own prices is refused with `422`). Staff may still send custom lines.
- The sample orders in the dashboard are placed from the first open restaurant's menu.
- CORS allows `PATCH`.
- Three Go files were reformatted (gofmt) so CI can check formatting.

### Carried over from before v1

Payments, refunds, the ledger, alerts and their notifications, the reconciler, sign-in and accounts, and the dashboard. See the README.

### Known limits

See [KNOWN_ISSUES.md](KNOWN_ISSUES.md#v1-what-it-is-not): stand-in drivers, no email, staff-reset passwords, plain menus, no TLS in the Docker stack, and a CI workflow that GitHub has not run yet.
