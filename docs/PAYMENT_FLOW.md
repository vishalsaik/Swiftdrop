# Payment flow: high-level architecture

How an order gets paid, what happens when something breaks halfway, and how money
goes back. This describes the code on `main`. For what is deliberately not built
yet, see [KNOWN_ISSUES.md](../KNOWN_ISSUES.md).

The diagrams are Mermaid, which GitHub renders.

## 1. The idea in one paragraph

Paying is two things that must agree: **our record** (an order and a payment row in
Postgres) and **the provider's record** (a Stripe charge). A network can fail
between them at any moment, so the design never assumes a call's answer arrives.
Instead: write down the intent **before** calling the provider, give the call an
idempotency key so repeating it is safe, and have **two independent ways** to learn
the outcome (the reply, and a webhook the provider pushes later). Every write that
changes state is a compare-and-set, so whichever reporter arrives first wins and
the other changes nothing.

## 2. The pieces

```mermaid
flowchart LR
  subgraph clients["Clients"]
    D["Dashboard (browser)"]
    A["curl or an app"]
  end

  subgraph api["API process (cmd/api)"]
    H["Handlers<br/>PayOrder, StripeWebhook,<br/>GetOrderByID, ListOrders"]
    R["payment.Router<br/>(Factory)"]
    P["Providers<br/>card, wallet, cash<br/>(Strategy)"]
    G["Gateway<br/>StripeGateway or FakeGateway<br/>(Adapter)"]
    W["StripeWebhook<br/>(EventParser: verifies and translates)"]
    J["refund.Service<br/>background job"]
  end

  DB[("Postgres<br/>orders, payments")]
  S["Stripe"]

  D --> H
  A --> H
  H --> R --> P --> G --> S
  S -- "POST /webhooks/stripe" --> H
  H --> W
  H <--> DB
  J <--> DB
  J --> G
```

| Piece | Where | Job |
|---|---|---|
| Handlers | `cmd/handlers` | HTTP in, HTTP out. `PayOrder` reads the request, hands it to the pay service and writes the answer; `PaymentWebhook` records an outcome Stripe pushes; `GetOrderByID` and `ListOrders` add the payment summary and `paymentIssue`. |
| Pay service | `internal/paying` | **Facade.** `Pay` runs the flow below (the payment record, the provider, the state machine, alerts) and returns a typed `Outcome`; it knows nothing of HTTP. It reads the request body lazily, through a function the handler passes, so a replay never reads it. |
| Router | `internal/payment/router.go` | **Factory.** Payment type in (`card`, `wallet`, `cash`), provider out. The handler asks per request because the type is only known after loading the order. |
| Providers | `internal/payment/providers` | **Strategy.** One per payment type. Card and wallet delegate to a gateway; cash authorizes with no gateway. A provider may also refuse a request up front (`RequestChecker`). |
| Gateway | `internal/payment`, `.../gateways` | **Adapter** to the outside. `StripeGateway` speaks Stripe's HTTP API; `FakeGateway` runs everything locally with no network. Both also implement `Refunder`. |
| EventParser | `.../gateways/stripe_webhook.go` | Verifies a webhook's signature and turns Stripe's event into our `payment.Event`. The handler never sees Stripe's format. |
| Stores | `internal/databases/ports`, `.../adapters` | **Repository.** `Store` (orders and payments) and `RefundStore` are interfaces; the Postgres adapter implements them. Handlers never see SQL. |
| Refund job | `internal/refund` | Finds refunds that are due and makes them. |

Dependencies point inward: handlers depend on interfaces in `ports` and `payment`,
and only `cmd/api/main.go` knows which concrete adapters are wired in.

## 3. State

### The order

```mermaid
stateDiagram-v2
  [*] --> created
  created --> payment_pending: pay claimed
  created --> cancelled
  payment_pending --> paid: payment authorized
  payment_pending --> created: declined, attempts left
  payment_pending --> cancelled: cancelled, or the last attempt declined
  paid --> preparing: accept
  paid --> cancelled
  preparing --> picked_up: pickup
  preparing --> cancelled
  picked_up --> delivered
  delivered --> [*]
  cancelled --> [*]
```

The allowed moves are one table (`legalTransitions` in `internal/order`). Food
readiness and the assigned driver are separate facts, not states.

### The payment and its refund

A row in `payments` per attempt. An order can have several after declines or
abandoned attempts, but only one that is not declined or abandoned: a partial unique
index is what makes the claim atomic. A payment has two independent dimensions:

