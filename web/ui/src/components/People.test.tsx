import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from '../App';
import { setSession } from '../api';
import type { AccountEvent, User } from '../lib/types';
import { customer, mockServer, session, staff, type Handler } from '../test/server';

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); });

const ada: User = { id: 'u-ada', email: 'ada@example.com', name: 'Ada Lovelace', role: 'customer', createdAt: '2026-11-01T09:00:00Z' };
const dee: User = { id: 'u-dee', email: 'dee@example.com', name: 'Dee Patch', role: 'staff', createdAt: '2026-11-02T09:00:00Z', mustChangePassword: true };
const gone: User = { id: 'u-gone', email: 'gone@example.com', name: 'Gone Away', role: 'customer', createdAt: '2026-10-01T09:00:00Z', disabledAt: '2026-11-03T09:00:00Z' };
const me: User = { ...staff, createdAt: '2026-10-20T09:00:00Z' };

const history: AccountEvent[] = [
  { id: 2, userId: 'u-ada', actorId: 'u-staff', actorEmail: 'sam@example.com', kind: 'password_reset', at: '2026-11-05T09:00:00Z' },
  { id: 1, userId: 'u-ada', actorId: 'u-staff', actorEmail: 'sam@example.com', kind: 'created', detail: 'role=customer', at: '2026-11-01T09:00:00Z' },
];

function people(accounts: User[] = [me, ada, dee, gone], extra: Record<string, Handler> = {}) {
  return mockServer({
    'GET /auth/me': () => ({ body: session(staff) }),
    'GET /orders': () => ({ body: { orders: [] } }),
    'GET /alerts': () => ({ body: { alerts: [] } }),
    'GET /users': () => ({ body: { accounts } }),
    'GET /users/u-ada': () => ({ body: { user: ada, events: history } }),
    'GET /users/u-gone': () => ({ body: { user: gone, events: [] } }),
    'GET /users/u-staff': () => ({ body: { user: me, events: [] } }),
    ...extra,
  });
}

async function openPeople(user: ReturnType<typeof userEvent.setup>) {
  render(<App />);
  const buttons = await screen.findAllByRole('button', { name: /^people$/i });
  await user.click(buttons[0]);
  return screen.findByText('Ada Lovelace');
}

describe('the people screen', () => {
  it('lists everyone with what they are and what is wrong', async () => {
    const user = userEvent.setup();
    people();
    await openPeople(user);
    const row = (name: RegExp) => screen.getByRole('button', { name });
    expect(within(row(/Ada Lovelace/)).getByText('Customer')).toBeTruthy();
    expect(within(row(/Dee Patch/)).getByText('Temporary password')).toBeTruthy();
    expect(within(row(/Gone Away/)).getByText('Disabled')).toBeTruthy();
    expect(within(row(/Sam Staff, sam@example.com/)).getByText('You')).toBeTruthy();
  });

  it('asks the server for the filter that was chosen, and for what was typed', async () => {
    const user = userEvent.setup();
    const server = people();
    await openPeople(user);
    await user.click(screen.getByRole('tab', { name: 'Staff' }));
    await waitFor(() => expect(server.calls.some((c) => c.path.startsWith('/users?') && c.path.includes('role=staff'))).toBe(true));
    await user.click(screen.getByRole('tab', { name: 'Disabled' }));
    await waitFor(() => expect(server.calls.some((c) => c.path.includes('status=disabled'))).toBe(true));
    await user.type(screen.getByLabelText('Search people'), 'ada');
    await waitFor(() => expect(server.calls.some((c) => c.path.includes('q=ada'))).toBe(true));
  });

  it('is not offered to a customer, and nothing is asked of the server', async () => {
    const server = mockServer({
      'GET /auth/me': () => ({ body: session(customer) }),
      'GET /orders': () => ({ body: { orders: [] } }),
    });
    render(<App />);
    await screen.findByText(/good (morning|afternoon|evening|night), casey/i);
    expect(screen.queryAllByRole('button', { name: /^people$/i })).toHaveLength(0);
    expect(server.calls.filter((c) => c.path.startsWith('/users'))).toHaveLength(0);
  });

  it('says so when the server refuses the list', async () => {
    const user = userEvent.setup();
    people([], { 'GET /users': () => ({ status: 500, body: { error: 'boom' } }) });
    render(<App />);
    await user.click((await screen.findAllByRole('button', { name: /^people$/i }))[0]);
    expect(await screen.findByRole('alert')).toBeTruthy();
  });
});

