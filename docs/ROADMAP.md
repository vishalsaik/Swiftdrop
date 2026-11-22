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
- [x] **Food home and restaurant page.** Food tab: `DeliverTo` on top, `NearbySearch` below, and
  restaurant cards (name, cuisine, distance when there is an address, open/closed) from
  `GET /restaurants` and `GET /search`. Tapping a card opens a **restaurant page** with the whole menu
  (`GET /restaurants/{id}/menu`): items with price, description, a Veg pill, a vegetarian-only
  filter, a search box that filters the menu on the page, and `+` / `-` steppers that put items in the
  cart (the cart is the next item; until then keep a minimal in-memory cart object behind one hook,
  `useCart`, so the next item only changes its storage). Tests for the cards, the page, the filters and
  the steppers.
- [x] **Cart and checkout.** `useCart` persists to `localStorage` per user id (safe when storage
  throws), one restaurant at a time with a confirm before switching, quantities 1 to 50, a running
  total from the menu prices it holds (display only), and a **cart page**: the lines with steppers and
  remove, the restaurant, the **delivery address** (`DeliverTo`), how to pay (card, wallet, cash), the
  total, and **Place order** that sends `{id, restaurantId, paymentType, deliveryAddressId|deliveryAddress,
  items: [{menuItemId, quantity}]}`. On success clear the cart and open the order in the tracker. Show
  the server's words for every 422 (closed restaurant, sold out, too far). Tests: persistence,
  switching restaurants, limits, the request body, every error path, a blocked `localStorage`.
- [x] **Order tracker.** Orders tab: the customer's orders (`GET /orders`, polled like the dashboard,
  newest first, with a friendly status line and total), and an **order page** with a vertical status
  timeline (`timeline(order)`), the plain-language message (`noticeFor(order)`), the items, the total,
  the **delivery address**, and the actions a customer may take (`actionsFor(order, 'customer')`:
  pay, cancel, with a two-tap confirm for cancel). Payment states (declined with attempts left,
  refund on the way, refunded) read as sentences. Tests for each state in the timeline and each action.
- [x] **Customer journey test and docs.** Extend `e2e/` (or add a component-level journey test if a
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

### 2026-11-12, `agent/2026-11-12-food-home`: Food home and restaurant page

- **Built:** `FoodHome` (DeliverTo, NearbySearch, a card per open restaurant from `GET /restaurants`), `RestaurantPage`
  (whole menu from `GET /restaurants/{id}/menu`, Veg pill, vegetarian filter and search box filtering on the page, steppers),
  and `useCart` with `setQuantity`/`quantityOf`/`totalCents`, still in memory (10500ed). Pure rules in `logic.js`:
  `distanceKm` (the server's haversine), `restaurantCards`, `cardStatusText`, `filterMenu`.
- **Design choices:** a card's distance and "delivers here" are computed in the browser from the restaurant's place and radius
  with the server's formula and rule (`away > radius` is too far, the radius itself delivers, unplaced takes no delivery
  orders), rather than from `GET /search?lat&lng`: search only returns restaurants with matching available items (30 at
  most), so it cannot tell "too far" from "nothing on the menu". Display only; the server checks again on Place order.
  "Closed" cannot reach a customer's list (the server hides closed restaurants), so a restaurant page that gets a 404 says it
  is not taking orders. The restaurant page is a view inside the Food tab (state, not a route), as the shell decision says.
  Another restaurant's item starts a new cart after a warning on the page (the confirm is the next item's); Add in the search
  results never replaces another restaurant's cart unseen. **A bridge until the cart page exists:** the cart sheet lists the
  lines and a display total, and **Check out** opens the existing order sheet filled from the cart (restaurant, what is still
  on the menu, the chosen address; new optional `start` prop on `NewOrderSheet`); a placed order empties the cart. "Start an
  order" stays on the Food home (the last customer item decides whether the order sheet stays for customers); while the order
  sheet is open the Food home's own address and search are not drawn underneath, so there is one of each. Rejected: a modal
  restaurant sheet (the menu is long and a page reads better on a phone), and filtering the menu on the server (it is already
  loaded whole).
