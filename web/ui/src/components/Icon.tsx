import type { ReactElement } from 'react';
import type { IconName } from '../lib/present';

// A small set of line symbols in the spirit of SF Symbols. They inherit the text colour.
const PATHS: Record<IconName, ReactElement> = {
  tray: <><path d="M4 13l2.2-6.6A2 2 0 0 1 8.1 5h7.8a2 2 0 0 1 1.9 1.4L20 13" /><path d="M4 13v4a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-4h-4.5a2.5 2.5 0 0 1-5 0z" /></>,
  bell: <><path d="M6 17V11a6 6 0 1 1 12 0v6l1.5 2h-15z" /><path d="M10 21a2 2 0 0 0 4 0" /></>,
  plus: <><path d="M12 5v14M5 12h14" /></>,
  moon: <><path d="M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z" /></>,
  sun: <><circle cx="12" cy="12" r="4" /><path d="M12 3v2M12 19v2M3 12h2M19 12h2M5.6 5.6 7 7M17 17l1.4 1.4M5.6 18.4 7 17M17 7l1.4-1.4" /></>,
  xmark: <><path d="M6 6l12 12M18 6 6 18" /></>,
  chevron: <><path d="M9 5l7 7-7 7" /></>,
  check: <><path d="M5 12.5l4.5 4.5L19 7.5" /></>,
  clock: <><circle cx="12" cy="12" r="8.5" /><path d="M12 7.5V12l3 2" /></>,
  card: <><rect x="3" y="5.5" width="18" height="13" rx="2.5" /><path d="M3 10h18M7 15h3" /></>,
  flame: <><path d="M12 3c.5 3-2.5 4.5-2.5 7.5 0 1 .5 1.8 1.2 2.3C10 11 11 9.5 12 9c0 3 3 3.5 3 6.5A3.5 3.5 0 0 1 8.5 17c-2.5-1-3.5-3.5-3-6C6 7.5 10 6 12 3z" /></>,
  scooter: <><circle cx="6.5" cy="17" r="2.5" /><circle cx="17.5" cy="17" r="2.5" /><path d="M9 17h6l-2.5-8H10M13 6h3l1.5 8" /></>,
  archive: <><rect x="3" y="4.5" width="18" height="4.5" rx="1.5" /><path d="M5 9v8a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V9M10 13h4" /></>,
  person: <><circle cx="12" cy="8.5" r="3.5" /><path d="M5 19.5c1-3.5 3.7-5 7-5s6 1.5 7 5" /></>,
  warning: <><path d="M12 4 3.5 19h17z" /><path d="M12 10v4M12 16.8v.2" /></>,
  eye: <><path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z" /><circle cx="12" cy="12" r="2.8" /></>,
  eyeSlash: <><path d="M4 4l16 16" /><path d="M9.5 6a10 10 0 0 1 2.5-.5c6 0 9.5 6.5 9.5 6.5a17 17 0 0 1-3 3.7M6.3 8A17 17 0 0 0 2.5 12S6 18.5 12 18.5c1.2 0 2.3-.3 3.3-.7" /></>,
};

export function Icon({ name, size = 22 }: { name: IconName; size?: number }) {
  return (
    <svg className="icon" viewBox="0 0 24 24" width={size} height={size} aria-hidden="true" fill="none" stroke="currentColor"
      strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
      {PATHS[name]}
    </svg>
  );
}
