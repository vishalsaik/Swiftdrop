import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App } from '../App';
import { setSession } from '../api';
import { mockServer, order, session, staff } from '../test/server';

// The libraries draw on a canvas, which jsdom does not have, so they are replaced by
// stand-ins that show what they were given. What is tested is where the effects appear
// and with which settings, not how they look.
vi.mock('thinking-orbs', async () => {
  const React = await import('react');
  return {
    ThinkingOrb: (p: Record<string, unknown>) => React.createElement('canvas', {
      'data-testid': 'orb', 'data-state': p.state, 'data-size': p.size, 'aria-hidden': p['aria-hidden'],
    }),
  };
});
vi.mock('border-beam', async () => {
  const React = await import('react');
  return {
    BorderBeam: (p: { active: boolean; theme: string; children: React.ReactNode }) => React.createElement(
      'div', { 'data-testid': 'beam', 'data-active': String(p.active), 'data-theme-prop': p.theme }, p.children),
  };
});

const confirming = order({
  id: 'wait-1', state: 'payment_pending', paymentIssue: 'confirming',
  payment: { status: 'processing', amountCents: 1900, attempt: 1, maxAttempts: 3 },
});
const refunded = order({
  id: 'done-1', state: 'cancelled', paymentIssue: 'refunded',
  payment: { status: 'authorized', amountCents: 1900, attempt: 1, maxAttempts: 3, refundStatus: 'refunded' },
});

function serve(orders = [confirming, refunded]) {
  mockServer({
    'GET /auth/me': () => ({ body: session(staff) }),
    'GET /orders': () => ({ body: { orders } }),
    'GET /alerts': () => ({ body: { alerts: [] } }),
    'GET /orders/wait-1/payment-history': () => ({ body: { events: [] } }),
    'GET /orders/done-1/payment-history': () => ({ body: { events: [] } }),
  });
}

beforeEach(() => { localStorage.clear(); document.documentElement.dataset.theme = 'light'; });
afterEach(() => { setSession(null); vi.unstubAllGlobals(); });

describe('the thinking orb', () => {
  it('sits beside an order whose payment is being confirmed, and only that one', async () => {
    serve();
    render(<App />);
    const waiting = await screen.findByRole('button', { name: /order wait-1/i });
    const orb = within(waiting).getByTestId('orb');
    expect(orb.getAttribute('data-state')).toBe('connecting');
    expect(orb.getAttribute('data-size')).toBe('20'); // inline with text
    expect(orb.getAttribute('aria-hidden')).toBe('true'); // the label next to it says it already

    const done = await screen.findByRole('button', { name: /order done-1/i });
    expect(within(done).queryByTestId('orb')).toBeNull();
  });
});

describe('the border beam', () => {
  it('runs round the notice while a payment is being confirmed', async () => {
    const user = userEvent.setup();
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /order wait-1/i }));
    const panel = await screen.findByLabelText('Order details');
    const beam = within(panel).getByTestId('beam');
    expect(beam.getAttribute('data-active')).toBe('true');
    expect(beam.getAttribute('data-theme-prop')).toBe('light');
    expect(within(beam).getByLabelText('What the customer sees').getAttribute('aria-busy')).toBe('true');
  });

  it('is off once the waiting is over', async () => {
    const user = userEvent.setup();
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /order done-1/i }));
    const panel = await screen.findByLabelText('Order details');
    expect(within(panel).getByTestId('beam').getAttribute('data-active')).toBe('false');
  });

  it('is told the surface is dark in dark mode', async () => {
    const user = userEvent.setup();
    localStorage.setItem('sd.theme', 'dark');
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /order wait-1/i }));
    const panel = await screen.findByLabelText('Order details');
    expect(within(panel).getByTestId('beam').getAttribute('data-theme-prop')).toBe('dark');
  });

  it('stays off for someone who asks for less motion', async () => {
    const user = userEvent.setup();
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches: query.includes('prefers-reduced-motion'), media: query, addEventListener() {}, removeEventListener() {},
    }));
    serve();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: /order wait-1/i }));
    const panel = await screen.findByLabelText('Order details');
    expect(within(panel).getByTestId('beam').getAttribute('data-active')).toBe('false');
  });
});
