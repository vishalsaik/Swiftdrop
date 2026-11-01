// Pure dashboard logic: no DOM, no network, so it can be tested with `node --test`.
// app.js does the drawing and the fetching and asks this file what things mean.

// The board's columns, left to right. An order sits in the column of its state.
export const COLUMNS = [
  { key: 'awaiting', title: 'Awaiting payment', states: ['created', 'payment_pending'] },
  { key: 'paid', title: 'Paid', states: ['paid'] },
  { key: 'kitchen', title: 'In the kitchen', states: ['preparing'] },
  { key: 'road', title: 'On the road', states: ['picked_up'] },
  { key: 'closed', title: 'Closed', states: ['delivered', 'cancelled'] },
];

export const STATE_LABELS = {
  created: 'Placed',
  payment_pending: 'Confirming payment',
  paid: 'Paid',
  preparing: 'In the kitchen',
  picked_up: 'On the road',
  delivered: 'Delivered',
  cancelled: 'Cancelled',
};

export function columnOf(order) {
  const col = COLUMNS.find((c) => c.states.includes(order.state));
  return col ? col.key : 'closed';
}

// money turns integer cents into "$12.34". Cents stay integers everywhere else.
export function money(cents) {
  const n = Number.isFinite(cents) ? Math.round(cents) : 0;
  const abs = Math.abs(n);
  const text = `$${Math.floor(abs / 100).toLocaleString('en-US')}.${String(abs % 100).padStart(2, '0')}`;
  return n < 0 ? `-${text}` : text;
}

export function totalCents(order) {
  return (order.items || []).reduce((sum, it) => sum + it.unitPriceCents * it.quantity, 0);
}

// itemsSummary is "2 x Burger, 1 x Fries + 1 more" for a card with little room.
export function itemsSummary(order, max = 2) {
  const items = order.items || [];
  const shown = items.slice(0, max).map((it) => `${it.quantity} x ${it.name}`);
  const rest = items.length - shown.length;
  return rest > 0 ? `${shown.join(', ')} + ${rest} more` : shown.join(', ');
}

const CANCELLABLE = ['created', 'payment_pending', 'paid', 'preparing'];

// actionsFor lists what can be done to an order now, mirroring the server's
// state machine. The server is still the judge: it answers 409 for anything
// else. Each action says why it is unavailable instead of silently hiding.
export function actionsFor(order) {
  const actions = [];
  const s = order.state;
  const charged = order.payment && order.payment.status === 'authorized' && order.paymentType !== 'cash';

  if (s === 'created') {
    // an earlier attempt that ended without a charge: declined, or abandoned
    // because the provider never saw it
    const failed = order.payment && ['declined', 'abandoned'].includes(order.payment.status);
    actions.push(failed
      ? {
        key: 'pay', label: 'Try payment again', primary: true, enabled: true,
        note: `Attempt ${order.payment.attempt + 1} of ${order.payment.maxAttempts}. The last ${order.payment.status === 'declined' ? 'decline' : 'failed attempt'} cancels the order.`,
      }
      : { key: 'pay', label: 'Take payment', primary: true, enabled: true });
  }
  if (s === 'payment_pending') {
    // a person is looking at a payment that got no answer for too long: the
    // server refuses a retry until it is settled
    const review = order.payment && order.payment.status === 'needs_review';
    actions.push(review
      ? {
        key: 'pay', label: 'Retry payment', enabled: false,
        why: 'A person is checking this payment. It cannot be tried again until that is done.',
      }
      : {
        key: 'pay', label: 'Retry payment', enabled: true,
        note: 'Only if the customer chooses to try again. The same payment is reused, never charged twice.',
      });
  }
  if (s === 'paid') {
    actions.push({ key: 'accept', label: 'Accept order', primary: true, enabled: true });
  }
  if (s === 'preparing') {
    const ready = Boolean(order.foodReady);
    actions.push({
      key: 'ready', label: ready ? 'Food is ready' : 'Mark food ready', primary: !ready,
      enabled: !ready, why: ready ? 'The kitchen already marked it ready.' : '',
    });
    const driver = Boolean(order.assignedDriverId);
    const canPick = ready && driver;
    let why = '';
    if (!ready) why = 'The food is not ready yet.';
    else if (!driver) why = 'No driver is assigned yet.';
    actions.push({ key: 'pickup', label: 'Pick up', primary: ready, enabled: canPick, why });
  }
  if (s === 'picked_up') {
    actions.push({ key: 'deliver', label: 'Mark delivered', primary: true, enabled: true });
  }
  if (CANCELLABLE.includes(s)) {
    actions.push({
      key: 'cancel', label: charged ? 'Cancel and refund' : 'Cancel order', danger: true, enabled: true,
      note: charged ? 'The customer was charged. The refund starts on its own.' : '',
    });
  }
  return actions;
}

