// Run with: node --test web/test
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  COLUMNS, columnOf, money, totalCents, itemsSummary, actionsFor, timeline, noticeFor,
  stats, newOrderId, validateOrderForm, priceToCents, errorMessage, timeAgo, describeEvent, describeAlert, openAlerts,
  capabilities, authErrorMessage, lockoutMessage, fieldErrors, validateSignup, greeting, initials,
} from './logic.js';

const order = (over = {}) => ({
  id: 'SD-1', state: 'created', paymentType: 'card',
  items: [{ sku: 'B', name: 'Burger', quantity: 2, unitPriceCents: 599 }],
  assignedDriverId: '', ...over,
});

test('money formats cents and keeps them integers', () => {
  assert.equal(money(0), '$0.00');
  assert.equal(money(5), '$0.05');
  assert.equal(money(1198), '$11.98');
  assert.equal(money(123456789), '$1,234,567.89');
  assert.equal(money(-250), '-$2.50');
  assert.equal(money(undefined), '$0.00');
  assert.equal(money(NaN), '$0.00');
  assert.equal(money(10.6), '$0.11');
});

test('totalCents sums quantity times price', () => {
  assert.equal(totalCents(order()), 1198);
  assert.equal(totalCents(order({ items: [] })), 0);
  assert.equal(totalCents({}), 0);
  assert.equal(totalCents(order({ items: [{ quantity: 3, unitPriceCents: 100 }, { quantity: 1, unitPriceCents: 25 }] })), 325);
});

test('itemsSummary shortens long carts', () => {
  const items = [1, 2, 3, 4].map((n) => ({ name: `Item${n}`, quantity: n, unitPriceCents: 1 }));
  assert.equal(itemsSummary(order({ items })), '1 x Item1, 2 x Item2 + 2 more');
  assert.equal(itemsSummary(order({ items: items.slice(0, 2) })), '1 x Item1, 2 x Item2');
  assert.equal(itemsSummary(order({ items: [] })), '');
});

test('every state lands in a column, and cancelled sits with delivered', () => {
  const cases = {
    created: 'awaiting', payment_pending: 'awaiting', paid: 'paid', preparing: 'kitchen',
    picked_up: 'road', delivered: 'closed', cancelled: 'closed',
  };
  for (const [state, col] of Object.entries(cases)) assert.equal(columnOf({ state }), col, state);
  assert.equal(columnOf({ state: 'something_new' }), 'closed');
  const covered = COLUMNS.flatMap((c) => c.states).sort();
  assert.deepEqual(covered, Object.keys(cases).sort());
});

const keys = (o) => actionsFor(o).map((a) => a.key);

test('actions follow the order state', () => {
  assert.deepEqual(keys(order({ state: 'created' })), ['pay', 'cancel']);
  assert.deepEqual(keys(order({ state: 'payment_pending' })), ['pay', 'cancel']);
  assert.deepEqual(keys(order({ state: 'paid' })), ['accept', 'cancel']);
  assert.deepEqual(keys(order({ state: 'preparing' })), ['ready', 'pickup', 'cancel']);
  assert.deepEqual(keys(order({ state: 'picked_up' })), ['deliver']);
  assert.deepEqual(keys(order({ state: 'delivered' })), []);
  assert.deepEqual(keys(order({ state: 'cancelled' })), []);
});

test('payment pending offers a retry, never a fresh payment', () => {
  const [pay] = actionsFor(order({ state: 'payment_pending' }));
  assert.equal(pay.label, 'Retry payment');
  assert.match(pay.note, /never charged twice/);
  assert.equal(actionsFor(order({ state: 'created' }))[0].label, 'Take payment');
});

test('pickup says why it is unavailable', () => {
  const get = (o) => actionsFor(o).find((a) => a.key === 'pickup');
  const notReady = get(order({ state: 'preparing', assignedDriverId: 'd1' }));
  assert.equal(notReady.enabled, false);
  assert.match(notReady.why, /not ready/);
  const noDriver = get(order({ state: 'preparing', foodReady: '2026-10-31T10:00:00Z' }));
  assert.equal(noDriver.enabled, false);
  assert.match(noDriver.why, /driver/);
  const ok = get(order({ state: 'preparing', foodReady: '2026-10-31T10:00:00Z', assignedDriverId: 'd1' }));
  assert.equal(ok.enabled, true);
  assert.equal(ok.why, '');
});

