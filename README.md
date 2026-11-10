# SwiftDrop

A food-delivery and driver-dispatch backend, built from scratch in Go as a hands-on way to learn backend engineering fundamentals — HTTP APIs, persistence, and (eventually) concurrency — one feature at a time.

## SwiftDrop v1

A customer signs up, browses a restaurant's menu, orders, pays, and follows the order to the door; dispatch staff run the kitchen and the delivery, manage menus and people, and deal with payment problems. Everything below runs from one command.

### Try it (Docker)

You need Docker. Nothing else.

```bash
docker compose up --build
```

That starts Postgres (not reachable from outside), creates the tables, and starts the server with the dashboard on <http://127.0.0.1:8080> (this computer only). Then, in another terminal:

```bash
# a demo menu: two restaurants, five items (safe to run again)
docker compose run --rm --entrypoint /app/seed api

# the first staff account: nobody can sign themselves up as staff
docker compose run --rm -e SWIFTDROP_NEW_PASSWORD="a long enough phrase" --entrypoint /app/user api -email you@example.com -name "Your Name"
```

Sign in at <http://127.0.0.1:8080> as that staff account to run the dashboard, and create a second account with **Create account** to see what a customer sees. (In Git Bash on Windows put `MSYS_NO_PATHCONV=1` in front of those two commands, or Git rewrites `/app/seed`.)