```mermaid
stateDiagram-v2
  direction LR
  state "payment status" as PS {
    [*] --> processing: claimed
    processing --> authorized: money taken
    processing --> declined: card refused
    processing --> abandoned: provider has no record, 30 min
    processing --> needs_review: no answer, 20 h
    needs_review --> authorized: late answer
    needs_review --> declined: late answer
  }
```

```mermaid
stateDiagram-v2
  direction LR
  state "refund status (only meaningful once authorized)" as RS {
    [*] --> none
    none --> refunding: claimed by the job
    refunding --> refunded: provider accepted
    refunding --> refunding: attempt failed, retried after the lease
    refunding --> failed: 5 attempts, a person must look
    refunded --> failed: the provider reverses it later
  }
```

A payment stays `authorized` forever (the money *was* taken); the refund status
says whether it has been given back.

`abandoned` and `needs_review` are what a payment becomes when it never got an
answer (section 8). `abandoned` is final, like `declined`: no money moved, and the
order is open again. `needs_review` is not final: a person has been asked, it still
counts as the order's live payment so no second attempt can start, and the
provider's answer, if it ever arrives, still settles it.

## 4. The happy path

```mermaid
sequenceDiagram
  autonumber
  participant C as Client
  participant H as PayOrder
  participant DB as Postgres
  participant P as Provider
  participant S as Stripe

  C->>H: POST /orders/:id/pay with paymentMethodId
  H->>DB: read the order and any payment
  H->>P: Check the request (422 if it could never succeed)
  H->>DB: StartPayment, one transaction:<br/>insert payment (processing, lease 60s)<br/>and move the order created to payment_pending
  Note over H,DB: of any number of simultaneous requests exactly one claims
  H->>P: Authorize with the payment id as the idempotency key
  P->>S: create and confirm a PaymentIntent
  S-->>P: succeeded
  H->>DB: FinishPayment, one transaction:<br/>payment authorized and order payment_pending to paid
  H-->>C: 200 with the order
```

Three rules that make this safe:

1. **Claim before charge.** If we charged first, every simultaneous request would
   charge, and a crash after the charge would leave no record of it.
2. **The payment's own uuid is the idempotency key.** Asking Stripe again with the
   same key returns the first answer instead of charging twice (for about 24
   hours; see [KNOWN_ISSUES.md](../KNOWN_ISSUES.md)).
3. **The payment row and the order move together** in one transaction, so there is
   never a payment for an order that did not move, or the reverse.

## 5. A retry, a crash, and a takeover

The payment row tells a request what it is. The same endpoint handles both a new
payment and a retry.

| What the request finds | What it does |
|---|---|
| no payment | a new payment: claim, charge, record |
| `authorized` | **replay**: `200` with the current order, no charge |
| `declined`, and the order is open again (`created`) | a **new attempt**: a new payment with its own key |
| `declined`, and the order is closed (`cancelled`) | **replay**: `402` with the same reason, no charge |
| `processing`, lease still live | someone is working on it: `409`, try again shortly |
| `processing`, lease expired | its owner probably died: **take it over** (compare-and-set on the expired lease), then ask Stripe again with the **same** payment id |
| `processing`, claimed more than 20 hours ago | **not sent again**: the provider may have forgotten the key and a send could charge twice. Handed to a person (`needs_review`, an alert), `409` |
| `needs_review` | `409`: a person is checking it, no new attempt until it is settled |
| `abandoned` | a **new attempt**, like after a decline: the provider never saw the old one |

A **lease** is a claim with a deadline held in the database row, timed by the
database's clock (so servers with drifting clocks agree). It is not a cache: if it
vanished, two requests could work on one payment, which is exactly what it
prevents.

A retry of a payment that is already `authorized` is the common case, and for it the
first read (which payment does this order have?) is served from a small in-memory
cache (`internal/paymentcache`, section 11). Only an authorized payment is kept: it is
final, and while it exists the order can have no other live payment, so the entry can
never be wrong. A payment that is processing, declined, abandoned or under review is
always read from the database, as is "no payment yet" (the first `/pay` must see
that), and the order view never uses the cache because a refund made later changes
what it shows.

A retry is always the **customer's** decision. No code re-sends a charge on its
own, because that could charge someone who walked away.

**A decline can be retried with another method.** A decline is a definite no (no
money moved), so it does not close the order: the order returns to `created`, the
payment stays `declined` as a record, and paying again starts a **new attempt**: a
new payment row (attempt 2, 3) with its own idempotency key, so the provider cannot
replay the earlier decline. The answer says `canRetry` and `attemptsLeft`. After
three attempts (`domain.MaxPaymentAttempts`) the last decline cancels the order
instead, and from then on a decline is replayed. Each attempt keeps its own history
in the ledger.

