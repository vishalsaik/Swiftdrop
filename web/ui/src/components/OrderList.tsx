import { COLUMNS, columnOf, itemsSummary, money, STATE_LABELS, timeAgo, totalCents } from '../lib/logic';
import { STAGE_STYLE, stampFor } from '../lib/present';
import type { Order } from '../lib/types';
import { PaymentOrb } from './Effects';
import { Icon } from './Icon';
import { Pill, Waiting } from './ui';

export type Stage = 'all' | string;

interface Props {
  orders: Order[];
  stage: Stage;
  selectedId: string | null;
  loaded: boolean;
  showCustomer: boolean;
  onSelect(id: string): void;
  onSamples(): void;
}

export function OrderList({ orders, stage, selectedId, loaded, showCustomer, onSelect, onSamples }: Props) {
  if (!loaded) {
    return <div className="empty"><Waiting label="Loading orders" /><p>Loading orders</p></div>;
  }
  if (orders.length === 0) {
    return (
      <div className="empty">
        <Icon name="tray" size={44} />
        <h3>No orders yet</h3>
        <p>New orders show up here as they come in.</p>
        <button className="btn tinted" type="button" onClick={onSamples}>Create 3 sample orders</button>
      </div>
    );
  }

  const groups = COLUMNS
    .filter((c) => stage === 'all' || c.key === stage)
    .map((c) => ({ column: c, rows: orders.filter((o) => columnOf(o) === c.key) }))
    .filter((g) => stage !== 'all' || g.rows.length > 0);

  if (groups.every((g) => g.rows.length === 0)) {
    return <div className="empty"><Icon name="check" size={44} /><h3>Nothing here</h3><p>No orders at this stage right now.</p></div>;
  }

  return (
    <div className="groups">
      {groups.map(({ column, rows }) => (
        <section key={column.key} className="group" aria-label={column.title}>
          <h3 className="group-title">{column.title}<span className="count">{rows.length}</span></h3>
          <ul className="list">
            {rows.map((o) => <OrderRow key={o.id} order={o} selected={o.id === selectedId} showCustomer={showCustomer} onSelect={onSelect} />)}
          </ul>
        </section>
      ))}
    </div>
  );
}

export function OrderRow({ order, selected, showCustomer, onSelect }: { order: Order; selected: boolean; showCustomer: boolean; onSelect(id: string): void }) {
  const stamp = stampFor(order);
  const style = STAGE_STYLE[columnOf(order)] ?? STAGE_STYLE.closed;
  const meta = [timeAgo(order.createdAt), order.paymentType, showCustomer ? order.customerId : ''].filter(Boolean).join(' / ');
  return (
    <li>
      <button type="button" className={`row ${selected ? 'selected' : ''}`} aria-current={selected ? 'true' : undefined}
        aria-label={`Order ${order.id}, ${STATE_LABELS[order.state] || order.state}`} onClick={() => onSelect(order.id)}>
        <span className={`tile ${style.tint}`}><Icon name={style.icon} size={20} /></span>
        <span className="row-main">
          <span className="row-title"><span className="mono">{order.id}</span>
            {order.state === 'payment_pending' && <PaymentOrb />}
          </span>
          <span className="row-sub">{itemsSummary(order) || 'No items'}</span>
          <span className="row-meta">{meta}</span>
        </span>
        <span className="row-trail">
          <span className="amount">{money(totalCents(order))}</span>
          {stamp && <Pill tone={stamp.tone}>{stamp.text}</Pill>}
        </span>
        <span className="chev"><Icon name="chevron" size={16} /></span>
      </button>
    </li>
  );
}
