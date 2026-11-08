import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { api, onSignedOutBehindOurBack, setSession } from './api';
import { authErrorMessage, fieldErrors } from './lib/logic';
import type { Session, User } from './lib/types';

export type AuthResult = { ok: true } | { ok: false; message?: string; fields?: Record<string, string> };

interface SessionValue {
  /** loading until the first answer from /auth/me */
  status: 'loading' | 'out' | 'in';
  user: User | null;
  /** Why the sign-in screen is showing, when it is not simply that nobody signed in. */
  notice: string;
  signIn(email: string, password: string): Promise<AuthResult>;
  signUp(payload: { email: string; name: string; password: string }): Promise<AuthResult>;
  signOut(): Promise<void>;
  changePassword(currentPassword: string, newPassword: string): Promise<AuthResult>;
}

const Ctx = createContext<SessionValue | null>(null);

export function useSession(): SessionValue {
  const v = useContext(Ctx);
  if (!v) throw new Error('useSession needs a SessionProvider');
  return v;
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<SessionValue['status']>('loading');
  const [user, setUser] = useState<User | null>(null);
  const [notice, setNotice] = useState('');

  const enter = useCallback((s: Session) => {
    setSession(s.csrfToken);
    setUser(s.user);
    setNotice('');
    setStatus('in');
  }, []);

  const leave = useCallback((why: string) => {
    setSession(null);
    setUser(null);
    setNotice(why);
    setStatus('out');
  }, []);

  useEffect(() => {
    onSignedOutBehindOurBack(() => leave('Your session ended. Sign in again.'));
    return () => onSignedOutBehindOurBack(null);
  }, [leave]);

  // Ask who we are. A live cookie goes straight to the app; otherwise the sign-in screen.
  useEffect(() => {
    let cancelled = false;
    api<Session>('GET', '/auth/me').then((res) => {
      if (cancelled) return;
      if (res.ok && res.data?.user) enter(res.data);
      else leave(res.status === 0 ? authErrorMessage(0, null) : '');
    });
    return () => { cancelled = true; };
  }, [enter, leave]);

  const finish = useCallback((res: Awaited<ReturnType<typeof api<Session>>>): AuthResult => {
    if (res.ok && res.data) {
      enter(res.data);
      return { ok: true };
    }
    if (res.status === 422) return { ok: false, fields: fieldErrors(res.data) };
    return { ok: false, message: authErrorMessage(res.status, res.data, res.retryAfter) };
  }, [enter]);

  const value = useMemo<SessionValue>(() => ({
    status, user, notice,
    signIn: async (email, password) => finish(await api<Session>('POST', '/auth/login', { email: email.trim(), password })),
    signUp: async (payload) => finish(await api<Session>('POST', '/auth/signup', payload)),
    signOut: async () => {
      // Even if the server cannot be reached, this screen stops showing the account.
      await api('POST', '/auth/logout');
      leave('');
    },
    changePassword: async (currentPassword, newPassword) => {
      const res = await api('POST', '/auth/password', { currentPassword, newPassword });
      if (res.ok) {
        // a temporary password is now replaced: the rest of the app opens up
        setUser((u) => (u ? { ...u, mustChangePassword: false } : u));
        return { ok: true };
      }
      if (res.status === 403) return { ok: false, fields: { currentPassword: 'That is not your current password' } };
      if (res.status === 422) return { ok: false, fields: fieldErrors(res.data) };
      return { ok: false, message: authErrorMessage(res.status, res.data, res.retryAfter) };
    },
  }), [status, user, notice, finish, leave]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