## 6. When the reply is lost: the webhook

If Stripe charged the card but the reply never reached us, the payment would stay
`processing`. Stripe also pushes the outcome to us on its own.

```mermaid
sequenceDiagram
  autonumber
  participant H as PayOrder
  participant S as Stripe
  participant W as Webhook handler
  participant DB as Postgres

  H->>S: charge (idempotency key = payment id)
  S-->>H: reply lost
  Note over H,DB: payment stays processing, order stays payment_pending
  S->>W: POST /webhooks/stripe payment_intent.succeeded<br/>request.idempotency_key = payment id
  W->>W: verify Stripe-Signature on the raw body
  W->>DB: find the payment by that id
  W->>DB: FinishPayment (the same code as the happy path)
  W-->>S: 2xx so Stripe stops retrying
```

* The event is matched to our payment through `event.request.idempotency_key`,
  which Stripe copies from the charge's `Idempotency-Key` header. That header is
  our payment id, so no metadata is sent.
* The reply and the webhook can arrive in either order, or both. Whichever lands
  first records the outcome; the other finds the payment finished and changes
  nothing. A duplicate delivery is harmless for the same reason.
* Status codes speak to Stripe's retry logic: `2xx` for anything a retry cannot
  improve (an event we do not act on, an unknown payment, already recorded), `400`
  for a request that fails verification, `5xx` only when our storage fails, so
  Stripe delivers it again (it retries for up to about three days in live mode).
* If the request never reached Stripe, no event ever comes (nothing happened).
  No money moved, so the payment stays `processing` and retrying is the
  customer's choice.

## 7. Cancelled while paying, and refunds

An order can be cancelled while a payment is in flight. The provider's answer is
a fact about money and the order is a fact about the kitchen, so they no longer
have to change together: the order stays cancelled and **the answer is kept** (an
authorized payment on a cancelled order). The same state arises when a paid order
is cancelled later.

A refund is **due** when all of these hold, which is derived from the data, so
cancelling needs no extra step:

* the payment is `authorized`,
* its order is `cancelled`,
* it was charged online (cash moves no money through us),
* its refund status is `none`, or `refunding` with an expired lease.

```mermaid
sequenceDiagram
  autonumber
  participant J as Refund job
  participant DB as Postgres
  participant S as Stripe

  loop every REFUND_INTERVAL (30s)
    J->>DB: list due refunds, oldest first
    J->>DB: ClaimRefund (lease 2 min, attempt counted)
    J->>S: refund with key refund-payment-id
    alt accepted (succeeded or pending)
      J->>DB: CompleteRefund (refunded)
    else failed
      J->>DB: FailRefund, retry after the lease<br/>(after 5 attempts: failed and NEEDS REVIEW)
    end
  end
```

* The claim is a compare-and-set with a lease, the same pattern as claiming a
  charge, so any number of servers can run the job and a payment is refunded once.
* A charge that Stripe says was already refunded counts as success: asking twice
  is never a problem.
* A refund that went through is recorded even while the server is shutting down,
  and an attempt cut short by shutdown is not counted against the payment.
* The customer sees the stage through `paymentIssue` (section 9).

**A refund the provider accepted can still fail.** "Accepted" is not "paid out": a
refund Stripe called `pending` can fail later (an expired or closed card, for one),
and then we would say the customer was refunded when they were not. Stripe reports it
with `refund.failed`, and with `refund.updated` when the refund ends up `failed` or
`canceled`; the data is the Refund object, with the payment intent it refunds. The
webhook matches it on that payment intent (not on the request's idempotency key: Stripe
changed the refund on its own, so no request of ours caused the event). Only the
refund we recorded as done, with that very refund id, moves to `failed`, with the
provider's reason; a refund someone made by hand in the Stripe dashboard failing says
nothing about ours. An alert (`refund_reversed`) asks a person to refund by hand, and
the customer sees the same message as for a refund we gave up on. Nothing asks Stripe
for a second refund: it said why the first failed, and a second could pay out twice.
If the report arrives while our own job is still making the refund and has not
written it down, the webhook answers `503` so Stripe delivers it again.

## 8. What can break, and what recovers it

