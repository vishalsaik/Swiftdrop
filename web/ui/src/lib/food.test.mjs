// Run with: node --test "src/lib/*.test.mjs"
import test from 'node:test';
import assert from 'node:assert/strict';
import { distanceKm, restaurantCards, cardStatusText, filterMenu } from './logic.js';

const indiranagar = { lat: 12.9719, lng: 77.6412 };

test('distance is the great-circle distance, the same sum as the server', () => {
  assert.equal(distanceKm(1, 2, 1, 2), 0);
  // one degree of latitude is about 111.19 km on a 6371 km earth
  assert.ok(Math.abs(distanceKm(0, 0, 1, 0) - 111.195) < 0.001);
  // Indiranagar to Bandra, about 845 km in a straight line
  const d = distanceKm(12.9719, 77.6412, 19.0596, 72.8295);
  assert.ok(d > 840 && d < 850, String(d));
  assert.equal(distanceKm(12.9719, 77.6412, 19.0596, 72.8295), distanceKm(19.0596, 72.8295, 12.9719, 77.6412));
  // the far side of the earth does not fail on rounding (a is capped at 1)
  assert.ok(Math.abs(distanceKm(0, 0, 0, 180) - Math.PI * 6371) < 0.001);
  // here the sum rounds to just over 1 (the square root rounds it back, and the cap keeps it so)
  assert.ok(Math.abs(distanceKm(-87.5, 0, 87.5, 180) - Math.PI * 6371) < 0.001);
});

const place = (id, name, lat, lng, radius = 5) => ({ id, name, active: true, latitude: lat, longitude: lng, deliveryRadiusKm: radius });

test('with no address every open restaurant is a card, in the server\'s order', () => {
  const list = [place('b', 'Bravo', 1, 1), { id: 'a', name: 'Alpha', active: true, deliveryRadiusKm: 5 }, { id: 'c', name: 'Closed', active: false, deliveryRadiusKm: 5 }];
  for (const near of [null, undefined, { lat: NaN, lng: 1 }, { lat: 1, lng: Infinity }, { lat: 1 }]) {
    const cards = restaurantCards(list, near);
    assert.deepEqual(cards.map((c) => [c.restaurant.id, c.status, c.distanceKm]), [['b', 'open', undefined], ['a', 'open', undefined]], JSON.stringify(near));
  }
  assert.deepEqual(restaurantCards(null, null), []);
  assert.deepEqual(restaurantCards(undefined, indiranagar), []);
});

test('with an address a card says whether it delivers there, by the server\'s rule', () => {
  const here = place('here', 'Here', 12.9719, 77.6412, 5);
  const far = place('far', 'Far', 19.0596, 72.8295, 5);
  const unplaced = { id: 'u', name: 'Unplaced', active: true, deliveryRadiusKm: 5 };
  const halfPlaced = { id: 'h', name: 'Half', active: true, latitude: 12.97, deliveryRadiusKm: 5 };
  const cards = restaurantCards([far, unplaced, here, halfPlaced], indiranagar);
  const by = Object.fromEntries(cards.map((c) => [c.restaurant.id, c]));
  assert.equal(by.here.status, 'delivers');
  assert.equal(by.here.distanceKm, 0);
  assert.equal(by.far.status, 'too_far');
  assert.ok(by.far.distanceKm > 800);
  assert.equal(by.u.status, 'not_placed');
  assert.equal(by.u.distanceKm, undefined);
  assert.equal(by.h.status, 'not_placed');
});

test('the radius itself still delivers, a hair beyond it does not', () => {
  const r = place('r', 'Edge', 13.0, 77.6412, 1);
  const exact = distanceKm(indiranagar.lat, indiranagar.lng, r.latitude, r.longitude);
  assert.equal(restaurantCards([{ ...r, deliveryRadiusKm: exact }], indiranagar)[0].status, 'delivers');
  assert.equal(restaurantCards([{ ...r, deliveryRadiusKm: exact - 1e-9 }], indiranagar)[0].status, 'too_far');
});