test('ready is a one-time action', () => {
  const ready = (o) => actionsFor(o).find((a) => a.key === 'ready');
  assert.equal(ready(order({ state: 'preparing' })).enabled, true);
  const done = ready(order({ state: 'preparing', foodReady: '2026-10-31T10:00:00Z' }));
  assert.equal(done.enabled, false);
  assert.match(done.why, /already/);
});

test('cancelling a charged order says a refund follows', () => {
  const cancel = (o) => actionsFor(o).find((a) => a.key === 'cancel');
  const charged = cancel(order({ state: 'paid', payment: { status: 'authorized' } }));
  assert.equal(charged.label, 'Cancel and refund');
  assert.match(charged.note, /refund/);
  assert.equal(cancel(order({ state: 'created' })).label, 'Cancel order');
  assert.equal(cancel(order({ state: 'paid', paymentType: 'cash', payment: { status: 'authorized' } })).label, 'Cancel order');
});

test('exactly one action is primary at a time', () => {
  for (const state of ['created', 'paid', 'preparing', 'picked_up']) {
    const o = order({ state, foodReady: state === 'preparing' ? '2026-10-31T10:00:00Z' : undefined, assignedDriverId: 'd1' });
    assert.equal(actionsFor(o).filter((a) => a.primary).length, 1, state);
  }
});

const statuses = (o) => timeline(o).map((s) => `${s.key}:${s.status}`).join(' ');

test('the timeline marks progress', () => {
  assert.equal(statuses(order({ state: 'created' })), 'created:current paid:todo preparing:todo picked_up:todo delivered:todo');
  assert.equal(statuses(order({ state: 'paid' })), 'created:done paid:current preparing:todo picked_up:todo delivered:todo');
  assert.equal(statuses(order({ state: 'picked_up' })), 'created:done paid:done preparing:done picked_up:current delivered:todo');
  assert.equal(statuses(order({ state: 'delivered' })), 'created:done paid:done preparing:done picked_up:done delivered:done');
});

test('payment pending annotates the first step', () => {
  const [first] = timeline(order({ state: 'payment_pending' }));
  assert.equal(first.status, 'current');
  assert.equal(first.note, 'confirming payment');
});

test('a cancelled order shows how far it got', () => {
  assert.equal(statuses(order({ state: 'cancelled', payment: { status: 'authorized' } })), 'created:done paid:done preparing:skipped picked_up:skipped cancelled:stopped');
  assert.equal(statuses(order({ state: 'cancelled', payment: { status: 'declined' } })), 'created:done paid:skipped preparing:skipped picked_up:skipped cancelled:stopped');
  assert.equal(statuses(order({ state: 'cancelled' })), 'created:done paid:skipped preparing:skipped picked_up:skipped cancelled:stopped');
});

test('the customer is told where their money is', () => {
  const pay = { status: 'authorized', amountCents: 2500 };
  const refundPending = noticeFor(order({ state: 'cancelled', paymentIssue: 'refund_pending', payment: pay }));
  assert.equal(refundPending.issue, 'refund_pending');
  assert.equal(refundPending.tone, 'money');
  assert.match(refundPending.body, /\$25\.00/);
  assert.match(refundPending.body, /credited soon/);
  assert.match(refundPending.body, /SD-1/);

  const refunded = noticeFor(order({ state: 'cancelled', paymentIssue: 'refunded', payment: pay, paymentType: 'wallet' }));
  assert.match(refunded.title, /\$25\.00/);
  assert.match(refunded.body, /wallet/);

  const failed = noticeFor(order({ state: 'cancelled', paymentIssue: 'refund_failed', payment: pay }));
  assert.equal(failed.tone, 'alert');
  assert.match(failed.body, /support/);
  assert.match(failed.body, /SD-1/);

  const confirming = noticeFor(order({ state: 'payment_pending', paymentIssue: 'confirming' }));
  assert.equal(confirming.tone, 'wait');
  assert.match(confirming.body, /automatically/);
});

test('a declined payment is explained and says nothing was charged', () => {
  const pay = (over = {}) => ({ status: 'declined', reason: 'Your card was declined.', attempt: 3, maxAttempts: 3, ...over });
  const n = noticeFor(order({ state: 'cancelled', paymentIssue: 'declined', payment: pay() }));
  assert.equal(n.issue, 'declined');
  assert.match(n.body, /Your card was declined\./);
  assert.match(n.body, /not charged/);
  assert.match(n.body, /order was cancelled/);
  const bare = noticeFor(order({ state: 'cancelled', paymentIssue: 'declined', payment: pay({ reason: '' }) }));
  assert.match(bare.body, /declined/);
});

