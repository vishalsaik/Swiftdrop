import type { ReactNode } from 'react';
import { BorderBeam } from 'border-beam';
import { ThinkingOrb } from 'thinking-orbs';
import { usePrefersReducedMotion, useResolvedTheme } from '../theme';

// The two libraries.dev effects the dashboard uses, kept in one file so where they appear
// is easy to see. Both are for waits that last (a payment that has no answer yet can take
// minutes); a short load keeps the plain dots in ui.tsx.

/** A small orb beside a line of text. The text already says what is happening, so it is hidden from screen readers. */
export function PaymentOrb() {
  return <ThinkingOrb state="connecting" size={20} aria-hidden="true" className="orb" />;
}

/** A light that travels round the card while `active`. Switched off for people who ask for less motion. */
export function WaitBeam({ active, children }: { active: boolean; children: ReactNode }) {
  const theme = useResolvedTheme();
  const reduced = usePrefersReducedMotion();
  return (
    <BorderBeam active={active && !reduced} theme={theme} strength={0.85} className="wait-beam">
      {children}
    </BorderBeam>
  );
}
