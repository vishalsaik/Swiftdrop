import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from '../App';
import { setSession } from '../api';
import type { MenuItem, Restaurant, SearchHit } from '../lib/types';
import { customer, mockServer, order, session, type Call, type Handler } from '../test/server';

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); Reflect.deleteProperty(navigator, 'geolocation'); });

const kitchen: Restaurant = { id: 'r1', name: 'Corner Kitchen', active: true, cuisine: 'American', latitude: 12.9719, longitude: 77.6412, deliveryRadiusKm: 6 };
const noodles: Restaurant = { id: 'r2', name: 'Noodle Bar', active: true, cuisine: 'Thai' };
const burger: MenuItem = { id: 'burger', restaurantId: 'r1', name: 'Smash burger', description: 'Two patties', priceCents: 950, available: true, veg: false };
const fries: MenuItem = { id: 'fries', restaurantId: 'r1', name: 'Fries', priceCents: 400, available: true, veg: true };
const padThai: MenuItem = { id: 'thai', restaurantId: 'r2', name: 'Pad thai', priceCents: 1390, available: true };

const hitFor = (r: Restaurant, items: MenuItem[], distanceKm?: number): SearchHit => ({ restaurant: r, items, ...(distanceKm === undefined ? {} : { distanceKm }) });

/** What the browser does when asked where the person is. */
function geolocate(result: { lat: number; lng: number } | { code: number }) {
  const getCurrentPosition = vi.fn((ok: (p: unknown) => void, fail: (e: unknown) => void) => {
    if ('lat' in result) ok({ coords: { latitude: result.lat, longitude: result.lng } });
    else fail({ code: result.code });
  });
  Object.defineProperty(navigator, 'geolocation', { value: { getCurrentPosition }, configurable: true });
  return getCurrentPosition;
}

const searches = (server: { calls: Call[] }) => server.calls.filter((c) => c.path.startsWith('/search')).map((c) => c.path);

function shop(search: Handler = () => ({ body: { results: [], located: false } }), extra: Record<string, Handler> = {}) {
  return mockServer({
    'GET /auth/me': () => ({ body: session(customer) }),
    'GET /orders': () => ({ body: { orders: [] } }),
    'GET /restaurants': () => ({ body: { restaurants: [kitchen, noodles] } }),
    'GET /restaurants/r1/menu': () => ({ body: { restaurant: kitchen, items: [burger, fries] } }),
    'GET /restaurants/r2/menu': () => ({ body: { restaurant: noodles, items: [padThai] } }),
    'GET /search': search,
    'POST /orders': (c) => ({ status: 201, body: { order: order({ id: (c.body as { id: string }).id }) } }),
    'GET /orders/*': () => ({ body: { order: order() } }),
    ...extra,
  });
}

async function openSheet(user: ReturnType<typeof userEvent.setup>) {
  render(<App />);
  await user.click((await screen.findAllByRole('button', { name: /new order/i }))[0]);
  await screen.findByLabelText('Search food');
}

const results = () => screen.findByRole('list', { name: 'Search results' });

