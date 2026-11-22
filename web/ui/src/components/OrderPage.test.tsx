import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from '../App';
import { setSession } from '../api';
import type { Order, PaymentView } from '../lib/types';
import { customer, mockServer, order, session, type Handler } from '../test/server';

// The customer's tracker: the Orders list and an order's page.

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); vi.useRealTimers(); });

const paid = (over: Partial<PaymentView> = {}): PaymentView => ({ status: 'authorized', attempt: 1, maxAttempts: 3, amountCents: 1900, ...over });

function serve(orders: Order[] | (() => Order[]), extra: Record<string, Handler> = {}) {
  const list = typeof orders === 'function' ? orders : () => orders;
  return mockServer({
    'GET /auth/me': () => ({ body: session(customer) }),
    'GET /orders': () => ({ body: { orders: list() } }),
    ...extra,
  });
}

const page = () => within(screen.getByRole('region', { name: 'Order details' }));

async function openOrder(user: ReturnType<typeof userEvent.setup>, id = 'o1') {
  render(<App />);
  await user.click(await screen.findByRole('button', { name: 'Orders' }));
  await user.click(await screen.findByRole('button', { name: new RegExp(`^Order ${id},`) }));
  return page();
}

function steps() {
  return page().getAllByRole('listitem').filter((li) => li.closest('.journey')).map((li) => [li.querySelector('.label')?.textContent, li.className]);
}

