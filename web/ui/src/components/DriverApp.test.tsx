import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from '../App';
import { setSession } from '../api';
import type { DriverJob, DriverOffer, User } from '../lib/types';
import { mockServer, session, type Handler } from '../test/server';

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); vi.useRealTimers(); Reflect.deleteProperty(navigator, 'geolocation'); });

const dev: User = { id: 'u-drv', email: 'dev@example.com', name: 'Dev Driver', role: 'driver' };

const offer = (over: Partial<DriverOffer> = {}): DriverOffer => ({
  id: 7, remainingSeconds: 45, restaurant: { name: 'Corner Kitchen', latitude: 12.9719, longitude: 77.6412 }, pickupKm: 2, ...over,
});
const job = (over: Partial<DriverJob> = {}): DriverJob => ({
  orderId: 'o1', state: 'preparing', foodReady: false, itemCount: 3,
  restaurant: { name: 'Corner Kitchen', latitude: 12.9719, longitude: 77.6412 },
  deliverTo: { line1: '42 Secret Lane', landmark: 'blue gate', city: 'Bengaluru', postalCode: '560038', latitude: 12.98, longitude: 77.64 },
  ...over,
});

interface Fake { online: boolean; offer: DriverOffer | null; job: DriverJob | null }

/** A small stateful fake of the driver's side of the server: what the driver does changes what the next read says. */
function driverServer(start: Partial<Fake> = {}, extra: Record<string, Handler> = {}) {
  const s: Fake = { online: false, offer: null, job: null, ...start };
  const me = () => ({ body: { driver: { userId: dev.id, name: dev.name, online: s.online, busy: s.job !== null, updatedAt: '2026-11-22T08:00:00Z' } } });
  const server = mockServer({
    'GET /auth/me': () => ({ body: session(dev) }),
    'GET /driver/me': me,
    'GET /driver/offers': () => ({ body: { offer: s.offer } }),
    'GET /driver/job': () => ({ body: { job: s.job } }),
    'POST /driver/online': () => { s.online = true; return me(); },
    'POST /driver/offline': () => { s.online = false; return me(); },
    'POST /driver/location': () => me(),
    'POST /driver/offers/7/accept': () => { s.job = job(); s.offer = null; return { body: { job: s.job } }; },
    'POST /driver/offers/7/decline': () => { s.offer = null; return { status: 204 }; },
    'POST /orders/o1/pickup': () => { s.job = job({ state: 'picked_up', foodReady: true }); return { body: { job: s.job } }; },
    'POST /orders/o1/deliver': () => { s.job = null; return { body: { job: job({ state: 'delivered', foodReady: true }) } }; },
    ...extra,
  });
  return { s, ...server };
}

/** The browser's position: given, or refused with the browser's own error code. */
function stubLocation(result: { lat: number; lng: number } | { code: number }) {
  const getCurrentPosition = vi.fn((ok: (p: unknown) => void, fail: (e: unknown) => void) => {
    if ('lat' in result) ok({ coords: { latitude: result.lat, longitude: result.lng } });
    else fail({ code: result.code });
  });
  Object.defineProperty(navigator, 'geolocation', { value: { getCurrentPosition }, configurable: true });
  return getCurrentPosition;
}

type User_ = ReturnType<typeof userEvent.setup>;
async function goOnline(user: User_) {
  await user.click(await screen.findByRole('button', { name: 'Go online' }));
  await screen.findByRole('button', { name: 'Go offline' });
}

