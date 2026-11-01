// SwiftDrop dispatch desk. Draws the board from GET /orders and sends the
// lifecycle calls. What things mean (actions, notices, totals) lives in
// logic.js, which is tested without a browser. Everything on screen is built with
// text nodes, never parsed as markup, so order data can never become markup.
import {
  COLUMNS, STATE_LABELS, columnOf, money, totalCents, itemsSummary, actionsFor, timeline,
  noticeFor, stats, newOrderId, validateOrderForm, priceToCents, errorMessage, timeAgo, describeEvent,
  describeAlert, openAlerts,
} from './logic.js';

const POLL_MS = 3000;
const $ = (selector) => document.querySelector(selector);

const state = {
  orders: [],
  byId: new Map(),
  selected: null,     // id of the order in the drawer
  loaded: false,      // false until the first answer, so history does not pop up
  online: null,
  issues: new Map(),  // order id -> the payment issue we last saw
  sigs: new Map(),    // order id -> signature of what its ticket shows
  confirming: null,   // the action waiting for a second click
  payOpen: false,
  method: 'pm_card_visa',
  busy: false,
  drawerSig: '',
  history: new Map(),     // order id -> its payment history (the ledger)
  historyKey: new Map(),  // order id -> what the history was last read for
  lastFocus: null,
  alerts: [],             // the open alerts, from GET /alerts
  alertSig: '',           // what the open alerts list last drew, so a poll does not redraw it
};

