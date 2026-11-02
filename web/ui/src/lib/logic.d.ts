// Types for logic.js, which stays plain JavaScript so `node --test` can run it with no tooling.
import type { Alert, LedgerEvent, Order, Role } from './types';

export interface Column { key: string; title: string; states: string[] }
export const COLUMNS: Column[];
export const STATE_LABELS: Record<string, string>;
export function columnOf(order: Order): string;
export function money(cents: number): string;
export function totalCents(order: Order): number;
export function itemsSummary(order: Order, max?: number): string;

export interface OrderAction {
  key: 'pay' | 'accept' | 'ready' | 'pickup' | 'deliver' | 'cancel';
  label: string;
  enabled: boolean;
  primary?: boolean;
  danger?: boolean;
  note?: string;
  why?: string;
}
export function actionsFor(order: Order, role?: Role | string): OrderAction[];
export interface Capabilities { alerts: boolean; history: boolean; placeForOthers: boolean; everyonesOrders: boolean; kitchen: boolean }
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

export interface OrderForm {
  id: string; customerId: string; restaurantId: string; paymentType: string;
  items: { name: string; quantity: string; price: string }[];
}
export interface OrderPayload {
  id: string; customerId: string; restaurantId: string; paymentType: string;
  items: { sku: string; name: string; quantity: number; unitPriceCents: number }[];
}
export type OrderFormErrors = Record<string, string | Record<string, string>>;
export function validateOrderForm(form: OrderForm): { ok: boolean; errors: OrderFormErrors; payload: OrderPayload | null };

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
