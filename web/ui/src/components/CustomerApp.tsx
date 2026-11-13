import { useCallback, useEffect, useMemo, useState } from 'react';
import { useOrders } from '../hooks/data';
import { CartProvider, useCart } from '../hooks/cart';
import { greeting, money } from '../lib/logic';
import type { IconName } from '../lib/present';
import type { User } from '../lib/types';
import { useSession } from '../session';
import type { Theme } from '../theme';
import { AccountBody, AccountSheet } from './AccountSheet';
import { Icon } from './Icon';
import type { Destination } from './DeliverTo';
import { FoodHome, nearOf } from './FoodHome';
import { NewOrderSheet } from './NewOrderSheet';
import { RestaurantPage } from './RestaurantPage';
import { OrderDetail } from './OrderDetail';
import { OrderRow } from './OrderList';
import { Avatar, Sheet, Waiting } from './ui';

// What a signed-in customer sees: their food, their orders and their account. Staff keep the
// dispatch dashboard (Shell); nothing from it (stage columns, totals, alerts, People, Menu admin)
// is drawn here. The server decides what a customer may do either way; this only draws.

export type CustomerTab = 'food' | 'orders' | 'account';

const TABS: { key: CustomerTab; label: string; icon: IconName }[] = [
  { key: 'food', label: 'Food', icon: 'flame' },
  { key: 'orders', label: 'Orders', icon: 'tray' },
  { key: 'account', label: 'Account', icon: 'person' },
];

export function CustomerApp(props: { theme: Theme; onTheme(t: Theme): void }) {
  return (
    <CartProvider>
      <CustomerView {...props} />
    </CartProvider>
  );
}