test('a decline that leaves the order open says how many attempts remain', () => {
  const pay = (attempt) => ({ status: 'declined', reason: 'Insufficient funds.', attempt, maxAttempts: 3 });
  const one = noticeFor(order({ state: 'created', paymentIssue: 'declined', payment: pay(1) }));
  assert.match(one.body, /try another card/);
  assert.match(one.body, /2 attempts left/);
  assert.doesNotMatch(one.body, /cancelled/);
  assert.match(noticeFor(order({ state: 'created', paymentIssue: 'declined', payment: pay(2) })).body, /1 attempt left/);
});

test('a declined payment shown without the server saying so gets no notice', () => {
  // the screen never works out issues for itself: it only repeats the server's
  assert.equal(noticeFor(order({ state: 'cancelled', payment: { status: 'declined', reason: 'x' } })), null);
});

test('after a decline the payment action offers another attempt', () => {
  const declined = order({ state: 'created', payment: { status: 'declined', attempt: 1, maxAttempts: 3 } });
  const [pay] = actionsFor(declined);
  assert.equal(pay.label, 'Try payment again');
  assert.match(pay.note, /Attempt 2 of 3/);
  assert.match(pay.note, /last decline cancels/);
  assert.equal(pay.enabled, true);
  assert.equal(actionsFor(order({ state: 'created' }))[0].label, 'Take payment');
  assert.equal(actionsFor(order({ state: 'created', payment: { status: 'processing' } }))[0].label, 'Take payment');
});

test('an abandoned attempt offers another one and says nothing was charged', () => {
  const abandoned = order({ state: 'created', payment: { status: 'abandoned', attempt: 1, maxAttempts: 3 } });
  const [pay] = actionsFor(abandoned);
  assert.equal(pay.label, 'Try payment again');
  assert.match(pay.note, /Attempt 2 of 3/);
  assert.match(pay.note, /last failed attempt cancels/);

  const notice = (state, attempt) => noticeFor(order({ state, paymentIssue: 'abandoned', payment: { status: 'abandoned', attempt, maxAttempts: 3 } }));
  assert.match(notice('created', 1).body, /not charged/);
  assert.match(notice('created', 1).body, /2 attempts left/);
  assert.match(notice('created', 2).body, /1 attempt left/);
  assert.doesNotMatch(notice('created', 1).body, /cancelled/);
  assert.match(notice('cancelled', 3).body, /not charged and the order was cancelled/);
  assert.equal(notice('created', 1).issue, 'abandoned');
});

test('a payment waiting for review cannot be retried and the customer is told it is being checked', () => {
  const review = order({ state: 'payment_pending', paymentIssue: 'confirming', payment: { status: 'needs_review', attempt: 1, maxAttempts: 3 } });
  const [pay] = actionsFor(review);
  assert.equal(pay.enabled, false);
  assert.match(pay.why, /person is checking/);
  assert.match(noticeFor(review).title, /still confirming/);
  // an ordinary pending payment can still be retried, and says the usual thing
  const pending = order({ state: 'payment_pending', paymentIssue: 'confirming', payment: { status: 'processing' } });
  assert.equal(actionsFor(pending)[0].enabled, true);
  assert.equal(noticeFor(pending).title, "We're confirming your payment");
});

test('the new ledger kinds and the new alert are described', () => {
  assert.equal(describeEvent({ kind: 'abandoned', detail: 'no record at the provider after 30m' }).label, 'Attempt abandoned, nothing was charged');
  assert.equal(describeEvent({ kind: 'needs_review', detail: 'x' }).label, 'No answer for too long, handed to a person');
  const a = describeAlert({ kind: 'payment_needs_review', message: 'm', occurrences: 1 });
  assert.match(a.title, /no answer for too long/);
  assert.match(a.advice, /not sent again/);
});

test('a reversed refund is described and says the customer is still owed', () => {
  const a = describeAlert({ kind: 'refund_reversed', message: 'm', occurrences: 1 });
  assert.match(a.title, /accepted, then failed/);
  assert.match(a.advice, /not got their money back/);
  assert.match(a.advice, /provider dashboard/);
});

