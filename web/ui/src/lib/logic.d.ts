// Types for logic.js, which stays plain JavaScript so `node --test` can run it with no tooling.
import type { AccountEvent, Alert, LedgerEvent, MenuItem, Order, Restaurant, Role, User } from './types';

export interface Column { key: string; title: string; states: string[] }
export const COLUMNS: Column[];
export const STATE_LABELS: Record<string, string>;
export function columnOf(order: Order): string;
export function money(cents: number): string;
export function totalCents(order: Order): number;
export function itemsSummary(order: Order, max?: number): string;

export interface OrderAction {
  key: 'pay' | 'accept' | 'ready' | 'pickup' | 'deliver' | 'cancel' | 'record_refund';
  label: string;
  enabled: boolean;
  primary?: boolean;
  danger?: boolean;
  note?: string;
  why?: string;
}
export function actionsFor(order: Order, role?: Role | string): OrderAction[];
export interface Capabilities { alerts: boolean; history: boolean; placeForOthers: boolean; everyonesOrders: boolean; kitchen: boolean; people: boolean; menu: boolean }
export function capabilities(role: Role | string | null | undefined): Capabilities;

export interface JourneyStep { key: string; label: string; status: 'done' | 'current' | 'todo' | 'skipped' | 'stopped'; note?: string }
export function timeline(order: Order): JourneyStep[];

export interface Notice { issue: string; tone: 'wait' | 'bad' | 'money' | 'good' | 'alert'; title: string; body: string }
export function noticeFor(order: Order): Notice | null;
export function describeEvent(event: LedgerEvent): { label: string; note: string };
export function describeAlert(alert: Alert): { title: string; advice: string; detail: string; seen: string };
export function openAlerts(alerts: Alert[] | null | undefined): Alert[];

export interface Stats { active: number; onTheRoad: number; takenCents: number; refundsPending: number; attention: number }
export function stats(orders: Order[]): Stats;
export function newOrderId(rand?: () => number): string;
export function priceToCents(text: string): number | null;

export const MAX_LINE_QUANTITY: number;
export const MAX_ORDER_LINES: number;
export function menuTotalCents(items: MenuItem[], quantities: Record<string, number | string>): number;

export interface OrderForm {
  id: string; customerId: string; restaurantId: string; paymentType: string;
  /** where it is going: a saved address's id, or an address to send with the order */
  deliveryAddressId?: string;
  deliveryAddress?: AddressPayload;
  /** menu lines: the server takes their names and prices from the menu */
  menu?: { menuItemId: string; quantity: number | string }[];
  /** custom lines, typed by staff */
  items: { name: string; quantity: string; price: string }[];
}
export interface AddressPayload {
  label?: string; line1: string; landmark?: string; city?: string; postalCode?: string; latitude: number; longitude: number;
}
export function addressLine(a: { line1?: string; city?: string; postalCode?: string } | null | undefined): string;
export interface AddressDraft {
  label: string; line1: string; landmark: string; city: string; postalCode: string; latitude: string; longitude: string;
}
export function candidateToDraft(c: { line1?: string; city?: string; postalCode?: string; latitude: number; longitude: number }): AddressDraft;
export function validateAddressForm(form: Partial<AddressDraft>): { ok: boolean; errors: Record<string, string>; payload: AddressPayload | null };

export type OrderPayloadLine =
  | { menuItemId: string; quantity: number }
  | { sku: string; name: string; quantity: number; unitPriceCents: number };
export interface OrderPayload {
  id: string; customerId: string; restaurantId: string; paymentType: string;
  deliveryAddressId?: string;
  deliveryAddress?: AddressPayload;
  items: OrderPayloadLine[];
}
export type OrderFormErrors = Record<string, string | Record<string, string>>;
export function validateOrderForm(form: OrderForm): { ok: boolean; errors: OrderFormErrors; payload: OrderPayload | null };

export function validateMenuItemForm(form: { name?: string; description?: string; price?: string; veg?: boolean }): {
  ok: boolean; errors: Record<string, string>; payload: { name: string; description: string; priceCents: number; veg?: boolean } | null;
};

export function roundCoord(x: number | string): number;
export function searchQueryString(q?: { q?: string; location?: { lat: number; lng: number } | null; veg?: boolean; maxPriceCents?: number }): string;
export function formatDistance(km: number | null | undefined): string;
export function locationProblem(code: number): string;
export function distanceKm(lat1: number, lng1: number, lat2: number, lng2: number): number;
export interface RestaurantCard {
  restaurant: Restaurant;
  status: 'open' | 'delivers' | 'too_far' | 'not_placed';
  distanceKm?: number;
}
export function restaurantCards(restaurants: Restaurant[] | null | undefined, near?: { lat: number; lng: number } | null): RestaurantCard[];
export function cardStatusText(card: RestaurantCard, placeName?: string): string;
export function filterMenu(items: MenuItem[] | null | undefined, filter?: { text?: string; vegOnly?: boolean }): MenuItem[];
export interface PlacePayload { cuisine: string; latitude?: number; longitude?: number; deliveryRadiusKm?: number }
export function validatePlaceForm(form: { cuisine?: string; latitude?: string; longitude?: string; radiusKm?: string }): {
  ok: boolean; errors: Record<string, string>; payload: PlacePayload | null;
};
export function validateRestaurantName(text: string | undefined): { ok: boolean; error: string; name: string };

export function errorMessage(status: number, body: unknown): string;
export function authErrorMessage(status: number, body: unknown, retryAfterSeconds?: number): string;
export function lockoutMessage(seconds: unknown): string;
export function fieldErrors(body: unknown): Record<string, string>;
export function validateSignup(form: { email?: string; name?: string; password?: string }): {
  ok: boolean; errors: Record<string, string>; payload: { email: string; name: string; password: string };
};
export function greeting(date?: Date): string;
export function initials(name: string | null | undefined): string;
export function timeAgo(iso: string | undefined, now?: number): string;

export function describeUserEvent(event: AccountEvent): { label: string; by: string };
export function accountBadges(user: User, youId?: string): { text: string; tone: 'neutral' | 'bad' | 'wait' | 'good' }[];
export function validateNewAccount(form: { email?: string; name?: string; role?: string }): {
  ok: boolean; errors: Record<string, string>; payload: { email: string; name: string; role: string };
};