describe('adding a person', () => {
  async function addSheet(user: ReturnType<typeof userEvent.setup>, extra: Record<string, Handler> = {}) {
    const server = people([me, ada], extra);
    await openPeople(user);
    await user.click(screen.getByRole('button', { name: /add a person/i }));
    return { server, sheet: await screen.findByRole('dialog') };
  }

  it('checks what was typed before sending anything', async () => {
    const user = userEvent.setup();
    const { server, sheet } = await addSheet(user);
    await user.type(within(sheet).getByLabelText('Email'), 'not an email');
    await user.click(within(sheet).getByRole('button', { name: 'Create account' }));
    expect(await within(sheet).findByText('Enter a valid email address')).toBeTruthy();
    expect(within(sheet).getByText('Enter their name')).toBeTruthy();
    expect(server.callsTo('POST', '/users')).toHaveLength(0);
  });

  it('sends the person with the CSRF token and shows the temporary password once', async () => {
    const user = userEvent.setup();
    const made: User = { id: 'u-new', email: 'new@example.com', name: 'New Person', role: 'staff', mustChangePassword: true };
    const { server, sheet } = await addSheet(user, { 'POST /users': () => ({ status: 201, body: { user: made, temporaryPassword: 'Tmp-Pass-123456789' } }) });
    await user.type(within(sheet).getByLabelText('Name'), '  New Person ');
    await user.type(within(sheet).getByLabelText('Email'), 'new@example.com');
    await user.click(within(sheet).getByRole('tab', { name: 'Staff' }));
    await user.click(within(sheet).getByRole('button', { name: 'Create account' }));

    expect(await within(sheet).findByText('Tmp-Pass-123456789')).toBeTruthy();
    const call = server.callsTo('POST', '/users')[0];
    expect(call.body).toEqual({ email: 'new@example.com', name: 'New Person', role: 'staff' });
    expect(call.headers['X-CSRF-Token']).toBe('csrf-u-staff');
    expect(within(sheet).getByText(/must choose their own password/)).toBeTruthy();
    // the list was read again, so the new person is there
    await waitFor(() => expect(server.callsTo('GET', '/users').length).toBeGreaterThan(1));
  });

  it('says when the email already has an account', async () => {
    const user = userEvent.setup();
    const { sheet } = await addSheet(user, { 'POST /users': () => ({ status: 409, body: { error: 'an account with that email already exists' } }) });
    await user.type(within(sheet).getByLabelText('Name'), 'Ada Again');
    await user.type(within(sheet).getByLabelText('Email'), 'ada@example.com');
    await user.click(within(sheet).getByRole('button', { name: 'Create account' }));
    expect(await within(sheet).findByText('an account with that email already exists')).toBeTruthy();
  });

  it('puts the server\'s problem with a field next to the form', async () => {
    const user = userEvent.setup();
    const { sheet } = await addSheet(user, { 'POST /users': () => ({ status: 422, body: { error: 'x', fields: { name: 'is required' } } }) });
    await user.type(within(sheet).getByLabelText('Name'), 'x');
    await user.type(within(sheet).getByLabelText('Email'), 'a@b.co');
    await user.click(within(sheet).getByRole('button', { name: 'Create account' }));
    expect(await within(sheet).findByText('Name is required')).toBeTruthy();
  });
});

describe('one person', () => {
  async function openAda(user: ReturnType<typeof userEvent.setup>, extra: Record<string, Handler> = {}) {
    const server = people([me, ada, gone], extra);
    await openPeople(user);
    await user.click(screen.getByRole('button', { name: /Ada Lovelace/ }));
    return { server, sheet: await screen.findByRole('dialog') };
  }

  it('shows their history, newest first, with who did what', async () => {
    const user = userEvent.setup();
    const { sheet } = await openAda(user);
    expect(await within(sheet).findByText('Password reset to a temporary one')).toBeTruthy();
    expect(within(sheet).getByText('Account created as customer')).toBeTruthy();
    expect(within(sheet).getAllByText('by sam@example.com')).toHaveLength(2);
  });

  it('needs two taps to disable, and sends the CSRF token', async () => {
    const user = userEvent.setup();
    const { server, sheet } = await openAda(user, { 'POST /users/u-ada/disable': () => ({ body: { user: { ...ada, disabledAt: '2026-11-06T09:00:00Z' } } }) });
    await user.click(within(sheet).getByRole('button', { name: 'Disable this account' }));
    expect(server.callsTo('POST', '/users/u-ada/disable')).toHaveLength(0); // the first tap only asks
    await user.click(within(sheet).getByRole('button', { name: /confirm: disable/i }));
    await waitFor(() => expect(server.callsTo('POST', '/users/u-ada/disable')).toHaveLength(1));
    expect(server.callsTo('POST', '/users/u-ada/disable')[0].headers['X-CSRF-Token']).toBe('csrf-u-staff');
    expect(await screen.findByText('Account disabled and signed out everywhere')).toBeTruthy();
  });

  it('shows a temporary password once after a reset', async () => {
    const user = userEvent.setup();
    const { server, sheet } = await openAda(user, { 'POST /users/u-ada/reset-password': () => ({ body: { user: ada, temporaryPassword: 'Reset-Pass-987654321' } }) });
    await user.click(within(sheet).getByRole('button', { name: 'Reset password' }));
    expect(server.callsTo('POST', '/users/u-ada/reset-password')).toHaveLength(0);
    await user.click(within(sheet).getByRole('button', { name: /confirm: reset/i }));
    expect(await within(sheet).findByText('Reset-Pass-987654321')).toBeTruthy();
  });

  it('offers to enable an account that is switched off', async () => {
    const user = userEvent.setup();
    const server = people([me, ada, gone], { 'POST /users/u-gone/enable': () => ({ body: { user: { ...gone, disabledAt: undefined } } }) });
    await openPeople(user);
    await user.click(screen.getByRole('button', { name: /Gone Away/ }));
    const sheet = await screen.findByRole('dialog');
    expect(within(sheet).queryByRole('button', { name: 'Disable this account' })).toBeNull();
    await user.click(within(sheet).getByRole('button', { name: 'Enable this account' }));
    await waitFor(() => expect(server.callsTo('POST', '/users/u-gone/enable')).toHaveLength(1)); // one tap, it is the safe direction
  });

  it('says why the server refused, such as the last staff account', async () => {
    const user = userEvent.setup();
    const { sheet } = await openAda(user, { 'POST /users/u-ada/disable': () => ({ status: 409, body: { error: 'that would leave no active staff account' } }) });
    await user.click(within(sheet).getByRole('button', { name: 'Disable this account' }));
    await user.click(within(sheet).getByRole('button', { name: /confirm: disable/i }));
    expect(await screen.findByText('that would leave no active staff account')).toBeTruthy();
  });

  it('offers no way to disable or reset your own account', async () => {
    const user = userEvent.setup();
    people([me, ada]);
    await openPeople(user);
    await user.click(screen.getByRole('button', { name: /Sam Staff, sam@example.com/ }));
    const sheet = await screen.findByRole('dialog');
    expect(within(sheet).getByText(/This is your own account/)).toBeTruthy();
    expect(within(sheet).queryByRole('button', { name: /disable|reset/i })).toBeNull();
  });
});