describe('the driver\'s screen', () => {
  it('is their own, and asks the server for nothing of the customer\'s or staff\'s', async () => {
    const fake = driverServer();
    render(<App />);
    expect(await screen.findByRole('button', { name: 'Go online' })).toBeTruthy();
    expect(screen.getByText('Offline. Go online to be offered orders')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1 }).textContent).toMatch(/, Dev$/);
    expect(screen.queryByRole('navigation', { name: 'Sections' })).toBeNull(); // no customer tabs
    expect(screen.queryByRole('button', { name: /new order|start an order/i })).toBeNull(); // no staff dashboard
    const asked = fake.calls.map((c) => c.path.split('?')[0]);
    for (const path of ['/orders', '/alerts', '/users', '/restaurants', '/addresses', '/search']) expect(asked).not.toContain(path);
  });

  it('asks where they are only when they press Go online, and then goes online and says where', async () => {
    const user = userEvent.setup();
    const where = stubLocation({ lat: 12.99, lng: 77.64 });
    const fake = driverServer();
    render(<App />);
    await screen.findByRole('button', { name: 'Go online' });
    expect(where).not.toHaveBeenCalled(); // not on load, not while reading their state

    await goOnline(user);
    expect(where).toHaveBeenCalledTimes(1);
    expect(fake.calls.filter((c) => c.method === 'POST').map((c) => c.path)).toEqual(['/driver/online', '/driver/location']);
    expect(fake.callsTo('POST', '/driver/location')[0].body).toEqual({ latitude: 12.99, longitude: 77.64 });
    expect(fake.callsTo('POST', '/driver/online')[0].headers['X-CSRF-Token']).toBe('csrf-u-drv');
    expect(await screen.findByText('Online. Waiting for an order near you')).toBeTruthy();
    expect(screen.getByText(/sent every 30 seconds while you are online/)).toBeTruthy();
  });

  it('does not go online when the browser says no, and says what that costs', async () => {
    const user = userEvent.setup();
    stubLocation({ code: 1 });
    const fake = driverServer();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Go online' }));

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toMatch(/turned off for this page/);
    expect(alert.textContent).toMatch(/cannot go online without it/);
    expect(fake.callsTo('POST', '/driver/online')).toHaveLength(0);
    expect(screen.getByRole('button', { name: 'Go online' })).toBeTruthy();
  });

  it('does not go online when the browser has no location at all', async () => {
    const user = userEvent.setup(); // no geolocation in this browser
    const fake = driverServer();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Go online' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/could not be found/);
    expect(fake.callsTo('POST', '/driver/online')).toHaveLength(0);
  });

  it('says so when going online is refused by the server', async () => {
    const user = userEvent.setup();
    stubLocation({ lat: 12.99, lng: 77.64 });
    const fake = driverServer({}, { 'POST /driver/online': () => ({ status: 500, body: { error: 'boom' } }) });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Go online' }));
    expect(await screen.findByText(/boom|went wrong|internal/i)).toBeTruthy();
    expect(fake.callsTo('POST', '/driver/location')).toHaveLength(0);
    expect(screen.getByRole('button', { name: 'Go online' })).toBeTruthy();
  });

  it('goes offline', async () => {
    const user = userEvent.setup();
    stubLocation({ lat: 12.99, lng: 77.64 });
    const fake = driverServer({ online: true });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Go offline' }));
    await screen.findByRole('button', { name: 'Go online' });
    expect(fake.callsTo('POST', '/driver/offline')).toHaveLength(1);
    expect(screen.getByText('Offline. Go online to be offered orders')).toBeTruthy();
  });

  it('says so when it cannot reach the server', async () => {
    driverServer({}, { 'GET /driver/me': () => ({ status: 503, body: { error: 'down' } }) });
    render(<App />);
    expect((await screen.findByRole('alert')).textContent).toMatch(/cannot reach the server/);
  });

  it('opens the account sheet and signs out', async () => {
    const user = userEvent.setup();
    driverServer({}, { 'POST /auth/logout': () => ({ status: 204 }) });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Your account, Dev Driver' }));
    await user.click(await screen.findByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('tab', { name: 'Sign in' })).toBeTruthy();
  });
});

describe('sending where they are while online', () => {
  it('sends a point every 30 seconds, and stops when they go offline', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    stubLocation({ lat: 12.99, lng: 77.64 });
    const fake = driverServer();
    render(<App />);
    await goOnline(user);
    const sent = () => fake.callsTo('POST', '/driver/location').length;
    expect(sent()).toBe(1); // going online sent the first

    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    await waitFor(() => expect(sent()).toBe(2));
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    await waitFor(() => expect(sent()).toBe(3));

    await user.click(screen.getByRole('button', { name: 'Go offline' }));
    await screen.findByRole('button', { name: 'Go online' });
    const stopped = sent();
    await act(async () => { await vi.advanceTimersByTimeAsync(90_000); });
    expect(sent()).toBe(stopped);
  });

  it('warns when the position stops coming, and keeps trying', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const where = stubLocation({ lat: 12.99, lng: 77.64 });
    driverServer();
    render(<App />);
    await goOnline(user);

    stubLocation({ code: 3 }); // the browser stops giving it
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect((await screen.findByRole('alert')).textContent).toMatch(/cannot see where you are, so you may not be offered orders/);
    expect(where).toHaveBeenCalledTimes(1);

    stubLocation({ lat: 12.99, lng: 77.64 }); // and gives it again: the warning goes
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
  });
});

