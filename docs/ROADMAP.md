# Roadmap, and the rules for the daily agent

This file is the task list for the scheduled agent that works on SwiftDrop each weekday morning. You
steer it by editing this file: reorder items, edit them, add `NEEDS DECISION:` to hold one back, or
delete what you do not want. The agent ticks items off and writes what it did in the **Log** at the
bottom, on its own branch.

## Rules the agent must follow

These are the user's standing rules for this project. They override anything an item says.

1. **Start only from a clean state.** Work in `C:\Users\saivi\swiftdrop`. Run `git status` and
   `git branch --show-current`. If the working tree is not clean, or the current branch is not `main`,
   **stop and report** that: a person is probably in the middle of something. Touch nothing.
2. **Never push, never merge, never open a pull request, never force anything, never delete a
   branch or a tag, never rewrite history.** Work on a new local branch named
   `agent/YYYY-MM-DD-<short-slug>` created from `main`, and commit there. Do not commit on `main`.
3. **Never handle secrets.** Do not ask for, read, print, write or repeat any key, token or password.
   Keys such as `STRIPE_KEY` and `GEOCODER_KEY` live only in the user's own shell. Test credentials
   you create yourself for a local throwaway are fine and must not be printed in the report.
4. **Take the first unticked item that is not marked `NEEDS DECISION`.** Do one item per run. If an
   item is too big for one run, do its first self-contained slice, commit only green work, and say
   what is left in the Log. If every item is ticked or held, run the health check at the end of this
   file instead and report.
5. **Follow the project's conventions** (read `README.md`, `KNOWN_ISSUES.md` and the code first):
   - Two commits per slice, code first and tests second, then a docs commit. Commit messages explain
     why, and end with the `Co-Authored-By` line the environment gives you.
   - Keep 100% statement coverage in `cmd/handlers` and the packages that have it. Test the edges.
   - Go files must be gofmt-clean. The working copy has CRLF line endings on Windows, so check with
     `tr -d '\r' < file | gofmt -d`, and never reformat unrelated files.
   - Run Go race tests from **PowerShell**, not Git Bash, with
     `SWIFTDROP_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5434/swiftdrop_test?sslmode=disable`
     and `go test -race -count=1 ./...`. If the container `postgres` is stopped, start it
     (`docker start postgres`, start Docker Desktop first if needed) and apply the schema with
     `DATABASE_URL=... go run ./cmd/migrate` to `swiftdrop_test`.
   - The dashboard's committed build in `web/static` must be built **on Linux from committed
     sources**, never from the Windows build: commit the sources first, then
     `git archive HEAD` into a scratch folder, and in a `node:24-alpine` container strip CRs from
     `web/ui` and `web/static`, run `npm ci && npm run build`, copy the result back (delete the two
     old hashed files with `git rm`, naming them), and commit it. CI fails if it differs. Use
     `MSYS_NO_PATHCONV=1` for `docker run` from Git Bash.
   - Write scripts and patches with the Write tool (a Bash heredoc containing apostrophes breaks).
     A security hook also blocks file content that names one particular Python serialization module,
     so avoid that module's name, even in prose.
   - Put scratch files in the session scratchpad, not the repo.
   - **A throwaway worktree must be self-contained.** Never link `node_modules` (or anything else
     that lives outside it) into a worktree with a junction or a symlink: `git worktree remove
     --force` follows the link and deletes the real folder. This happened on 2026-11-12 and
     `web/ui/node_modules` had to be reinstalled. For web mutation checks, change the file in the
     main working copy on your own branch and restore it with `git checkout -- <file>` (the tree must
     be clean before and after), or run `npm ci` inside the worktree. Go mutation checks need no
     `node_modules`. Before removing any worktree, look at what is in it.
6. **Leave things as you found them.** At the end: be back on `main` with a clean tree (your work is
   on your branch), stop the `postgres` container and any `swiftdrop` compose stack you started
   (`docker compose down`, keep volumes), and do not touch the `kind` clusters, which belong to other
   projects.
7. **Be honest in the report.** Say what you built, what passed (with the commands), what you did not
   or could not check, and anything you are unsure about. Never describe red tests as green. If you
   could not get something working in reasonable effort, stop, commit nothing broken, and say so.
