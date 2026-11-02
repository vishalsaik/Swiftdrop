import { useState } from 'react';
import { api } from '../api';
import { describeAlert, errorMessage, timeAgo } from '../lib/logic';
import type { Alert } from '../lib/types';
import { useToast } from '../toasts';
import { Icon } from './Icon';

interface Props {
  alerts: Alert[];
  /** ids of orders on the board, so "Open order" is offered only where there is one */
  knownOrders: Set<string>;
  onOpenOrder(id: string): void;
  onResolved(): Promise<void>;
}

export function AlertsView({ alerts, knownOrders, onOpenOrder, onResolved }: Props) {
  if (alerts.length === 0) {
    return <div className="empty"><Icon name="check" size={44} /><h3>All clear</h3><p>Nothing needs a look. Problems the system cannot settle by itself appear here.</p></div>;
  }
  return (
    <div className="groups">
      <p className="hint">Problems the system cannot settle by itself. They stay here until someone resolves them.</p>
      <ul className="list alert-list">
        {alerts.map((a) => <AlertItem key={a.id} alert={a} canOpen={Boolean(a.orderId && knownOrders.has(a.orderId))} onOpenOrder={onOpenOrder} onResolved={onResolved} />)}
      </ul>
    </div>
  );
}

function AlertItem({ alert, canOpen, onOpenOrder, onResolved }: { alert: Alert; canOpen: boolean; onOpenOrder(id: string): void; onResolved(): Promise<void> }) {
  const toast = useToast();
  const d = describeAlert(alert);
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);

  async function resolve() {
    setBusy(true);
    const trimmed = note.trim();
    const res = await api('POST', `/alerts/${alert.id}/resolve`, trimmed ? { note: trimmed } : undefined);
    setBusy(false);
    // 409 means somebody else already did it, which is what the person wanted
    if (res.ok || res.status === 409) toast('Alert resolved', 'ok'); else toast(errorMessage(res.status, res.data), 'bad');
    await onResolved();
  }

  return (
    <li className="alert-item">
      <div className="alert-head"><span className="tile red"><Icon name="warning" size={20} /></span><h3>{d.title}</h3></div>
      <p>{d.advice}</p>
      {d.detail && <p className="detail-text">{d.detail}</p>}
      <p className="row-meta">{[alert.orderId ? `order ${alert.orderId}` : '', d.seen, timeAgo(alert.lastSeenAt)].filter(Boolean).join(' / ')}</p>
      <div className="alert-actions">
        <input aria-label="Note for resolving" placeholder="What was done (optional)" value={note} onChange={(e) => setNote(e.target.value)} />
        {canOpen && <button className="btn tinted" type="button" onClick={() => onOpenOrder(alert.orderId!)}>Open order</button>}
        <button className="btn primary" type="button" disabled={busy} onClick={() => void resolve()}>Resolve</button>
      </div>
    </li>
  );
}