test('there is no notice when there is nothing to say', () => {
  assert.equal(noticeFor(order({ state: 'paid', payment: { status: 'authorized', amountCents: 100 } })), null);
  assert.equal(noticeFor(order()), null);
});

test('the notice amount falls back to the order total without a payment', () => {
  const n = noticeFor(order({ state: 'cancelled', paymentIssue: 'refund_pending' }));
  assert.match(n.body, /\$11\.98/);
});

test('ledger lines are described in words', () => {
  const d = (kind, detail = '') => describeEvent({ kind, detail });
  assert.deepEqual(d('claimed', 'amount_cents=1447'), { label: 'Payment started', note: '$14.47' });
  assert.deepEqual(d('authorized', 'provider_payment_id=pi_123'), { label: 'Money taken', note: 'pi_123' });
  assert.deepEqual(d('declined', 'Your card was declined.'), { label: 'Payment declined', note: 'Your card was declined.' });
  assert.deepEqual(d('taken_over'), { label: 'Taken over by a retry or the payment checker', note: '' });
  assert.deepEqual(d('refund_started', 'attempt 2'), { label: 'Refund started', note: 'attempt 2' });
  assert.deepEqual(d('refund_attempt_failed', 'stripe: HTTP 500'), { label: 'Refund attempt failed', note: 'stripe: HTTP 500' });
  assert.deepEqual(d('refunded', 'refund_id=re_9'), { label: 'Refund sent', note: 're_9' });
  assert.equal(d('refund_failed', 'no such charge').label, 'Refund given up, needs a person');
});

test('an unknown ledger kind or odd detail still reads sensibly', () => {
  assert.equal(describeEvent({ kind: 'something_new' }).label, 'something new');
  assert.equal(describeEvent({}).label, 'Something happened');
  // a short lower-case word before "=" is read as a key, so only its value is shown
  assert.equal(describeEvent({ kind: 'declined', detail: 'a=b is not a key' }).note, 'b is not a key');
  assert.equal(describeEvent({ kind: 'claimed', detail: 'amount_cents=abc' }).note, 'abc');
  assert.equal(describeEvent({ kind: 'declined', detail: 'Card = declined' }).note, 'Card = declined');
});

test('an alert is described in words a person can act on', () => {
  for (const kind of ['unrecorded_charge', 'contradicting_event', 'refund_given_up', 'refund_unrecorded']) {
    const d = describeAlert({ kind, message: 'm', occurrences: 1 });
    assert.ok(d.title.length > 5 && d.advice.length > 20, kind);
    assert.equal(d.detail, 'm');
    assert.equal(d.seen, '');
  }
  // each kind says something different, so they are not mixed up
  const titles = new Set(['unrecorded_charge', 'contradicting_event', 'refund_given_up', 'refund_unrecorded'].map((kind) => describeAlert({ kind }).title));
  assert.equal(titles.size, 4);
  // refunding twice is the costly mistake, so that alert says not to
  assert.match(describeAlert({ kind: 'refund_unrecorded' }).advice, /not refund again/i);
  assert.equal(describeAlert({ kind: 'refund_given_up', occurrences: 3 }).seen, 'seen 3 times');
  assert.equal(describeAlert({ kind: 'refund_given_up', occurrences: 'x' }).seen, '');
});

test('an alert of a kind the page does not know is still shown', () => {
  const d = describeAlert({ kind: 'needs_review', message: 'hmm' });
  assert.equal(d.title, 'needs review');
  assert.equal(d.detail, 'hmm');
  assert.equal(describeAlert({}).title, 'Needs a look');
});

test('only open alerts are listed, the most recently seen first', () => {
  const list = openAlerts([
    { id: 1, lastSeenAt: '2026-01-01T10:00:00Z' },
    { id: 2, lastSeenAt: '2026-01-01T12:00:00Z' },
    { id: 3, lastSeenAt: '2026-01-01T13:00:00Z', resolvedAt: '2026-01-01T13:05:00Z' },
  ]);
  assert.deepEqual(list.map((a) => a.id), [2, 1]);
  assert.deepEqual(openAlerts(null), []);
});

