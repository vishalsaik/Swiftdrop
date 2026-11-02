import { useCallback, useEffect, useState } from 'react';

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
