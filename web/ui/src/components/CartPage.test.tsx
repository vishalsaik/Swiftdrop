import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, renderHook, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { App } from '../App';
import { setSession } from '../api';
import { CartProvider, useCart, type CartItem } from '../hooks/cart';
import { cartKey } from '../lib/logic';
import type { MenuItem, Order, Restaurant } from '../lib/types';
import { home } from '../test/deliver';
import { customer, mockServer, order, session, type Handler } from '../test/server';

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

const kitchen: Restaurant = { id: 'r1', name: 'Corner Kitchen', active: true, cuisine: 'Burgers', latitude: 12.9725, longitude: 77.6412, deliveryRadiusKm: 5 };
const tandoor: Restaurant = { id: 'r2', name: 'Bandra Tandoor', active: true, cuisine: 'North Indian', latitude: 19.0596, longitude: 72.8295, deliveryRadiusKm: 5 };
const burger: MenuItem = { id: 'burger', restaurantId: 'r1', name: 'Smash burger', priceCents: 950, available: true };
const fries: MenuItem = { id: 'fries', restaurantId: 'r1', name: 'Fries', priceCents: 400, available: true, veg: true };
const paneer: MenuItem = { id: 'paneer', restaurantId: 'r2', name: 'Paneer tikka', priceCents: 900, available: true, veg: true };

const KEY = cartKey(customer.id);
const keptCart = (lines = [{ menuItemId: 'burger', name: 'Smash burger', unitPriceCents: 950, quantity: 2 }, { menuItemId: 'fries', name: 'Fries', unitPriceCents: 400, quantity: 1 }]) =>
  JSON.stringify({ restaurantId: 'r1', restaurantName: 'Corner Kitchen', lines });
const stored = () => JSON.parse(localStorage.getItem(KEY) ?? 'null') as { restaurantId: string; lines: { menuItemId: string; quantity: number }[] } | null;

function serve(extra: Record<string, Handler> = {}, placed: Order[] = []) {
  return mockServer({
    'GET /auth/me': () => ({ body: session(customer) }),
    'GET /orders': () => ({ body: { orders: placed } }),
    'GET /addresses': () => ({ body: { addresses: [home] } }),
    'GET /restaurants': () => ({ body: { restaurants: [kitchen, tandoor] } }),
    'GET /restaurants/r1/menu': () => ({ body: { restaurant: kitchen, items: [burger, fries] } }),
    'GET /restaurants/r2/menu': () => ({ body: { restaurant: tandoor, items: [paneer] } }),
    'GET /search': () => ({ body: { results: [], located: false } }),
    'POST /orders': (c) => {
      const o = order({ id: (c.body as { id: string }).id, state: 'created' });
      placed.push(o);
      return { status: 201, body: o };
    },
    ...extra,
  });
}

type User = ReturnType<typeof userEvent.setup>;
const cartButton = () => screen.getByRole('button', { name: /^Cart,/ });
const tabs = () => within(screen.getByRole('navigation', { name: 'Sections' }));
const page = () => within(screen.getByRole('region', { name: 'Your cart' }));
const lineTexts = () => within(page().getByRole('list', { name: 'In your cart' })).getAllByRole('listitem').map((li) => li.querySelector('.row-title')?.textContent);
const placeButton = () => page().getByRole('button', { name: /^(Place order|Placing your order)/ });

/** Signs in with a kept cart and opens the cart page. */
async function openCart(user: User) {
  render(<App />);
  await user.click(await screen.findByRole('button', { name: /^Cart, \d/ }));
  await screen.findByRole('heading', { name: 'Your cart' });
}

async function openKitchen(user: User) {
  render(<App />);
  await user.click(await screen.findByRole('button', { name: /^Corner Kitchen/ }));
  await screen.findByRole('list', { name: 'Menu' });
}

