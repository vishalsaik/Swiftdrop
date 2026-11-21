import { useEffect, useRef, useState } from 'react';
import { api } from '../api';
import { actionsFor, errorMessage, type OrderAction } from '../lib/logic';
import type { Order, Role } from '../lib/types';
import { useToast } from '../toasts';

// The order actions both the staff drawer and the customer's order page offer, in one place: the
// pay form that opens on the first tap, the two-tap confirm for anything dangerous (cancel), the
// request, the toast with the server's words and a reload. Recording a refund by hand is staff's
// own form and stays in OrderDetail.

const ENDPOINT: Record<string, string> = { accept: 'accept', ready: 'ready', pickup: 'pickup', deliver: 'deliver', cancel: 'cancel' };
export const DONE: Record<string, string> = {
  pay: 'Payment taken', accept: 'Order accepted', ready: 'Marked ready', pickup: 'Picked up', deliver: 'Delivered', cancel: 'Order cancelled',
  record_refund: 'Refund recorded',
};

export function useOrderActions(order: Order, role: Role, onChanged: () => Promise<void>) {
  const toast = useToast();
  const [confirming, setConfirming] = useState<string | null>(null);
  const [payOpen, setPayOpen] = useState(false);
  const [method, setMethod] = useState('pm_card_visa');
  const [busy, setBusy] = useState(false);
  const confirmTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // A different order starts with nothing half-done.
  useEffect(() => { setConfirming(null); setPayOpen(false); }, [order.id]);
  useEffect(() => () => { if (confirmTimer.current) clearTimeout(confirmTimer.current); }, []);

  const actions = actionsFor(order, role);

  async function run(action: OrderAction) {
    if (action.key === 'pay' && !payOpen && order.paymentType !== 'cash') { setPayOpen(true); return; } // the first click opens the form
    if (action.danger && confirming !== action.key) {
      setConfirming(action.key); // two taps to cancel, with no browser dialog
      if (confirmTimer.current) clearTimeout(confirmTimer.current);
      confirmTimer.current = setTimeout(() => setConfirming((c) => (c === action.key ? null : c)), 4000);
      return;
    }
    setBusy(true);
    setConfirming(null);
    const id = encodeURIComponent(order.id);
    const result = action.key === 'pay'
      ? await api<{ canRetry?: boolean; attemptsLeft?: number }>('POST', `/orders/${id}/pay`, order.paymentType === 'cash' || !method.trim() ? undefined : { paymentMethodId: method.trim() })
      : await api<{ canRetry?: boolean; attemptsLeft?: number }>('POST', `/orders/${id}/${ENDPOINT[action.key]}`);
    setBusy(false);
    setPayOpen(false);
    if (result.ok) {
      toast(`${order.id}: ${DONE[action.key]}`, 'ok');
    } else {
      const d = result.data;
      const more = result.status === 402 && d?.canRetry
        ? ` Try another method (${d.attemptsLeft} ${d.attemptsLeft === 1 ? 'attempt' : 'attempts'} left).` : '';
      toast(`${order.id}: ${errorMessage(result.status, d)}${more}`, 'bad');
    }
    await onChanged();
  }

  // why an action is greyed out, and the note of the one being confirmed or the pay form
  const hints = (extra: (a: OrderAction) => boolean = () => false) => actions
    .filter((a) => (a.why && !a.enabled) || (a.note && (confirming === a.key || a.key === 'pay' || extra(a))))
    .map((a) => a.why || a.note);

  const label = (a: OrderAction) => (confirming === a.key ? `Confirm: ${a.label.toLowerCase()}` : a.label);

  return { actions, run, busy, setBusy, payOpen, method, setMethod, hints, label };
}

// The field the pay form shows for card and wallet (cash needs nothing typed).
export function PayForm({ order, method, onMethod }: { order: Order; method: string; onMethod(m: string): void }) {
  if (order.paymentType === 'cash') return <p className="why">Cash on delivery: nothing is charged now. The order is marked paid.</p>;
  return (
    <div className="payform">
      <label className="field stacked">
        <span>Payment method id</span>
        <input id="method" value={method} spellCheck={false} autoComplete="off" autoFocus onChange={(e) => onMethod(e.target.value)} />
      </label>
      <small>Stripe needs a test token such as pm_card_visa (a decline is pm_card_visa_chargeDeclined). The built-in fake gateway ignores it.</small>
    </div>
  );
}
