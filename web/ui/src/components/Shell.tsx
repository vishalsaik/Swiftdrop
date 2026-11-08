import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api';
import { useAlerts, useOrders } from '../hooks/data';
import { capabilities, COLUMNS, columnOf, greeting, money, newOrderId, noticeFor, stats, type Notice } from '../lib/logic';
import { STAGE_STYLE } from '../lib/present';
import type { Order, User } from '../lib/types';
import { useSession } from '../session';
import type { Theme } from '../theme';
import { AccountSheet } from './AccountSheet';
import { AlertsView } from './AlertsView';
import { Icon } from './Icon';
import { NewOrderSheet } from './NewOrderSheet';
import { OrderDetail } from './OrderDetail';
import { OrderList, type Stage } from './OrderList';
import { PeopleView } from './People';
import { Avatar, Waiting } from './ui';

type View = 'orders' | 'alerts' | 'people';
type SheetName = 'new' | 'account' | null;

interface NoticeEntry { id: string; orderId: string; notice: Notice }

export function Shell({ theme, onTheme }: { theme: Theme; onTheme(t: Theme): void }) {
  const { user } = useSession() as { user: User };
  const caps = capabilities(user.role);

  const { orders, loaded, online, offlineMessage, refresh } = useOrders(true);
  const { alerts, refresh: refreshAlerts } = useAlerts(caps.alerts);

  const [view, setView] = useState<View>('orders');
  const [stage, setStage] = useState<Stage>('all');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [sheet, setSheet] = useState<SheetName>(null);
  const [notices, setNotices] = useState<NoticeEntry[]>([]);

  const byId = useMemo(() => new Map(orders.map((o) => [o.id, o])), [orders]);
  const selected = selectedId ? byId.get(selectedId) ?? null : null;
  const totals = useMemo(() => stats(orders), [orders]);

  // A message appears when an order's payment issue CHANGES while we are watching.
  // What was already true when the page opened is not announced again.
  const issues = useRef(new Map<string, string>());
  const seeded = useRef(false);
  useEffect(() => {
    if (!loaded) return;
    const fresh: NoticeEntry[] = [];
    for (const o of orders) {
      const n = noticeFor(o);
      const issue = n ? n.issue : '';
      if (seeded.current && n && issues.current.get(o.id) !== issue) fresh.push({ id: o.id, orderId: o.id, notice: n });
      issues.current.set(o.id, issue);
    }
    seeded.current = true;
    if (fresh.length) setNotices((ns) => [...ns.filter((x) => !fresh.some((f) => f.id === x.id)), ...fresh].slice(-3));
  }, [orders, loaded]);

  // Escape closes the open order when nothing else is on top of it.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('dialog[open]')) setSelectedId(null);
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, []);

  const openOrder = useCallback((id: string) => { setView('orders'); setSelectedId(id); }, []);

  const refreshAll = useCallback(async () => {
    await refresh();
    if (caps.alerts) await refreshAlerts();
  }, [refresh, refreshAlerts, caps.alerts]);

  async function createSamples() {
    const samples: [string, [string, number, number][]][] = [
      ['card', [['Smash burger', 2, 950], ['Fries', 1, 400]]],
      ['wallet', [['Pad thai', 1, 1390], ['Miso soup', 1, 350]]],
      ['cash', [['Margherita pizza', 1, 1200], ['Lemonade', 2, 325]]],
    ];
    for (const [paymentType, items] of samples) {
      await api('POST', '/orders', {
        id: newOrderId(), customerId: caps.placeForOthers ? 'cust-1' : user.id, restaurantId: 'rest-1', paymentType,
        items: items.map(([name, quantity, unitPriceCents]) => ({ sku: name.replace(/\W/g, '').toUpperCase().slice(0, 8), name, quantity, unitPriceCents })),
      });
    }
    await refresh();
  }

  const counts = useMemo(() => {
    const m: Record<string, number> = { all: orders.length };
    for (const c of COLUMNS) m[c.key] = 0;
    for (const o of orders) m[columnOf(o)]++;
    return m;
  }, [orders]);

  const pick = (v: View, s: Stage = 'all') => { setView(v); setStage(s); };

  const tiles: [string, string, string][] = [
    ['Active', String(totals.active), 'blue'],
    ['On the road', String(totals.onTheRoad), 'teal'],
    ['Taken, in play', money(totals.takenCents), ''],
    ['Refunds on the way', String(totals.refundsPending), totals.refundsPending ? 'orange' : ''],
  ];
  if (totals.attention) tiles.push(['Needs a look', String(totals.attention), 'orange']);

  return (
    <div className="app">
      <a className="skip" href="#content">Skip to the content</a>

      <nav className="sidebar" aria-label="Main">
        <div className="side-brand">
          <svg className="mark" viewBox="0 0 32 32" aria-hidden="true">
            <rect width="32" height="32" rx="8" fill="currentColor" />
            <path d="M7 11h13M5 16h17M9 21h12" stroke="#fff" strokeWidth="3" strokeLinecap="round" />
            <path d="M22 9l6 7-6 7" fill="none" stroke="#fff" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          <span>SwiftDrop</span>
        </div>

        <h2 className="side-title">Orders</h2>
        <ul className="side-list">
          <SideItem icon="tray" tint="blue" label="All orders" count={counts.all} active={view === 'orders' && stage === 'all'} onClick={() => pick('orders')} />
          {COLUMNS.map((c) => (
            <SideItem key={c.key} icon={STAGE_STYLE[c.key].icon} tint={STAGE_STYLE[c.key].tint} label={c.title} count={counts[c.key]}
              active={view === 'orders' && stage === c.key} onClick={() => pick('orders', c.key)} />
          ))}
        </ul>

        {caps.alerts && (
          <>
            <h2 className="side-title">Attention</h2>
            <ul className="side-list">
              <SideItem icon="bell" tint="red" label="Alerts" count={alerts.length} badge active={view === 'alerts'} onClick={() => pick('alerts')} />
            </ul>
          </>
        )}

        {caps.people && (
          <>
            <h2 className="side-title">Admin</h2>
            <ul className="side-list">
              <SideItem icon="person" tint="purple" label="People" active={view === 'people'} onClick={() => pick('people')} />
            </ul>
          </>
        )}

        <div className="side-foot">
          <button className="btn primary wide" type="button" onClick={() => setSheet('new')}><Icon name="plus" size={18} /> New order</button>
          <button className="account-row" type="button" aria-haspopup="dialog" onClick={() => setSheet('account')} aria-label={`Your account, ${user.name}`}>
            <Avatar name={user.name} />
            <span><strong>{user.name}</strong><small>{user.role}</small></span>
          </button>
        </div>
      </nav>

      <div className="main" id="content" tabIndex={-1}>
        <header className="page-head">
          <div>
            <h1>{view === 'alerts' ? 'Alerts' : view === 'people' ? 'People' : `${greeting()}, ${user.name.trim().split(/\s+/)[0] || 'there'}`}</h1>
            <p className="sub">{view === 'alerts' ? 'Things that need a person.' : view === 'people' ? 'Staff and customers, and what they can do.' : caps.everyonesOrders ? 'Here is how the orders are doing.' : 'Here are your orders.'}</p>
          </div>
          <div className="head-tools">
            <span className={`live ${online === false ? 'off' : online ? 'on' : ''}`} role="status">
              {online === null ? <Waiting label="Connecting" /> : <i />}
              <span>{online === null ? 'Connecting' : online ? 'Live' : 'Offline'}</span>
            </span>
            <button className="btn primary head-new" type="button" onClick={() => setSheet('new')}>New order</button>
          </div>
        </header>

        {online === false && <div className="banner" role="alert">{offlineMessage}</div>}

        {view === 'orders' ? (
          <>
            <dl className="stats" aria-label="Live totals">
              {tiles.map(([label, value, tone]) => <div className={`stat ${tone}`} key={label}><dt>{label}</dt><dd>{value}</dd></div>)}
            </dl>
            <div className="chips" role="tablist" aria-label="Stage">
              <Chip label="All" count={counts.all} active={stage === 'all'} onClick={() => setStage('all')} />
              {COLUMNS.map((c) => <Chip key={c.key} label={c.title} count={counts[c.key]} active={stage === c.key} onClick={() => setStage(c.key)} />)}
            </div>
            <OrderList orders={orders} stage={stage} selectedId={selectedId} loaded={loaded} showCustomer={caps.everyonesOrders}
              onSelect={setSelectedId} onSamples={() => void createSamples()} />
          </>
        ) : view === 'people' && caps.people ? (
          <PeopleView me={user} />
        ) : (
          <AlertsView alerts={alerts} knownOrders={new Set(byId.keys())} onOpenOrder={openOrder} onResolved={refreshAll} />
        )}
      </div>

      <nav className="tabbar" aria-label="Tabs">
        <TabButton icon="tray" label="Orders" active={view === 'orders'} onClick={() => pick('orders', stage)} />
        {caps.alerts && <TabButton icon="bell" label="Alerts" badge={alerts.length} active={view === 'alerts'} onClick={() => pick('alerts')} />}
        {caps.people && <TabButton icon="person" label="People" active={view === 'people'} onClick={() => pick('people')} />}
        <TabButton icon="plus" label="New" onClick={() => setSheet('new')} />
        <TabButton icon="person" label="Account" onClick={() => setSheet('account')} />
      </nav>

      <div className={`scrim ${selected ? 'show' : ''}`} onClick={() => setSelectedId(null)} hidden={!selected} />
      <aside className={`inspector ${selected ? 'open' : ''}`} aria-label="Order details" aria-hidden={!selected}>
        {selected && (
          <OrderDetail order={selected as Order} role={user.role} canSeeHistory={caps.history} onClose={() => setSelectedId(null)} onChanged={refreshAll} />
        )}
      </aside>

      <div className="notices" aria-live="polite">
        {notices.map((n) => (
          <article key={n.id} className={`notice ${n.notice.tone}`} role="status">
            <small>Message to customer / {n.orderId}</small>
            <strong>{n.notice.title}</strong>
            <p>{n.notice.body}</p>
            <div className="notice-actions">
              <button type="button" className="text-btn" onClick={() => setNotices((ns) => ns.filter((x) => x.id !== n.id))}>Dismiss</button>
              <button type="button" className="text-btn strong" onClick={() => { openOrder(n.orderId); setNotices((ns) => ns.filter((x) => x.id !== n.id)); }}>Open order</button>
            </div>
          </article>
        ))}
      </div>

      <NewOrderSheet open={sheet === 'new'} user={user} placeForOthers={caps.placeForOthers} onClose={() => setSheet(null)}
        onPlaced={async (id) => { await refreshAll(); openOrder(id); }} />
      <AccountSheet open={sheet === 'account'} user={user} theme={theme} onTheme={onTheme} onClose={() => setSheet(null)} />
    </div>
  );
}