| Where it breaks | Money moved? | What we have | What recovers it |
|---|---|---|---|
| Before the claim commits | no | nothing written | the customer retries |
| Claimed, the request never reached Stripe | no | `processing` | the customer's retry takes over after the lease; otherwise it stays pending |
| Stripe charged, the reply was lost | **yes** | `processing` | the **webhook** (or the retry, within about 24 h) |
| Stripe answered, our write failed | **yes** | `processing` | the webhook, or the retry |
| Order cancelled while the charge was in flight | **yes** | answer kept as `authorized` on a cancelled order | the **refund job** |
| Order cancelled after paying | **yes** | `authorized` on a cancelled order | the **refund job** |
| Two requests pay one order at once | one charge | one row | the claim: the loser replays or gets `409` |
| Webhook delivered twice, or out of order | n/a | already recorded | compare-and-set makes it a no-op |
| Refund call fails | yes, still | `refunding` with an error | retried after the lease; a person is asked after 5 attempts |
| The provider accepted a refund and reverses it later | **yes**, still owed | `refunded`, then `failed` | the **webhook** (`refund.failed`): an alert for a person, who refunds by hand |
| No answer ever arrives (a crash, a lost reply, no webhook) | unknown | `processing` | the **reconciler** asks the provider: a success or a refusal is recorded, no record after 30 min is `abandoned`, nothing decided after 20 h is `needs_review` |

### A payment that never got an answer

The claim comes before the provider call (that is what stops two requests both
charging), so a crash, a timeout or a lost reply leaves a payment `processing` with
nobody working on it. The webhook recovers the cases where the provider sends an
event. For the rest, the **reconciler** (`internal/reconcile`, every
`RECONCILE_INTERVAL`) asks the provider what became of the payment. It only asks:

| The provider says | The reconciler |
|---|---|
| it succeeded, or the card was refused | records it exactly as a webhook would (`internal/settle`): the order is paid, or reopened for another attempt (cancelled after the last) |
| it has the payment and has not decided | waits; after 20 hours hands it to a person |
| it has no record, and the attempt is under 30 minutes old | waits: the lookup may simply be early |
| it has no record, and the attempt is 30 minutes old or more | **abandons** it: no money moved, the order is open again |
| it cannot be asked (an outage, a wrong key, a search it could not finish) | tries again next run; after 20 hours hands it to a person |

The reconciler never re-sends the charge. The customer may have given up and walked
away, and once Stripe forgets the idempotency key (at least 24 hours) a second send is
a new charge. Re-sending is the customer's decision, made by retrying themselves. For
the same reason a customer's retry of a payment older than 20 hours is not sent
either: it is handed to a person.

To hold a payment while it asks, the job takes it over like a retry does (a lease, on
the database clock), so a customer's retry and another server never work on it at
the same time. A payment under review is only read, never held.

How Stripe is asked: the charge carries `metadata[payment_id]` and
`metadata[order_id]`, and the lookup lists the payment intents created since the
claim and picks the one with our id. A list is current, where Stripe's search is
eventually consistent and not offered to every account. If it cannot read everything
since the claim it returns an error, never "no record": that would abandon a payment
that may have been charged.

Whenever the table says a person is asked, the system raises an **alert**: a row
in `alerts`, one open per kind and subject, with a count of how many times the
same problem was seen. The kinds are `unrecorded_charge` (money taken that could
not be written down), `contradicting_event` (the provider disagrees with our
record), `refund_given_up` and `refund_unrecorded`. The row is written on a
context that outlives the request, so a client that hangs up cannot lose it, and
failing to write it is only logged: it never changes what the customer or
the provider is told.

The cases not fully covered (a person who is not told, a charge found after we
gave up on the attempt, a customer paying twice through a new order) are written
down in [KNOWN_ISSUES.md](../KNOWN_ISSUES.md) with what would unlock each.

## 9. What a screen is told

`GET /orders/:id` and `GET /orders` return the order, a `payment` summary (no
provider ids or error text) and a `paymentIssue` derived on every read from the
order and its payment, so it cannot go out of date:

| `paymentIssue` | When | Customer message |
|---|---|---|
| `confirming` | payment `processing` (or `needs_review`) on a pending or cancelled order | "We're confirming your payment. If you were charged and the order doesn't go ahead, it is refunded." |
| `refund_pending` | charged, order cancelled, refund not done | "Your order was cancelled. Your money is on its way back and will be credited soon." |
| `refunded` | the provider accepted the refund | "Your refund has been sent." |
| `refund_failed` | the job gave up, or the provider reversed an accepted refund | "Please contact support and quote the order id." |
| `abandoned` | an attempt got no answer and the provider has no record of it (order open again, or cancelled after the last attempt) | "We never received an answer for that attempt and you were not charged. You can try again (2 attempts left)." |
| `declined` | the latest payment was refused (order open again, or cancelled after the last attempt) | "Your payment was declined. You were not charged. You can try another card (2 attempts left)." |
| *(absent)* | nothing to say, and always for cash | |

