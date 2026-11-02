import { noticeFor } from './logic';
import type { Order } from './types';

export type Tone = 'wait' | 'bad' | 'good' | 'money' | 'alert' | 'cancel' | 'paid';

const STAMP_LABELS: Record<string, string> = {
  refund_pending: 'Refund on its way', refunded: 'Refunded', refund_failed: 'Refund failed',
  confirming: 'Confirming', declined: 'Declined', abandoned: 'Not charged',
};

// stampFor is the one short label a row shows about an order's money, or null.
export function stampFor(order: Order): { text: string; tone: Tone } | null {
  const notice = noticeFor(order);
  if (notice) {
    const text = STAMP_LABELS[notice.issue];
    if (text) return { text, tone: notice.tone };
  }
  if (order.state === 'cancelled') return { text: 'Cancelled', tone: 'cancel' };
  if (order.payment && order.payment.status === 'authorized' && order.paymentType !== 'cash') return { text: 'Paid', tone: 'paid' };
  return null;
}

// signature changes when what an order shows changes, so a screen can tell a real change
// from a poll that found nothing new.
export function signature(o: Order): string {
  return [
    o.state, o.paymentIssue || '', o.payment ? o.payment.status + (o.payment.refundStatus ?? '') : '',
    o.foodReady ? 1 : 0, o.assignedDriverId ? 1 : 0,
  ].join('|');
}

export type IconName = 'tray' | 'bell' | 'plus' | 'moon' | 'sun' | 'xmark' | 'chevron' | 'check' | 'clock' | 'card' | 'flame' | 'scooter'
  | 'archive' | 'person' | 'warning' | 'eye' | 'eyeSlash';

// Each stage of the board has a colour and a symbol, like the tiles in a SwiftUI sidebar.
export const STAGE_STYLE: Record<string, { tint: string; icon: IconName }> = {
  awaiting: { tint: 'orange', icon: 'clock' },
  paid: { tint: 'blue', icon: 'card' },
  kitchen: { tint: 'purple', icon: 'flame' },
  road: { tint: 'teal', icon: 'scooter' },
  closed: { tint: 'gray', icon: 'archive' },
};
