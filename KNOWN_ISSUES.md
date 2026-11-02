# Known issues and deferred work

Things we decided **not** to build yet, written down so they are not forgotten. The payment rows below are the ones that would hurt in production. Each has a "when" so we know what unlocks it.

## Payments: production issues we are accepting for now

| # | Issue | What happens | Why we accept it for now | Unlocked by |
|---|-------|--------------|--------------------------|-------------|
| 1 | **A payment can still wait for a person** | No webhook event ever arrives and nobody retries: the request never reached Stripe (no money moved), or Stripe's event was lost (our endpoint was down, or had no secret, for longer than Stripe retries: about 3 days in live mode). Mostly fixed: the reconciler asks the provider what became of it (never charging again), records a success or a refusal, abandons an attempt the provider has no record of after 30 minutes, and after 20 hours of no answer (the provider cannot be asked, or never decides) hands it to a person: `needs_review`, an alert, no new attempt for the order. What is left is that person: nothing notifies them (see 6), and until they act the order stays `payment_pending`. | Retrying is the **user's** decision, and no code path may re-send a charge on its own (that could charge someone who walked away). | A notification channel, and a screen to settle a payment by hand |
| 2 | **Money may still have moved with no record** | The provider charged, but the response was lost or `FinishPayment` failed. The webhook records this when Stripe's event arrives (seconds later, retried for about 3 days). It stays unrecorded only if that event is lost or `STRIPE_WEBHOOK_SECRET` is not set, and then it looks identical to "never reached the provider". | We cannot tell the two apart without asking the provider. | Provider lookup |
| 3 | **A retry more than ~24 hours later is a new charge** | Fixed. Stripe prunes idempotency keys after at least 24 hours, so re-sending the same key would be a new charge. A retry of a payment older than 20 hours is no longer sent: it is marked `needs_review`, an alert is raised and the retry is answered `409`. The age is read from the application clock, which only matters to the minute, not to the second. | | |
| 4 | **A customer can pay twice through a new order** | The old order errors, the customer places a new one and pays again. If the first charge succeeded, they paid twice. The webhook and the reconciler shrink this to the time before one of them settles the old payment (about two minutes at best: the reconciler leaves a payment alone for 2 minutes), after which the old order is visibly paid. | Nothing links the two orders, and cancelling the duplicate is a person's call. | Matching orders by customer, then a refund tool |
| 5 | **Charged but cancelled** | Fixed. The provider's answer is kept even when it arrives after the cancel (an authorized payment on a cancelled order), and the refund job gives the money back within one interval (30 s by default). A refund that fails 5 times is marked `failed` and raised as an alert for a person, and so is one Stripe accepts and reverses later (`refund.failed`). What is left is the person (see 10). | | |
| 6 | **Alerts reach nobody who is not looking** | Problems only a person can fix (an unrecorded charge, a contradicting event, a refund given up on) are kept as alerts and shown on the dashboard, but nothing pushes them out: no email, chat message, pager or metric. Someone has to open the dashboard. | No ops tooling yet. | A notification channel and a metric on open alerts |
| 7 | **A charge found after we gave up is only an alert** | The reconciler abandons an attempt the provider has no record of after 30 minutes, then never looks at it again. If the provider reports a charge for it later (a very slow search index, an unusual outage), only the webhook can say so, as a `contradicting_event` alert. Without `STRIPE_WEBHOOK_SECRET` nobody notices. | The 30 minutes is long against Stripe's list API, which is current. | Re-checking abandoned payments for a day |
| 8 | **Payments claimed before the charge carried metadata cannot be found** | The lookup finds a payment by the `metadata[payment_id]` the charge sends. A payment claimed by an older build has none, so a lookup says "no record" and the attempt is abandoned after 30 minutes even if it was charged. | There is no real traffic yet. Before running this against a database with live `processing` payments, settle them by hand. | |
| 9 | **The Stripe lookup lists recent payment intents** | It reads every intent created since the claim (up to 2,000) and picks ours, because Stripe's search is eventually consistent and not offered to every account. Past 2,000 it gives up with an error, never "no record", and the payment goes to a person after 20 hours. | Fine at this volume. | Stripe search, or storing the intent id before confirming |
| 10 | **A reversed refund is only an alert** | When Stripe reverses a refund, the refund becomes `failed`, an alert is raised and the customer is told to contact support. Nothing refunds again on its own (a second refund could pay out twice), and there is no way to record that a person refunded by hand: resolving the alert only closes it. | No ops tooling yet. | A screen to mark a refund done by hand, and a notification channel |

## Payments: design not built

- **The ledger is a history, not yet a double-entry book.** `payment_events` records what happened to each payment, written by the database itself and append-only. It does not hold amounts as debits and credits, so it cannot yet be reconciled against Stripe's settlement reports. **Do not delete payment rows**: a row holds the `provider_payment_id` a refund needs, and a payment with a history cannot be deleted anyway.
- **No provider lookup, metadata or create-the-PaymentIntent-first.** Ignored on purpose until the ledger exists. Today the gateway creates and confirms in one call and sends no metadata. The webhook matches events through `event.request.idempotency_key` instead, so an event that none of our requests caused (a dashboard refund, a dispute) carries no key and is ignored.
- **No auth-then-capture, no end-of-day reconciliation, no polling reconciler.** Real payment systems layer these under webhooks as backstops. Not planned yet.
- **Refunds are automatic but basic.** One full refund per payment, made by a job inside the API process (`REFUND_INTERVAL`). No partial refunds and no refund reasons. A separate `cmd/worker` process would be the next step, since jobs should not share a process with request handling.
- **Client `Idempotency-Key` header is ignored.** Our own payment uuid is the key.
- **A retry or takeover must send the same body.** Otherwise Stripe reports an idempotency conflict.

