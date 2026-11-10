// Run with: node --test src/lib/*.test.mjs
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  validateOrderForm, menuTotalCents, validateMenuItemForm, validateRestaurantName, MAX_LINE_QUANTITY, MAX_ORDER_LINES,
} from './logic.js';

const menu = [{ id: 'burger', priceCents: 950 }, { id: 'fries', priceCents: 400 }];

test('the menu total is the sum of price times quantity, and ignores what is not a quantity', () => {
  assert.equal(menuTotalCents(menu, { burger: 2, fries: 1 }), 2300);
  assert.equal(menuTotalCents(menu, { burger: '3' }), 2850);
  for (const q of [0, -1, 1.5, 'x', '', null, undefined]) assert.equal(menuTotalCents(menu, { burger: q }), 0, String(q));
  assert.equal(menuTotalCents(menu, { ghost: 5 }), 0);
  assert.equal(menuTotalCents(menu, undefined), 0);
  assert.equal(menuTotalCents(undefined, { burger: 1 }), 0);
});

test('menu lines go to the server as an id and a count, with no name or price', () => {
  const r = validateOrderForm({
    id: 'SD-1', customerId: 'c', restaurantId: 'r1', paymentType: 'card',
    menu: [{ menuItemId: 'burger', quantity: 2 }, { menuItemId: 'fries', quantity: '1' }], items: [],
  });
  assert.equal(r.ok, true);
  assert.deepEqual(r.payload.items, [{ menuItemId: 'burger', quantity: 2 }, { menuItemId: 'fries', quantity: 1 }]);
});

test('menu lines and a custom line can be on one order', () => {
  const r = validateOrderForm({
    id: 'SD-1', customerId: 'c', restaurantId: 'r1', paymentType: 'cash',
    menu: [{ menuItemId: 'burger', quantity: 1 }], items: [{ name: 'Cake', quantity: '1', price: '42' }],
  });
  assert.equal(r.ok, true);
  assert.deepEqual(r.payload.items, [{ menuItemId: 'burger', quantity: 1 }, { sku: 'CAKE', name: 'Cake', quantity: 1, unitPriceCents: 4200 }]);
});

test('a menu line with no item or a silly count is named, and an empty order is refused', () => {
  const form = (menuLines) => ({ id: 'SD-1', customerId: 'c', restaurantId: 'r1', paymentType: 'card', menu: menuLines, items: [] });
  assert.ok(validateOrderForm(form([{ menuItemId: '', quantity: 1 }])).errors['menu.0']);
  for (const quantity of [0, -1, 1.5, 'x', MAX_LINE_QUANTITY + 1]) {
    const r = validateOrderForm(form([{ menuItemId: 'burger', quantity }]));
    assert.equal(r.ok, false, String(quantity));
    assert.match(r.errors['menu.0'], /1 to 50/);
    assert.equal(r.errors.items, undefined, 'the line is named, not "add an item"');
  }
  assert.equal(validateOrderForm(form([{ menuItemId: 'burger', quantity: MAX_LINE_QUANTITY }])).ok, true);
  assert.equal(validateOrderForm(form([])).errors.items, 'Add at least one item');
  const many = Array.from({ length: MAX_ORDER_LINES + 1 }, (_, i) => ({ menuItemId: `m${i}`, quantity: 1 }));
  assert.match(validateOrderForm(form(many)).errors.items, /at most 30 lines/);
  assert.equal(validateOrderForm(form(many.slice(0, MAX_ORDER_LINES))).ok, true);
});

test('a menu item as typed becomes a name, a description and a price in cents', () => {
  const r = validateMenuItemForm({ name: '  Smash burger ', description: ' two patties ', price: '9.50' });
  assert.equal(r.ok, true);
  assert.deepEqual(r.payload, { name: 'Smash burger', description: 'two patties', priceCents: 950 });
  assert.equal(validateMenuItemForm({ name: 'Water', price: '0' }).payload.priceCents, 0);
  assert.equal(validateMenuItemForm({ name: 'Water', description: undefined, price: '0' }).payload.description, '');
  assert.equal(validateMenuItemForm({ name: 'Feast', price: '10000' }).ok, true);
});

test('a menu item names what is wrong with it', () => {
  assert.deepEqual(Object.keys(validateMenuItemForm({}).errors).sort(), ['name', 'priceCents']);
  assert.ok(validateMenuItemForm({ name: 'x'.repeat(81), price: '1' }).errors.name);
  assert.equal(validateMenuItemForm({ name: 'x'.repeat(80), price: '1' }).ok, true);
  assert.ok(validateMenuItemForm({ name: 'x', price: '1', description: 'y'.repeat(301) }).errors.description);
  assert.equal(validateMenuItemForm({ name: 'x', price: '1', description: 'y'.repeat(300) }).ok, true);
  for (const price of ['', '-1', 'abc', '10000.01']) assert.ok(validateMenuItemForm({ name: 'x', price }).errors.priceCents, price);
  assert.equal(validateMenuItemForm({}).payload, null);
});

test('a restaurant needs a name of up to 80 characters', () => {
  assert.deepEqual(validateRestaurantName('  Noodle Bar '), { ok: true, error: '', name: 'Noodle Bar' });
  for (const bad of ['', '   ', undefined, null]) assert.equal(validateRestaurantName(bad).ok, false, String(bad));
  assert.equal(validateRestaurantName('x'.repeat(80)).ok, true);
  assert.match(validateRestaurantName('x'.repeat(81)).error, /80/);
});
