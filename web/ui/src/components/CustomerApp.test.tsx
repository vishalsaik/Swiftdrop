import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, renderHook, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { App, Gate } from '../App';
import { setSession } from '../api';
import { SessionProvider, useSession } from '../session';
import { ToastProvider } from '../toasts';
import { CartProvider, useCart, type CartLine } from '../hooks/cart';
import type { MenuItem, Order, Restaurant } from '../lib/types';
import { home, placeOrder } from '../test/deliver';
import { customer, mockServer, order, session, staff, type Handler } from '../test/server';

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); });

const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString();

function serve(user = customer, orders: Order[] = [], extra: Record<string, Handler> = {}) {
  return mockServer({
    'GET /auth/me': () => ({ body: session(user) }),
    'GET /orders': () => ({ body: { orders } }),
    'GET /alerts': () => ({ body: { alerts: [] } }),
    'POST /auth/logout': () => ({ status: 204 }),
    ...extra,
  });
}

const tabs = () => within(screen.getByRole('navigation', { name: 'Sections' }));

describe('what a customer sees', () => {
  it('has three tabs, Food first, and none of the dispatch dashboard', async () => {
    const server = serve(customer, [order({ id: 'o1', state: 'paid' })]);
    render(<App />);
    expect(await screen.findByRole('heading', { name: /good (morning|afternoon|evening|night), casey/i })).toBeTruthy();
    expect(tabs().getAllByRole('button').map((b) => b.textContent)).toEqual(['Food', 'Orders', 'Account']);
    expect(tabs().getByRole('button', { name: 'Food' }).getAttribute('aria-current')).toBe('page');

    // the staff tools are not drawn at all, not just hidden
    expect(screen.queryByLabelText('Live totals')).toBeNull();
    expect(screen.queryByRole('tablist', { name: 'Stage' })).toBeNull();
    expect(screen.queryByRole('navigation', { name: 'Main' })).toBeNull();
    for (const name of [/alerts/i, /^menu$/i, /^people$/i, /sample/i, /^new order$/i, /all orders/i]) {
      expect(screen.queryAllByRole('button', { name })).toHaveLength(0);
    }
    expect(screen.queryByText(/message to customer/i)).toBeNull();
    // and nothing staff-only is asked of the server
    await waitFor(() => expect(server.callsTo('GET', '/orders').length).toBeGreaterThan(0));
    expect(server.calls.filter((c) => /^\/(alerts|users)/.test(c.path) || c.path.includes('all=true'))).toHaveLength(0);
  });

  it('greets someone with no name as "there"', async () => {
    serve({ ...customer, name: '   ' });
    render(<App />);
    expect(await screen.findByRole('heading', { name: /, there$/ })).toBeTruthy();
  });

  it('lists their orders newest first, with no stage groups and no customer column', async () => {
    const user = userEvent.setup();
    serve(customer, [
      order({ id: 'older', state: 'delivered', createdAt: minutesAgo(90) }),
      order({ id: 'newest', state: 'created', createdAt: minutesAgo(1) }),
      order({ id: 'middle', state: 'preparing', createdAt: minutesAgo(30) }),
    ]);
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Orders' }));
    expect(tabs().getByRole('button', { name: 'Orders' }).getAttribute('aria-current')).toBe('page');
    const rows = await screen.findAllByRole('button', { name: /^Order / });
    expect(rows.map((r) => r.getAttribute('aria-label')?.split(',')[0])).toEqual(['Order newest', 'Order middle', 'Order older']);
    expect(screen.queryByRole('region', { name: /awaiting|kitchen|on the road/i })).toBeNull();
    expect(screen.queryByText(/u-cust/)).toBeNull();
  });

  it('keeps an order without a date at the end instead of failing', async () => {
    const user = userEvent.setup();
    serve(customer, [order({ id: 'undated', createdAt: undefined }), order({ id: 'dated', createdAt: minutesAgo(5) })]);
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Orders' }));
    expect(await screen.findByRole('button', { name: /order undated/i })).toBeTruthy();
    expect(screen.getByRole('button', { name: /order dated/i })).toBeTruthy();
  });

  it('opens an order, and closes it with Escape or the scrim', async () => {
    const user = userEvent.setup();
    // the server would answer with a history, so its absence is the view's choice, not an empty answer
    const server = serve(customer, [order({ id: 'o1', state: 'paid', payment: { status: 'authorized', amountCents: 1900, attempt: 1, maxAttempts: 3 } })], {
      'GET /orders/o1/payment-history': () => ({ body: { events: [{ kind: 'attempt', at: minutesAgo(1) }] } }),
    });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Orders' }));
    await user.click(await screen.findByRole('button', { name: /order o1/i }));
    const panel = screen.getByLabelText('Order details');
    expect(panel.getAttribute('aria-hidden')).toBe('false');
    expect(within(panel).queryByText('Payment history')).toBeNull();
    expect(server.callsTo('GET', '/orders/o1/payment-history')).toHaveLength(0);

    fireEvent.keyDown(document, { key: 'Escape' });
    await waitFor(() => expect(screen.getByLabelText('Order details', { selector: 'aside' }).getAttribute('aria-hidden')).toBe('true'));

    await user.click(screen.getByRole('button', { name: /order o1/i }));
    fireEvent.click(document.querySelector('.scrim') as Element);
    await waitFor(() => expect(screen.getByLabelText('Order details', { selector: 'aside' }).getAttribute('aria-hidden')).toBe('true'));
  });

  it('says so while the orders are loading, and when the server cannot be reached', async () => {
    const user = userEvent.setup();
    let answer: Handler = () => ({ status: 503, body: { error: 'down for a moment' } });
    serve(customer, [], { 'GET /orders': (c) => answer(c) });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Orders' }));
    expect(screen.getByText('Loading orders', { selector: 'p' })).toBeTruthy();
    expect((await screen.findByRole('alert')).textContent).toMatch(/down for a moment.*Retrying/);
    answer = () => ({ body: { orders: [] } });
  });
});