// timeline is the order's journey as rows a departure board can show. A
// cancelled order cannot say where it stopped, so the payment is the evidence
// that it got as far as being paid.
export function timeline(order) {
  const labels = [
    ['created', 'Placed'], ['paid', 'Paid'], ['preparing', 'Kitchen'], ['picked_up', 'On the road'], ['delivered', 'Delivered'],
  ];
  if (order.state === 'cancelled') {
    const wasPaid = Boolean(order.payment && order.payment.status === 'authorized');
    return [
      { key: 'created', label: 'Placed', status: 'done' },
      { key: 'paid', label: 'Paid', status: wasPaid ? 'done' : 'skipped' },
      { key: 'preparing', label: 'Kitchen', status: 'skipped' },
      { key: 'picked_up', label: 'On the road', status: 'skipped' },
      { key: 'cancelled', label: 'Cancelled', status: 'stopped' },
    ];
  }
  const position = { created: 0, payment_pending: 0, paid: 1, preparing: 2, picked_up: 3, delivered: 4 }[order.state] ?? 0;
  return labels.map(([key, label], i) => {
    let status = 'todo';
    if (i < position) status = 'done';
    else if (i === position) status = order.state === 'delivered' ? 'done' : 'current';
    const step = { key, label, status };
    if (i === 0 && order.state === 'payment_pending') step.note = 'confirming payment';
    return step;
  });
}

function payWord(order) {
  return order.paymentType === 'wallet' ? 'wallet' : 'card';
}

// noticeFor is the message the customer sees about their money, or null when
// there is nothing to say. It is chosen from the server's paymentIssue (and a
// declined payment), so the screen never works out refunds for itself.
export function noticeFor(order) {
  const amount = money(order.payment ? order.payment.amountCents : totalCents(order));
  switch (order.paymentIssue) {
    case 'confirming':
      if (order.payment && order.payment.status === 'needs_review') {
        return {
          issue: 'confirming', tone: 'wait',
          title: "We're still confirming your payment",
          body: "This is taking longer than usual and our team is checking it. If you were charged and the order doesn't go ahead, the amount is returned to you.",
        };
      }
      return {
        issue: 'confirming', tone: 'wait',
        title: "We're confirming your payment",
        body: "This usually takes a few seconds. If you were charged and the order doesn't go ahead, the amount is returned to you automatically.",
      };
    case 'abandoned': {
      if (order.state === 'created') {
        const left = order.payment ? order.payment.maxAttempts - order.payment.attempt : 0;
        return {
          issue: 'abandoned', tone: 'bad',
          title: "Your payment didn't go through",
          body: `We never received an answer for that attempt and you were not charged. You can try again (${left} ${left === 1 ? 'attempt' : 'attempts'} left).`,
        };
      }
      return {
        issue: 'abandoned', tone: 'bad',
        title: "Your payment didn't go through",
        body: 'We never received an answer for the last attempt. You were not charged and the order was cancelled.',
      };
    }
    case 'refund_pending':
      return {
        issue: 'refund_pending', tone: 'money',
        title: 'Your order was cancelled. Your money is on its way back',
        body: `We took ${amount} for order ${order.id}. It is being returned to your ${payWord(order)} and will be credited soon. You don't need to do anything.`,
      };
    case 'refunded':
      return {
        issue: 'refunded', tone: 'good',
        title: `Your refund of ${amount} has been sent`,
        body: `It was returned to your ${payWord(order)}. Your bank can take a few days to show it.`,
      };
    case 'refund_failed':
      return {
        issue: 'refund_failed', tone: 'alert',
        title: "We couldn't return your money automatically",
        body: `Please contact support and quote order ${order.id}. We will return ${amount} to you. You do not need to pay again.`,
      };
    case 'declined': {
      const reason = (order.payment && order.payment.reason) || 'The payment was declined.';
      if (order.state === 'created') {
        const left = order.payment ? order.payment.maxAttempts - order.payment.attempt : 0;
        return {
          issue: 'declined', tone: 'bad',
          title: 'Your payment was declined',
          body: `${reason} You were not charged. You can try another card or payment method (${left} ${left === 1 ? 'attempt' : 'attempts'} left).`,
        };
      }
      return {
        issue: 'declined', tone: 'bad',
        title: 'Your payment was declined',
        body: `${reason} You were not charged and the order was cancelled.`,
      };
    }
    default:
      return null;
  }
}

