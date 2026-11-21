// The customer tracker's words: the status line, the payment sentence and the timeline.
import test from 'node:test';
import assert from 'node:assert/strict';
import { customerStatus, customerTimeline, paymentSentence, timeline } from './logic.js';

const order = (over = {}) => ({
  id: 'SD-1', state: 'created', paymentType: 'card',
  items: [{ sku: 'B', name: 'Burger', quantity: 2, unitPriceCents: 950 }], ...over,
});
const paid = (over = {}) => ({ status: 'authorized', attempt: 1, maxAttempts: 3, amountCents: 1900, ...over });

test('customerStatus says where the order is in the customer\'s words', () => {
  assert.equal(customerStatus(order()), 'Waiting for your payment');
  assert.equal(customerStatus(order({ paymentType: 'cash' })), 'Waiting for you to confirm cash on delivery');
  assert.equal(customerStatus(order({ paymentIssue: 'declined' })), 'Payment declined. You can try again');
  assert.equal(customerStatus(order({ paymentIssue: 'abandoned' })), "Payment didn't go through. You can try again");
  assert.equal(customerStatus(order({ state: 'payment_pending' })), 'Confirming your payment');
  assert.equal(customerStatus(order({ state: 'paid' })), 'Paid. Waiting for the restaurant to accept it');
  assert.equal(customerStatus(order({ state: 'paid', paymentType: 'cash' })), 'Confirmed. Waiting for the restaurant to accept it');
  assert.equal(customerStatus(order({ state: 'preparing' })), 'Being prepared in the kitchen');
  assert.equal(customerStatus(order({ state: 'preparing', foodReady: '2026-11-21T08:00:00Z' })), 'Ready. Waiting for a driver');
  assert.equal(customerStatus(order({ state: 'preparing', foodReady: '2026-11-21T08:00:00Z', assignedDriverId: 'd1' })), 'Ready. A driver is picking it up');
  // a driver assigned before the food is ready does not make it ready
  assert.equal(customerStatus(order({ state: 'preparing', assignedDriverId: 'd1' })), 'Being prepared in the kitchen');
  assert.equal(customerStatus(order({ state: 'picked_up' })), 'On its way to you');
  assert.equal(customerStatus(order({ state: 'delivered' })), 'Delivered');
  assert.equal(customerStatus(order({ state: 'mystery' })), 'mystery');
  assert.equal(customerStatus(order({ state: undefined })), '');
});

test('customerStatus tells a cancelled order\'s money from the server\'s paymentIssue', () => {
  const cancelled = (paymentIssue) => customerStatus(order({ state: 'cancelled', paymentIssue }));
  assert.equal(cancelled(undefined), 'Cancelled');
  assert.equal(cancelled('refund_pending'), 'Cancelled. Your refund is on its way');
  assert.equal(cancelled('refunded'), 'Cancelled and refunded');
  assert.equal(cancelled('refund_failed'), 'Cancelled. Support will return your money');
  assert.equal(cancelled('confirming'), 'Cancelled. Confirming your payment');
  assert.equal(cancelled('declined'), 'Cancelled: the payment was declined');
  assert.equal(cancelled('abandoned'), "Cancelled: the payment didn't go through");
});

test('paymentSentence reads a card or wallet payment as one sentence', () => {
  assert.equal(paymentSentence(order()), 'Not paid yet. $19.00 with your card.');
  assert.equal(paymentSentence(order({ paymentType: 'wallet' })), 'Not paid yet. $19.00 with your wallet.');
  assert.equal(paymentSentence(order({ state: 'paid', payment: paid({ amountCents: 1850 }) })), 'Paid $18.50 with your card.');
  assert.equal(paymentSentence(order({ state: 'payment_pending', paymentIssue: 'confirming', payment: paid({ status: 'processing' }) })),
    "We're confirming your payment of $19.00.");
  assert.equal(paymentSentence(order({ state: 'payment_pending', paymentIssue: 'confirming', payment: paid({ status: 'needs_review' }) })),
    'A person is checking your payment of $19.00. You are never charged twice.');
  assert.equal(paymentSentence(order({ state: 'cancelled' })), 'Nothing was charged.');
});

