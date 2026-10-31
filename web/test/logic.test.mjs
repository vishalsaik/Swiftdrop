// Run with: node --test web/test
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  COLUMNS, columnOf, money, totalCents, itemsSummary, actionsFor, timeline, noticeFor,
  stats, newOrderId, validateOrderForm, priceToCents, errorMessage, timeAgo, describeEvent,
} from '../static/logic.js';

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
  const n = noticeFor(order({ state: 'cancelled', payment: { status: 'declined', reason: 'Your card was declined.' } }));
  assert.equal(n.issue, 'declined');
  assert.match(n.body, /Your card was declined\./);
  assert.match(n.body, /not charged/);
  const bare = noticeFor(order({ state: 'cancelled', payment: { status: 'declined', reason: '' } }));
  assert.match(bare.body, /declined/);
});

test('there is no notice when there is nothing to say', () => {
  assert.equal(noticeFor(order({ state: 'paid', payment: { status: 'authorized', amountCents: 100 } })), null);
  assert.equal(noticeFor(order()), null);
  assert.equal(noticeFor(order({ state: 'created', payment: { status: 'declined' } })), null);
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
  assert.deepEqual(d('taken_over'), { label: 'A retry took the payment over', note: '' });
  assert.deepEqual(d('refund_started', 'attempt 2'), { label: 'Refund started', note: 'attempt 2' });
  assert.deepEqual(d('refund_attempt_failed', 'stripe: HTTP 500'), { label: 'Refund attempt failed', note: 'stripe: HTTP 500' });
  assert.deepEqual(d('refunded', 'refund_id=re_9'), { label: 'Refund sent', note: 're_9' });
  assert.equal(d('refund_failed', 'no such charge').label, 'Refund given up, needs a person');
});

test('an unknown ledger kind or odd detail still reads sensibly', () => {
  assert.equal(describeEvent({ kind: 'needs_review' }).label, 'needs review');
  assert.equal(describeEvent({}).label, 'Something happened');
  // a short lower-case word before "=" is read as a key, so only its value is shown
  assert.equal(describeEvent({ kind: 'declined', detail: 'a=b is not a key' }).note, 'b is not a key');
  assert.equal(describeEvent({ kind: 'claimed', detail: 'amount_cents=abc' }).note, 'abc');
  assert.equal(describeEvent({ kind: 'declined', detail: 'Card = declined' }).note, 'Card = declined');
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
