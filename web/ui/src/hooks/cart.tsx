import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { cartKey, MAX_LINE_QUANTITY, MAX_ORDER_LINES, readCart, type StoredCart } from '../lib/logic';

// The customer's cart, behind one hook so the screens never care where it is kept.
//
// It is kept in the browser (localStorage), one cart per signed-in account, so a reload or signing
// out and back in finds it again. Storage can be blocked (a private window, a strict setting) or full:
// every read and write is wrapped, and then the cart lives in memory for as long as the page is open
// and `kept` says so. What is read back is checked line by line (readCart), never trusted.
//
// One restaurant at a time: an item of another restaurant is refused unless the caller says the
// customer agreed to start a new cart (`replace`), so nothing can empty a cart unseen.
// Prices here are the menu prices the customer saw, for showing a total; the server prices the order.

export interface CartLine { menuItemId: string; name: string; unitPriceCents: number; quantity: number }

/** What a stepper knows about the item it changes: enough to make a cart line. */
export interface CartItem { id: string; restaurantId: string; name: string; priceCents: number }

export interface SetOptions {
  /** the restaurant's name, shown on the cart page (a later change without one keeps the name it has) */
  restaurantName?: string;
  /** the customer agreed to empty a cart of another restaurant and start a new one with this item */
  replace?: boolean;
}

export interface CartValue {
  restaurantId: string | null;
  restaurantName: string;
  lines: CartLine[];
  /** how many things are in the cart, counting quantities: what the cart button shows */
  count: number;
  /** price times quantity from the menu prices the cart holds: display only, the server prices the order */
  totalCents: number;
  /** false when the browser would not keep the cart: it is gone when the page closes */
  kept: boolean;
  quantityOf(menuItemId: string): number;
  /** true when adding an item of this restaurant would empty a cart of another one */
  wouldReplace(restaurantId: string): boolean;
  /** true when this item cannot be added as a new line (the cart has the most lines an order may have) */
  isFull(menuItemId: string): boolean;
  /**
   * Sets how many of an item are in the cart, kept between 0 and the server's 50; 0 removes the line.
   * Answers false, changing nothing, for an item of another restaurant without `replace`, or a new line
   * past the server's 30.
   */
  setQuantity(item: CartItem, quantity: number, options?: SetOptions): boolean;
  remove(menuItemId: string): void;
  clear(): void;
}

const Ctx = createContext<CartValue | null>(null);

export function useCart(): CartValue {
  const v = useContext(Ctx);
  if (!v) throw new Error('useCart needs a CartProvider');
  return v;
}

const EMPTY: StoredCart = { restaurantId: null, restaurantName: '', lines: [] };

function load(key: string | null): { cart: StoredCart; kept: boolean } {
  if (!key) return { cart: EMPTY, kept: false };
  try {
    return { cart: readCart(window.localStorage.getItem(key)), kept: true };
  } catch {
    return { cart: EMPTY, kept: false };
  }
}

/** Writes the cart (or removes it when empty); false when the browser would not. */
function save(key: string, cart: StoredCart): boolean {
  try {
    if (cart.lines.length === 0) window.localStorage.removeItem(key);
    else window.localStorage.setItem(key, JSON.stringify({ restaurantId: cart.restaurantId, restaurantName: cart.restaurantName, lines: cart.lines }));
    return true;
  } catch {
    return false;
  }
}

/** `userId` names the account whose cart this is; null keeps it in memory only. */
export function CartProvider({ children, userId }: { children: ReactNode; userId: string | null }) {
  const key = userId ? cartKey(userId) : null;
  const [first] = useState(() => load(key));
  const [cart, setCart] = useState<StoredCart>(first.cart);
  const [kept, setKept] = useState(first.kept);
  // the cart as it is right now, so setQuantity can answer at once whether it changed anything
  const now = useRef(cart);

  const commit = useCallback((next: StoredCart) => { now.current = next; setCart(next); }, []);

  useEffect(() => {
    // writing back what another tab stored is harmless: the same text fires no event there
    if (key) setKept(save(key, cart));
  }, [key, cart]);

  // the same account in another tab changed the cart: show that one, so the two never overwrite each other
  useEffect(() => {
    if (!key) return;
    const onStorage = (e: StorageEvent) => {
      if (e.key !== key && e.key !== null) return; // null: the other tab cleared all storage
      commit(readCart(e.key === null ? null : e.newValue));
    };
    window.addEventListener('storage', onStorage);
    return () => window.removeEventListener('storage', onStorage);
  }, [key, commit]);

  const setQuantity = useCallback((item: CartItem, quantity: number, options: SetOptions = {}) => {
    const s = now.current;
    const n = Number.isFinite(quantity) ? Math.max(0, Math.min(MAX_LINE_QUANTITY, Math.trunc(quantity))) : 0;
    const other = s.restaurantId !== item.restaurantId;
    if (other && n === 0) return false; // nothing of another restaurant to remove
    if (other && s.lines.length > 0 && !options.replace) return false; // the customer has not agreed to lose their cart
    const lines = other ? [] : s.lines;
    const at = lines.findIndex((l) => l.menuItemId === item.id);
    if (at === -1 && n > 0 && lines.length >= MAX_ORDER_LINES) return false;
    const line: CartLine = { menuItemId: item.id, name: item.name, unitPriceCents: item.priceCents, quantity: n };
    const next = n === 0 ? lines.filter((l) => l.menuItemId !== item.id) : at === -1 ? [...lines, line] : lines.map((l, i) => (i === at ? line : l));
    if (next.length === 0) commit(EMPTY);
    else commit({ restaurantId: item.restaurantId, restaurantName: (other ? options.restaurantName : options.restaurantName || s.restaurantName) ?? '', lines: next });
    return true;
  }, [commit]);

  const remove = useCallback((menuItemId: string) => {
    const next = now.current.lines.filter((l) => l.menuItemId !== menuItemId);
    commit(next.length === 0 ? EMPTY : { ...now.current, lines: next });
  }, [commit]);

  const value = useMemo<CartValue>(() => ({
    restaurantId: cart.restaurantId,
    restaurantName: cart.restaurantName,
    lines: cart.lines as CartLine[],
    count: cart.lines.reduce((n, l) => n + l.quantity, 0),
    totalCents: cart.lines.reduce((sum, l) => sum + l.unitPriceCents * l.quantity, 0),
    kept,
    quantityOf: (id: string) => cart.lines.find((l) => l.menuItemId === id)?.quantity ?? 0,
    wouldReplace: (restaurantId: string) => cart.lines.length > 0 && cart.restaurantId !== restaurantId,
    isFull: (id: string) => cart.lines.length >= MAX_ORDER_LINES && !cart.lines.some((l) => l.menuItemId === id),
    setQuantity,
    remove,
    clear: () => commit(EMPTY),
  }), [cart, kept, setQuantity, remove, commit]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