function SideItem(props: { icon: Parameters<typeof Icon>[0]['name']; tint: string; label: string; count?: number; active: boolean; badge?: boolean; onClick(): void }) {
  return (
    <li>
      <button type="button" className={`side-item ${props.active ? 'active' : ''}`} aria-current={props.active ? 'page' : undefined} onClick={props.onClick}>
        <span className={`tile ${props.tint}`}><Icon name={props.icon} size={18} /></span>
        <span className="label">{props.label}</span>
        {props.count === undefined ? null : props.badge ? (props.count > 0 && <span className="badge">{props.count}</span>) : <span className="count">{props.count}</span>}
      </button>
    </li>
  );
}

function Chip({ label, count, active, onClick }: { label: string; count: number; active: boolean; onClick(): void }) {
  return (
    <button type="button" role="tab" aria-selected={active} className={`chip ${active ? 'active' : ''}`} onClick={onClick}>
      {label}<span className="count">{count}</span>
    </button>
  );
}

function TabButton(props: { icon: Parameters<typeof Icon>[0]['name']; label: string; active?: boolean; badge?: number; onClick(): void }) {
  return (
    <button type="button" className={`tab ${props.active ? 'active' : ''}`} aria-current={props.active ? 'page' : undefined} onClick={props.onClick}>
      <Icon name={props.icon} size={24} />
      <span>{props.label}</span>
      {props.badge ? <span className="badge">{props.badge}</span> : null}
    </button>
  );
}