describe('a temporary password', () => {
  const mustChange = { user: { ...staff, mustChangePassword: true }, csrfToken: 'csrf-temp' };

  function temp(extra: Record<string, Handler> = {}) {
    return mockServer({
      'GET /auth/me': () => ({ body: mustChange }),
      'GET /orders': () => ({ body: { orders: [] } }),
      'GET /alerts': () => ({ body: { alerts: [] } }),
      'POST /auth/logout': () => ({ status: 204 }),
      ...extra,
    });
  }

  it('must be replaced before anything else: the app is not even asked for', async () => {
    const server = temp();
    render(<App />);
    expect(await screen.findByRole('heading', { name: /choose your own password/i })).toBeTruthy();
    expect(screen.queryByText(/good (morning|afternoon|evening|night)/i)).toBeNull();
    expect(server.callsTo('GET', '/orders')).toHaveLength(0);
  });

  it('checks the new password before sending it', async () => {
    const user = userEvent.setup();
    const server = temp();
    render(<App />);
    await screen.findByRole('heading', { name: /choose your own password/i });
    await user.click(screen.getByRole('button', { name: 'Save and continue' }));
    expect(await screen.findByText('Enter the temporary password you were given')).toBeTruthy();
    expect(screen.getByText('Use at least 10 characters')).toBeTruthy();
    expect(server.callsTo('POST', '/auth/password')).toHaveLength(0);
  });

  it('opens the app once the person has chosen their own', async () => {
    const user = userEvent.setup();
    const server = temp({ 'POST /auth/password': () => ({ status: 204 }) });
    render(<App />);
    await screen.findByRole('heading', { name: /choose your own password/i });
    await user.type(screen.getByLabelText('Temporary'), 'Tmp-Pass-123456789');
    await user.type(screen.getByLabelText('New'), 'my own long passphrase');
    await user.click(screen.getByRole('button', { name: 'Save and continue' }));

    expect(await screen.findByText(/good (morning|afternoon|evening|night), sam/i)).toBeTruthy();
    const call = server.callsTo('POST', '/auth/password')[0];
    expect(call.body).toEqual({ currentPassword: 'Tmp-Pass-123456789', newPassword: 'my own long passphrase' });
    expect(call.headers['X-CSRF-Token']).toBe('csrf-temp');
  });

  it('says when the temporary password was typed wrong', async () => {
    const user = userEvent.setup();
    temp({ 'POST /auth/password': () => ({ status: 403, body: { error: 'current password is incorrect' } }) });
    render(<App />);
    await screen.findByRole('heading', { name: /choose your own password/i });
    await user.type(screen.getByLabelText('Temporary'), 'wrong');
    await user.type(screen.getByLabelText('New'), 'my own long passphrase');
    await user.click(screen.getByRole('button', { name: 'Save and continue' }));
    expect(await screen.findByText('That is not your current password')).toBeTruthy();
    expect(screen.queryByText(/good (morning|afternoon|evening|night)/i)).toBeNull();
  });

  it('can be left by signing out', async () => {
    const user = userEvent.setup();
    const server = temp();
    render(<App />);
    await screen.findByRole('heading', { name: /choose your own password/i });
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('heading', { name: /welcome to swiftdrop/i })).toBeTruthy();
    expect(server.callsTo('POST', '/auth/logout')).toHaveLength(1);
  });
});
