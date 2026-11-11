import { createContext, useContext, useMemo, useState, type ReactNode } from 'react';

// The customer's cart, behind one hook so the screens never care where it is kept.
// For now it lives in memory and is gone when the page reloads or the customer signs out
// (CartProvider sits inside the customer view, so it unmounts with it). The cart item on the
// roadmap gives it localStorage per user, one restaurant at a time and the quantity limits;
// only this file changes then.

export interface CartLine { menuItemId: string; name: string; unitPriceCents: number; quantity: number }

export interface CartValue {
  restaurantId: string | null;
  lines: CartLine[];
  /** how many things are in the cart, counting quantities: what the cart button shows */
  count: number;
  clear(): void;
}

const Ctx = createContext<CartValue | null>(null);

export function useCart(): CartValue {
  const v = useContext(Ctx);
  if (!v) throw new Error('useCart needs a CartProvider');
  return v;
}

export function CartProvider({ children, initial = [] }: { children: ReactNode; initial?: CartLine[] }) {
  const [lines, setLines] = useState<CartLine[]>(initial);
  const [restaurantId, setRestaurantId] = useState<string | null>(null);
  const value = useMemo<CartValue>(() => ({
    restaurantId,
    lines,
    count: lines.reduce((n, l) => n + l.quantity, 0),
    clear: () => { setLines([]); setRestaurantId(null); },
  }), [lines, restaurantId]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
