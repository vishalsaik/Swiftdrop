import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from '../App';
import { setSession } from '../api';
import type { Candidate, MenuItem, Restaurant, SavedAddress } from '../lib/types';
import { deliverHome, deliverTyped, home } from '../test/deliver';
import { customer, mockServer, order, session, staff, type Call, type Handler } from '../test/server';

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); Reflect.deleteProperty(navigator, 'geolocation'); });

const kitchen: Restaurant = { id: 'r1', name: 'Corner Kitchen', active: true, latitude: 12.9719, longitude: 77.6412, deliveryRadiusKm: 6 };
const burger: MenuItem = { id: 'burger', restaurantId: 'r1', name: 'Smash burger', priceCents: 950, available: true };
const work: SavedAddress = { id: 'a-work', userId: 'u-cust', label: 'Work', line1: '3 Church Street', city: 'Bengaluru', latitude: 12.9753, longitude: 77.6054 };
const mgRoad: Candidate = { label: '12 MG Road, Indiranagar, Bengaluru 560038', line1: '12 MG Road, Indiranagar', city: 'Bengaluru', postalCode: '560038', latitude: 12.9719, longitude: 77.6412 };

function shop(user = customer, extra: Record<string, Handler> = {}) {
  return mockServer({
    'GET /auth/me': () => ({ body: session(user) }),
    'GET /orders': () => ({ body: { orders: [] } }),
    'GET /alerts': () => ({ body: { alerts: [] } }),
    'GET /users': () => ({ body: { accounts: [] } }),
    'GET /addresses': () => ({ body: { addresses: [home, work] } }),
    'GET /restaurants': () => ({ body: { restaurants: [kitchen] } }),
    'GET /restaurants/r1/menu': () => ({ body: { restaurant: kitchen, items: [burger] } }),
    'GET /search': () => ({ body: { located: true, results: [] } }),
    'POST /orders': (c) => ({ status: 201, body: { order: order({ id: (c.body as { id: string }).id }) } }),
    'GET /orders/*': () => ({ body: { order: order() } }),
    ...extra,
  });
}

async function openSheet(user: ReturnType<typeof userEvent.setup>) {
  render(<App />);
  await user.click((await screen.findAllByRole('button', { name: /new order/i }))[0]);
  await screen.findByRole('region', { name: 'Deliver to' });
}

const posts = (s: { calls: Call[] }) => s.calls.filter((c) => c.method === 'POST' && c.path === '/orders');
const addToBasket = (user: ReturnType<typeof userEvent.setup>) => user.click(screen.getByRole('button', { name: 'More Smash burger' }));
const deliverRegion = () => within(screen.getByRole('region', { name: 'Deliver to' }));

function geolocate(result: { lat: number; lng: number } | { code: number }) {
  const getCurrentPosition = vi.fn((ok: (p: unknown) => void, fail: (e: unknown) => void) => {
    if ('lat' in result) ok({ coords: { latitude: result.lat, longitude: result.lng } });
    else fail({ code: result.code });
  });
  Object.defineProperty(navigator, 'geolocation', { value: { getCurrentPosition }, configurable: true });
  return getCurrentPosition;
}

