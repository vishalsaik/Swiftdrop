// The shapes the Go API answers with. See cmd/handlers and internal/domain.

export type Role = 'customer' | 'staff';

export interface User {
  id: string; email: string; name: string; role: Role; createdAt?: string;
  /** set while the password is a temporary one: the account can do nothing but change it */
  mustChangePassword?: boolean;
  disabledAt?: string;
}

/** One line of an account's history. */
export interface AccountEvent { id: number; userId: string; actorId: string; actorEmail: string; kind: string; detail?: string; at: string }
export interface Session { user: User; csrfToken: string }

export interface OrderItem { sku: string; name: string; quantity: number; unitPriceCents: number; menuItemId?: string }

export interface Restaurant { id: string; name: string; active: boolean; createdAt?: string }
export interface MenuItem {
  id: string; restaurantId: string; name: string; description?: string; priceCents: number;
  /** false for something the kitchen has run out of; only staff see it */
  available: boolean; createdAt?: string;
}

export interface PaymentView {
  status: string;
  attempt: number;
  maxAttempts: number;
  amountCents: number;
  reason?: string;
  refundStatus?: string;
}

export type PaymentType = 'card' | 'wallet' | 'cash';

export interface Order {
  id: string;
  customerId: string;
  restaurantId: string;
  paymentType: PaymentType;
  state: string;
  items: OrderItem[];
  createdAt: string;
  assignedDriverId?: string;
  foodReady?: string;
  payment?: PaymentView;
  paymentIssue?: string;
}

export interface Alert {
  id: number;
  kind: string;
  orderId?: string;
  message: string;
  occurrences: number;
  lastSeenAt: string;
  resolvedAt?: string;
  /** the last time the alert was sent out (a webhook); absent if it never was */
  notifiedAt?: string;
}

export interface LedgerEvent { kind: string; detail?: string; at: string }