- **Bugs found and fixed on the way:** going from the cart sheet straight to the order sheet, the cart `<dialog>`'s late
  `close` event set the sheet to none and the order sheet shut as it opened (65acece: each sheet now closes only itself);
  search Add replacing another restaurant's cart before the warning could be read (3b75d67).
- **Tests:** 8 new `node --test` rules tests (`src/lib/food.test.mjs`), 27 new component tests (`FoodHome.test.tsx`: cards
  with and without an address, loading, empty, failing; the page, distance, filters asking the server nothing, a closed
  restaurant, a failing menu, going back; steppers 0 to 50 and the count; switching restaurants; search Add; Check out's body
  holding only menu ids and counts, the address, sold-out lines left out, the cart emptied; `useCart` edges).
  `npm test`: 90 node tests and 180 vitest tests passing; one full run had a 5 s timeout in the untouched staff
  `Effects.test.tsx` (orb beside a confirming payment, 5.9 s) while Docker was busy; it passed alone and in two more full runs.
  `tsc --noEmit` clean.
- **Mutation check:** 25 hand mutants of `restaurantCards`, `distanceKm`, `filterMenu`, `cardStatusText` and `useCart`, in a
  throwaway worktree: 24 killed. The survivor (removing `Math.min(1, a)` in the haversine) is equivalent in JavaScript: the
  sum overshoots 1 by one rounding step and `Math.sqrt` rounds it back; the cap stays to match the server, and a test
  documents the antipodes case (fef3c13). Side effect: removing that worktree with `--force` followed its `node_modules`
  junction and emptied `web/ui/node_modules`; restored with `npm ci` from the lockfile (git-ignored, nothing tracked changed).
- **Go:** no Go changed. `go vet ./...` clean; `go test -race -count=1 ./...` from PowerShell against `swiftdrop_test` (port
  5434, schema up to date via `cmd/migrate`): every package ok.
- **web/static:** rebuilt on Linux (`node:24-alpine`, CRs stripped, `npm ci && npm run build`) from the committed sources twice
  (826343f, fd075a2); after each, a second fresh Linux build of HEAD was `diff -r` identical to the committed `web/static`.
- **Live check** (throwaway compose project `sdagent`, demo seed, fake gateway, demo geocoder): signed up a throwaway customer;
  the Food home showed three cards, all Open; searching "Indiranagar" and choosing 12 100 Feet Road re-sorted them to Noodle
  Bar 50 m and Corner Kitchen 700 m (delivers) and Harbour Grill 849 km (too far); Noodle Bar's page showed both items with
  prices, description and the Veg pill; the vegetarian filter left Spring rolls; steppers made "View cart · 3 items · $34.30";
  the cart sheet listed 2 × Pad thai and 1 × Spring rolls; Check out opened the order sheet with that address and those
  quantities; Place order landed on Orders with SD-VMFFV at $34.30 and an empty cart. At 375 px: no sideways scroll, one
  column of cards, the cart bar above the bottom tab bar. Checked through page text and measured layout; screenshots timed
  out (the app window was likely behind another), so nothing was looked at by eye. Stack taken down (`docker compose -p
  sdagent down`, volumes kept).
- **Left for later:** the cart in `localStorage` with a confirm on switching, and the cart page (next item).

### 2026-11-13, `agent/2026-11-13-cart-checkout`: Cart and checkout

- **Built (agent):** `useCart` in `localStorage` per account (`swiftdrop.cart.<userId>`, every read and write wrapped in
  try/catch, a `kept` flag, the `storage` event keeping tabs in step, `setQuantity` answering false instead of changing a cart
  the customer has not agreed to lose); `CartPage` (lines with steppers and Remove, `DeliverTo`, how to pay, total, Place order);
  `cartKey`, `readCart` (trusts nothing it reads back, applies the server's limits), `checkoutBody` (menu ids and counts only,
  never a price or name) and `checkoutProblem` in `logic.js`; the restaurant page's "Start a new cart?" confirm; the cart
  button opening the page. Place order keeps one order number per cart contents, so a second press or a retry after a dropped
  connection is the same order: a 409 asks `GET /orders/{id}` whether it is the customer's own (then it is done) or a number
  someone else took (then a fresh one is made).
- **Run cut short:** the agent's last Bash call returned exit 107 and the session went idle with `web/static` half rebuilt (two
  old files staged for deletion, the new ones not added) and no docs, tick or Log. Finished in review: `web/static`
  (c5f1594), these docs, the tick and this entry.