## Payments: the webhook

Built: `POST /webhooks/stripe` (see the README). What it leaves open:

- **Checked against real Stripe test mode (2026-11-01)**, with real charges through our own server and `stripe listen`: a charge with our exact request is accepted and a card success and a decline are each read correctly; `payment_intent.succeeded` and `payment_intent.payment_failed` both arrive with `request.idempotency_key` equal to our payment id, are signature-verified and matched (so the lost-reply recovery rests on something real); `refund.updated` does fire for an ordinary refund (status `succeeded`) and is read and ignored as not a failure; the reconciler's lookup finds a charge by the `metadata[payment_id]` we send and settles the payment and its order (tried by putting a real charge back into the "reply lost" state); a cancelled paid order is refunded through Stripe by the refund job on the first attempt, with a real `re_` id.
- **Still not checked against real Stripe:** only the exact payload of a refund that fails (`refund.failed`, and whether `refund.updated` also fires with a failed status then), which the CLI cannot generate. We read both and match on the payment intent, which is on every refund event. `stripe trigger` fixtures make requests of their own, so their events carry no key of ours: only real charges through our server prove this. `scripts/stripe-dev.ps1` sets up a matching listener and server.
- **Production setup.** A public HTTPS endpoint and a separate `STRIPE_WEBHOOK_SECRET` for test and live mode. Without the secret the route answers `404`, Stripe's deliveries fail, and after its retry window they stop. Secrets should be rolled periodically; the parser accepts several `v1` signatures while one rolls.
- **Only a few event types are handled**: `payment_intent.succeeded`, `payment_intent.payment_failed` (a definite decline, as in the synchronous path), `refund.failed`, and `refund.updated` when the refund ended up failed or canceled. Stripe's guide does not say whether `refund.updated` also fires when a refund fails (it names `refund.failed` for that), so both are read. Not yet seen from real Stripe: the payload of a refund that actually fails.
- **Events can arrive out of order.** A second event that contradicts a recorded outcome raises a `contradicting_event` alert and changes nothing.
- **No table of processed event ids.** Compare-and-set makes a duplicate harmless today. A table would be needed once a handler has side effects (an email, a refund).
- **The work happens inline.** Stripe wants a quick `2xx`. One transaction is fine; a queue is for when the handler does more.

## Orders and API

- Nothing sweeps old `payment_pending` orders.
- A `payment_pending` order whose payment never gets an answer (the request never reached Stripe, or its event was lost) stays pending until someone cancels it.
- Orders made before accounts existed carry a free-text `customerId` (like `cust-1`) that belongs to no account, so only staff can see them. A customer's own orders are the ones placed after they signed up.
- **Accounts are basic.** No email address checking (an account can be made with any address), no password reset, no two-factor sign-in, no screen for staff to make, disable or delete accounts (a disabled account is a database change, and it ends its sessions at the next request). Staff are made with `go run ./cmd/user`.
- **The sign-in limits live in the server's memory.** Several servers each count for themselves, and a restart forgets the counts. They are per email (5 in 15 minutes), per address (30) and for sign-ups per address (10 an hour).
- **The address that is limited is the connection's, not a forwarding header.** Behind a proxy that is the proxy's address, so every person would share one limit until the server is told which proxy to trust.
- **Sessions use the application's clock.** Their lifetimes are days and hours, so a few seconds of drift between servers does not matter, unlike the payment leases, which use the database's.
- **One CSRF token per session, kept for the session's life.** The cookie is `SameSite=Strict` as well, which already stops other sites from sending it, so the token is the second layer.

## The dashboard

- It polls every 3 seconds. Pushing changes to the screen (server-sent events) is not built.
- Its rules (`logic.js`) and its screens (in a simulated browser, against a fake server) have automated tests. How it looks, the animation and the phone layout were checked by hand in a browser, so a change to those needs a look in a browser.
- The build output in `web/static` is committed, because the Go server embeds it and `go run` must work without Node. After a change in `web/ui`, run `npm run build` there and commit the result; a stale build is the one thing no test can tell, because both halves are valid on their own.
- The dashboard is React only through its own build. The effect libraries from libraries.dev (border beam, thinking orbs, bot avatars) are not installed yet; the waiting dots are plain CSS.
- The customer messages are shown to the dispatcher, as a preview of what the customer would see. Nothing is sent to a customer: there is no customer app, email or push yet.
- It uses the system font (SF on Apple devices, Segoe UI on Windows, Roboto elsewhere), so there is nothing to download and it looks different from device to device.
- Shutting down on Ctrl+C (the server stops, then the refund job finishes what it is doing) is in `main.go`, which has no automated test.

## Performance and structure

- The payment cache (`internal/paymentcache`) holds only authorized payments and only serves `/pay`. It is per process, which is fine because what it keeps never changes, and it has no expiry for the same reason (an entry leaves only when the cache is full). Deleting a payment row by hand would leave it in a running server's cache until a restart; nothing deletes payments.
- `/pay` lives in `internal/paying` (a Facade); the handler is thin. The webhook handler and the reconciler still record answers through `internal/settle`, which `/pay` does not use: `/pay` records through its own `finish` and `keep` because it has already claimed the payment itself. Folding the three into one is possible but would change what a failed write does in each.

## Stand-ins

- Wallet payments go to the same Stripe gateway as cards.
- `/accept` assigns a random UUID as the driver. No driver registry, no dispatch.
- Without `STRIPE_KEY` the fake gateway is used. Use only Stripe **test-mode** keys while developing.