To stop: `docker compose down` (add `-v` to forget the data). Settings are environment variables in **your own shell**, never in a file: `STRIPE_KEY` (a Stripe **test** key; without it a built-in fake gateway is used, which declines a card order over $500 and a wallet order over $100), `STRIPE_WEBHOOK_SECRET`, `ALERT_WEBHOOK_URL`, `SWIFTDROP_PORT`. Without Docker, see [Getting started](#getting-started).

### What v1 does

- **Customers** find food by what it is called, by restaurant or cuisine, vegetarian only, and **near them**: with their say-so the page learns where they are and shows only the restaurants that deliver there, nearest first (see [Finding food near you](#finding-food-near-you)). They sign up, pick an open restaurant, order from its menu (the server prices the order from the menu; a customer cannot name a price), pay by card, wallet or cash, and watch the order move. They can cancel, and a paid order that is cancelled is refunded by itself.
- **Staff** see every order and run it: accept, ready, pickup, deliver, cancel; place an order for a customer (or a walk-in), with a custom line if it is not on the menu; manage **Menu** (restaurants, items, prices, sold out, open or closed); manage **People** (make, switch off, reset); and handle **Alerts** about money that needs a person, which can also go to a Slack-compatible channel.
- **Payments** are safe to retry, settled by webhook and by a reconciler, refunded by a job, and written to an append-only ledger. See [docs/PAYMENT_FLOW.md](docs/PAYMENT_FLOW.md).

### What v1 is not

Drivers are a stand-in (a random id is assigned when the kitchen accepts), nothing is sent by email, a forgotten password is reset by staff, and menus are plain (no photos, categories, options, fees or tax). The full list, and what each leaves open, is in [KNOWN_ISSUES.md](KNOWN_ISSUES.md#v1-what-it-is-not). What changed is in [CHANGELOG.md](CHANGELOG.md).

### How it is checked

`go test -race ./...` (against Postgres), the dashboard's own tests, and an end-to-end test that walks the whole product over HTTP against the Docker stack: staff set up a menu, a customer signs up and orders (with a made-up price that must be ignored), pays, the kitchen and the road do their parts, the customer sees it delivered; then a price change, a cancel with a refund, a closed restaurant, and what a customer may not touch. CI (`.github/workflows/ci.yml`) runs all of it, and fails if the committed dashboard build is not what `web/ui` builds.

### Finding food near you

Every restaurant has a **place** (latitude and longitude), a **delivery radius** (5 km until staff say otherwise) and a **cuisine**; items can be marked **vegetarian**. Staff set these on a restaurant under **Menu** (there is a "Use my current location" button for staff standing in the restaurant), or by `PUT /restaurants/{id}/place`.

The **Find food** box on the order sheet calls `GET /search`. The customer's position is asked for **only when they press "Use my location"** (the browser shows its own permission prompt then, never on page load); if they say no, searching by name still works. The position is looked up once, sent rounded to about 110 metres, used for that answer and **kept nowhere**. With a position, only restaurants that have been placed and whose radius covers the customer are found; otherwise every open restaurant is. Results are best match first (an item's own name, then its restaurant's name or cuisine, then its description), then nearest, at most 30 restaurants with at most 8 matching items each. Postgres trigrams (`pg_trgm`) forgive a typing mistake in an item's name. Browsers only allow location on `https://` pages or `localhost`, so a real deployment needs TLS first.

### Where an order goes

Every order carries a **delivery address**: the words a driver can read and the point on the map they stand for. The customer chooses it under **Deliver to** on the order sheet: one of up to ten **saved addresses**, an address found by **searching**, **where they are** (the browser asks only when that button is pressed), or one **typed in** with its coordinates. Staff placing a phone order choose it the same way (without saved addresses, which are the customer's own). The food search then looks near that address.

The server decides whether the restaurant delivers there, on every order: the restaurant must exist, must have said where it is, and must be no farther from the address than its delivery radius, or the order is refused with a `422` that says how far (for example "Corner Kitchen delivers up to 6.0 km and that address is 7.4 km away"). **A restaurant that has not been placed takes no delivery orders.** An order keeps its **own copy** of the address, so editing or deleting a saved address never changes an order that exists. Orders from before addresses existed have none.

**Address search** (`GET /geocode?q=`, and `GET /geocode/reverse?lat=&lng=` for "where I am") asks a geocoder, chosen with environment variables set in your own shell:

| Variable | Effect |
|---|---|
| `GEOCODER` | `off` (the default for a plain `go run`): no address search, and the other ways of saying where still work. `demo`: a built-in list of streets in Bengaluru and Mumbai, no network (the Docker stack's default, so the quickstart works offline and CI never calls the internet). `nominatim`: OpenStreetMap's Nominatim, or any service that speaks its protocol |
| `GEOCODER_URL` | for `nominatim`: where it is; the default is the public `https://nominatim.openstreetmap.org` |
| `GEOCODER_KEY` | for `nominatim`: a key, if your service wants one. A secret: never in a file, never logged |
| `GEOCODER_COUNTRY` | for `nominatim`: limit searches to these countries, two-letter codes: `in` or `in,lk` |
| `GEOCODER_CONTACT` | for `nominatim`: who to contact about this program's traffic (an email or web address); the public service's policy asks for it, and it goes in the User-Agent |

The public Nominatim allows **light use only** (one request a second, no search-as-you-type, an identifying User-Agent, caching), so the dashboard searches when **Search** is pressed, answers are cached for an hour, and each person may search 30 times a minute. For real traffic run a Nominatim of your own, or a service that speaks the same protocol, and point `GEOCODER_URL` at it. What a customer types in the search box is sent to the geocoder, and the results are OpenStreetMap data (© OpenStreetMap contributors).

## Status

For how the payment flow fits together (the pieces, the states, what happens when a call fails, and how refunds work), see [docs/PAYMENT_FLOW.md](docs/PAYMENT_FLOW.md).

Early stage. Working today:

- **Orders are priced from a menu, never from the request.** Staff keep `restaurants` and `menu_items` (`internal/catalog`, `cmd/handlers/catalog.go`). A customer orders by `menuItemId` and a count; `catalog.Pricer` takes the name, sku and price from the menu, merges repeats, and refuses a closed or missing restaurant, an item that is missing, on another restaurant's menu or sold out, a count over 50 and more than 30 lines. A customer cannot place a custom line at all; staff may (a phone order), with a price of their own, next to menu lines. An order keeps copies of the name and price it was placed at, so changing the menu never changes an order. A customer sees only open restaurants and available items; a closed restaurant is not found, like one that does not exist
- Domain model: `Order`, `Driver`, `Payment`, `Event` types (`internal/domain`)
- HTTP API on top of [gorilla/mux](https://github.com/gorilla/mux): health check, create order, get order by ID, and the full order lifecycle: pay → accept → ready → pickup → deliver, plus cancel
- Postgres-backed persistence via [pgx](https://github.com/jackc/pgx), behind a `ports.IDatabase` interface so the storage backend is swappable
- Order state machine (`internal/order`) — a `legalTransitions` table + `CanTransition`/`Apply` that decide which order-state changes are legal. `Order.State` is a short linear chain (`Created → PaymentPending → Paid → Preparing → PickedUp → Delivered`, plus `Cancelled` from any pre-`PickedUp` state); food-ready and driver-assignment are tracked as independent fields (`FoodReady`, `AssignedDriverID`) rather than states, since either can become true before the other

- Request validation on `POST /orders` as two Chain-of-Responsibility pipelines: an HTTP middleware chain (body size limit, `Content-Type` check) and a validator chain of field rules (`internal/validation`) that stops at the first failure. Orders always start as `created`, and the payment type (`card`, `cash`, `wallet`) is required and stored
- CORS (`CORS` in `cmd/handlers/middleware.go`) as the outermost link: it wraps the whole router via `Server.Handler()` so preflights and error responses are covered. Allowed origins are an explicit list (`allowedOrigins` in `handlers.go`); methods `GET, POST, OPTIONS`, header `Content-Type`, `Vary: Origin` on every response. Browsers enforce it, so it protects a user's logged-in session from other sites, not the API from direct callers
- Storage errors cross the `IDatabase` boundary as sentinels (`ports.ErrOrderNotFound`, `ports.ErrOrderExists`), so handlers never see driver-specific errors
- Payments (`internal/payment`) as Strategy + Adapter + Factory: a `PaymentProvider` per payment type (card, wallet, cash in `providers/`), a `Gateway` seam that card and wallet delegate to (`StripeGateway`, `FakeGateway`, and a test-only `MockGateway`), and a `Router` that is the factory (payment type in, provider out). `Server` receives the router through `NewServer`, so the handler asks per order instead of being wired to one provider. A decline is a definite no (`402`): the order goes back to `created` so the customer can try another method, and the third decline cancels it (`domain.MaxPaymentAttempts`); a provider error means the outcome is unknown, so the order stays `payment_pending` (`500`) and the cause goes to the log, not the client
- `StripeGateway` tells a decline from an error by reading the HTTP status first: only a `402` card error (or a recorded decline on the PaymentIntent) is a decline; a wrong key, a rate limit, a bad request, an outage, or a payment still `processing` is an error
- `main.go` picks the gateway once at startup: Stripe when `STRIPE_KEY` is set, otherwise `FakeGateway`, which declines card amounts over 50,000 cents and wallet amounts over 10,000, so a decline is reproducible with no network
- Every order state write is a compare-and-set (`UPDATE orders SET state = $to WHERE id = $id AND state = $from`), so of two requests that read the same state exactly one write lands; the loser gets a `409` and changes nothing (`ports.ErrStateConflict`)
- `/pay` is idempotent, built on a `payments` table (an order can have several payments after declines, but only one that is not declined, enforced by a partial unique index) and a `ports.PaymentStore`. The claim comes **before** the provider call: `StartPayment` inserts the payment row and moves the order to `payment_pending` in one transaction, so of any number of simultaneous requests exactly one reaches the provider. The payment's own uuid is the provider's idempotency key, and `FinishPayment` records the outcome and moves the order to `paid` or `cancelled` in one transaction. A retry replays a finished outcome without charging again; while another request holds the payment (a 60 second lease, timed by the database clock) it is a `409`; after the lease expires a retry takes the payment over and asks the provider again with the **same** payment id
- Concurrency is tested for real: 50 goroutines paying, cancelling or accepting one order, and 20 goroutines racing every `PaymentStore` operation against Postgres, all under `go test -race`
- A Stripe webhook (`POST /webhooks/stripe`) is the second way a payment outcome reaches us. If Stripe charges the card but the reply to our call is lost, the payment would stay `processing`; Stripe instead pushes `payment_intent.succeeded` or `payment_intent.payment_failed` to us on its own, and the handler records it with the same `FinishPayment` that `/pay` uses. Whichever of the two arrives first wins and the other finds the payment finished and changes nothing, so a duplicate delivery is harmless. `StripeWebhook` (`internal/payment/gateways`) verifies the `Stripe-Signature` header (HMAC-SHA256 over the timestamp and the raw body, 5 minute tolerance) and finds our payment through `event.request.idempotency_key`, which Stripe copies from our charge's `Idempotency-Key` header (our payment id), so no metadata is sent. It answers `2xx` for anything a retry cannot improve, `400` for a request that fails verification, and `5xx` only when our storage fails so Stripe retries. It is switched on by `STRIPE_WEBHOOK_SECRET`; without it the route answers `404`
- **Everything needs a sign-in, and a customer can only touch their own orders.** There are two roles. A **customer** signs up on the web (`POST /auth/signup`), places orders for themselves, and sees, pays for and cancels their own orders; someone else's order answers `404`, exactly like a missing one, so ids cannot be probed. **Staff** also run the kitchen and the delivery (`accept`, `ready`, `pickup`, `deliver`), see every order, read the payment history and the alerts. Nobody can make themselves staff on the web: the first staff account comes from `go run ./cmd/user`, and after that **staff manage accounts** under **People** in the dashboard (or `/users` by API): make a customer or a staff account (the person gets a **temporary password, shown once**, and must choose their own at their first sign-in; until then every route but sign-out and change-password refuses them), switch an account off or on (switching off signs them out at once), and reset a password (which also signs them out and makes them choose again). You cannot disable or reset your own account, and the last active staff account can never be disabled, even by two staff at the same moment. Each change is recorded with who made it. Passwords are stored only as argon2id hashes (10 to 128 characters, no composition rules, not a common password). A sign-in is a random token in an `HttpOnly`, `SameSite=Strict` cookie; the server keeps only its SHA-256, so a copy of the database cannot be used to act as anyone. A session ends after 14 days or 24 hours unused, and changing the password ends every other sign-in. Every state-changing request must also send the session's CSRF token in `X-CSRF-Token` (the page reads it from `GET /auth/me`). Five wrong passwords lock an email for 15 minutes (even the right password waits), 30 failures lock an address, and a failed sign-in never says whether the email exists. The webhook, `/health` and the dashboard's own files need no sign-in.
- **A retried `/pay` on a paid order is answered from memory.** `internal/paymentcache` is a small bounded LRU cache (a Decorator) in front of the payment lookup that `/pay` does first. It keeps only an `authorized` payment: that one is final and, while it exists, the order can have no other live payment, so it can never be stale. Everything that can still change (processing, declined, abandoned, under review), "no payment yet" and every error are never kept. It serves `/pay` only (`Server.Lookup`); the order view and everything else read the database, because a refund made later changes the payment's refund fields. `PAYMENT_CACHE_SIZE` sets how many payments are kept (default 10,000, `0` switches it off). Measured against Postgres (`go test ./internal/paymentcache -bench . -benchmem`): about 1.28 ms for a lookup through the database, about 187 ns and no allocations through the cache, on a laptop with Postgres in Docker, so read the ratio, not the numbers. What it saves is one query per retry of a paid order.
- **What `/pay` means is decided in `internal/paying`, not in the handler.** `paying.Service` is a Facade over the payment record, the provider, the order's state machine and alerts: `Pay` returns a typed outcome (paid, declined with whether and how often to retry, or a conflict, bad request, too large, unprocessable, not found, internal), and the handler only reads the request and writes the matching HTTP answer. The request body is read lazily, only when a request is about to need the payment method, so a replay of a finished payment never reads, or complains about, the body. The service defines the storage it needs (`paying.Store`), so it is tested with a small in-memory store, apart from HTTP and Postgres.
- **A refund Stripe accepted can still fail later** (an expired or closed card, for one), and then the customer has not got their money back while we say they have. The webhook also reads `refund.failed` (and `refund.updated` when the refund ended up `failed` or `canceled`), matched on the payment intent the refund belongs to (Stripe changes a refund on its own, so the event is not caused by a request of ours and carries no idempotency key we can use). The refund we recorded as done, and only that one (a refund someone made by hand failing says nothing about ours), moves to `failed` with the provider's reason, a `refund_reversed` alert tells a person to refund by hand, and the customer is told to contact support like for any refund we gave up on. Nothing refunds a second time on its own: the provider said why it failed, and a second refund could pay out twice. When a person has refunded the customer by hand (in the Stripe dashboard, by a bank transfer), they **record** it: `POST /orders/{id}/refund` with the refund's reference and an optional note, or **Record a refund done by hand** on the order in the dashboard. The refund becomes `refunded`, the ledger says it was done by hand and by whom, the alerts about that refund close, and the customer is told it was sent. It records, it never moves money. A report that arrives while our own job is still making the refund is answered `503`, so Stripe delivers it again once it is recorded.
- A charged order that is cancelled is refunded, whichever way it got there: cancelled after paying, or cancelled while the payment was in flight (the provider's answer is kept even when the order has moved on, as an authorized payment on a cancelled order). A refund is **due** when a payment is authorized, its order is cancelled, it was charged online (cash moves no money through us) and its refund is not done; that is derived from the data, so cancelling needs no extra step. A background job (`internal/refund`, started by `main`, every `REFUND_INTERVAL`, default `30s`) claims each due refund with a lease (the same pattern as claiming a charge), asks the provider through the small `payment.Refunder` interface (`StripeGateway` and `FakeGateway` implement it, with the idempotency key `refund-<payment id>`), and records the result. A failed attempt waits for its lease and is retried; after 5 attempts the refund is marked `failed` and logged `NEEDS REVIEW`. Any number of servers can run the job and a payment is still refunded once. A refund that goes through is recorded even while the server is shutting down
- `GET /orders/{id}` and `GET /orders` answer with the order, a `payment` summary (no provider ids or error text) and a `paymentIssue` derived on every read from the order and its payment: `confirming`, `refund_pending`, `refunded` or `refund_failed`. It is the message a customer-facing screen shows ("your money is on its way back")
- A dispatch dashboard at `/` (a React app in `web/ui`, built with Vite, and compiled into the binary so the API serves it): a SwiftUI-style layout with a sidebar of stages (a tab bar on a phone), grouped order lists, an order inspector with the journey, the money and the actions that make sense, sheets for a new order and the account, status pills for payment and refund states, and the pop-up message a customer would see when their money situation changes (an order whose payment has no answer yet gets a thinking orb and a travelling border light, from libraries.dev). Light by default, with a dark mode chosen under Account. Its rules (`web/ui/src/lib/logic.js`) are plain functions tested with `node --test`
- **A declined payment can be retried.** A decline does not close the order: it returns to `created` and `/pay` again starts a new attempt (a new payment row, its own idempotency key, `attempt` 2, 3). Each attempt keeps its own ledger. `paymentIssue` is `declined`, with how many attempts are left. The third decline cancels the order, and after that a decline is replayed. Paying while another attempt is still `processing` is as before: a `409`, or a takeover after the lease
- **A payment that never got an answer is settled by asking, never by charging again.** The claim comes before the provider call, so a crash or a lost reply used to leave a payment `processing` for ever unless the customer retried. A reconciler job (`internal/reconcile`, every `RECONCILE_INTERVAL`, default 1 minute) now asks the provider what became of it (`payment.Looker`; Stripe finds the intent by the `metadata[payment_id]` the charge carries) and acts on the answer: succeeded or refused is recorded exactly as a webhook would record it (both go through `internal/settle`); no record at the provider after 30 minutes means the attempt is **abandoned** (a new final status, no money moved, the order is open again for another attempt or cancelled after the last); an answer that never comes, or a provider that cannot be asked, for 20 hours hands it to a person (**needs_review**, an alert, and no new attempt for that order until it is settled; the provider's answer, if it ever arrives, still settles it). Re-sending the charge is never how this is done: the customer may have walked away, and once Stripe forgets the idempotency key (at least 24 hours) a second send is a new charge. For the same reason a customer's retry of a payment older than 20 hours is no longer sent: it is handed to a person and answered `409`. The customer sees `abandoned` ("you were not charged, try again") or, for a payment under review, `confirming`.
- Problems only a person can fix are kept as **alerts**, not just logged: money taken that could not be written down (`unrecorded_charge`), a provider report that contradicts our record (`contradicting_event`), a refund the job gave up on (`refund_given_up`) and a refund the provider made that we could not record (`refund_unrecorded`). There is one open alert per kind and subject (usually a payment id): the same problem raised again only counts another occurrence, so a webhook delivered a hundred times is one alert. An alert is written even if the request that found it was cancelled, and failing to write it is logged and never changes the answer. The dashboard shows them under **Alerts**, with a Resolve button that takes a note; `GET /alerts` and `POST /alerts/{id}/resolve` do the same by API. The log line `NEEDS REVIEW` is still written. They also go **out**: set `ALERT_WEBHOOK_URL` and a job posts each new open alert to it (Slack-compatible JSON, so a Slack incoming webhook works, and so does Discord's with `/slack` added to its address), retries a failed send with a growing wait, and sends a reminder while it stays unresolved (`ALERT_REMINDER`, 24 hours by default, `0` for none). The address is a secret and is never logged.
- Every payment has a **ledger**: an append-only history (`payment_events`) of what happened to it: started, taken over by a retry, authorized or declined, and every refund attempt. A trigger in the database writes it, in the same transaction as the change it describes, so a change that is rolled back leaves no trace and no code path can forget to record one. History rows cannot be updated, and a payment with a history cannot be deleted. `GET /orders/{id}/payment-history` reads it, and the dashboard shows it in the order drawer
- Small guarantees added on the way: `SetFoodReady` is a compare-and-set like every other write; a card or wallet payment without a valid payment method is refused with a `422` **before** anything is claimed (so it cannot leave a payment stuck in `processing`); a body that is not JSON is a `400`; orders record `createdAt` and `updatedAt`

Stand-ins for now: wallet payments go to the same Stripe gateway as cards, `/accept` assigns a random UUID as the driver. Not built yet: a real driver registry and dispatch.

## Tech stack

- Go 1.25
- [gorilla/mux](https://github.com/gorilla/mux) for HTTP routing
- [pgx](https://github.com/jackc/pgx) for Postgres access
- [google/uuid](https://github.com/google/uuid) for the stand-in driver IDs and the fake payment IDs
- PostgreSQL 16 (via Docker for local dev)
- The dashboard is React 19 and TypeScript, built with Vite (`web/ui`). Its build output is committed in `web/static`, so `go run ./cmd/api` needs no Node. Node 20+ is only needed to change or test the dashboard

## Project layout

```
cmd/
  api/            entry point — wires dependencies, starts the HTTP server
  handlers/       Server struct, HTTP handlers, and the middleware chain
  migrate/        creates or upgrades the tables (the schema is embedded; safe to repeat, safe to run at once)
  seed/           adds a small demo menu, only what is missing
  user/           makes an account, such as the first staff account
e2e/              the end-to-end test: the whole product over HTTP against a running stack
internal/
  domain/         plain data types: Order, Driver, Payment, Event
  order/          legal order-state transitions (CanTransition/Apply)
  payment/        PaymentProvider (strategy), Gateway (adapter seam), Router (factory), EventParser (webhook seam), FakeGateway, MockGateway
    providers/    card, wallet and cash providers
    gateways/     StripeGateway (the real HTTP adapter to Stripe) and StripeWebhook (verifies and translates Stripe's events)
  validation/     the field rules for a new order and the chain that runs them
  catalog/        prices an order from the menu (Pricer), checks a restaurant delivers to an address (Deliverer), and seeds the demo menu (placed in Bengaluru and Mumbai)
  geocode/        the Geocoder port: Nominatim (OpenStreetMap), a built-in demo list, a cache, a limiter, and FromEnv (GEOCODER)
  refund/         the background job that refunds charged orders that were cancelled
  databases/
    ports/        IDatabase — the storage interface handlers depend on
    adapters/     Postgres implementation of IDatabase
docs/             architecture notes (PAYMENT_FLOW.md)
web/ui/           the dashboard's source (React, TypeScript, Vite) and its tests
web/static/       the dashboard as built (committed, compiled into the binary)
schema.sql        database schema (embedded by schema.go, applied by cmd/migrate)
Dockerfile        builds the server and its commands into one small image
docker-compose.yml  the database, the migration and the server, for one command
.github/workflows/ci.yml  what CI runs
CHANGELOG.md      what each release changed
curls.txt         example curl commands for manual API testing
KNOWN_ISSUES.md   what we chose not to build yet, and the production problems that leaves
```

## Getting started

**Prerequisites:** Go 1.25+, Docker. (To only run the product, `docker compose up --build` above is enough.)

1. Start Postgres:

   ```bash
   docker run --name postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=swiftdrop -p 5434:5432 -d postgres:16-alpine
   ```

2. Apply the schema, and add a demo menu:

   ```bash
   go run ./cmd/migrate
   go run ./cmd/seed
   ```

   (`migrate` and `seed` use `DATABASE_URL`, or the local database above. `docker exec -i postgres psql -U postgres -d swiftdrop < schema.sql` does the same as `migrate`.)

   Re-run it whenever you pull changes: it is safe to repeat and upgrades an existing database in place (for example it adds the `payments` table). Do the same for the test database below.

3. Run the API:

   ```bash
   go run ./cmd/api
   ```

   The server listens on `127.0.0.1:8080` (set `LISTEN_ADDR` to change that; the Docker image uses `0.0.0.0:8080`). With no `STRIPE_KEY` set it uses the fake payment gateway and never touches the network. To try real Stripe, set `STRIPE_KEY` to a Stripe **test-mode** secret key and send a test payment method in the `/pay` body (`{"paymentMethodId": "pm_..."}`). Never use a live key while developing, and never commit a key. `powershell -ExecutionPolicy Bypass -File .\scripts\stripe-dev.ps1` does all of this for you, including the webhook listener: it asks for the key (hidden, refuses anything but `sk_test_`), fetches the matching webhook signing secret from the Stripe CLI so the two cannot disagree, opens `stripe listen` in a new window and starts the server.

   To receive Stripe's webhook as well, set `STRIPE_WEBHOOK_SECRET` to the endpoint's signing secret (the `whsec_` value). In development run `stripe listen --forward-to localhost:8080/webhooks/stripe` with the [Stripe CLI](https://docs.stripe.com/stripe-cli); it prints the secret to use. It works with the fake gateway too, so you can try it with events you sign yourself (see [curls.txt](curls.txt)).

   Sign-in is required, and nobody is signed up yet. Make the first staff account (it prints a password once; sign in and change it):

   ```bash
   go run ./cmd/user -email you@example.com -name "Your Name"
   ```

   Customers sign up themselves on the dashboard. Set `COOKIE_SECURE=true` when TLS is ended by a proxy in front of the server (a direct TLS connection is detected by itself).

   Open <http://127.0.0.1:8080/> for the dashboard. The refund job looks for due refunds every 30 seconds; set `REFUND_INTERVAL` (for example `5s`) to watch a refund happen faster. The payment reconciler looks for payments without an answer every minute (`RECONCILE_INTERVAL`). Set `ALERT_WEBHOOK_URL` (a `https://` address, set in your own shell, never in a file) to have alerts sent to a chat channel; `DASHBOARD_URL` adds a link to each message. Set `DATABASE_URL` to use another database, for example a throwaway one to demo the dashboard without your test orders.

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

The dashboard has its own tests, run from `web/ui` (`npm install` once first):

```bash
cd web/ui
npm test          # its rules with node --test, then the screens with Vitest
npm run typecheck
npm run build     # refreshes web/static, which Go embeds; commit the result
```

The end-to-end test (`e2e/`) needs a running server and is skipped unless `SWIFTDROP_E2E_URL` is set. Against the Docker stack, with a staff account made as in the quickstart (start the stack with `REFUND_INTERVAL=2s` so the refund step is quick):

```bash
SWIFTDROP_E2E_URL=http://127.0.0.1:8080 SWIFTDROP_E2E_STAFF_EMAIL=you@example.com SWIFTDROP_E2E_STAFF_PASSWORD='your password' go test -count=1 -v ./e2e
```

It adds a restaurant of its own and a customer of its own each run, and leaves them behind.

The screens are tested in a simulated browser against a fake server (sign-in, roles, the order actions, signing out). A change to how it looks still needs a look in a real browser: `npm run dev` serves the page on port 5173 and sends API calls to the Go server on 8080. The Go tests check that `web/static` is a whole build (every file the page loads is there) and that the dashboard's source never turns text into markup or code.

## API

| Method | Path                   | Description                                                                              |
|--------|------------------------|--------------------------------------------------------------------------------------------|
| GET    | `/health`              | Health check                                                                               |
| POST   | `/auth/signup`         | `{email, name, password}`: make a **customer** account and sign it in. `201` with `{user, csrfToken}` and the session cookie; `422` names every field that is wrong; `409` if the email has an account; `429` after 10 sign-ups an hour from one address |
| POST   | `/auth/login`          | `{email, password}`: `200` with `{user, csrfToken}` and the session cookie; `401` with the same answer for a wrong password and an unknown email; `429` with `Retry-After` while locked |
| GET    | `/auth/me`             | Who is signed in, and the CSRF token the page must send back. `401` when nobody is |
| POST   | `/auth/logout`         | End this session and clear the cookie (`204`) |
| GET    | `/users`               | Staff only. Accounts, newest first: `{"accounts": [...]}`. Filters `?role=staff\|customer`, `?status=active\|disabled`, `?q=` (part of the email or name), `?limit=` (default 100, at most 500); `400` for a value that is none of those. Never holds a password or a hash |
| POST   | `/users`               | Staff only. `{"email", "name", "role": "staff"\|"customer"}` makes an account and answers `201` with `{"user", "temporaryPassword"}`: the password is shown only here, and the person must replace it at first sign-in. `422` names the bad fields, `409` an email that has an account |
| GET    | `/users/{id}`          | Staff only. `{"user", "events"}`: the account and its history (created, disabled, enabled, password reset, password changed), newest first, each naming who did it. `404` unknown |
| POST   | `/users/{id}/disable`, `/enable` | Staff only. `200` with the account. Disabling ends all its sign-ins in the same transaction; doing it twice changes nothing. `409` for your own account or for the last active staff account; `404` unknown |
| POST   | `/users/{id}/reset-password` | Staff only. A new temporary password, answered once as `{"user", "temporaryPassword"}`; ends all the account's sign-ins and makes the person choose their own. `409` for your own account (use `/auth/password`); `404` unknown |
| POST   | `/auth/password`       | `{currentPassword, newPassword}`: change the password and end every other sign-in (`204`; `403` wrong current password; `422` weak new one) |
| GET    | `/restaurants`         | Open restaurants, `{"restaurants": [...]}` (staff may add `?all=true` for closed ones too) |
| GET    | `/search`              | Find food: `?q=` (words, up to 80 characters), `?lat=&lng=` (together; the customer's position, rounded to about 110 m and not kept), `?veg=true`, `?maxPrice=` (cents). `{"results": [{"restaurant", "distanceKm"?, "items"}], "located": bool}`. Only open restaurants and available items; with a position, only restaurants that deliver there, nearest first among equal matches; never cached. `400` for a bad parameter |
| PUT    | `/restaurants/{id}/place` | Staff only. `{"cuisine"?, "latitude"?, "longitude"?, "deliveryRadiusKm"?}` replaces the restaurant's place: what is left out is cleared (no place, no cuisine) or reset (radius 5 km). Latitude and longitude go together. `422` names the bad fields, `404` unknown |
| POST   | `/restaurants`         | Staff only. `{"name"}` opens a restaurant (`201`). `422` names the bad field |
| PATCH  | `/restaurants/{id}`    | Staff only. `{"name"?, "active"?}` renames, opens or closes (`404` unknown, `422` bad or empty) |
| GET    | `/restaurants/{id}/menu` | `{"restaurant", "items"}`. A customer gets only the available items of an open restaurant (a closed one is `404`); staff with `?all=true` get everything |
| POST   | `/restaurants/{id}/menu` | Staff only. `{"name", "description"?, "priceCents", "veg"?}` adds an item (`201`); the price is 0 to 1,000,000 cents. `422` names every bad field |
| PATCH  | `/menu/{id}`           | Staff only. Any of `{"name", "description", "priceCents", "available", "veg"}`. A new price applies to orders placed from then on; placed orders keep theirs |
| GET    | `/geocode`             | Address search: `?q=` (3 to 120 characters) answers `{"candidates": [{label, line1, city, postalCode, latitude, longitude}]}`, up to 5, best first. `503` when the server has no geocoder (`GEOCODER`), `502` when the service is down, `429` with `Retry-After` after 30 searches a minute, `400` for a bad `q`. Never cached |
| GET    | `/geocode/reverse`     | The address at a point: `?lat=&lng=` answers `{"candidate": {...}}` or `{"candidate": null}`; the same `503`, `502`, `429` and `400` |
| GET    | `/addresses`           | The signed-in person's saved addresses, oldest first: `{"addresses": [...]}` |
| POST   | `/addresses`           | Save one: `{"line1", "latitude", "longitude"}` and optionally `label`, `landmark`, `city`, `postalCode` (`201`). `422` names the bad fields (a place is required: an address that is not anywhere cannot be delivered to), `409` when they already keep ten |
| PUT    | `/addresses/{id}`      | Replace one (what is left out is cleared). Orders already placed keep their own copy. `404` if it is not theirs |
| DELETE | `/addresses/{id}`      | Remove one (`204`). `404` if it is not theirs: someone else's is the same as one that is not there |
| POST   | `/orders`              | Create an order (always starts `created`; it must say where it is going, and its restaurant must deliver there; a customer's lines are menu items by `menuItemId` and the server prices them, `422` says what does not fit the menu); `422` rule failure, `400` bad body, `415` wrong Content-Type, `413` over 1 MB, `409` duplicate id |
| GET    | `/orders`              | The newest orders, `{"orders": [...]}`, each as in `GET /orders/{id}`. `?limit=N`, default 50, at most 200 (`400` otherwise). Read in one query, so an order and its payment are from the same moment |
| GET    | `/orders/{id}`         | Get an order by ID (`404` if missing), with a `payment` summary and a `paymentIssue` when there is something to tell the customer                                                      |
| POST   | `/orders/{id}/pay`     | Idempotent. A new payment is claimed (`created → payment_pending` plus a `payments` row), authorized through the order's provider, then recorded: `paid`, or on a decline a `402` with the reason, `canRetry` and `attemptsLeft` (the order returns to `created`; paying again is a NEW attempt with its own payment, up to 3, and the last decline cancels the order). A retry of a finished payment replays its outcome (`200` with the order, or the same `402` once the order is closed) without charging again; `409` "still being processed" while another request holds the payment, or if the order moved on meanwhile; `409` for an order that cannot be paid from its state; `422` if the payment method is missing or not a Stripe id (nothing is claimed), `400` for a body that is not JSON; `409` with a refund promise if the order was cancelled while the payment was being processed; `500` if the provider errors (the payment stays `processing`; the webhook records the outcome if Stripe took the charge, or a retry after its 60 s lease takes it over) or the stored payment type has no provider |
| POST   | `/webhooks/stripe`     | Stripe's push of a payment outcome (`payment_intent.succeeded` or `.payment_failed`) or of a refund that failed after being accepted (`refund.failed`, `refund.updated`), signed with `STRIPE_WEBHOOK_SECRET`. Not for browsers or clients. `200` with `recorded`, `already recorded`, `order moved on`, `unknown payment` or `ignored`; `400` if the signature, timestamp or body fails verification; `404` while no secret is configured; `500` only if our storage fails, so Stripe retries |
| GET    | `/orders/{id}/payment-history` | The ledger of the order's payments, oldest first: `{"events": [{id, paymentId, at, kind, detail}]}`. For the people running the service (it names provider ids and errors), not for a customer. `404` for an unknown order |
| GET    | `/alerts`              | The problems a person has to look at, newest activity first: `{"alerts": [{id, kind, subject, orderId, message, occurrences, createdAt, lastSeenAt}]}`. Open ones only; `?all=true` adds the resolved ones. For the people running the service |
| POST   | `/orders/{id}/refund`  | Staff only. Record a refund a person made by hand after the system gave up on it or the provider reversed it: `{"reference": "re_...", "note": "paid from the dashboard"}` (reference required, up to 200 characters; note optional, up to 500). Works only on a payment whose refund `failed`; answers `200` with the order, `409` with the reason for anything else (still being refunded, already done, never tried, never charged, no payment), `422` for a bad field. It never moves money or asks the provider for anything. Who recorded it is the signed-in account |
| POST   | `/alerts/{id}/resolve` | Close an alert, optionally with a note: `{"note": "refunded by hand"}` (no body is fine). `404` unknown id, `409` already resolved, `400` bad id or body |
| POST   | `/orders/{id}/accept`  | Restaurant accepts: `paid → preparing` and a stub driver is assigned; `409` otherwise       |
| POST   | `/orders/{id}/cancel`  | Cancel an order (`409` if its current state can't be cancelled)                            |
| POST   | `/orders/{id}/ready`   | Mark the food ready; only while `preparing`, otherwise `409` (also if the order changed while the request ran). State stays `preparing`      |
| POST   | `/orders/{id}/pickup`  | `preparing → picked_up`; `409` unless food is ready **and** a driver is assigned           |
| POST   | `/orders/{id}/deliver` | `picked_up → delivered`; `409` from any other state                                        |

Every lifecycle endpoint answers `404` for an unknown order and `409` for a move the order's current state doesn't allow. Errors are always `{"error": "..."}`.

A create request names where the order goes, either `"deliveryAddressId": "<a saved address of the customer>"` or `"deliveryAddress": {"line1": "12 MG Road", "latitude": 12.9719, "longitude": 77.6412}` (with optional `label`, `landmark`, `city`, `postalCode`), never both and never neither. A create request from a customer looks like this (`paymentType` is one of `card`, `cash`, `wallet`; unknown fields are rejected, and the server owns `state`, `foodReady`, `assignedDriverId` and every price). `customerId` may be left out: it is the signed-in customer:

```json
{
  "id": "order-1",
  "restaurantId": "demo-corner-kitchen",
  "paymentType": "card",
  "deliveryAddress": { "line1": "45 CMH Road, Indiranagar", "city": "Bengaluru", "latitude": 12.9719, "longitude": 77.6412 },
  "items": [{ "menuItemId": "demo-smash-burger", "quantity": 2 }]
}
```

Staff must say whose order it is (`customerId`, any text for a walk-in) and may add a custom line, which carries its own name and price: `{ "sku": "CAKE", "name": "Birthday cake", "quantity": 1, "unitPriceCents": 4200 }`.
