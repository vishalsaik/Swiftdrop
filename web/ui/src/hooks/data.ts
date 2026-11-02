import { useCallback, useEffect, useRef, useState } from 'react';
import { api } from '../api';
import { errorMessage, openAlerts } from '../lib/logic';
import type { Alert, Order } from '../lib/types';

export const POLL_MS = 3000;

// Runs fn now and then every POLL_MS while the page is visible and `enabled` is true.
function usePoll(fn: () => Promise<void>, enabled: boolean) {
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => {
    if (!enabled) return;
    void ref.current();
    const tick = () => { if (!document.hidden) void ref.current(); };
    const timer = setInterval(tick, POLL_MS);
    document.addEventListener('visibilitychange', tick);
    return () => { clearInterval(timer); document.removeEventListener('visibilitychange', tick); };
  }, [enabled]);
}

export interface OrdersState {
  orders: Order[];
  /** false until the first answer, so what was already true when the page opened is not announced */
  loaded: boolean;
  online: boolean | null;
  offlineMessage: string;
  refresh(): Promise<void>;
}

export function useOrders(enabled: boolean): OrdersState {
  const [orders, setOrders] = useState<Order[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [online, setOnline] = useState<boolean | null>(null);
  const [offlineMessage, setOfflineMessage] = useState('');
  const inflight = useRef(false);

  const refresh = useCallback(async () => {
    if (inflight.current) return;
    inflight.current = true;
    try {
      const res = await api<{ orders: Order[] | null }>('GET', '/orders?limit=100');
      if (!res.ok) {
        setOnline(false);
        setOfflineMessage(`${errorMessage(res.status, res.data)} Retrying every few seconds.`);
        return;
      }
      setOnline(true);
      setOrders(res.data?.orders ?? []);
      setLoaded(true);
    } finally {
      inflight.current = false;
    }
  }, []);

  // Nothing of the last person stays when someone signs out.
  useEffect(() => {
    if (!enabled) { setOrders([]); setLoaded(false); setOnline(null); setOfflineMessage(''); }
  }, [enabled]);

  usePoll(refresh, enabled);
  return { orders, loaded, online, offlineMessage, refresh };
}

export function useAlerts(enabled: boolean) {
  const [alerts, setAlerts] = useState<Alert[]>([]);

  const refresh = useCallback(async () => {
    const res = await api<{ alerts: Alert[] | null }>('GET', '/alerts');
    if (res.ok) setAlerts(openAlerts(res.data?.alerts));
  }, []);

  useEffect(() => { if (!enabled) setAlerts([]); }, [enabled]);

  usePoll(refresh, enabled);
  return { alerts, refresh };
}