describe('placing an order from the Food tab', () => {
  const kitchen: Restaurant = { id: 'r1', name: 'Corner Kitchen', active: true };
  const burger: MenuItem = { id: 'burger', restaurantId: 'r1', name: 'Smash burger', priceCents: 950, available: true };

  it('takes the customer to Orders with the new order open', async () => {
    const user = userEvent.setup();
    const placed: Order[] = [];
    const server = serve(customer, [], {
      'GET /orders': () => ({ body: { orders: placed } }),
      'GET /addresses': () => ({ body: { addresses: [home] } }),
      'GET /restaurants': () => ({ body: { restaurants: [kitchen] } }),
      'GET /restaurants/r1/menu': () => ({ body: { restaurant: kitchen, items: [burger] } }),
      'POST /orders': (c) => {
        const o = order({ id: (c.body as { id: string }).id, state: 'created' });
        placed.push(o);
        return { status: 201, body: { order: o } };
      },
    });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Start an order' }));
    await user.click(await screen.findByRole('button', { name: 'More Smash burger' }));
    await placeOrder(user);

    await waitFor(() => expect(server.callsTo('POST', '/orders')).toHaveLength(1));
    const id = (server.callsTo('POST', '/orders')[0].body as { id: string }).id;
    expect(await screen.findByRole('heading', { name: 'Your orders' })).toBeTruthy();
    const panel = screen.getByLabelText('Order details', { selector: 'aside' });
    await waitFor(() => expect(panel.getAttribute('aria-hidden')).toBe('false'));
    expect(within(panel).getByText(id)).toBeTruthy();
  });
});

describe('the cart button', () => {
  it('says the cart is empty, and its Find food goes back to the Food tab', async () => {
    const user = userEvent.setup();
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Orders' }));
    await user.click(screen.getByRole('button', { name: 'Cart, empty' }));
    const page = within(await screen.findByRole('region', { name: 'Your cart' }));
    expect(page.getByText('Your cart is empty')).toBeTruthy();
    await user.click(page.getByRole('button', { name: 'Find food' }));
    expect(screen.queryByRole('region', { name: 'Your cart' })).toBeNull();
    expect(tabs().getByRole('button', { name: 'Food' }).getAttribute('aria-current')).toBe('page');
  });

  it('opens a page, not a sheet, and Back closes it', async () => {
    const user = userEvent.setup();
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Cart, empty' }));
    expect(screen.queryByRole('dialog', { name: 'Cart' })).toBeNull();
    await user.click(within(await screen.findByRole('region', { name: 'Your cart' })).getByRole('button', { name: 'Back' }));
    expect(screen.queryByRole('region', { name: 'Your cart' })).toBeNull();
    expect(screen.getByText('What would you like to eat?')).toBeTruthy();
  });
});

describe('useCart', () => {
  const lines: CartLine[] = [
    { menuItemId: 'burger', name: 'Smash burger', unitPriceCents: 950, quantity: 2 },
    { menuItemId: 'fries', name: 'Fries', unitPriceCents: 400, quantity: 1 },
  ];
  const wrapper = ({ children }: { children: ReactNode }) => <CartProvider userId="u-cust">{children}</CartProvider>;

  it('counts the things in the cart, not the lines, and empties', () => {
    localStorage.setItem('swiftdrop.cart.u-cust', JSON.stringify({ restaurantId: 'r1', restaurantName: 'Corner Kitchen', lines }));
    const { result } = renderHook(() => useCart(), { wrapper });
    expect(result.current.count).toBe(3);
    expect(result.current.lines).toHaveLength(2);
    act(() => result.current.clear());
    expect(result.current.count).toBe(0);
    expect(result.current.lines).toEqual([]);
    expect(result.current.restaurantId).toBeNull();
  });

  it('starts empty', () => {
    const { result } = renderHook(() => useCart(), { wrapper: ({ children }) => <CartProvider userId="u-cust">{children}</CartProvider> });
    expect(result.current.count).toBe(0);
  });

  it('needs a provider', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    expect(() => renderHook(() => useCart())).toThrow('useCart needs a CartProvider');
  });
});

