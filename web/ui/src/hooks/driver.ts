import { useCallback, useEffect, useRef, useState } from 'react';
import { api } from '../api';
import { DRIVER_LOCATION_MS, driverLocationProblem, errorMessage } from '../lib/logic';
import type { Driver, DriverJob, DriverOffer } from '../lib/types';
import { useToast } from '../toasts';
import { usePoll } from './data';
import { currentPosition } from './location';

// Everything a driver's screen knows: whether they are online, the order they are being offered, the delivery
// they are doing. It is read by polling (every few seconds, like the customer's orders), and while the driver
// is online their position is sent every half minute. The position is asked for only when they press Go
// online (and then each time it is sent), never before.

export interface DriverWork {
  driver: Driver | null;
  offer: DriverOffer | null;
  /** when the offer was read, by this browser's clock: its countdown runs from here, never from a time of day */
  offerSeenAt: number;
  job: DriverJob | null;
  loaded: boolean;
  /** false when the last read failed */
  reachable: boolean | null;
  /** why the driver cannot be seen where they are, if they cannot */
  problem: string;
  busy: boolean;
  goOnline(): Promise<void>;
  goOffline(): Promise<void>;
  accept(): Promise<void>;
  decline(): Promise<void>;
  step(action: 'pickup' | 'deliver'): Promise<void>;
}

export function useDriverWork(): DriverWork {
  const toast = useToast();
  const [driver, setDriver] = useState<Driver | null>(null);
  const [offer, setOffer] = useState<DriverOffer | null>(null);
  const [offerSeenAt, setOfferSeenAt] = useState(0);
  const [job, setJob] = useState<DriverJob | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [reachable, setReachable] = useState<boolean | null>(null);
  const [problem, setProblem] = useState('');
  const [busy, setBusy] = useState(false);
  const inflight = useRef(false);

  const refresh = useCallback(async () => {
    if (inflight.current) return;
    inflight.current = true;
    try {
      const [me, offers, jobs] = await Promise.all([
        api<{ driver: Driver }>('GET', '/driver/me'),
        api<{ offer: DriverOffer | null }>('GET', '/driver/offers'),
        api<{ job: DriverJob | null }>('GET', '/driver/job'),
      ]);
      if (!me.ok || !offers.ok || !jobs.ok) { setReachable(false); return; }
      setReachable(true);
      setDriver(me.data?.driver ?? null);
      const current = jobs.data?.job ?? null;
      setJob(current);
      // a driver on a delivery is not shown another order
      setOffer(current ? null : offers.data?.offer ?? null);
      setOfferSeenAt(Date.now());
      setLoaded(true);
    } finally {
      inflight.current = false;
    }
  }, []);

  usePoll(refresh, true);

  // While online, say where they are every half minute. (Going online sends the first one.)
  const online = driver?.online ?? false;
  useEffect(() => {
    if (!online) return;
    let alive = true;
    const send = async () => {
      const pos = await currentPosition();
      if (!alive) return;
      if (!pos.ok) { setProblem(`We cannot see where you are, so you may not be offered orders. ${driverLocationProblem(pos.code)}`); return; }
      setProblem('');
      await api('POST', '/driver/location', { latitude: pos.position.lat, longitude: pos.position.lng });
    };
    const timer = setInterval(() => void send(), DRIVER_LOCATION_MS);
    return () => { alive = false; clearInterval(timer); };
  }, [online]);

  const goOnline = useCallback(async () => {
    setBusy(true);
    setProblem('');
    try {
      // They cannot be offered orders without a place, so the browser is asked first, and nothing changes if it says no.
      const pos = await currentPosition();
      if (!pos.ok) { setProblem(driverLocationProblem(pos.code)); return; }
      const on = await api('POST', '/driver/online');
      if (!on.ok) { toast(errorMessage(on.status, on.data), 'bad'); return; }
      await api('POST', '/driver/location', { latitude: pos.position.lat, longitude: pos.position.lng });
      await refresh();
    } finally {
      setBusy(false);
    }
  }, [refresh, toast]);

  const goOffline = useCallback(async () => {
    setBusy(true);
    try {
      const off = await api('POST', '/driver/offline');
      if (!off.ok) toast(errorMessage(off.status, off.data), 'bad');
      setProblem('');
      await refresh();
    } finally {
      setBusy(false);
    }
  }, [refresh, toast]);

  const accept = useCallback(async () => {
    if (!offer) return;
    setBusy(true);
    try {
      const res = await api<{ job: DriverJob }>('POST', `/driver/offers/${offer.id}/accept`);
      if (res.ok && res.data?.job) {
        setJob(res.data.job);
        setOffer(null);
        return;
      }
      toast(res.status === 404 ? 'That order was taken or ran out of time.' : errorMessage(res.status, res.data), 'bad');
      await refresh();
    } finally {
      setBusy(false);
    }
  }, [offer, refresh, toast]);

  const decline = useCallback(async () => {
    if (!offer) return;
    setBusy(true);
    try {
      const res = await api('POST', `/driver/offers/${offer.id}/decline`);
      if (!res.ok && res.status !== 404) toast(errorMessage(res.status, res.data), 'bad');
      setOffer(null);
      await refresh();
    } finally {
      setBusy(false);
    }
  }, [offer, refresh, toast]);

  const step = useCallback(async (action: 'pickup' | 'deliver') => {
    if (!job) return;
    setBusy(true);
    try {
      const res = await api<{ job: DriverJob }>('POST', `/orders/${encodeURIComponent(job.orderId)}/${action}`);
      if (res.ok) toast(action === 'pickup' ? 'Marked as picked up' : 'Marked as delivered', 'ok');
      else toast(errorMessage(res.status, res.data), 'bad');
      await refresh();
    } finally {
      setBusy(false);
    }
  }, [job, refresh, toast]);

  return { driver, offer, offerSeenAt, job, loaded, reachable, problem, busy, goOnline, goOffline, accept, decline, step };
}
