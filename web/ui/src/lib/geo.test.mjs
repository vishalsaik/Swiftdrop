// Run with: node --test "src/lib/*.test.mjs"
import test from 'node:test';
import assert from 'node:assert/strict';
import { roundCoord, searchQueryString, formatDistance, locationProblem, validatePlaceForm, validateMenuItemForm, fieldErrors } from './logic.js';

test('a position is kept to three decimals, about 110 metres', () => {
  assert.equal(roundCoord(12.97194), 12.972);
  assert.equal(roundCoord('77.64149'), 77.641);
  assert.equal(roundCoord(-77.6412), -77.641);
  assert.equal(roundCoord(0.0004), 0);
});

test('the search query holds only what was asked for', () => {
  assert.equal(searchQueryString(), '');
  assert.equal(searchQueryString({}), '');
  assert.equal(searchQueryString({ q: '   ' }), '');
  assert.equal(searchQueryString({ q: '  pizza ' }), '?q=pizza');
  assert.equal(searchQueryString({ q: 'dal & rice' }), '?q=dal+%26+rice');
  assert.equal(searchQueryString({ veg: true }), '?veg=true');
  assert.equal(searchQueryString({ veg: false }), '');
  assert.equal(searchQueryString({ maxPriceCents: 500 }), '?maxPrice=500');
  for (const bad of [0, -1, 1.5, NaN, '5', undefined]) assert.equal(searchQueryString({ maxPriceCents: bad }), '', String(bad));
});

test('a position in the query is rounded, and only a whole position is sent', () => {
  assert.equal(searchQueryString({ location: { lat: 12.97194, lng: 77.64149 } }), '?lat=12.972&lng=77.641');
  assert.equal(searchQueryString({ q: 'tea', veg: true, location: { lat: 1, lng: -2 } }), '?q=tea&lat=1&lng=-2&veg=true');
  for (const location of [null, undefined, {}, { lat: 12.9 }, { lng: 77.6 }, { lat: NaN, lng: 1 }, { lat: 1, lng: Infinity }]) {
    assert.equal(searchQueryString({ location }), '', JSON.stringify(location));
  }
});

test('a distance is said the way a person would', () => {
  assert.equal(formatDistance(0), '50 m');
  assert.equal(formatDistance(0.02), '50 m');
  assert.equal(formatDistance(0.7), '700 m');
  assert.equal(formatDistance(0.724), '700 m');
  assert.equal(formatDistance(0.99), '1000 m');
  assert.equal(formatDistance(1), '1.0 km');
  assert.equal(formatDistance(2.34), '2.3 km');
  assert.equal(formatDistance(9.96), '10.0 km');
  assert.equal(formatDistance(10), '10 km');
  assert.equal(formatDistance(12.6), '13 km');
  for (const bad of [-1, NaN, Infinity, undefined, null]) assert.equal(formatDistance(bad), '', String(bad));
});

test('each reason for no position has its own words, and says what to do instead', () => {
  const said = [1, 2, 3, 0, 99].map(locationProblem);
  assert.match(said[0], /turned off.*search by name/i);
  assert.match(said[2], /too long/i);
  assert.match(said[1], /could not be found/i);
  assert.equal(said[3], said[1]);
  assert.equal(said[4], said[1]);
  assert.equal(new Set([said[0], said[1], said[2]]).size, 3);
});

test('a restaurant place as typed becomes numbers, and a blank radius is left to the server', () => {
  const r = validatePlaceForm({ cuisine: '  North Indian ', latitude: ' 12.9719', longitude: '77.6412 ', radiusKm: '7.5' });
  assert.equal(r.ok, true);
  assert.deepEqual(r.payload, { cuisine: 'North Indian', latitude: 12.9719, longitude: 77.6412, deliveryRadiusKm: 7.5 });
  assert.deepEqual(validatePlaceForm({ cuisine: '', latitude: '', longitude: '', radiusKm: '' }).payload, { cuisine: '' });
  assert.deepEqual(validatePlaceForm({}).payload, { cuisine: '' });
  assert.deepEqual(validatePlaceForm({ latitude: '-90', longitude: '-180', radiusKm: '100' }).payload,
    { cuisine: '', latitude: -90, longitude: -180, deliveryRadiusKm: 100 });
  assert.deepEqual(validatePlaceForm({ latitude: '0', longitude: '0' }).payload, { cuisine: '', latitude: 0, longitude: 0 });
});

test('a restaurant place names what is wrong', () => {
  const errs = (form) => validatePlaceForm(form).errors;
  assert.ok(errs({ latitude: '12.9' }).longitude, 'longitude alone');
  assert.ok(errs({ longitude: '77.6' }).latitude, 'latitude alone');
  assert.ok(errs({ latitude: 'north', longitude: '1' }).latitude);
  assert.ok(errs({ latitude: '91', longitude: '1' }).latitude);
  assert.ok(errs({ latitude: '1', longitude: '181' }).longitude);
  assert.ok(errs({ latitude: '1', longitude: '-181' }).longitude);
  for (const radiusKm of ['0', '-1', '100.5', 'far']) assert.ok(errs({ radiusKm }).deliveryRadiusKm, radiusKm);
  assert.ok(errs({ cuisine: 'x'.repeat(41) }).cuisine);
  assert.equal(validatePlaceForm({ cuisine: 'x'.repeat(40) }).ok, true);
  const bad = validatePlaceForm({ latitude: '1' });
  assert.equal(bad.ok, false);
  assert.equal(bad.payload, null);
});

test('a menu item may say it is vegetarian, and says nothing when it is not asked', () => {
  assert.equal(validateMenuItemForm({ name: 'Dal', price: '4', veg: true }).payload.veg, true);
  assert.equal(validateMenuItemForm({ name: 'Wings', price: '4', veg: false }).payload.veg, false);
  assert.equal('veg' in validateMenuItemForm({ name: 'Wings', price: '4' }).payload, false);
});

test('the server\'s field complaints about places and veg read as sentences', () => {
  const said = fieldErrors({ fields: { latitude: 'and longitude go together: send both or neither', deliveryRadiusKm: 'must be above 0 and at most 100', cuisine: 'must be at most 40 characters' } });
  assert.equal(said.latitude, 'Latitude and longitude go together: send both or neither');
  assert.equal(said.deliveryRadiusKm, 'Delivery radius must be above 0 and at most 100');
  assert.equal(said.cuisine, 'Cuisine must be at most 40 characters');
});
