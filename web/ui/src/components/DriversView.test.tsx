import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from '../App';
import { setSession } from '../api';
import type { Driver } from '../lib/types';
import { customer, mockServer, session, staff, type Handler } from '../test/server';

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); });

const recent = new Date(Date.now() - 20_000).toISOString();
const dev: Driver = { userId: 'u-1', name: 'Dev Driver', online: true, busy: false, updatedAt: recent, locationAt: recent, latitude: 12.97, longitude: 77.64 };
const busy: Driver = { userId: 'u-2', name: 'Bea Busy', online: true, busy: true, updatedAt: recent, locationAt: recent };
const away: Driver = { userId: 'u-3', name: 'Ann Away', online: false, busy: false, updatedAt: recent };
const silent: Driver = { userId: 'u-4', name: 'Sid Silent', online: true, busy: false, updatedAt: recent };

function staffServer(drivers: Driver[] | null = [dev, busy, away, silent], extra: Record<string, Handler> = {}) {
  return mockServer({
    'GET /auth/me': () => ({ body: session(staff) }),
    'GET /orders': () => ({ body: { orders: [] } }),
    'GET /alerts': () => ({ body: { alerts: [] } }),
    'GET /drivers': () => ({ body: { drivers } }),
    ...extra,
  });
}

async function openDrivers(user: ReturnType<typeof userEvent.setup>) {
  render(<App />);
  await user.click((await screen.findAllByRole('button', { name: /^drivers$/i }))[0]);
}

describe('staff\'s list of drivers', () => {
  it('shows who is online, free or on a delivery, and when each was last heard from', async () => {
    const user = userEvent.setup();
    staffServer();
    await openDrivers(user);
    const row = (name: RegExp) => within(screen.getByLabelText(name));

    expect(await screen.findByText('3 of 4 online')).toBeTruthy();
    expect(row(/^Dev Driver/).getByText('Online')).toBeTruthy();
    expect(row(/^Dev Driver/).getByText('Free')).toBeTruthy();
    expect(row(/^Dev Driver/).getByText(/Last seen (just now|\d+s ago)/)).toBeTruthy();
    expect(row(/^Bea Busy/).getByText('On a delivery')).toBeTruthy();
    expect(row(/^Ann Away/).getByText('Offline')).toBeTruthy();
    expect(row(/^Ann Away/).getByText('Not taking deliveries')).toBeTruthy();
    expect(row(/^Sid Silent/).getByText('Has not sent a location yet')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Drivers');
  });

  it('is read from the server and followed by it', async () => {
    const user = userEvent.setup();
    const server = staffServer();
    await openDrivers(user);
    await screen.findByText('3 of 4 online');
    expect(server.callsTo('GET', '/drivers').length).toBeGreaterThanOrEqual(1);
  });

  it('says there are none, and where to make one', async () => {
    const user = userEvent.setup();
    staffServer([]);
    await openDrivers(user);
    expect(await screen.findByText('No drivers yet')).toBeTruthy();
    expect(screen.getByText(/Add one under People, with the Driver role/)).toBeTruthy();
  });

  it('treats an answer of nothing as no drivers', async () => {
    const user = userEvent.setup();
    staffServer(null);
    await openDrivers(user);
    expect(await screen.findByText('No drivers yet')).toBeTruthy();
  });

  it('says so when the server refuses the list', async () => {
    const user = userEvent.setup();
    staffServer([], { 'GET /drivers': () => ({ status: 500, body: { error: 'boom' } }) });
    await openDrivers(user);
    expect(await screen.findByRole('alert')).toBeTruthy();
  });

  it('is not offered to a customer, and nothing is asked of the server', async () => {
    const server = mockServer({
      'GET /auth/me': () => ({ body: session(customer) }),
      'GET /orders': () => ({ body: { orders: [] } }),
    });
    render(<App />);
    await screen.findByText(/good (morning|afternoon|evening|night), casey/i);
    expect(screen.queryAllByRole('button', { name: /^drivers$/i })).toHaveLength(0);
    expect(server.calls.filter((c) => c.path.startsWith('/drivers'))).toHaveLength(0);
  });
});

describe('a driver on an order, as staff and customers see it', () => {
  it('shows staff the driver\'s name in the order, instead of an id', async () => {
    const user = userEvent.setup();
    mockServer({
      'GET /auth/me': () => ({ body: session(staff) }),
      'GET /orders': () => ({ body: { orders: [{
        id: 'o1', customerId: 'u-cust', restaurantId: 'r1', paymentType: 'cash', state: 'preparing', createdAt: new Date().toISOString(),
        items: [{ sku: 'B', name: 'Burger', quantity: 1, unitPriceCents: 500 }], assignedDriverId: '1f2e3d4c-aaaa-bbbb-cccc-000000000000', driverName: 'Dev Driver',
      }] } }),
      'GET /alerts': () => ({ body: { alerts: [] } }),
    });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /^Order o1/i }).catch(() => screen.findByText('o1')));
    const drawer = within(await screen.findByLabelText('Order details'));
    expect(drawer.getByText('Dev Driver')).toBeTruthy();
    expect(drawer.queryByText('1f2e3d4c')).toBeNull();
  });
});