- **Review:** `tsc --noEmit` clean; `npm test` 99 node tests and 218 vitest tests pass. In one full run, under load from Docker
  and two kind clusters (files took 90 to 110 s instead of about 10), three tests hit their timeouts: the cart "survives a
  reload" test, the FoodHome stepper-to-50 test and the staff orb test in `Effects.test.tsx`; all three pass alone and in a
  re-run once the stack was down. The tests that time out are timing-sensitive, not wrong; raising `asyncUtilTimeout` in
  `src/test/setup.ts` would be the fix and has not been done.
- **Mutation check:** 13 hand mutants of `readCart`, `checkoutBody`, `checkoutProblem` and `useCart` (a count past 50, a
  negative price, a repeated item, no 30-line limit, a price sent with the order, no address needed, only the first 422 field
  shown, a cart emptied by another restaurant without `replace`, the 30-line limit off by one, another tab ignored, a new order
  number on each press, a taken number not checked): 12 killed at first. The survivor was setting another restaurant's item to 0
  emptying the cart unseen; a test now covers it with and without `replace` (0925515) and it is killed.
- **web/static:** rebuilt on Linux (`node:24-alpine`, CRs stripped, `npm ci && npm run build`) from the branch's committed
  sources (c5f1594: `index-Bbrz_A4N.js`, `index-CYZ4-Pr2.css`); a second fresh Linux build of HEAD matched the committed
  `web/static` file for file (3 files, 0 differences).
- **Go:** no Go changed.
- **Live check** (throwaway compose project `sdcart`, demo seed, demo geocoder, built-in browser): signed up a throwaway
  customer; built a Noodle Bar cart of 2 Pad thai and 1 Spring rolls ($34.30) and reloaded: the cart was still there; adding
  Fries from Corner Kitchen asked "Start a new cart?" and "Keep my cart" kept it; Place order with no address said "Choose
  where to deliver."; after choosing and saving 12 100 Feet Road, Place order pressed twice at once sent the same order number
  twice (201, then 409, then `GET /orders/SD-WCML8`) and left exactly one order, priced by the server at $34.30 with the
  address, the cart emptied and the order open in Orders. At 375 px: no sideways scroll; the empty cart and a cart with lines
  both fit. Stack taken down (`docker compose -p sdcart down -v`).
- **Found, not fixed:** the "Choose where to deliver." line stays on screen after an address is picked (until the next Place
  order); the address chosen on the Food home is not chosen again on the cart page, so the customer picks it once more (a
  saved address is one tap). Both are in `KNOWN_ISSUES.md`. Wallet and card orders are placed as before and paid from the
  order drawer ("Pay now"); the cart page does not take payment itself.
- **Left for later:** the order tracker (the Orders tab and order page polish) and the customer journey test and docs.

### 2026-11-21, `agent/2026-11-21-order-tracker`: Order tracker

- **Built:** the Orders tab lists each order with a status line in the customer's words, its items, total and money stamp
  (`CustomerOrderRow`), and an order opens as a **page** inside the tab (`OrderPage`): a vertical timeline with the current step
  marked (`aria-current="step"` and hidden words for screen readers), the payment notice (`noticeFor`), the items and total, the
  delivery address with label and landmark, one sentence about the money, and only Pay now and Cancel (two taps). Three pure rules
  in `logic.js`: `customerStatus`, `paymentSentence`, `customerTimeline` (77f5d2e). Back, Escape or the Orders tab return to the
  list; another tab and back keeps the order open.
- **Design choices:** a page, not the staff drawer (the drawer reads like a dispatch tool and covers the list on a phone); state,
  not a route, like the restaurant page. Every refund and failure sentence reads the server's `paymentIssue`, never works it out
  from the state, so the page cannot claim money is owed when the server does not (the "screen never works out refunds" rule of
  `noticeFor`). `customerTimeline` wraps `timeline` and only renames steps (cash says "Confirmed", ready food is noted on the
  kitchen step) so staff and customers share one rule for what is done. The pay form and two-tap confirm moved from
  `OrderDetail` into one hook, `useOrderActions` (with `PayForm`), used by both the staff drawer and the customer page, so the two
  cannot drift; recording a refund by hand stays staff's own form in `OrderDetail`. The page reads the polled list (every 3 s) and
  its status line is a polite live region, so it moves on by itself and is announced. Rejected: fetching `GET /orders/{id}` on the
  page (the list already holds it and is polled; a second poll doubles the load), and a separate customer copy of the pay/cancel
  code. Not done: the restaurant's name on the order (the order holds only its id) and a real card form (Pay now still asks for a
  payment method id, as the drawer did); both in `KNOWN_ISSUES.md`.