describe('choosing a saved address', () => {
  it('lists the customer\'s own addresses, and choosing one shows it and sends its id', async () => {
    const user = userEvent.setup();
    const s = shop();
    await openSheet(user);
    const list = await screen.findByRole('list', { name: 'Saved addresses' });
    expect(within(list).getByText('12 MG Road, Bengaluru 560038 · blue gate')).toBeTruthy();
    await deliverHome(user);
    const region = deliverRegion();
    expect(region.getByText('Home')).toBeTruthy();
    expect(region.getByText('12 MG Road, Bengaluru 560038')).toBeTruthy();
    expect(region.getByText('blue gate')).toBeTruthy();
    expect(screen.queryByRole('list', { name: 'Saved addresses' })).toBeNull(); // collapsed

    await screen.findByLabelText('Smash burger quantity');
    await addToBasket(user);
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    await waitFor(() => expect(posts(s)).toHaveLength(1));
    const sent = posts(s)[0].body as Record<string, unknown>;
    expect(sent.deliveryAddressId).toBe('a-home');
    expect(sent).not.toHaveProperty('deliveryAddress');
  });

  it('Change opens the choices again', async () => {
    const user = userEvent.setup();
    const s = shop();
    await openSheet(user);
    await deliverHome(user);
    await user.click(deliverRegion().getByRole('button', { name: 'Change' }));
    await user.click(await screen.findByRole('button', { name: 'Deliver to Work' }));
    expect(deliverRegion().getByText('Work')).toBeTruthy();
    await screen.findByLabelText('Smash burger quantity');
    await addToBasket(user);
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    await waitFor(() => expect(posts(s)).toHaveLength(1));
    expect((posts(s)[0].body as { deliveryAddressId: string }).deliveryAddressId).toBe('a-work');
  });

  it('will not place an order until it is said where it is going', async () => {
    const user = userEvent.setup();
    const s = shop();
    await openSheet(user);
    await screen.findByLabelText('Smash burger quantity');
    await addToBasket(user);
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    expect(await screen.findByText('Choose where to deliver')).toBeTruthy();
    expect(posts(s)).toHaveLength(0);
  });

  it('removes a saved address, and forgets it if it was the one chosen', async () => {
    const user = userEvent.setup();
    const s = shop(customer, { 'DELETE /addresses/*': () => ({ status: 204 }) });
    await openSheet(user);
    await user.click(await screen.findByRole('button', { name: 'Remove Work' }));
    await waitFor(() => expect(s.callsTo('DELETE', '/addresses/a-work')).toHaveLength(1));
    expect(s.callsTo('DELETE', '/addresses/a-work')[0].headers['X-CSRF-Token']).toBeTruthy();
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Deliver to Work' })).toBeNull());
    // now the chosen one: reopen after choosing Home, remove it, and the choice is gone
    await deliverHome(user);
    await user.click(deliverRegion().getByRole('button', { name: 'Change' }));
    await user.click(await screen.findByRole('button', { name: 'Remove Home' }));
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Deliver to Home' })).toBeNull());
    expect(deliverRegion().queryByText('blue gate')).toBeNull();
  });

  it('says what went wrong when an address cannot be removed', async () => {
    const user = userEvent.setup();
    shop(customer, { 'DELETE /addresses/*': () => ({ status: 500, body: { error: 'Failed to delete the address due to an internal server error' } }) });
    await openSheet(user);
    await user.click(await screen.findByRole('button', { name: 'Remove Work' }));
    expect(await screen.findByText(/Failed to delete the address/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Deliver to Work' })).toBeTruthy(); // still there
  });
});

describe('searching for an address', () => {
  it('searches when Search is pressed, not as the person types', async () => {
    const user = userEvent.setup();
    const s = shop(customer, { 'GET /geocode': () => ({ body: { candidates: [mgRoad] } }) });
    await openSheet(user);
    await user.type(screen.getByLabelText('Search an address'), '12 mg road');
    expect(s.calls.some((c) => c.path.startsWith('/geocode'))).toBe(false);
    await user.click(screen.getByRole('button', { name: 'Search' }));
    const found = await screen.findByRole('list', { name: 'Addresses found' });
    expect(s.calls.filter((c) => c.path.startsWith('/geocode'))).toHaveLength(1);
    expect(s.calls.find((c) => c.path.startsWith('/geocode'))!.path).toBe('/geocode?q=12%20mg%20road');
    expect(within(found).getByText('12 MG Road, Indiranagar')).toBeTruthy();
    expect(within(found).getByText('12 MG Road, Indiranagar, Bengaluru 560038')).toBeTruthy();
  });

  it('Enter in the box searches too and never places an order', async () => {
    const user = userEvent.setup();
    const s = shop(customer, { 'GET /geocode': () => ({ body: { candidates: [mgRoad] } }) });
    await openSheet(user);
    await user.type(screen.getByLabelText('Search an address'), 'mg road{Enter}');
    await screen.findByRole('list', { name: 'Addresses found' });
    expect(posts(s)).toHaveLength(0);
  });

  it('a candidate becomes an address to confirm, and is sent with the order as typed', async () => {
    const user = userEvent.setup();
    const s = shop(customer, { 'GET /geocode': () => ({ body: { candidates: [mgRoad] } }) });
    await openSheet(user);
    await user.type(screen.getByLabelText('Search an address'), 'mg road');
    await user.click(screen.getByRole('button', { name: 'Search' }));
    await user.click(await screen.findByRole('button', { name: /12 MG Road, Indiranagar12 MG Road/ }));
    const form = within(await screen.findByRole('form', { name: 'Confirm the address' }));
    expect((form.getByLabelText('Street') as HTMLInputElement).value).toBe('12 MG Road, Indiranagar');
    expect((form.getByLabelText('City') as HTMLInputElement).value).toBe('Bengaluru');
    expect((form.getByLabelText('Postal code') as HTMLInputElement).value).toBe('560038');
    expect(form.queryByLabelText('Latitude')).toBeNull(); // the place came from the search; it is not typed
    await user.type(form.getByLabelText('Landmark'), '  second floor ');
    await user.click(form.getByRole('button', { name: 'Deliver here' }));
    expect(deliverRegion().getByText('12 MG Road, Indiranagar, Bengaluru 560038')).toBeTruthy();

    await screen.findByLabelText('Smash burger quantity');
    await addToBasket(user);
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    await waitFor(() => expect(posts(s)).toHaveLength(1));
    const sent = posts(s)[0].body as Record<string, unknown>;
    expect(sent.deliveryAddress).toEqual({
      line1: '12 MG Road, Indiranagar', landmark: 'second floor', city: 'Bengaluru', postalCode: '560038', latitude: 12.9719, longitude: 77.6412,
    });
    expect(sent).not.toHaveProperty('deliveryAddressId');
  });

  it('asks for more words before it asks the service anything', async () => {
    const user = userEvent.setup();
    const s = shop();
    await openSheet(user);
    await user.type(screen.getByLabelText('Search an address'), 'ab');
    await user.click(screen.getByRole('button', { name: 'Search' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/at least 3 characters/);
    expect(s.calls.some((c) => c.path.startsWith('/geocode'))).toBe(false);
  });

  it('says so when nothing is found', async () => {
    const user = userEvent.setup();
    shop(customer, { 'GET /geocode': () => ({ body: { candidates: [] } }) });
    await openSheet(user);
    await user.type(screen.getByLabelText('Search an address'), 'nowhere at all');
    await user.click(screen.getByRole('button', { name: 'Search' }));
    expect(await screen.findByText(/No address found for that/)).toBeTruthy();
  });

  it('says so when the server has no address search, and the other ways still work', async () => {
    const user = userEvent.setup();
    shop(customer, { 'GET /geocode': () => ({ status: 503, body: { error: 'address search is not set up on this server' } }) });
    await openSheet(user);
    await user.type(screen.getByLabelText('Search an address'), 'mg road');
    await user.click(screen.getByRole('button', { name: 'Search' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/not set up here.*saved address/);
    expect(screen.getByRole('button', { name: 'Deliver to Home' })).toBeTruthy();
  });

  it('shows the server\'s words when the service is down', async () => {
    const user = userEvent.setup();
    shop(customer, { 'GET /geocode': () => ({ status: 502, body: { error: 'address search is unavailable right now; use your location or try again in a moment' } }) });
    await openSheet(user);
    await user.type(screen.getByLabelText('Search an address'), 'mg road');
    await user.click(screen.getByRole('button', { name: 'Search' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/unavailable right now/);
  });
});

describe('delivering to where the person is', () => {
  it('asks the browser only when pressed, names the place, and keeps the exact point', async () => {
    const user = userEvent.setup();
    const ask = geolocate({ lat: 12.97194321, lng: 77.64121234 });
    const s = shop(customer, { 'GET /geocode/reverse': () => ({ body: { candidate: mgRoad } }) });
    await openSheet(user);
    expect(ask).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Deliver to where I am' }));
    const form = within(await screen.findByRole('form', { name: 'Confirm the address' }));
    expect(s.calls.find((c) => c.path.startsWith('/geocode/reverse'))!.path).toBe('/geocode/reverse?lat=12.971943&lng=77.641212');
    expect((form.getByLabelText('Street') as HTMLInputElement).value).toBe('12 MG Road, Indiranagar');
    await user.click(form.getByRole('button', { name: 'Deliver here' }));
    await screen.findByLabelText('Smash burger quantity');
    await addToBasket(user);
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    await waitFor(() => expect(posts(s)).toHaveLength(1));
    const address = (posts(s)[0].body as { deliveryAddress: { latitude: number; longitude: number } }).deliveryAddress;
    expect([address.latitude, address.longitude]).toEqual([12.971943, 77.641212]); // where they are, not where the street is
  });

  it('when the place cannot be named the person types the street', async () => {
    const user = userEvent.setup();
    geolocate({ lat: 12.9, lng: 77.6 });
    shop(customer, { 'GET /geocode/reverse': () => ({ body: { candidate: null } }) });
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Deliver to where I am' }));
    const form = within(await screen.findByRole('form', { name: 'Confirm the address' }));
    expect((form.getByLabelText('Street') as HTMLInputElement).value).toBe('');
    expect(screen.getByText(/found where you are but not the street/i)).toBeTruthy();
    await user.click(form.getByRole('button', { name: 'Deliver here' }));
    expect(await screen.findByText('The street address is required')).toBeTruthy();
    await user.type(form.getByLabelText('Street'), 'Behind the temple');
    await user.click(form.getByRole('button', { name: 'Deliver here' }));
    expect(deliverRegion().getByText('Behind the temple')).toBeTruthy();
  });

  it('still offers the place when the address service is not there at all', async () => {
    const user = userEvent.setup();
    geolocate({ lat: 12.9, lng: 77.6 });
    shop(customer, { 'GET /geocode/reverse': () => ({ status: 503, body: { error: 'address search is not set up on this server' } }) });
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Deliver to where I am' }));
    await screen.findByRole('form', { name: 'Confirm the address' });
    expect(screen.queryByText(/not set up/)).toBeNull(); // not an error the customer can do anything about
  });

  it('shows another failure of the service', async () => {
    const user = userEvent.setup();
    geolocate({ lat: 12.9, lng: 77.6 });
    shop(customer, { 'GET /geocode/reverse': () => ({ status: 429, body: { error: 'too many address searches; wait a moment and try again' } }) });
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Deliver to where I am' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/too many address searches/);
    await screen.findByRole('form', { name: 'Confirm the address' }); // the point is still theirs to use
  });

  it('explains a refusal', async () => {
    const user = userEvent.setup();
    geolocate({ code: 1 });
    shop();
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Deliver to where I am' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/turned off for this page/);
    expect(screen.queryByRole('form', { name: 'Confirm the address' })).toBeNull();
  });
});

describe('typing an address in', () => {
  it('needs a street and a place, and says which is missing', async () => {
    const user = userEvent.setup();
    shop();
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Type it in' }));
    const form = within(await screen.findByRole('form', { name: 'Confirm the address' }));
    await user.click(form.getByRole('button', { name: 'Deliver here' }));
    expect(await screen.findByText('The street address is required')).toBeTruthy();
    expect(screen.getByText('Latitude is a number from -90 to 90')).toBeTruthy();
    expect(screen.getByText('Longitude is a number from -180 to 180')).toBeTruthy();
    await user.click(form.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('form', { name: 'Confirm the address' })).toBeNull();
  });

  it('is how staff say where a phone order goes, and staff are not offered saved addresses', async () => {
    const user = userEvent.setup();
    const s = shop(staff);
    await openSheet(user);
    expect(s.calls.some((c) => c.path.startsWith('/addresses'))).toBe(false);
    expect(screen.queryByRole('list', { name: 'Saved addresses' })).toBeNull();
    await deliverTyped(user, '7 Phone Order Lane');
    expect(deliverRegion().getByText('7 Phone Order Lane')).toBeTruthy();
    await screen.findByLabelText('Smash burger quantity');
    await addToBasket(user);
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    await waitFor(() => expect(posts(s)).toHaveLength(1));
    expect((posts(s)[0].body as { deliveryAddress: unknown }).deliveryAddress).toEqual({ line1: '7 Phone Order Lane', latitude: 12.9719, longitude: 77.6412 });
  });

  it('shows staff no way to save an address', async () => {
    const user = userEvent.setup();
    shop(staff);
    await openSheet(user);
    await user.click(screen.getByRole('button', { name: 'Type it in' }));
    expect(screen.queryByLabelText(/Save this address/)).toBeNull();
  });
});

describe('saving an address for next time', () => {
  async function toTheForm(user: ReturnType<typeof userEvent.setup>) {
    await user.type(screen.getByLabelText('Search an address'), 'mg road');
    await user.click(screen.getByRole('button', { name: 'Search' }));
    await user.click(await screen.findByRole('button', { name: /12 MG Road, Indiranagar12 MG Road/ }));
    return within(await screen.findByRole('form', { name: 'Confirm the address' }));
  }

  it('saves it, uses the saved one, and adds it to the list', async () => {
    const user = userEvent.setup();
    const made: SavedAddress = { ...home, id: 'a-new', label: 'Gym', line1: '12 MG Road, Indiranagar' };
    const s = shop(customer, {
      'GET /geocode': () => ({ body: { candidates: [mgRoad] } }),
      'POST /addresses': () => ({ status: 201, body: { address: made } }),
    });
    await openSheet(user);
    const form = await toTheForm(user);
    expect(form.queryByLabelText('Name')).toBeNull();
    await user.click(form.getByLabelText(/Save this address/));
    await user.type(form.getByLabelText('Name'), 'Gym');
    await user.click(form.getByRole('button', { name: 'Deliver here' }));
    await waitFor(() => expect(s.callsTo('POST', '/addresses')).toHaveLength(1));
    expect(s.callsTo('POST', '/addresses')[0].body).toEqual({
      label: 'Gym', line1: '12 MG Road, Indiranagar', city: 'Bengaluru', postalCode: '560038', latitude: 12.9719, longitude: 77.6412,
    });
    expect(await screen.findByText('Address saved')).toBeTruthy();
    // it is in the list now
    await user.click(deliverRegion().getByRole('button', { name: 'Change' }));
    expect(await screen.findByRole('button', { name: 'Deliver to Gym' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Deliver to Gym' }));
    await screen.findByLabelText('Smash burger quantity');
    await addToBasket(user);
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    await waitFor(() => expect(posts(s)).toHaveLength(1));
    expect((posts(s)[0].body as { deliveryAddressId: string }).deliveryAddressId).toBe('a-new');
  });

  it('shows what the server says is wrong and does not go on', async () => {
    const user = userEvent.setup();
    const s = shop(customer, {
      'GET /geocode': () => ({ body: { candidates: [mgRoad] } }),
      'POST /addresses': () => ({ status: 422, body: { error: 'some fields are not acceptable', fields: { label: 'must be at most 40 characters' } } }),
    });
    await openSheet(user);
    const form = await toTheForm(user);
    await user.click(form.getByLabelText(/Save this address/));
    await user.click(form.getByRole('button', { name: 'Deliver here' }));
    expect(await screen.findByText('Name must be at most 40 characters')).toBeTruthy();
    expect(screen.getByRole('form', { name: 'Confirm the address' })).toBeTruthy(); // still there to fix
    expect(posts(s)).toHaveLength(0);
  });

  it('says so when the person already keeps ten', async () => {
    const user = userEvent.setup();
    shop(customer, {
      'GET /geocode': () => ({ body: { candidates: [mgRoad] } }),
      'POST /addresses': () => ({ status: 409, body: { error: 'you can keep at most 10 saved addresses; delete one first' } }),
    });
    await openSheet(user);
    const form = await toTheForm(user);
    await user.click(form.getByLabelText(/Save this address/));
    await user.click(form.getByRole('button', { name: 'Deliver here' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/at most 10 saved addresses/);
  });
});

describe('the search and the order follow the address', () => {
  it('the food search is near the chosen address and asks nothing of the browser', async () => {
    const user = userEvent.setup();
    const ask = geolocate({ lat: 1, lng: 1 });
    const s = shop();
    await openSheet(user);
    await deliverHome(user);
    expect(await screen.findByText('Near Home')).toBeTruthy();
    await waitFor(() => expect(s.calls.some((c) => c.path === '/search?lat=12.972&lng=77.641')).toBe(true));
    expect(ask).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: 'Use my location' })).toBeNull(); // the address is the location
  });

  it('shows the server\'s words when the restaurant does not deliver there', async () => {
    const user = userEvent.setup();
    shop(customer, { 'POST /orders': () => ({ status: 422, body: { error: 'Corner Kitchen delivers up to 6.0 km and that address is 7.4 km away' } }) });
    await openSheet(user);
    await deliverHome(user);
    await screen.findByLabelText('Smash burger quantity');
    await addToBasket(user);
    await user.click(screen.getByRole('button', { name: 'Place order' }));
    expect(await screen.findByText('Corner Kitchen delivers up to 6.0 km and that address is 7.4 km away')).toBeTruthy();
  });
});

describe('an order shows where it is going', () => {
  it('in the order drawer, with the landmark', async () => {
    const user = userEvent.setup();
    mockServer({
      'GET /auth/me': () => ({ body: session(staff) }),
      'GET /orders': () => ({ body: { orders: [order({ id: 'o1', deliveryAddress: { line1: '12 MG Road', city: 'Bengaluru', postalCode: '560038', landmark: 'blue gate', latitude: 1, longitude: 2 } }), order({ id: 'old' })] } }),
      'GET /alerts': () => ({ body: { alerts: [] } }),
    });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /order o1/i }));
    const panel = within(await screen.findByLabelText('Order details'));
    expect(panel.getByText('Deliver to')).toBeTruthy();
    expect(panel.getByText('12 MG Road, Bengaluru 560038 · blue gate')).toBeTruthy();
    // an order from before addresses existed has none to show
    await user.click(screen.getByRole('button', { name: /order old/i }));
    await waitFor(() => expect(within(screen.getByLabelText('Order details')).queryByText('Deliver to')).toBeNull());
  });
});