describe('the cart is kept in the browser', () => {
  it('survives a reload, for the same account', async () => {
    const user = userEvent.setup();
    serve();
    const first = render(<App />);
    await user.click(await screen.findByRole('button', { name: /^Corner Kitchen/ }));
    await user.click(await screen.findByRole('button', { name: 'More Smash burger' }));
    await user.click(screen.getByRole('button', { name: 'More Smash burger' }));
    await waitFor(() => expect(stored()?.lines).toEqual([{ menuItemId: 'burger', name: 'Smash burger', unitPriceCents: 950, quantity: 2 }]));
    expect(stored()?.restaurantId).toBe('r1');
    first.unmount();
    setSession(null);

    render(<App />);
    await waitFor(() => expect(cartButton().getAttribute('aria-label')).toBe('Cart, 2 items'));
    await user.click(cartButton());
    expect(await screen.findByRole('heading', { name: 'Corner Kitchen' })).toBeTruthy();
    expect(lineTexts()).toEqual(['Smash burger']);
  });

  it('is one cart per account: another customer does not see it', async () => {
    localStorage.setItem(cartKey('u-someone-else'), keptCart());
    serve();
    render(<App />);
    await screen.findByRole('list', { name: 'Restaurants' });
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, empty');
    expect(localStorage.getItem(cartKey('u-someone-else'))).toBe(keptCart());
  });

  it('reads a kept cart it cannot trust as empty, and keeps only the lines it can', async () => {
    localStorage.setItem(KEY, '{"restaurantId": "r1", "lines": [');
    serve();
    const first = render(<App />);
    await screen.findByRole('list', { name: 'Restaurants' });
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, empty');
    first.unmount();
    setSession(null);

    localStorage.setItem(KEY, keptCart([
      { menuItemId: 'burger', name: 'Smash burger', unitPriceCents: 950, quantity: 900 },
      { menuItemId: 'fries', name: 'Fries', unitPriceCents: -5, quantity: 1 },
    ]));
    render(<App />);
    await waitFor(() => expect(cartButton().getAttribute('aria-label')).toBe('Cart, 50 items'));
  });

  it('is removed from the browser when it is emptied', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart([{ menuItemId: 'fries', name: 'Fries', unitPriceCents: 400, quantity: 1 }]));
    serve();
    await openCart(user);
    await user.click(page().getByRole('button', { name: 'Remove Fries' }));
    expect(await screen.findByText('Your cart is empty')).toBeTruthy();
    expect(localStorage.getItem(KEY)).toBeNull();
  });

  it('follows the same account in another tab', async () => {
    localStorage.setItem(KEY, keptCart());
    serve();
    render(<App />);
    await waitFor(() => expect(cartButton().getAttribute('aria-label')).toBe('Cart, 3 items'));
    const next = keptCart([{ menuItemId: 'fries', name: 'Fries', unitPriceCents: 400, quantity: 4 }]);
    localStorage.setItem(KEY, next);
    act(() => { window.dispatchEvent(new StorageEvent('storage', { key: KEY, newValue: next })); });
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 4 items');
    // another key changing means nothing; storage cleared elsewhere (key null) empties it
    act(() => { window.dispatchEvent(new StorageEvent('storage', { key: 'something.else', newValue: '{}' })); });
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 4 items');
    act(() => { window.dispatchEvent(new StorageEvent('storage', { key: null })); });
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, empty');
  });

  it('still works in memory when the browser blocks storage, and says it is not kept', async () => {
    const user = userEvent.setup();
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new DOMException('blocked', 'SecurityError'); });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new DOMException('blocked', 'SecurityError'); });
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => { throw new DOMException('blocked', 'SecurityError'); });
    const server = serve();
    await openKitchen(user);
    await user.click(screen.getByRole('button', { name: 'More Fries' }));
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 1 item');
    await user.click(screen.getByRole('button', { name: /View cart/ }));
    expect(await screen.findByText('This browser is not keeping your cart, so it is gone if you close the page.')).toBeTruthy();
    await user.click(page().getByRole('button', { name: 'Deliver to Home' }));
    await user.click(placeButton());
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    expect(await screen.findByRole('heading', { name: 'Your orders' })).toBeTruthy();
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, empty');
  });

  it('says nothing about keeping it when the browser keeps it', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart());
    serve();
    await openCart(user);
    expect(screen.queryByText(/not keeping your cart/)).toBeNull();
  });

  it('notices when storage stops working (full) and says so', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart());
    serve();
    await openCart(user);
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new DOMException('full', 'QuotaExceededError'); });
    await user.click(page().getByRole('button', { name: 'More Fries' }));
    expect(await screen.findByText(/not keeping your cart/)).toBeTruthy();
    expect(page().getByLabelText('Fries in cart').textContent).toBe('2');
  });
});