- **Tests:** 8 new `node --test` tests (`src/lib/tracker.test.mjs`) and 24 component tests (`OrderPage.test.tsx`: the list's status
  lines; every state's timeline with the current step; refunded, refund failed, refund on its way and declined read as sentences;
  the greyed-out retry while a person checks; card pay (two taps, method and CSRF sent), cash pay (one tap, no body), a 402 with
  tries left, the two-tap cancel and its 4 s timeout, a refused cancel, the page moving on with a poll, an order the list no longer
  has). Tests that described the drawer for customers (CustomerApp, CartPage, NewOrderSheet) now expect the page (f42c521).
  `npx tsc --noEmit` clean; `npm test`: 107 node tests and 242 vitest tests passing.
- **Mutation check:** 22 hand mutants of the three rules, `OrderPage`, `useOrderActions` and the tab handling in `CustomerApp`,
  applied in the working copy on this branch and restored with `git checkout --` each time: 21 killed. The survivor described any
  payment (even a declined or unsettled one the server says nothing about) as "Paid"; a test now covers it and it is killed
  (507a8e1).
- **Go:** no Go changed. From PowerShell against `swiftdrop_test` (port 5434, `cmd/migrate`: up to date): `go vet ./...` clean,
  `go test -race -count=1 ./...` every package ok.
- **web/static:** rebuilt on Linux (`node:24-alpine`, CRs stripped, `npm ci && npm run build`) from the committed sources (1e5891d:
  `index-DeS7mOwP.js`, `index-ChnbBnRn.css`); a second fresh Linux build of HEAD was `diff -r` identical to the committed
  `web/static`.
- **Live check** (throwaway compose project `sdorders` on port 8093, demo seed, fake gateway, demo geocoder, built-in browser with
  the pane hidden, so read through page text and the accessibility tree, no screenshots): signed up a throwaway customer, put 2 Pad
  thai and 1 Spring rolls from Noodle Bar in the cart, chose 12 100 Feet Road with a landmark and placed the order: it opened on its
  page, "Waiting for your payment", Order placed current, $34.30, the address and landmark, "Not paid yet. $34.30 with your card.".
  Pay now (two taps) moved it to "Paid. Waiting for the restaurant to accept it". A throwaway staff account accepted and marked it
  ready over the API, and the open page followed by itself: "Being prepared in the kitchen", then "Ready. A driver is picking it up"
  with the note on the kitchen step. A first tap on Cancel and refund showed "Confirm: cancel and refund" and "You were charged. The
  refund starts on its own.", and after about 5 s went back to "Cancel and refund" (the timeout working); two quick taps cancelled
  it: "Cancelled. Your refund is on its way", the notice, the timeline stopping at Cancelled, "Nothing for you to do". About 20 s
  later the refund job had run and the page said "Cancelled and refunded". Escape went back to the list (status line, items, $34.30,
  Refunded stamp). At 375 px the page had no sideways scroll and nothing wider than the screen. Stack taken down (`docker compose -p
  sdorders down`, volumes kept).
- **Found, not mine:** a compose project `sdtrack` (created 2026-11-14, API on port 8091) whose database container is stopped, so
  its API restarts in a loop every few seconds since Docker started. Left untouched.
- **Left for later:** the customer journey test and docs (next item).

### 2026-11-21, `agent/2026-11-21-customer-journey`: Customer journey test and docs