describe('finding food', () => {
  it('asks nothing of the server, and nothing of the browser, until the person does something', async () => {
    const user = userEvent.setup();
    const ask = geolocate({ lat: 12.97, lng: 77.64 });
    const server = shop();
    await openSheet(user);
    await screen.findByText('Smash burger'); // the first restaurant's menu has loaded
    expect(searches(server)).toEqual([]);
    expect(ask).not.toHaveBeenCalled(); // no permission prompt just for opening the page
    expect(screen.getByText(/not kept/i)).toBeTruthy();
  });

  it('searches as the person types, waiting a moment, and shows what it found', async () => {
    const user = userEvent.setup();
    const server = shop(() => ({ body: { located: false, results: [hitFor(kitchen, [burger, fries])] } }));
    await openSheet(user);
    await user.type(screen.getByLabelText('Search food'), 'burger');
    const list = await results();
    expect(within(list).getByText('Corner Kitchen')).toBeTruthy();
    expect(within(list).getByText('American')).toBeTruthy();
    expect(within(list).getByText('$9.50')).toBeTruthy();
    expect(within(list).getByText('Two patties')).toBeTruthy();
    expect(within(list).getByText('Veg')).toBeTruthy(); // fries only
    // one request for the whole word, not one for each letter
    expect(searches(server)).toEqual(['/search?q=burger']);
  });

  it('says so when nothing matches', async () => {
    const user = userEvent.setup();
    shop();
    await openSheet(user);
    await user.type(screen.getByLabelText('Search food'), 'zzz');
    expect(await screen.findByText('Nothing matches that.')).toBeTruthy();
  });

  it('shows what went wrong, in the server\'s words', async () => {
    const user = userEvent.setup();
    shop(() => ({ status: 500, body: { error: 'Failed to search due to an internal server error' } }));
    await openSheet(user);
    await user.type(screen.getByLabelText('Search food'), 'x');
    expect((await screen.findAllByRole('alert')).some((a) => /Failed to search/.test(a.textContent ?? ''))).toBe(true);
  });

  it('can be limited to vegetarian food', async () => {
    const user = userEvent.setup();
    const server = shop(() => ({ body: { located: false, results: [hitFor(kitchen, [fries])] } }));
    await openSheet(user);
    await user.click(screen.getByLabelText('Vegetarian only'));
    await results();
    expect(searches(server)).toEqual(['/search?veg=true']);
    await user.click(screen.getByLabelText('Vegetarian only'));
    await waitFor(() => expect(screen.queryByRole('list', { name: 'Search results' })).toBeNull());
    expect(screen.getByText(/not kept/i)).toBeTruthy(); // nothing asked for again: back to the hint
  });

  it('a search box cannot place an order: Enter in it does nothing but search', async () => {
    const user = userEvent.setup();
    const server = shop(() => ({ body: { located: false, results: [hitFor(kitchen, [burger])] } }));
    await openSheet(user);
    await user.type(screen.getByLabelText('Search food'), 'burger{Enter}');
    await results();
    expect(server.callsTo('POST', '/orders')).toHaveLength(0);
  });
});

describe('finding food near the person', () => {
  it('asks the browser only when "Use my location" is pressed, and searches with the position rounded', async () => {
    const user = userEvent.setup();
    const ask = geolocate({ lat: 12.97194, lng: 77.64149 });
    const server = shop(() => ({ body: { located: true, results: [hitFor(kitchen, [burger], 0.7), hitFor(noodles, [padThai], 2.34)] } }));
    await openSheet(user);
    expect(ask).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Use my location' }));
    const list = await results();
    expect(ask).toHaveBeenCalledTimes(1);
    expect(searches(server)).toEqual(['/search?lat=12.972&lng=77.641']);
    expect(within(list).getByText('American · 700 m away')).toBeTruthy();
    expect(within(list).getByText('Thai · 2.3 km away')).toBeTruthy();
    expect(screen.getByRole('button', { name: /near you/i }).getAttribute('aria-pressed')).toBe('true');
  });

  it('combines a position with words and the vegetarian filter', async () => {
    const user = userEvent.setup();
    geolocate({ lat: 12.9719, lng: 77.6412 });
    const server = shop(() => ({ body: { located: true, results: [] } }));
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Use my location' }));
    await user.click(screen.getByLabelText('Vegetarian only'));
    await user.type(screen.getByLabelText('Search food'), 'tea');
    await waitFor(() => expect(searches(server).at(-1)).toBe('/search?q=tea&lat=12.972&lng=77.641&veg=true'));
    expect(await screen.findByText('Nothing that delivers to you matches that yet.')).toBeTruthy();
  });

  it('stops using the position when told to, and forgets it', async () => {
    const user = userEvent.setup();
    geolocate({ lat: 12.9719, lng: 77.6412 });
    const server = shop(() => ({ body: { located: true, results: [hitFor(kitchen, [burger], 0.1)] } }));
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Use my location' }));
    await results();
    await user.click(screen.getByRole('button', { name: /near you/i }));
    expect(await screen.findByRole('button', { name: 'Use my location' })).toBeTruthy();
    await waitFor(() => expect(screen.queryByRole('list', { name: 'Search results' })).toBeNull());
    expect(searches(server)).toEqual(['/search?lat=12.972&lng=77.641']); // nothing more was sent
    expect(localStorage.length).toBe(0); // and nothing was kept
  });

  it('explains a refusal, and the search by name still works', async () => {
    const user = userEvent.setup();
    geolocate({ code: 1 });
    const server = shop(() => ({ body: { located: false, results: [hitFor(kitchen, [burger])] } }));
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Use my location' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/turned off for this page.*search by name/i);
    expect(searches(server)).toEqual([]); // no position, so nothing is searched
    expect(screen.getByRole('button', { name: 'Use my location' })).toBeTruthy(); // can try again
    await user.type(screen.getByLabelText('Search food'), 'burger');
    await results();
    expect(searches(server)).toEqual(['/search?q=burger']); // by name only: no position was sent
  });

  it('says so when the position cannot be found or takes too long', async () => {
    const user = userEvent.setup();
    geolocate({ code: 3 });
    shop();
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Use my location' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/too long/i);
    geolocate({ code: 2 });
    await user.click(screen.getByRole('button', { name: 'Use my location' }));
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/could not be found/i));
  });

  it('says so when the browser cannot say where anyone is', async () => {
    const user = userEvent.setup();
    shop(); // jsdom has no geolocation
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Use my location' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/could not be found/i);
  });

  it('shows that it is looking while the browser decides', async () => {
    const user = userEvent.setup();
    let answer: (p: unknown) => void = () => {};
    Object.defineProperty(navigator, 'geolocation', { value: { getCurrentPosition: (ok: (p: unknown) => void) => { answer = ok; } }, configurable: true });
    shop(() => ({ body: { located: true, results: [] } }));
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Use my location' }));
    const waiting = await screen.findByRole('button', { name: /finding you/i });
    expect((waiting as HTMLButtonElement).disabled).toBe(true);
    answer({ coords: { latitude: 1, longitude: 2 } });
    expect(await screen.findByRole('button', { name: /near you/i })).toBeTruthy();
  });
});

