import { useEffect, useRef, useState } from 'react';
import { api } from '../api';
import {
  actionsFor, describeEvent, errorMessage, money, noticeFor, STATE_LABELS, timeAgo, timeline, totalCents,
  type OrderAction,
} from '../lib/logic';
import { signature } from '../lib/present';
import type { LedgerEvent, Order, Role } from '../lib/types';
import { useToast } from '../toasts';
import { PaymentOrb, WaitBeam } from './Effects';
import { Icon } from './Icon';
import { Pill } from './ui';

const ENDPOINT: Record<string, string> = { accept: 'accept', ready: 'ready', pickup: 'pickup', deliver: 'deliver', cancel: 'cancel' };
const DONE: Record<string, string> = {
  pay: 'Payment taken', accept: 'Order accepted', ready: 'Marked ready', pickup: 'Picked up', deliver: 'Delivered', cancel: 'Order cancelled',
};

interface Props {
  order: Order;
  role: Role;
  canSeeHistory: boolean;
  onClose(): void;
  onChanged(): Promise<void>;
}

export function OrderDetail({ order, role, canSeeHistory, onClose, onChanged }: Props) {
  const toast = useToast();
  const [confirming, setConfirming] = useState<string | null>(null);
  const [payOpen, setPayOpen] = useState(false);
  const [method, setMethod] = useState('pm_card_visa');
  const [busy, setBusy] = useState(false);
  const [history, setHistory] = useState<LedgerEvent[] | null>(null);
  const confirmTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // A different order starts with nothing half-done.
  useEffect(() => { setConfirming(null); setPayOpen(false); setHistory(null); }, [order.id]);
  useEffect(() => () => { if (confirmTimer.current) clearTimeout(confirmTimer.current); }, []);

  // The history is read again when what the order shows changes, not on every poll.
  const sig = `${signature(order)}|${order.payment?.refundStatus ?? ''}`;
  useEffect(() => {
    if (!canSeeHistory || !order.payment) return;
    let cancelled = false;
    void api<{ events: LedgerEvent[] | null }>('GET', `/orders/${encodeURIComponent(order.id)}/payment-history`).then((res) => {
      if (!cancelled && res.ok) setHistory(res.data?.events ?? []);
    });
    return () => { cancelled = true; };
  }, [order.id, sig, canSeeHistory, order.payment]);

  const notice = noticeFor(order);
  const pay = order.payment;
  const actions = actionsFor(order, role);
  const refund = pay?.refundStatus && pay.refundStatus !== 'none'
    ? ({ refunding: 'in progress', refunded: 'sent back', failed: 'could not be sent' } as Record<string, string>)[pay.refundStatus] ?? pay.refundStatus
    : null;

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

  const hints = actions.filter((a) => (a.why && !a.enabled) || (a.note && (confirming === a.key || a.key === 'pay'))).map((a) => a.why || a.note);

  const details: [string, string][] = [
    ['Pays by', order.paymentType],
    ['Payment', pay ? pay.status : 'not started'],
    ...(pay ? [['Amount', money(pay.amountCents)] as [string, string]] : []),
    ...(pay && (pay.attempt > 1 || ['declined', 'abandoned'].includes(pay.status)) ? [['Attempt', `${pay.attempt} of ${pay.maxAttempts}`] as [string, string]] : []),
    ...(pay?.reason ? [['Decline reason', pay.reason] as [string, string]] : []),
    ...(refund ? [['Refund', refund] as [string, string]] : []),
    ...(role === 'staff' ? [['Customer', order.customerId] as [string, string], ['Restaurant', order.restaurantId] as [string, string]] : []),
    ...(order.assignedDriverId ? [['Driver', order.assignedDriverId.slice(0, 8)] as [string, string]] : []),
    ...(order.foodReady ? [['Food ready', timeAgo(order.foodReady)] as [string, string]] : []),
  ];

  return (
    <div className="detail">
      <header className="detail-bar">
        <div className="titles">
          <h2 className="mono">{order.id}</h2>
          <span className="sub">{timeAgo(order.createdAt) || 'new'}</span>
        </div>
        <Pill tone="neutral">{STATE_LABELS[order.state] || order.state}</Pill>
        <button className="icon-btn" type="button" aria-label="Close" onClick={onClose}><Icon name="xmark" size={20} /></button>
      </header>

      <div className="detail-scroll">
        {notice && (
          <WaitBeam active={notice.issue === 'confirming'}>
            <section className={`callout ${notice.tone}`} aria-label="What the customer sees" aria-busy={notice.issue === 'confirming'}>
              <small>{role === 'staff' ? 'What the customer sees' : 'About your payment'}</small>
              <strong>{notice.title}</strong>
              <p>{notice.body}</p>
            </section>
          </WaitBeam>
        )}

        <section>
          <h3 className="group-title">Journey</h3>
          <ol className="list journey">
            {timeline(order).map((step) => (
              <li key={step.key} className={step.status}>
                <span className="dot">{step.status === 'done' && <Icon name="check" size={12} />}</span>
                <span className="label">{step.label}</span>
                {step.note && <span className="note"><PaymentOrb /> {step.note}</span>}
              </li>
            ))}
          </ol>
        </section>

        <section>
          <h3 className="group-title">Items</h3>
          <div className="list">
            {order.items.map((it, i) => (
              <div className="kv" key={`${it.sku}-${i}`}><span>{it.quantity} x {it.name}</span><span className="v">{money(it.unitPriceCents * it.quantity)}</span></div>
            ))}
            <div className="kv total"><span>Total</span><span className="v">{money(totalCents(order))}</span></div>
          </div>
        </section>

        <section>
          <h3 className="group-title">Details</h3>
          <dl className="list">
            {details.map(([k, v]) => <div className="kv" key={k}><dt>{k}</dt><dd className="v">{v}</dd></div>)}
          </dl>
        </section>

        {canSeeHistory && pay && (
          <section>
            <h3 className="group-title">Payment history</h3>
            {history === null && <p className="why">Loading</p>}
            {history !== null && history.length === 0 && <p className="why">Nothing recorded yet.</p>}
            {history !== null && history.length > 0 && (
              <ol className="list history">
                {history.map((ev, i) => {
                  const { label, note } = describeEvent(ev);
                  const when = new Date(ev.at);
                  return (
                    <li key={`${ev.at}-${i}`}>
                      <time dateTime={ev.at}>{Number.isNaN(when.getTime()) ? '' : when.toLocaleTimeString([], { hour12: false })}</time>
                      <span className="what">{label}</span>
                      {note && <span className="note">{note}</span>}
                    </li>
                  );
                })}
              </ol>
            )}
          </section>
        )}
      </div>

      <footer className="detail-actions">
        {actions.length === 0 && (
          <p className="why">{role === 'staff' ? 'This order is finished. There is nothing left to do.' : 'Nothing for you to do on this order right now.'}</p>
        )}
        {payOpen && order.paymentType === 'cash' && <p className="why">Cash on delivery: nothing is charged now. The order is marked paid.</p>}
        {payOpen && order.paymentType !== 'cash' && (
          <div className="payform">
            <label className="field stacked">
              <span>Payment method id</span>
              <input id="method" value={method} spellCheck={false} autoComplete="off" autoFocus onChange={(e) => setMethod(e.target.value)} />
            </label>
            <small>Stripe needs a test token such as pm_card_visa (a decline is pm_card_visa_chargeDeclined). The built-in fake gateway ignores it.</small>
          </div>
        )}
        {hints.map((h) => <p key={h} className="why">{h}</p>)}
        {actions.length > 0 && (
          <div className="action-row">
            {actions.map((a) => (
              <button key={a.key} type="button" disabled={!a.enabled || busy}
                className={`btn ${a.danger ? 'danger' : a.primary ? 'primary' : 'tinted'}`} onClick={() => void run(a)}>
                {confirming === a.key ? `Confirm: ${a.label.toLowerCase()}` : a.label}
              </button>
            ))}
          </div>
        )}
      </footer>
    </div>
  );
}