describe('an offer', () => {
  it('is shown with the restaurant, how far it is and the time left, and nothing of the customer', async () => {
    driverServer({ online: true, offer: offer() });
    render(<App />);
    const card = within(await screen.findByRole('region', { name: 'New order offer' }));
    expect(card.getByRole('heading', { name: 'Corner Kitchen' })).toBeTruthy();
    expect(card.getByText('2.0 km to the restaurant')).toBeTruthy();
    expect(card.getByRole('timer').textContent).toMatch(/^\d+ s to answer$/);
    expect(card.getByRole('button', { name: 'Accept' })).toBeTruthy();
    expect(card.getByRole('button', { name: 'Decline' })).toBeTruthy();
    expect(screen.getByText('An order is waiting for your answer')).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/Secret Lane|customer|\$\d/i);
  });

  it('leaves the distance out when the driver has not said where they are', async () => {
    driverServer({ online: true, offer: offer({ pickupKm: undefined }) });
    render(<App />);
    const card = within(await screen.findByRole('region', { name: 'New order offer' }));
    expect(card.queryByText(/km to the restaurant|at the restaurant/)).toBeNull();
  });

  it('can be accepted, and becomes the delivery with where it goes', async () => {
    const user = userEvent.setup();
    const fake = driverServer({ online: true, offer: offer() });
    render(<App />);
    await user.click(within(await screen.findByRole('region', { name: 'New order offer' })).getByRole('button', { name: 'Accept' }));

    const card = within(await screen.findByRole('region', { name: 'Your delivery' }));
    expect(fake.callsTo('POST', '/driver/offers/7/accept')).toHaveLength(1);
    expect(card.getByRole('heading', { name: 'Corner Kitchen' })).toBeTruthy();
    expect(card.getByText('3 items')).toBeTruthy();
    expect(card.getByText(/42 Secret Lane, Bengaluru 560038/)).toBeTruthy();
    expect(card.getByText('Landmark: blue gate')).toBeTruthy();
    expect(card.getByText('Go to the restaurant. The kitchen is still cooking.')).toBeTruthy();
    expect(card.queryByRole('button', { name: /picked up|delivered/i })).toBeNull(); // not before the food is ready
    expect(card.getAllByRole('link', { name: 'Open in maps' })).toHaveLength(2);
    expect(card.getAllByRole('link', { name: 'Open in maps' })[0].getAttribute('href')).toBe('https://www.google.com/maps/search/?api=1&query=12.9719,77.6412');
    expect(screen.queryByRole('region', { name: 'New order offer' })).toBeNull();
    expect(screen.getByText('On a delivery: collect it from the restaurant')).toBeTruthy();
  });

  it('can be declined', async () => {
    const user = userEvent.setup();
    const fake = driverServer({ online: true, offer: offer() });
    render(<App />);
    await user.click(within(await screen.findByRole('region', { name: 'New order offer' })).getByRole('button', { name: 'Decline' }));
    await waitFor(() => expect(screen.queryByRole('region', { name: 'New order offer' })).toBeNull());
    expect(fake.callsTo('POST', '/driver/offers/7/decline')).toHaveLength(1);
    expect(screen.getByText('Online. Waiting for an order near you')).toBeTruthy();
  });

  it('that was taken or ran out is said so, and goes', async () => {
    const user = userEvent.setup();
    const fake = driverServer({ online: true, offer: offer() }, {
      'POST /driver/offers/7/accept': () => { fake.s.offer = null; return { status: 404, body: { error: 'that offer is no longer open' } }; },
    });
    render(<App />);
    await user.click(within(await screen.findByRole('region', { name: 'New order offer' })).getByRole('button', { name: 'Accept' }));
    expect(await screen.findByText('That order was taken or ran out of time.')).toBeTruthy();
    await waitFor(() => expect(screen.queryByRole('region', { name: 'New order offer' })).toBeNull());
  });

  it('that cannot be accepted because they already have a delivery says what the server said', async () => {
    const user = userEvent.setup();
    driverServer({ online: true, offer: offer() }, {
      'POST /driver/offers/7/accept': () => ({ status: 409, body: { error: 'finish the delivery you have before taking another' } }),
    });
    render(<App />);
    await user.click(within(await screen.findByRole('region', { name: 'New order offer' })).getByRole('button', { name: 'Accept' }));
    expect(await screen.findByText('finish the delivery you have before taking another')).toBeTruthy();
  });

  it('counts down on this browser\'s own clock, and cannot be accepted once time is up', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const started = Date.now();
    driverServer({ online: true, offer: offer() }, {
      // the server counts down too: each read says what is left now
      'GET /driver/offers': () => ({ body: { offer: offer({ remainingSeconds: Math.max(0, 5 - (Date.now() - started) / 1000) }) } }),
    });
    render(<App />);
    const card = within(await screen.findByRole('region', { name: 'New order offer' }));
    expect(card.getByRole('timer').textContent).toMatch(/^[45] s to answer$/);
    expect((card.getByRole('button', { name: 'Accept' }) as HTMLButtonElement).disabled).toBe(false);

    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    expect(card.getByRole('timer').textContent).toMatch(/^[23] s to answer$/);

    await act(async () => { await vi.advanceTimersByTimeAsync(4_000); });
    await waitFor(() => expect(card.getByRole('timer').textContent).toBe('Time is up'));
    expect((card.getByRole('button', { name: 'Accept' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('is not shown to a driver who is already on a delivery', async () => {
    driverServer({ online: true, offer: offer(), job: job() });
    render(<App />);
    await screen.findByRole('region', { name: 'Your delivery' });
    expect(screen.queryByRole('region', { name: 'New order offer' })).toBeNull();
  });
});

describe('a delivery', () => {
  it('moves from cooking to collected to delivered, and each button is there only when it can be pressed', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const fake = driverServer({ online: true, job: job() });
    render(<App />);
    const card = within(await screen.findByRole('region', { name: 'Your delivery' }));
    expect(card.queryByRole('button', { name: 'Picked up' })).toBeNull();

    // the kitchen finishes: the next read shows it
    fake.s.job = job({ foodReady: true });
    await act(async () => { await vi.advanceTimersByTimeAsync(3_100); });
    await user.click(await card.findByRole('button', { name: 'Picked up' }));
    expect(fake.callsTo('POST', '/orders/o1/pickup')).toHaveLength(1);
    expect(await screen.findByText('Marked as picked up')).toBeTruthy();

    await waitFor(() => expect(card.getByText('Take it to the customer.')).toBeTruthy());
    expect(card.queryByRole('button', { name: 'Picked up' })).toBeNull();
    await user.click(card.getByRole('button', { name: 'Delivered' }));
    expect(fake.callsTo('POST', '/orders/o1/deliver')).toHaveLength(1);
    expect(await screen.findByText('Marked as delivered')).toBeTruthy();

    // the delivery is done: the card goes, and they are back to waiting
    await waitFor(() => expect(screen.queryByRole('region', { name: 'Your delivery' })).toBeNull());
    expect(screen.getByText('Online. Waiting for an order near you')).toBeTruthy();
  });

  it('says what the server said when a step is refused, and reads the delivery again', async () => {
    const user = userEvent.setup();
    const fake = driverServer({ online: true, job: job({ foodReady: true }) }, {
      'POST /orders/o1/pickup': () => ({ status: 409, body: { error: 'Food is not ready yet for o1' } }),
    });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Picked up' }));
    expect(await screen.findByText('Food is not ready yet for o1')).toBeTruthy();
    expect(fake.callsTo('GET', '/driver/job').length).toBeGreaterThan(1);
  });

  it('with no address kept says so instead of leaving a gap', async () => {
    driverServer({ online: true, job: job({ deliverTo: undefined }) });
    render(<App />);
    const card = within(await screen.findByRole('region', { name: 'Your delivery' }));
    expect(card.getByText('No address was kept for this order.')).toBeTruthy();
    expect(card.getAllByRole('link', { name: 'Open in maps' })).toHaveLength(1); // the restaurant's only
  });

  it('says one item for one item, and has no map link for a restaurant that is not placed', async () => {
    driverServer({ online: true, job: job({ itemCount: 1, restaurant: { name: 'Nowhere Diner' } }) });
    render(<App />);
    const card = within(await screen.findByRole('region', { name: 'Your delivery' }));
    expect(card.getByText('1 item')).toBeTruthy();
    expect(card.getAllByRole('link', { name: 'Open in maps' })).toHaveLength(1); // the delivery address's only
  });

  it('shows a delivery to an order whose restaurant has no name', async () => {
    driverServer({ online: true, job: job({ restaurant: { name: '' } }) });
    render(<App />);
    const card = within(await screen.findByRole('region', { name: 'Your delivery' }));
    expect(card.getByRole('heading', { name: 'The restaurant' })).toBeTruthy();
  });
});