- **Built:** `web/ui/src/CustomerJourney.test.tsx`, one test that runs the real app from the sign-in screen against a small stateful
  fake of the API: sign up (the account lands on Food with Food, Orders and Account and nothing of the dashboard, and no `/alerts`
  or `/users` request), search "Indiranagar", pick the address, add a landmark and save it as Home (`POST /addresses`), see Corner
  Kitchen "m away" and Harbour Grill "too far", open Corner Kitchen, 2 burgers and 1 fries, View cart, Home already chosen, card,
  Place order (the body is exactly `{id, restaurantId, paymentType, deliveryAddressId, items: [{menuItemId, quantity}]}`), the
  tracker opens on the order with "Waiting for your payment", $23.00 and the landmark, the cart is empty, Pay now twice, "Paid.
  Waiting for the restaurant to accept it", the fake kitchen accepts it and the open page reads "Being prepared in the kitchen" on
  the next poll, and back on the list the row says so with its total (9288a19).
- **Retired the customer's old order sheet** (4c6f5d4): the Food home's "Start an order" button and the `NewOrderSheet` it opened
  were a second, older way to order with their own address picker and search; nothing else in the customer view needed them. The
  sheet is now staff's phone-order form only: the `user` and `placeForOthers` props are gone, so it always shows the order id, the
  customer picker and custom lines, and an id clash stays on the id field. Kept: `Shell`'s capability flags (the staff shell is
  still written against them) and `DeliverTo`'s `allowSaved` prop (the sheet passes `false`, the Food home and cart page `true`).
- **Design choices:** a component-level journey test rather than a browser in CI: the repo has no browser toolchain, adding
  Playwright to CI is a large new dependency for one test, and the Go e2e test already walks the same journey over HTTP against the
  real server and Postgres (and passed against this run's stack). The fake server is stateful where the journey depends on it (it
  keeps the saved address, prices from its menu, refuses an order without a saved address id, moves the order on), so the test
  proves the screens join up rather than that one screen draws. Rejected: extending `e2e/` (it already covers signup, addresses,
  menus, order, pay and the whole lifecycle over HTTP; what was missing was the screens together).
- **Tests moved, not lost:** `DeliverTo.test.tsx` reached the customer's `DeliverTo` through the order sheet; it now opens the cart
  page with a kept one-line cart (customer) or the New order sheet (staff, via an explicit `openSheet(user, 'staff')`), and its
  "food search near the address" case moved to the Food home. `NearbySearch.test.tsx` and `NewOrderSheet.test.tsx` run as staff
  (it is their sheet now); the customer's search is covered by `FoodHome.test.tsx`. The customer-only 409 test of the sheet and
  FoodHome's "Start an order" test were removed; `CustomerApp.test.tsx` now orders through the restaurant page and cart and checks
  the button is gone. The `placeOrder` helper matches "Place order · $total".
- **Checks:** `npx tsc --noEmit` clean; `npm test`: 107 node tests and 241 vitest tests passing (242 before, minus two removed, plus
  the journey). From PowerShell against `swiftdrop_test` (port 5434, `cmd/migrate`: up to date): `go vet ./...` clean, `go test
  -race -count=1 ./...` every package ok (no Go changed). Go e2e (`go test -count=1 -v ./e2e`) against this run's compose stack with
  a throwaway staff account: PASS.
- **Mutation check:** 8 mutants of the code the journey crosses, each applied in the working copy and restored with `git checkout
  --`, journey test run alone: the cart kept after placing, the placed order not opened, the cart page not given the Food home's
  address, polling every 60 s instead of 3, a customer given the staff shell, every card saying it delivers, a price sent with the
  order, a saved address sent as words instead of its id. 8 of 8 killed; tree clean after.
- **web/static:** rebuilt on Linux (`node:24-alpine`, CRs stripped, `npm ci && npm run build`) from the committed sources (0a40ad2:
  `index-Bo4obIvm.js` replaces `index-DeS7mOwP.js`; the stylesheet `index-ChnbBnRn.css` is unchanged); a second fresh Linux build of
  HEAD was `diff -r` identical to the committed `web/static`.
