import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from '../App';
import { setSession } from '../api';
import type { MenuItem, Restaurant } from '../lib/types';
import { mockServer, session, staff, type Handler } from '../test/server';

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); Reflect.deleteProperty(navigator, 'geolocation'); });

const unplaced: Restaurant = { id: 'r1', name: 'Corner Kitchen', active: true, deliveryRadiusKm: 5 };
const placed: Restaurant = { id: 'r2', name: 'Noodle Bar', active: true, cuisine: 'Thai', latitude: 12.9784, longitude: 77.6408, deliveryRadiusKm: 4 };
const dal: MenuItem = { id: 'dal', restaurantId: 'r1', name: 'Dal', priceCents: 400, available: true, veg: true };
const wings: MenuItem = { id: 'wings', restaurantId: 'r1', name: 'Wings', priceCents: 600, available: true, veg: false };

function server(extra: Record<string, Handler> = {}) {
  return mockServer({
    'GET /auth/me': () => ({ body: session(staff) }),
    'GET /orders': () => ({ body: { orders: [] } }),
    'GET /alerts': () => ({ body: { alerts: [] } }),
    'GET /restaurants': () => ({ body: { restaurants: [unplaced, placed] } }),
    'GET /restaurants/r1/menu': () => ({ body: { restaurant: unplaced, items: [dal, wings] } }),
    'GET /restaurants/r2/menu': () => ({ body: { restaurant: placed, items: [] } }),
    ...extra,
  });
}

async function openRestaurant(user: ReturnType<typeof userEvent.setup>, name: RegExp) {
  render(<App />);
  await user.click((await screen.findAllByRole('button', { name: /^menu$/i }))[0]);
  await user.click(await screen.findByRole('button', { name }));
  return within(await screen.findByRole('dialog', { name: /./ }));
}

describe('placing a restaurant', () => {
  it('says a restaurant that is not placed will not be found near anyone', async () => {
    const user = userEvent.setup();
    server();
    const sheet = await openRestaurant(user, /Corner Kitchen/);
    expect(await sheet.findByText(/not placed yet/i)).toBeTruthy();
    expect((sheet.getByLabelText('Latitude') as HTMLInputElement).value).toBe('');
    expect((sheet.getByLabelText('Delivers up to (km)') as HTMLInputElement).value).toBe('5');
  });

  it('starts from where a placed restaurant is', async () => {
    const user = userEvent.setup();
    server();
    const sheet = await openRestaurant(user, /Noodle Bar/);
    expect(await sheet.findByText(/within the delivery radius/i)).toBeTruthy();
    expect((sheet.getByLabelText('Cuisine') as HTMLInputElement).value).toBe('Thai');
    expect((sheet.getByLabelText('Latitude') as HTMLInputElement).value).toBe('12.9784');
    expect((sheet.getByLabelText('Longitude') as HTMLInputElement).value).toBe('77.6408');
    expect((sheet.getByLabelText('Delivers up to (km)') as HTMLInputElement).value).toBe('4');
  });

  it('saves the place as numbers', async () => {
    const user = userEvent.setup();
    const s = server({ 'PUT /restaurants/r1/place': () => ({ body: { restaurant: { ...unplaced, cuisine: 'American' } } }) });
    const sheet = await openRestaurant(user, /Corner Kitchen/);
    await user.type(await sheet.findByLabelText('Cuisine'), ' American ');
    await user.type(sheet.getByLabelText('Latitude'), '12.9719');
    await user.type(sheet.getByLabelText('Longitude'), '77.6412');
    await user.clear(sheet.getByLabelText('Delivers up to (km)'));
    await user.type(sheet.getByLabelText('Delivers up to (km)'), '7.5');
    await user.click(sheet.getByRole('button', { name: 'Save place' }));
    await waitFor(() => expect(s.callsTo('PUT', '/restaurants/r1/place')).toHaveLength(1));
    const call = s.callsTo('PUT', '/restaurants/r1/place')[0];
    expect(call.body).toEqual({ cuisine: 'American', latitude: 12.9719, longitude: 77.6412, deliveryRadiusKm: 7.5 });
    expect(call.headers['X-CSRF-Token']).toBeTruthy();
    expect(await screen.findByText('Place saved')).toBeTruthy();
  });

  it('checks the place before sending anything', async () => {
    const user = userEvent.setup();
    const s = server({ 'PUT /restaurants/r1/place': () => ({ body: {} }) });
    const sheet = await openRestaurant(user, /Corner Kitchen/);
    await user.type(await sheet.findByLabelText('Latitude'), '95');
    await user.click(sheet.getByRole('button', { name: 'Save place' }));
    expect(await sheet.findByText('Latitude is a number from -90 to 90')).toBeTruthy();
    expect(sheet.getByText('Longitude is a number from -180 to 180')).toBeTruthy(); // latitude alone is not a place
    expect(s.callsTo('PUT', '/restaurants/r1/place')).toHaveLength(0);
  });

  it('shows what the server says is wrong', async () => {
    const user = userEvent.setup();
    server({ 'PUT /restaurants/r1/place': () => ({ status: 422, body: { error: 'some fields are not acceptable', fields: { deliveryRadiusKm: 'must be above 0 and at most 100' } } }) });
    const sheet = await openRestaurant(user, /Corner Kitchen/);
    await user.click(await sheet.findByRole('button', { name: 'Save place' }));
    expect(await sheet.findByText('Delivery radius must be above 0 and at most 100')).toBeTruthy();
  });

  it('shows a failure that is not about a field', async () => {
    const user = userEvent.setup();
    server({ 'PUT /restaurants/r1/place': () => ({ status: 500, body: { error: 'Failed to set the restaurant\'s place due to an internal server error' } }) });
    const sheet = await openRestaurant(user, /Corner Kitchen/);
    await user.click(await sheet.findByRole('button', { name: 'Save place' }));
    expect(await screen.findByText(/Failed to set the restaurant/)).toBeTruthy();
  });

  it('can fill in where staff are standing', async () => {
    const user = userEvent.setup();
    const getCurrentPosition = vi.fn((ok: (p: unknown) => void) => ok({ coords: { latitude: 12.971987, longitude: 77.641234 } }));
    Object.defineProperty(navigator, 'geolocation', { value: { getCurrentPosition }, configurable: true });
    server();
    const sheet = await openRestaurant(user, /Corner Kitchen/);
    expect(getCurrentPosition).not.toHaveBeenCalled();
    await user.click(await sheet.findByRole('button', { name: 'Use my current location' }));
    await waitFor(() => expect((sheet.getByLabelText('Latitude') as HTMLInputElement).value).toBe('12.97199'));
    expect((sheet.getByLabelText('Longitude') as HTMLInputElement).value).toBe('77.64123');
  });

  it('says so when the position cannot be had', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'geolocation', { value: { getCurrentPosition: (_ok: unknown, fail: (e: unknown) => void) => fail({ code: 1 }) }, configurable: true });
    server();
    const sheet = await openRestaurant(user, /Corner Kitchen/);
    await user.click(await sheet.findByRole('button', { name: 'Use my current location' }));
    expect((await sheet.findByRole('alert')).textContent).toMatch(/turned off/i);
  });
});