8. **Size of a run: about an hour of real work.** Spend it on a whole vertical slice (design,
   schema, store, server, dashboard, a live check, docs), not on writing tests for their own sake:
   tests are how you prove the work, not the work. Think about the design first and write it down in
   the Log (what you chose and why, what you rejected). Put the effort where the risk is:
   concurrency, authorization (who may do what), failure paths, data that must not be lost or
   doubled. For any new core rule, run a quick mutation check on the fast packages (copy the idea of
   changing one operator or condition at a time in a throwaway `git worktree`, run that package's
   tests, and fix every survivor with a test), and say how many mutants you ran.
9. **Look at it working.** When an item changes what a person sees, bring up the Docker stack
   (`docker compose up -d --build`, `go run ./cmd/seed`, make a throwaway staff or customer account
   with `/app/user` or the sign-up form), use the browser tools to click through the real flow, and say
   in the report what you saw. Then `docker compose down`.
10. **Definition of done: do not report until every box is true, and say so in the report.**
    - The item is ticked (or the Log says exactly what is left), and the Log entry is committed on
      your branch (design choices, commands, results).
    - There is a **docs commit** (README, CHANGELOG "Unreleased", KNOWN_ISSUES as the item needs).
    - If anything under `web/ui` changed, `web/static` is **rebuilt on Linux from the committed
      sources and committed** (rule 5). Prove it: after committing, rebuild again in a scratch copy and
      `diff -r` it against `web/static`; the diff must be empty.
    - You looked at it working in the browser against the real stack (rule 9), and the report says what
      you saw (or why you could not).
    - A run is about an hour. If you are done in far less, you have not finished: review your own
      diff against the item and the decisions, look for the cases the item names that you skipped,
      run the mutation check on new core rules, and read the code as a reviewer would.
    - **You are back on `main` with a clean tree.** Run `git switch main`, then print
      `git branch --show-current` and `git status --short` and put that output in the report. Docker
      containers you started are stopped.
11. **Do not edit this Rules section.** You may tick items and append to the Log.

## Decisions already made for the customer view (edit if you disagree)

- One web app, **role-based shell**: a signed-in customer gets the customer view, staff keep today's
  dispatch dashboard unchanged. No new routes or bundles.
- The **cart lives in the browser** (`localStorage`, one cart per signed-in user id, wrapped in
  try/catch because storage can be blocked) and holds **one restaurant at a time**: adding an item
  from another restaurant asks before it empties the cart.
- The **tracker is status only** (no map, no driver name): the drivers are still a stand-in.
- Reuse `web/ui/src/lib/logic.js` (`timeline`, `noticeFor`, `actionsFor`, `money`), `NearbySearch`,
  `DeliverTo`, `hooks/data.ts` and the existing API. Add server routes only if an item needs one, with
  tests and docs.
- The server remains the authority on prices, delivery radius and who may do what; the view only
  draws. Never put a price in a request.

## Items

Tick an item by changing `[ ]` to `[x]` and add a line to the Log.

- [x] **Customer shell.** In `web/ui/src/App.tsx`/`Shell.tsx`, render a new `CustomerApp` for
  `role === 'customer'` and the existing `Shell` for staff. `CustomerApp` has a header (greeting,
  account button, cart button with a count) and three tabs: **Food** (home), **Orders**, **Account**.
  Nothing from the dispatch dashboard (stage columns, stats tiles, alerts, People, Menu admin)
  appears for a customer. Reuse `AccountSheet`, the theme and the toasts. Tests: a customer sees the
  three tabs and none of the dispatch things; staff still see the dashboard; the account sheet works.
- [ ] **Food home and restaurant page.** Food tab: `DeliverTo` on top, `NearbySearch` below, and
  restaurant cards (name, cuisine, distance when there is an address, open/closed) from
  `GET /restaurants` and `GET /search`. Tapping a card opens a **restaurant page** with the whole menu
  (`GET /restaurants/{id}/menu`): items with price, description, a Veg pill, a vegetarian-only
  filter, a search box that filters the menu on the page, and `+` / `-` steppers that put items in the
  cart (the cart is the next item; until then keep a minimal in-memory cart object behind one hook,
  `useCart`, so the next item only changes its storage). Tests for the cards, the page, the filters and
  the steppers.