describe('one restaurant at a time', () => {
  it('asks before another restaurant empties the cart, and Keep my cart keeps it', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart());
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /^Bandra Tandoor/ }));
    expect((await screen.findByRole('note')).textContent).toBe('Your cart has food from Corner Kitchen. Adding something here asks before it starts a new cart.');
    await user.click(await screen.findByRole('button', { name: 'More Paneer tikka' }));

    const ask = screen.getByRole('alertdialog', { name: 'Start a new cart?' });
    expect(ask.textContent).toContain('Your cart has 3 items from Corner Kitchen. An order comes from one restaurant, so adding Paneer tikka empties it.');
    await user.click(within(ask).getByRole('button', { name: 'Keep my cart' }));
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 3 items');
    expect(screen.getByLabelText('Paneer tikka in cart').textContent).toBe('0');
    expect(stored()?.restaurantId).toBe('r1');
  });

  it('Start a new cart empties it and puts the item in a new one', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart([{ menuItemId: 'fries', name: 'Fries', unitPriceCents: 400, quantity: 1 }]));
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /^Bandra Tandoor/ }));
    await user.click(await screen.findByRole('button', { name: 'More Paneer tikka' }));
    expect(screen.getByRole('alertdialog').textContent).toContain('Your cart has 1 item from Corner Kitchen.');
    await user.click(screen.getByRole('button', { name: 'Start a new cart' }));
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(screen.queryByRole('note')).toBeNull();
    expect(screen.getByLabelText('Paneer tikka in cart').textContent).toBe('1');
    await waitFor(() => expect(stored()).toMatchObject({ restaurantId: 'r2', lines: [{ menuItemId: 'paneer', quantity: 1 }] }));
    await user.click(screen.getByRole('button', { name: /View cart/ }));
    expect(await screen.findByRole('heading', { name: 'Bandra Tandoor' })).toBeTruthy();
    expect(lineTexts()).toEqual(['Paneer tikka']);
  });

  it('Add in the search results asks too, rather than replacing the cart unseen', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart());
    serve({ 'GET /search': () => ({ body: { located: false, results: [{ restaurant: tandoor, items: [paneer] }] } }) });
    render(<App />);
    await user.type(await screen.findByLabelText('Search food'), 'paneer');
    await user.click(await screen.findByRole('button', { name: 'Add Paneer tikka from Bandra Tandoor' }));
    expect(await screen.findByRole('alertdialog', { name: 'Start a new cart?' })).toBeTruthy();
    expect(screen.getByLabelText('Paneer tikka in cart').textContent).toBe('0');
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 3 items');
    await user.click(screen.getByRole('button', { name: 'Start a new cart' }));
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 1 item');
  });
});

