import { useEffect, useId, useRef, type ReactNode } from 'react';
import type { Tone } from '../lib/present';
import { initials } from '../lib/logic';

// Small pieces shared by the screens.

/** A modal sheet: a bottom sheet on a phone, a centred card on a larger screen. Built on <dialog>, so focus and Escape work. */
export function Sheet(props: {
  open: boolean; onClose: () => void; title: string; leading?: ReactNode; trailing?: ReactNode; children: ReactNode; className?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const { open } = props;

  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);

  return (
    <dialog
      ref={ref} className={`sheet ${props.className ?? ''}`} aria-labelledby={titleId}
      onClose={props.onClose}
      onClick={(e) => { if (e.target === ref.current) props.onClose(); }}
    >
      {open && (
        <div className="sheet-body">
          <header className="sheet-bar">
            <div className="bar-side">{props.leading}</div>
            <h2 id={titleId}>{props.title}</h2>
            <div className="bar-side end">{props.trailing}</div>
          </header>
          {props.children}
        </div>
      )}
    </dialog>
  );
}

/** Three dots that breathe: something is being waited for. */
export function Waiting({ label = 'Waiting' }: { label?: string }) {
  return (
    <span className="waiting" role="img" aria-label={label}>
      <i /><i /><i />
    </span>
  );
}

export function Pill({ tone, children }: { tone: Tone | 'neutral'; children: ReactNode }) {
  return <span className={`pill ${tone}`}>{children}</span>;
}

export function Avatar({ name, size = 'md' }: { name: string; size?: 'md' | 'lg' }) {
  return <span className={`avatar ${size}`} aria-hidden="true">{initials(name)}</span>;
}

/** The segmented control of iOS: one choice out of a few. */
export function Segmented<T extends string>(props: {
  value: T; onChange: (v: T) => void; options: { value: T; label: string }[]; label: string;
}) {
  return (
    <div className="segmented" role="tablist" aria-label={props.label}>
      {props.options.map((o) => (
        <button key={o.value} type="button" role="tab" aria-selected={props.value === o.value} onClick={() => props.onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}
