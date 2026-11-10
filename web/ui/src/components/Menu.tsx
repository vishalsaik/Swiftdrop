import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { useLocation } from '../hooks/location';
import { api } from '../api';
import { errorMessage, fieldErrors, money, validateMenuItemForm, validatePlaceForm, validateRestaurantName } from '../lib/logic';
import type { MenuItem, Restaurant } from '../lib/types';
import { useToast } from '../toasts';
import { Icon } from './Icon';
import { Pill, Sheet } from './ui';

// Restaurants and what they sell. Only staff see this, and the server checks. A customer orders
// these items by id and the server prices the order from here, so these are the only prices.

interface Editing { restaurantId: string; item: MenuItem | null }

export function MenuView() {
  const [restaurants, setRestaurants] = useState<Restaurant[] | null>(null);
  const [failed, setFailed] = useState('');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<Editing | null>(null);
  const [menuVersion, setMenuVersion] = useState(0); // bumped after an item changes, so the open menu reloads

  const load = useCallback(async () => {
    const res = await api<{ restaurants: Restaurant[] | null }>('GET', '/restaurants?all=true');
    if (!res.ok) { setFailed(errorMessage(res.status, res.data)); return; }
    setFailed('');
    setRestaurants(res.data?.restaurants ?? []);
  }, []);
  useEffect(() => { void load(); }, [load]);

  const selected = restaurants?.find((r) => r.id === selectedId) ?? null;

  return (
    <div className="groups">
      <div className="people-tools">
        <p className="sub grow">Customers order from these menus. Prices are set here and nowhere else.</p>
        <button className="btn primary" type="button" onClick={() => setAdding(true)}><Icon name="plus" size={18} /> Add a restaurant</button>
      </div>

      {failed && <div className="banner" role="alert">{failed}</div>}
      {restaurants === null && !failed && <div className="empty"><p>Loading restaurants</p></div>}
      {restaurants !== null && restaurants.length === 0 && (
        <div className="empty"><Icon name="tray" size={44} /><h3>No restaurants yet</h3><p>Add one, then put some items on its menu so customers can order.</p></div>
      )}
      {restaurants !== null && restaurants.length > 0 && (
        <ul className="list">
          {restaurants.map((r) => (
            <li key={r.id}>
              <button type="button" className="row" aria-label={`${r.name}, ${r.active ? 'open' : 'closed'}`} onClick={() => setSelectedId(r.id)}>
                <span className="tile orange"><Icon name="flame" size={20} /></span>
                <span className="row-main"><span className="row-title">{r.name}</span></span>
                <span className="row-trail"><Pill tone={r.active ? 'good' : 'neutral'}>{r.active ? 'Open' : 'Closed'}</Pill></span>
                <span className="chev"><Icon name="chevron" size={16} /></span>
              </button>
            </li>
          ))}
        </ul>
      )}

      <Sheet open={selected !== null} onClose={() => setSelectedId(null)} title={selected?.name ?? ''} className="form-sheet"
        trailing={<button type="button" className="text-btn strong" onClick={() => setSelectedId(null)}>Done</button>}>
        {selected && (
          <RestaurantBody key={selected.id} restaurant={selected} version={menuVersion} onChanged={load}
            onEdit={(item) => setEditing({ restaurantId: selected.id, item })} onItemsChanged={() => setMenuVersion((v) => v + 1)} />
        )}
      </Sheet>

      <Sheet open={adding} onClose={() => setAdding(false)} title="Add a restaurant" className="form-sheet"
        leading={<button type="button" className="text-btn" onClick={() => setAdding(false)}>Close</button>}>
        <AddRestaurantForm onClose={() => setAdding(false)} onAdded={async (r) => { await load(); setSelectedId(r.id); }} />
      </Sheet>

      <Sheet open={editing !== null} onClose={() => setEditing(null)} title={editing?.item ? 'Edit item' : 'Add an item'} className="form-sheet"
        leading={<button type="button" className="text-btn" onClick={() => setEditing(null)}>Close</button>}>
        {editing && (
          <ItemForm key={editing.item?.id ?? 'new'} restaurantId={editing.restaurantId} item={editing.item}
            onClose={() => setEditing(null)} onSaved={() => setMenuVersion((v) => v + 1)} />
        )}
      </Sheet>
    </div>
  );
}

