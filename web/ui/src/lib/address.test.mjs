// Run with: node --test "src/lib/*.test.mjs"
import test from 'node:test';
import assert from 'node:assert/strict';
import { addressLine, candidateToDraft, validateAddressForm, validateOrderForm, fieldErrors } from './logic.js';

test('an address on one line leaves out what is not there', () => {
  assert.equal(addressLine({ line1: '12 MG Road', city: 'Bengaluru', postalCode: '560038' }), '12 MG Road, Bengaluru 560038');
  assert.equal(addressLine({ line1: '12 MG Road', city: 'Bengaluru' }), '12 MG Road, Bengaluru');
  assert.equal(addressLine({ line1: '12 MG Road', postalCode: '560038' }), '12 MG Road, 560038');
  assert.equal(addressLine({ line1: '12 MG Road' }), '12 MG Road');
  assert.equal(addressLine({ city: 'Bengaluru' }), 'Bengaluru');
  for (const nothing of [null, undefined, {}]) assert.equal(addressLine(nothing), '', String(nothing));
});

test('a place the search found starts the address form, with the numbers as text', () => {
  assert.deepEqual(candidateToDraft({ label: 'long label', line1: '12 MG Road', city: 'Bengaluru', postalCode: '560038', latitude: 12.9719, longitude: 77.6412 }), {
    label: '', line1: '12 MG Road', landmark: '', city: 'Bengaluru', postalCode: '560038', latitude: '12.9719', longitude: '77.6412',
  });
  // a place with no words (where the person is, not named)
  assert.deepEqual(candidateToDraft({ latitude: -1.5, longitude: 0 }), {
    label: '', line1: '', landmark: '', city: '', postalCode: '', latitude: '-1.5', longitude: '0',
  });
});

test('a good address becomes what is sent, leaving out what was not typed', () => {
  const r = validateAddressForm({ label: ' Home ', line1: ' 12 MG Road ', landmark: ' blue gate', city: 'Bengaluru', postalCode: '560038', latitude: '12.9719', longitude: ' 77.6412' });
  assert.equal(r.ok, true);
  assert.deepEqual(r.payload, { label: 'Home', line1: '12 MG Road', landmark: 'blue gate', city: 'Bengaluru', postalCode: '560038', latitude: 12.9719, longitude: 77.6412 });
  assert.deepEqual(validateAddressForm({ line1: 'x', latitude: '0', longitude: '0' }).payload, { line1: 'x', latitude: 0, longitude: 0 }); // 0, 0 is a place
  assert.deepEqual(validateAddressForm({ line1: 'x', label: '  ', landmark: '', latitude: '90', longitude: '-180' }).payload, { line1: 'x', latitude: 90, longitude: -180 });
});

test('an address names what is wrong with it', () => {
  const errs = (form) => validateAddressForm({ line1: 'x', latitude: '1', longitude: '1', ...form }).errors;
  assert.match(errs({ line1: '  ' }).line1, /required/);
  assert.match(errs({ line1: 'x'.repeat(201) }).line1, /at most 200/);
  assert.match(errs({ landmark: 'x'.repeat(201) }).landmark, /at most 200/);
  assert.match(errs({ label: 'x'.repeat(41) }).label, /at most 40/);
  assert.match(errs({ city: 'x'.repeat(81) }).city, /at most 80/);
  assert.match(errs({ postalCode: '1'.repeat(21) }).postalCode, /at most 20/);
  for (const latitude of ['', 'north', '91', '-91', 'NaN']) assert.ok(errs({ latitude }).latitude, `latitude ${latitude}`);
  for (const longitude of ['', 'east', '181', '-181']) assert.ok(errs({ longitude }).longitude, `longitude ${longitude}`);
  const bad = validateAddressForm({});
  assert.equal(bad.ok, false);
  assert.equal(bad.payload, null);
  assert.deepEqual(Object.keys(bad.errors).sort(), ['latitude', 'line1', 'longitude']);
  // the limits are characters, not bytes, and are allowed exactly
  assert.equal(validateAddressForm({ line1: 'é'.repeat(200), landmark: 'é'.repeat(200), label: 'é'.repeat(40), city: 'é'.repeat(80), postalCode: '1'.repeat(20), latitude: '1', longitude: '1' }).ok, true);
});

const form = (extra = {}) => ({
  id: 'SD-1', customerId: 'c', restaurantId: 'r1', paymentType: 'card', menu: [{ menuItemId: 'burger', quantity: 1 }], items: [], ...extra,
});

test('an order is sent with a saved address by its id', () => {
  const r = validateOrderForm(form({ deliveryAddressId: 'a-home' }));
  assert.equal(r.ok, true);
  assert.equal(r.payload.deliveryAddressId, 'a-home');
  assert.equal('deliveryAddress' in r.payload, false);
});

test('an order is sent with an address of its own in the body', () => {
  const address = { line1: '12 MG Road', latitude: 12.9719, longitude: 77.6412 };
  const r = validateOrderForm(form({ deliveryAddress: address }));
  assert.equal(r.ok, true);
  assert.deepEqual(r.payload.deliveryAddress, address);
  assert.equal('deliveryAddressId' in r.payload, false);
});

test('a saved address wins if both are given, because the server refuses both', () => {
  const r = validateOrderForm(form({ deliveryAddressId: 'a-home', deliveryAddress: { line1: 'x', latitude: 1, longitude: 1 } }));
  assert.equal(r.payload.deliveryAddressId, 'a-home');
  assert.equal('deliveryAddress' in r.payload, false);
});

test('an order with neither is still a valid form: where it is going is asked for by the sheet and checked by the server', () => {
  const r = validateOrderForm(form());
  assert.equal(r.ok, true);
  assert.equal('deliveryAddress' in r.payload || 'deliveryAddressId' in r.payload, false);
});

test('the server\'s complaints about an address read as sentences', () => {
  const said = fieldErrors({ fields: { line1: 'is required', landmark: 'must be at most 200 characters', city: 'must be at most 80 characters', postalCode: 'must be at most 20 characters', label: 'must be at most 40 characters' } });
  assert.equal(said.line1, 'Street is required');
  assert.equal(said.landmark, 'Landmark must be at most 200 characters');
  assert.equal(said.city, 'City must be at most 80 characters');
  assert.equal(said.postalCode, 'Postal code must be at most 20 characters');
  assert.equal(said.label, 'Name must be at most 40 characters');
});