const EVENT_LABELS = {
  claimed: 'Payment started',
  taken_over: 'A retry took the payment over',
  authorized: 'Money taken',
  declined: 'Payment declined',
  abandoned: 'Attempt abandoned, nothing was charged',
  needs_review: 'No answer for too long, handed to a person',
  refund_started: 'Refund started',
  refund_attempt_failed: 'Refund attempt failed',
  refunded: 'Refund sent',
  refund_failed: 'Refund given up, needs a person',
};

// describeEvent turns a ledger line into words for the history list. The ledger's
// detail is "key=value" or free text; the value is what a person wants to read.
export function describeEvent(event) {
  const kind = String(event.kind || '');
  const detail = String(event.detail || '');
  const label = EVENT_LABELS[kind] || kind.replace(/_/g, ' ') || 'Something happened';
  let note = detail;
  const eq = detail.indexOf('=');
  if (eq > 0 && /^[a-z_]+$/.test(detail.slice(0, eq))) {
    const key = detail.slice(0, eq);
    const value = detail.slice(eq + 1);
    note = key === 'amount_cents' && /^\d+$/.test(value) ? money(Number(value)) : value;
  }
  return { label, note };
}

const ALERT_TEXT = {
  unrecorded_charge: {
    title: 'Money taken, but not written down',
    advice: 'The provider charged the customer and we could not record it. Check the payment in the provider dashboard, then fix the order by hand.',
  },
  contradicting_event: {
    title: 'The provider disagrees with our record',
    advice: 'The provider reported something that contradicts what we recorded. Look the payment up at the provider and decide which is right.',
  },
  refund_given_up: {
    title: 'A refund could not be sent',
    advice: 'The refund job tried every time it was allowed to and failed. Refund the customer from the provider dashboard.',
  },
  payment_needs_review: {
    title: 'A payment has had no answer for too long',
    advice: 'We could not find out whether the customer was charged, and the payment was not sent again. Look it up in the provider dashboard: if money was taken, record it and the order proceeds; if not, the customer can try again.',
  },
  refund_unrecorded: {
    title: 'A refund was sent, but not written down',
    advice: 'The provider accepted the refund and we could not record it. Do not refund again: check the provider dashboard and mark it done.',
  },
};

// describeAlert turns an alert from GET /alerts into words for the alerts list.
export function describeAlert(alert) {
  const kind = String(alert.kind || '');
  const known = ALERT_TEXT[kind];
  const n = Number(alert.occurrences) || 1;
  return {
    title: known ? known.title : (kind.replace(/_/g, ' ') || 'Needs a look'),
    advice: known ? known.advice : 'Something needs a person to look at it.',
    detail: String(alert.message || ''),
    seen: n > 1 ? `seen ${n} times` : '',
  };
}

// openAlerts keeps the alerts nobody has resolved, newest activity first.
export function openAlerts(alerts) {
  return (alerts || []).filter((a) => !a.resolvedAt)
    .sort((a, b) => String(b.lastSeenAt).localeCompare(String(a.lastSeenAt)));
}

