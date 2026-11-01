# Known issues and deferred work

Things we decided **not** to build yet, written down so they are not forgotten. The payment rows below are the ones that would hurt in production. Each has a "when" so we know what unlocks it.

## Payments: production issues we are accepting for now

| # | Issue | What happens | Why we accept it for now | Unlocked by |
|---|-------|--------------|--------------------------|-------------|
| 1 | **A payment stuck in `processing` can still have no way out** | No webhook event ever arrives and nobody retries: the request never reached Stripe (no money moved), or Stripe's event was lost (our endpoint was down, or had no secret, for longer than Stripe retries: about 3 days in live mode). The row stays `processing` and the order stays `payment_pending`. The restaurant never sees the order. | Retrying is the **user's** decision, and no code path may re-send a charge on its own (that could charge someone who walked away). | Provider lookup + a read-only reconciler, then a terminal `needs_review` |
| 2 | **Money may still have moved with no record** | The provider charged, but the response was lost or `FinishPayment` failed. The webhook records this when Stripe's event arrives (seconds later, retried for about 3 days). It stays unrecorded only if that event is lost or `STRIPE_WEBHOOK_SECRET` is not set, and then it looks identical to "never reached the provider". | We cannot tell the two apart without asking the provider. | Provider lookup |
| 3 | **A retry more than ~24 hours later is a new charge** | Stripe prunes idempotency keys after at least 24 hours, so re-sending the same key is treated as a new request. | We decided: no expiry, no cutoff, no pruning for now. Revisit when optimising. | Takeover cutoff (about 20h, inside the takeover SQL) or a provider lookup |
| 4 | **A customer can pay twice through a new order** | The old order errors, the customer places a new one and pays again. If the first charge succeeded, they paid twice. The webhook shrinks this to the time before the event arrives. | Needs the provider lookup to detect. | Lookup / reconciler / ledger |
| 5 | **Charged but cancelled** | Fixed. The provider's answer is kept even when it arrives after the cancel (an authorized payment on a cancelled order), and the refund job gives the money back within one interval (30 s by default). What is left: Stripe can accept a refund and later fail it, which nothing tracks yet; and a refund that fails 5 times is marked `failed` and raised as an alert for a person. | Refunds are automatic, but only as far as Stripe accepting them. | Stripe's refund events (not looked up yet) |
| 6 | **Alerts reach nobody who is not looking** | Problems only a person can fix (an unrecorded charge, a contradicting event, a refund given up on) are kept as alerts and shown on the dashboard, but nothing pushes them out: no email, chat message, pager or metric. Someone has to open the dashboard. | No ops tooling yet. | A notification channel and a metric on open alerts |

## Payments: design not built

- **The ledger is a history, not yet a double-entry book.** `payment_events` records what happened to each payment, written by the database itself and append-only. It does not hold amounts as debits and credits, so it cannot yet be reconciled against Stripe's settlement reports. **Do not delete payment rows**: a row holds the `provider_payment_id` a refund needs, and a payment with a history cannot be deleted anyway.
- **No provider lookup, metadata or create-the-PaymentIntent-first.** Ignored on purpose until the ledger exists. Today the gateway creates and confirms in one call and sends no metadata. The webhook matches events through `event.request.idempotency_key` instead, so an event that none of our requests caused (a dashboard refund, a dispute) carries no key and is ignored.
- **No auth-then-capture, no end-of-day reconciliation, no polling reconciler.** Real payment systems layer these under webhooks as backstops. Not planned yet.
- **Refunds are automatic but basic.** One full refund per payment, made by a job inside the API process (`REFUND_INTERVAL`). No partial refunds, no refund reasons, and nothing tracks a refund that Stripe accepts and later reverses. A separate `cmd/worker` process would be the next step, since jobs should not share a process with request handling.
- **Client `Idempotency-Key` header is ignored.** Our own payment uuid is the key.
- **A retry or takeover must send the same body.** Otherwise Stripe reports an idempotency conflict.

## Payments: the webhook

Built: `POST /webhooks/stripe` (see the README). What it leaves open:

- **Not verified against real Stripe yet.** That Stripe copies our `Idempotency-Key` header into `event.request.idempotency_key` comes from Stripe's documentation, and so does the assumption that `payment_failed` events carry it too. The tests use events we sign ourselves. `stripe trigger` fixtures make requests of their own, so their events carry no key of ours: check with real test-mode charges before relying on it.
- **Production setup.** A public HTTPS endpoint and a separate `STRIPE_WEBHOOK_SECRET` for test and live mode. Without the secret the route answers `404`, Stripe's deliveries fail, and after its retry window they stop. Secrets should be rolled periodically; the parser accepts several `v1` signatures while one rolls.
- **Only two event types are handled** (`payment_intent.succeeded`, `payment_intent.payment_failed`). `payment_failed` is treated as a definite decline, as in the synchronous path.
- **Events can arrive out of order.** A second event that contradicts a recorded outcome raises a `contradicting_event` alert and changes nothing.
- **No table of processed event ids.** Compare-and-set makes a duplicate harmless today. A table would be needed once a handler has side effects (an email, a refund).
- **The work happens inline.** Stripe wants a quick `2xx`. One transaction is fine; a queue is for when the handler does more.

## Orders and API

- Nothing sweeps old `payment_pending` orders.
- A `payment_pending` order whose payment never gets an answer (the request never reached Stripe, or its event was lost) stays pending until someone cancels it.
- There is no authentication. Anyone who can reach the server can pay, accept or cancel any order, and the dashboard has no login.

## The dashboard

- It polls every 3 seconds. Pushing changes to the screen (server-sent events) is not built.
- Only its rules (`logic.js`) have automated tests. The drawing code (`app.js`) was checked by hand in a browser, so a change to it needs a look in a browser.
- The customer messages are shown to the dispatcher, as a preview of what the customer would see. Nothing is sent to a customer: there is no customer app, email or push yet.
- The fonts come from Google Fonts. Offline the stylesheet falls back to the system fonts it names (not tried offline).
- Shutting down on Ctrl+C (the server stops, then the refund job finishes what it is doing) is in `main.go`, which has no automated test.

## Performance and structure

- No cache in front of the payment lookup (planned as a `CachedPaymentStore` decorator, to be benchmarked with and without).
- `PayOrder` is now about 250 lines of orchestration across helpers: candidate for a service layer (Facade).

## Stand-ins

- Wallet payments go to the same Stripe gateway as cards.
- `/accept` assigns a random UUID as the driver. No driver registry, no dispatch.
- No authentication.
- Without `STRIPE_KEY` the fake gateway is used. Use only Stripe **test-mode** keys while developing.
