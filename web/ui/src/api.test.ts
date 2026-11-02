import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, onSignedOutBehindOurBack, setSession } from './api';
import { mockServer } from './test/server';

afterEach(() => {
  setSession(null);
  onSignedOutBehindOurBack(null);
  vi.unstubAllGlobals();
});

describe('api', () => {
  it('sends the CSRF token with every change and never with a read', async () => {
    const server = mockServer({ 'GET /orders': () => ({ body: {} }), 'POST /orders/o1/cancel': () => ({ body: {} }) });
    setSession('tok-123');
    await api('GET', '/orders');
    await api('POST', '/orders/o1/cancel');
    expect(server.calls[0].headers['X-CSRF-Token']).toBeUndefined();
    expect(server.calls[1].headers['X-CSRF-Token']).toBe('tok-123');
  });

  it('sends no token when nobody is signed in', async () => {
    const server = mockServer({ 'POST /auth/login': () => ({ body: {} }) });
    await api('POST', '/auth/login', { email: 'a@b.co', password: 'x' });
    expect(server.calls[0].headers['X-CSRF-Token']).toBeUndefined();
    expect(server.calls[0].headers['Content-Type']).toBe('application/json');
  });

  it('answers what the server said, and the wait of a Retry-After', async () => {
    mockServer({ 'POST /auth/login': () => ({ status: 429, body: { error: 'slow down' }, headers: { 'Retry-After': '600' } }) });
    const res = await api('POST', '/auth/login', {});
    expect(res).toMatchObject({ status: 429, ok: false, data: { error: 'slow down' }, retryAfter: 600 });
  });

  it('says status 0 when the server cannot be reached', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('network'); }));
    expect(await api('GET', '/orders')).toEqual({ status: 0, ok: false, data: null, retryAfter: 0 });
  });

  it('tells the app when a signed-in call is answered 401, but not for sign-in calls', async () => {
    mockServer({
      'GET /orders': () => ({ status: 401, body: { error: 'sign in required' } }),
      'POST /auth/login': () => ({ status: 401, body: { error: 'nope' } }),
    });
    const ended = vi.fn();
    onSignedOutBehindOurBack(ended);

    await api('GET', '/orders'); // nobody is signed in as far as this page knows
    expect(ended).not.toHaveBeenCalled();

    setSession('tok');
    await api('POST', '/auth/login', {}); // a wrong password is not a session ending
    expect(ended).not.toHaveBeenCalled();
    await api('GET', '/orders');
    expect(ended).toHaveBeenCalledTimes(1);
  });
});
