import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from './App';
import { setSession } from './api';
import type { Candidate, MenuItem, Order, Restaurant, SavedAddress, User } from './lib/types';
import { mockServer, type Call } from './test/server';

// A customer's whole first visit, through the real app from the sign-in screen: sign up, say where the
// food goes, find a restaurant that delivers there, fill a cart, place the order, pay, and follow it as
// the kitchen works. The server is a small stateful fake that behaves like the Go server where the
// journey depends on it (it prices from the menu, keeps the saved address, moves the order on). The Go
// end-to-end test in e2e/ walks the same journey over HTTP against the real server and database; this
// one proves the screens join up, which CI can do without a browser.

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); });

const kitchen: Restaurant = { id: 'r1', name: 'Corner Kitchen', active: true, cuisine: 'Burgers', latitude: 12.9725, longitude: 77.6412, deliveryRadiusKm: 5 };
const grill: Restaurant = { id: 'r2', name: 'Harbour Grill', active: true, cuisine: 'Seafood', latitude: 19.0596, longitude: 72.8295, deliveryRadiusKm: 5 };
const menus: Record<string, MenuItem[]> = {
  r1: [
    { id: 'burger', restaurantId: 'r1', name: 'Smash burger', description: 'Two patties', priceCents: 950, available: true },
    { id: 'fries', restaurantId: 'r1', name: 'Fries', priceCents: 400, available: true, veg: true },
  ],
  r2: [{ id: 'prawns', restaurantId: 'r2', name: 'Garlic prawns', priceCents: 1800, available: true }],
};
const found: Candidate = { label: '12 100 Feet Road, Indiranagar, Bengaluru', line1: '12 100 Feet Road', city: 'Bengaluru', postalCode: '560038', latitude: 12.9719, longitude: 77.6412 };

/** The fake server's state, so the test can move the order on as the kitchen would. */
function fakeServer() {
  let me: User | null = null;
  const addresses: SavedAddress[] = [];
  const orders: Order[] = [];
  const restaurants = { r1: kitchen, r2: grill } as Record<string, Restaurant>;

  const server = mockServer({
    'GET /auth/me': () => (me ? { body: { user: me, csrfToken: 'csrf-new' } } : { status: 401, body: { error: 'not signed in' } }),
    'POST /auth/signup': (c) => {
      const { email, name } = c.body as { email: string; name: string };
      me = { id: 'u-new', email, name, role: 'customer' }; // the server makes every sign-up a customer
      return { status: 201, body: { user: me, csrfToken: 'csrf-new' } };
    },
    'GET /orders': () => ({ body: { orders: me ? orders.filter((o) => o.customerId === me!.id) : [] } }),
    'GET /addresses': () => ({ body: { addresses } }),
    'POST /addresses': (c) => {
      const a = { ...(c.body as Omit<SavedAddress, 'id' | 'userId'>), id: `a${addresses.length + 1}`, userId: me!.id };
      addresses.push(a);
      return { status: 201, body: { address: a } };
    },
    'GET /geocode': () => ({ body: { candidates: [found] } }),
    'GET /search': () => ({ body: { results: [], located: false } }),
    'GET /restaurants': () => ({ body: { restaurants: [kitchen, grill] } }),
    'GET /restaurants/*': (c) => {
      const id = c.path.split('/')[2];
      return { body: { restaurant: restaurants[id], items: menus[id] } };
    },
    'POST /orders': (c) => {
      const body = c.body as { id: string; restaurantId: string; paymentType: Order['paymentType']; deliveryAddressId?: string; items: { menuItemId: string; quantity: number }[] };
      const saved = addresses.find((a) => a.id === body.deliveryAddressId);
      if (!saved) return { status: 422, body: { error: 'deliveryAddressId: not one of your saved addresses' } };
      // priced from the menu, as the server does: the request names items and counts only
      const items = body.items.map((l) => {
        const m = menus[body.restaurantId].find((x) => x.id === l.menuItemId)!;
        return { sku: m.id.toUpperCase(), name: m.name, quantity: l.quantity, unitPriceCents: m.priceCents };
      });
      const o: Order = {
        id: body.id, customerId: me!.id, restaurantId: body.restaurantId, paymentType: body.paymentType, state: 'created',
        createdAt: new Date().toISOString(), items, deliveryAddress: { ...saved },
      };
      orders.push(o);
      return { status: 201, body: { order: o } };
    },
    'POST /orders/*': (c) => {
      const [, , id, action] = c.path.split('/');
      const o = orders.find((x) => x.id === id);
      if (!o || action !== 'pay') return { status: 404, body: { error: 'order not found' } };
      const total = o.items.reduce((s, l) => s + l.unitPriceCents * l.quantity, 0);
      Object.assign(o, { state: 'paid', payment: { status: 'authorized', attempt: 1, maxAttempts: 3, amountCents: total } });
      return { body: { order: o } };
    },
  });

  return {
    ...server,
    /** the kitchen and the road, as staff would move the order on */
    advance(state: Order['state']) { orders[0].state = state; },
  };
}

type Session = ReturnType<typeof userEvent.setup>;
const tabs = () => within(screen.getByRole('navigation', { name: 'Sections' }));
const lastCall = (calls: Call[]) => calls[calls.length - 1];

async function signUp(user: Session) {
  await user.click(await screen.findByRole('tab', { name: 'Create account' }));
  await user.type(screen.getByLabelText('Name'), 'Robin Diner');
  await user.type(screen.getByLabelText('Email'), 'robin@example.test');
  await user.type(screen.getByLabelText('Password'), 'a long enough phrase');
  await user.click(screen.getByRole('button', { name: 'Create account' }));
}

