import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from './App';
import { setSession } from './api';
import { customer, mockServer, order, session, staff, type Handler } from './test/server';

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); });

const signedOut: Record<string, Handler> = { 'GET /auth/me': () => ({ status: 401, body: { error: 'sign in required' } }) };

function board(user = staff, orders = [order({ id: 'o1', state: 'paid' }), order({ id: 'o2', state: 'created' })], extra: Record<string, Handler> = {}) {
  return mockServer({
    'GET /auth/me': () => ({ body: session(user) }),
    'GET /orders': () => ({ body: { orders } }),
    'GET /alerts': () => ({ body: { alerts: [] } }),
    'POST /auth/logout': () => ({ status: 204 }),
    ...extra,
  });
}

describe('signing in', () => {
  it('shows the sign-in screen when nobody is signed in', async () => {
    mockServer(signedOut);
    render(<App />);
    expect(await screen.findByRole('heading', { name: /welcome to swiftdrop/i })).toBeTruthy();
  });

  it('sends the email and password and then shows the board with the CSRF token ready', async () => {
    const user = userEvent.setup();
    let signedIn = false;
    const server = mockServer({
      'GET /auth/me': () => (signedIn ? { body: session(staff) } : { status: 401, body: {} }),
      'POST /auth/login': () => { signedIn = true; return { body: session(staff) }; },
      'GET /orders': () => ({ body: { orders: [order({ id: 'o1', state: 'paid' })] } }),
      'GET /alerts': () => ({ body: { alerts: [] } }),
    });
    render(<App />);
    await user.type(await screen.findByLabelText('Email'), '  sam@example.com ');
    await user.type(screen.getByLabelText('Password'), 'correct horse battery');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect(await screen.findByText(/good (morning|afternoon|evening|night), sam/i)).toBeTruthy();
    expect(server.callsTo('POST', '/auth/login')[0].body).toEqual({ email: 'sam@example.com', password: 'correct horse battery' });
    expect(await screen.findByText('o1')).toBeTruthy();
  });

  it('says a wrong password and an unknown email the same way', async () => {
    const user = userEvent.setup();
    mockServer({ ...signedOut, 'POST /auth/login': () => ({ status: 401, body: { error: 'email or password is incorrect' } }) });
    render(<App />);
    await user.type(await screen.findByLabelText('Email'), 'a@b.co');
    await user.type(screen.getByLabelText('Password'), 'whatever it is');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText('That email and password do not match.')).toBeTruthy();
  });

  it('says how long a lockout lasts', async () => {
    const user = userEvent.setup();
    mockServer({ ...signedOut, 'POST /auth/login': () => ({ status: 429, body: { error: 'x' }, headers: { 'Retry-After': '600' } }) });
    render(<App />);
    await user.type(await screen.findByLabelText('Email'), 'a@b.co');
    await user.type(screen.getByLabelText('Password'), 'whatever it is');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText('Too many attempts. Try again in 10 minutes.')).toBeTruthy();
  });

  it('checks a new account before sending anything', async () => {
    const user = userEvent.setup();
    const server = mockServer(signedOut);
    render(<App />);
    await user.click(await screen.findByRole('tab', { name: 'Create account' }));
    await user.type(screen.getByLabelText('Email'), 'not an email');
    await user.type(screen.getByLabelText('Password'), 'short');
    await user.click(screen.getByRole('button', { name: 'Create account' }));
    expect(await screen.findByText('Enter a valid email address')).toBeTruthy();
    expect(screen.getByText('Enter your name')).toBeTruthy();
    expect(screen.getByText('Use at least 10 characters')).toBeTruthy();
    expect(server.callsTo('POST', '/auth/signup')).toHaveLength(0);
  });

  it('puts the server\'s field problems next to the form', async () => {
    const user = userEvent.setup();
    mockServer({ ...signedOut, 'POST /auth/signup': () => ({ status: 422, body: { error: 'x', fields: { password: 'is one of the most common passwords' } } }) });
    render(<App />);
    await user.click(await screen.findByRole('tab', { name: 'Create account' }));
    await user.type(screen.getByLabelText('Name'), 'Ada');
    await user.type(screen.getByLabelText('Email'), 'ada@example.com');
    await user.type(screen.getByLabelText('Password'), 'password123');
    await user.click(screen.getByRole('button', { name: 'Create account' }));
    expect(await screen.findByText('Password is one of the most common passwords')).toBeTruthy();
  });
});

