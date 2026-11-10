import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from '../App';
import { setSession } from '../api';
import type { MenuItem, Restaurant, User } from '../lib/types';
import { customer, mockServer, order, session, staff, type Handler } from '../test/server';

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); });

const kitchen: Restaurant = { id: 'r1', name: 'Corner Kitchen', active: true };
const noodles: Restaurant = { id: 'r2', name: 'Noodle Bar', active: true };
const burger: MenuItem = { id: 'burger', restaurantId: 'r1', name: 'Smash burger', description: 'Two patties', priceCents: 950, available: true };
const fries: MenuItem = { id: 'fries', restaurantId: 'r1', name: 'Fries', priceCents: 400, available: true };
const padThai: MenuItem = { id: 'thai', restaurantId: 'r2', name: 'Pad thai', priceCents: 1390, available: true };
const ada: User = { id: 'u-ada', email: 'ada@example.com', name: 'Ada Lovelace', role: 'customer' };

function shop(user: User, extra: Record<string, Handler> = {}) {
  return mockServer({
    'GET /auth/me': () => ({ body: session(user) }),
    'GET /orders': () => ({ body: { orders: [] } }),
    'GET /alerts': () => ({ body: { alerts: [] } }),
    'GET /users': () => ({ body: { accounts: [ada] } }),
    'GET /restaurants': () => ({ body: { restaurants: [kitchen, noodles] } }),
    'GET /restaurants/r1/menu': () => ({ body: { restaurant: kitchen, items: [burger, fries] } }),
    'GET /restaurants/r2/menu': () => ({ body: { restaurant: noodles, items: [padThai] } }),
    'POST /orders': (c) => ({ status: 201, body: { order: order({ id: (c.body as { id: string }).id }) } }),
    'GET /orders/*': () => ({ body: { order: order() } }),
    ...extra,
  });
}

async function openSheet(user: ReturnType<typeof userEvent.setup>) {
  render(<App />);
  const buttons = await screen.findAllByRole('button', { name: /new order/i });
  await user.click(buttons[0]);
  await screen.findByText('Smash burger');
}

const more = (user: ReturnType<typeof userEvent.setup>, name: string, times = 1) =>
  (async () => { for (let i = 0; i < times; i++) await user.click(screen.getByRole('button', { name: `More ${name}` })); })();