- **Live check** (throwaway compose project `sdjourney` on port 8094, demo seed, fake gateway, demo geocoder, built-in browser with
  the pane hidden, so read through page text and the accessibility tree, no screenshots): signed up a throwaway customer; the Food
  home had no "Start an order" button; searched "Indiranagar", picked 12 100 Feet Road, added "blue gate" and saved it as Home; the
  cards said Noodle Bar 50 m and Corner Kitchen 700 m deliver to Home, Harbour Grill 849 km is too far; Corner Kitchen's page, 2
  Smash burger and 1 Fries, "View cart · 3 items · $23.00"; the cart page already had Home chosen; Place order opened SD-X8KTD on
  its page ("Waiting for your payment", Order placed current, $23.00, Home with the landmark, "Not paid yet. $23.00 with your
  card."); Pay now twice: "Paid. Waiting for the restaurant to accept it"; a throwaway staff account accepted it over the API and
  the open page read "Being prepared in the kitchen" by itself; Escape showed the row "Order SD-X8KTD, Being prepared in the
  kitchen". At 375 px the Food home had no sideways scroll and nothing wider than the screen. Stack taken down (`docker compose -p
  sdjourney down`, volumes kept). Not looked at in the browser: staff's New order sheet after the prop removal (covered by the 16
  staff tests in `NewOrderSheet.test.tsx`).
- **Found on the way:** the KNOWN_ISSUES gap "the address chosen on the Food home is not chosen again on the cart page" is no
  longer true (the cart page shares the Food home's destination; the journey test and the live check both show it), so it is
  removed. "Choose where to deliver." staying on screen is still listed; not rechecked.
- **Left for later:** nothing on this item. The customer items are all ticked; next is "Real drivers and dispatch", slice 1.

### 2026-11-22, `feature/drivers-dispatch`: Real drivers and dispatch, slice 1 (drivers exist)

Built by Claude in the session with the user (not by the daily agent), all four slices on one branch; the item is ticked when
slice 4 is done.

- **Built:** `domain.RoleDriver`; the `users_role_check` constraint re-created with `driver` (the same drop-and-add as
  `payments_status_check`, so an older database upgrades in place; checked on a database that had the two-role check); a `drivers`
  table (user id, `online`, `online_since`, one `latitude`/`longitude` and `location_at`, both-or-neither constraint); `ports.DriverStore`
  with the Postgres adapter and an in-memory `drivertest.Board`; routes `GET /driver/me`, `POST /driver/online`, `/driver/offline`,
  `/driver/location` for drivers and `GET /drivers` for staff; staff make drivers under People (role choice, Drivers filter) and
  `cmd/user -role driver`.
- **Design choices:** (1) **busy is never stored.** A driver is busy while an order assigned to them is `preparing` or `picked_up`,
  and their idle time runs from going online or from their last finished or cancelled order; both are read from `orders`, so they
  cannot disagree with it (a stored flag would be one more thing to keep in step across a crash). (2) **A location is kept only while
  online and never as a trail:** one row, one point, replaced by the next, cleared when the driver goes offline; a location from an
  offline driver is a 409. (3) **`signedIn` now means customer or staff.** Before, any signed-in account passed it, so a driver would
  have reached `/orders`, `/restaurants` and `/addresses`; now a driver gets a 403 there, and `anyAccount` keeps sign out, who-am-I
  and change password for every role. (4) **The driver store is its own interface on `Server`** (nil until `main` sets it, then the
  routes answer 503), like `Accounts` and `Geocoder`, so no existing fake had to learn about drivers. (5) The location limit reuses
  the geocoder's per-person limiter (6 a minute; the app sends every 30 s). Rejected: a `status` column (`available`, `on_delivery`)
  as the old `domain.Driver` had, for the reason in (1); a location history table (nothing needs a trail, and it is the kind of data
  that is better not to have).
- **Tests:** against Postgres (`drivers_test.go`: only driver accounts are drivers, a new driver is offline, going online twice keeps
  the first time, a point is kept only while online and replaced not added, the database refuses a half point, a latitude of 91 and
  an unknown role, busy and idle time from the orders, the list online first and without disabled accounts, names) and through real
  sign-in (`driver_test.go`: every driver route refused to staff, customers and signed-out visitors; a driver refused on 15 customer
  and staff routes but able to see who they are and sign out; a driver made by staff must replace the temporary password first; the
  point must exist; the rate limit is per driver; 503 when not set up; 500 without leaking the cause). Web: badge, validation, the
  Driver choice and the Drivers filter. `go test -race -count=1 ./...` passes with `cmd/handlers` at 100.0%.
- **Not yet:** nothing offers a driver an order, and a driver who signs in sees the staff dashboard (its calls are refused); both
  are the next slices.
