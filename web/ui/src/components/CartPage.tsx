import { useRef, useState } from 'react';
import { api } from '../api';
import { useCart } from '../hooks/cart';
import { checkoutBody, checkoutProblem, MAX_LINE_QUANTITY, money, newOrderId, type CheckoutDestination } from '../lib/logic';
import type { PaymentType } from '../lib/types';
import { useToast } from '../toasts';
import { DeliverTo, type Destination } from './DeliverTo';
import { Icon } from './Icon';

interface Props {
  destination: Destination | null;
  onDestination(next: Destination | null): void;
  onBack(): void;
  /** open the cart's restaurant, to add more */
  onShop(restaurantId: string): void;
  onFindFood(): void;
  /** the order is placed (the cart is already empty): open it */
  onPlaced(id: string): Promise<void> | void;
}

const PAY: { value: PaymentType; label: string; note: string }[] = [
  { value: 'card', label: 'Card', note: 'Pay now, from the order' },
  { value: 'wallet', label: 'Wallet', note: 'Pay now, from the order' },
  { value: 'cash', label: 'Cash', note: 'Pay the driver at the door' },
];

// The cart page: what is in the cart, where it goes, how it is paid, and Place order. The total is
// from the menu prices the cart holds and is only shown; the server prices the order from the menu and
// checks the restaurant is open, every item is available and the address is within its reach. When it
// says no, its own words are shown and the cart is kept.
export function CartPage({ destination, onDestination, onBack, onShop, onFindFood, onPlaced }: Props) {
  const cart = useCart();
  const toast = useToast();
  const [paymentType, setPaymentType] = useState<PaymentType>('card');
  const [problem, setProblem] = useState('');
  const [busy, setBusy] = useState(false);
  // One order number per thing being ordered: pressing Place order again for the same cart, address and
  // payment (after a reply that never came) sends the same number, so it can never become two orders.
  // Any change to what is ordered gets a new number.
  const attempt = useRef<{ what: string; id: string } | null>(null);

  const { body, problem: missing } = checkoutBody({
    id: '', cart, paymentType, destination: destination as CheckoutDestination | null,
  });

  async function place() {
    setProblem('');
    if (!body) { setProblem(missing); return; }
    const what = JSON.stringify(body);
    if (attempt.current?.what !== what) attempt.current = { what, id: newOrderId() };
    const id = attempt.current.id;
    setBusy(true);
    const res = await api('POST', '/orders', { ...body, id });
    let placed = res.ok;
    if (res.status === 409) {
      // that number is taken: by this very order if an earlier press got through, else by someone else's
      const mine = await api('GET', `/orders/${encodeURIComponent(id)}`);
      placed = mine.ok;
      if (mine.status === 404) { attempt.current = null; setProblem('That order number was taken. Press Place order again.'); }
      // not known yet: keep the number, so pressing again still cannot make a second order
      else if (!placed) setProblem(checkoutProblem(mine.status, mine.data));
    } else if (!res.ok) {
      setProblem(checkoutProblem(res.status, res.data));
    }
    setBusy(false);
    if (!placed) return;
    attempt.current = null;
    cart.clear();
    toast(`Order ${id} placed`, 'ok');
    await onPlaced(id);
  }

  return (
    <section className="cart-page" aria-labelledby="cart-title">
      <button type="button" className="text-btn back" onClick={onBack}>
        <Icon name="chevron" size={16} /> Back
      </button>
      <div className="page-head"><div><h1 id="cart-title">Your cart</h1></div></div>

      {cart.count === 0 ? (
        <div className="empty">
          <Icon name="bag" size={44} />
          <h3>Your cart is empty</h3>
          <p>Add food from a restaurant and it waits here.</p>
          <button className="btn tinted" type="button" onClick={onFindFood}>Find food</button>
        </div>
      ) : (
        <>
          <div className="food-panel">
            <div className="cart-from">
              <h3 className="group-title">{cart.restaurantName || 'Your restaurant'}</h3>
              {cart.restaurantId && <button type="button" className="text-btn" onClick={() => onShop(cart.restaurantId!)}>Add more</button>}
            </div>
            <ul className="form-group menu-pick cart-lines" aria-label="In your cart">
              {cart.lines.map((l) => {
                const item = { id: l.menuItemId, restaurantId: cart.restaurantId!, name: l.name, priceCents: l.unitPriceCents };
                return (
                  <li key={l.menuItemId} className="pick-row">
                    <span className="row-main">
                      <span className="row-title">{l.name}</span>
                      <span className="row-sub">{money(l.unitPriceCents)} each</span>
                      <span className="price-tag" aria-label={`${l.name} comes to ${money(l.unitPriceCents * l.quantity)}`}>{money(l.unitPriceCents * l.quantity)}</span>
                    </span>
                    <span className="stepper" role="group" aria-label={`Quantity of ${l.name}`}>
                      <button type="button" className="icon-btn" aria-label={`Fewer ${l.name}`} disabled={l.quantity <= 1} onClick={() => cart.setQuantity(item, l.quantity - 1)}>−</button>
                      <output aria-label={`${l.name} in cart`}>{l.quantity}</output>
                      <button type="button" className="icon-btn" aria-label={`More ${l.name}`} disabled={l.quantity >= MAX_LINE_QUANTITY} onClick={() => cart.setQuantity(item, l.quantity + 1)}><Icon name="plus" size={16} /></button>
                    </span>
                    <button type="button" className="text-btn danger-text" aria-label={`Remove ${l.name}`} onClick={() => cart.remove(l.menuItemId)}>Remove</button>
                  </li>
                );
              })}
            </ul>
          </div>

          <div className="food-panel"><DeliverTo allowSaved value={destination} onChange={onDestination} /></div>

          <fieldset className="food-panel pay-pick">
            <legend className="group-title">How you pay</legend>
            {PAY.map((p) => (
              <label key={p.value} className="pay-option">
                <input type="radio" name="pay" value={p.value} checked={paymentType === p.value} onChange={() => setPaymentType(p.value)} />
                <span className="row-main"><span className="row-title">{p.label}</span><span className="row-sub">{p.note}</span></span>
              </label>
            ))}
          </fieldset>

          <div className="food-panel">
            <div className="order-total"><span>Total</span><strong>{money(cart.totalCents)}</strong></div>
            <p className="hint">From the menu prices you saw. The restaurant prices the order when you place it, and that price is the one that counts.</p>
            {!cart.kept && <p className="hint" role="note">This browser is not keeping your cart, so it is gone if you close the page.</p>}
            {problem && <p className="form-error" role="alert">{problem}</p>}
            <button className="btn primary wide" type="button" disabled={busy} onClick={() => void place()}>
              {busy ? 'Placing your order' : `Place order · ${money(cart.totalCents)}`}
            </button>
          </div>
        </>
      )}
    </section>
  );
}