// ---------------------------------------------------------------------------
// adding a restaurant
// ---------------------------------------------------------------------------

function AddRestaurantForm({ onClose, onAdded }: { onClose(): void; onAdded(r: Restaurant): Promise<void> }) {
  const toast = useToast();
  const [name, setName] = useState('');
  const [problem, setProblem] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const check = validateRestaurantName(name);
    setProblem(check.error);
    if (!check.ok) return;
    setBusy(true);
    const res = await api<{ restaurant: Restaurant }>('POST', '/restaurants', { name: check.name });
    setBusy(false);
    if (res.status === 422) { setProblem(fieldErrors(res.data).name ?? errorMessage(res.status, res.data)); return; }
    if (!res.ok || !res.data) { toast(errorMessage(res.status, res.data), 'bad'); return; }
    onClose();
    toast(`${res.data.restaurant.name} added`, 'ok');
    await onAdded(res.data.restaurant);
  }

  return (
    <form onSubmit={submit} noValidate className="sheet-form">
      <div className="form-group">
        <label className="field"><span>Name</span>
          <input value={name} autoComplete="off" spellCheck={false} onChange={(e) => setName(e.target.value)} aria-invalid={Boolean(problem)} />
        </label>
      </div>
      {problem && <p className="form-error">{problem}</p>}
      <button className="btn primary wide" type="submit" disabled={busy}>Add restaurant</button>
      <p className="hint">It opens straight away. Add items to its menu next.</p>
    </form>
  );
}

// ---------------------------------------------------------------------------
// one restaurant and its menu
// ---------------------------------------------------------------------------

