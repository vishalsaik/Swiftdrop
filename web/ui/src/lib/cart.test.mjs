// Run with: node --test "src/lib/*.test.mjs"
import test from 'node:test';
import assert from 'node:assert/strict';
import { cartKey, checkoutBody, checkoutProblem, readCart } from './logic.js';

const line = (over = {}) => ({ menuItemId: 'burger', name: 'Smash burger', unitPriceCents: 950, quantity: 2, ...over });
const kept = (over = {}) => JSON.stringify({ restaurantId: 'r1', restaurantName: 'Corner Kitchen', lines: [line()], ...over });
const empty = { restaurantId: null, restaurantName: '', lines: [] };

test('a cart is kept under one key per account', () => {
  assert.equal(cartKey('u-1'), 'swiftdrop.cart.u-1');
  assert.notEqual(cartKey('u-1'), cartKey('u-2'));
});

test('readCart reads back a kept cart', () => {
  assert.deepEqual(readCart(kept()), { restaurantId: 'r1', restaurantName: 'Corner Kitchen', lines: [line()] });
  // a free item is still an item
  assert.equal(readCart(kept({ lines: [line({ unitPriceCents: 0 })] })).lines.length, 1);
  // no name kept is an empty name, not a lost cart
  assert.equal(readCart(kept({ restaurantName: 42 })).restaurantName, '');
});

test('readCart gives an empty cart for anything it cannot read', () => {
  for (const text of [null, undefined, '', 'not json', '{"restaurantId":', '[]', 'null', '7', '"text"',
    JSON.stringify({ lines: [line()] }), // no restaurant
    kept({ restaurantId: '' }), kept({ restaurantId: 5 }), kept({ lines: 'many' }), kept({ lines: [] })]) {
    assert.deepEqual(readCart(text), empty, String(text));
  }
});

test('readCart drops every line it cannot trust and keeps the rest', () => {
  const lines = [
    null, 'burger', 7,
    line({ menuItemId: '' }), line({ menuItemId: 3 }), line({ name: undefined }),
    line({ unitPriceCents: -1 }), line({ unitPriceCents: 9.5 }), line({ unitPriceCents: '950' }), line({ unitPriceCents: 2 ** 60 }),
    line({ quantity: 0 }), line({ quantity: -2 }), line({ quantity: 1.5 }), line({ quantity: '2' }),
    line({ menuItemId: 'fries', name: 'Fries', unitPriceCents: 400, quantity: 1 }),
  ];
  assert.deepEqual(readCart(kept({ lines })).lines, [{ menuItemId: 'fries', name: 'Fries', unitPriceCents: 400, quantity: 1 }]);
  // only the fields a line has are kept
  assert.deepEqual(Object.keys(readCart(kept({ lines: [{ ...line(), priceCents: 1, extra: true }] })).lines[0]),
    ['menuItemId', 'name', 'unitPriceCents', 'quantity']);
});

test('readCart keeps the server limits: 50 of an item, one line per item, 30 lines', () => {
  assert.equal(readCart(kept({ lines: [line({ quantity: 51 })] })).lines[0].quantity, 50);
  assert.equal(readCart(kept({ lines: [line({ quantity: 50 })] })).lines[0].quantity, 50);
  const twice = readCart(kept({ lines: [line({ quantity: 3 }), line({ quantity: 9 })] })).lines;
  assert.deepEqual(twice.map((l) => l.quantity), [3]);
  const many = Array.from({ length: 35 }, (_, i) => line({ menuItemId: `m${i}` }));
  const read = readCart(kept({ lines: many })).lines;
  assert.equal(read.length, 30);
  assert.equal(read[29].menuItemId, 'm29');
});

const cart = { restaurantId: 'r1', lines: [line(), line({ menuItemId: 'fries', name: 'Fries', unitPriceCents: 400, quantity: 1 })] };

