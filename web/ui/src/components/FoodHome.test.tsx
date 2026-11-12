import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, renderHook, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { App } from '../App';
import { setSession } from '../api';
import { CartProvider, useCart, type CartItem } from '../hooks/cart';
import type { MenuItem, Order, Restaurant } from '../lib/types';
import { home } from '../test/deliver';
import { customer, mockServer, order, session, type Handler } from '../test/server';

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); });

// Home is at 12.9719, 77.6412 (Indiranagar).
const kitchen: Restaurant = { id: 'r1', name: 'Corner Kitchen', active: true, cuisine: 'Burgers', latitude: 12.9725, longitude: 77.6412, deliveryRadiusKm: 5 };
const tandoor: Restaurant = { id: 'r2', name: 'Bandra Tandoor', active: true, cuisine: 'North Indian', latitude: 19.0596, longitude: 72.8295, deliveryRadiusKm: 5 };
const popup: Restaurant = { id: 'r3', name: 'Pop-up Stall', active: true, deliveryRadiusKm: 5 };
const burger: MenuItem = { id: 'burger', restaurantId: 'r1', name: 'Smash burger', description: 'Two patties, cheese', priceCents: 950, available: true };
const fries: MenuItem = { id: 'fries', restaurantId: 'r1', name: 'Fries', priceCents: 400, available: true, veg: true };
const salad: MenuItem = { id: 'salad', restaurantId: 'r1', name: 'Garden salad', description: 'Leaves and a lemon dressing', priceCents: 650, available: true, veg: true };
const paneer: MenuItem = { id: 'paneer', restaurantId: 'r2', name: 'Paneer tikka', priceCents: 900, available: true, veg: true };

function serve(extra: Record<string, Handler> = {}, placed: Order[] = []) {
  return mockServer({
    'GET /auth/me': () => ({ body: session(customer) }),
    'GET /orders': () => ({ body: { orders: placed } }),
    'GET /addresses': () => ({ body: { addresses: [home] } }),
    'GET /restaurants': () => ({ body: { restaurants: [tandoor, kitchen, popup] } }),
    'GET /restaurants/r1/menu': () => ({ body: { restaurant: kitchen, items: [burger, fries, salad] } }),
    'GET /restaurants/r2/menu': () => ({ body: { restaurant: tandoor, items: [paneer] } }),
    'GET /search': () => ({ body: { results: [], located: false } }),
    'POST /orders': (c) => {
      const o = order({ id: (c.body as { id: string }).id, state: 'created' });
      placed.push(o);
      return { status: 201, body: { order: o } };
    },
    ...extra,
  });
}

/** Requests for exactly this path never get an answer, so the screen stays waiting. */
function hang(server: ReturnType<typeof serve>, path: string) {
  const answer = server.fetchMock.getMockImplementation()!;
  server.fetchMock.mockImplementation((input, init) => (String(input) === path ? new Promise<Response>(() => {}) : answer(input, init)));
}

const cards = () => within(screen.getByRole('list', { name: 'Restaurants' })).getAllByRole('button');
const cartButton = () => screen.getByRole('button', { name: /^Cart,/ });
type User = ReturnType<typeof userEvent.setup>;

async function openKitchen(user: User) {
  render(<App />);
  await user.click(await screen.findByRole('button', { name: /^Corner Kitchen/ }));
  await screen.findByRole('heading', { name: 'Corner Kitchen' });
  await screen.findByRole('list', { name: 'Menu' });
}

