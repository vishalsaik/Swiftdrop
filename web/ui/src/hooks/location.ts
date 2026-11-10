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