test('checkoutBody sends the restaurant, how to pay, where to and the menu ids with counts, nothing else', () => {
  const { body, problem } = checkoutBody({ id: 'SD-ABCDE', cart, paymentType: 'wallet', destination: { saved: { id: 'a-home', line1: 'x' } } });
  assert.equal(problem, '');
  assert.deepEqual(body, {
    id: 'SD-ABCDE', restaurantId: 'r1', paymentType: 'wallet', deliveryAddressId: 'a-home',
    items: [{ menuItemId: 'burger', quantity: 2 }, { menuItemId: 'fries', quantity: 1 }],
  });
  assert.doesNotMatch(JSON.stringify(body), /price|name|customer/i);
});

test('checkoutBody sends an address that is not saved with only the fields the server reads', () => {
  const address = { label: '', line1: '1 Test Street', landmark: 'blue gate', city: 'Bengaluru', postalCode: '', latitude: 12.97, longitude: 77.64, id: 'x', userId: 'u' };
  const { body } = checkoutBody({ id: 'SD-1', cart, paymentType: 'cash', destination: { address } });
  assert.deepEqual(body.deliveryAddress, { line1: '1 Test Street', latitude: 12.97, longitude: 77.64, landmark: 'blue gate', city: 'Bengaluru' });
  assert.equal('deliveryAddressId' in body, false);
  const full = { label: 'Work', line1: 'A', landmark: 'B', city: 'C', postalCode: '560038', latitude: 0, longitude: 0 };
  assert.deepEqual(checkoutBody({ id: 'SD-1', cart, paymentType: 'card', destination: { address: full } }).body.deliveryAddress, full);
});

test('checkoutBody says what is missing, in order', () => {
  const to = { saved: { id: 'a' } };
  assert.equal(checkoutBody({ id: 'x', cart: null, paymentType: 'card', destination: to }).problem, 'Your cart is empty.');
  assert.equal(checkoutBody({ id: 'x', cart: { restaurantId: null, lines: [] }, paymentType: 'card', destination: to }).problem, 'Your cart is empty.');
  assert.equal(checkoutBody({ id: 'x', cart: { restaurantId: 'r1', lines: [] }, paymentType: 'card', destination: to }).problem, 'Your cart is empty.');
  assert.equal(checkoutBody({ id: 'x', cart: { restaurantId: '', lines: cart.lines }, paymentType: 'card', destination: to }).problem, 'Your cart is empty.');
  assert.equal(checkoutBody({ id: 'x', cart, paymentType: 'cheque', destination: null }).problem, 'Choose how to pay.');
  assert.equal(checkoutBody({ id: 'x', cart, paymentType: 'card', destination: null }).problem, 'Choose where to deliver.');
  assert.equal(checkoutBody({ id: 'x', cart, paymentType: 'card', destination: null }).body, null);
  for (const p of ['card', 'cash', 'wallet']) assert.equal(checkoutBody({ id: 'x', cart, paymentType: p, destination: to }).problem, '');
});

test('checkoutProblem shows the server\'s words, every named field, and something honest otherwise', () => {
  assert.equal(checkoutProblem(422, { error: 'restaurant Corner Kitchen is closed' }), 'restaurant Corner Kitchen is closed');
  assert.equal(checkoutProblem(422, { error: 'Fries is sold out' }), 'Fries is sold out');
  assert.equal(checkoutProblem(422, { error: 'x', fields: { line1: 'is required', latitude: 'must be between -90 and 90' } }),
    'Street is required. Latitude must be between -90 and 90');
  assert.equal(checkoutProblem(422, { fields: {} }), 'The request failed (HTTP 422).');
  assert.equal(checkoutProblem(400, { error: 'invalid request body', fields: { line1: 'is required' } }), 'invalid request body');
  assert.match(checkoutProblem(0, null), /^Can't reach the server\. Your cart is kept/);
  assert.equal(checkoutProblem(500, null), 'The server had a problem. Try again in a moment.');
  assert.equal(checkoutProblem(401, null), 'Your session ended. Sign in again.');
});
