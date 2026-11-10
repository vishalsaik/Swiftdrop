import { useEffect, useRef, useState } from 'react';
import { api } from '../api';
import { useLocation } from '../hooks/location';
import { errorMessage, formatDistance, money, searchQueryString } from '../lib/logic';
import type { SearchHit } from '../lib/types';
import { Pill } from './ui';

interface Props {
  /** the person chose a restaurant, or an item of it */
  onChoose(restaurantId: string, itemId?: string): void;
  /** where the order is going, once chosen: the search is then near that, and asks nothing of the browser */
  near?: { lat: number; lng: number; name: string } | null;
}

// Find food: by what it is called, by the restaurant or the cuisine, vegetarian only, and near the
// person when they let the page know where they are. The browser asks for the position only when
// "Use my location" is pressed, and the position is used for the search and kept nowhere.
export function NearbySearch({ onChoose, near = null }: Props) {
  const where = useLocation();
  const [text, setText] = useState('');
  const [veg, setVeg] = useState(false);
  const [hits, setHits] = useState<SearchHit[] | null>(null);
  const [failed, setFailed] = useState('');
  const latest = useRef(0);

  const position = near ?? (where.status === 'ready' ? where.position : null);
  const wantsSearch = text.trim() !== '' || position !== null || veg;

  useEffect(() => {
    if (!wantsSearch) { setHits(null); setFailed(''); return; }
    const mine = ++latest.current; // an older, slower answer must not replace a newer one
    const timer = setTimeout(async () => {
      const res = await api<{ results: SearchHit[] | null }>('GET', `/search${searchQueryString({ q: text, location: position, veg })}`);
      if (mine !== latest.current) return;
      if (!res.ok) { setFailed(errorMessage(res.status, res.data)); return; }
      setFailed('');
      setHits(res.data?.results ?? []);
    }, text ? 250 : 0); // typing waits a moment
    return () => clearTimeout(timer);
  }, [text, position, veg, wantsSearch]);

  return (
    <section className="find" aria-label="Find food">
      <h3 className="group-title">Find food</h3>
      <div className="form-group">
        <label className="field"><span>Search</span>
          <input type="search" value={text} placeholder="A dish, a restaurant or a cuisine" autoComplete="off"
            aria-label="Search food" onChange={(e) => setText(e.target.value)} />
        </label>
      </div>

      <div className="find-tools">
        {near ? (
          <span className="chip active" role="status">Near {near.name}</span>
        ) : position ? (
          <button type="button" className="chip active" aria-pressed="true" onClick={where.clear}>Near you · stop</button>
        ) : (
          <button type="button" className="chip" disabled={where.status === 'asking'} onClick={where.ask}>
            {where.status === 'asking' ? 'Finding you…' : 'Use my location'}
          </button>
        )}
        <label className="chip check">
          <input type="checkbox" checked={veg} onChange={(e) => setVeg(e.target.checked)} /> Vegetarian only
        </label>
      </div>
      {where.status === 'problem' && <p className="form-error" role="alert">{where.message}</p>}
      {failed && <p className="form-error" role="alert">{failed}</p>}
      {!wantsSearch && <p className="hint">Search by name, or share your location to see only what delivers to you. Your location is used for this search and is not kept.</p>}

      {hits !== null && hits.length === 0 && (
        <p className="why">{position ? 'Nothing that delivers to you matches that yet.' : 'Nothing matches that.'}</p>
      )}
      {hits !== null && hits.length > 0 && (
        <ul className="find-results" aria-label="Search results">
          {hits.map((h) => (
            <li key={h.restaurant.id} className="find-hit">
              <button type="button" className="find-restaurant" aria-label={`Order from ${h.restaurant.name}`} onClick={() => onChoose(h.restaurant.id)}>
                <span className="row-title">{h.restaurant.name}</span>
                <span className="row-sub">{[h.restaurant.cuisine, h.distanceKm !== undefined ? `${formatDistance(h.distanceKm)} away` : ''].filter(Boolean).join(' · ')}</span>
              </button>
              <ul className="find-items">
                {h.items.map((m) => (
                  <li key={m.id} className="pick-row">
                    <span className="row-main">
                      <span className="row-title">{m.name}{m.veg && <Pill tone="good">Veg</Pill>}</span>
                      {m.description && <span className="row-sub">{m.description}</span>}
                    </span>
                    <span className="price-tag">{money(m.priceCents)}</span>
                    <button type="button" className="text-btn strong" aria-label={`Add ${m.name} from ${h.restaurant.name}`} onClick={() => onChoose(h.restaurant.id, m.id)}>Add</button>
                  </li>
                ))}
              </ul>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
