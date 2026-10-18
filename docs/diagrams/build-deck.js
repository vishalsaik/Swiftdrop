// Generator for SwiftDrop-Design-Patterns.excalidraw(.md), the hand-drawn companion to ../PATTERNS.md.
//
// It uses the gen5.js deck toolkit from the hld-deck-from-pdf Claude skill
// (C:\Users\saivi\.claude\skills\hld-deck-from-pdf\gen5.js, with check.js beside it). To rebuild:
//   1. copy gen5.js and check.js next to this file
//   2. node build-deck.js            (writes out5/SwiftDrop-Design-Patterns.excalidraw.md and .excalidraw)
//   3. node check.js out5/SwiftDrop-Design-Patterns.excalidraw.md     (must print ALL CLEAR)
//   4. copy the two files from out5/ into this folder
// The checker cannot see everything, so render the slides and look at them before committing.
// Lessons kept from building it: arrow heads use the plain triangle (hollow ones are not drawn by every
// Excalidraw build), long titles drop to a smaller font so they never wrap onto the subtitle, and the
// first exported frame needs a font warm-up in the renderer or it comes out in a plain sans font.

const fs = require('fs');
const path = require('path');
const G = require('./gen5.js');
const { GREEN, BLUE, PURPLE, ORANGE, RED, GRAY, YELLOW, box, freeText, connect, seqArrow, lifeline, frame, withFrame, build, SLIDE_W, nextSlideX } = G;
const MARGIN = 50;
const FULL_W = SLIDE_W - 2 * MARGIN;
const DECK = 'SwiftDrop-Design-Patterns';
const INK = '#1e1e1e', MUTED = '#495057';

// freeText() does not auto-wrap, so every caption goes through this first.
function wrapT(label, widthPx, fontSize) {
  const maxChars = Math.floor(widthPx / (fontSize * 0.85));
  const words = label.split(' ');
  const lines = [];
  let cur = '';
  for (const w of words) {
    const cand = cur ? cur + ' ' + w : w;
    if (cand.length > maxChars && cur) { lines.push(cur); cur = w; }
    else cur = cand;
  }
  if (cur) lines.push(cur);
  return lines.join('\n');
}

// height a box would get, without adding it to the scene
function boxH(w, label, fs, o = {}) {
  return box(0, 0, w, label, { fontSize: fs || 17, padX: o.padX, padY: o.padY }).height;
}

const els = [];

class Slide {
  constructor(sx) { this.sx = sx; this.maxY = 0; }
  note(x, y, label, o = {}) {
    const fs = o.fontSize || 18;
    const t = freeText(this.sx + x, y, wrapT(label, o.width || 700, fs), { fontSize: fs, color: o.color || INK, align: o.align });
    els.push(t.el);
    this.maxY = Math.max(this.maxY, t.bottom);
    return t;
  }
  node(x, y, w, label, col, o = {}) {
    const b = box(this.sx + x, y, w, label, {
      backgroundColor: col.bg, strokeColor: col.border, fontSize: o.fontSize || 17,
      height: o.height, padX: o.padX, padY: o.padY, dashedBorder: o.dashed, textColor: o.textColor,
    });
    els.push(b.rect, b.text);
    this.maxY = Math.max(this.maxY, b.bottom);
    return b;
  }
  arrow(a, b, o = {}) { els.push(connect(a, b, o)); }
  seq(x1, y1, x2, y2, label, o = {}) {
    const r = seqArrow(this.sx + x1, y1, this.sx + x2, y2, label, o);
    els.push(r.el);
    if (r.labelEls) els.push(...r.labelEls);
  }
  life(x, y1, y2) { els.push(lifeline(this.sx + x, y1, y2)); this.maxY = Math.max(this.maxY, y2); }
  // a small caption placed near an arrow
  tag(x, y, label, color, width) { return this.note(x, y, label, { fontSize: 14, color: color || MUTED, width: width || 260 }); }
}

function slide(name, title, subtitle, fn) {
  const sx = nextSlideX();
  const f = frame(sx, 0, SLIDE_W, 1000, name);
  els.push(f.el);
  withFrame(f.id, () => {
    const S = new Slide(sx);
    // long titles drop to a smaller size so they never wrap onto the subtitle
    S.note(MARGIN, 28, title, { fontSize: title.length > 48 ? 30 : 36, width: FULL_W });
    if (subtitle) S.note(MARGIN, 82, subtitle, { fontSize: 19, color: MUTED, width: FULL_W });
    fn(S);
    f.el.height = Math.max(S.maxY + 50, 720);
  });
}

// a row of equal-height boxes; items are [label, color, dashed?]
function row(S, x, y, w, gap, items, fs) {
  const h = Math.max(...items.map(it => boxH(w, it[0], fs || 17)));
  const out = [];
  let cx = x;
  items.forEach(it => { out.push(S.node(cx, y, w, it[0], it[1], { fontSize: fs || 17, height: h, dashed: it[2] })); cx += w + gap; });
  return out;
}
function link(S, bs, o) { for (let i = 0; i < bs.length - 1; i++) S.arrow(bs[i], bs[i + 1], o); }
function notes(S, lines, o = {}) {
  let y = S.maxY + (o.gap || 45);
  lines.forEach(l => { const t = S.note(o.x ?? MARGIN, y, l, { fontSize: o.fontSize || 19, width: o.width || 1550, color: o.color }); y = t.bottom + 14; });
}

