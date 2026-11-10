import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { api } from '../api';
import {
  errorMessage, fieldErrors, MAX_LINE_QUANTITY, menuTotalCents, money, newOrderId, priceToCents, validateOrderForm, type OrderFormErrors,
} from '../lib/logic';
import type { AddressPayload } from '../lib/logic';
import type { MenuItem, PaymentType, Restaurant, User } from '../lib/types';
import { useToast } from '../toasts';
import { DeliverTo, destinationAddress, type Destination } from './DeliverTo';
import { Icon } from './Icon';
import { NearbySearch } from './NearbySearch';
import { Sheet } from './ui';

// A custom line is for staff only (a phone order, something off the menu): they name it and price it.
interface Row { key: number; name: string; quantity: string; price: string }

let rowKey = 1;
const newRow = (v: Partial<Row> = {}): Row => ({ key: rowKey++, name: '', quantity: '1', price: '', ...v });

interface Props {
  open: boolean;
  user: User;
  /** staff may place an order for any customer, and add a custom line; a customer orders for themselves, from the menu */
  placeForOthers: boolean;
  onClose(): void;
  onPlaced(id: string): Promise<void>;
}

export function NewOrderSheet(props: Props) {
  return (
    <Sheet open={props.open} onClose={props.onClose} title="New order" className="form-sheet"
      leading={<button type="button" className="text-btn" onClick={props.onClose}>Cancel</button>}>
      <OrderForm {...props} />
    </Sheet>
  );
}

