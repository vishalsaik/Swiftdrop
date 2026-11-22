import test from 'node:test';
import assert from 'node:assert/strict';
import {
  DRIVER_LOCATION_MS, capabilities, driverBadges, driverLocationProblem, driverStatusLine, jobStep, lastHeard, offerCountdown, pickupText,
} from './logic.js';

const driver = (over = {}) => ({ userId: 'u', name: 'Dev Driver', online: true, busy: false, updatedAt: '2026-11-22T08:00:00Z', ...over });
const job = (over = {}) => ({ orderId: 'o1', state: 'preparing', foodReady: false, itemCount: 3, restaurant: { name: 'Corner Kitchen' }, ...over });

test('a position is sent every half minute, well inside the three minutes after which dispatch stops trusting it', () => {
  assert.equal(DRIVER_LOCATION_MS, 30_000);
});

test('the status line says where the driver stands, a delivery first', () => {
  assert.equal(driverStatusLine(driver({ online: false }), null, null), 'Offline. Go online to be offered orders');
  assert.equal(driverStatusLine(null, null, null), 'Offline. Go online to be offered orders');
  assert.equal(driverStatusLine(driver(), null, null), 'Online. Waiting for an order near you');
  assert.equal(driverStatusLine(driver(), { id: 1 }, null), 'An order is waiting for your answer');
  assert.equal(driverStatusLine(driver(), null, job()), 'On a delivery: collect it from the restaurant');
  assert.equal(driverStatusLine(driver(), null, job({ state: 'picked_up' })), 'On a delivery: take it to the customer');
  // a delivery outranks an offer: a driver on one is not shown another
  assert.equal(driverStatusLine(driver(), { id: 1 }, job()), 'On a delivery: collect it from the restaurant');
});

test('the countdown counts whole seconds up, never below nothing, and says when time is up', () => {
  assert.equal(offerCountdown(42.2), '43 s to answer');
  assert.equal(offerCountdown(60), '60 s to answer');
  assert.equal(offerCountdown(0.4), '1 s to answer');
  assert.equal(offerCountdown(0), 'Time is up');
  assert.equal(offerCountdown(-5), 'Time is up');
  assert.equal(offerCountdown(undefined), 'Time is up');
  assert.equal(offerCountdown('abc'), 'Time is up');
});

test('how far the restaurant is is said only when the driver has said where they are', () => {
  assert.equal(pickupText(undefined), '');
  assert.equal(pickupText(null), '');
  assert.equal(pickupText(Number.NaN), '');
  assert.equal(pickupText(-1), '');
  assert.equal(pickupText('2'), '');
  assert.equal(pickupText(2), '2.0 km to the restaurant');
  assert.equal(pickupText(1.26), '1.3 km to the restaurant');
  assert.equal(pickupText(0), 'You are at the restaurant');
  assert.equal(pickupText(0.09), 'You are at the restaurant');
  assert.equal(pickupText(0.1), '0.1 km to the restaurant');
});

test('a delivery offers the one button that moves it on, and only when it can be pressed', () => {
  assert.equal(jobStep(null), null);
  assert.equal(jobStep(undefined), null);
  assert.deepEqual(jobStep(job()), { text: 'Go to the restaurant. The kitchen is still cooking.', action: null, label: '' });
  assert.deepEqual(jobStep(job({ foodReady: true })), { text: 'The food is ready. Collect it.', action: 'pickup', label: 'Picked up' });
  assert.deepEqual(jobStep(job({ state: 'picked_up', foodReady: true })), { text: 'Take it to the customer.', action: 'deliver', label: 'Delivered' });
  assert.deepEqual(jobStep(job({ state: 'picked_up' })), { text: 'Take it to the customer.', action: 'deliver', label: 'Delivered' });
  assert.deepEqual(jobStep(job({ state: 'delivered' })), { text: 'Delivered. Thank you.', action: null, label: '' });
  assert.deepEqual(jobStep(job({ state: 'cancelled' })), { text: 'This delivery has ended.', action: null, label: '' });
});

test('staff see whether a driver is taking deliveries, and whether they are free or busy', () => {
  assert.deepEqual(driverBadges(driver({ online: false })), [{ text: 'Offline', tone: 'neutral' }]);
  assert.deepEqual(driverBadges(driver()), [{ text: 'Online', tone: 'good' }, { text: 'Free', tone: 'good' }]);
  assert.deepEqual(driverBadges(driver({ busy: true })), [{ text: 'Online', tone: 'good' }, { text: 'On a delivery', tone: 'wait' }]);
  // busy and offline: they went offline on a delivery; the list says what matters first
  assert.deepEqual(driverBadges(driver({ online: false, busy: true })), [{ text: 'Offline', tone: 'neutral' }]);
});

test('staff are told when an online driver was last heard from', () => {
  const now = Date.parse('2026-11-22T08:10:00Z');
  assert.equal(lastHeard(driver({ online: false, locationAt: '2026-11-22T08:09:59Z' }), now), '');
  assert.equal(lastHeard(driver({ locationAt: '2026-11-22T08:09:58Z' }), now), 'Last seen just now');
  assert.equal(lastHeard(driver({ locationAt: '2026-11-22T08:08:00Z' }), now), 'Last seen 2m ago');
  assert.equal(lastHeard(driver({ locationAt: undefined }), now), 'Has not sent a location yet');
  assert.equal(lastHeard(driver({ locationAt: 'not a time' }), now), 'Has not sent a location yet');
});

test('a driver who cannot be located is told why and what it costs them', () => {
  assert.match(driverLocationProblem(1), /turned off for this page/);
  assert.match(driverLocationProblem(3), /took too long/);
  assert.match(driverLocationProblem(2), /could not be found/);
  assert.match(driverLocationProblem(99), /could not be found/);
  for (const code of [1, 2, 3]) assert.match(driverLocationProblem(code), /cannot go online without it/);
});

test('only staff see the drivers list', () => {
  assert.equal(capabilities('staff').drivers, true);
  assert.equal(capabilities('customer').drivers, false);
  assert.equal(capabilities('driver').drivers, false);
  assert.equal(capabilities(null).drivers, false);
});
