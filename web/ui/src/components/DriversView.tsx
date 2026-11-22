import { useCallback, useEffect, useState } from 'react';
import { api } from '../api';
import { usePoll } from '../hooks/data';
import { driverBadges, errorMessage, lastHeard } from '../lib/logic';
import type { Driver } from '../lib/types';
import { Avatar, Pill, Waiting } from './ui';

// Staff's list of drivers: who is online, free or busy, and when each was last heard from. It follows the
// server by polling, like the orders. Only staff see it, and the server checks.
export function DriversView() {
  const [drivers, setDrivers] = useState<Driver[] | null>(null);
  const [failed, setFailed] = useState('');
  const [now, setNow] = useState(() => Date.now());

  const load = useCallback(async () => {
    const res = await api<{ drivers: Driver[] | null }>('GET', '/drivers');
    if (!res.ok) { setFailed(errorMessage(res.status, res.data)); return; }
    setFailed('');
    setDrivers(res.data?.drivers ?? []);
    setNow(Date.now());
  }, []);
  usePoll(load, true);

  // "last seen" keeps counting between reads
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 10_000);
    return () => clearInterval(t);
  }, []);

  const online = drivers?.filter((d) => d.online).length ?? 0;
  return (
    <div className="groups">
      {failed && <div className="banner" role="alert">{failed}</div>}
      {drivers === null && !failed && <div className="empty"><Waiting label="Loading drivers" /><p>Loading drivers</p></div>}
      {drivers !== null && drivers.length === 0 && (
        <div className="empty"><h3>No drivers yet</h3><p>Add one under People, with the Driver role.</p></div>
      )}
      {drivers !== null && drivers.length > 0 && (
        <>
          <p className="sub" role="status">{online} of {drivers.length} online</p>
          <ul className="list">
            {drivers.map((d) => (
              <li key={d.userId}>
                <div className="row person driver-row" aria-label={`${d.name}, ${driverBadges(d).map((b) => b.text).join(', ')}`}>
                  <Avatar name={d.name} />
                  <span className="row-main">
                    <span className="row-title">{d.name}</span>
                    <span className="row-sub">{lastHeard(d, now) || 'Not taking deliveries'}</span>
                  </span>
                  <span className="row-trail badges">
                    {driverBadges(d).map((b) => <Pill key={b.text} tone={b.tone}>{b.text}</Pill>)}
                  </span>
                </div>
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}