function OrderForm({ user, placeForOthers, onClose, onPlaced }: Props) {
  const toast = useToast();
  const [id, setId] = useState(() => newOrderId());
  const [paymentType, setPaymentType] = useState<PaymentType>('card');
  const [customerId, setCustomerId] = useState(placeForOthers ? 'walk-in' : user.id);
  const [customers, setCustomers] = useState<User[]>([]);
  const [restaurants, setRestaurants] = useState<Restaurant[] | null>(null);
  const [restaurantId, setRestaurantId] = useState('');
  const [menu, setMenu] = useState<MenuItem[] | null>(null);
  const [quantities, setQuantities] = useState<Record<string, number>>({});
  const [custom, setCustom] = useState<Row[]>([]);
  const [errors, setErrors] = useState<OrderFormErrors>({});
  const [loadError, setLoadError] = useState('');
  const [busy, setBusy] = useState(false);
  const [destination, setDestination] = useState<Destination | null>(null);
  // an item chosen in the search results, to put in the basket once its restaurant's menu has loaded
  const wanted = useRef<string | null>(null);

  // the restaurants that are open
  useEffect(() => {
    let current = true;
    void api<{ restaurants: Restaurant[] | null }>('GET', '/restaurants').then((res) => {
      if (!current) return;
      if (!res.ok) { setLoadError(errorMessage(res.status, res.data)); return; }
      const list = res.data?.restaurants ?? [];
      setRestaurants(list);
      setRestaurantId((now) => now || list[0]?.id || '');
    });
    return () => { current = false; };
  }, []);

  // staff pick the customer from the accounts there are (or a walk-in, who has none)
  useEffect(() => {
    if (!placeForOthers) return;
    let current = true;
    void api<{ accounts: User[] | null }>('GET', '/users?role=customer&limit=200').then((res) => {
      if (current && res.ok) setCustomers(res.data?.accounts ?? []);
    });
    return () => { current = false; };
  }, [placeForOthers]);

  // the chosen restaurant's menu (only what is available); a new restaurant starts a new basket
  useEffect(() => {
    setQuantities({});
    setMenu(null);
    if (!restaurantId) return;
    let current = true;
    void api<{ items: MenuItem[] | null }>('GET', `/restaurants/${encodeURIComponent(restaurantId)}/menu`).then((res) => {
      if (!current) return;
      if (!res.ok) { setLoadError(errorMessage(res.status, res.data)); setMenu([]); return; }
      setLoadError('');
      setMenu(res.data?.items ?? []);
      if (wanted.current) { // chosen in the search results: one of it, to start
        const itemId = wanted.current;
        wanted.current = null;
        setQuantities({ [itemId]: 1 });
      }
    });
    return () => { current = false; };
  }, [restaurantId]);

  const customCents = useMemo(() => custom.reduce((sum, r) => {
    const cents = priceToCents(r.price);
    const qty = Number(r.quantity);
    return cents === null || !Number.isInteger(qty) || qty < 1 ? sum : sum + cents * qty;
  }, 0), [custom]);
  const total = menuTotalCents(menu ?? [], quantities) + customCents;

  // a restaurant, or an item of it, chosen in the search results
  function choose(chosenRestaurantId: string, itemId?: string) {
    if (chosenRestaurantId === restaurantId) {
      if (itemId) setQuantities((q) => ({ ...q, [itemId]: Math.min(MAX_LINE_QUANTITY, (q[itemId] ?? 0) + 1) }));
      return;
    }
    wanted.current = itemId ?? null;
    setRestaurantId(chosenRestaurantId);
  }

  const setQty = (itemId: string, next: number) =>
    setQuantities((q) => ({ ...q, [itemId]: Math.max(0, Math.min(MAX_LINE_QUANTITY, next)) }));
  const setRow = (key: number, patch: Partial<Row>) => setCustom((rs) => rs.map((r) => (r.key === key ? { ...r, ...patch } : r)));

  async function submit(e: FormEvent) {
    e.preventDefault();
    const result = validateOrderForm({
      id, customerId, restaurantId, paymentType,
      menu: Object.entries(quantities).filter(([, q]) => q > 0).map(([menuItemId, quantity]) => ({ menuItemId, quantity })),
      items: custom.map(({ name, quantity, price }) => ({ name, quantity, price })),
      ...(destination && 'saved' in destination ? { deliveryAddressId: destination.saved.id } : {}),
      ...(destination && 'address' in destination ? { deliveryAddress: destination.address as AddressPayload } : {}),
    });
    // where it is going is required, and the server checks it again (and that the restaurant delivers there)
    const errors = destination ? result.errors : { ...result.errors, deliveryAddress: 'Choose where to deliver' };
    setErrors(errors);
    if (!result.ok || !result.payload || !destination) return;
    setBusy(true);
    const res = await api('POST', '/orders', result.payload);
    setBusy(false);
    if (!res.ok) {
      const message = errorMessage(res.status, res.data);
      if (res.status === 409 && placeForOthers) setErrors({ id: message });
      else if (res.status === 409) { setId(newOrderId()); toast('That order number was taken. Press Place order again.', 'bad'); }
      else if (res.status === 422 && Object.keys(fieldErrors(res.data)).length) setErrors(fieldErrors(res.data));
      else toast(message, 'bad'); // the server's own words: the restaurant closed, an item sold out
      return;
    }
    onClose();
    toast(`Order ${result.payload.id} placed`, 'ok');
    await onPlaced(result.payload.id);
  }

  const err = (k: string) => (typeof errors[k] === 'string' ? (errors[k] as string) : '');
  const rowErr = (i: number) => {
    const e = errors[`items.${i}`];
    return e && typeof e === 'object' ? Object.values(e).join('. ') : '';
  };
  const noRestaurants = restaurants !== null && restaurants.length === 0;
  // the search is near where the order is going, once that is chosen
  const to = destination ? destinationAddress(destination) : null;
  const near = to ? { lat: to.latitude, lng: to.longitude, name: to.label || to.line1 } : null;

  return (
    <>
    <div className="sheet-form deliver-wrap"><DeliverTo allowSaved={!placeForOthers} value={destination} onChange={setDestination} /></div>
    <div className="sheet-form find-wrap"><NearbySearch onChoose={choose} near={near} /></div>
    <form onSubmit={submit} noValidate className="sheet-form">
      <div className="form-group">
        {placeForOthers && (
          <label className="field"><span>Order id</span>
            <input value={id} autoComplete="off" spellCheck={false} onChange={(e) => setId(e.target.value)} aria-invalid={Boolean(err('id'))} />
          </label>
        )}
        <label className="field"><span>Pays by</span>
          <select value={paymentType} onChange={(e) => setPaymentType(e.target.value as PaymentType)}>
            <option value="card">Card</option>
            <option value="wallet">Wallet</option>
            <option value="cash">Cash on delivery</option>
          </select>
        </label>
        {placeForOthers && (
          <label className="field"><span>Customer</span>
            <select value={customerId} onChange={(e) => setCustomerId(e.target.value)} aria-invalid={Boolean(err('customerId'))}>
              <option value="walk-in">Walk-in (no account)</option>
              {customers.map((c) => <option key={c.id} value={c.id}>{c.name} ({c.email})</option>)}
            </select>
          </label>
        )}
        <label className="field"><span>Restaurant</span>
          <select value={restaurantId} disabled={!restaurants?.length} onChange={(e) => setRestaurantId(e.target.value)} aria-invalid={Boolean(err('restaurantId'))}>
            {restaurants === null && <option value="">Loading</option>}
            {noRestaurants && <option value="">None are open</option>}
            {restaurants?.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
          </select>
        </label>
      </div>
      {['id', 'customerId', 'restaurantId', 'paymentType'].map((k) => err(k) && <p key={k} className="form-error">{err(k)}</p>)}
      {loadError && <p className="form-error" role="alert">{loadError}</p>}
      {noRestaurants && <p className="hint">{placeForOthers ? 'No restaurant is open. Open one in Menu.' : 'No restaurant is open right now. Try again later.'}</p>}

      <h3 className="group-title">Menu</h3>
      {menu === null && restaurantId && <p className="why">Loading the menu</p>}
      {menu !== null && menu.length === 0 && restaurantId && <p className="why">Nothing is available from this restaurant right now.</p>}
      {menu !== null && menu.length > 0 && (
        <ul className="form-group menu-pick">
          {menu.map((m) => {
            const q = quantities[m.id] ?? 0;
            return (
              <li key={m.id} className="pick-row">
                <span className="row-main">
                  <span className="row-title">{m.name}</span>
                  {m.description && <span className="row-sub">{m.description}</span>}
                </span>
                <span className="price-tag">{money(m.priceCents)}</span>
                <span className="stepper" role="group" aria-label={`Quantity of ${m.name}`}>
                  <button type="button" className="icon-btn" aria-label={`Fewer ${m.name}`} disabled={q === 0} onClick={() => setQty(m.id, q - 1)}>−</button>
                  <output aria-label={`${m.name} quantity`}>{q}</output>
                  <button type="button" className="icon-btn" aria-label={`More ${m.name}`} disabled={q >= MAX_LINE_QUANTITY} onClick={() => setQty(m.id, q + 1)}><Icon name="plus" size={16} /></button>
                </span>
              </li>
            );
          })}
        </ul>
      )}
      {Object.keys(errors).filter((k) => k.startsWith('menu.')).map((k) => <p key={k} className="form-error">{err(k)}</p>)}

      {placeForOthers && (
        <>
          <h3 className="group-title">Custom lines</h3>
          {custom.length > 0 && (
            <div className="form-group items">
              {custom.map((r, i) => (
                <div className="item-row" key={r.key}>
                  <input placeholder="Item" aria-label="Item name" value={r.name} onChange={(e) => setRow(r.key, { name: e.target.value })} />
                  <input className="qty" type="number" min="1" step="1" aria-label="Quantity" value={r.quantity} onChange={(e) => setRow(r.key, { quantity: e.target.value })} />
                  <input className="price" inputMode="decimal" placeholder="0.00" aria-label="Price in dollars" value={r.price} onChange={(e) => setRow(r.key, { price: e.target.value })} />
                  <button type="button" className="icon-btn" aria-label="Remove item" onClick={() => setCustom((rs) => rs.filter((x) => x.key !== r.key))}><Icon name="xmark" size={18} /></button>
                  {rowErr(i) && <small className="form-error span">{rowErr(i)}</small>}
                </div>
              ))}
            </div>
          )}
          <div className="item-tools">
            <button type="button" className="text-btn" onClick={() => setCustom((rs) => [...rs, newRow()])}>Add a custom line</button>
          </div>
          <p className="hint">For something that is not on the menu: you set its name and price.</p>
        </>
      )}
      {err('items') && <p className="form-error">{err('items')}</p>}
      {err('deliveryAddress') && <p className="form-error">{err('deliveryAddress')}</p>}

      <div className="order-total"><span>Total</span><strong>{money(total)}</strong></div>
      <p className="hint">Prices come from the menu. The built-in fake payment gateway declines a card order over <b>$500.00</b> and a wallet order over <b>$100.00</b>, so you can see a decline.</p>
      <button className="btn primary wide" type="submit" disabled={busy || noRestaurants}>Place order</button>
    </form>
    </>
  );
}
