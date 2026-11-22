import { useCallback, useState } from 'react';
import { locationProblem } from '../lib/logic';

export interface Position { lat: number; lng: number }

export interface LocationState {
  /** idle: not asked; asking: the browser is deciding or looking; ready: we have a position; problem: we do not, and message says why */
  status: 'idle' | 'asking' | 'ready' | 'problem';
  position: Position | null;
  message: string;
}

const IDLE: LocationState = { status: 'idle', position: null, message: '' };

export type PositionResult = { ok: true; position: Position } | { ok: false; code: number };

/**
 * One look at where the person is, asked for now (the browser shows its own prompt the first time). For a
 * driver, who sends it on purpose and again every half minute while they are online; code is the browser's
 * (1 denied, 2 unavailable, 3 timed out). Nothing is kept here.
 */
export function currentPosition(): Promise<PositionResult> {
  return new Promise((resolve) => {
    if (typeof navigator === 'undefined' || !('geolocation' in navigator)) { resolve({ ok: false, code: 2 }); return; }
    navigator.geolocation.getCurrentPosition(
      (p) => resolve({ ok: true, position: { lat: p.coords.latitude, lng: p.coords.longitude } }),
      (e) => resolve({ ok: false, code: e.code }),
      { enableHighAccuracy: true, timeout: 10_000, maximumAge: 10_000 },
    );
  });
}

/**
 * Where the person is, only when they ask for it. Nothing happens until ask() is called (the
 * browser shows its own permission prompt then, not before), the position is kept in memory only,
 * and clear() forgets it. It is looked up once, not followed: a customer who moves asks again.
 */
export function useLocation() {
  const [state, setState] = useState<LocationState>(IDLE);

  const ask = useCallback(() => {
    if (typeof navigator === 'undefined' || !('geolocation' in navigator)) {
      setState({ status: 'problem', position: null, message: locationProblem(2) });
      return;
    }
    setState((s) => ({ ...s, status: 'asking', message: '' }));
    navigator.geolocation.getCurrentPosition(
      (p) => setState({ status: 'ready', position: { lat: p.coords.latitude, lng: p.coords.longitude }, message: '' }),
      (e) => setState({ status: 'problem', position: null, message: locationProblem(e.code) }),
      { enableHighAccuracy: false, timeout: 10_000, maximumAge: 60_000 },
    );
  }, []);

  const clear = useCallback(() => setState(IDLE), []);
  return { ...state, ask, clear };
}
