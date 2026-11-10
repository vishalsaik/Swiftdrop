import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from '../App';
import { setSession } from '../api';
import type { MenuItem, Restaurant } from '../lib/types';
import { customer, mockServer, session, staff, type Handler } from '../test/server';

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); });

const kitchen: Restaurant = { id: 'r1', name: 'Corner Kitchen', active: true };
const cafe: Restaurant = { id: 'r2', name: 'Quiet Cafe', active: false };
const burger: MenuItem = { id: 'burger', restaurantId: 'r1', name: 'Smash burger', description: 'Two patties', priceCents: 950, available: true };
const fries: MenuItem = { id: 'fries', restaurantId: 'r1', name: 'Fries', priceCents: 400, available: false };

function menuServer(extra: Record<string, Handler> = {}) {
  return mockServer({
    'GET /auth/me': () => ({ body: session(staff) }),
    'GET /orders': () => ({ body: { orders: [] } }),
    'GET /alerts': () => ({ body: { alerts: [] } }),
    'GET /restaurants': () => ({ body: { restaurants: [kitchen, cafe] } }),
    'GET /restaurants/r1/menu': () => ({ body: { restaurant: kitchen, items: [burger, fries] } }),
    ...extra,
  });
}

async function openMenu(user: ReturnType<typeof userEvent.setup>) {
  render(<App />);
  const buttons = await screen.findAllByRole('button', { name: /^menu$/i });
  await user.click(buttons[0]);
  return screen.findByRole('button', { name: /Corner Kitchen, open/ });
}