The dashboard shows these as a message in the order drawer and a pop-up when an
order's issue changes while someone is watching. Nothing is sent to a customer yet.

The people running the service see the alerts of section 8 under **Alerts** in the
dashboard header (a count badge, then a list with what to do and a Resolve button
that takes a note), or through `GET /alerts` and `POST /alerts/{id}/resolve`.

## 10. Guarantees

1. A payment is **claimed before** the provider is called.
2. An order is **charged at most once**: one live (not declined) payment per order,
   the payment id is the idempotency key, and a retry of a payment reuses it. A new
   attempt after a decline is a different payment with its own key.
3. A payment and its order **change in one transaction**, or neither does.
4. Every state write is a **compare-and-set** on the state the caller read, so a
   lost race answers `409` and changes nothing.
5. A provider's answer is **never dropped**: it is recorded even when the order has
   moved on.
6. Nothing re-sends a charge on its own; a retry is the customer's decision.
7. A refund is made **once per payment**, however many workers run the job.
8. Errors never leak: the cause goes to the log, the client gets a generic message.

## 11. Patterns, where they are

| Pattern | Here |
|---|---|
| Strategy | `PaymentProvider` per payment type |
| Factory | `payment.Router.For` |
| Adapter | `StripeGateway`, `StripeWebhook`, `FakeGateway` behind `Gateway`, `Refunder`, `EventParser` |
| Repository | `ports.Store`, `ports.RefundStore` and the Postgres adapter |
| Dependency injection | the handler receives the router; `main` wires the gateways |
| Chain of Responsibility | the middleware chain and the order validators |
| State as data | `legalTransitions` |
| Observer (pub/sub, across a network) | Stripe's webhook into our handler |
| Facade | `paying.Service`: one `Pay` over the payment record, the provider, the state machine and alerts |

Also used: Decorator (`paymentcache.Cache` wraps the payment lookup of `/pay`).

Not used yet but planned: Decorator again (retries and a circuit breaker around the
gateway), Command, Builder.

## 12. Configuration

| Variable | Effect | Default |
|---|---|---|
| `STRIPE_KEY` | use Stripe (a **test-mode** key while developing); unset uses the fake gateway | unset |
| `STRIPE_WEBHOOK_SECRET` | switch on `POST /webhooks/stripe` (the `whsec_` value); unset answers `404` | unset |
| `REFUND_INTERVAL` | how often the refund job looks for due refunds | `30s` |
| `RECONCILE_INTERVAL` | how often the reconciler looks for payments without an answer | `1m` |
| `PAYMENT_CACHE_SIZE` | how many paid orders' payments `/pay` keeps in memory (`0` = off) | `10000` |
| `DATABASE_URL` | another database than the local development one | local Postgres |

With no `STRIPE_KEY` the fake gateway declines a card order over 50,000 cents and a
wallet order over 10,000 cents, and refunds everything, so every path above can be
exercised with no network.

## 13. Where to look

| For | File |
|---|---|
| The whole `/pay` flow, and an answer for an order that moved on | `internal/paying/service.go` |
| `/pay` as HTTP: the body, the status codes | `cmd/handlers/handlers.go` (`PayOrder`, `payMethod`, `writePayOutcome`) |
| The webhook handler and its signature check | `cmd/handlers/webhook.go`, `internal/payment/gateways/stripe_webhook.go` |
| The claim, lease, takeover and the two transactions | `internal/databases/adapters/payments.go` |
| Refunds: storage, provider call, job | `adapters/refunds.go`, `gateways/stripe_refund.go`, `internal/refund/service.go` |
| The payment cache and its benchmark | `internal/paymentcache/cache.go`, `bench_test.go` |
| Payments without an answer: lookup, job, recording | `gateways/stripe_lookup.go`, `internal/reconcile/service.go`, `internal/settle/settle.go` |
| What a screen is told | `internal/domain/issue.go`, `cmd/handlers/view.go` |
| Alerts: raising, listing, resolving | `cmd/handlers/alerts.go`, `adapters/alerts.go`, `internal/domain/alert.go` |
| The schema | `schema.sql` |
| Try it by hand | `curls.txt` |