// ---------- tiny helpers ----------

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v === null || v === undefined) continue;
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = v;
    else if (k === 'dataset') Object.assign(el.dataset, v);
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid === null || kid === undefined || kid === false) continue;
    el.append(typeof kid === 'object' ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

function store(key, value) {
  try {
    if (value === undefined) return localStorage.getItem(key);
    localStorage.setItem(key, value);
  } catch { /* private mode or blocked storage: the page works without it */ }
  return null;
}

async function api(method, path, body) {
  try {
    const res = await fetch(path, {
      method, headers: body ? { 'Content-Type': 'application/json' } : {}, body: body ? JSON.stringify(body) : undefined,
    });
    let data = null;
    try { data = await res.json(); } catch { /* an empty or non-JSON body */ }
    return { status: res.status, ok: res.ok, data };
  } catch {
    return { status: 0, ok: false, data: null };
  }
}

function toast(message, kind = '') {
  const el = h('div', { class: `toast ${kind}`, role: kind === 'bad' ? 'alert' : 'status', text: message });
  $('#toasts').append(el);
  setTimeout(() => el.remove(), kind === 'bad' ? 7000 : 4000);
}

// ---------- the board ----------

const columnEls = new Map(); // column key -> { stack, count, empty }
const ticketEls = new Map(); // order id -> ticket element

function buildBoard() {
  const board = $('#board');
  for (const col of COLUMNS) {
    const stack = h('div', { class: 'stack' });
    const count = h('span', { class: 'count', text: '0' });
    const empty = h('p', { class: 'empty', text: 'Nothing here' });
    board.append(h('section', { class: 'col', dataset: { col: col.key }, 'aria-label': col.title },
      h('header', {}, h('h2', { text: col.title }), count), stack, empty));
    columnEls.set(col.key, { stack, count, empty });
  }
}

function stampFor(order) {
  const n = noticeFor(order);
  if (n) {
    const label = {
      refund_pending: 'Refund on its way', refunded: 'Refunded', refund_failed: 'Refund failed',
      confirming: 'Confirming', declined: 'Declined', abandoned: 'Not charged',
    }[n.issue];
    if (label) return { text: label, cls: n.tone };
  }
  if (order.state === 'cancelled') return { text: 'Cancelled', cls: 'cancel' };
  if (order.payment && order.payment.status === 'authorized' && order.paymentType !== 'cash') return { text: 'Paid', cls: 'paid' };
  return null;
}

function signature(o) {
  return [o.state, o.paymentIssue || '', o.payment ? o.payment.status + o.payment.refundStatus : '', o.foodReady ? 1 : 0, o.assignedDriverId ? 1 : 0].join('|');
}

function buildTicket(order) {
  const stamp = stampFor(order);
  const el = h('button', { class: 'ticket', type: 'button', dataset: { id: order.id, sig: signature(order) }, 'aria-label': `Order ${order.id}, ${STATE_LABELS[order.state] || order.state}` },
    h('span', { class: 'top' }, h('span', { class: 'no', text: order.id }), h('span', { class: 'age', text: timeAgo(order.createdAt) })),
    h('p', { class: 'items', text: itemsSummary(order) || 'No items' }),
    stamp ? h('span', { class: `stamp ${stamp.cls}`, text: stamp.text }) : null,
    h('span', { class: 'foot' },
      h('span', { class: 'sum', text: money(totalCents(order)) }),
      h('span', { class: 'chip', text: order.paymentType }),
      order.state === 'payment_pending' ? h('span', { class: 'chip', text: 'confirming' }) : null));
  el.addEventListener('click', () => openDrawer(order.id, el));
  return el;
}

function renderBoard() {
  const seen = new Set();
  const perColumn = new Map(COLUMNS.map((c) => [c.key, []]));

  state.orders.forEach((order) => {
    seen.add(order.id);
    const sig = signature(order);
    let el = ticketEls.get(order.id);
    const isNew = !el;
    if (!el || el.dataset.sig !== sig) {
      const changed = Boolean(el) && state.loaded;
      el = buildTicket(order);
      el.classList.add(isNew ? 'enter' : 'changed');
      if (!isNew && !changed) el.classList.remove('changed');
      el.style.setProperty('--i', String(Math.min(perColumn.get(columnOf(order)).length, 8)));
      el.addEventListener('animationend', () => el.classList.remove('enter', 'changed'), { once: true });
      ticketEls.set(order.id, el);
    }
    el.querySelector('.age').textContent = timeAgo(order.createdAt);
    el.setAttribute('aria-current', state.selected === order.id ? 'true' : 'false');
    perColumn.get(columnOf(order)).push(el);
  });
  for (const [id, el] of ticketEls) {
    if (!seen.has(id)) { el.remove(); ticketEls.delete(id); }
  }

  for (const col of COLUMNS) {
    const { stack, count, empty } = columnEls.get(col.key);
    const els = perColumn.get(col.key);
    // Put each ticket where it belongs without re-inserting the ones already
    // there, so a poll never replays an animation.
    els.forEach((el, i) => { if (stack.children[i] !== el) stack.insertBefore(el, stack.children[i] || null); });
    while (stack.children.length > els.length) stack.lastElementChild.remove();
    count.textContent = String(els.length);
    empty.hidden = els.length > 0;
  }
  renderFirstRun();
}

function renderFirstRun() {
  const { empty } = columnEls.get('awaiting');
  if (state.orders.length > 0) { empty.textContent = 'Nothing here'; return; }
  empty.replaceChildren(
    h('span', { text: state.loaded ? 'No orders yet.' : 'Loading orders' }),
    state.loaded ? h('br') : null,
    state.loaded ? h('button', { class: 'btn primary', type: 'button', style: 'margin-top:12px', text: 'Create 3 sample orders', onclick: createSamples }) : null,
  );
}

function renderStats() {
  const s = stats(state.orders);
  const items = [
    ['On the board', String(s.active), 'hot'],
    ['On the road', String(s.onTheRoad), ''],
    ['Taken, in play', money(s.takenCents), ''],
    ['Refunds on the way', String(s.refundsPending), s.refundsPending ? 'warn' : ''],
  ];
  if (s.attention) items.push(['Needs a look', String(s.attention), 'warn']);
  $('#stats').replaceChildren(...items.map(([label, value, cls]) => h('div', { class: `stat ${cls}` }, h('dt', { text: label }), h('dd', { text: value }))));
}

// ---------- alerts ----------

function renderAlerts() {
  const open = state.alerts;
  const badge = $('#alertCount');
  badge.hidden = open.length === 0;
  badge.textContent = String(open.length);
  $('#alertsBtn').setAttribute('aria-label', open.length ? `Alerts, ${open.length} open` : 'Alerts, none open');
  if (!$('#alertsSheet').open) return;
  // The list is drawn again only when it changed, so a poll never takes the
  // focus out of a note someone is typing.
  const sig = open.map((a) => `${a.id}:${a.occurrences}:${a.lastSeenAt}:${state.byId.has(a.orderId)}`).join('|');
  if (sig === state.alertSig) return;
  state.alertSig = sig;
  if (open.length === 0) {
    $('#alertList').replaceChildren(h('li', { class: 'alert-empty', text: 'Nothing needs a look.' }));
    return;
  }
  // a note already typed survives a real change to the list
  const notes = new Map([...$('#alertList').querySelectorAll('input')].map((i) => [i.dataset.id, i.value]));
  $('#alertList').replaceChildren(...open.map((alert) => alertItem(alert, notes.get(String(alert.id)) || '')));
}

function alertItem(alert, note) {
  const d = describeAlert(alert);
  const input = h('input', { 'aria-label': 'Note for resolving', placeholder: 'What was done (optional)', dataset: { id: String(alert.id) }, value: note });
  return h('li', {},
    h('h3', { text: d.title }),
    h('p', { text: d.advice }),
    h('div', { class: 'detail', text: d.detail }),
    h('div', { class: 'meta', text: [alert.orderId ? `order ${alert.orderId}` : '', d.seen, timeAgo(alert.lastSeenAt)].filter(Boolean).join(' / ') }),
    h('div', { class: 'row' },
      input,
      alert.orderId && state.byId.has(alert.orderId)
        ? h('button', { class: 'btn ghost', type: 'button', text: 'Open order', onclick: () => { $('#alertsSheet').close(); openDrawer(alert.orderId); } }) : null,
      h('button', { class: 'btn primary', type: 'button', text: 'Resolve', onclick: () => resolveAlert(alert, input.value.trim()) })));
}

async function resolveAlert(alert, note) {
  const res = await api('POST', `/alerts/${alert.id}/resolve`, note ? { note } : undefined);
  // 409 means somebody else already did it, which is what the person wanted
  if (res.ok || res.status === 409) toast('Alert resolved', 'ok');
  else toast(errorMessage(res.status, res.data), 'bad');
  await refreshAlerts();
}

async function refreshAlerts() {
  const res = await api('GET', '/alerts');
  if (!res.ok) return; // the board shows the offline banner; the alerts keep their last answer
  state.alerts = openAlerts(res.data.alerts);
  renderAlerts();
}

// ---------- customer messages ----------

const noticeEls = new Map(); // order id -> the message on screen

function showNotice(order, notice) {
  noticeEls.get(order.id)?.remove();
  const el = h('article', { class: 'notice', dataset: { tone: notice.tone }, role: 'status' },
    h('div', { class: 'from' }, h('i'), `Message to customer / ${order.id}`),
    h('h3', { text: notice.title }),
    h('p', { text: notice.body }),
    h('div', { class: 'row' },
      h('button', { class: 'btn ghost', type: 'button', text: 'Dismiss', onclick: () => { el.remove(); noticeEls.delete(order.id); } }),
      h('button', { class: 'btn primary', type: 'button', text: 'Open order', onclick: () => { openDrawer(order.id); el.remove(); noticeEls.delete(order.id); } })));
  noticeEls.set(order.id, el);
  $('#notices').append(el);
  while ($('#notices').children.length > 3) $('#notices').firstElementChild.remove();
}

// A message appears when an order's payment issue CHANGES while we are watching.
// What was already true when the page opened is not announced again.
function announce(orders) {
  for (const order of orders) {
    const notice = noticeFor(order);
    const issue = notice ? notice.issue : '';
    if (state.loaded && issue && state.issues.get(order.id) !== issue) showNotice(order, notice);
    state.issues.set(order.id, issue);
  }
}

// ---------- the drawer ----------

function openDrawer(id, fromEl) {
  state.selected = id;
  state.confirming = null;
  state.payOpen = false;
  state.drawerSig = '';
  state.lastFocus = fromEl || document.activeElement;
  $('#scrim').hidden = false;
  renderDrawer();
  const drawer = $('#drawer');
  drawer.classList.add('open');
  drawer.setAttribute('aria-hidden', 'false');
  drawer.focus({ preventScroll: true });
  renderBoard();
}

function closeDrawer() {
  if (!state.selected) return;
  state.selected = null;
  $('#scrim').hidden = true;
  const drawer = $('#drawer');
  drawer.classList.remove('open');
  drawer.setAttribute('aria-hidden', 'true');
  renderBoard();
  if (state.lastFocus && document.contains(state.lastFocus)) state.lastFocus.focus({ preventScroll: true });
}

function drawerSignature(o) {
  return `${signature(o)}|${o.payment ? o.payment.refundStatus : ''}|${state.payOpen}|${state.confirming}|${state.busy}|${(state.history.get(o.id) || []).length}`;
}

// The history is read again whenever what the order shows changes, not on every
// poll, so an open drawer does not ask the server the same question every 3 seconds.
function ensureHistory(order) {
  const key = `${signature(order)}|${order.payment ? order.payment.refundStatus : ''}`;
  if (state.historyKey.get(order.id) === key) return;
  state.historyKey.set(order.id, key);
  api('GET', `/orders/${encodeURIComponent(order.id)}/payment-history`).then((res) => {
    if (!res.ok) { state.historyKey.delete(order.id); return; }
    state.history.set(order.id, res.data.events || []);
    state.drawerSig = '';
    if (state.selected === order.id) renderDrawer();
  });
}

function renderDrawer() {
  const order = state.byId.get(state.selected);
  const drawer = $('#drawer');
  if (!order) { drawer.replaceChildren(); return; }
  ensureHistory(order);
  const sig = drawerSignature(order);
  if (sig === state.drawerSig) return;
  state.drawerSig = sig;

  const notice = noticeFor(order);
  const pay = order.payment;
  const rows = timeline(order).map((step) => h('li', { class: step.status }, h('span', { text: step.label }), step.note ? h('span', { class: 'note', text: step.note }) : null));

  drawer.replaceChildren(
    h('div', { class: 'd-head' },
      h('div', {},
        h('h2', { text: order.id }),
        h('span', { class: 'pill', dataset: { state: order.state }, text: STATE_LABELS[order.state] || order.state }),
        h('div', { class: 'meta', text: `${order.paymentType} / ${order.customerId} / ${order.restaurantId} / ${timeAgo(order.createdAt) || 'new'}` })),
      h('button', { class: 'btn ghost icon', type: 'button', 'aria-label': 'Close', text: '×', onclick: closeDrawer })),
    h('div', { class: 'd-body' },
      notice ? h('section', { class: 'msg', dataset: { tone: notice.tone } },
        h('small', { text: 'What the customer sees' }), h('strong', { text: notice.title }), h('p', { text: notice.body })) : null,
      h('section', {}, h('h3', { text: 'Journey' }), h('ol', { class: 'journey' }, rows)),
      h('section', {}, h('h3', { text: 'Items' }), itemsTable(order)),
      h('section', {}, h('h3', { text: 'Money' }), moneyList(order, pay)),
      order.payment ? h('section', {}, h('h3', { text: 'Payment history' }), historyList(order)) : null,
      h('section', {}, h('h3', { text: 'Actions' }), actionsBlock(order))));
}

function itemsTable(order) {
  return h('table', { class: 'lines' },
    h('tbody', {},
      (order.items || []).map((it) => h('tr', {}, h('td', { text: `${it.quantity} x ${it.name}` }), h('td', { text: money(it.unitPriceCents * it.quantity) }))),
      h('tr', { class: 'total' }, h('td', { text: 'Total' }), h('td', { text: money(totalCents(order)) }))));
}

function moneyList(order, pay) {
  const refund = pay && pay.refundStatus && pay.refundStatus !== 'none'
    ? { refunding: 'in progress', refunded: 'sent back', failed: 'could not be sent' }[pay.refundStatus] || pay.refundStatus : null;
  const entries = [
    ['Pays by', order.paymentType],
    ['Payment', pay ? pay.status : 'not started'],
    pay ? ['Amount', money(pay.amountCents)] : null,
    pay && (pay.attempt > 1 || ['declined', 'abandoned'].includes(pay.status)) ? ['Attempt', `${pay.attempt} of ${pay.maxAttempts}`] : null,
    pay && pay.reason ? ['Decline reason', pay.reason] : null,
    refund ? ['Refund', refund] : null,
    order.assignedDriverId ? ['Driver', order.assignedDriverId.slice(0, 8)] : null,
    order.foodReady ? ['Food ready', timeAgo(order.foodReady)] : null,
  ].filter(Boolean);
  return h('dl', { class: 'kv' }, entries.map(([k, v]) => [h('dt', { text: k }), h('dd', { text: v })]));
}

function historyList(order) {
  const events = state.history.get(order.id);
  if (!events) return h('p', { class: 'why', text: 'Loading' });
  if (events.length === 0) return h('p', { class: 'why', text: 'Nothing recorded yet.' });
  return h('ol', { class: 'history' }, events.map((ev) => {
    const { label, note } = describeEvent(ev);
    const when = new Date(ev.at);
    return h('li', {},
      h('time', { datetime: ev.at, text: Number.isNaN(when.getTime()) ? '' : when.toLocaleTimeString([], { hour12: false }) }),
      h('span', { class: 'what', text: label }),
      note ? h('span', { class: 'note', text: note }) : null);
  }));
}

function actionsBlock(order) {
  const actions = actionsFor(order);
  if (actions.length === 0) return h('p', { class: 'why', text: 'This order is finished. There is nothing left to do.' });
  const buttons = actions.map((a) => {
    const confirming = state.confirming === a.key;
    return h('button', {
      class: `btn ${a.primary ? 'primary' : ''} ${a.danger ? 'danger' : ''}`, type: 'button', disabled: !a.enabled || state.busy,
      text: confirming ? `Confirm: ${a.label.toLowerCase()}` : a.label,
      onclick: () => onAction(order, a),
    });
  });
  const hints = actions.filter((a) => (a.why && !a.enabled) || (a.note && (state.confirming === a.key || a.key === 'pay'))).map((a) => h('p', { class: 'why', text: a.why || a.note }));
  return h('div', { class: 'actions' }, state.payOpen ? payForm(order) : null, h('div', { class: 'row' }, buttons), hints);
}

function payForm(order) {
  if (order.paymentType === 'cash') {
    return h('div', { class: 'payform' }, h('small', { text: 'Cash on delivery: nothing is charged now. The order is marked paid.' }));
  }
  return h('div', { class: 'payform' },
    h('label', {}, 'Payment method id',
      h('input', { id: 'method', value: state.method, spellcheck: 'false', autocomplete: 'off', oninput: (e) => { state.method = e.target.value; } })),
    h('small', { text: 'Stripe needs a test token such as pm_card_visa (a decline is pm_card_visa_chargeDeclined). The built-in fake gateway ignores it.' }));
}

const ENDPOINTS = { accept: 'accept', ready: 'ready', pickup: 'pickup', deliver: 'deliver', cancel: 'cancel' };
const DONE = {
  pay: 'Payment taken', accept: 'Order accepted', ready: 'Marked ready', pickup: 'Picked up', deliver: 'Delivered', cancel: 'Order cancelled',
};

async function onAction(order, action) {
  if (action.key === 'pay' && !state.payOpen && order.paymentType !== 'cash') {
    state.payOpen = true; // first click opens the form, the second takes the payment
    renderDrawer();
    $('#method')?.focus();
    return;
  }
  if (action.danger && state.confirming !== action.key) {
    state.confirming = action.key; // two clicks to cancel, with no browser dialog
    renderDrawer();
    setTimeout(() => { if (state.confirming === action.key) { state.confirming = null; renderDrawer(); } }, 4000);
    return;
  }
  state.busy = true; state.confirming = null;
  renderDrawer();

  let result;
  if (action.key === 'pay') {
    const method = order.paymentType === 'cash' ? '' : state.method.trim();
    result = await api('POST', `/orders/${encodeURIComponent(order.id)}/pay`, method ? { paymentMethodId: method } : undefined);
  } else {
    result = await api('POST', `/orders/${encodeURIComponent(order.id)}/${ENDPOINTS[action.key]}`);
  }
  state.busy = false; state.payOpen = false;
  if (result.ok) {
    toast(`${order.id}: ${DONE[action.key]}`, 'ok');
  } else {
    // a decline says whether the customer can try another method
    const d = result.data;
    const more = result.status === 402 && d && d.canRetry
      ? ` Try another method (${d.attemptsLeft} ${d.attemptsLeft === 1 ? 'attempt' : 'attempts'} left).` : '';
    toast(`${order.id}: ${errorMessage(result.status, d)}${more}`, 'bad');
  }
  await refresh();
}

// ---------- new order ----------

const MENU = [
  ['Smash burger', 9.5], ['Fries', 4], ['Lemonade', 3.25], ['Pad thai', 13.9], ['Margherita pizza', 12], ['Miso soup', 3.5], ['Tiramisu', 6.75],
];

function itemRow(values = {}) {
  const row = h('div', { class: 'item' },
    h('input', { name: 'name', placeholder: 'Item', value: values.name || '', 'aria-label': 'Item name' }),
    h('input', { name: 'quantity', type: 'number', min: '1', step: '1', value: values.quantity || '1', 'aria-label': 'Quantity' }),
    h('input', { name: 'price', inputmode: 'decimal', placeholder: '0.00', value: values.price || '', 'aria-label': 'Price in dollars' }),
    h('button', { class: 'btn ghost icon x', type: 'button', 'aria-label': 'Remove item', text: '×', onclick: () => { row.remove(); updateFormTotal(); } }),
    h('small', { class: 'err', style: 'grid-column:1/-1' }));
  row.addEventListener('input', updateFormTotal);
  return row;
}

function readForm() {
  const f = $('#orderForm');
  return {
    id: f.elements.id.value, customerId: f.elements.customerId.value, restaurantId: f.elements.restaurantId.value,
    paymentType: f.elements.paymentType.value,
    items: [...$('#rows').children].map((row) => ({
      name: row.querySelector('[name=name]').value, quantity: row.querySelector('[name=quantity]').value, price: row.querySelector('[name=price]').value,
    })),
  };
}

function updateFormTotal() {
  const cents = readForm().items.reduce((sum, it) => {
    const price = priceToCents(it.price);
    const qty = Number(it.quantity);
    return price === null || !Number.isInteger(qty) || qty < 1 ? sum : sum + price * qty;
  }, 0);
  $('#formTotal').textContent = money(cents);
}

function showFormErrors(errors) {
  const f = $('#orderForm');
  f.querySelectorAll('[data-err]').forEach((el) => { el.textContent = errors[el.dataset.err] || ''; });
  for (const name of ['id', 'customerId', 'restaurantId', 'paymentType']) {
    f.elements[name].setAttribute('aria-invalid', errors[name] ? 'true' : 'false');
  }
  [...$('#rows').children].forEach((row, i) => {
    const e = errors[`items.${i}`] || {};
    row.querySelector('.err').textContent = Object.values(e).join('. ');
    for (const field of ['name', 'quantity', 'price']) row.querySelector(`[name=${field}]`).setAttribute('aria-invalid', e[field] ? 'true' : 'false');
  });
}

function openSheet() {
  const f = $('#orderForm');
  f.elements.id.value = newOrderId();
  f.elements.paymentType.value = 'card';
  $('#rows').replaceChildren(itemRow({ name: 'Smash burger', quantity: '1', price: '9.50' }));
  showFormErrors({});
  updateFormTotal();
  $('#sheet').showModal();
  f.elements.id.select();
}

function fillSample() {
  const picks = [...MENU].sort(() => Math.random() - 0.5).slice(0, 2 + Math.floor(Math.random() * 2));
  $('#rows').replaceChildren(...picks.map(([name, price]) => itemRow({ name, quantity: String(1 + Math.floor(Math.random() * 2)), price: price.toFixed(2) })));
  updateFormTotal();
}

async function submitOrder(event) {
  event.preventDefault();
  const result = validateOrderForm(readForm());
  showFormErrors(result.errors);
  if (!result.ok) return;
  $('#sheetSubmit').disabled = true;
  const res = await api('POST', '/orders', result.payload);
  $('#sheetSubmit').disabled = false;
  if (!res.ok) {
    const message = errorMessage(res.status, res.data);
    if (res.status === 409) showFormErrors({ id: message });
    else toast(message, 'bad');
    return;
  }
  $('#sheet').close();
  toast(`Order ${result.payload.id} placed`, 'ok');
  await refresh();
  openDrawer(result.payload.id);
}

async function createSamples() {
  const samples = [
    ['card', [['Smash burger', 2, 950], ['Fries', 1, 400]]],
    ['wallet', [['Pad thai', 1, 1390], ['Miso soup', 1, 350]]],
    ['cash', [['Margherita pizza', 1, 1200], ['Lemonade', 2, 325]]],
  ];
  for (const [paymentType, items] of samples) {
    await api('POST', '/orders', {
      id: newOrderId(), customerId: 'cust-1', restaurantId: 'rest-1', paymentType,
      items: items.map(([name, quantity, unitPriceCents]) => ({ sku: name.replace(/\W/g, '').toUpperCase().slice(0, 8), name, quantity, unitPriceCents })),
    });
  }
  await refresh();
}

// ---------- polling ----------

let inflight = false;

function setOnline(on, message) {
  if (state.online === on) { if (!on) $('#banner').textContent = message; return; }
  state.online = on;
  const live = $('#live');
  live.className = `live ${on ? 'on' : 'off'}`;
  $('#liveText').textContent = on ? 'Live' : 'Offline';
  $('#banner').hidden = on;
  if (!on) $('#banner').textContent = message;
}

async function refresh() {
  if (inflight) return;
  inflight = true;
  try {
    const res = await api('GET', '/orders?limit=100');
    if (!res.ok) { setOnline(false, `${errorMessage(res.status, res.data)} Retrying every few seconds.`); return; }
    setOnline(true);
    const orders = res.data.orders || [];
    announce(orders);
    state.orders = orders;
    state.byId = new Map(orders.map((o) => [o.id, o]));
    state.loaded = true;
    renderStats();
    renderBoard();
    if (state.selected) renderDrawer();
    await refreshAlerts();
  } finally {
    inflight = false;
  }
}

// ---------- start ----------

function applyTheme(theme) {
  if (theme) document.documentElement.dataset.theme = theme;
}

function init() {
  applyTheme(store('sd.theme'));
  buildBoard();
  renderFirstRun();
  renderStats();

  $('#newOrder').addEventListener('click', openSheet);
  $('#sheetClose').addEventListener('click', () => $('#sheet').close());
  $('#sheetCancel').addEventListener('click', () => $('#sheet').close());
  $('#addRow').addEventListener('click', () => { $('#rows').append(itemRow()); updateFormTotal(); });
  $('#sample').addEventListener('click', fillSample);
  $('#orderForm').addEventListener('submit', submitOrder);
  $('#scrim').addEventListener('click', closeDrawer);
  $('#alertsBtn').addEventListener('click', () => { state.alertSig = null; $('#alertsSheet').showModal(); renderAlerts(); });
  $('#alertsClose').addEventListener('click', () => $('#alertsSheet').close());
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !$('#sheet').open && !$('#alertsSheet').open) closeDrawer(); });
  $('#theme').addEventListener('click', () => {
    const dark = document.documentElement.dataset.theme
      ? document.documentElement.dataset.theme === 'dark'
      : !matchMedia('(prefers-color-scheme: light)').matches;
    const next = dark ? 'light' : 'dark';
    applyTheme(next);
    store('sd.theme', next);
  });

  refresh();
  setInterval(() => { if (!document.hidden) refresh(); }, POLL_MS);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
}

init();