test('those that deliver come first, nearest first; the rest follow by name', () => {
  const near1 = place('n1', 'Zed Near', 12.9725, 77.6412, 5); // about 70 m
  const near2 = place('n2', 'Alpha Further', 12.99, 77.6412, 5); // about 2 km
  const far = place('f', 'Beta Far', 19.0596, 72.8295, 5);
  const unplaced = { id: 'u', name: 'Aardvark Unplaced', active: true, deliveryRadiusKm: 5 };
  const cards = restaurantCards([far, near2, unplaced, near1], indiranagar);
  assert.deepEqual(cards.map((c) => c.restaurant.id), ['n1', 'n2', 'u', 'f']);
  // the same distance falls back to the name
  const twinA = place('ta', 'Apple', 12.98, 77.6412, 5);
  const twinB = place('tb', 'Banana', 12.98, 77.6412, 5);
  assert.deepEqual(restaurantCards([twinB, twinA], indiranagar).map((c) => c.restaurant.id), ['ta', 'tb']);
});

test('a card says what it means in words', () => {
  assert.equal(cardStatusText({ status: 'open' }), 'Open');
  assert.equal(cardStatusText({ status: 'delivers' }, 'Home'), 'Delivers to Home');
  assert.equal(cardStatusText({ status: 'delivers' }), 'Delivers to you');
  assert.equal(cardStatusText({ status: 'too_far' }, 'Work'), 'Too far to deliver to Work');
  assert.equal(cardStatusText({ status: 'too_far' }, ''), 'Too far to deliver to you');
  assert.equal(cardStatusText({ status: 'not_placed' }, 'Home'), 'Not taking delivery orders yet');
});

const menu = [
  { id: 'paneer', name: 'Paneer tikka', description: 'Smoky, from the tandoor', veg: true, priceCents: 900 },
  { id: 'chicken', name: 'Chicken tikka', description: 'Smoky, from the tandoor', priceCents: 1100 },
  { id: 'dal', name: 'Dal makhani', veg: true, priceCents: 700 },
  { id: 'naan', name: 'Butter naan', description: undefined, veg: false, priceCents: 200 },
];
const ids = (list) => list.map((m) => m.id);

test('the menu filter keeps what matches every typed word, in the name or the description, any case', () => {
  assert.deepEqual(ids(filterMenu(menu)), ['paneer', 'chicken', 'dal', 'naan']);
  assert.deepEqual(ids(filterMenu(menu, { text: '   ' })), ['paneer', 'chicken', 'dal', 'naan']);
  assert.deepEqual(ids(filterMenu(menu, { text: 'TIKKA' })), ['paneer', 'chicken']);
  assert.deepEqual(ids(filterMenu(menu, { text: 'tandoor' })), ['paneer', 'chicken']);
  assert.deepEqual(ids(filterMenu(menu, { text: '  smoky   chicken ' })), ['chicken']);
  assert.deepEqual(ids(filterMenu(menu, { text: 'pizza' })), []);
  assert.deepEqual(ids(filterMenu(menu, { text: 'undefined' })), [], 'a missing description is not the word "undefined"');
  assert.deepEqual(filterMenu(null, { text: 'x' }), []);
});

test('vegetarian only keeps the items marked veg, and combines with the words', () => {
  assert.deepEqual(ids(filterMenu(menu, { vegOnly: true })), ['paneer', 'dal']);
  assert.deepEqual(ids(filterMenu(menu, { text: 'tikka', vegOnly: true })), ['paneer']);
  assert.deepEqual(ids(filterMenu(menu, { text: 'naan', vegOnly: true })), []);
  assert.deepEqual(ids(filterMenu(menu, { vegOnly: false })), ['paneer', 'chicken', 'dal', 'naan']);
});