test('stats count what matters', () => {
  const charged = (state, extra = {}) => order({ id: state, state, payment: { status: 'authorized', amountCents: 1000 }, ...extra });
  const s = stats([
    charged('paid'), charged('picked_up'), charged('delivered'),
    charged('cancelled', { paymentIssue: 'refund_pending' }),
    order({ state: 'created' }),
    order({ state: 'payment_pending', paymentIssue: 'confirming' }),
    charged('cancelled', { paymentIssue: 'refund_failed' }),
    order({ state: 'paid', paymentType: 'cash', payment: { status: 'authorized', amountCents: 5000 } }),
  ]);
  assert.deepEqual(s, { active: 5, onTheRoad: 1, takenCents: 3000, refundsPending: 1, attention: 2 });
  assert.deepEqual(stats([]), { active: 0, onTheRoad: 0, takenCents: 0, refundsPending: 0, attention: 0 });
});

test('order ids are short, readable and deterministic with a fixed source', () => {
  assert.equal(newOrderId(() => 0), 'SD-AAAAA');
  assert.match(newOrderId(), /^SD-[A-Z2-9]{5}$/);
  assert.equal(newOrderId(() => 0.999999), 'SD-99999');
  assert.doesNotMatch(newOrderId(), /[01OI]/);
});

const goodForm = () => ({
  id: 'SD-1', customerId: 'cust-1', restaurantId: 'rest-1', paymentType: 'card',
  items: [{ name: 'Burger', quantity: '2', price: '5.99' }, { name: '', quantity: '1', price: '' }],
});

test('a good form becomes a request in cents and ignores blank rows', () => {
  const r = validateOrderForm(goodForm());
  assert.equal(r.ok, true);
  assert.deepEqual(r.payload, {
    id: 'SD-1', customerId: 'cust-1', restaurantId: 'rest-1', paymentType: 'card',
    items: [{ sku: 'BURGER', name: 'Burger', quantity: 2, unitPriceCents: 599 }],
  });
});

test('prices round to the nearest cent without float drift', () => {
  const f = goodForm();
  f.items = [{ name: 'A', quantity: 1, price: '19.99' }, { name: 'B', quantity: 1, price: '0.1' }, { name: 'C', quantity: 1, price: '1.005' }];
  const cents = validateOrderForm(f).payload.items.map((i) => i.unitPriceCents);
  assert.deepEqual(cents, [1999, 10, 101]);
});

test('priceToCents reads plain decimals only', () => {
  const cases = {
    '5.99': 599, '5': 500, '0': 0, '0.00': 0, '.5': 50, '5.': 500, '12.3': 1230, '1.005': 101,
    '1.004': 100, '0.999': 100, ' 7.25 ': 725, '1000000': 100000000,
  };
  for (const [text, cents] of Object.entries(cases)) assert.equal(priceToCents(text), cents, text);
  for (const bad of ['', '.', '-1', '1e3', '1,5', 'abc', '1.2.3', '$5', '99999999999999999999']) {
    assert.equal(priceToCents(bad), null, JSON.stringify(bad));
  }
});

test('free items are allowed, like the server', () => {
  const f = goodForm();
  f.items = [{ name: 'Water', quantity: 1, price: '0' }];
  assert.equal(validateOrderForm(f).ok, true);
});

test('the form names every problem next to its field', () => {
  const r = validateOrderForm({ id: ' ', customerId: '', restaurantId: '', paymentType: 'crypto', items: [] });
  assert.equal(r.ok, false);
  assert.equal(r.payload, null);
  assert.deepEqual(Object.keys(r.errors).sort(), ['customerId', 'id', 'items', 'paymentType', 'restaurantId']);
  assert.equal(r.errors.id, 'Order id is required');
});

test('item rows are checked one by one', () => {
  const f = goodForm();
  f.items = [
    { name: 'Burger', quantity: '0', price: '5' },
    { name: '', quantity: '1', price: '3' },
    { name: 'Fries', quantity: '1', price: '-1' },
    { name: 'Soda', quantity: '1.5', price: 'abc' },
    { name: 'Ok', quantity: '1', price: '1' },
  ];
  const r = validateOrderForm(f);
  assert.equal(r.ok, false);
  assert.deepEqual(r.errors['items.0'], { quantity: 'Quantity must be 1 or more' });
  assert.deepEqual(r.errors['items.1'], { name: 'Name the item' });
  assert.deepEqual(r.errors['items.2'], { price: 'Enter a price of 0 or more' });
  assert.deepEqual(Object.keys(r.errors['items.3']).sort(), ['price', 'quantity']);
  assert.equal(r.errors['items.4'], undefined);
  assert.equal(r.errors.items, undefined);
});