// A sequence diagram. steps: {a,b,t,d,c} arrow | {note,at,w,col} | {branch,col}
function sequence(S, o) {
  const n = o.actors.length, aw = o.aw || 230, fs = o.fs || 16;
  const xs = o.actors.map((_, i) => o.x0 + aw / 2 + i * ((o.x1 - o.x0 - aw) / (n - 1)));
  const ah = Math.max(...o.actors.map(a => boxH(aw, a.label, fs)));
  const plan = [];
  let y = o.y0 + ah + 40;
  for (const s of o.steps) {
    if (s.t !== undefined) { plan.push({ s, y, kind: 'arrow' }); y += 84; }
    else if (s.note) { const w = s.w || 360; const h = boxH(w, s.note, 15, { padY: 12 }); plan.push({ s, y, kind: 'note', w, h }); y += h + 36; }
    else { const w = xs[n - 1] - xs[0] + aw; const h = boxH(w, s.branch, 15, { padY: 10 }); plan.push({ s, y, kind: 'branch', w, h }); y += h + 32; }
  }
  const yEnd = y;
  xs.forEach(x => S.life(x, o.y0 + ah, yEnd));
  o.actors.forEach((a, i) => S.node(xs[i] - aw / 2, o.y0, aw, a.label, a.col, { fontSize: fs, height: ah }));
  for (const p of plan) {
    const s = p.s;
    if (p.kind === 'arrow') {
      const ay = p.y + 50;
      S.seq(xs[s.a], ay, xs[s.b], ay, s.t, { dashed: s.d, strokeColor: s.c || INK, labelColor: s.c || INK, fontSize: 14 });
    } else if (p.kind === 'note') {
      const nx = Math.max(o.x0, Math.min(xs[s.at] - p.w / 2, o.x1 - p.w));
      S.node(nx, p.y, p.w, s.note, s.col || ORANGE, { fontSize: 15, padY: 12 });
    } else {
      S.node(xs[0] - aw / 2, p.y, p.w, s.branch, s.col || GRAY, { fontSize: 15, padY: 10, dashed: true });
    }
  }
  S.maxY = Math.max(S.maxY, yEnd);
  return yEnd;
}

const R = '#e03131', Gn = '#2f9e44', Bl = '#1971c2';