describe('choosing from the results', () => {
  it('Add on an item of another restaurant switches to it and starts a basket with that item', async () => {
    const user = userEvent.setup();
    const server = shop(() => ({ body: { located: false, results: [hitFor(noodles, [padThai])] } }));
    await openSheet(user);
    await user.type(screen.getByLabelText('Search food'), 'thai');
    await user.click(await screen.findByRole('button', { name: 'Add Pad thai from Noodle Bar' }));
    // the restaurant is now Noodle Bar and its menu shows Pad thai with one in the basket
    await waitFor(() => expect((screen.getByLabelText('Restaurant') as HTMLSelectElement).value).toBe('r2'));
    expect(await screen.findByLabelText('Pad thai quantity')).toBeTruthy();
    await waitFor(() => expect(screen.getByLabelText('Pad thai quantity').textContent).toBe('1'));
    expect(within(document.querySelector('.order-total') as HTMLElement).getByText(/\$/).textContent).toBe('$13.90');

    await user.click(screen.getByRole('button', { name: 'Place order' }));
    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    const sent = server.callsTo('POST', '/orders')[0].body as { restaurantId: string; items: unknown[] };
    expect(sent.restaurantId).toBe('r2');
    expect(sent.items).toEqual([{ menuItemId: 'thai', quantity: 1 }]); // an id and a count, as ever
  });

  it('Add on an item of the restaurant already chosen adds one more each time', async () => {
    const user = userEvent.setup();
    shop(() => ({ body: { located: false, results: [hitFor(kitchen, [burger])] } }));
    await openSheet(user);
    await screen.findByLabelText('Smash burger quantity'); // Corner Kitchen is the first restaurant
    await user.type(screen.getByLabelText('Search food'), 'burger');
    const add = await screen.findByRole('button', { name: 'Add Smash burger from Corner Kitchen' });
    await user.click(add);
    await user.click(add);
    expect(screen.getByLabelText('Smash burger quantity').textContent).toBe('2');
  });

  it('choosing a restaurant starts its menu with an empty basket', async () => {
    const user = userEvent.setup();
    shop(() => ({ body: { located: false, results: [hitFor(noodles, [padThai])] } }));
    await openSheet(user);
    await user.click(await (async () => { await user.type(screen.getByLabelText('Search food'), 'thai'); return screen.findByRole('button', { name: 'Order from Noodle Bar' }); })());
    await waitFor(() => expect((screen.getByLabelText('Restaurant') as HTMLSelectElement).value).toBe('r2'));
    expect((await screen.findByLabelText('Pad thai quantity')).textContent).toBe('0');
  });
});