describe('a customer\'s first order, start to finish', () => {
  it('signs up, picks an address, finds food, fills a cart, orders, pays and reads the tracker', async () => {
    const user = userEvent.setup();
    const server = fakeServer();
    render(<App />);

    // 1. Sign up: the new account is a customer and lands on Food, with nothing of the dispatch dashboard
    await signUp(user);
    expect(await screen.findByRole('heading', { name: /, Robin$/ })).toBeTruthy();
    expect(lastCall(server.callsTo('POST', '/auth/signup')).body).toEqual({ email: 'robin@example.test', name: 'Robin Diner', password: 'a long enough phrase' });
    expect(tabs().getAllByRole('button').map((b) => b.textContent)).toEqual(['Food', 'Orders', 'Account']);
    expect(screen.queryByRole('button', { name: /new order|start an order/i })).toBeNull();
    expect(server.calls.some((c) => c.path.startsWith('/alerts') || c.path.startsWith('/users'))).toBe(false);

    // 2. Where it goes: search an address, keep it as Home
    const deliver = within(screen.getByRole('region', { name: 'Deliver to' }));
    await user.type(deliver.getByLabelText('Search an address'), 'Indiranagar');
    await user.click(deliver.getByRole('button', { name: 'Search' }));
    await user.click(await deliver.findByRole('button', { name: /12 100 Feet Road/ }));
    await user.type(deliver.getByLabelText('Landmark'), 'blue gate');
    await user.click(deliver.getByRole('checkbox', { name: /Save this address/ }));
    await user.type(deliver.getByLabelText('Name'), 'Home');
    await user.click(deliver.getByRole('button', { name: 'Deliver here' }));
    await waitFor(() => expect(server.callsTo('POST', '/addresses')).toHaveLength(1));
    expect(server.callsTo('POST', '/addresses')[0].body).toMatchObject({ label: 'Home', line1: '12 100 Feet Road', landmark: 'blue gate', latitude: 12.9719, longitude: 77.6412 });

    // 3. Find food: the cards say how far each restaurant is and whether it delivers to Home
    const cards = within(await screen.findByRole('list', { name: 'Restaurants' }));
    const near = await cards.findByRole('button', { name: /^Corner Kitchen, Burgers, .*m away/ });
    expect(cards.getByRole('button', { name: /^Harbour Grill/ }).getAttribute('aria-label')).toMatch(/too far/i);
    await user.click(near);

    // 4. Fill a cart on the restaurant page
    const menu = within(await screen.findByRole('list', { name: 'Menu' }));
    await user.click(menu.getByRole('button', { name: 'More Smash burger' }));
    await user.click(menu.getByRole('button', { name: 'More Smash burger' }));
    await user.click(menu.getByRole('button', { name: 'More Fries' }));
    expect(screen.getByRole('button', { name: /^Cart, 3 items$/ })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'View cart · 3 items · $23.00' }));

    // 5. The cart page: the lines, Home already chosen, cash, Place order
    const cart = within(await screen.findByRole('region', { name: 'Your cart' }));
    expect(cart.getByText('Corner Kitchen')).toBeTruthy();
    expect(within(cart.getByRole('list', { name: 'In your cart' })).getAllByRole('listitem')).toHaveLength(2);
    expect(within(cart.getByRole('region', { name: 'Deliver to' })).getByText(/12 100 Feet Road/)).toBeTruthy();
    await user.click(cart.getByRole('radio', { name: /Card/ }));
    await user.click(cart.getByRole('button', { name: 'Place order · $23.00' }));

    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    const sent = server.callsTo('POST', '/orders')[0].body as { id: string };
    expect(sent).toEqual({
      id: sent.id, restaurantId: 'r1', paymentType: 'card', deliveryAddressId: 'a1',
      items: [{ menuItemId: 'burger', quantity: 2 }, { menuItemId: 'fries', quantity: 1 }],
    }); // never a price, a name or someone else's id

    // 6. The tracker opens on the new order, and the cart is empty
    const page = within(await screen.findByRole('region', { name: 'Order details' }));
    expect(page.getByRole('heading', { name: sent.id })).toBeTruthy();
    expect(page.getByRole('status').textContent).toBe('Waiting for your payment');
    expect(page.getByText('$23.00')).toBeTruthy();
    expect(page.getByText('Landmark: blue gate')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Cart, empty' })).toBeTruthy();
    expect(tabs().getByRole('button', { name: 'Orders' }).getAttribute('aria-current')).toBe('page');

    // 7. Pay (the form opens on the first tap, the second pays)
    await user.click(page.getByRole('button', { name: 'Pay now' }));
    await user.click(page.getByRole('button', { name: 'Pay now' }));
    await waitFor(() => expect(page.getByRole('status').textContent).toBe('Paid. Waiting for the restaurant to accept it'));
    expect(page.getByText('Paid $23.00 with your card.')).toBeTruthy();

    // 8. The kitchen accepts it; the open page follows by itself on the next poll
    server.advance('preparing');
    await waitFor(() => expect(page.getByRole('status').textContent).toBe('Being prepared in the kitchen'));
    const current = page.getAllByRole('listitem').find((li) => li.getAttribute('aria-current') === 'step');
    expect(current?.textContent).toMatch(/Kitchen|Preparing|prepared/i);

    // 9. Back to the list: the order reads as a status line with its total
    await user.click(page.getByRole('button', { name: /Your orders/ }));
    const row = await screen.findByRole('button', { name: `Order ${sent.id}, Being prepared in the kitchen` });
    expect(within(row).getByText('$23.00')).toBeTruthy();
  }, 30_000);
});