test('paymentSentence says how many tries are left after a decline or an attempt with no answer', () => {
  const declined = order({ paymentIssue: 'declined', payment: paid({ status: 'declined', attempt: 1 }) });
  assert.equal(paymentSentence(declined), 'Your last payment was declined (attempt 1 of 3). You were not charged. You can try 2 more times.');
  const once = order({ paymentIssue: 'abandoned', payment: paid({ status: 'abandoned', attempt: 2 }) });
  assert.equal(paymentSentence(once), 'Your last payment got no answer (attempt 2 of 3). You were not charged. You can try 1 more time.');
  const over = order({ state: 'cancelled', paymentIssue: 'declined', payment: paid({ status: 'declined', attempt: 3 }) });
  assert.equal(paymentSentence(over), 'Your last payment was declined (attempt 3 of 3). You were not charged.');
  // never a negative count, whatever the server says
  const odd = order({ paymentIssue: 'declined', payment: paid({ status: 'declined', attempt: 4 }) });
  assert.match(paymentSentence(odd), /You can try 0 more times\.$/);
  // a decline issue without the payment falls back to the plain words
  assert.equal(paymentSentence(order({ paymentIssue: 'declined' })), 'Not paid yet. $19.00 with your card.');
});

test('paymentSentence reads refunds from the server, never from the order state alone', () => {
  const cancelled = (paymentIssue, refundStatus) => order({ state: 'cancelled', paymentIssue, payment: paid({ refundStatus }) });
  assert.equal(paymentSentence(cancelled('refund_pending', 'refunding')), 'You paid $19.00 with your card. It is on its way back to your card.');
  assert.equal(paymentSentence(cancelled('refunded', 'refunded')), 'You paid $19.00 with your card. It was refunded to your card.');
  assert.equal(paymentSentence(cancelled('refund_failed', 'failed')),
    'You paid $19.00 with your card. It could not be refunded automatically; support will return it.');
  // the amount is what was taken, not what the items add up to now
  assert.match(paymentSentence({ ...cancelled('refunded'), payment: paid({ amountCents: 2500 }) }), /^You paid \$25\.00/);
});

test('paymentSentence for cash: nothing is charged online', () => {
  const cash = (over) => order({ paymentType: 'cash', ...over });
  assert.equal(paymentSentence(cash()), 'Cash on delivery: $19.00. Press Pay now to confirm the order; nothing is charged now.');
  assert.equal(paymentSentence(cash({ state: 'preparing', payment: paid() })), 'Cash on delivery: you pay $19.00 when it arrives.');
  assert.equal(paymentSentence(cash({ state: 'delivered', payment: paid() })), 'Paid $19.00 in cash on delivery.');
  assert.equal(paymentSentence(cash({ state: 'cancelled', payment: paid() })), 'Cash on delivery. Nothing was charged.');
  // cash never has a refund to speak of, even if an issue were sent
  assert.equal(paymentSentence(cash({ state: 'cancelled', paymentIssue: 'refund_pending', payment: paid() })), 'Cash on delivery. Nothing was charged.');
});

test('customerTimeline keeps timeline\'s steps and statuses, in the customer\'s words', () => {
  for (const state of ['created', 'payment_pending', 'paid', 'preparing', 'picked_up', 'delivered', 'cancelled']) {
    const o = order({ state });
    assert.deepEqual(customerTimeline(o).map((s) => [s.key, s.status]), timeline(o).map((s) => [s.key, s.status]), state);
  }
  assert.deepEqual(customerTimeline(order({ state: 'picked_up' })).map((s) => s.label),
    ['Order placed', 'Paid', 'Being prepared', 'On the way', 'Delivered']);
  assert.deepEqual(customerTimeline(order({ state: 'cancelled' })).map((s) => s.label),
    ['Order placed', 'Paid', 'Being prepared', 'On the way', 'Cancelled']);
  assert.equal(customerTimeline(order({ state: 'paid', paymentType: 'cash' }))[1].label, 'Confirmed');
  assert.equal(customerTimeline(order({ state: 'payment_pending' }))[0].note, 'confirming payment');
});

test('customerTimeline notes ready food on the kitchen step only while it is current', () => {
  const ready = '2026-11-21T08:00:00Z';
  assert.equal(customerTimeline(order({ state: 'preparing' }))[2].note, undefined);
  assert.equal(customerTimeline(order({ state: 'preparing', foodReady: ready }))[2].note, 'ready, waiting for a driver');
  assert.equal(customerTimeline(order({ state: 'preparing', foodReady: ready, assignedDriverId: 'd1' }))[2].note, 'ready, a driver is on the way');
  assert.equal(customerTimeline(order({ state: 'picked_up', foodReady: ready }))[2].note, undefined);
});