describe('vegetarian items', () => {
  it('marks the vegetarian ones in the menu', async () => {
    const user = userEvent.setup();
    server();
    const sheet = await openRestaurant(user, /Corner Kitchen/);
    const dalRow = (await sheet.findByText('Dal')).closest('li') as HTMLElement;
    expect(within(dalRow).getByText('Veg')).toBeTruthy();
    const wingsRow = sheet.getByText('Wings').closest('li') as HTMLElement;
    expect(within(wingsRow).queryByText('Veg')).toBeNull();
  });

  it('adds an item with its vegetarian flag, and edits it', async () => {
    const user = userEvent.setup();
    const s = server({
      'POST /restaurants/r1/menu': () => ({ status: 201, body: { item: dal } }),
      'PATCH /menu/wings': () => ({ body: { item: wings } }),
    });
    const sheet = await openRestaurant(user, /Corner Kitchen/);
    await user.click(await sheet.findByRole('button', { name: /add an item/i }));
    const form = within(await screen.findByRole('dialog', { name: 'Add an item' }));
    await user.type(form.getByLabelText('Name'), 'Paneer');
    await user.type(form.getByLabelText('Price'), '5');
    await user.click(form.getByLabelText('Vegetarian'));
    await user.click(form.getByRole('button', { name: 'Add to the menu' }));
    await waitFor(() => expect(s.callsTo('POST', '/restaurants/r1/menu')).toHaveLength(1));
    expect(s.callsTo('POST', '/restaurants/r1/menu')[0].body).toEqual({ name: 'Paneer', description: '', priceCents: 500, veg: true });

    // editing starts from what the item is now: wings are not vegetarian
    await user.click(await sheet.findByRole('button', { name: 'Edit Wings' }));
    const edit = within(await screen.findByRole('dialog', { name: 'Edit item' }));
    expect((edit.getByLabelText('Vegetarian') as HTMLInputElement).checked).toBe(false);
    await user.click(edit.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(s.callsTo('PATCH', '/menu/wings')).toHaveLength(1));
    expect(s.callsTo('PATCH', '/menu/wings')[0].body).toEqual({ name: 'Wings', description: '', priceCents: 600, veg: false });
  });
});
