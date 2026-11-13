import { useEffect, useRef, useState } from 'react';
import { api } from '../api';
import { useCart } from '../hooks/cart';
import { cardStatusText, errorMessage, filterMenu, formatDistance, MAX_LINE_QUANTITY, money, restaurantCards } from '../lib/logic';
import type { MenuItem, Restaurant } from '../lib/types';
import { Icon } from './Icon';
import { Pill, Waiting } from './ui';

interface Props {
  restaurantId: string;
  /** where the food is going, when chosen: the page says how far it is and whether it delivers there */
  near: { lat: number; lng: number; name: string } | null;
  /** an item chosen in the search results: one of it goes in the cart when the menu has loaded */
  addItemId?: string;
  onBack(): void;
  onViewCart(): void;
}

// One restaurant's whole menu, from GET /restaurants/{id}/menu (a customer is only shown what is
// available; a closed restaurant answers 404). The search box and the vegetarian filter work on the
// page; the steppers put items in the cart. Prices here are for showing: the server prices the order.
export function RestaurantPage({ restaurantId, near, addItemId, onBack, onViewCart }: Props) {
  const cart = useCart();
  const [restaurant, setRestaurant] = useState<Restaurant | null>(null);
  const [items, setItems] = useState<MenuItem[] | null>(null);
  const [problem, setProblem] = useState('');
  const [closed, setClosed] = useState(false);
  const [text, setText] = useState('');
  const [vegOnly, setVegOnly] = useState(false);
  const added = useRef(false);
  // an item the customer wants that would empty a cart of another restaurant: they are asked first
  const [pending, setPending] = useState<MenuItem | null>(null);

  useEffect(() => {
    let current = true;
    void api<{ restaurant: Restaurant; items: MenuItem[] | null }>('GET', `/restaurants/${encodeURIComponent(restaurantId)}/menu`).then((res) => {
      if (!current) return;
      if (res.status === 404) { setClosed(true); setItems([]); return; }
      if (!res.ok || !res.data) { setProblem(errorMessage(res.status, res.data)); setItems([]); return; }
      setRestaurant(res.data.restaurant);
      setItems(res.data.items ?? []);
    });
    return () => { current = false; };
  }, [restaurantId]);

  // one of the item chosen in the search results, once (a re-render must not add another), and never
  // by replacing a cart of another restaurant unseen: then the customer is asked first
  useEffect(() => {
    if (added.current || !addItemId || !items || !restaurant) return;
    added.current = true;
    const item = items.find((m) => m.id === addItemId);
    if (!item) return;
    if (!cart.setQuantity(item, cart.quantityOf(item.id) + 1, { restaurantName: restaurant.name }) && cart.wouldReplace(item.restaurantId)) setPending(item);
  }, [addItemId, items, restaurant, cart]);

  // a stepper: an item of this restaurant while the cart holds another's asks before emptying it
  const change = (m: MenuItem, n: number) => {
    if (!cart.setQuantity(m, n, { restaurantName: restaurant?.name }) && cart.wouldReplace(m.restaurantId) && n > 0) setPending(m);
  };
  const startNewCart = () => {
    if (pending) cart.setQuantity(pending, 1, { restaurantName: restaurant?.name, replace: true });
    setPending(null);
  };

  const shown = filterMenu(items, { text, vegOnly });
  const card = restaurant ? restaurantCards([restaurant], near)[0] : undefined;
  const otherCart = cart.restaurantId !== null && cart.restaurantId !== restaurantId && cart.count > 0;
  const ours = cart.restaurantId === restaurantId && cart.count > 0;

  return (
    <section className="rest-page" aria-labelledby="rest-title">
      <button type="button" className="text-btn back" onClick={onBack}>
        <Icon name="chevron" size={16} /> Restaurants
      </button>

      {closed ? (
        <div className="empty">
          <h3 id="rest-title">This restaurant is not taking orders</h3>
          <p>It has closed since the list was loaded, or it is no longer here.</p>
          <button className="btn tinted" type="button" onClick={onBack}>Back to restaurants</button>
        </div>
      ) : (
        <>
          <div className="page-head">
            <div>
              <h1 id="rest-title">{restaurant?.name ?? 'Menu'}</h1>
              {restaurant && (
                <p className="sub">
                  {[restaurant.cuisine, card?.distanceKm !== undefined ? `${formatDistance(card.distanceKm)} away` : ''].filter(Boolean).join(' · ')}
                  {card && card.status !== 'open' && <> <Pill tone={card.status === 'delivers' ? 'good' : 'neutral'}>{cardStatusText(card, near?.name)}</Pill></>}
                </p>
              )}
            </div>
          </div>

          {problem && <p className="form-error" role="alert">{problem}</p>}
          {pending ? (
            <div className="confirm-box" role="alertdialog" aria-labelledby="switch-title" aria-describedby="switch-why">
              <h3 id="switch-title">Start a new cart?</h3>
              <p id="switch-why">
                Your cart has {cart.count} {cart.count === 1 ? 'item' : 'items'} from {cart.restaurantName || 'another restaurant'}.
                An order comes from one restaurant, so adding {pending.name} empties it.
              </p>
              <div className="confirm-actions">
                <button type="button" className="btn tinted" onClick={() => setPending(null)} autoFocus>Keep my cart</button>
                <button type="button" className="btn primary" onClick={startNewCart}>Start a new cart</button>
              </div>
            </div>
          ) : otherCart && (
            <p className="hint cart-note" role="note">
              Your cart has food from {cart.restaurantName || 'another restaurant'}. Adding something here asks before it starts a new cart.
            </p>
          )}

          <div className="form-group">
            <label className="field"><span>Search</span>
              <input type="search" value={text} placeholder="Search this menu" autoComplete="off" aria-label="Search this menu"
                onChange={(e) => setText(e.target.value)} />
            </label>
          </div>
          <div className="find-tools">
            <label className="chip check">
              <input type="checkbox" checked={vegOnly} onChange={(e) => setVegOnly(e.target.checked)} /> Vegetarian only
            </label>
          </div>

          {items === null && <div className="empty small"><Waiting label="Loading the menu" /><p>Loading the menu</p></div>}
          {items !== null && items.length === 0 && !problem && <p className="why">Nothing is available from this restaurant right now.</p>}
          {items !== null && items.length > 0 && shown.length === 0 && (
            <p className="why">{vegOnly && !text.trim() ? 'Nothing vegetarian on this menu right now.' : 'Nothing on this menu matches that.'}</p>
          )}
          {shown.length > 0 && (
            <ul className="form-group menu-pick rest-menu" aria-label="Menu">
              {shown.map((m) => {
                const q = cart.quantityOf(m.id);
                const set = (n: number) => change(m, n);
                return (
                  <li key={m.id} className="pick-row">
                    <span className="row-main">
                      <span className="row-title">{m.name}{m.veg && <> <Pill tone="good">Veg</Pill></>}</span>
                      {m.description && <span className="row-sub">{m.description}</span>}
                      <span className="price-tag">{money(m.priceCents)}</span>
                    </span>
                    <span className="stepper" role="group" aria-label={`Quantity of ${m.name}`}>
                      <button type="button" className="icon-btn" aria-label={`Fewer ${m.name}`} disabled={q === 0} onClick={() => set(q - 1)}>−</button>
                      <output aria-label={`${m.name} in cart`}>{q}</output>
                      <button type="button" className="icon-btn" aria-label={`More ${m.name}`} disabled={q >= MAX_LINE_QUANTITY || cart.isFull(m.id)} onClick={() => set(q + 1)}><Icon name="plus" size={16} /></button>
                    </span>
                  </li>
                );
              })}
            </ul>
          )}

          {ours && (
            <div className="cart-bar">
              <button type="button" className="btn primary wide" onClick={onViewCart}>
                View cart · {cart.count} {cart.count === 1 ? 'item' : 'items'} · {money(cart.totalCents)}
              </button>
            </div>
          )}
        </>
      )}
    </section>
  );
}
