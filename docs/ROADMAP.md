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
6. **Leave things as you found them.** At the end: be back on `main` with a clean tree (your work is
   on your branch), stop the `postgres` container and any `swiftdrop` compose stack you started
   (`docker compose down`, keep volumes), and do not touch the `kind` clusters, which belong to other
   projects.
7. **Be honest in the report.** Say what you built, what passed (with the commands), what you did not
   or could not check, and anything you are unsure about. Never describe red tests as green. If you
   could not get something working in reasonable effort, stop, commit nothing broken, and say so.
8. **Do not edit this Rules section.** You may tick items and append to the Log.

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

- [ ] **Customer shell.** In `web/ui/src/App.tsx`/`Shell.tsx`, render a new `CustomerApp` for
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

## Health check (when there is nothing to build)

Read-only: confirm `main` equals `origin/main` (`git fetch` is fine; do not pull or push), run the Go
race tests and `npm test` in `web/ui`, read the latest GitHub Actions run for `main` from the public API
(`https://api.github.com/repos/vishalsaik/Swiftdrop/actions/runs?per_page=1`), list untracked files and
stale branches, and report what needs a person's attention.

## Log

(The agent appends one entry per run, newest last: date, branch, what it did, the commands it ran and
their results, what is left.)
