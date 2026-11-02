import { useCallback, useEffect, useState, useSyncExternalStore } from 'react';

// Light is the default; dark only when chosen; "auto" follows the device.
export type Theme = 'light' | 'dark' | 'auto';

const KEY = 'sd.theme';

function stored(): Theme {
  try {
    const v = localStorage.getItem(KEY);
    if (v === 'light' || v === 'dark' || v === 'auto') return v;
  } catch { /* private mode or blocked storage: the page works without it */ }
  return 'light';
}

function apply(theme: Theme): void {
  document.documentElement.dataset.theme = theme;
}

export function useTheme(): [Theme, (t: Theme) => void] {
  const [theme, setTheme] = useState<Theme>(stored);
  useEffect(() => { apply(theme); }, [theme]);
  const choose = useCallback((t: Theme) => {
    setTheme(t);
    try { localStorage.setItem(KEY, t); } catch { /* see above */ }
  }, []);
  return [theme, choose];
}

// The theme as it is actually drawn: "auto" becomes light or dark by the device. For the
// effects that must be told which surface they sit on.
function resolved(): 'light' | 'dark' {
  const set = document.documentElement.dataset.theme;
  if (set === 'dark') return 'dark';
  if (set === 'auto') return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  return 'light';
}

function subscribe(onChange: () => void): () => void {
  const watch = new MutationObserver(onChange);
  watch.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  const media = window.matchMedia?.('(prefers-color-scheme: dark)');
  media?.addEventListener('change', onChange);
  return () => { watch.disconnect(); media?.removeEventListener('change', onChange); };
}

export function useResolvedTheme(): 'light' | 'dark' {
  return useSyncExternalStore(subscribe, resolved, () => 'light');
}

/** True when the device asks for less motion. */
export function usePrefersReducedMotion(): boolean {
  return useSyncExternalStore(
    (onChange) => {
      const media = window.matchMedia?.('(prefers-reduced-motion: reduce)');
      media?.addEventListener('change', onChange);
      return () => media?.removeEventListener('change', onChange);
    },
    () => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false,
    () => false,
  );
}