// stats are the headline numbers over the orders on the board.
export function stats(orders) {
  const out = { active: 0, onTheRoad: 0, takenCents: 0, refundsPending: 0, attention: 0 };
  for (const o of orders) {
    if (!['delivered', 'cancelled'].includes(o.state)) out.active++;
    if (o.state === 'picked_up') out.onTheRoad++;
    if (o.payment && o.payment.status === 'authorized' && o.state !== 'cancelled' && o.paymentType !== 'cash') {
      out.takenCents += o.payment.amountCents;
    }
    if (o.paymentIssue === 'refund_pending') out.refundsPending++;
    if (o.paymentIssue === 'refund_failed' || o.paymentIssue === 'confirming') out.attention++;
  }
  return out;
}

// newOrderId makes a short readable id. `rand` is a parameter so tests are exact.
export function newOrderId(rand = Math.random) {
  const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789'; // no 0/O/1/I to misread
  let id = '';
  for (let i = 0; i < 5; i++) id += alphabet[Math.floor(rand() * alphabet.length)];
  return `SD-${id}`;
}

// priceToCents reads "5.99" as 599 using the digits, never floating point
// (1.005 * 100 is 100.49999...). A third decimal rounds half up. Anything that is
// not a plain non-negative decimal gives null.
export function priceToCents(text) {
  const m = String(text).trim().match(/^(\d*)(?:\.(\d*))?$/);
  if (!m || (m[1] === '' && !m[2])) return null;
  const frac = (m[2] || '').padEnd(3, '0');
  const cents = Number(m[1] || '0') * 100 + Number(frac.slice(0, 2)) + (Number(frac[2]) >= 5 ? 1 : 0);
  return Number.isSafeInteger(cents) ? cents : null;
}

// validateOrderForm mirrors the server's rules so mistakes are shown next to the
// field, and builds the request body (prices in cents). Blank item rows are
// ignored. The server validates again, so this is only for a kinder form.
export function validateOrderForm(form) {
  const errors = {};
  const need = (key, label) => {
    if (!String(form[key] ?? '').trim()) errors[key] = `${label} is required`;
  };
  need('id', 'Order id');
  need('customerId', 'Customer');
  need('restaurantId', 'Restaurant');
  if (!['card', 'cash', 'wallet'].includes(form.paymentType)) errors.paymentType = 'Choose how the customer pays';

  const items = [];
  (form.items || []).forEach((row, index) => {
    const blank = !String(row.name ?? '').trim() && !String(row.price ?? '').trim();
    if (blank) return;
    const itemErrors = {};
    if (!String(row.name ?? '').trim()) itemErrors.name = 'Name the item';
    const qty = Number(row.quantity);
    if (!Number.isInteger(qty) || qty < 1) itemErrors.quantity = 'Quantity must be 1 or more';
    const cents = priceToCents(row.price ?? '');
    if (cents === null) itemErrors.price = 'Enter a price of 0 or more';
    if (Object.keys(itemErrors).length) {
      errors[`items.${index}`] = itemErrors;
      return;
    }
    const name = String(row.name).trim();
    items.push({
      sku: (name.replace(/[^a-z0-9]/gi, '').toUpperCase().slice(0, 8)) || 'ITEM',
      name, quantity: qty, unitPriceCents: cents,
    });
  });
  if (items.length === 0 && !Object.keys(errors).some((k) => k.startsWith('items.'))) {
    errors.items = 'Add at least one item';
  }
  if (Object.keys(errors).length) return { ok: false, errors, payload: null };
  return {
    ok: true, errors: {},
    payload: {
      id: form.id.trim(), customerId: form.customerId.trim(), restaurantId: form.restaurantId.trim(),
      paymentType: form.paymentType, items,
    },
  };
}

// errorMessage is what the screen says when a call fails. The server's own words
// win; otherwise something honest for the status.
export function errorMessage(status, body) {
  if (body && typeof body.error === 'string' && body.error) return body.error;
  if (status === 0) return "Can't reach the server. Is it running?";
  if (status === 404) return 'That order no longer exists.';
  if (status >= 500) return 'The server had a problem. Try again in a moment.';
  return `The request failed (HTTP ${status}).`;
}

// timeAgo is a short relative time for a card.
export function timeAgo(iso, now = Date.now()) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t) || t <= 0 || new Date(t).getUTCFullYear() < 2000) return '';
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 10) return 'just now';
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}