describe('who sees what', () => {
  it('shows staff the alerts and everyone\'s orders', async () => {
    board(staff, [order({ id: 'o1', customerId: 'someone' })]);
    render(<App />);
    expect((await screen.findAllByRole('button', { name: /alerts/i })).length).toBeGreaterThan(0); // the sidebar and the tab bar, one of which CSS hides
    expect(await screen.findByText(/someone/)).toBeTruthy();
  });

  it('shows a customer no alerts, and never asks for them', async () => {
    const server = board(customer, [order({ id: 'o1' })]);
    render(<App />);
    expect(await screen.findByText('o1')).toBeTruthy();
    expect(screen.queryAllByRole('button', { name: /alerts/i })).toHaveLength(0);
    expect(server.callsTo('GET', '/alerts')).toHaveLength(0);
    expect(screen.getByText('Here are your orders.')).toBeTruthy();
  });

  it('offers a customer only paying and cancelling their own order', async () => {
    const user = userEvent.setup();
    board(customer, [order({ id: 'o1', state: 'created' })]);
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /order o1/i }));
    const panel = await screen.findByLabelText('Order details');
    expect(within(panel).getByRole('button', { name: 'Pay now' })).toBeTruthy();
    expect(within(panel).getByRole('button', { name: 'Cancel order' })).toBeTruthy();
    expect(within(panel).queryByRole('button', { name: /accept|take payment/i })).toBeNull();
    expect(within(panel).queryByText('Payment history')).toBeNull();
  });
});

describe('acting on an order', () => {
  it('needs two taps to cancel, and sends the CSRF token', async () => {
    const user = userEvent.setup();
    const server = board(staff, [order({ id: 'o1', state: 'paid' })], { 'POST /orders/o1/cancel': () => ({ body: {} }), 'GET /orders/o1/payment-history': () => ({ body: { events: [] } }) });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /order o1/i }));
    const panel = await screen.findByLabelText('Order details');
    await user.click(within(panel).getByRole('button', { name: 'Cancel order' }));
    expect(server.callsTo('POST', '/orders/o1/cancel')).toHaveLength(0); // the first tap only asks
    await user.click(within(panel).getByRole('button', { name: /confirm: cancel order/i }));
    await waitFor(() => expect(server.callsTo('POST', '/orders/o1/cancel')).toHaveLength(1));
    expect(server.callsTo('POST', '/orders/o1/cancel')[0].headers['X-CSRF-Token']).toBe('csrf-u-staff');
    expect(await screen.findByText('o1: Order cancelled')).toBeTruthy();
  });

  it('says why a payment failed and whether another try is allowed', async () => {
    const user = userEvent.setup();
    board(staff, [order({ id: 'o1', state: 'created' })], {
      'POST /orders/o1/pay': () => ({ status: 402, body: { error: 'card declined', canRetry: true, attemptsLeft: 2 } }),
    });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /order o1/i }));
    const panel = await screen.findByLabelText('Order details');
    await user.click(within(panel).getByRole('button', { name: 'Take payment' })); // opens the method field
    await user.click(within(panel).getByRole('button', { name: 'Take payment' }));
    expect(await screen.findByText(/o1: card declined Try another method \(2 attempts left\)\./)).toBeTruthy();
  });
});

describe('leaving', () => {
  it('goes back to the sign-in screen after signing out, and forgets the board', async () => {
    const user = userEvent.setup();
    const server = board();
    render(<App />);
    await screen.findByText('o1');
    await user.click(screen.getByRole('button', { name: /your account/i }));
    await user.click(await screen.findByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('heading', { name: /welcome to swiftdrop/i })).toBeTruthy();
    expect(screen.queryByText('o1')).toBeNull();
    expect(server.callsTo('POST', '/auth/logout')).toHaveLength(1);
  });

  it('goes back to sign-in, with a reason, when the server stops knowing the session', async () => {
    let alive = true;
    board(staff, [order({ id: 'o1', state: 'paid' })], {
      'GET /orders': () => (alive ? { body: { orders: [order({ id: 'o1', state: 'paid' })] } } : { status: 401, body: { error: 'sign in required' } }),
    });
    render(<App />);
    await screen.findByText('o1');
    alive = false; // the next poll (every 3 seconds) finds out
    expect(await screen.findByText('Your session ended. Sign in again.', {}, { timeout: 6000 })).toBeTruthy();
    expect(screen.getByRole('heading', { name: /welcome to swiftdrop/i })).toBeTruthy();
    expect(screen.queryByText('o1')).toBeNull();
  }, 10000);

  it('remembers the appearance chosen', async () => {
    const user = userEvent.setup();
    board();
    render(<App />);
    await screen.findByText('o1');
    await user.click(screen.getByRole('button', { name: /your account/i }));
    await user.click(await screen.findByRole('tab', { name: 'Dark' }));
    expect(document.documentElement.dataset.theme).toBe('dark');
    expect(localStorage.getItem('sd.theme')).toBe('dark');
  });
});