function RestaurantBody(props: {
  restaurant: Restaurant; version: number; onChanged(): Promise<void>; onEdit(item: MenuItem | null): void; onItemsChanged(): void;
}) {
  const { restaurant } = props;
  const toast = useToast();
  const [items, setItems] = useState<MenuItem[] | null>(null);
  const [failed, setFailed] = useState('');
  const [name, setName] = useState(restaurant.name);
  const [nameProblem, setNameProblem] = useState('');
  const [busy, setBusy] = useState(false);
  const latest = useRef(0);

  const loadMenu = useCallback(async () => {
    const mine = ++latest.current;
    const res = await api<{ items: MenuItem[] | null }>('GET', `/restaurants/${encodeURIComponent(restaurant.id)}/menu?all=true`);
    if (mine !== latest.current) return;
    if (!res.ok) { setFailed(errorMessage(res.status, res.data)); return; }
    setFailed('');
    setItems(res.data?.items ?? []);
  }, [restaurant.id]);
  useEffect(() => { void loadMenu(); }, [loadMenu, props.version]);

  async function patchRestaurant(body: { name?: string; active?: boolean }, done: string) {
    setBusy(true);
    const res = await api('PATCH', `/restaurants/${encodeURIComponent(restaurant.id)}`, body);
    setBusy(false);
    if (res.status === 422) { setNameProblem(fieldErrors(res.data).name ?? errorMessage(res.status, res.data)); return; }
    if (!res.ok) { toast(errorMessage(res.status, res.data), 'bad'); return; }
    toast(done, 'ok');
    await props.onChanged();
  }

  function rename(e: FormEvent) {
    e.preventDefault();
    const check = validateRestaurantName(name);
    setNameProblem(check.error);
    if (!check.ok) return;
    if (check.name === restaurant.name) return;
    void patchRestaurant({ name: check.name }, 'Renamed');
  }

  async function toggle(item: MenuItem) {
    const res = await api('PATCH', `/menu/${encodeURIComponent(item.id)}`, { available: !item.available });
    if (!res.ok) { toast(errorMessage(res.status, res.data), 'bad'); return; }
    toast(item.available ? `${item.name} is sold out` : `${item.name} is back on the menu`, 'ok');
    props.onItemsChanged();
  }

  return (
    <div className="sheet-form">
      <div className="who">
        <div>
          <strong>{restaurant.name}</strong>
          <span className="badges"><Pill tone={restaurant.active ? 'good' : 'neutral'}>{restaurant.active ? 'Open' : 'Closed'}</Pill></span>
        </div>
      </div>

      <form onSubmit={rename} noValidate>
        <div className="form-group">
          <label className="field"><span>Name</span>
            <input value={name} autoComplete="off" spellCheck={false} onChange={(e) => setName(e.target.value)} aria-invalid={Boolean(nameProblem)} />
          </label>
        </div>
        {nameProblem && <p className="form-error">{nameProblem}</p>}
        <div className="action-stack">
          <button type="submit" className="btn tinted wide" disabled={busy || name.trim() === restaurant.name}>Save name</button>
          <button type="button" className={`btn wide ${restaurant.active ? 'danger' : 'tinted'}`} disabled={busy}
            onClick={() => void patchRestaurant({ active: !restaurant.active }, restaurant.active ? 'Closed to new orders' : 'Open for orders')}>
            {restaurant.active ? 'Close this restaurant' : 'Open this restaurant'}
          </button>
          <p className="hint">{restaurant.active ? 'Closing hides it from customers and refuses new orders. Orders already placed carry on.' : 'It is closed: customers cannot see it or order from it.'}</p>
        </div>
      </form>

      <h3 className="group-title">Place</h3>
      <PlaceForm key={`${restaurant.id}:${restaurant.latitude}:${restaurant.longitude}:${restaurant.deliveryRadiusKm}:${restaurant.cuisine}`} restaurant={restaurant} onSaved={props.onChanged} />

      <h3 className="group-title">Menu</h3>
      {failed && <div className="banner" role="alert">{failed}</div>}
      {items === null && !failed && <p className="why">Loading</p>}
      {items !== null && items.length === 0 && <p className="why">Nothing on the menu yet.</p>}
      {items !== null && items.length > 0 && (
        <ul className="list menu-list">
          {items.map((m) => (
            <li key={m.id} className={m.available ? '' : 'sold-out'}>
              <div className="menu-row">
                <span className="row-main">
                  <span className="row-title">{m.name}</span>
                  {m.description && <span className="row-sub">{m.description}</span>}
                </span>
                <span className="price-tag">{money(m.priceCents)}</span>
                {m.veg && <Pill tone="good">Veg</Pill>}
                {!m.available && <Pill tone="wait">Sold out</Pill>}
              </div>
              <div className="menu-actions">
                <button type="button" className="text-btn" aria-label={`Edit ${m.name}`} onClick={() => props.onEdit(m)}>Edit</button>
                <button type="button" className="text-btn" aria-label={`${m.available ? 'Mark sold out' : 'Put back on the menu'}: ${m.name}`} onClick={() => void toggle(m)}>
                  {m.available ? 'Mark sold out' : 'Put back'}
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
      <button type="button" className="btn primary wide" onClick={() => props.onEdit(null)}><Icon name="plus" size={18} /> Add an item</button>
      <p className="hint">A new price applies to orders placed from then on. Orders already placed keep the price they were placed at.</p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// adding or editing an item
// ---------------------------------------------------------------------------

function ItemForm({ restaurantId, item, onClose, onSaved }: { restaurantId: string; item: MenuItem | null; onClose(): void; onSaved(): void }) {
  const toast = useToast();
  const [name, setName] = useState(item?.name ?? '');
  const [description, setDescription] = useState(item?.description ?? '');
  const [price, setPrice] = useState(item ? (item.priceCents / 100).toFixed(2) : '');
  const [veg, setVeg] = useState(item?.veg ?? false);
  const [problems, setProblems] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const check = validateMenuItemForm({ name, description, price, veg });
    setProblems(check.errors);
    if (!check.ok || !check.payload) return;
    setBusy(true);
    const res = item
      ? await api('PATCH', `/menu/${encodeURIComponent(item.id)}`, check.payload)
      : await api('POST', `/restaurants/${encodeURIComponent(restaurantId)}/menu`, check.payload);
    setBusy(false);
    if (res.status === 422) { setProblems(fieldErrors(res.data)); return; }
    if (!res.ok) { toast(errorMessage(res.status, res.data), 'bad'); return; }
    toast(item ? `${check.payload.name} updated` : `${check.payload.name} added`, 'ok');
    onSaved();
    onClose();
  }

  return (
    <form onSubmit={submit} noValidate className="sheet-form">
      <div className="form-group">
        <label className="field"><span>Name</span>
          <input value={name} autoComplete="off" spellCheck={false} onChange={(e) => setName(e.target.value)} aria-invalid={Boolean(problems.name)} />
        </label>
        <label className="field"><span>Description</span>
          <input value={description} autoComplete="off" placeholder="Optional" onChange={(e) => setDescription(e.target.value)} aria-invalid={Boolean(problems.description)} />
        </label>
        <label className="field"><span>Price</span>
          <input inputMode="decimal" placeholder="0.00" value={price} onChange={(e) => setPrice(e.target.value)} aria-invalid={Boolean(problems.priceCents)} />
        </label>
        <label className="field check-field"><span>Vegetarian</span>
          <input type="checkbox" checked={veg} onChange={(e) => setVeg(e.target.checked)} />
        </label>
      </div>
      {Object.entries(problems).map(([k, t]) => <p key={k} className="form-error">{t}</p>)}
      <button className="btn primary wide" type="submit" disabled={busy}>{item ? 'Save changes' : 'Add to the menu'}</button>
      <p className="hint">Prices are in dollars, for example 9.50.</p>
    </form>
  );
}

// ---------------------------------------------------------------------------
// where a restaurant is, so customers near it can find it
// ---------------------------------------------------------------------------

function PlaceForm({ restaurant, onSaved }: { restaurant: Restaurant; onSaved(): Promise<void> }) {
  const toast = useToast();
  const where = useLocation();
  const [cuisine, setCuisine] = useState(restaurant.cuisine ?? '');
  const [latitude, setLatitude] = useState(restaurant.latitude === undefined ? '' : String(restaurant.latitude));
  const [longitude, setLongitude] = useState(restaurant.longitude === undefined ? '' : String(restaurant.longitude));
  const [radiusKm, setRadiusKm] = useState(restaurant.deliveryRadiusKm === undefined ? '' : String(restaurant.deliveryRadiusKm));
  const [problems, setProblems] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  // staff standing in the restaurant can use where they are
  useEffect(() => {
    if (where.status === 'ready' && where.position) {
      setLatitude(where.position.lat.toFixed(5));
      setLongitude(where.position.lng.toFixed(5));
    }
  }, [where.status, where.position]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const check = validatePlaceForm({ cuisine, latitude, longitude, radiusKm });
    setProblems(check.errors);
    if (!check.ok || !check.payload) return;
    setBusy(true);
    const res = await api('PUT', `/restaurants/${encodeURIComponent(restaurant.id)}/place`, check.payload);
    setBusy(false);
    if (res.status === 422) { setProblems(fieldErrors(res.data)); return; }
    if (!res.ok) { toast(errorMessage(res.status, res.data), 'bad'); return; }
    toast('Place saved', 'ok');
    await onSaved();
  }

  const placed = restaurant.latitude !== undefined && restaurant.longitude !== undefined;
  return (
    <form onSubmit={submit} noValidate>
      <div className="form-group">
        <label className="field"><span>Cuisine</span>
          <input value={cuisine} autoComplete="off" placeholder="For example North Indian" onChange={(e) => setCuisine(e.target.value)} aria-invalid={Boolean(problems.cuisine)} />
        </label>
        <label className="field"><span>Latitude</span>
          <input inputMode="decimal" value={latitude} placeholder="12.9719" onChange={(e) => setLatitude(e.target.value)} aria-invalid={Boolean(problems.latitude)} />
        </label>
        <label className="field"><span>Longitude</span>
          <input inputMode="decimal" value={longitude} placeholder="77.6412" onChange={(e) => setLongitude(e.target.value)} aria-invalid={Boolean(problems.longitude)} />
        </label>
        <label className="field"><span>Delivers up to (km)</span>
          <input inputMode="decimal" value={radiusKm} placeholder="5" onChange={(e) => setRadiusKm(e.target.value)} aria-invalid={Boolean(problems.deliveryRadiusKm)} />
        </label>
      </div>
      {Object.entries(problems).map(([k, t]) => <p key={k} className="form-error">{t}</p>)}
      {where.status === 'problem' && <p className="form-error" role="alert">{where.message}</p>}
      <div className="action-stack">
        <button type="button" className="btn tinted wide" disabled={where.status === 'asking'} onClick={where.ask}>
          {where.status === 'asking' ? 'Finding you…' : 'Use my current location'}
        </button>
        <button type="submit" className="btn primary wide" disabled={busy}>Save place</button>
        <p className="hint">{placed ? 'Customers within the delivery radius of this point can find this restaurant.' : 'Not placed yet: customers who share their location will not find this restaurant until you save where it is.'}</p>
      </div>
    </form>
  );
}
