import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react';
import { MAX_LINE_QUANTITY } from '../lib/logic';

// The customer's cart, behind one hook so the screens never care where it is kept.
// For now it lives in memory and is gone when the page reloads or the customer signs out
// (CartProvider sits inside the customer view, so it unmounts with it). The cart item on the
// roadmap gives it localStorage per user and a confirm before switching restaurants; only this
// file changes then.

export interface CartLine { menuItemId: string; name: string; unitPriceCents: number; quantity: number }

/** What a stepper knows about the item it changes: enough to make a cart line. */
export interface CartItem { id: string; restaurantId: string; name: string; priceCents: number }

export interface CartValue {
  restaurantId: string | null;
  restaurantName: string;
  lines: CartLine[];
  /** how many things are in the cart, counting quantities: what the cart button shows */
  count: number;
  /** price times quantity from the menu prices the cart holds: display only, the server prices the order */
  totalCents: number;
  quantityOf(menuItemId: string): number;
  /**
   * Sets how many of an item are in the cart, kept between 0 and the server's 50; 0 removes the line.
   * An item of another restaurant starts a new cart (one restaurant at a time).
   */
  setQuantity(item: CartItem, quantity: number, restaurantName?: string): void;
  clear(): void;
}

const Ctx = createContext<CartValue | null>(null);

export function useCart(): CartValue {
  const v = useContext(Ctx);
  if (!v) throw new Error('useCart needs a CartProvider');
  return v;
}

interface State { restaurantId: string | null; restaurantName: string; lines: CartLine[] }

export function CartProvider({ children, initial = [], restaurantId: startId = null }: {
  children: ReactNode; initial?: CartLine[]; restaurantId?: string | null;
}) {
  const [state, setState] = useState<State>({ restaurantId: startId, restaurantName: '', lines: initial });

  const setQuantity = useCallback((item: CartItem, quantity: number, restaurantName = '') => {
    const n = Number.isFinite(quantity) ? Math.max(0, Math.min(MAX_LINE_QUANTITY, Math.trunc(quantity))) : 0;
    setState((s) => {
      const fresh = s.restaurantId !== item.restaurantId;
      if (fresh && n === 0) return s; // nothing of another restaurant to remove: keep the cart as it is
      const lines = fresh ? [] : s.lines;
      const rest = lines.filter((l) => l.menuItemId !== item.id);
      const at = lines.findIndex((l) => l.menuItemId === item.id);
      const line: CartLine = { menuItemId: item.id, name: item.name, unitPriceCents: item.priceCents, quantity: n };
      const next = n === 0 ? rest : at === -1 ? [...lines, line] : lines.map((l, i) => (i === at ? line : l));
      if (next.length === 0) return { restaurantId: null, restaurantName: '', lines: [] };
      return {
        restaurantId: item.restaurantId,
        restaurantName: (fresh ? restaurantName : restaurantName || s.restaurantName),
        lines: next,
      };
    });
  }, []);

  const value = useMemo<CartValue>(() => ({
    restaurantId: state.restaurantId,
    restaurantName: state.restaurantName,
    lines: state.lines,
    count: state.lines.reduce((n, l) => n + l.quantity, 0),
    totalCents: state.lines.reduce((sum, l) => sum + l.unitPriceCents * l.quantity, 0),
    quantityOf: (id: string) => state.lines.find((l) => l.menuItemId === id)?.quantity ?? 0,
    setQuantity,
    clear: () => setState({ restaurantId: null, restaurantName: '', lines: [] }),
  }), [state, setQuantity]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