test('a name with no symbols still gets a sku', () => {
  const f = goodForm();
  f.items = [{ name: '!!!', quantity: 1, price: '1' }];
  assert.equal(validateOrderForm(f).payload.items[0].sku, 'ITEM');
});

test('errors speak plainly and prefer the server', () => {
  assert.equal(errorMessage(409, { error: 'order x cannot be paid' }), 'order x cannot be paid');
  assert.match(errorMessage(0, null), /Can't reach the server/);
  assert.match(errorMessage(404, null), /no longer exists/);
  assert.match(errorMessage(503, {}), /server had a problem/);
  assert.equal(errorMessage(418, { error: '' }), 'The request failed (HTTP 418).');
});

test('timeAgo is short and ignores the unset timestamp', () => {
  const now = Date.parse('2026-10-31T12:00:00Z');
  const at = (s) => new Date(now - s * 1000).toISOString();
  assert.equal(timeAgo(at(3), now), 'just now');
  assert.equal(timeAgo(at(45), now), '45s ago');
  assert.equal(timeAgo(at(300), now), '5m ago');
  assert.equal(timeAgo(at(7200), now), '2h ago');
  assert.equal(timeAgo(at(3 * 86400), now), '3d ago');
  assert.equal(timeAgo('0001-01-01T00:00:00Z', now), '');
  assert.equal(timeAgo('', now), '');
  assert.equal(timeAgo('garbage', now), '');
  assert.equal(timeAgo(new Date(now + 5000).toISOString(), now), 'just now');
});

// ---------------------------------------------------------------------------
// accounts: what each role is offered, and what the sign-in form says
// ---------------------------------------------------------------------------

test('a customer is offered paying and cancelling their own order and nothing of the kitchen', () => {
  const keys = (o, role) => actionsFor(o, role).map((a) => a.key);
  assert.deepEqual(keys(order({ state: 'created' }), 'customer'), ['pay', 'cancel']);
  assert.deepEqual(keys(order({ state: 'paid' }), 'customer'), ['cancel']);
  assert.deepEqual(keys(order({ state: 'preparing', foodReady: '2026-01-01T00:00:00Z', assignedDriverId: 'd' }), 'customer'), ['cancel']);
  assert.deepEqual(keys(order({ state: 'picked_up' }), 'customer'), []);
  assert.deepEqual(keys(order({ state: 'delivered' }), 'customer'), []);
  // staff still get the whole kitchen, and that is the default
  assert.deepEqual(keys(order({ state: 'paid' }), 'staff'), ['accept', 'cancel']);
  assert.deepEqual(keys(order({ state: 'paid' })), ['accept', 'cancel']);
});

test('a customer is spoken to as the customer: "Pay now", and their own refund', () => {
  const [pay] = actionsFor(order({ state: 'created' }), 'customer');
  assert.equal(pay.label, 'Pay now');
  assert.equal(actionsFor(order({ state: 'created' }), 'staff')[0].label, 'Take payment');

  const retry = actionsFor(order({ state: 'payment_pending', payment: { status: 'processing' } }), 'customer')[0];
  assert.match(retry.note, /never charged twice/);
  const again = actionsFor(order({ state: 'created', payment: { status: 'declined', attempt: 1, maxAttempts: 3 } }), 'customer')[0];
  assert.equal(again.label, 'Try payment again');

  const charged = order({ state: 'paid', paymentType: 'card', payment: { status: 'authorized', amountCents: 1000 } });
  const cancel = actionsFor(charged, 'customer').find((a) => a.key === 'cancel');
  assert.match(cancel.note, /You were charged/);
  assert.equal(cancel.label, 'Cancel and refund');
  assert.equal(actionsFor(order({ state: 'created' }), 'customer').find((a) => a.key === 'cancel').note, '');
});

test('only staff are shown the alerts, the history and everyone\'s orders', () => {
  assert.deepEqual(capabilities('staff'), { alerts: true, history: true, placeForOthers: true, everyonesOrders: true, kitchen: true });
  for (const role of ['customer', 'admin', '', undefined]) {
    assert.ok(Object.values(capabilities(role)).every((v) => v === false), String(role));
  }
});

test('the sign-in form speaks plainly and never says which half was wrong', () => {
  assert.match(authErrorMessage(0), /reach the server/);
  assert.equal(authErrorMessage(401, { error: 'email or password is incorrect' }), 'That email and password do not match.');
  assert.match(authErrorMessage(409), /already has an account/);
  assert.match(authErrorMessage(500), /server had a problem/);
  assert.equal(authErrorMessage(400, { error: 'invalid request body' }), 'invalid request body');
  assert.match(authErrorMessage(418, null), /HTTP 418/);
  assert.match(authErrorMessage(429, null, 600), /10 minutes/);
});

test('a lockout says how long, rounded up and never too short', () => {
  assert.equal(lockoutMessage(30), 'Too many attempts. Try again in 30 seconds.');
  assert.equal(lockoutMessage(29.2), 'Too many attempts. Try again in 30 seconds.');
  assert.equal(lockoutMessage(89), 'Too many attempts. Try again in 89 seconds.');
  assert.equal(lockoutMessage(90), 'Too many attempts. Try again in 2 minutes.');
  assert.equal(lockoutMessage(899), 'Too many attempts. Try again in 15 minutes.');
  assert.equal(lockoutMessage(901), 'Too many attempts. Try again in 16 minutes.');
  for (const bad of [undefined, null, 'soon', 0, -5, NaN]) {
    assert.equal(lockoutMessage(bad), 'Too many attempts. Try again in a little while.', String(bad));
  }
});

test('the server\'s field problems become sentences next to the field', () => {
  assert.deepEqual(fieldErrors({ error: 'x', fields: { email: 'must be a valid email address', password: 'must be at least 10 characters', newPassword: 'must be different from the current one', other: 'is odd' } }), {
    email: 'Email must be a valid email address',
    password: 'Password must be at least 10 characters',
    newPassword: 'New password must be different from the current one',
    other: 'other is odd',
  });
  for (const none of [null, undefined, {}, { fields: null }, { fields: 'x' }]) assert.deepEqual(fieldErrors(none), {});
});

test('the sign-up form catches the easy mistakes before anything is sent', () => {
  const ok = validateSignup({ email: '  ada@example.com ', name: ' Ada Lovelace ', password: 'a long enough phrase' });
  assert.equal(ok.ok, true);
  assert.deepEqual(ok.payload, { email: 'ada@example.com', name: 'Ada Lovelace', password: 'a long enough phrase' });
  const bad = validateSignup({ email: 'nope', name: '  ', password: 'short' });
  assert.equal(bad.ok, false);
  assert.deepEqual(Object.keys(bad.errors).sort(), ['email', 'name', 'password']);
  for (const email of ['a@b', 'a b@c.co', '@c.co', 'a@.co']) assert.ok(validateSignup({ email, name: 'A', password: 'x'.repeat(10) }).errors.email, email);
  // letters are counted, not bytes: nine accented letters are nine characters
  assert.equal(validateSignup({ email: 'a@b.co', name: 'A', password: 'ééééééééé' }).errors.password, 'Use at least 10 characters');
  assert.equal(validateSignup({ email: 'a@b.co', name: 'A', password: 'éééééééééé' }).ok, true);
  assert.equal(validateSignup({}).ok, false);
});

test('the board greets by the hour and the avatar shows initials', () => {
  const at = (h) => new Date(2026, 10, 2, h, 0, 0);
  assert.equal(greeting(at(0)), 'Good night');
  assert.equal(greeting(at(4)), 'Good night');
  assert.equal(greeting(at(5)), 'Good morning');
  assert.equal(greeting(at(11)), 'Good morning');
  assert.equal(greeting(at(12)), 'Good afternoon');
  assert.equal(greeting(at(17)), 'Good afternoon');
  assert.equal(greeting(at(18)), 'Good evening');
  assert.equal(greeting(at(23)), 'Good evening');
  assert.match(greeting(), /^Good (night|morning|afternoon|evening)$/);

  assert.equal(initials('Ada Lovelace'), 'AL');
  assert.equal(initials('  ada  '), 'A');
  assert.equal(initials('Jean Luc Picard'), 'JP');
  assert.equal(initials('élan vital'), 'ÉV');
  for (const none of ['', '   ', null, undefined]) assert.equal(initials(none), '?');
});

test('a signed-out answer anywhere says the session ended, whatever else it says', () => {
  assert.equal(errorMessage(401, { error: 'sign in required' }), 'Your session ended. Sign in again.');
  assert.equal(errorMessage(403, { error: 'this needs a staff account' }), 'this needs a staff account');
});