- [ ] **Cart and checkout.** `useCart` persists to `localStorage` per user id (safe when storage
  throws), one restaurant at a time with a confirm before switching, quantities 1 to 50, a running
  total from the menu prices it holds (display only), and a **cart page**: the lines with steppers and
  remove, the restaurant, the **delivery address** (`DeliverTo`), how to pay (card, wallet, cash), the
  total, and **Place order** that sends `{id, restaurantId, paymentType, deliveryAddressId|deliveryAddress,
  items: [{menuItemId, quantity}]}`. On success clear the cart and open the order in the tracker. Show
  the server's words for every 422 (closed restaurant, sold out, too far). Tests: persistence,
  switching restaurants, limits, the request body, every error path, a blocked `localStorage`.
- [ ] **Order tracker.** Orders tab: the customer's orders (`GET /orders`, polled like the dashboard,
  newest first, with a friendly status line and total), and an **order page** with a vertical status
  timeline (`timeline(order)`), the plain-language message (`noticeFor(order)`), the items, the total,
  the **delivery address**, and the actions a customer may take (`actionsFor(order, 'customer')`:
  pay, cancel, with a two-tap confirm for cancel). Payment states (declined with attempts left,
  refund on the way, refunded) read as sentences. Tests for each state in the timeline and each action.
- [ ] **Customer journey test and docs.** Extend `e2e/` (or add a component-level journey test if a
  browser is impractical in CI) so a customer signs up, picks an address, finds food, fills a cart,
  orders, and reads the tracker. Update `README.md` (what the customer sees), `CHANGELOG.md`
  (Unreleased), `KNOWN_ISSUES.md` (what the customer view still lacks: no map, no driver, no
  notifications) and remove the old customer use of the order sheet only if nothing else needs it.

### Heavier items (each is a day's agent run, or several; do the first self-contained slice and say what is left)