describe('the menu screen', () => {
  it('lists every restaurant, open or closed, asking for all of them', async () => {
    const user = userEvent.setup();
    const server = menuServer();
    await openMenu(user);
    expect(screen.getByRole('button', { name: /Quiet Cafe, closed/ })).toBeTruthy();
    expect(server.calls.some((c) => c.path === '/restaurants?all=true')).toBe(true);
  });

  it('is not offered to a customer', async () => {
    const server = mockServer({
      'GET /auth/me': () => ({ body: session(customer) }),
      'GET /orders': () => ({ body: { orders: [] } }),
    });
    render(<App />);
    await screen.findByText(/good (morning|afternoon|evening|night), casey/i);
    expect(screen.queryByRole('button', { name: /^menu$/i })).toBeNull();
    expect(server.calls.some((c) => c.path.startsWith('/restaurants?all'))).toBe(false);
  });

  it('says so when there is nothing yet', async () => {
    const user = userEvent.setup();
    menuServer({ 'GET /restaurants': () => ({ body: { restaurants: [] } }) });
    render(<App />);
    await user.click((await screen.findAllByRole('button', { name: /^menu$/i }))[0]);
    expect(await screen.findByText('No restaurants yet')).toBeTruthy();
  });

  it('says so when the list cannot be read', async () => {
    const user = userEvent.setup();
    menuServer({ 'GET /restaurants': () => ({ status: 500, body: { error: 'Failed to list restaurants due to an internal server error' } }) });
    render(<App />);
    await user.click((await screen.findAllByRole('button', { name: /^menu$/i }))[0]);
    expect((await screen.findByRole('alert')).textContent).toMatch(/internal server error/);
  });

  it('adds a restaurant, with a name check before anything is sent', async () => {
    const user = userEvent.setup();
    const server = menuServer({
      'POST /restaurants': (c) => ({ status: 201, body: { restaurant: { id: 'r9', name: (c.body as { name: string }).name, active: true } } }),
    });
    await openMenu(user);
    await user.click(screen.getByRole('button', { name: /add a restaurant/i }));
    await user.click(await screen.findByRole('button', { name: 'Add restaurant' }));
    expect(await screen.findByText('Name the restaurant')).toBeTruthy();
    expect(server.callsTo('POST', '/restaurants')).toHaveLength(0);

    await user.type(screen.getByLabelText('Name'), '  Noodle Bar ');
    await user.click(screen.getByRole('button', { name: 'Add restaurant' }));
    await waitFor(() => expect(server.callsTo('POST', '/restaurants')).toHaveLength(1));
    expect(server.callsTo('POST', '/restaurants')[0].body).toEqual({ name: 'Noodle Bar' });
    expect(server.callsTo('POST', '/restaurants')[0].headers['X-CSRF-Token']).toBeTruthy();
  });

  it('shows what the server says is wrong with a name', async () => {
    const user = userEvent.setup();
    menuServer({ 'POST /restaurants': () => ({ status: 422, body: { error: 'some fields are not acceptable', fields: { name: 'must not contain control characters' } } }) });
    await openMenu(user);
    await user.click(screen.getByRole('button', { name: /add a restaurant/i }));
    await user.type(await screen.findByLabelText('Name'), 'Odd');
    await user.click(screen.getByRole('button', { name: 'Add restaurant' }));
    expect(await screen.findByText('Name must not contain control characters')).toBeTruthy();
  });

  it('shows a restaurant with its whole menu, sold-out items marked', async () => {
    const user = userEvent.setup();
    const server = menuServer();
    await openMenu(user);
    await user.click(screen.getByRole('button', { name: /Corner Kitchen, open/ }));
    expect(await screen.findByText('Smash burger')).toBeTruthy();
    expect(screen.getByText('$9.50')).toBeTruthy();
    expect(screen.getByText('Two patties')).toBeTruthy();
    expect(screen.getByText('Sold out')).toBeTruthy();
    expect(server.calls.some((c) => c.path === '/restaurants/r1/menu?all=true')).toBe(true);
  });

  it('closes and opens a restaurant', async () => {
    const user = userEvent.setup();
    let active = true;
    const server = menuServer({
      'GET /restaurants': () => ({ body: { restaurants: [{ ...kitchen, active }, cafe] } }),
      'PATCH /restaurants/r1': (c) => { active = (c.body as { active: boolean }).active; return { body: { restaurant: { ...kitchen, active } } }; },
    });
    await openMenu(user);
    await user.click(screen.getByRole('button', { name: /Corner Kitchen, open/ }));
    await user.click(await screen.findByRole('button', { name: 'Close this restaurant' }));
    await waitFor(() => expect(server.callsTo('PATCH', '/restaurants/r1')[0]?.body).toEqual({ active: false }));
    expect(await screen.findByRole('button', { name: 'Open this restaurant' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Open this restaurant' }));
    await waitFor(() => expect(server.callsTo('PATCH', '/restaurants/r1')[1]?.body).toEqual({ active: true }));
  });

  it('renames a restaurant only when the name changed and is good', async () => {
    const user = userEvent.setup();
    const server = menuServer({ 'PATCH /restaurants/r1': () => ({ body: { restaurant: { ...kitchen, name: 'Corner Diner' } } }) });
    await openMenu(user);
    await user.click(screen.getByRole('button', { name: /Corner Kitchen, open/ }));
    const save = await screen.findByRole('button', { name: 'Save name' });
    expect((save as HTMLButtonElement).disabled).toBe(true); // unchanged
    const name = within(save.closest('form') as HTMLElement).getByLabelText('Name');
    await user.clear(name);
    await user.click(save);
    expect(await screen.findByText('Name the restaurant')).toBeTruthy();
    expect(server.callsTo('PATCH', '/restaurants/r1')).toHaveLength(0);
    await user.type(name, 'Corner Diner');
    await user.click(save);
    await waitFor(() => expect(server.callsTo('PATCH', '/restaurants/r1')[0]?.body).toEqual({ name: 'Corner Diner' }));
  });

  it('marks an item sold out and puts it back', async () => {
    const user = userEvent.setup();
    const server = menuServer({ 'PATCH /menu/*': () => ({ body: { item: burger } }) });
    await openMenu(user);
    await user.click(screen.getByRole('button', { name: /Corner Kitchen, open/ }));
    await user.click(await screen.findByRole('button', { name: 'Mark sold out: Smash burger' }));
    await waitFor(() => expect(server.callsTo('PATCH', '/menu/burger')[0]?.body).toEqual({ available: false }));
    await user.click(screen.getByRole('button', { name: 'Put back on the menu: Fries' }));
    await waitFor(() => expect(server.callsTo('PATCH', '/menu/fries')[0]?.body).toEqual({ available: true }));
  });

  it('adds an item with its price in cents', async () => {
    const user = userEvent.setup();
    const server = menuServer({ 'POST /restaurants/r1/menu': () => ({ status: 201, body: { item: burger } }) });
    await openMenu(user);
    await user.click(screen.getByRole('button', { name: /Corner Kitchen, open/ }));
    await user.click(await screen.findByRole('button', { name: /add an item/i }));
    const form = within(await screen.findByRole('dialog', { name: 'Add an item' }));
    await user.click(form.getByRole('button', { name: 'Add to the menu' }));
    expect(await form.findByText('Name the item')).toBeTruthy();
    expect(form.getByText('Enter a price of 0 or more')).toBeTruthy();
    expect(server.callsTo('POST', '/restaurants/r1/menu')).toHaveLength(0);

    await user.type(form.getByLabelText('Name'), 'Milkshake');
    await user.type(form.getByLabelText('Price'), '5.50');
    await user.click(form.getByRole('button', { name: 'Add to the menu' }));
    await waitFor(() => expect(server.callsTo('POST', '/restaurants/r1/menu')).toHaveLength(1));
    expect(server.callsTo('POST', '/restaurants/r1/menu')[0].body).toEqual({ name: 'Milkshake', description: '', priceCents: 550, veg: false });
  });

  it('edits an item, starting from what it is now, and shows the server\'s complaint', async () => {
    const user = userEvent.setup();
    const server = menuServer({
      'PATCH /menu/burger': () => ({ status: 422, body: { error: 'some fields are not acceptable', fields: { priceCents: 'must be between 0 and 1000000 (that is $10,000.00)' } } }),
    });
    await openMenu(user);
    await user.click(screen.getByRole('button', { name: /Corner Kitchen, open/ }));
    await user.click(await screen.findByRole('button', { name: 'Edit Smash burger' }));
    const form = within(await screen.findByRole('dialog', { name: 'Edit item' }));
    const price = form.getByLabelText('Price') as HTMLInputElement;
    expect(price.value).toBe('9.50');
    expect((form.getByLabelText('Name') as HTMLInputElement).value).toBe('Smash burger');
    await user.clear(price);
    await user.type(price, '12');
    await user.click(form.getByRole('button', { name: 'Save changes' }));
    expect(await screen.findByText(/Price must be between 0 and 1000000/)).toBeTruthy();
    expect(server.callsTo('PATCH', '/menu/burger')[0].body).toEqual({ name: 'Smash burger', description: 'Two patties', priceCents: 1200, veg: false });
  });

  it('tells the person when a change is refused', async () => {
    const user = userEvent.setup();
    menuServer({ 'PATCH /menu/*': () => ({ status: 500, body: { error: 'Failed to update the menu item due to an internal server error' } }) });
    await openMenu(user);
    await user.click(screen.getByRole('button', { name: /Corner Kitchen, open/ }));
    await user.click(await screen.findByRole('button', { name: 'Mark sold out: Smash burger' }));
    expect(await screen.findByText(/Failed to update the menu item/)).toBeTruthy();
  });
});
