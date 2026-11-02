import { describe, expect, it } from 'vitest';
import { signature, stampFor } from './present';
import { order } from '../test/server';

describe('stampFor', () => {
  it('says what happened to the money in a few words', () => {
    expect(stampFor(order({ state: 'cancelled', paymentIssue: 'refund_pending', payment: { status: 'authorized', amountCents: 100, attempt: 1, maxAttempts: 3 } }))).toEqual({ text: 'Refund on its way', tone: 'money' });
    expect(stampFor(order({ state: 'cancelled', paymentIssue: 'refunded', payment: { status: 'authorized', amountCents: 100, attempt: 1, maxAttempts: 3 } }))?.text).toBe('Refunded');
    expect(stampFor(order({ state: 'payment_pending', paymentIssue: 'confirming', payment: { status: 'processing', amountCents: 100, attempt: 1, maxAttempts: 3 } }))?.text).toBe('Confirming');
  });

  it('marks a paid card order, but not a cash one, and a cancelled one is Cancelled', () => {
    const paid = { status: 'authorized', amountCents: 100, attempt: 1, maxAttempts: 3 };
    expect(stampFor(order({ state: 'paid', payment: paid }))).toEqual({ text: 'Paid', tone: 'paid' });
    expect(stampFor(order({ state: 'paid', paymentType: 'cash', payment: paid }))).toBeNull();
    expect(stampFor(order({ state: 'cancelled' }))).toEqual({ text: 'Cancelled', tone: 'cancel' });
    expect(stampFor(order({ state: 'created' }))).toBeNull();
  });
});

describe('signature', () => {
  it('changes when what an order shows changes, and not otherwise', () => {
    const a = order({ state: 'paid' });
    expect(signature(a)).toBe(signature({ ...a, createdAt: 'later' }));
    expect(signature(a)).not.toBe(signature({ ...a, state: 'preparing' }));
    expect(signature(a)).not.toBe(signature({ ...a, foodReady: 'now' }));
    expect(signature(a)).not.toBe(signature({ ...a, assignedDriverId: 'd1' }));
    expect(signature(a)).not.toBe(signature({ ...a, paymentIssue: 'declined' }));
  });
});
