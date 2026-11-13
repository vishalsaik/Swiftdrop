import { useEffect, useState } from 'react';
import { api } from '../api';
import { cardStatusText, errorMessage, formatDistance, restaurantCards } from '../lib/logic';
import type { Restaurant } from '../lib/types';
import { DeliverTo, destinationAddress, type Destination } from './DeliverTo';
import { NearbySearch } from './NearbySearch';
import { Pill, Waiting } from './ui';

interface Props {
  destination: Destination | null;
  onDestination(next: Destination | null): void;
  /** open a restaurant's page; with an item, one of it goes in the cart once the menu is there */
  onOpen(restaurantId: string, itemId?: string): void;
}

/** Where the food is going, as the point and name a search and a card use; null until it is chosen. */
export function nearOf(d: Destination | null) {
  if (!d) return null;
  const a = destinationAddress(d);
  return { lat: a.latitude, lng: a.longitude, name: a.label || a.line1 };
}

// The Food home: where the food goes, a search, and the restaurants that are open. Once an address is
// chosen each card says how far the restaurant is and whether it delivers there, by the server's own
// rule; the server checks again when the order is placed.
export function FoodHome({ destination, onDestination, onOpen }: Props) {
  const [restaurants, setRestaurants] = useState<Restaurant[] | null>(null);
  const [problem, setProblem] = useState('');

  useEffect(() => {
    let current = true;
    void api<{ restaurants: Restaurant[] | null }>('GET', '/restaurants').then((res) => {
      if (!current) return;
      if (!res.ok) { setProblem(errorMessage(res.status, res.data)); setRestaurants([]); return; }
      setProblem('');
      setRestaurants(res.data?.restaurants ?? []);
    });
    return () => { current = false; };
  }, []);

  const near = nearOf(destination);
  const cards = restaurantCards(restaurants, near);

  return (
    <div className="food-home">
      <div className="food-panel"><DeliverTo allowSaved value={destination} onChange={onDestination} /></div>
      <div className="food-panel"><NearbySearch onChoose={onOpen} near={near} /></div>

      <section aria-labelledby="restaurants-title" className="food-panel">
        <h3 className="group-title" id="restaurants-title">Restaurants</h3>
        {restaurants === null && <div className="empty small"><Waiting label="Loading restaurants" /><p>Loading restaurants</p></div>}
        {problem && <p className="form-error" role="alert">{problem}</p>}
        {restaurants !== null && !problem && cards.length === 0 && (
          <p className="why">No restaurant is open right now. Try again later.</p>
        )}
        {cards.length > 0 && (
          <ul className="rest-cards" aria-label="Restaurants">
            {cards.map((c) => {
              const r = c.restaurant;
              const status = cardStatusText(c, near?.name);
              const far = c.distanceKm !== undefined ? `${formatDistance(c.distanceKm)} away` : '';
              const tone = c.status === 'delivers' || c.status === 'open' ? 'good' : 'neutral';
              return (
                <li key={r.id}>
                  <button type="button" className={`rest-card ${tone === 'good' ? '' : 'muted'}`} onClick={() => onOpen(r.id)}
                    aria-label={[r.name, r.cuisine, far, status].filter(Boolean).join(', ')}>
                    <span className="rest-name">{r.name}</span>
                    <span className="row-sub">{[r.cuisine, far].filter(Boolean).join(' · ') || 'Restaurant'}</span>
                    <Pill tone={tone}>{status}</Pill>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
}