- [ ] **Real drivers and dispatch.** The "driver" on an order is a random id made up when the kitchen
  accepts it. Replace it with the real thing, in slices (one slice per run; tick the item only when all
  four are done, and log each slice):
  1. *Drivers exist.* Add `RoleDriver` (`'driver'`) to `internal/domain` and the `users.role` check in
     `schema.sql` (an in-place upgrade that works on an existing database), let staff make driver
     accounts under **People** (role choice), a `driverOnly` guard, and a `drivers` table (user id,
     `online` bool, `latitude`/`longitude` nullable, `location_at`, `updated_at`) with store methods
     behind a port (`ports.DriverStore`) and an in-memory fake. Routes: `POST /driver/online`,
     `POST /driver/offline`, `POST /driver/location` (validated point, rate limited), `GET /driver/me`.
  2. *Dispatch decides.* A package `internal/dispatch` with a pure, well tested function that picks the
     best online, free driver for an order (nearest to the restaurant by `domain.DistanceKm`, within a
     configurable `DISPATCH_RADIUS_KM`, default 8, ties by longest idle) and a **claim** that offers the
     order to one driver at a time with a lease (60 seconds, timed by the database clock, like the payment
     and refund claims), a decline or expiry moves on to the next driver, an append-only
     `order_offers` history (who was offered, when, how it ended), and a background job (like
     `internal/refund`) that offers orders that are `preparing` and have no driver, every
     `DISPATCH_INTERVAL` (default 10s), safe with several servers running it. Accepting an offer sets
     `assigned_driver_id` in one transaction with the offer's outcome. No random ids remain: delete the
     stand-in in `AcceptOrder`.
  3. *The driver's routes.* `GET /driver/offers` (the one open offer, with restaurant, pickup address and
     distance), `POST /driver/offers/{id}/accept|decline`, `GET /driver/job` (the current delivery with
     the customer's delivery address and landmark), and the driver may `pickup` and `deliver` **only
     their own job** (a driver on someone else's order is a 404). Staff keep every action. A driver must
     not see customer data beyond the delivery address of their own current job.
  4. *Screens.* A role-based **Driver** view (online toggle that asks for the browser's position only
     when pressed and then sends it every 30 seconds while online, the offer card with a countdown, the
     job with the address and buttons Picked up and Delivered), the staff dashboard shows the driver's
     name instead of an id and a **Drivers** list (online, free or busy, last seen), and the customer
     tracker (when it exists) shows the driver's first name.
  Concurrency matters most here: tests and a race test for many orders and drivers at once (an order is
  never offered to two drivers at the same time, a driver never holds two jobs, a lost lease is never
  completed by the old holder). Document it in `docs/PAYMENT_FLOW.md`-style prose in a new
  `docs/DISPATCH.md` with a state diagram (Mermaid) and update `KNOWN_ISSUES.md`.
- [ ] **Email notifications and self-service password reset.** Build the email side properly with an
  outbox: a `notifications` table (id, user id, kind, payload JSON, status `pending|sent|failed`,
  attempts, lease, send-after, created, sent) written **in the same transaction** as the change that
  caused it (order paid, picked up, delivered, cancelled, refund sent, refund failed), a `Sender`
  port with an SMTP adapter (settings in the user's shell: `SMTP_URL` like
  `smtp://user:pass@host:587`, `MAIL_FROM`; never logged), a **log sender** for development that prints
  the message and a **recording sender** for tests, and a background job (copy the shape of
  `internal/alertnotify`: lease, backoff, give up after N, one message once even with several servers).
  Templates are plain Go templates in `internal/mail/templates`, text and HTML, with the order summary
  and the delivery address, reusing the customer messages in `logic.js` as the wording source of truth
  (port the sentences, keep them in step with a test). A customer may turn each kind off in their
  account (a `notification_prefs` JSON on the user, shown in the Account sheet). **Password reset by
  email:** `POST /auth/forgot` (always answers the same 202, whatever the email, rate limited per address
  and per IP) creates a single-use token (random, only its SHA-256 stored, expires in 30 minutes) and
  emails a link; `POST /auth/reset` takes the token and a new password (same strength rules), ends every
  session of the account, marks the token used, and a used or expired token is refused with one message.
  Staff-reset temporary passwords stay as they are. Screens: "Forgot your password?" on sign-in and the
  reset page. Think hard about enumeration (timing and wording), token handling and the outbox
  guarantees; write them up in `docs/NOTIFICATIONS.md`.
- [ ] **Restaurant and menu import and export.** Staff upload a CSV (or JSON) of restaurants and menus:
  `POST /import/menu` (multipart or raw CSV, up to 2 MB) with columns `restaurant_ref, restaurant_name,
  cuisine, latitude, longitude, radius_km, item_ref, item_name, description, price, veg, available`
  where `*_ref` are the importer's own stable ids stored as `external_ref` (new nullable unique columns
  on both tables, so re-importing **updates** instead of duplicating, and never deletes). It validates
  every row with the same rules as the forms and answers **all** the problems by line number; `?dry_run=true`
  changes nothing and says what would be created, updated or left alone; a real run is one transaction
  (all or nothing). Prices are parsed with the dashboard's decimal rules (never floating point).
  `GET /export/menu.csv` gives the same format back, so export then import is a no-op (test that round
  trip). Dashboard: an **Import** sheet under Menu with a file chooser, a preview table and per-line errors,
  and an **Export** button. Streaming, size limits and CSV edge cases (quotes, commas, BOM, CRLF,
  unicode) deserve real care; document the format in `docs/IMPORT.md` with an example file under
  `docs/examples/`.
- [ ] **Deploy for real: TLS, metrics, backups.** Add a `docker-compose.prod.yml` override with a
  Caddy reverse proxy (automatic HTTPS for a domain from `SWIFTDROP_DOMAIN`, `tls internal` for local
  trials), the API no longer published, `COOKIE_SECURE=true`, secrets read from files
  (`*_FILE` variants of the existing settings, so nothing sensitive is in an environment listing) and
  the database password required rather than defaulted. Add `GET /metrics` (Prometheus text format,
  staff session or a bearer token from `METRICS_TOKEN_FILE`) with request counts and latency by route
  and status, the oldest unprocessed refund, open alerts, queued notifications and the dispatch queue
  (add a tiny dependency-free exposition writer or the official client, your choice, with the reason in
  the Log), structured JSON request logs with a request id that never include cookies, tokens, addresses or
  coordinates, `scripts/backup.ps1` and `scripts/backup.sh` (`pg_dump` to a dated file, keep the last N)
  and `scripts/restore-test.sh` that restores the latest dump into a scratch database and runs a
  sanity query, and `docs/DEPLOY.md` (domain, ports, first staff account, secrets, backups, upgrade by
  `migrate`, what to monitor). Add a CI job that validates both compose files (`docker compose config`)
  and starts the production override against a local `tls internal` proxy and checks `/health` over
  HTTPS.
- [ ] **Pickup orders.** A customer may choose **pickup** instead of delivery: the order has a
  `fulfilment` (`delivery` default, `pickup`), needs no address and no radius check, skips driver
  dispatch (the kitchen marking it ready is the last step before the customer collects it: add a
  `ready_for_pickup`-style path to the state machine in `internal/order` with its transition table
  and tests, and a **Collected** staff action), and the customer tracker and messages say "ready to
  collect at <restaurant>". Think through every place the lifecycle is assumed to be linear (refunds,
  cancellation windows, the reconciler, `actionsFor` in `logic.js`, the dashboard stage columns) and fix
  each. Update the order sheet / cart, the staff phone-order form, the docs and `KNOWN_ISSUES.md`.

