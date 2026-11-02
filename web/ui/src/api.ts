// The one place that talks to the server. It sends the session's CSRF token with
// every change, and says so when the server stops recognising the session.

export interface ApiResult<T = unknown> {
  status: number;
  ok: boolean;
  data: T | null;
  /** Seconds from a Retry-After header, or 0. */
  retryAfter: number;
}

let csrf = '';
let signedIn = false;
let onSessionEnded: (() => void) | null = null;

/** Called by the session when someone signs in or out, so the helper knows what to send. */
export function setSession(token: string | null): void {
  csrf = token ?? '';
  signedIn = token !== null;
}

/** What to do when a call that needs a session is answered 401 (it expired or ended elsewhere). */
export function onSignedOutBehindOurBack(fn: (() => void) | null): void {
  onSessionEnded = fn;
}

export async function api<T = unknown>(method: string, path: string, body?: unknown): Promise<ApiResult<T>> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET' && csrf) headers['X-CSRF-Token'] = csrf;
  try {
    const res = await fetch(path, {
      method, headers, credentials: 'same-origin', body: body === undefined ? undefined : JSON.stringify(body),
    });
    let data: T | null = null;
    try { data = (await res.json()) as T; } catch { /* an empty or non-JSON body */ }
    const retryAfter = Number(res.headers.get('Retry-After')) || 0;
    if (res.status === 401 && signedIn && !path.startsWith('/auth/')) onSessionEnded?.();
    return { status: res.status, ok: res.ok, data, retryAfter };
  } catch {
    return { status: 0, ok: false, data: null, retryAfter: 0 };
  }
}