describe('the Orders list', () => {
  it('gives each order a status line in the customer\'s words, its items, total and money stamp', async () => {
    const user = userEvent.setup();
    serve([
      order({ id: 'o1', state: 'preparing', payment: paid() }),
      order({ id: 'o2', state: 'cancelled', paymentIssue: 'refund_pending', payment: paid({ refundStatus: 'refunding' }) }),
    ]);
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Orders' }));
    const kitchen = await screen.findByRole('button', { name: 'Order o1, Being prepared in the kitchen' });
    expect(within(kitchen).getByText('Being prepared in the kitchen')).toBeTruthy();
    expect(within(kitchen).getByText('2 x Smash burger')).toBeTruthy();
    expect(within(kitchen).getByText('$19.00')).toBeTruthy();
    expect(within(kitchen).getByText('Paid')).toBeTruthy();
    const refund = screen.getByRole('button', { name: 'Order o2, Cancelled. Your refund is on its way' });
    expect(within(refund).getByText('Refund on its way')).toBeTruthy();
    // the list says nothing staff-like: no payment type column, no customer id
    expect(screen.queryByText(/card \//)).toBeNull();
    expect(screen.queryByText(/u-cust/)).toBeNull();
  });
});

describe('an order\'s page', () => {
  it('shows the status, the items and total, the delivery address and the money, and nothing of staff', async () => {
    const user = userEvent.setup();
    serve([order({
      id: 'o1', state: 'paid', payment: paid(), restaurantId: 'r-secret', assignedDriverId: 'driver-123456789', driverName: 'Ana',
      items: [{ sku: 'A', name: 'Pad thai', quantity: 2, unitPriceCents: 1290 }, { sku: 'B', name: 'Spring rolls', quantity: 1, unitPriceCents: 850 }],
      deliveryAddress: { label: 'Home', line1: '12 100 Feet Road', city: 'Bengaluru', landmark: 'Opposite the park', latitude: 12.97, longitude: 77.64 },
    })]);
    const p = await openOrder(user);
    expect(p.getByRole('heading', { name: 'o1' })).toBeTruthy();
    expect(p.getByRole('status').textContent).toBe('Paid. Waiting for the restaurant to accept it');
    expect(p.getByText('2 x Pad thai')).toBeTruthy();
    expect(p.getByText('$25.80')).toBeTruthy();
    expect(p.getByText('$34.30')).toBeTruthy();
    expect(p.getByText('Home:')).toBeTruthy();
    expect(p.getByText(/12 100 Feet Road/)).toBeTruthy();
    expect(p.getByText('Landmark: Opposite the park')).toBeTruthy();
    expect(p.getByText('Paid $19.00 with your card.')).toBeTruthy();
    // no restaurant id, no driver id, no customer id, no payment history
    expect(p.queryByText(/r-secret|driver-1|u-cust/)).toBeNull();
    expect(p.queryByText('Payment history')).toBeNull();
  });

  it('says so when an order has no delivery address', async () => {
    const user = userEvent.setup();
    serve([order({ id: 'o1', state: 'delivered', payment: paid() })]);
    const p = await openOrder(user);
    expect(p.getByText('No delivery address on this order.')).toBeTruthy();
    expect(p.getByText('Nothing for you to do on this order right now.')).toBeTruthy();
    expect(p.queryAllByRole('button').map((b) => b.textContent)).toEqual([' Your orders']);
  });

  it.each([
    ['created', 'card', [['Order placed', 'current'], ['Paid', 'todo'], ['Being prepared', 'todo'], ['On the way', 'todo'], ['Delivered', 'todo']]],
    ['payment_pending', 'card', [['Order placed', 'current'], ['Paid', 'todo'], ['Being prepared', 'todo'], ['On the way', 'todo'], ['Delivered', 'todo']]],
    ['paid', 'cash', [['Order placed', 'done'], ['Confirmed', 'current'], ['Being prepared', 'todo'], ['On the way', 'todo'], ['Delivered', 'todo']]],
    ['preparing', 'card', [['Order placed', 'done'], ['Paid', 'done'], ['Being prepared', 'current'], ['On the way', 'todo'], ['Delivered', 'todo']]],
    ['picked_up', 'card', [['Order placed', 'done'], ['Paid', 'done'], ['Being prepared', 'done'], ['On the way', 'current'], ['Delivered', 'todo']]],
    ['delivered', 'card', [['Order placed', 'done'], ['Paid', 'done'], ['Being prepared', 'done'], ['On the way', 'done'], ['Delivered', 'done']]],
    ['cancelled', 'card', [['Order placed', 'done'], ['Paid', 'skipped'], ['Being prepared', 'skipped'], ['On the way', 'skipped'], ['Cancelled', 'stopped']]],
  ] as const)('draws the timeline of a %s order (%s)', async (state, paymentType, want) => {
    const user = userEvent.setup();
    serve([order({ id: 'o1', state, paymentType, payment: state === 'created' || state === 'cancelled' ? undefined : paid() })]);
    await openOrder(user);
    expect(steps()).toEqual(want);
    const current = page().getAllByRole('listitem').filter((li) => li.getAttribute('aria-current') === 'step');
    expect(current.map((li) => li.querySelector('.label')?.textContent)).toEqual(want.filter(([, s]) => s === 'current').map(([l]) => l));
  });

  it('marks a cancelled order that was paid as having been paid, and says the refund is on its way', async () => {
    const user = userEvent.setup();
    serve([order({ id: 'o1', state: 'cancelled', paymentIssue: 'refund_pending', payment: paid({ refundStatus: 'refunding' }) })]);
    const p = await openOrder(user);
    expect(steps()[1]).toEqual(['Paid', 'done']);
    expect(p.getByRole('status').textContent).toBe('Cancelled. Your refund is on its way');
    expect(p.getByRole('region', { name: 'About your payment' }).textContent).toMatch(/Your money is on its way back/);
    expect(p.getByText('You paid $19.00 with your card. It is on its way back to your card.')).toBeTruthy();
  });

  it('notes ready food on the kitchen step', async () => {
    const user = userEvent.setup();
    serve([order({ id: 'o1', state: 'preparing', payment: paid(), foodReady: new Date().toISOString() })]);
    const p = await openOrder(user);
    expect(p.getByText(/ready, waiting for a driver/)).toBeTruthy();
    expect(p.getByRole('status').textContent).toBe('Ready. Waiting for a driver');
  });

  it.each([
    ['refunded', 'refunded', 'Cancelled and refunded', /refund of \$19\.00 has been sent/, 'It was refunded to your card.'],
    ['refund_failed', 'failed', 'Cancelled. Support will return your money', /couldn't return your money automatically/, 'support will return it'],
  ])('reads a %s payment as sentences', async (issue, refundStatus, status, notice, sentence) => {
    const user = userEvent.setup();
    serve([order({ id: 'o1', state: 'cancelled', paymentIssue: issue, payment: paid({ refundStatus }) })]);
    const p = await openOrder(user);
    expect(p.getByRole('status').textContent).toBe(status);
    expect(p.getByRole('region', { name: 'About your payment' }).textContent).toMatch(notice);
    expect(p.getByText(new RegExp(sentence))).toBeTruthy();
    expect(p.queryByRole('button', { name: /record a refund/i })).toBeNull(); // staff's tool
  });

  it('after a decline says how many tries are left and offers to try again', async () => {
    const user = userEvent.setup();
    serve([order({ id: 'o1', state: 'created', paymentIssue: 'declined', payment: paid({ status: 'declined', reason: 'Your card has insufficient funds.' }) })]);
    const p = await openOrder(user);
    expect(p.getByRole('status').textContent).toBe('Payment declined. You can try again');
    expect(p.getByRole('region', { name: 'About your payment' }).textContent).toMatch(/insufficient funds.*2 attempts left/);
    expect(p.getByText('Your last payment was declined (attempt 1 of 3). You were not charged. You can try 2 more times.')).toBeTruthy();
    expect(p.getByRole('button', { name: 'Try payment again' })).toBeTruthy();
    expect(p.getByText(/Attempt 2 of 3\. The last decline cancels the order\./)).toBeTruthy();
  });

  it('while a person checks the payment, the retry is greyed out and says why', async () => {
    const user = userEvent.setup();
    serve([order({ id: 'o1', state: 'payment_pending', paymentIssue: 'confirming', payment: paid({ status: 'needs_review' }) })]);
    const p = await openOrder(user);
    expect((p.getByRole('button', { name: 'Retry payment' }) as HTMLButtonElement).disabled).toBe(true);
    expect(p.getByText(/A person is checking this payment/)).toBeTruthy();
    expect(p.getByText('A person is checking your payment of $19.00. You are never charged twice.')).toBeTruthy();
  });
});

describe('acting on an order from its page', () => {
  it('pays by card: the first tap opens the method, the second sends it', async () => {
    const user = userEvent.setup();
    let state = 'created';
    const server = serve(() => [order({ id: 'o1', state, payment: state === 'paid' ? paid() : undefined })], {
      'POST /orders/o1/pay': () => { state = 'paid'; return { body: {} }; },
    });
    const p = await openOrder(user);
    await user.click(p.getByRole('button', { name: 'Pay now' }));
    expect(server.callsTo('POST', '/orders/o1/pay')).toHaveLength(0);
    const field = p.getByLabelText('Payment method id');
    await user.clear(field);
    await user.type(field, 'pm_card_mastercard');
    await user.click(p.getByRole('button', { name: 'Pay now' }));
    await waitFor(() => expect(server.callsTo('POST', '/orders/o1/pay')).toHaveLength(1));
    expect(server.callsTo('POST', '/orders/o1/pay')[0].body).toEqual({ paymentMethodId: 'pm_card_mastercard' });
    expect(server.callsTo('POST', '/orders/o1/pay')[0].headers['X-CSRF-Token']).toBe('csrf-u-cust');
    expect(await screen.findByText('o1: Payment taken')).toBeTruthy();
    // the page reloads the order and moves on
    await waitFor(() => expect(page().getByRole('status').textContent).toBe('Paid. Waiting for the restaurant to accept it'));
    expect(page().queryByLabelText('Payment method id')).toBeNull();
  });

  it('confirms cash on delivery in one tap with nothing typed', async () => {
    const user = userEvent.setup();
    const server = serve([order({ id: 'o1', state: 'created', paymentType: 'cash' })], { 'POST /orders/o1/pay': () => ({ body: {} }) });
    const p = await openOrder(user);
    expect(p.getByText(/Cash on delivery: \$19\.00\. Press Pay now/)).toBeTruthy();
    await user.click(p.getByRole('button', { name: 'Pay now' }));
    await waitFor(() => expect(server.callsTo('POST', '/orders/o1/pay')).toHaveLength(1));
    expect(server.callsTo('POST', '/orders/o1/pay')[0].body).toBeUndefined();
  });

  it('says why a payment was declined and how many tries are left', async () => {
    const user = userEvent.setup();
    serve([order({ id: 'o1', state: 'created' })], {
      'POST /orders/o1/pay': () => ({ status: 402, body: { error: 'card declined', canRetry: true, attemptsLeft: 1 } }),
    });
    const p = await openOrder(user);
    await user.click(p.getByRole('button', { name: 'Pay now' }));
    await user.click(p.getByRole('button', { name: 'Pay now' }));
    expect(await screen.findByText('o1: card declined Try another method (1 attempt left).')).toBeTruthy();
  });

  it('cancels only on the second tap, and says a charged order is refunded', async () => {
    const user = userEvent.setup();
    const server = serve([order({ id: 'o1', state: 'preparing', payment: paid() })], { 'POST /orders/o1/cancel': () => ({ body: {} }) });
    const p = await openOrder(user);
    expect(p.queryByRole('button', { name: /accept|ready|pick up|deliver/i })).toBeNull(); // the kitchen's and the driver's
    await user.click(p.getByRole('button', { name: 'Cancel and refund' }));
    expect(server.callsTo('POST', '/orders/o1/cancel')).toHaveLength(0);
    expect(p.getByText('You were charged. The refund starts on its own.')).toBeTruthy();
    await user.click(p.getByRole('button', { name: 'Confirm: cancel and refund' }));
    await waitFor(() => expect(server.callsTo('POST', '/orders/o1/cancel')).toHaveLength(1));
    expect(await screen.findByText('o1: Order cancelled')).toBeTruthy();
  });

  it('forgets a first cancel tap after a few seconds', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const server = serve([order({ id: 'o1', state: 'created' })], { 'POST /orders/o1/cancel': () => ({ body: {} }) });
    const p = await openOrder(user);
    await user.click(p.getByRole('button', { name: 'Cancel order' }));
    expect(p.getByRole('button', { name: 'Confirm: cancel order' })).toBeTruthy();
    await vi.advanceTimersByTimeAsync(4100);
    expect(await p.findByRole('button', { name: 'Cancel order' })).toBeTruthy();
    expect(server.callsTo('POST', '/orders/o1/cancel')).toHaveLength(0);
  });

  it('shows the server\'s words when a cancel is refused', async () => {
    const user = userEvent.setup();
    serve([order({ id: 'o1', state: 'created' })], { 'POST /orders/o1/cancel': () => ({ status: 409, body: { error: 'order can no longer be cancelled' } }) });
    const p = await openOrder(user);
    await user.click(p.getByRole('button', { name: 'Cancel order' }));
    await user.click(p.getByRole('button', { name: 'Confirm: cancel order' }));
    expect(await screen.findByText('o1: order can no longer be cancelled')).toBeTruthy();
  });
});

describe('the page follows the order', () => {
  it('moves on by itself when a poll finds the order further along', async () => {
    const user = userEvent.setup();
    let state = 'preparing';
    serve(() => [order({ id: 'o1', state, payment: paid() })]);
    const p = await openOrder(user);
    expect(p.getByRole('status').textContent).toBe('Being prepared in the kitchen');
    state = 'picked_up';
    await waitFor(() => expect(page().getByRole('status').textContent).toBe('On its way to you'), { timeout: 6000 });
    expect(steps()[3]).toEqual(['On the way', 'current']);
  }, 10000);

  it('says it cannot find an order the list no longer has, and goes back', async () => {
    const user = userEvent.setup();
    let orders = [order({ id: 'o1', state: 'created' })];
    serve(() => orders);
    const p = await openOrder(user);
    orders = [];
    expect(await p.findByRole('heading', { name: "We can't find this order" }, { timeout: 6000 })).toBeTruthy();
    await user.click(p.getByRole('button', { name: 'Back to your orders' }));
    expect(await screen.findByRole('heading', { name: 'Your orders' })).toBeTruthy();
  }, 10000);
});