describe('ordering from the menu', () => {
  it('shows the first open restaurant\'s menu and a total that follows the baskets', async () => {
    const user = userEvent.setup();
    shop(customer);
    await openSheet(user);
    expect(screen.getByText('Two patties')).toBeTruthy();
    expect(screen.getByText('$9.50')).toBeTruthy();
    const total = () => within(document.querySelector('.order-total') as HTMLElement).getByText(/\$/).textContent;
    expect(total()).toBe('$0.00');
    await more(user, 'Smash burger', 2);
    await more(user, 'Fries');
    expect(total()).toBe('$23.00');
    await user.click(screen.getByRole('button', { name: 'Fewer Fries' }));
    expect(total()).toBe('$19.00');
    expect((screen.getByRole('button', { name: 'Fewer Fries' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('sends the menu item ids and counts and nothing about prices', async () => {
    const user = userEvent.setup();
    const server = shop(customer);
    await openSheet(user);
    await more(user, 'Smash burger', 2);
    await more(user, 'Fries');
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    const sent = server.callsTo('POST', '/orders')[0].body as { id: string; customerId: string; restaurantId: string; items: unknown[] };
    expect(sent.items).toEqual([{ menuItemId: 'burger', quantity: 2 }, { menuItemId: 'fries', quantity: 1 }]);
    expect(sent.restaurantId).toBe('r1');
    expect(sent.customerId).toBe('u-cust');
    expect(JSON.stringify(sent)).not.toMatch(/unitPriceCents|sku/);
    expect(await screen.findByText(`Order ${sent.id} placed`)).toBeTruthy();
  });

  it('asks for something before an empty order goes anywhere', async () => {
    const user = userEvent.setup();
    const server = shop(customer);
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    expect(await screen.findByText('Add at least one item')).toBeTruthy();
    expect(server.callsTo('POST', '/orders')).toHaveLength(0);
  });

  it('a different restaurant means a different menu and a fresh basket', async () => {
    const user = userEvent.setup();
    const server = shop(customer);
    await openSheet(user);
    await more(user, 'Smash burger');
    await user.selectOptions(screen.getByLabelText('Restaurant'), 'r2');
    expect(await screen.findByText('Pad thai')).toBeTruthy();
    expect(screen.queryByText('Smash burger')).toBeNull();
    await more(user, 'Pad thai');
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    const sent = server.callsTo('POST', '/orders')[0].body as { restaurantId: string; items: unknown[] };
    expect(sent.restaurantId).toBe('r2');
    expect(sent.items).toEqual([{ menuItemId: 'thai', quantity: 1 }]);
  });

  it('says so when no restaurant is open, and does not offer to place an order', async () => {
    const user = userEvent.setup();
    shop(customer, { 'GET /restaurants': () => ({ body: { restaurants: [] } }) });
    render(<App />);
    await user.click((await screen.findAllByRole('button', { name: /new order/i }))[0]);
    expect(await screen.findByText(/No restaurant is open right now/)).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Place order' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('says so when a menu is empty', async () => {
    const user = userEvent.setup();
    shop(customer, { 'GET /restaurants/r1/menu': () => ({ body: { restaurant: kitchen, items: [] } }) });
    render(<App />);
    await user.click((await screen.findAllByRole('button', { name: /new order/i }))[0]);
    expect(await screen.findByText(/Nothing is available from this restaurant/)).toBeTruthy();
  });

  it('shows what went wrong loading, in the server\'s words', async () => {
    const user = userEvent.setup();
    shop(customer, { 'GET /restaurants': () => ({ status: 500, body: { error: 'Failed to list restaurants due to an internal server error' } }) });
    render(<App />);
    await user.click((await screen.findAllByRole('button', { name: /new order/i }))[0]);
    expect((await screen.findAllByRole('alert')).some((a) => /internal server error/.test(a.textContent ?? ''))).toBe(true);
  });

  it('shows the server\'s words when the order is refused, such as an item that sold out meanwhile', async () => {
    const user = userEvent.setup();
    shop(customer, { 'POST /orders': () => ({ status: 422, body: { error: 'items[0]: Fries is not available right now' } }) });
    await openSheet(user);
    await more(user, 'Fries');
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    expect(await screen.findByText('items[0]: Fries is not available right now')).toBeTruthy();
  });

  it('shows field complaints from the server next to the form', async () => {
    const user = userEvent.setup();
    shop(customer, { 'POST /orders': () => ({ status: 422, body: { error: 'bad', fields: { restaurantId: 'is not a restaurant' } } }) });
    await openSheet(user);
    await more(user, 'Fries');
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    expect(await screen.findByText('Restaurant is not a restaurant')).toBeTruthy();
  });

  it('gives a customer a new order number when theirs was taken', async () => {
    const user = userEvent.setup();
    const server = shop(customer, { 'POST /orders': () => ({ status: 409, body: { error: 'order already exists' } }) });
    await openSheet(user);
    await more(user, 'Fries');
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    expect(await screen.findByText(/order number was taken/)).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(2));
    const [first, second] = server.callsTo('POST', '/orders').map((c) => (c.body as { id: string }).id);
    expect(first).not.toBe(second);
  });
});

describe('ordering as staff', () => {
  it('chooses the customer from the accounts, or a walk-in', async () => {
    const user = userEvent.setup();
    const server = shop(staff);
    await openSheet(user);
    expect(server.calls.some((c) => c.path.startsWith('/users?') && c.path.includes('role=customer'))).toBe(true);
    const select = screen.getByLabelText('Customer') as HTMLSelectElement;
    expect(select.value).toBe('walk-in');
    await user.selectOptions(select, 'u-ada');
    await more(user, 'Fries');
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    expect((server.callsTo('POST', '/orders')[0].body as { customerId: string }).customerId).toBe('u-ada');
  });

  it('may add a custom line with a price of their own, beside menu lines', async () => {
    const user = userEvent.setup();
    const server = shop(staff);
    await openSheet(user);
    await more(user, 'Smash burger');
    await user.click(screen.getByRole('button', { name: 'Add a custom line' }));
    await user.type(screen.getByLabelText('Item name'), 'Birthday cake');
    await user.type(screen.getByLabelText('Price in dollars'), '42');
    const total = () => within(document.querySelector('.order-total') as HTMLElement).getByText(/\$/).textContent;
    expect(total()).toBe('$51.50');
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    expect((server.callsTo('POST', '/orders')[0].body as { items: unknown[] }).items).toEqual([
      { menuItemId: 'burger', quantity: 1 },
      { sku: 'BIRTHDAY', name: 'Birthday cake', quantity: 1, unitPriceCents: 4200 },
    ]);
  });

  it('shows a custom line\'s mistake next to it, and can take the line away again', async () => {
    const user = userEvent.setup();
    const server = shop(staff);
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Add a custom line' }));
    await user.type(screen.getByLabelText('Item name'), 'Cake');
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    expect(await screen.findByText('Enter a price of 0 or more')).toBeTruthy();
    expect(server.callsTo('POST', '/orders')).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: 'Remove item' }));
    expect(screen.queryByLabelText('Item name')).toBeNull();
  });

  it('keeps an order id clash on the id field, where staff can change it', async () => {
    const user = userEvent.setup();
    shop(staff, { 'POST /orders': () => ({ status: 409, body: { error: 'order SD-1 already exists' } }) });
    await openSheet(user);
    await more(user, 'Fries');
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    expect(await screen.findByText('order SD-1 already exists')).toBeTruthy();
  });

  it('points at the Menu when no restaurant is open', async () => {
    const user = userEvent.setup();
    shop(staff, { 'GET /restaurants': () => ({ body: { restaurants: [] } }) });
    render(<App />);
    await user.click((await screen.findAllByRole('button', { name: /new order/i }))[0]);
    expect(await screen.findByText(/Open one in Menu/)).toBeTruthy();
  });
});

describe('the sample orders', () => {
  const emptyBoard = { 'GET /orders': () => ({ body: { orders: [] } }) };

  it('are placed from the menu, three of them, by menu item id', async () => {
    const user = userEvent.setup();
    const server = shop(staff, emptyBoard);
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /sample/i }));
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(3));
    for (const c of server.callsTo('POST', '/orders')) {
      const body = c.body as { restaurantId: string; items: { menuItemId: string; quantity: number }[] };
      expect(body.restaurantId).toBe('r1');
      expect(body.items.length).toBeGreaterThan(0);
      for (const line of body.items) expect(['burger', 'fries']).toContain(line.menuItemId);
    }
  });

  it('say there is no menu rather than place nothing silently', async () => {
    const user = userEvent.setup();
    const server = shop(staff, { ...emptyBoard, 'GET /restaurants': () => ({ body: { restaurants: [] } }) });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /sample/i }));
    expect(await screen.findByText(/no menu yet/i)).toBeTruthy();
    expect(server.callsTo('POST', '/orders')).toHaveLength(0);
  });

  it('say so to a customer in words that fit them', async () => {
    const user = userEvent.setup();
    shop(customer, { ...emptyBoard, 'GET /restaurants': () => ({ body: { restaurants: [] } }) });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /sample/i }));
    expect(await screen.findByText(/No restaurant has a menu yet/)).toBeTruthy();
  });
});