describe('the cart page', () => {
  it('opens from the header on any tab, lists the lines, and Back returns to that tab', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart());
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Orders' }));
    await user.click(screen.getByRole('button', { name: 'Cart, 3 items' }));
    expect(screen.getByRole('button', { name: 'Cart, 3 items' }).getAttribute('aria-current')).toBe('page');
    expect(page().getByRole('heading', { name: 'Corner Kitchen' })).toBeTruthy();
    expect(lineTexts()).toEqual(['Smash burger', 'Fries']);
    expect(page().getByLabelText('Smash burger comes to $19.00')).toBeTruthy();
    expect(page().getByText('$9.50 each')).toBeTruthy();
    expect(page().getByText('$23.00')).toBeTruthy();
    expect(placeButton().textContent).toBe('Place order · $23.00');
    expect(screen.queryByRole('heading', { name: 'Your orders' })).toBeNull();
    await user.click(page().getByRole('button', { name: 'Back' }));
    expect(await screen.findByRole('heading', { name: 'Your orders' })).toBeTruthy();
  });

  it('a tab leaves it for that tab', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart());
    serve();
    await openCart(user);
    await user.click(tabs().getByRole('button', { name: 'Account' }));
    expect(screen.queryByRole('region', { name: 'Your cart' })).toBeNull();
    expect(screen.getByRole('region', { name: 'Account' })).toBeTruthy();
  });

  it('keeps a quantity from 1 to 50 with its steppers, and Remove takes a line out', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart([{ menuItemId: 'fries', name: 'Fries', unitPriceCents: 400, quantity: 1 }, { menuItemId: 'burger', name: 'Smash burger', unitPriceCents: 950, quantity: 49 }]));
    serve();
    await openCart(user);
    expect((page().getByRole('button', { name: 'Fewer Fries' }) as HTMLButtonElement).disabled).toBe(true);
    await user.click(page().getByRole('button', { name: 'More Fries' }));
    expect(page().getByLabelText('Fries in cart').textContent).toBe('2');
    await user.click(page().getByRole('button', { name: 'Fewer Fries' }));
    expect(page().getByLabelText('Fries in cart').textContent).toBe('1');
    await user.click(page().getByRole('button', { name: 'More Smash burger' }));
    expect(page().getByLabelText('Smash burger in cart').textContent).toBe('50');
    expect((page().getByRole('button', { name: 'More Smash burger' }) as HTMLButtonElement).disabled).toBe(true);
    expect(placeButton().textContent).toBe('Place order · $479.00');
    await user.click(page().getByRole('button', { name: 'Remove Smash burger' }));
    expect(lineTexts()).toEqual(['Fries']);
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 1 item');
    await waitFor(() => expect(stored()?.lines).toEqual([{ menuItemId: 'fries', name: 'Fries', unitPriceCents: 400, quantity: 1 }]));
  });

  it('Add more opens the restaurant, and an empty cart\'s Find food goes to the Food home', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart([{ menuItemId: 'fries', name: 'Fries', unitPriceCents: 400, quantity: 1 }]));
    serve();
    await openCart(user);
    await user.click(page().getByRole('button', { name: 'Add more' }));
    expect(await screen.findByRole('heading', { name: 'Corner Kitchen' })).toBeTruthy();
    expect(screen.getByLabelText('Fries in cart').textContent).toBe('1');

    await user.click(cartButton());
    await user.click(page().getByRole('button', { name: 'Remove Fries' }));
    await user.click(page().getByRole('button', { name: 'Find food' }));
    expect(await screen.findByRole('list', { name: 'Restaurants' })).toBeTruthy();
    expect(tabs().getByRole('button', { name: 'Food' }).getAttribute('aria-current')).toBe('page');
  });

  it('opens empty from the header when nothing is in the cart', async () => {
    const user = userEvent.setup();
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Cart, empty' }));
    expect(await screen.findByText('Your cart is empty')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /^Place order/ })).toBeNull();
  });

  it('shares the address chosen on the Food home', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart());
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Deliver to Home' }));
    await user.click(cartButton());
    expect(page().getByRole('button', { name: 'Change' })).toBeTruthy();
    expect(page().getByText('Home')).toBeTruthy();
  });
});