function CustomerView({ theme, onTheme }: { theme: Theme; onTheme(t: Theme): void }) {
  const { user } = useSession() as { user: User };
  const cart = useCart();
  const { orders, loaded, online, offlineMessage, refresh } = useOrders(true);

  const [tab, setTab] = useState<CustomerTab>('food');
  const [sheet, setSheet] = useState<'account' | 'cart' | 'order' | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  // where the food goes, kept while the customer moves between the Food home and a restaurant page
  const [destination, setDestination] = useState<Destination | null>(null);
  // the restaurant page that is open on the Food tab; `visit` remounts it when it is opened again
  const [shop, setShop] = useState<{ id: string; addItemId?: string; visit: number } | null>(null);

  const newestFirst = useMemo(
    () => [...orders].sort((a, b) => Date.parse(b.createdAt ?? '') - Date.parse(a.createdAt ?? '') || 0),
    [orders],
  );
  const selected = selectedId ? orders.find((o) => o.id === selectedId) ?? null : null;

  // Escape closes the open order when no sheet is on top of it.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('dialog[open]')) setSelectedId(null);
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, []);

  const openOrder = useCallback((id: string) => { setTab('orders'); setSelectedId(id); }, []);
  const first = user.name.trim().split(/\s+/)[0] || 'there';
  // A <dialog> says it closed a moment after it is told to, so going straight from one sheet to another
  // (the cart's Check out) must not let the first one's close shut the second.
  const closeSheet = (which: 'account' | 'cart' | 'order') => () => setSheet((s) => (s === which ? null : s));

  return (
    <div className="capp">
      <a className="skip" href="#content">Skip to the content</a>

      <header className="capp-head">
        <div className="capp-brand">
          <svg className="mark" viewBox="0 0 32 32" aria-hidden="true">
            <rect width="32" height="32" rx="8" fill="currentColor" />
            <path d="M7 11h13M5 16h17M9 21h12" stroke="#fff" strokeWidth="3" strokeLinecap="round" />
            <path d="M22 9l6 7-6 7" fill="none" stroke="#fff" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          <span>SwiftDrop</span>
        </div>
        <nav className="ctabs" aria-label="Sections">
          {TABS.map((t) => (
            <button key={t.key} type="button" className={`ctab ${tab === t.key ? 'active' : ''}`} aria-current={tab === t.key ? 'page' : undefined}
              onClick={() => { if (t.key === 'food' && tab === 'food') setShop(null); setTab(t.key); }}>
              <Icon name={t.icon} size={22} />
              <span>{t.label}</span>
            </button>
          ))}
        </nav>
        <div className="capp-tools">
          <button type="button" className="icon-btn cart-btn" aria-haspopup="dialog" onClick={() => setSheet('cart')}
            aria-label={cart.count === 0 ? 'Cart, empty' : `Cart, ${cart.count} ${cart.count === 1 ? 'item' : 'items'}`}>
            <Icon name="bag" size={22} />
            {cart.count > 0 && <span className="badge" aria-hidden="true">{cart.count}</span>}
          </button>
          <button type="button" className="icon-btn" aria-haspopup="dialog" onClick={() => setSheet('account')} aria-label={`Your account, ${user.name}`}>
            <Avatar name={user.name} />
          </button>
        </div>
      </header>

      <main className="capp-main" id="content" tabIndex={-1}>
        {online === false && <div className="banner" role="alert">{offlineMessage}</div>}

        {tab === 'food' && !shop && (
          <section aria-labelledby="food-title">
            <div className="page-head">
              <div>
                <h1 id="food-title">{`${greeting()}, ${first}`}</h1>
                <p className="sub">What would you like to eat?</p>
              </div>
              <button className="btn tinted head-order" type="button" onClick={() => setSheet('order')}>Start an order</button>
            </div>
            {/* the order sheet has its own address and search; one of each at a time */}
            {sheet !== 'order' && (
              <FoodHome destination={destination} onDestination={setDestination}
                onOpen={(id, itemId) => setShop((s) => ({ id, addItemId: itemId, visit: (s?.visit ?? 0) + 1 }))} />
            )}
          </section>
        )}
        {tab === 'food' && shop && sheet !== 'order' && (
          <RestaurantPage key={shop.visit} restaurantId={shop.id} addItemId={shop.addItemId} near={nearOf(destination)}
            onBack={() => setShop(null)} onViewCart={() => setSheet('cart')} />
        )}

        {tab === 'orders' && (
          <section aria-labelledby="orders-title">
            <div className="page-head">
              <div>
                <h1 id="orders-title">Your orders</h1>
                <p className="sub">Newest first. Tap one to see where it is.</p>
              </div>
            </div>
            {!loaded ? (
              <div className="empty"><Waiting label="Loading orders" /><p>Loading orders</p></div>
            ) : newestFirst.length === 0 ? (
              <div className="empty">
                <Icon name="tray" size={44} />
                <h3>No orders yet</h3>
                <p>What you order shows up here.</p>
                <button className="btn tinted" type="button" onClick={() => setTab('food')}>Find food</button>
              </div>
            ) : (
              <ul className="list">
                {newestFirst.map((o) => <OrderRow key={o.id} order={o} selected={o.id === selectedId} showCustomer={false} onSelect={setSelectedId} />)}
              </ul>
            )}
          </section>
        )}

        {tab === 'account' && (
          <section aria-labelledby="account-title" className="capp-account">
            <div className="page-head"><div><h1 id="account-title">Account</h1></div></div>
            <AccountBody open user={user} theme={theme} onTheme={onTheme} onClose={() => {}} />
          </section>
        )}
      </main>

      <div className={`scrim ${selected ? 'show' : ''}`} onClick={() => setSelectedId(null)} hidden={!selected} />
      <aside className={`inspector ${selected ? 'open' : ''}`} aria-label="Order details" aria-hidden={!selected}>
        {selected && <OrderDetail order={selected} role={user.role} canSeeHistory={false} onClose={() => setSelectedId(null)} onChanged={refresh} />}
      </aside>

      <Sheet open={sheet === 'cart'} onClose={closeSheet('cart')} title="Cart" className="form-sheet"
        trailing={<button type="button" className="text-btn strong" onClick={() => setSheet(null)}>Done</button>}>
        <div className="sheet-form">
          {cart.count === 0 ? (
            <div className="empty">
              <Icon name="bag" size={44} />
              <h3>Your cart is empty</h3>
              <p>Add food from a restaurant and it waits here.</p>
              <button className="btn tinted" type="button" onClick={() => { setSheet(null); setTab('food'); }}>Find food</button>
            </div>
          ) : (
            <>
              {cart.restaurantName && <h3 className="group-title">{cart.restaurantName}</h3>}
              <ul className="list cart-lines" aria-label="In your cart">
                {cart.lines.map((l) => (
                  <li key={l.menuItemId} className="kv">
                    <span>{l.quantity} × {l.name}</span><span className="price-tag">{money(l.unitPriceCents * l.quantity)}</span>
                  </li>
                ))}
              </ul>
              <div className="order-total"><span>Total</span><strong>{money(cart.totalCents)}</strong></div>
              <p className="hint">From the menu prices you saw. The restaurant's price when you order is the one that counts.</p>
              <button className="btn primary wide" type="button" onClick={() => setSheet('order')}>Check out</button>
            </>
          )}
        </div>
      </Sheet>

      <NewOrderSheet open={sheet === 'order'} user={user} placeForOthers={false} onClose={closeSheet('order')}
        start={cart.restaurantId ? {
          restaurantId: cart.restaurantId,
          quantities: Object.fromEntries(cart.lines.map((l) => [l.menuItemId, l.quantity])),
          destination,
        } : undefined}
        onPlaced={async (id) => { cart.clear(); setShop(null); await refresh(); openOrder(id); }} />
      <AccountSheet open={sheet === 'account'} user={user} theme={theme} onTheme={onTheme} onClose={closeSheet('account')} />
    </div>
  );
}
