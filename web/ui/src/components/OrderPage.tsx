import { PayForm, useOrderActions } from '../hooks/orderActions';
import {
  addressLine, customerStatus, customerTimeline, itemsSummary, money, noticeFor, paymentSentence, STATE_LABELS, timeAgo, totalCents,
} from '../lib/logic';
import { stampFor } from '../lib/present';
import type { Order } from '../lib/types';
import { PaymentOrb, WaitBeam } from './Effects';
import { Icon } from './Icon';
import { Pill, Waiting } from './ui';

// The customer's tracker: their orders as a list, and one order as a page inside the Orders tab
// (state, not a route, like the restaurant page). Both read the orders the app polls every few
// seconds, so the page moves on by itself. The status is shown only: no map and no driver name,
// because drivers are still a stand-in. The server decides what the customer may do; the page
// offers only paying and cancelling (actionsFor with the customer role).

export function CustomerOrderRow({ order, onOpen }: { order: Order; onOpen(id: string): void }) {
  const stamp = stampFor(order);
  return (
    <li>
      <button type="button" className="row corder" aria-label={`Order ${order.id}, ${customerStatus(order)}`} onClick={() => onOpen(order.id)}>
        <span className="row-main">
          <span className="row-title"><span className="mono">{order.id}</span>
            {order.state === 'payment_pending' && <PaymentOrb />}
          </span>
          <span className="row-status">{customerStatus(order)}</span>
          <span className="row-sub">{itemsSummary(order) || 'No items'}</span>
          <span className="row-meta">{timeAgo(order.createdAt)}</span>
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

interface PageProps {
  /** the order, from the polled list; null when the list has no such order (any more) */
  order: Order | null;
  /** false until the first answer of the list */
  loaded: boolean;
  onBack(): void;
  onChanged(): Promise<void>;
}

export function OrderPage({ order, loaded, onBack, onChanged }: PageProps) {
  return (
    <section className="order-page" aria-label="Order details">
      <button type="button" className="text-btn back" onClick={onBack}>
        <Icon name="chevron" size={16} /> Your orders
      </button>
      {order ? <Tracker order={order} onChanged={onChanged} /> : !loaded ? (
        <div className="empty"><Waiting label="Loading the order" /><p>Loading the order</p></div>
      ) : (
        <div className="empty">
          <h3>We can't find this order</h3>
          <p>It may belong to another account, or the list has not loaded it yet.</p>
          <button className="btn tinted" type="button" onClick={onBack}>Back to your orders</button>
        </div>
      )}
    </section>
  );
}

function Tracker({ order, onChanged }: { order: Order; onChanged(): Promise<void> }) {
  const { actions, run, busy, payOpen, method, setMethod, hints, label } = useOrderActions(order, 'customer', onChanged);
  const notice = noticeFor(order);
  const address = order.deliveryAddress;

  return (
    <>
      <div className="page-head">
        <div>
          <h1 className="mono">{order.id}</h1>
          <p className="sub">{[timeAgo(order.createdAt) ? `Placed ${timeAgo(order.createdAt)}` : '', itemsSummary(order, 1)].filter(Boolean).join(' · ')}</p>
        </div>
        <Pill tone="neutral">{STATE_LABELS[order.state] || order.state}</Pill>
      </div>

      {/* read out when a poll moves the order on */}
      <p className="order-status" role="status" aria-live="polite">{customerStatus(order)}</p>

      {notice && (
        <WaitBeam active={notice.issue === 'confirming'}>
          <section className={`callout ${notice.tone}`} aria-label="About your payment" aria-busy={notice.issue === 'confirming'}>
            <small>About your payment</small>
            <strong>{notice.title}</strong>
            <p>{notice.body}</p>
          </section>
        </WaitBeam>
      )}

      <section aria-labelledby="track-title">
        <h2 className="group-title" id="track-title">Progress</h2>
        <ol className="list journey vertical">
          {customerTimeline(order).map((step) => (
            <li key={step.key} className={step.status} aria-current={step.status === 'current' ? 'step' : undefined}>
              <span className="dot">{step.status === 'done' && <Icon name="check" size={12} />}</span>
              <span className="label">{step.label}</span>
              <span className="visually-hidden">{STEP_WORDS[step.status]}</span>
              {step.note && <span className="note">{step.key === 'created' && <PaymentOrb />} {step.note}</span>}
            </li>
          ))}
        </ol>
      </section>

      <section aria-labelledby="items-title">
        <h2 className="group-title" id="items-title">Items</h2>
        <div className="list">
          {order.items.map((it, i) => (
            <div className="kv" key={`${it.sku}-${i}`}><span>{it.quantity} x {it.name}</span><span className="v">{money(it.unitPriceCents * it.quantity)}</span></div>
          ))}
          <div className="kv total"><span>Total</span><span className="v">{money(totalCents(order))}</span></div>
        </div>
      </section>

      <section aria-labelledby="where-title">
        <h2 className="group-title" id="where-title">Delivery address</h2>
        <div className="list">
          <div className="kv address">
            {address ? (
              <span>
                {address.label && <strong>{address.label}: </strong>}
                {addressLine(address)}
                {address.landmark && <span className="row-sub">Landmark: {address.landmark}</span>}
              </span>
            ) : <span className="why">No delivery address on this order.</span>}
          </div>
        </div>
      </section>

      <section aria-labelledby="pay-title">
        <h2 className="group-title" id="pay-title">Payment</h2>
        <div className="list"><p className="kv pay-sentence">{paymentSentence(order)}</p></div>
      </section>

      <footer className="order-actions">
        {actions.length === 0 && <p className="why">Nothing for you to do on this order right now.</p>}
        {payOpen && <PayForm order={order} method={method} onMethod={setMethod} />}
        {hints().map((h) => <p key={h} className="why">{h}</p>)}
        {actions.length > 0 && (
          <div className="action-row">
            {actions.map((a) => (
              <button key={a.key} type="button" disabled={!a.enabled || busy}
                className={`btn ${a.danger ? 'danger' : a.primary ? 'primary' : 'tinted'}`} onClick={() => void run(a)}>
                {label(a)}
              </button>
            ))}
          </div>
        )}
      </footer>
    </>
  );
}

const STEP_WORDS: Record<string, string> = { done: ', done', current: ', now', todo: ', still to come', skipped: ', skipped', stopped: ', stopped here' };