describe('the cart cannot hold more than an order may', () => {
  it('refuses a 31st line, and the stepper says so', async () => {
    const user = userEvent.setup();
    const thirty = Array.from({ length: 30 }, (_, i) => ({ menuItemId: `m${i}`, name: `Item ${i}`, unitPriceCents: 100, quantity: 1 }));
    localStorage.setItem(KEY, keptCart(thirty));
    serve();
    await openKitchen(user);
    expect((screen.getByRole('button', { name: 'More Fries' }) as HTMLButtonElement).disabled).toBe(true);
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 30 items');
  });
});

describe('Place order', () => {
  async function ready(user: User) {
    localStorage.setItem(KEY, keptCart());
    await openCart(user);
    await user.click(page().getByRole('button', { name: 'Deliver to Home' }));
  }

  it('sends the restaurant, how to pay, the saved address and the menu ids with counts, no prices', async () => {
    const user = userEvent.setup();
    const server = serve();
    await ready(user);
    await user.click(page().getByLabelText(/^Wallet/));
    await user.click(placeButton());
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    const call = server.callsTo('POST', '/orders')[0];
    const body = call.body as Record<string, unknown>;
    expect(Object.keys(body).sort()).toEqual(['deliveryAddressId', 'id', 'items', 'paymentType', 'restaurantId']);
    expect(body).toMatchObject({ restaurantId: 'r1', paymentType: 'wallet', deliveryAddressId: 'a-home' });
    expect(body.items).toEqual([{ menuItemId: 'burger', quantity: 2 }, { menuItemId: 'fries', quantity: 1 }]);
    expect(body.id).toMatch(/^SD-[A-Z2-9]{5}$/);
    expect(JSON.stringify(body)).not.toMatch(/price|name|customer/i);
    expect(call.headers['X-CSRF-Token']).toBe('csrf-u-cust');
  });

  it('pays by card unless another way is chosen, and cash is offered', async () => {
    const user = userEvent.setup();
    const server = serve();
    await ready(user);
    expect((page().getByLabelText(/^Card/) as HTMLInputElement).checked).toBe(true);
    await user.click(page().getByLabelText(/^Cash/));
    await user.click(placeButton());
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    expect((server.callsTo('POST', '/orders')[0].body as { paymentType: string }).paymentType).toBe('cash');
  });

  it('sends an address that is not saved as the address itself', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart());
    const server = serve();
    await openCart(user);
    await user.click(page().getByRole('button', { name: 'Type it in' }));
    const form = await screen.findByRole('form', { name: 'Confirm the address' });
    await user.type(within(form).getByLabelText('Street'), '1 Test Street');
    await user.type(within(form).getByLabelText('Latitude'), '12.9719');
    await user.type(within(form).getByLabelText('Longitude'), '77.6412');
    await user.click(screen.getByRole('button', { name: 'Deliver here' }));
    await user.click(placeButton());
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    const body = server.callsTo('POST', '/orders')[0].body as Record<string, unknown>;
    expect(body.deliveryAddress).toEqual({ line1: '1 Test Street', latitude: 12.9719, longitude: 77.6412 });
    expect('deliveryAddressId' in body).toBe(false);
  });

  it('asks where to deliver before sending anything', async () => {
    const user = userEvent.setup();
    localStorage.setItem(KEY, keptCart());
    const server = serve();
    await openCart(user);
    await user.click(placeButton());
    expect(page().getByRole('alert').textContent).toBe('Choose where to deliver.');
    expect(server.callsTo('POST', '/orders')).toHaveLength(0);
  });

  it('on success empties the cart (and the browser\'s copy) and opens the order on Orders', async () => {
    const user = userEvent.setup();
    const server = serve();
    await ready(user);
    await user.click(placeButton());
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    const id = (server.callsTo('POST', '/orders')[0].body as { id: string }).id;
    expect(await screen.findByRole('heading', { name: 'Your orders' })).toBeTruthy();
    expect(tabs().getByRole('button', { name: 'Orders' }).getAttribute('aria-current')).toBe('page');
    const panel = screen.getByLabelText('Order details', { selector: 'aside' });
    await waitFor(() => expect(panel.getAttribute('aria-hidden')).toBe('false'));
    expect(within(panel).getByText(id)).toBeTruthy();
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, empty');
    expect(localStorage.getItem(KEY)).toBeNull();
    expect(screen.getByText(`Order ${id} placed`)).toBeTruthy();
  });

  it.each([
    ['a closed restaurant', 'Corner Kitchen is closed'],
    ['a sold-out item', 'items[1]: Fries is not available right now'],
    ['an address too far', 'Corner Kitchen delivers up to 5 km and that address is 845.2 km away'],
    ['an item no longer on the menu', 'items[0]: there is no menu item "burger"'],
  ])('shows the server\'s words for %s and keeps the cart', async (_, words) => {
    const user = userEvent.setup();
    const server = serve({ 'POST /orders': () => ({ status: 422, body: { error: words } }) });
    await ready(user);
    await user.click(placeButton());
    expect((await page().findByRole('alert')).textContent).toBe(words);
    expect(server.callsTo('POST', '/orders')).toHaveLength(1);
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 3 items');
    expect(stored()?.lines).toHaveLength(2);
    expect((placeButton() as HTMLButtonElement).disabled).toBe(false);
  });

  it('names every field of an address the server would not take', async () => {
    const user = userEvent.setup();
    serve({ 'POST /orders': () => ({ status: 422, body: { error: 'deliveryAddress: line1 is required', fields: { line1: 'is required', latitude: 'must be between -90 and 90' } } }) });
    await ready(user);
    await user.click(placeButton());
    expect((await page().findByRole('alert')).textContent).toBe('Street is required. Latitude must be between -90 and 90');
  });

  it('says the server had a problem on a 500, and when it cannot be reached keeps the cart and sends the same number again', async () => {
    const user = userEvent.setup();
    let answer: 'down' | 'broken' | 'fine' = 'down';
    const server = serve();
    const route = server.fetchMock.getMockImplementation()!;
    const sent: string[] = [];
    server.fetchMock.mockImplementation(async (input, init) => {
      if (String(input) === '/orders' && init?.method === 'POST') sent.push((JSON.parse(String(init.body)) as { id: string }).id);
      if (String(input) === '/orders' && init?.method === 'POST' && answer !== 'fine') {
        if (answer === 'down') throw new TypeError('Failed to fetch');
        return new Response(JSON.stringify({ error: 'Failed to create order due to an internal server error' }), { status: 500 });
      }
      return route(input, init);
    });
    await ready(user);
    await user.click(placeButton());
    expect((await page().findByRole('alert')).textContent).toMatch(/^Can't reach the server\. Your cart is kept/);
    answer = 'broken';
    await user.click(placeButton());
    expect((await page().findByRole('alert')).textContent).toBe('Failed to create order due to an internal server error');
    answer = 'fine';
    await user.click(placeButton());
    expect(await screen.findByRole('heading', { name: 'Your orders' })).toBeTruthy();
    expect(sent).toHaveLength(3);
    expect(new Set(sent).size).toBe(1);
  });

  it('a 409 for an order that is ours means an earlier press got through: it is opened, not placed again', async () => {
    const user = userEvent.setup();
    const placed: Order[] = [];
    const server = serve({
      'POST /orders': (c) => {
        placed.push(order({ id: (c.body as { id: string }).id }));
        return { status: 409, body: { error: 'order already exists' } };
      },
      'GET /orders/*': (c) => ({ body: placed.find((o) => c.path.endsWith(o.id)) }),
    }, placed);
    await ready(user);
    await user.click(placeButton());
    expect(await screen.findByRole('heading', { name: 'Your orders' })).toBeTruthy();
    expect(server.callsTo('POST', '/orders')).toHaveLength(1);
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, empty');
  });

  it('a 409 for someone else\'s number asks to press again, and the next press sends a new number', async () => {
    const user = userEvent.setup();
    let first = true;
    const server = serve({
      'POST /orders': (c) => {
        if (first) { first = false; return { status: 409, body: { error: 'order SD-XXXXX already exists' } }; }
        return { status: 201, body: order({ id: (c.body as { id: string }).id }) };
      },
      'GET /orders/*': () => ({ status: 404, body: { error: 'Order could not be found' } }),
    });
    await ready(user);
    await user.click(placeButton());
    expect((await page().findByRole('alert')).textContent).toBe('That order number was taken. Press Place order again.');
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 3 items');
    await user.click(placeButton());
    expect(await screen.findByRole('heading', { name: 'Your orders' })).toBeTruthy();
    const ids = server.callsTo('POST', '/orders').map((c) => (c.body as { id: string }).id);
    expect(ids[0]).not.toBe(ids[1]);
  });

  it('a 409 it cannot check keeps the number, so the next press still cannot make a second order', async () => {
    const user = userEvent.setup();
    const server = serve({
      'POST /orders': () => ({ status: 409, body: { error: 'order already exists' } }),
      'GET /orders/*': () => ({ status: 500 }),
    });
    await ready(user);
    await user.click(placeButton());
    expect((await page().findByRole('alert')).textContent).toBe('The server had a problem. Try again in a moment.');
    await user.click(placeButton());
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(2));
    const ids = server.callsTo('POST', '/orders').map((c) => (c.body as { id: string }).id);
    expect(ids[0]).toBe(ids[1]);
  });

  it('a changed cart gets a new number', async () => {
    const user = userEvent.setup();
    const server = serve({ 'POST /orders': () => ({ status: 422, body: { error: 'items[1]: Fries is not available right now' } }) });
    await ready(user);
    await user.click(placeButton());
    await page().findByRole('alert');
    await user.click(page().getByRole('button', { name: 'Remove Fries' }));
    await user.click(placeButton());
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(2));
    const [a, b] = server.callsTo('POST', '/orders').map((c) => c.body as { id: string; items: unknown[] });
    expect(a.id).not.toBe(b.id);
    expect(b.items).toEqual([{ menuItemId: 'burger', quantity: 2 }]);
  });

  it('cannot be pressed twice while the first press is on its way', async () => {
    const user = userEvent.setup();
    const server = serve();
    const route = server.fetchMock.getMockImplementation()!;
    let release: () => void = () => {};
    let posts = 0;
    server.fetchMock.mockImplementation((input, init) => {
      if (String(input) === '/orders' && init?.method === 'POST') {
        posts++;
        return new Promise<Response>((resolve) => { release = () => void route(input, init).then(resolve); });
      }
      return route(input, init);
    });
    await ready(user);
    await user.click(placeButton());
    expect(placeButton().textContent).toBe('Placing your order');
    expect((placeButton() as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(placeButton());
    expect(posts).toBe(1);
    act(() => release());
    expect(await screen.findByRole('heading', { name: 'Your orders' })).toBeTruthy();
  });
});