describe('the Food home', () => {
  it('shows a card for every open restaurant, with its cuisine, and Open while no address is chosen', async () => {
    serve();
    render(<App />);
    await screen.findByRole('list', { name: 'Restaurants' });
    expect(cards().map((c) => c.getAttribute('aria-label'))).toEqual([
      'Bandra Tandoor, North Indian, Open', 'Corner Kitchen, Burgers, Open', 'Pop-up Stall, Open',
    ]);
    // the address and the food search are there, above the cards
    expect(screen.getByRole('region', { name: 'Deliver to' })).toBeTruthy();
    expect(screen.getByRole('region', { name: 'Find food' })).toBeTruthy();
    expect(screen.queryByText(/away/)).toBeNull();
  });

  it('says how far each is once an address is chosen, and puts those that deliver first', async () => {
    const user = userEvent.setup();
    const server = serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Deliver to Home' }));
    await waitFor(() => expect(cards()[0].getAttribute('aria-label')).toBe('Corner Kitchen, Burgers, 50 m away, Delivers to Home'));
    expect(cards().map((c) => c.getAttribute('aria-label'))).toEqual([
      'Corner Kitchen, Burgers, 50 m away, Delivers to Home',
      'Bandra Tandoor, North Indian, 850 km away, Too far to deliver to Home',
      'Pop-up Stall, Not taking delivery orders yet',
    ]);
    // the food search follows the address
    await waitFor(() => expect(server.calls.some((c) => c.path.startsWith('/search?lat=12.972&lng=77.641'))).toBe(true));
  });

  it('says so while loading, when no restaurant is open, and when the list cannot be read', async () => {
    let answer: () => { status?: number; body?: unknown } = () => ({ body: { restaurants: null } });
    serve({ 'GET /restaurants': () => answer() });
    const { unmount } = render(<App />);
    expect(await screen.findByText('No restaurant is open right now. Try again later.')).toBeTruthy();
    unmount();

    answer = () => ({ status: 500, body: { error: 'Failed to list restaurants due to an internal server error' } });
    render(<App />);
    expect((await screen.findAllByRole('alert')).some((a) => /internal server error/.test(a.textContent ?? ''))).toBe(true);
    expect(screen.queryByText('No restaurant is open right now. Try again later.')).toBeNull();
  });

  it('shows that it is loading the restaurants', async () => {
    hang(serve(), '/restaurants');
    render(<App />);
    expect(await screen.findByRole('img', { name: 'Loading restaurants' })).toBeTruthy();
  });
});

