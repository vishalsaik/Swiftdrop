import { useEffect, useState } from 'react';
import { useDriverWork, type DriverWork } from '../hooks/driver';
import { driverStatusLine, greeting, jobStep, offerCountdown, pickupText } from '../lib/logic';
import type { DriverJob, DriverOffer, Place, User } from '../lib/types';
import { useSession } from '../session';
import type { Theme } from '../theme';
import { AccountSheet } from './AccountSheet';
import { Avatar, Waiting } from './ui';

// What a signed-in driver sees: whether they are taking deliveries, the order they are being offered, and the
// delivery they are doing. Nothing of the customer's or staff's is drawn, and the server shows a driver nothing
// more than this: the restaurant, how far it is, and for a delivery they have accepted, where it goes.

export function DriverApp({ theme, onTheme }: { theme: Theme; onTheme(t: Theme): void }) {
  const { user } = useSession() as { user: User };
  const work = useDriverWork();
  const [account, setAccount] = useState(false);
  const first = user.name.trim().split(/\s+/)[0] || 'there';

  return (
    <div className="capp driver-app">
      <a className="skip" href="#content">Skip to the content</a>
      <header className="capp-head">
        <div className="capp-brand">
          <svg className="mark" viewBox="0 0 32 32" aria-hidden="true">
            <rect width="32" height="32" rx="8" fill="currentColor" />
            <path d="M7 11h13M5 16h17M9 21h12" stroke="#fff" strokeWidth="3" strokeLinecap="round" />
            <path d="M22 9l6 7-6 7" fill="none" stroke="#fff" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          <span>SwiftDrop</span>
        </div>
        <div className="capp-tools">
          <button type="button" className="icon-btn" aria-haspopup="dialog" onClick={() => setAccount(true)} aria-label={`Your account, ${user.name}`}>
            <Avatar name={user.name} />
          </button>
        </div>
      </header>

      <main className="capp-main" id="content" tabIndex={-1}>
        {work.reachable === false && <div className="banner" role="alert">We cannot reach the server. Retrying every few seconds.</div>}
        {work.problem && <div className="banner" role="alert">{work.problem}</div>}

        <div className="page-head">
          <div>
            <h1>{`${greeting()}, ${first}`}</h1>
            <p className="sub" role="status">{work.loaded ? driverStatusLine(work.driver, work.offer, work.job) : 'Loading'}</p>
          </div>
        </div>

        {!work.loaded ? (
          <div className="empty"><Waiting label="Loading" /><p>Loading</p></div>
        ) : (
          <>
            <Availability work={work} />
            {work.offer && !work.job && <OfferCard offer={work.offer} seenAt={work.offerSeenAt} work={work} />}
            {work.job && <JobCard job={work.job} work={work} />}
            {!work.offer && !work.job && work.driver?.online && (
              <div className="empty"><Waiting label="Waiting for an order" /><p>Waiting for an order near you. Keep this page open.</p></div>
            )}
          </>
        )}
      </main>

      <AccountSheet open={account} user={user} theme={theme} onTheme={onTheme} onClose={() => setAccount(false)} />
    </div>
  );
}

function Availability({ work }: { work: DriverWork }) {
  const online = work.driver?.online ?? false;
  return (
    <section className="driver-card" aria-label="Availability">
      <button type="button" className={`btn wide ${online ? 'tinted' : 'primary'}`} disabled={work.busy}
        onClick={() => void (online ? work.goOffline() : work.goOnline())}>
        {online ? 'Go offline' : 'Go online'}
      </button>
      <p className="hint">
        {online
          ? 'Your location is sent every 30 seconds while you are online. Going offline stops it and forgets it.'
          : 'Your browser will ask where you are when you go online. It is used to offer you orders near you, and kept only while you are online.'}
      </p>
    </section>
  );
}

/** Counts down from what the server said was left when it was read, using only this browser's own clock. */
function useRemaining(offer: DriverOffer, seenAt: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 250);
    return () => clearInterval(t);
  }, []);
  return Math.max(0, offer.remainingSeconds - (now - seenAt) / 1000);
}

function OfferCard({ offer, seenAt, work }: { offer: DriverOffer; seenAt: number; work: DriverWork }) {
  const left = useRemaining(offer, seenAt);
  const distance = pickupText(offer.pickupKm);
  const timeUp = left <= 0;
  return (
    <section className="driver-card offer" aria-label="New order offer">
      <small>New order nearby</small>
      <h2>{offer.restaurant.name || 'A restaurant'}</h2>
      {distance && <p className="op-text">{distance}</p>}
      <p className={`countdown ${timeUp ? 'late' : ''}`} role="timer" aria-live="off">{offerCountdown(left)}</p>
      <div className="action-row">
        <button type="button" className="btn primary" disabled={work.busy || timeUp} onClick={() => void work.accept()}>Accept</button>
        <button type="button" className="btn tinted" disabled={work.busy} onClick={() => void work.decline()}>Decline</button>
      </div>
    </section>
  );
}

function mapLink(place: { latitude?: number; longitude?: number }): string | null {
  if (place.latitude === undefined || place.longitude === undefined) return null;
  return `https://www.google.com/maps/search/?api=1&query=${place.latitude},${place.longitude}`;
}

function PlaceLine({ label, place }: { label: string; place: Place }) {
  const href = mapLink(place);
  return (
    <div className="kv"><span>{label}</span>
      <span className="v">{place.name || 'The restaurant'}{href && <> · <a href={href} target="_blank" rel="noreferrer noopener">Open in maps</a></>}</span>
    </div>
  );
}

function JobCard({ job, work }: { job: DriverJob; work: DriverWork }) {
  const step = jobStep(job);
  const to = job.deliverTo;
  const toHref = to ? mapLink(to) : null;
  return (
    <section className="driver-card job" aria-label="Your delivery">
      <small>Your delivery</small>
      <h2>{job.restaurant.name || 'The restaurant'}</h2>
      <div className="list">
        <PlaceLine label="Collect from" place={job.restaurant} />
        <div className="kv"><span>Order</span><span className="v">{job.itemCount} {job.itemCount === 1 ? 'item' : 'items'}</span></div>
        <div className="kv address"><span>Deliver to</span>
          <span className="v">
            {to ? <>{to.line1}{to.city ? `, ${to.city}` : ''}{to.postalCode ? ` ${to.postalCode}` : ''}{to.landmark && <span className="row-sub">Landmark: {to.landmark}</span>}{toHref && <a href={toHref} target="_blank" rel="noreferrer noopener">Open in maps</a>}</> : 'No address was kept for this order.'}
          </span>
        </div>
      </div>
      {step && <p className="op-text" role="status">{step.text}</p>}
      {step?.action && (
        <button type="button" className="btn primary wide" disabled={work.busy} onClick={() => void work.step(step.action!)}>{step.label}</button>
      )}
    </section>
  );
}
