import { useMemo, useState, type FormEvent } from 'react';
import { api } from '../api';
import { errorMessage, money, newOrderId, priceToCents, validateOrderForm, type OrderFormErrors } from '../lib/logic';
import type { PaymentType, User } from '../lib/types';
import { useToast } from '../toasts';
import { Icon } from './Icon';
import { Sheet } from './ui';

const MENU: [string, number][] = [
  ['Smash burger', 9.5], ['Fries', 4], ['Lemonade', 3.25], ['Pad thai', 13.9], ['Margherita pizza', 12], ['Miso soup', 3.5], ['Tiramisu', 6.75],
];

interface Row { key: number; name: string; quantity: string; price: string }

let rowKey = 1;
const newRow = (v: Partial<Row> = {}): Row => ({ key: rowKey++, name: '', quantity: '1', price: '', ...v });

interface Props {
  open: boolean;
  user: User;
  /** staff may place an order for any customer; a customer orders for themselves */
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
  const [customerId, setCustomerId] = useState(placeForOthers ? 'cust-1' : user.id);
  const [restaurantId, setRestaurantId] = useState('rest-1');
  const [rows, setRows] = useState<Row[]>(() => [newRow({ name: 'Smash burger', price: '9.50' })]);
  const [errors, setErrors] = useState<OrderFormErrors>({});
  const [busy, setBusy] = useState(false);

  const total = useMemo(() => rows.reduce((sum, r) => {
    const cents = priceToCents(r.price);
    const qty = Number(r.quantity);
    return cents === null || !Number.isInteger(qty) || qty < 1 ? sum : sum + cents * qty;
  }, 0), [rows]);

  const setRow = (key: number, patch: Partial<Row>) => setRows((rs) => rs.map((r) => (r.key === key ? { ...r, ...patch } : r)));

  function fillSample() {
    const picks = [...MENU].sort(() => Math.random() - 0.5).slice(0, 2 + Math.floor(Math.random() * 2));
    setRows(picks.map(([name, price]) => newRow({ name, quantity: String(1 + Math.floor(Math.random() * 2)), price: price.toFixed(2) })));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const result = validateOrderForm({
      id, customerId, restaurantId, paymentType,
      items: rows.map(({ name, quantity, price }) => ({ name, quantity, price })),
    });
    setErrors(result.errors);
    if (!result.ok || !result.payload) return;
    setBusy(true);
    const res = await api('POST', '/orders', result.payload);
    setBusy(false);
    if (!res.ok) {
      const message = errorMessage(res.status, res.data);
      if (res.status === 409) setErrors({ id: message }); else toast(message, 'bad');
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

  return (
    <form onSubmit={submit} noValidate className="sheet-form">
      <div className="form-group">
        <label className="field"><span>Order id</span>
          <input value={id} autoComplete="off" spellCheck={false} onChange={(e) => setId(e.target.value)} aria-invalid={Boolean(err('id'))} />
        </label>
        <label className="field"><span>Pays by</span>
          <select value={paymentType} onChange={(e) => setPaymentType(e.target.value as PaymentType)}>
            <option value="card">Card</option>
            <option value="wallet">Wallet</option>
            <option value="cash">Cash on delivery</option>
          </select>
        </label>
        {placeForOthers && (
          <label className="field"><span>Customer</span>
            <input value={customerId} autoComplete="off" spellCheck={false} onChange={(e) => setCustomerId(e.target.value)} aria-invalid={Boolean(err('customerId'))} />
          </label>
        )}
        <label className="field"><span>Restaurant</span>
          <input value={restaurantId} autoComplete="off" spellCheck={false} onChange={(e) => setRestaurantId(e.target.value)} aria-invalid={Boolean(err('restaurantId'))} />
        </label>
      </div>
      {['id', 'customerId', 'restaurantId', 'paymentType'].map((k) => err(k) && <p key={k} className="form-error">{err(k)}</p>)}

      <h3 className="group-title">Items</h3>
      <div className="form-group items">
        {rows.map((r, i) => (
          <div className="item-row" key={r.key}>
            <input placeholder="Item" aria-label="Item name" value={r.name} onChange={(e) => setRow(r.key, { name: e.target.value })} />
            <input className="qty" type="number" min="1" step="1" aria-label="Quantity" value={r.quantity} onChange={(e) => setRow(r.key, { quantity: e.target.value })} />
            <input className="price" inputMode="decimal" placeholder="0.00" aria-label="Price in dollars" value={r.price} onChange={(e) => setRow(r.key, { price: e.target.value })} />
            <button type="button" className="icon-btn" aria-label="Remove item" onClick={() => setRows((rs) => rs.filter((x) => x.key !== r.key))}><Icon name="xmark" size={18} /></button>
            {rowErr(i) && <small className="form-error span">{rowErr(i)}</small>}
          </div>
        ))}
      </div>
      {err('items') && <p className="form-error">{err('items')}</p>}
      <div className="item-tools">
        <button type="button" className="text-btn" onClick={() => setRows((rs) => [...rs, newRow()])}>Add item</button>
        <button type="button" className="text-btn" onClick={fillSample}>Fill a sample</button>
        <span className="total">{money(total)}</span>
      </div>

      <p className="hint">Tip: the built-in fake payment gateway declines a card order over <b>$500.00</b> and a wallet order over <b>$100.00</b>, so you can see a decline.</p>
      <button className="btn primary wide" type="submit" disabled={busy}>Place order</button>
    </form>
  );
}