build(DECK, () => {

  // ================= 01 TITLE =================
  slide('01 Title', 'SwiftDrop: design patterns we use',
    'Where each pattern lives in the code, why it is there, and what it is easily confused with. A hand-drawn companion to docs/PATTERNS.md.', S => {
      row(S, 50, 230, 500, 50, [
        ['COVERED (built and tested)\n\nRepository\nAdapter\nStrategy\nFactory\nChain of Responsibility', GREEN],
        ['PARTLY (the idea is in the code)\n\nState, as a transition table (not one object per state)', YELLOW],
        ['PLANNED (from the project spec)\n\nDecorator\nObserver / Pub-Sub\nCommand\nBuilder\nFacade\nAbstract Factory', BLUE],
      ], 20);
      notes(S, [
        'Also in the doc: dependency injection, decline versus error, test doubles, atomic claim and lease, compare-and-set, cache-aside.',
        'The rule from the spec: a pattern has to solve a real problem here. If it would only exist to be a pattern, we do not build it.',
      ], { fontSize: 20, color: MUTED });
    });

  // ================= 02 MAP =================
  slide('02 The map', 'Where each pattern sits in the code today',
    'Follow one request from the client down to Postgres or Stripe.', S => {
      const client = S.node(50, 140, 300, 'Client or browser', GRAY);
      const mw = S.node(50, 260, 300, 'Middleware chain\nCORS, MaxBody, RequireJSON\n= Chain of Responsibility', PURPLE, { fontSize: 15 });
      const h = S.node(50, 470, 300, 'Handlers\n(Server methods)', BLUE);
      S.arrow(client, mw); S.arrow(mw, h);

      const repo = S.node(480, 130, 270, 'Repository\nports.IDatabase', PURPLE);
      const dba = S.node(820, 130, 270, 'DBAdapter\n= Adapter over pgx', GREEN);
      const pg = S.node(1160, 130, 270, 'Postgres', GRAY);
      S.arrow(h, repo); S.arrow(repo, dba); S.arrow(dba, pg);

      const fac = S.node(480, 380, 270, 'Factory\npayment.Router', ORANGE);
      const strat = S.node(820, 380, 270, 'PaymentProvider\n= Strategy', PURPLE);
      const gw = S.node(1160, 380, 270, 'Gateway\n= Adapter seam', GREEN);
      const ext = S.node(1160, 600, 270, 'StripeGateway\nor FakeGateway\n(no network)', GRAY);
      S.arrow(h, fac); S.arrow(fac, strat); S.arrow(strat, gw); S.arrow(gw, ext);

      const st = S.node(480, 640, 330, 'order.CanTransition\n= State as a table', YELLOW);
      S.arrow(h, st);
      notes(S, ['Handlers depend on interfaces. They never import pgx or Stripe, so tests plug in fakes.'], { x: 480, width: 1100 });
    });

  // ================= 03 REPOSITORY =================
  slide('03 Repository', 'Repository: storage behind an interface',
    'Callers ask for orders in the domain language. They never write SQL or see a database driver.', S => {
      const srv = S.node(60, 260, 280, 'Server\n(the handlers)\nholds DB IDatabase', BLUE);
      const idb = S.node(500, 190, 340, 'IDatabase\n<<interface>>\nCreateOrder\nGetOrderbyId\nUpdateOrderState\nSetFoodReady\nAcceptOrder', PURPLE, { fontSize: 16 });
      const dba = S.node(1010, 140, 300, 'DBAdapter\nproduction, uses pgx', GREEN);
      const fake = S.node(1010, 400, 300, 'fakeDB\ntests, in memory', ORANGE);
      const pg = S.node(1420, 140, 220, 'Postgres', GRAY);
      S.arrow(srv, idb);
      S.arrow(dba, idb, { dashed: true });
      S.arrow(fake, idb, { dashed: true });
      S.arrow(dba, pg);
      notes(S, [
        'Why: handlers must not know pgx exists, and tests must run with no database.',
        'Trade-off: the interface grows with the handlers, so keep it narrow. IDatabase is really the ORDER repository today.',
        'Not the same as Adapter: a repository is a role (domain-shaped storage). DBAdapter also adapts pgx to that role. One type can be both.',
      ]);
    });

  // ================= 04 ADAPTER =================
  slide('04 Adapter', 'Adapter: make a foreign API fit our interface',
    'StripeGateway translates on the way out and again on the way back. The second translation is where the meaning is.', S => {
      sequence(S, {
        x0: 60, x1: 1640, y0: 140, aw: 320,
        actors: [
          { label: 'PayOrder handler', col: BLUE },
          { label: 'StripeGateway\n(the Adapter)', col: GREEN },
          { label: 'Stripe API', col: PURPLE },
        ],
        steps: [
          { a: 0, b: 1, t: 'Charge(our PaymentRequest)' },
          { note: 'translate to Stripe form: amount, lowercase currency, payment_method', at: 1, w: 520 },
          { a: 1, b: 2, t: 'POST /v1/payment_intents, bearer key, Idempotency-Key' },
          { a: 2, b: 1, t: 'HTTP status and JSON body', d: true },
          { note: 'translate back: read the HTTP status FIRST', at: 1, w: 420 },
          { branch: 'if 2xx and status succeeded', col: GREEN },
          { a: 1, b: 0, t: 'authorized', d: true, c: Gn },
          { branch: 'if 402 with card_error', col: ORANGE },
          { a: 1, b: 0, t: 'declined + reason', d: true, c: '#e8590c' },
          { branch: 'anything else', col: RED },
          { a: 1, b: 0, t: 'error: outcome unknown', d: true, c: R },
        ],
      });
      notes(S, ['An adapter translates MEANING, not only shape: a refused card and a wrong API key must not both become "declined".']);
    });

  // ================= 05 STRATEGY =================
  slide('05 Strategy', 'Strategy: one job, three ways to do it',
    'One job, three behaviours: card, wallet, cash. The handler keeps no growing switch.', S => {
      const pp = S.node(450, 130, 500, 'PaymentProvider  <<interface>>\nAuthorize(ctx, req) -> PaymentResult', PURPLE);
      const card = S.node(290, 440, 260, 'CardProvider', GREEN);
      const wallet = S.node(570, 440, 260, 'WalletProvider', GREEN);
      const cash = S.node(850, 440, 260, 'CashProvider\n(no gateway)', GREEN);
      [card, wallet, cash].forEach(b => S.arrow(b, pp, { dashed: true }));
      const gw = S.node(315, 650, 500, 'Gateway  <<interface>>\nCharge(ctx, req)   the Adapter seam', PURPLE);
      S.arrow(card, gw); S.arrow(wallet, gw);

      const bad = S.node(1180, 130, 470, 'TEXTBOOK CONTEXT (shared, mutable)\nA: SetStrategy(card)\nB: SetStrategy(wallet)\nA: ProcessPayment()\nA is charged with the WALLET strategy. A race.', RED, { fontSize: 16 });
      S.node(1180, bad.bottom + 40, 470, 'SWIFTDROP (no shared state)\nrequest A: Payments.For(card), a local variable\nrequest B: Payments.For(wallet), a local variable\nEach request owns its strategy.', GREEN, { fontSize: 16 });
      notes(S, [
        'Strategies are built once in main.go and shared by every request, so they must be safe for concurrent use (today they are stateless).',
        'Not the same as State: with Strategy the CALLER picks the behaviour. With State the object decides from its own condition.',
      ]);
    });

  // ================= 06 FACTORY =================
  slide('06 Factory', 'Factory: decide at request time, not at wiring time',
    'The right provider depends on order.PaymentType, which is only known after the order is loaded.', S => {
      sequence(S, {
        x0: 50, x1: 1650, y0: 140, aw: 250,
        actors: [
          { label: 'main.go', col: GRAY },
          { label: 'Router\n(the factory)', col: ORANGE },
          { label: 'PayOrder\nhandler', col: BLUE },
          { label: 'IDatabase\n(Repository)', col: PURPLE },
          { label: 'PaymentProvider\n(Strategy)', col: GREEN },
        ],
        steps: [
          { branch: 'WIRING TIME: once, at startup', col: YELLOW },
          { a: 0, b: 1, t: 'NewRouter(card, cash, wallet)' },
          { a: 0, b: 2, t: 'NewServer(db, router)' },
          { branch: 'REQUEST TIME: for every order', col: BLUE },
          { a: 2, b: 3, t: 'GetOrderbyId' },
          { a: 3, b: 2, t: 'order, PaymentType = wallet', d: true },
          { a: 2, b: 1, t: 'For(wallet)' },
          { a: 1, b: 2, t: 'WalletProvider', d: true },
          { a: 2, b: 4, t: 'Authorize(req)' },
        ],
      });
      notes(S, [
        'What is passed to the handler is the CHOOSER, not a provider. A provider chosen at wiring time is one fixed provider, wrong for two of the three types.',
        'The lookup can fail (a legacy order with an empty type), so it runs BEFORE any state is saved. Otherwise the order would be stranded in payment_pending.',
      ]);
    });

  // ================= 07 CHAIN =================
  slide('07 Chain', 'Chain of Responsibility: a line of gatekeepers',
    'Each link may answer the request, stop it, or pass it on. Two chains live in SwiftDrop.', S => {
      S.note(60, 140, 'Chain 1: HTTP middleware', { fontSize: 22, color: Bl, width: 600 });
      const c = S.node(60, 190, 380, 'Client', GRAY);
      const cors = S.node(60, 300, 380, 'CORS\noutermost, wraps the router', PURPLE);
      const mb = S.node(60, 440, 380, 'MaxBody\ncaps body at 1 MB', PURPLE);
      const rj = S.node(60, 580, 380, 'RequireJSON', PURPLE);
      const hd = S.node(60, 700, 380, 'Route handler', BLUE);
      S.arrow(c, cors); S.arrow(cors, mb); S.arrow(mb, rj); S.arrow(rj, hd);
      const x204 = S.node(540, 315, 380, '204: preflight answered here', ORANGE, { fontSize: 15 });
      const x415 = S.node(540, 570, 380, '415: wrong Content-Type', RED, { fontSize: 15 });
      const x413 = S.node(540, 700, 380, '413: body over 1 MB, when read', RED, { fontSize: 15 });
      S.arrow(cors, x204, { dashed: true }); S.arrow(rj, x415, { dashed: true }); S.arrow(hd, x413, { dashed: true });

      S.note(1000, 140, 'Chain 2: order validators', { fontSize: 22, color: Bl, width: 600 });
      const rules = ['RequireID', 'RequireCustomerID', 'RequireRestaurantID', 'RequirePaymentType', 'RequireItems', 'RequirePositiveQuantities', 'RequireNonNegativePrices']
        .map((r, i) => S.node(1000, 200 + i * 95, 410, r, GREEN, { fontSize: 16 }));
      link(S, rules);
      const fail = S.node(1480, 410, 200, '422 with its message. First failure wins.', RED, { fontSize: 15 });
      S.arrow(rules[0], fail, { dashed: true }); S.arrow(rules[3], fail, { dashed: true }); S.arrow(rules[6], fail, { dashed: true });
      notes(S, [
        'CORS must be outermost so even 413, 415 and 422 errors carry the CORS headers. mux skips middleware for unmatched OPTIONS, so CORS wraps the whole router.',
        'Looks like Decorator (it wraps a handler), but the intent is gatekeeping: each link is allowed to stop the request.',
      ], { gap: 40 });
    });

  // ================= 08 STATE =================
  slide('08 State', 'State: the order lifecycle is a table',
    'order.legalTransitions says what is legal. CanTransition and Apply read it. Handlers answer 409 for an illegal move.', S => {
      const bs = row(S, 50, 250, 240, 24, [
        ['created', GRAY], ['payment_pending', ORANGE], ['paid', GREEN], ['preparing', BLUE], ['picked_up', PURPLE], ['delivered', GREEN],
      ], 15);
      link(S, bs);
      const canc = S.node(350, 800, 320, 'cancelled\n(terminal)', RED, { fontSize: 18 });
      [bs[0], bs[1], bs[2], bs[3]].forEach(b => S.arrow(b, canc, { dashed: true, strokeColor: R }));
      S.node(1150, 440, 500, 'Entering picked_up also needs two facts that are NOT states: FoodReady and AssignedDriverID.', ORANGE, { fontSize: 16 });
      S.note(1230, 360, 'delivered is terminal too', { fontSize: 16, color: MUTED, width: 400 });
      notes(S, [
        'State as DATA: one table and two small functions, easy to test exhaustively. Behaviour per state lives in the handlers.',
        'A table says what is LEGAL. It does not make the write happen once: two requests can both pass CanTransition. Compare-and-set closes that.',
      ], { gap: 60 });
    });

  // ================= 09 DECLINE VS ERROR =================
  slide('09 Decline vs error', 'Decline versus error: three outcomes, not two',
    'StripeGateway reads the HTTP status first. A rejected request is not a declined card.', S => {
      const reply = S.node(650, 130, 400, 'Reply from the provider', GRAY);
      const q = S.node(650, 250, 400, 'HTTP status?', YELLOW, { dashed: true });
      S.arrow(reply, q);
      const c402 = S.node(60, 420, 420, '402 with error.type card_error', ORANGE);
      const c2xx = S.node(640, 420, 420, '2xx: a PaymentIntent.\nWhat is its status?', ORANGE);
      const coth = S.node(1220, 420, 420, '400, 401, 403, 404, 409,\n424, 429 or 5xx', ORANGE);
      [c402, c2xx, coth].forEach(b => S.arrow(q, b));
      const dec = S.node(60, 740, 420, 'DECLINED: a definite no\norder -> cancelled, answer 402 and the reason', RED, { fontSize: 16 });
      const app = S.node(640, 740, 420, 'APPROVED: a definite yes\norder -> paid, answer 200', GREEN, { fontSize: 16 });
      const err = S.node(1220, 740, 420, 'ERROR: we do not know\norder stays payment_pending, answer 500, cause only in the log', ORANGE, { fontSize: 16 });
      S.arrow(c402, dec); S.arrow(coth, err);
      S.arrow(c2xx, app); S.arrow(c2xx, dec); S.arrow(c2xx, err);
      S.tag(875, 640, 'succeeded', Gn, 200);
      S.tag(330, 600, 'requires_payment_method + a recorded error', R, 300);
      S.tag(1175, 600, 'processing, requires_action, anything else', '#e8590c', 230);
      notes(S, ['Treat an error as a decline and a customer order is cancelled for no reason. Treat a decline as an error and orders get stuck.'], { gap: 40 });
    });

  // ================= 10 DECORATOR =================
  slide('10 Decorator', 'Decorator (planned): wrap, same interface, add behaviour',
    'Every wrapper IS a Gateway and HAS a Gateway, so wrappers stack: retry, timeout, cache.', S => {
      const gw = S.node(550, 130, 600, 'Gateway  <<interface>>\nCharge(ctx, req) -> PaymentResult', PURPLE);
      const stripe = S.node(460, 430, 240, 'StripeGateway\nthe real one', GREEN);
      const retry = S.node(730, 430, 240, 'Retry\nholds next Gateway', BLUE);
      const tmo = S.node(1000, 430, 240, 'Timeout\nholds next Gateway', BLUE);
      [stripe, retry, tmo].forEach(b => S.arrow(b, gw, { dashed: true }));

      S.note(60, 600, 'The order you wrap in decides the behaviour:', { fontSize: 20, color: Bl, width: 800 });
      row(S, 50, 650, 250, 20, [
        ['Charge call', GRAY], ['Logging', BLUE], ['Circuit breaker', BLUE], ['Retry, errors only', BLUE], ['Timeout per attempt', BLUE], ['StripeGateway', GREEN],
      ], 17).forEach((b, i, all) => { if (i < all.length - 1) S.arrow(b, all[i + 1]); });

      S.note(60, 840, 'The same idea on the payment store (the cache):', { fontSize: 20, color: Bl, width: 800 });
      const svc = S.node(60, 890, 200, 'Service', GRAY);
      const cps = S.node(340, 890, 320, 'CachedPaymentStore', BLUE);
      const cache = S.node(800, 890, 340, 'cache\na disposable copy', ORANGE);
      const ps = S.node(800, 1040, 340, 'PaymentStore', PURPLE);
      const tbl = S.node(1280, 1040, 360, 'payments table\nsource of truth, holds the lease', GREEN, { fontSize: 16 });
      S.arrow(svc, cps); S.arrow(cps, cache); S.arrow(cps, ps); S.arrow(ps, tbl);
      S.tag(700, 893, 'hit', Gn, 100);
      S.tag(600, 1040, 'miss, then fill', MUTED, 200);
      notes(S, [
        'Never retry a call that is not safe to repeat. Retrying a charge needs a stable idempotency key, and only ERRORS are retryable, never a decline.',
        'Not the same as Adapter: an adapter changes the interface, a decorator keeps it.',
      ], { gap: 40 });
    });

  // ================= 11 OBSERVER + COMMAND =================
  slide('11 Observer and Command', 'Observer and Command (planned)',
    'Both are about events and actions, and both are where concurrency really starts.', S => {
      S.note(60, 130, 'Observer: react without knowing who', { fontSize: 21, color: Bl, width: 1000 });
      const svc = S.node(60, 330, 230, 'Order service\na state changed', BLUE);
      const bus = S.node(370, 330, 200, 'Event bus', PURPLE);
      const subs = ['Customer notifier', 'Restaurant notifier', 'Driver notifier', 'Analytics', 'SSE stream for one order']
        .map((l, i) => S.node(680, 190 + i * 100, 320, l, GREEN, { fontSize: 16 }));
      S.arrow(svc, bus);
      subs.forEach(b => S.arrow(bus, b));

      S.note(1140, 130, 'Command: action as a value', { fontSize: 20, color: Bl, width: 510 });
      const h = S.node(1140, 190, 480, 'HTTP handler', BLUE);
      const cmd = S.node(1140, 300, 480, 'Command value\naction, order id, idempotency key', PURPLE, { fontSize: 16 });
      const q = S.node(1140, 450, 480, 'Bounded queue', ORANGE);
      const w = S.node(1140, 560, 480, 'Worker pool', GREEN);
      const sm = S.node(1140, 670, 480, 'State check and compare-and-set', GRAY, { fontSize: 16 });
      link(S, [h, cmd, q, w, sm]);
      notes(S, [
        'Observer: a slow subscriber must not block the publisher (bounded buffers), events for one order stay in order, and cancelling a subscriber must stop its goroutine.',
        'Command: queued work can run twice or out of order. The state machine and compare-and-set are what keep that safe.',
      ], { gap: 50 });
    });

  // ================= 12 FACADE + BUILDER + ABSTRACT FACTORY =================
  slide('12 Facade, Builder, Abstract Factory', 'Facade, Builder and Abstract Factory (planned)',
    'Three smaller patterns. Facade is the one the code is already asking for.', S => {
      S.note(60, 130, 'Facade: PayOrder is about 70 lines of orchestration inside an HTTP handler', { fontSize: 20, color: Bl, width: 1500 });
      const t0 = S.node(60, 190, 260, 'TODAY\nPayOrder handler:\nHTTP + orchestration', RED, { fontSize: 15 });
      const t1 = S.node(420, 190, 200, 'IDatabase', PURPLE, { fontSize: 15 });
      const t2 = S.node(420, 270, 200, 'order state', YELLOW, { fontSize: 15 });
      const t3 = S.node(420, 350, 250, 'payment.Router', ORANGE, { fontSize: 15 });
      [t1, t2, t3].forEach(b => S.arrow(t0, b));
      const p0 = S.node(800, 190, 230, 'PLANNED\nhandler: HTTP only', GREEN, { fontSize: 15 });
      const fac = S.node(1110, 190, 240, 'OrderService\nthe facade', BLUE, { fontSize: 15 });
      const f1 = S.node(1430, 190, 210, 'repositories', PURPLE, { fontSize: 15 });
      const f2 = S.node(1430, 270, 210, 'order state', YELLOW, { fontSize: 15 });
      const f3 = S.node(1430, 350, 210, 'payments', ORANGE, { fontSize: 15 });
      const f4 = S.node(1430, 430, 210, 'events', GRAY, { fontSize: 15 });
      S.arrow(p0, fac);
      [f1, f2, f3, f4].forEach(b => S.arrow(fac, b));

      S.note(60, 520, 'Builder: build a complex object step by step', { fontSize: 20, color: Bl, width: 1500 });
      const bld = row(S, 60, 580, 270, 50, [['WithCustomer', BLUE], ['WithItems', BLUE], ['WithPayment', BLUE], ['Build and validate', GREEN], ['Order', GRAY]], 16);
      link(S, bld);

      S.note(60, 700, 'Abstract Factory: a whole family used together (names are made up)', { fontSize: 20, color: Bl, width: 1500 });
      const rf = S.node(560, 760, 580, 'RegionFactory  <<interface>>\nPayment()  Routing()  Notify()', PURPLE, { fontSize: 16 });
      const eu = S.node(450, 990, 380, 'EUFactory:\nEU payment, EU routing, EU notify', GREEN, { fontSize: 16 });
      const us = S.node(870, 990, 380, 'USFactory:\nUS payment, US routing, US notify', GREEN, { fontSize: 16 });
      S.arrow(eu, rf, { dashed: true }); S.arrow(us, rf, { dashed: true });
      notes(S, ['payment.Router makes ONE product by type. An abstract factory returns the whole set, chosen once per region.'], { gap: 40 });
    });

  // ================= 13 DI + HEXAGONAL + SENTINEL =================
  slide('13 DI, ports, sentinels', 'Techniques we rely on: injection, ports, sentinel errors',
    'Not GoF patterns, but they are why the tests can run the same handler that ships.', S => {
      S.note(60, 130, 'Dependency injection: receive what you need, do not create it', { fontSize: 20, color: Bl, width: 1500 });
      const m = S.node(60, 190, 240, 'main.go', GRAY);
      const srvP = S.node(560, 190, 340, 'Server\n(real DBAdapter + Router)', BLUE, { fontSize: 16 });
      const t = S.node(60, 330, 240, 'a test', ORANGE);
      const srvT = S.node(560, 330, 340, 'Server\n(fakeDB + stub gateway)', BLUE, { fontSize: 16 });
      S.arrow(m, srvP); S.arrow(t, srvT);
      S.tag(310, 200, 'DBAdapter, Router', MUTED, 240);
      S.tag(310, 340, 'fakeDB, stub', MUTED, 240);
      S.node(1000, 190, 640, 'The SAME handler code runs in production and in tests. Only what is handed to NewServer differs.', GREEN, { fontSize: 16 });

      S.note(60, 480, 'Ports and adapters: dependencies point inward', { fontSize: 20, color: Bl, width: 1500 });
      const hd = S.node(60, 540, 240, 'Handlers\nand domain', BLUE);
      const p1 = S.node(480, 540, 300, 'port: IDatabase', PURPLE);
      const p2 = S.node(480, 650, 300, 'port: Gateway', PURPLE);
      const a1 = S.node(960, 540, 340, 'DBAdapter (pgx, Postgres)', GREEN, { fontSize: 16 });
      const a2 = S.node(960, 650, 340, 'StripeGateway (HTTP)', GREEN, { fontSize: 16 });
      const a3 = S.node(960, 760, 340, 'FakeGateway (no network)', GREEN, { fontSize: 16 });
      S.arrow(hd, p1); S.arrow(hd, p2);
      S.arrow(a1, p1, { dashed: true }); S.arrow(a2, p2, { dashed: true }); S.arrow(a3, p2, { dashed: true });

      S.note(60, 900, 'Sentinel errors: keep driver types on their side of the wall', { fontSize: 20, color: Bl, width: 1500 });
      const e0 = S.node(60, 960, 360, 'pgx: ErrNoRows or\nSQLSTATE 23505', GRAY, { fontSize: 15 });
      const e1 = S.node(540, 960, 300, 'translate() in DBAdapter', GREEN, { fontSize: 15 });
      const e2 = S.node(960, 960, 300, 'ErrOrderNotFound', PURPLE, { fontSize: 15 });
      const e3 = S.node(960, 1050, 300, 'ErrOrderExists', PURPLE, { fontSize: 15 });
      const e4 = S.node(1380, 960, 260, 'errors.Is -> 404', BLUE, { fontSize: 15 });
      const e5 = S.node(1380, 1050, 260, 'errors.Is -> 409', BLUE, { fontSize: 15 });
      S.arrow(e0, e1); S.arrow(e1, e2); S.arrow(e1, e3); S.arrow(e2, e4); S.arrow(e3, e5);
    });

  // ================= 14 CLAIM DECISION =================
  slide('14 Claim decision', 'Idempotent /pay (planned): new or retry? The row decides',
    'One endpoint. The server tells new from retry by whether a payment row exists for the order.', S => {
      const req = S.node(330, 130, 340, 'POST /orders/o1/pay', GRAY);
      const q = S.node(330, 240, 340, 'payment row for o1?', YELLOW, { dashed: true });
      S.arrow(req, q);
      S.tag(130, 440, 'no: this is NEW', Gn, 220);
      S.tag(720, 440, 'yes: this is a RETRY', Bl, 260);

      const n1 = S.node(60, 560, 340, 'NEW: insert the row atomically\nnew payment id, status processing, lease set', GREEN, { fontSize: 15 });
      const n2 = S.node(60, 740, 340, 'call the provider\npayment id = the idempotency key', GREEN, { fontSize: 15 });
      const n3 = S.node(60, 900, 340, 'record authorized or declined\nand move the order', GREEN, { fontSize: 15 });
      link(S, [n1, n2, n3]);

      const st = S.node(680, 560, 260, 'status?', YELLOW, { dashed: true });
      const ra = S.node(480, 810, 220, 'authorized:\nreplay the success', BLUE, { fontSize: 15 });
      const rd = S.node(740, 810, 220, 'declined:\nreplay the decline', BLUE, { fontSize: 15 });
      const pr = S.node(1000, 810, 260, 'processing:\nlease expired?', YELLOW, { fontSize: 15, dashed: true });
      S.arrow(q, n1); S.arrow(q, st);
      [ra, rd, pr].forEach(b => S.arrow(st, b));
      const no = S.node(870, 1000, 190, 'no: answer\n409 in progress', ORANGE, { fontSize: 15 });
      const yes = S.node(1090, 1000, 240, 'yes: take over\nagainst the SAME id', RED, { fontSize: 15 });
      S.arrow(pr, no); S.arrow(pr, yes);

      S.note(1330, 130, 'The payment row\'s life', { fontSize: 19, color: Bl, width: 360 });
      const sp = S.node(1360, 200, 280, 'processing\nclaim, new payment id', ORANGE, { fontSize: 15 });
      const sa = S.node(1320, 400, 180, 'authorized', GREEN, { fontSize: 15 });
      const sd = S.node(1510, 400, 150, 'declined', RED, { fontSize: 15 });
      S.arrow(sp, sa); S.arrow(sp, sd);
      S.node(1340, 520, 310, 'lease expired:\ntake over and reconcile.\nIt stays processing, and the id never changes.', YELLOW, { fontSize: 15 });
      notes(S, ['The lease is the claimant telling other requests: stay away until this time, then you may take over. It is a column in the row, not a cache entry.'], { gap: 50 });
    });

  // ================= 15 RACE =================
  slide('15 Two requests one winner', 'The claim: two requests arrive together, one wins',
    'A read followed by a write is not enough, because both requests can read "nothing yet". The database picks the winner.', S => {
      sequence(S, {
        x0: 60, x1: 1640, y0: 140, aw: 300,
        actors: [
          { label: 'Request A', col: BLUE },
          { label: 'Request B', col: BLUE },
          { label: 'Database', col: PURPLE },
        ],
        steps: [
          { a: 0, b: 2, t: 'INSERT payment for o1' },
          { a: 1, b: 2, t: 'INSERT payment for o1' },
          { a: 2, b: 0, t: '1 row inserted: you own it', d: true, c: Gn },
          { a: 2, b: 1, t: 'conflict, 0 rows', d: true, c: R },
          { note: 'A calls the provider with the payment id', at: 0, w: 420, col: GREEN },
          { a: 1, b: 2, t: 'read the existing row' },
          { a: 2, b: 1, t: 'processing, lease valid', d: true },
          { note: 'B answers "in progress" and never calls the provider', at: 1, w: 520, col: ORANGE },
        ],
      });
      notes(S, ['The same idea one level down: compare-and-set on the order state. UPDATE ... WHERE state = created updates one row for exactly one request.']);
    });

  // ================= 16 CRASH + RECONCILE =================
  slide('16 Crash and reconcile', 'The lease: the owner crashes, a later retry reconciles',
    'The row stays. The retry asks the provider again with the SAME payment id, never a new one.', S => {
      sequence(S, {
        x0: 50, x1: 1650, y0: 140, aw: 300,
        actors: [
          { label: 'Request A\noriginal owner', col: BLUE },
          { label: 'payments row', col: PURPLE },
          { label: 'Request B\na later retry', col: BLUE },
          { label: 'Provider', col: GREEN },
        ],
        steps: [
          { a: 0, b: 1, t: 'claim pay-7f3, lease until t = 30s' },
          { a: 0, b: 3, t: 'charge with key pay-7f3' },
          { note: 'the process crashes before recording the result', at: 0, w: 380, col: RED },
          { a: 2, b: 1, t: 'retry at t = 10s' },
          { a: 1, b: 2, t: 'processing, lease valid: in progress', d: true },
          { a: 2, b: 1, t: 'retry at t = 45s' },
          { a: 1, b: 2, t: 'lease expired', d: true, c: '#e8590c' },
          { a: 2, b: 1, t: 'take over (compare-and-set)' },
          { a: 2, b: 3, t: 'ask with the SAME key pay-7f3' },
          { a: 3, b: 2, t: 'the saved answer, or runs it once now', d: true, c: Gn },
          { a: 2, b: 1, t: 'record it, only if still processing' },
        ],
      });
      notes(S, ['Stripe replays the saved response for a key, but may prune keys after 24 hours. A key reused after pruning is a NEW charge, so a new payment id is never the answer to an expiry.']);
    });

  // ================= 17 CAS + CACHE + POOL =================
  slide('17 CAS, cache-aside, pool', 'Compare-and-set, cache-aside and the worker pool (planned)',
    'Three small mechanisms around the claim.', S => {
      S.note(60, 130, 'Compare-and-set: write only if unchanged', { fontSize: 20, color: Bl, width: 760 });
      const y1 = sequence(S, {
        x0: 40, x1: 800, y0: 190, aw: 200, fs: 15,
        actors: [
          { label: 'Request A', col: BLUE },
          { label: 'Request B', col: BLUE },
          { label: 'Database', col: PURPLE },
        ],
        steps: [
          { a: 0, b: 2, t: 'read: state = created' },
          { a: 1, b: 2, t: 'read: state = created' },
          { a: 0, b: 2, t: 'UPDATE ... WHERE created' },
          { a: 2, b: 0, t: '1 row updated', d: true, c: Gn },
          { a: 1, b: 2, t: 'UPDATE ... WHERE created' },
          { a: 2, b: 1, t: '0 rows: lost the race', d: true, c: R },
          { note: 'B answers 409', at: 1, w: 240, col: ORANGE },
        ],
      });

      S.note(900, 130, 'Cache-aside: a copy, the DB decides', { fontSize: 20, color: Bl, width: 740 });
      sequence(S, {
        x0: 900, x1: 1650, y0: 190, aw: 200, fs: 15,
        actors: [
          { label: 'Service', col: BLUE },
          { label: 'Cache', col: ORANGE },
          { label: 'Database', col: PURPLE },
        ],
        steps: [
          { a: 0, b: 1, t: 'get payment' },
          { branch: 'hit', col: GREEN },
          { a: 1, b: 0, t: 'the copy', d: true, c: Gn },
          { branch: 'miss', col: ORANGE },
          { a: 0, b: 2, t: 'read the row' },
          { a: 2, b: 0, t: 'the row', d: true },
          { a: 0, b: 1, t: 'fill with a TTL' },
        ],
      });

      S.note(60, Math.max(S.maxY, y1) + 50, 'Bounded worker pool: a burst can not create unbounded goroutines', { fontSize: 20, color: Bl, width: 1500 });
      const top = S.maxY + 80;
      const inw = S.node(60, top, 260, 'Incoming work', GRAY);
      const q = S.node(400, top, 260, 'Bounded queue', ORANGE);
      const w1 = S.node(780, top - 50, 220, 'worker 1', GREEN);
      const w2 = S.node(780, top + 40, 220, 'worker 2', GREEN);
      const w3 = S.node(780, top + 130, 220, 'worker N', GREEN);
      const bp = S.node(330, top + 150, 420, 'queue full: backpressure.\nReject or wait, never grow forever.', RED, { fontSize: 16 });
      S.arrow(inw, q); [w1, w2, w3].forEach(b => S.arrow(q, b));
      S.arrow(q, bp, { dashed: true });
    });

  // ================= 18 SAME SHAPE =================
  slide('18 Same shape', 'Same shape, different intent',
    'Many implementations behind one interface is just polymorphism. The question that names the pattern: what is this FOR?', S => {
      const rowsY = [150, 360, 570, 780];
      const heads = [
        ['STRATEGY\nthe caller picks one', GREEN],
        ['DECORATOR\nwraps and stacks, same interface', BLUE],
        ['ADAPTER\ntranslates a different interface', PURPLE],
        ['CHAIN\neach link may stop the request', ORANGE],
      ];
      heads.forEach((hd, i) => S.node(50, rowsY[i], 360, hd[0], hd[1], { fontSize: 17, height: 110 }));
      // strategy
      const s0 = S.node(500, rowsY[0] + 25, 200, 'Caller', GRAY, { height: 60 });
      const s1 = S.node(760, rowsY[0] + 25, 200, 'interface', PURPLE, { height: 60 });
      const sa = S.node(1040, rowsY[0] - 10, 200, 'impl A', GREEN, { height: 55 });
      const sb = S.node(1040, rowsY[0] + 65, 200, 'impl B', GREEN, { height: 55 });
      S.arrow(s0, s1); S.arrow(s1, sa); S.arrow(s1, sb);
      // decorator
      const d = row(S, 500, rowsY[1] + 25, 200, 60, [['Caller', GRAY], ['wrapper', BLUE], ['same interface', PURPLE], ['real thing', GREEN]], 17);
      link(S, d);
      // adapter
      const a = row(S, 500, rowsY[2] + 25, 200, 60, [['Caller', GRAY], ['our interface', PURPLE], ['adapter', GREEN], ['foreign API', GRAY]], 17);
      link(S, a);
      // chain
      const c = row(S, 500, rowsY[3] + 5, 200, 60, [['Request', GRAY], ['link 1', ORANGE], ['link 2', ORANGE], ['handler', BLUE]], 17);
      link(S, c);
      const stop = S.node(730, rowsY[3] + 85, 260, 'may answer: stop', RED, { height: 50, fontSize: 15 });
      S.arrow(c[1], stop, { dashed: true });
    });

  // ================= 19 WHICH PATTERN =================
  slide('19 Which pattern', 'Which pattern do I need?',
    'Read down the left column. The first question you can answer "yes" names the pattern.', S => {
      const ds = [
        'Swap behaviour at runtime?', 'Wrapping something?', 'A line of checks that can stop the request?',
        'Legal moves between conditions?', 'Many parties react to an event?', 'Queue, log or replay an action?', 'Hide how storage works?',
      ];
      const ys = [150, 280, 560, 690, 820, 950, 1080];
      const dn = ds.map((t, i) => S.node(60, ys[i], 480, t, YELLOW, { fontSize: 17, dashed: true }));
      link(S, dn);
      const yes = (d, label, col, y) => {
        const o = S.node(700, y, 600, label, col, { fontSize: 17 });
        S.arrow(d, o);
        return o;
      };
      yes(dn[0], 'Strategy, plus a Factory if the choice depends on data', GREEN, ys[0]);
      yes(dn[1], 'a foreign interface: Adapter', PURPLE, 250);
      yes(dn[1], 'same interface, add behaviour: Decorator', BLUE, 340);
      yes(dn[1], 'many subsystems, one entry: Facade', BLUE, 430);
      yes(dn[2], 'Chain of Responsibility', ORANGE, ys[2]);
      yes(dn[3], 'State', YELLOW, ys[3]);
      yes(dn[4], 'Observer / Pub-Sub', GREEN, ys[4]);
      yes(dn[5], 'Command', PURPLE, ys[5]);
      yes(dn[6], 'Repository', GREEN, ys[6]);
      const none = S.node(60, 1240, 480, 'Maybe none. Do not force a pattern.', GRAY, { fontSize: 17 });
      S.arrow(dn[6], none);
      S.tag(560, 150, 'yes', Gn, 80);
      S.tag(560, 560, 'yes', Gn, 80);
      S.tag(318, 222, 'no', R, 80);
      S.tag(318, 470, 'no', R, 80);
      S.tag(318, 1165, 'no', R, 80);
      S.note(1340, 150, 'Construction with many optional parts? Try a constructor or options first, and only then a Builder.', { fontSize: 17, color: MUTED, width: 300 });
    });

  // ================= 20 THE /pay FLOW =================
  slide('20 The pay flow', 'POST /orders/{id}/pay: every participant tagged by pattern',
    'The patterns work together. Each arrow is a call, each tag is the role the participant plays.', S => {
      sequence(S, {
        x0: 40, x1: 1660, y0: 140, aw: 240, fs: 15,
        actors: [
          { label: 'Client', col: GRAY },
          { label: 'Middleware chain\n(Chain)', col: PURPLE },
          { label: 'PayOrder\nhandler', col: BLUE },
          { label: 'IDatabase\n(Repository)', col: PURPLE },
          { label: 'Router\n(Factory)', col: ORANGE },
          { label: 'Provider + Gateway\n(Strategy, Adapter)', col: GREEN },
        ],
        steps: [
          { a: 0, b: 1, t: 'POST /orders/o1/pay' },
          { a: 1, b: 2, t: 'the checks pass' },
          { a: 2, b: 3, t: 'GetOrderbyId' },
          { note: 'CanTransition: created to payment_pending (State)', at: 2, w: 420 },
          { a: 2, b: 4, t: 'For(order.PaymentType)' },
          { a: 4, b: 2, t: 'the provider', d: true },
          { a: 2, b: 3, t: 'save payment_pending' },
          { a: 2, b: 5, t: 'Authorize' },
          { a: 5, b: 2, t: 'result or error', d: true },
          { branch: 'if approved', col: GREEN },
          { a: 2, b: 3, t: 'save paid' },
          { a: 2, b: 0, t: '200', d: true, c: Gn },
          { branch: 'if declined', col: ORANGE },
          { a: 2, b: 3, t: 'save cancelled' },
          { a: 2, b: 0, t: '402 and the reason', d: true, c: '#e8590c' },
          { branch: 'if error', col: RED },
          { a: 2, b: 0, t: '500, the order stays payment_pending', d: true, c: R },
        ],
      });
      notes(S, ['Next: a payment record and a claim (Repository plus the atomic claim), then the cached store (Decorator) and a service layer (Facade).']);
    });

  // ================= 21 LEARNING LOG =================
  slide('21 Learning log', 'Learning log: the insights worth keeping',
    'Add a row here whenever a new pattern or a hard-won lesson comes up.', S => {
      const widths = [280, 420, 860];
      const gap = 20;
      const tableRow = (y, cells, col) => {
        const h = Math.max(...cells.map((c, i) => boxH(widths[i], c, 16)));
        let x = 50;
        cells.forEach((c, i) => { S.node(x, y, widths[i], c, col, { fontSize: 16, height: h }); x += widths[i] + gap; });
        return y + h + 20;
      };
      let y = 150;
      y = tableRow(y, ['WHEN', 'TOPIC', 'THE INSIGHT WORTH KEEPING'], GRAY);
      const logRows = [
        ['earlier sessions', 'Repository, Adapter, State, Chain (validators)', 'Put storage behind an interface. State moves as data in a table. A validator chain stops at the first failure.'],
        ['2026-10-13', 'CORS and Chain of Responsibility', 'CORS must be the outermost link. mux skips middleware for unmatched OPTIONS, so wrap the whole router. CORS protects the user browser session, not the API.'],
        ['2026-10-14 to 17', 'Strategy, Adapter, Factory, dependency injection', 'Inject the chooser, not a provider: the type is only known after the order is loaded. A shared mutable Strategy context is a race on a server. The factory returns the interface, it is not the interface.'],
        ['2026-10-17', 'Decline versus error', 'A rejected request is not a declined card. Classify by HTTP status first.'],
        ['2026-10-17', 'Lease versus cache', 'A cache is a copy and its loss costs speed. A lease is a claim and its loss costs correctness. The lease lives in the database row.'],
      ];
      const cols = [BLUE, GREEN, ORANGE, RED, PURPLE];
      logRows.forEach((r, i) => { y = tableRow(y, r, cols[i % cols.length]); });
    });

  return els;
});

// Also write the plain scene as a .excalidraw file (opens in excalidraw.com or the VS Code extension).
(function exportPlain() {
  const md = fs.readFileSync(path.join(__dirname, 'out5', DECK + '.excalidraw.md'), 'utf8');
  const start = md.indexOf('```json') + 7;
  const end = md.lastIndexOf('```');
  const scene = JSON.parse(md.slice(start, end).trim());
  fs.writeFileSync(path.join(__dirname, 'out5', DECK + '.excalidraw'), JSON.stringify(scene), 'utf8');
  console.log('also wrote ' + DECK + '.excalidraw');
})();