describe('useCart', () => {
  const memory = ({ children }: { children: ReactNode }) => <CartProvider userId={null}>{children}</CartProvider>;
  const item = (over: Partial<CartItem> = {}): CartItem => ({ id: 'burger', restaurantId: 'r1', name: 'Smash burger', priceCents: 950, ...over });

  it('refuses another restaurant\'s item without the customer\'s say-so, and answers whether it changed anything', () => {
    const { result } = renderHook(() => useCart(), { wrapper: memory });
    let changed = false;
    act(() => { changed = result.current.setQuantity(item(), 2, { restaurantName: 'Corner Kitchen' }); });
    expect(changed).toBe(true);
    expect(result.current.wouldReplace('r1')).toBe(false);
    expect(result.current.wouldReplace('r2')).toBe(true);
    act(() => { changed = result.current.setQuantity(item({ id: 'paneer', restaurantId: 'r2' }), 1, { restaurantName: 'Bandra Tandoor' }); });
    expect(changed).toBe(false);
    expect(result.current.restaurantId).toBe('r1');
    expect(result.current.count).toBe(2);
    act(() => { changed = result.current.setQuantity(item({ id: 'paneer', restaurantId: 'r2' }), 1, { restaurantName: 'Bandra Tandoor', replace: true }); });
    expect(changed).toBe(true);
    expect(result.current.restaurantId).toBe('r2');
    expect(result.current.restaurantName).toBe('Bandra Tandoor');
    expect(result.current.lines.map((l) => l.menuItemId)).toEqual(['paneer']);
  });

  it('setting another restaurant\'s item to 0 changes nothing, not even with the customer\'s say-so', () => {
    const { result } = renderHook(() => useCart(), { wrapper: memory });
    act(() => { result.current.setQuantity(item(), 2, { restaurantName: 'Corner Kitchen' }); });
    let changed = true;
    act(() => { changed = result.current.setQuantity(item({ id: 'paneer', restaurantId: 'r2' }), 0); });
    expect(changed).toBe(false);
    act(() => { changed = result.current.setQuantity(item({ id: 'paneer', restaurantId: 'r2' }), 0, { replace: true }); });
    expect(changed).toBe(false);
    expect(result.current.restaurantId).toBe('r1');
    expect(result.current.quantityOf('burger')).toBe(2);
  });

  it('an empty cart takes any restaurant without asking', () => {
    const { result } = renderHook(() => useCart(), { wrapper: memory });
    expect(result.current.wouldReplace('r2')).toBe(false);
    act(() => { result.current.setQuantity(item({ restaurantId: 'r2' }), 1); });
    expect(result.current.restaurantId).toBe('r2');
    expect(result.current.restaurantName).toBe('');
  });

  it('removes a line, and removing the last empties the cart', () => {
    const { result } = renderHook(() => useCart(), { wrapper: memory });
    act(() => { result.current.setQuantity(item(), 2, { restaurantName: 'Corner Kitchen' }); });
    act(() => { result.current.setQuantity(item({ id: 'fries', name: 'Fries', priceCents: 400 }), 1); });
    act(() => result.current.remove('burger'));
    expect(result.current.lines.map((l) => l.menuItemId)).toEqual(['fries']);
    expect(result.current.restaurantName).toBe('Corner Kitchen');
    act(() => result.current.remove('nothing'));
    expect(result.current.count).toBe(1);
    act(() => result.current.remove('fries'));
    expect(result.current.restaurantId).toBeNull();
    expect(result.current.restaurantName).toBe('');
  });

  it('holds at most 30 lines, but a line it has can still change', () => {
    const { result } = renderHook(() => useCart(), { wrapper: memory });
    for (let i = 0; i < 30; i++) act(() => { result.current.setQuantity(item({ id: `m${i}` }), 1); });
    expect(result.current.isFull('m30')).toBe(true);
    expect(result.current.isFull('m0')).toBe(false);
    let changed = true;
    act(() => { changed = result.current.setQuantity(item({ id: 'm30' }), 1); });
    expect(changed).toBe(false);
    expect(result.current.lines).toHaveLength(30);
    act(() => { changed = result.current.setQuantity(item({ id: 'm0' }), 5); });
    expect(changed).toBe(true);
    expect(result.current.quantityOf('m0')).toBe(5);
    act(() => { result.current.setQuantity(item({ id: 'm0' }), 0); });
    expect(result.current.isFull('m30')).toBe(false);
  });

  it('in memory only, it says it is not kept and touches no storage', () => {
    const set = vi.spyOn(Storage.prototype, 'setItem');
    const { result } = renderHook(() => useCart(), { wrapper: memory });
    act(() => { result.current.setQuantity(item(), 1); });
    expect(result.current.kept).toBe(false);
    expect(set).not.toHaveBeenCalled();
  });
});
