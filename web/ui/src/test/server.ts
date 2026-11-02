import { vi } from 'vitest';
import type { Order, Role, User } from '../lib/types';

// A fake of the Go server for component tests: fetch is replaced by routes, and every
// call is recorded so a test can say what was sent (and with which headers).

export interface Call { method: string; path: string; headers: Record<string, string>; body: unknown }
export type Handler = (call: Call) => { status?: number; body?: unknown; headers?: Record<string, string> };

export function json(status: number, body?: unknown, headers: Record<string, string> = {}): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });
}

export function mockServer(routes: Record<string, Handler>) {
  const calls: Call[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = (init?.method ?? 'GET').toUpperCase();
    const path = String(input);
    const headers = Object.fromEntries(Object.entries((init?.headers as Record<string, string>) ?? {}));
    const call: Call = { method, path, headers, body: init?.body ? JSON.parse(String(init.body)) : undefined };
    calls.push(call);
    const key = `${method} ${path.split('?')[0]}`;
    const handler = routes[key] ?? Object.entries(routes).find(([k]) => k.endsWith('*') && key.startsWith(k.slice(0, -1)))?.[1];
    if (!handler) return json(404, { error: `no route for ${key}` });
    const out = handler(call);
    return json(out.status ?? 200, out.body, out.headers);
  });
  vi.stubGlobal('fetch', fetchMock);
  return { calls, fetchMock, callsTo: (method: string, path: string) => calls.filter((c) => c.method === method && c.path.split('?')[0] === path) };
}

export const staff: User = { id: 'u-staff', email: 'sam@example.com', name: 'Sam Staff', role: 'staff' };
export const customer: User = { id: 'u-cust', email: 'casey@example.com', name: 'Casey Customer', role: 'customer' };
export const session = (user: User) => ({ user, csrfToken: `csrf-${user.id}` });
export const asRole = (role: Role) => (role === 'staff' ? staff : customer);

export function order(over: Partial<Order> = {}): Order {
  return {
    id: 'o1', customerId: 'u-cust', restaurantId: 'r1', paymentType: 'card', state: 'created', createdAt: new Date().toISOString(),
    items: [{ sku: 'BURGER', name: 'Smash burger', quantity: 2, unitPriceCents: 950 }], ...over,
  };
}
