import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { AlertsView } from './AlertsView';
import { ToastProvider } from '../toasts';
import type { Alert } from '../lib/types';

afterEach(() => vi.unstubAllGlobals());

const alert = (over: Partial<Alert> = {}): Alert => ({
  id: 1, kind: 'refund_given_up', orderId: 'o1', message: 'refund failed 5 times', occurrences: 1,
  lastSeenAt: new Date().toISOString(), ...over,
});

function show(alerts: Alert[]) {
  return render(
    <ToastProvider>
      <AlertsView alerts={alerts} knownOrders={new Set(['o1'])} onOpenOrder={() => {}} onResolved={async () => {}} />
    </ToastProvider>,
  );
}

describe('AlertsView', () => {
  it('says when an alert was last sent to the channel', () => {
    show([alert({ notifiedAt: new Date(Date.now() - 5 * 60_000).toISOString() })]);
    expect(screen.getByText(/sent to the channel 5m ago/)).toBeTruthy();
  });

  it('says nothing about sending for an alert that never was', () => {
    show([alert()]);
    expect(screen.queryByText(/sent to the channel/)).toBeNull();
    expect(screen.getByText('A refund could not be sent')).toBeTruthy();
  });
});