describe('recording a refund done by hand', () => {
  const failed = order({
    id: 'o1', state: 'cancelled', paymentIssue: 'refund_failed',
    payment: { status: 'authorized', amountCents: 1900, attempt: 1, maxAttempts: 3, refundStatus: 'failed' },
  });

  async function open(user: ReturnType<typeof userEvent.setup>, extra: Record<string, Handler> = {}, who = staff) {
    const server = board(who, [failed], { 'GET /orders/o1/payment-history': () => ({ body: { events: [] } }), ...extra });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /order o1/i }));
    return { server, panel: await screen.findByLabelText('Order details') };
  }

  it('opens a form on the first tap and sends nothing until a reference is given', async () => {
    const user = userEvent.setup();
    const { server, panel } = await open(user);
    await user.click(within(panel).getByRole('button', { name: 'Record a refund done by hand' }));
    expect(within(panel).getByLabelText('Reference')).toBeTruthy();
    expect(within(panel).getByText(/does not send any money/)).toBeTruthy();

    await user.click(within(panel).getByRole('button', { name: 'Record a refund done by hand' }));
    expect(await within(panel).findByText('Enter the refund id or reference')).toBeTruthy();
    expect(server.callsTo('POST', '/orders/o1/refund')).toHaveLength(0);
  });

  it('sends the reference and note with the CSRF token, and says it was recorded', async () => {
    const user = userEvent.setup();
    const { server, panel } = await open(user, { 'POST /orders/o1/refund': () => ({ body: {} }) });
    await user.click(within(panel).getByRole('button', { name: 'Record a refund done by hand' }));
    await user.type(within(panel).getByLabelText('Reference'), '  re_hand_1 ');
    await user.type(within(panel).getByLabelText(/Note/), 'paid from the dashboard');
    await user.click(within(panel).getByRole('button', { name: 'Record a refund done by hand' }));

    await waitFor(() => expect(server.callsTo('POST', '/orders/o1/refund')).toHaveLength(1));
    const call = server.callsTo('POST', '/orders/o1/refund')[0];
    expect(call.body).toEqual({ reference: 're_hand_1', note: 'paid from the dashboard' });
    expect(call.headers['X-CSRF-Token']).toBe('csrf-u-staff');
    expect(await screen.findByText('o1: Refund recorded')).toBeTruthy();
  });

  it('puts the server\'s problem with a field next to the form', async () => {
    const user = userEvent.setup();
    const { panel } = await open(user, {
      'POST /orders/o1/refund': () => ({ status: 422, body: { error: 'x', fields: { reference: 'must be at most 200 characters' } } }),
    });
    await user.click(within(panel).getByRole('button', { name: 'Record a refund done by hand' }));
    await user.type(within(panel).getByLabelText('Reference'), 'r');
    await user.click(within(panel).getByRole('button', { name: 'Record a refund done by hand' }));
    expect(await within(panel).findByText('Reference must be at most 200 characters')).toBeTruthy();
  });

  it('says why when the server refuses', async () => {
    const user = userEvent.setup();
    const { panel } = await open(user, {
      'POST /orders/o1/refund': () => ({ status: 409, body: { error: 'the refund of this order is already recorded as done' } }),
    });
    await user.click(within(panel).getByRole('button', { name: 'Record a refund done by hand' }));
    await user.type(within(panel).getByLabelText('Reference'), 'r');
    await user.click(within(panel).getByRole('button', { name: 'Record a refund done by hand' }));
    expect(await screen.findByText('o1: the refund of this order is already recorded as done')).toBeTruthy();
  });

  it('is not offered to a customer, who is only told what happened', async () => {
    const user = userEvent.setup();
    const { panel } = await open(user, {}, customer);
    expect(within(panel).queryByRole('button', { name: /record a refund/i })).toBeNull();
    expect(within(panel).getByText(/contact support/)).toBeTruthy();
  });
});