describe('a restaurant page', () => {
  it('shows the whole menu with prices, descriptions and a Veg pill', async () => {
    const user = userEvent.setup();
    serve();
    await openKitchen(user);
    const menu = within(screen.getByRole('list', { name: 'Menu' }));
    const rows = menu.getAllByRole('listitem');
    expect(rows).toHaveLength(3);
    expect(rows[0].textContent).toContain('Smash burger');
    expect(rows[0].textContent).toContain('Two patties, cheese');
    expect(rows[0].textContent).toContain('$9.50');
    expect(within(rows[0]).queryByText('Veg')).toBeNull();
    expect(within(rows[1]).getByText('Veg')).toBeTruthy();
    expect(screen.getByText('Burgers')).toBeTruthy();
  });

  it('says how far it is and whether it delivers to the chosen address', async () => {
    const user = userEvent.setup();
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Deliver to Home' }));
    await user.click(await screen.findByRole('button', { name: /^Bandra Tandoor/ }));
    await screen.findByRole('heading', { name: 'Bandra Tandoor' });
    expect(screen.getByText(/850 km away/)).toBeTruthy();
    expect(screen.getByText('Too far to deliver to Home')).toBeTruthy();
  });

  it('filters the menu by what is typed and by vegetarian, on the page, asking the server nothing', async () => {
    const user = userEvent.setup();
    const server = serve();
    await openKitchen(user);
    const notPolling = () => server.calls.filter((c) => !c.path.startsWith('/orders')).length;
    const before = notPolling();
    const names = () => within(screen.getByRole('list', { name: 'Menu' })).getAllByRole('listitem').map((li) => li.querySelector('.row-title')?.textContent);

    await user.type(screen.getByLabelText('Search this menu'), 'LEMON');
    expect(names()).toEqual(['Garden salad Veg']);
    await user.clear(screen.getByLabelText('Search this menu'));
    await user.click(screen.getByLabelText('Vegetarian only'));
    expect(names()).toEqual(['Fries Veg', 'Garden salad Veg']);
    await user.type(screen.getByLabelText('Search this menu'), 'burger');
    expect(screen.getByText('Nothing on this menu matches that.')).toBeTruthy();
    await user.clear(screen.getByLabelText('Search this menu'));
    await user.click(screen.getByLabelText('Vegetarian only'));
    expect(names()).toHaveLength(3);
    expect(notPolling()).toBe(before);
  });

  it('says when nothing on the menu is vegetarian, and when nothing is available at all', async () => {
    const user = userEvent.setup();
    serve({ 'GET /restaurants/r1/menu': () => ({ body: { restaurant: kitchen, items: [burger] } }) });
    await openKitchen(user);
    await user.click(screen.getByLabelText('Vegetarian only'));
    expect(screen.getByText('Nothing vegetarian on this menu right now.')).toBeTruthy();
  });

  it('says when a restaurant has nothing available', async () => {
    const user = userEvent.setup();
    serve({ 'GET /restaurants/r1/menu': () => ({ body: { restaurant: kitchen, items: null } }) });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /^Corner Kitchen/ }));
    expect(await screen.findByText('Nothing is available from this restaurant right now.')).toBeTruthy();
  });

  it('says a restaurant that closed after the list loaded is not taking orders, and goes back', async () => {
    const user = userEvent.setup();
    serve({ 'GET /restaurants/r1/menu': () => ({ status: 404, body: { error: 'there is no such restaurant' } }) });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /^Corner Kitchen/ }));
    expect(await screen.findByRole('heading', { name: 'This restaurant is not taking orders' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /More/ })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Back to restaurants' }));
    expect(await screen.findByRole('list', { name: 'Restaurants' })).toBeTruthy();
  });

  it('shows the server\'s words when the menu cannot be read', async () => {
    const user = userEvent.setup();
    serve({ 'GET /restaurants/r1/menu': () => ({ status: 500, body: { error: 'Failed to read the menu due to an internal server error' } }) });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /^Corner Kitchen/ }));
    expect((await screen.findAllByRole('alert')).some((a) => /read the menu/.test(a.textContent ?? ''))).toBe(true);
    expect(screen.queryByText('Nothing is available from this restaurant right now.')).toBeNull();
  });

  it('shows that it is loading the menu', async () => {
    const user = userEvent.setup();
    hang(serve(), '/restaurants/r1/menu');
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /^Corner Kitchen/ }));
    expect(await screen.findByRole('img', { name: 'Loading the menu' })).toBeTruthy();
  });

  it('goes back with the back button, and with the Food tab', async () => {
    const user = userEvent.setup();
    serve();
    await openKitchen(user);
    await user.click(screen.getByRole('button', { name: 'Restaurants' }));
    expect(await screen.findByRole('list', { name: 'Restaurants' })).toBeTruthy();

    await user.click(screen.getByRole('button', { name: /^Corner Kitchen/ }));
    await screen.findByRole('heading', { name: 'Corner Kitchen' });
    // another tab and back keeps the page; the Food tab while on Food goes home
    const tabs = within(screen.getByRole('navigation', { name: 'Sections' }));
    await user.click(tabs.getByRole('button', { name: 'Orders' }));
    await user.click(tabs.getByRole('button', { name: 'Food' }));
    expect(screen.getByRole('heading', { name: 'Corner Kitchen' })).toBeTruthy();
    await user.click(tabs.getByRole('button', { name: 'Food' }));
    expect(await screen.findByRole('list', { name: 'Restaurants' })).toBeTruthy();
  });
});