describe('the account', () => {
  it('is a tab with the account settings, where a password can be changed', async () => {
    const user = userEvent.setup();
    const server = serve(customer, [], { 'POST /auth/password': () => ({ status: 204 }) });
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Account' }));
    const page = within(screen.getByRole('region', { name: 'Account' }));
    expect(page.getByText('casey@example.com')).toBeTruthy();
    await user.type(page.getByLabelText('Current'), 'old password here');
    await user.type(page.getByLabelText('New'), 'a much better one');
    await user.click(page.getByRole('button', { name: 'Change password' }));
    await waitFor(() => expect(server.callsTo('POST', '/auth/password')).toHaveLength(1));
    expect(server.callsTo('POST', '/auth/password')[0].headers['X-CSRF-Token']).toBe('csrf-u-cust');
    expect(await screen.findByText('Password changed. Other devices were signed out.')).toBeTruthy();
  });

  it('changes the appearance from the tab', async () => {
    const user = userEvent.setup();
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Account' }));
    await user.click(screen.getByRole('tab', { name: 'Dark' }));
    expect(document.documentElement.dataset.theme).toBe('dark');
  });

  it('opens as a sheet from the header button, and signs out from there', async () => {
    const user = userEvent.setup();
    const server = serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Your account, Casey Customer' }));
    const sheet = within(await screen.findByRole('dialog', { name: 'Account' }));
    await user.click(sheet.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('heading', { name: /welcome to swiftdrop/i })).toBeTruthy();
    expect(server.callsTo('POST', '/auth/logout')).toHaveLength(1);
    expect(screen.queryByRole('navigation', { name: 'Sections' })).toBeNull();
  });
});

describe('staff are unchanged', () => {
  it('still get the dispatch dashboard and none of the customer view', async () => {
    serve(staff, [order({ id: 'o1', state: 'paid', customerId: 'someone' })]);
    render(<App />);
    expect(await screen.findByLabelText('Live totals')).toBeTruthy();
    expect(screen.getByRole('navigation', { name: 'Main' })).toBeTruthy();
    expect(screen.getAllByRole('button', { name: /new order/i }).length).toBeGreaterThan(0);
    expect(await screen.findByText(/someone/)).toBeTruthy();
    expect(screen.queryByRole('navigation', { name: 'Sections' })).toBeNull();
    expect(screen.queryByRole('button', { name: /^cart/i })).toBeNull();
  });
});

describe('switching accounts', () => {
  // signIn while already signed in replaces the user without the sign-in screen in between, so the
  // customer view is not unmounted: only its key (the user's id) makes it start afresh.
  function Switcher() {
    const { signIn } = useSession();
    return <button type="button" onClick={() => void signIn('bea@example.com', 'a long enough phrase')}>switch account</button>;
  }

  it('starts the next customer on the Food tab with no order open, nothing left of the last', async () => {
    const user = userEvent.setup();
    const bea = { ...customer, id: 'u-bea', email: 'bea@example.com', name: 'Bea Buyer' };
    serve(customer, [order({ id: 'o1', state: 'created' })], { 'POST /auth/login': () => ({ body: session(bea) }) });
    render(<SessionProvider><ToastProvider><Gate /><Switcher /></ToastProvider></SessionProvider>);
    expect(await screen.findByRole('heading', { name: /, casey$/i })).toBeTruthy();

    // casey is on the Orders tab with an order open
    await user.click(tabs().getByRole('button', { name: 'Orders' }));
    await user.click(await screen.findByRole('button', { name: /order o1/i }));
    expect(within(await screen.findByLabelText('Order details')).getByText('o1')).toBeTruthy();

    await user.click(screen.getByRole('button', { name: 'switch account' }));

    expect(await screen.findByRole('heading', { name: /, bea$/i })).toBeTruthy();
    expect(tabs().getByRole('button', { name: 'Food' }).getAttribute('aria-current')).toBe('page');
    expect(screen.getByLabelText('Order details').getAttribute('aria-hidden')).toBe('true');
    expect(within(screen.getByLabelText('Order details')).queryByText('o1')).toBeNull();
  });
});
