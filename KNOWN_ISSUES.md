# Known issues and deferred work

Things we decided **not** to build yet, written down so they are not forgotten. The payment rows below are the ones that would hurt in production. Each has a "when" so we know what unlocks it.

## Payments: production issues we are accepting for now

| # | Issue | What happens | Why we accept it for now | Unlocked by |
|---|-------|--------------|--------------------------|-------------|
| 1 | **A payment stuck in `processing` has no way out** | The provider call dies (crash, partition, timeout) and nobody retries. The row stays `processing` and the order stays `payment_pending` forever. The restaurant never sees the order. | Retrying is the **user's** decision, and no code path may re-send a charge on its own (that could charge someone who walked away). | Provider lookup + a read-only reconciler, then a terminal `needs_review` |
| 2 | **Money may have moved with no record** | The provider charged, but the response was lost or `FinishPayment` failed. Our row says `processing`. From our side this looks identical to "never reached the provider". | We cannot tell the two apart without asking the provider. | Provider lookup (needs the ledger first) |
| 3 | **A retry more than ~24 hours later is a new charge** | Stripe prunes idempotency keys after at least 24 hours, so re-sending the same key is treated as a new request. | We decided: no expiry, no cutoff, no pruning for now. Revisit when optimising. | Takeover cutoff (about 20h, inside the takeover SQL) or a provider lookup |
| 4 | **A customer can pay twice through a new order** | The old order errors, the customer places a new one and pays again. If the first charge succeeded, they paid twice. | Needs the provider lookup to detect. | Lookup / reconciler / ledger |
| 5 | **Charged but cancelled** | `/cancel` is allowed while the order is `payment_pending`. If the payment then succeeds, `FinishPayment` finds the order moved on: 409, a `NEEDS REVIEW` log line, nothing recorded. Money moved, order is cancelled. | Refunds are a later feature. A test documents this behaviour. | Refunds, or refusing `/cancel` while a payment is in flight |
| 6 | **No alert on `NEEDS REVIEW`** | The only signal for #1, #2 and #5 is a log line. No metric, no query, no dashboard. | No ops tooling yet. | Metrics, or at least a query for old `processing` rows |

## Payments: design not built

- **No ledger / payment history.** `payments` is the idempotent claim store: one row per order holding the latest status. It is not an audit trail. **Do not delete payment rows** until the ledger and refunds exist: a refund needs `provider_payment_id`.
- **No provider lookup, metadata or create-the-PaymentIntent-first.** Ignored on purpose until the ledger exists. Today the gateway creates and confirms in one call and sends no metadata.
- **No webhooks, no auth-then-capture, no end-of-day reconciliation.** This is how real payment systems close the gap in #1 and #2. Not planned yet.
- **No refunds.**
- **Client `Idempotency-Key` header is ignored.** Our own payment uuid is the key.
- **A retry or takeover must send the same body.** Otherwise Stripe reports an idempotency conflict.

## Orders and API

- `SetFoodReady` is still check-then-write (every other state write is compare-and-set).
- `paymentMethodId` from the `/pay` body is not validated and a body decode error is ignored. A card order with no `paymentMethodId` under real Stripe is a `500`; it should be a `4xx`.
- `createdAt` / `updatedAt` on orders are never set.
- Nothing sweeps old `payment_pending` orders.

## Performance and structure

- No cache in front of the payment lookup (planned as a `CachedPaymentStore` decorator, to be benchmarked with and without).
- `PayOrder` is now about 250 lines of orchestration across helpers: candidate for a service layer (Facade).

## Stand-ins

- Wallet payments go to the same Stripe gateway as cards.
- `/accept` assigns a random UUID as the driver. No driver registry, no dispatch.
- No authentication.
- Without `STRIPE_KEY` the fake gateway is used. Use only Stripe **test-mode** keys while developing.