describe('the steppers', () => {
  it('put items in the cart, count them on the cart button, and stop at 0 and 50', async () => {
    const user = userEvent.setup();
    serve();
    await openKitchen(user);
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, empty');
    expect((screen.getByRole('button', { name: 'Fewer Smash burger' }) as HTMLButtonElement).disabled).toBe(true);

    await user.click(screen.getByRole('button', { name: 'More Smash burger' }));
    await user.click(screen.getByRole('button', { name: 'More Smash burger' }));
    await user.click(screen.getByRole('button', { name: 'More Fries' }));
    expect(screen.getByLabelText('Smash burger in cart').textContent).toBe('2');
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 3 items');
    expect(screen.getByRole('button', { name: /View cart · 3 items · \$23\.00/ })).toBeTruthy();

    await user.click(screen.getByRole('button', { name: 'Fewer Smash burger' }));
    await user.click(screen.getByRole('button', { name: 'Fewer Fries' }));
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 1 item');
    expect(screen.getByRole('button', { name: /View cart · 1 item · \$9\.50/ })).toBeTruthy();

    for (let i = 0; i < 49; i++) await user.click(screen.getByRole('button', { name: 'More Smash burger' }));
    expect(screen.getByLabelText('Smash burger in cart').textContent).toBe('50');
    expect((screen.getByRole('button', { name: 'More Smash burger' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('keep their counts through the filters', async () => {
    const user = userEvent.setup();
    serve();
    await openKitchen(user);
    await user.click(screen.getByRole('button', { name: 'More Fries' }));
    await user.click(screen.getByLabelText('Vegetarian only'));
    expect(screen.getByLabelText('Fries in cart').textContent).toBe('1');
    await user.click(screen.getByLabelText('Vegetarian only'));
    expect(screen.getByLabelText('Smash burger in cart').textContent).toBe('0');
  });

  it('warn that another restaurant starts a new cart, and then start one', async () => {
    const user = userEvent.setup();
    serve();
    await openKitchen(user);
    await user.click(screen.getByRole('button', { name: 'More Smash burger' }));
    await user.click(screen.getByRole('button', { name: 'Restaurants' }));
    await user.click(await screen.findByRole('button', { name: /^Bandra Tandoor/ }));
    expect(await screen.findByRole('note')).toHaveProperty('textContent', 'Your cart has food from Corner Kitchen. Adding something here starts a new cart.');
    expect(screen.queryByRole('button', { name: /View cart/ })).toBeNull();
    await user.click(await screen.findByRole('button', { name: 'More Paneer tikka' }));
    expect(screen.queryByRole('note')).toBeNull();
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 1 item');

    await user.click(cartButton());
    const sheet = within(await screen.findByRole('dialog', { name: 'Cart' }));
    expect(sheet.getByText('Bandra Tandoor')).toBeTruthy();
    expect(sheet.queryByText(/Smash burger/)).toBeNull();
  });

  it('Add in the search results opens the restaurant with one of the item in the cart, once', async () => {
    const user = userEvent.setup();
    serve({ 'GET /search': () => ({ body: { located: false, results: [{ restaurant: kitchen, items: [fries] }] } }) });
    render(<App />);
    await user.type(await screen.findByLabelText('Search food'), 'fries');
    await user.click(await screen.findByRole('button', { name: 'Add Fries from Corner Kitchen' }));
    await screen.findByRole('heading', { name: 'Corner Kitchen' });
    await waitFor(() => expect(screen.getByLabelText('Fries in cart').textContent).toBe('1'));
    await user.click(screen.getByRole('button', { name: 'More Smash burger' }));
    expect(screen.getByLabelText('Fries in cart').textContent).toBe('1');
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, 2 items');
  });

  it('an item the search found but the menu no longer has adds nothing', async () => {
    const user = userEvent.setup();
    const gone: MenuItem = { ...fries, id: 'gone', name: 'Old special' };
    serve({ 'GET /search': () => ({ body: { located: false, results: [{ restaurant: kitchen, items: [gone] }] } }) });
    render(<App />);
    await user.type(await screen.findByLabelText('Search food'), 'special');
    await user.click(await screen.findByRole('button', { name: 'Add Old special from Corner Kitchen' }));
    await screen.findByRole('list', { name: 'Menu' });
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, empty');
  });
});

describe('the cart until the cart page exists', () => {
  it('lists the lines and the total, and Check out places the order from the cart and empties it', async () => {
    const user = userEvent.setup();
    const server = serve();
    await openKitchen(user);
    await user.click(screen.getByRole('button', { name: 'More Smash burger' }));
    await user.click(screen.getByRole('button', { name: 'More Smash burger' }));
    await user.click(screen.getByRole('button', { name: 'More Fries' }));
    await user.click(screen.getByRole('button', { name: /View cart/ }));

    const sheet = within(await screen.findByRole('dialog', { name: 'Cart' }));
    expect(sheet.getByText('Corner Kitchen')).toBeTruthy();
    const lines = within(sheet.getByRole('list', { name: 'In your cart' })).getAllByRole('listitem').map((l) => l.textContent);
    expect(lines).toEqual(['2 × Smash burger$19.00', '1 × Fries$4.00']);
    expect(sheet.getByText('$23.00')).toBeTruthy();
    await user.click(sheet.getByRole('button', { name: 'Check out' }));

    const order = within(await screen.findByRole('dialog', { name: 'New order' }));
    await waitFor(() => expect(order.getByLabelText('Smash burger quantity').textContent).toBe('2'));
    expect(order.getByLabelText('Fries quantity').textContent).toBe('1');
    expect(order.getByLabelText('Garden salad quantity').textContent).toBe('0');
    await user.click(order.getByRole('button', { name: 'Deliver to Home' }));
    await user.click(order.getByRole('button', { name: 'Place order' }));

    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    const body = server.callsTo('POST', '/orders')[0].body as { restaurantId: string; items: unknown[]; deliveryAddressId: string };
    expect(body.restaurantId).toBe('r1');
    expect(body.deliveryAddressId).toBe('a-home');
    expect(body.items).toEqual([{ menuItemId: 'burger', quantity: 2 }, { menuItemId: 'fries', quantity: 1 }]);
    expect(JSON.stringify(body)).not.toMatch(/price/i);
    expect(await screen.findByRole('heading', { name: 'Your orders' })).toBeTruthy();
    expect(cartButton().getAttribute('aria-label')).toBe('Cart, empty');
  });

  it('carries the chosen address into the order, and leaves out what is no longer on the menu', async () => {
    const user = userEvent.setup();
    let items = [burger, fries, salad];
    serve({ 'GET /restaurants/r1/menu': () => ({ body: { restaurant: kitchen, items } }) });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Deliver to Home' }));
    await user.click(await screen.findByRole('button', { name: /^Corner Kitchen/ }));
    await user.click(await screen.findByRole('button', { name: 'More Smash burger' }));
    await user.click(screen.getByRole('button', { name: 'More Fries' }));
    items = [burger, salad]; // the fries sell out meanwhile
    await user.click(screen.getByRole('button', { name: /View cart/ }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Cart' })).getByRole('button', { name: 'Check out' }));

    const order = within(await screen.findByRole('dialog', { name: 'New order' }));
    await waitFor(() => expect(order.getByLabelText('Smash burger quantity').textContent).toBe('1'));
    expect(order.queryByLabelText('Fries quantity')).toBeNull();
    expect(order.getByRole('button', { name: 'Change' })).toBeTruthy();
    expect(order.getByText('Home')).toBeTruthy();
  });

  it('Start an order with an empty cart opens an empty order sheet', async () => {
    const user = userEvent.setup();
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Start an order' }));
    const order = within(await screen.findByRole('dialog', { name: 'New order' }));
    // the first open restaurant, as before, with nothing chosen
    await waitFor(() => expect(order.getByLabelText('Paneer tikka quantity').textContent).toBe('0'));
    // the Food home's own address and search are not drawn underneath while the sheet is open
    expect(screen.getAllByRole('region', { name: 'Find food' })).toHaveLength(1);
  });
});

describe('useCart', () => {
  const wrapper = ({ children }: { children: ReactNode }) => <CartProvider>{children}</CartProvider>;
  const item = (over: Partial<CartItem> = {}): CartItem => ({ id: 'burger', restaurantId: 'r1', name: 'Smash burger', priceCents: 950, ...over });

  it('sets a quantity, adds a line, and removes it at 0', () => {
    const { result } = renderHook(() => useCart(), { wrapper });
    act(() => result.current.setQuantity(item(), 2, 'Corner Kitchen'));
    act(() => result.current.setQuantity(item({ id: 'fries', name: 'Fries', priceCents: 400 }), 1));
    expect(result.current.restaurantId).toBe('r1');
    expect(result.current.restaurantName).toBe('Corner Kitchen');
    expect(result.current.count).toBe(3);
    expect(result.current.totalCents).toBe(2300);
    expect(result.current.quantityOf('burger')).toBe(2);
    expect(result.current.quantityOf('nothing')).toBe(0);
    act(() => result.current.setQuantity(item(), 5));
    expect(result.current.lines.map((l) => [l.menuItemId, l.quantity])).toEqual([['burger', 5], ['fries', 1]]);
    act(() => result.current.setQuantity(item(), 0));
    expect(result.current.lines.map((l) => l.menuItemId)).toEqual(['fries']);
    act(() => result.current.setQuantity(item({ id: 'fries' }), 0));
    expect(result.current.lines).toEqual([]);
    expect(result.current.restaurantId).toBeNull();
    expect(result.current.restaurantName).toBe('');
  });

  it('keeps a quantity between 0 and 50, in whole numbers', () => {
    const { result } = renderHook(() => useCart(), { wrapper });
    act(() => result.current.setQuantity(item(), 51));
    expect(result.current.quantityOf('burger')).toBe(50);
    act(() => result.current.setQuantity(item(), 2.7));
    expect(result.current.quantityOf('burger')).toBe(2);
    act(() => result.current.setQuantity(item(), Number.NaN));
    expect(result.current.count).toBe(0);
    act(() => result.current.setQuantity(item(), -3));
    expect(result.current.count).toBe(0);
  });

  it('holds one restaurant at a time: another restaurant\'s item starts a new cart', () => {
    const { result } = renderHook(() => useCart(), { wrapper });
    act(() => result.current.setQuantity(item(), 2, 'Corner Kitchen'));
    act(() => result.current.setQuantity(item({ id: 'paneer', restaurantId: 'r2', name: 'Paneer tikka' }), 1, 'Bandra Tandoor'));
    expect(result.current.restaurantId).toBe('r2');
    expect(result.current.restaurantName).toBe('Bandra Tandoor');
    expect(result.current.lines.map((l) => l.menuItemId)).toEqual(['paneer']);
  });

  it('removing nothing of another restaurant keeps the cart as it is', () => {
    const { result } = renderHook(() => useCart(), { wrapper });
    act(() => result.current.setQuantity(item(), 2, 'Corner Kitchen'));
    act(() => result.current.setQuantity(item({ id: 'paneer', restaurantId: 'r2' }), 0, 'Bandra Tandoor'));
    expect(result.current.restaurantId).toBe('r1');
    expect(result.current.restaurantName).toBe('Corner Kitchen');
    expect(result.current.count).toBe(2);
  });

  it('keeps the restaurant name it has when a later change does not give one', () => {
    const { result } = renderHook(() => useCart(), { wrapper });
    act(() => result.current.setQuantity(item(), 1, 'Corner Kitchen'));
    act(() => result.current.setQuantity(item(), 2));
    expect(result.current.restaurantName).toBe('Corner Kitchen');
  });
});