## Health check (when there is nothing to build)

Read-only: confirm `main` equals `origin/main` (`git fetch` is fine; do not pull or push), run the Go
race tests and `npm test` in `web/ui`, read the latest GitHub Actions run for `main` from the public API
(`https://api.github.com/repos/vishalsaik/Swiftdrop/actions/runs?per_page=1`), list untracked files and
stale branches, and report what needs a person's attention.

## Log

(The agent appends one entry per run, newest last: date, branch, what it did, the commands it ran and
their results, what is left.)

### 2026-11-11, `agent/2026-11-11-customer-shell`: Customer shell (first run, then reviewed and finished by Claude)

- **Agent run (07:40 to 07:48, about 8 minutes):** `CustomerApp` for `role === 'customer'` (App.tsx picks the shell by role,
  keyed by the user's id), three tabs, a header with the cart button, `useCart` as an in-memory placeholder, the order list
  and drawer reused with customer-only actions, 16 new tests, five existing test files adapted to the new customer path.
  Commits: 0511f5c (code), 84058c3 (tests). It did not tick the item, write a Log entry or docs, rebuild `web/static`, or
  switch back to `main`.
- **Review (Claude, on the user's word):** `tsc` clean, web tests 152 passing (136 before). Live on a throwaway compose
  project (demo seed): a new customer sees Food, Orders, Account and none of stage columns, totals, alerts, Menu or
  People, places an order and lands on Orders with it open, the drawer offers only Pay now and Cancel order, and no
  alerts, users or payment-history request was made; the cart sheet, account tab and a 375 px phone layout (tab bar at the
  bottom, no sideways scroll) work. Six hand mutants of the new code: five caught, one survived (removing the `key` on
  `CustomerApp`, so one account's tab and open order would survive a direct account switch).
- **Fixes (Claude):** `Gate` exported so a test can switch accounts inside one session (a8f6165); a test that signs in as
  another customer without the sign-in screen in between and checks the Food tab and no order open (c4af1a0), after which all
  six mutants are caught; `web/static` rebuilt on Linux from the committed sources (656d423) and proved identical to a
  second fresh Linux build of HEAD (`cmp` on all three files); docs (this commit). Not done by anyone yet: the restaurant
  page, the real cart and the tracker (the next three items).
- **Design notes:** a role-based shell in one bundle (decided in advance); the cart behind one hook so only that file changes
  when it gets `localStorage`; `AccountBody` and `OrderRow` exported for reuse rather than copied. Rejected: a separate
  route or bundle for customers (more to build and to keep in step, and the server checks the role anyway).
