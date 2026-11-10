import { useEffect, useState, type FormEvent } from 'react';
import { api } from '../api';
import { useLocation } from '../hooks/location';
import { addressLine, candidateToDraft, errorMessage, fieldErrors, validateAddressForm, type AddressDraft } from '../lib/logic';
import type { Candidate, DeliveryAddress, SavedAddress } from '../lib/types';
import { useToast } from '../toasts';

/** Where an order is going: one of the person's saved addresses, or an address sent with the order. */
export type Destination = { saved: SavedAddress } | { address: DeliveryAddress };

export const destinationAddress = (d: Destination): DeliveryAddress => ('saved' in d ? d.saved : d.address);

interface Props {
  /** a customer keeps addresses to use again; staff placing an order for someone else do not (they are not the customer's) */
  allowSaved: boolean;
  value: Destination | null;
  onChange(next: Destination | null): void;
}

const EMPTY: AddressDraft = { label: '', line1: '', landmark: '', city: '', postalCode: '', latitude: '', longitude: '' };

// Where is the food going? A saved address, where the person is, an address found by searching, or
// one typed in by hand. The browser is asked for the position only when "Deliver to where I am" is
// pressed; the address search is made when "Search" is pressed (not as the person types: the free
// OpenStreetMap service does not allow that), and what is typed goes to the geocoding service.
export function DeliverTo({ allowSaved, value, onChange }: Props) {
  const toast = useToast();
  const where = useLocation();
  const [open, setOpen] = useState(value === null);
  const [saved, setSaved] = useState<SavedAddress[] | null>(null);
  const [query, setQuery] = useState('');
  const [candidates, setCandidates] = useState<Candidate[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [problem, setProblem] = useState('');
  const [draft, setDraft] = useState<AddressDraft | null>(null);
  const [manual, setManual] = useState(false);
  const [saveIt, setSaveIt] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  // the person's saved addresses, when they are offered
  useEffect(() => {
    if (!allowSaved) return;
    let current = true;
    void api<{ addresses: SavedAddress[] | null }>('GET', '/addresses').then((res) => {
      if (current && res.ok) setSaved(res.data?.addresses ?? []);
    });
    return () => { current = false; };
  }, [allowSaved]);

  // where the person is, named if the service can: the address form opens with it
  useEffect(() => {
    if (where.status !== 'ready' || !where.position) return;
    let current = true;
    const lat = Number(where.position.lat.toFixed(6));
    const lng = Number(where.position.lng.toFixed(6));
    void api<{ candidate: Candidate | null }>('GET', `/geocode/reverse?lat=${lat}&lng=${lng}`).then((res) => {
      if (!current) return;
      const named = res.ok ? res.data?.candidate : null;
      // the words are the service's, the point is where the person is; with no words the person types the street
      setDraft({ ...candidateToDraft(named ?? { latitude: lat, longitude: lng }), latitude: String(lat), longitude: String(lng) });
      setManual(false);
      setCandidates(null);
      setProblem(res.ok || res.status === 503 || res.status === 502 ? '' : errorMessage(res.status, res.data));
      where.clear();
    });
    return () => { current = false; };
  }, [where.status, where.position]);

  async function search(e: FormEvent) {
    e.preventDefault();
    const q = query.trim();
    if ([...q].length < 3) { setProblem('Type at least 3 characters of the address.'); return; }
    setSearching(true);
    const res = await api<{ candidates: Candidate[] | null }>('GET', `/geocode?q=${encodeURIComponent(q)}`);
    setSearching(false);
    if (!res.ok) {
      setCandidates(null);
      setProblem(res.status === 503
        ? 'Address search is not set up here. Use where you are, a saved address, or type the address in.'
        : errorMessage(res.status, res.data));
      return;
    }
    setProblem('');
    setCandidates(res.data?.candidates ?? []);
  }

  function choose(c: Candidate) {
    setDraft(candidateToDraft(c));
    setManual(false);
    setCandidates(null);
    setErrors({});
  }

  function use(next: Destination) {
    onChange(next);
    setOpen(false);
    setDraft(null);
    setCandidates(null);
    setProblem('');
  }

  async function confirm(e: FormEvent) {
    e.preventDefault();
    if (!draft) return;
    const check = validateAddressForm(draft);
    setErrors(check.errors);
    if (!check.ok || !check.payload) return;
    if (!(saveIt && allowSaved)) { use({ address: check.payload }); return; }
    setBusy(true);
    const res = await api<{ address: SavedAddress }>('POST', '/addresses', check.payload);
    setBusy(false);
    if (res.status === 422) { setErrors(fieldErrors(res.data)); return; }
    if (!res.ok || !res.data) { setProblem(errorMessage(res.status, res.data)); return; }
    setSaved((list) => [...(list ?? []), res.data!.address]);
    use({ saved: res.data.address });
    toast('Address saved', 'ok');
  }

  async function remove(a: SavedAddress) {
    const res = await api('DELETE', `/addresses/${encodeURIComponent(a.id)}`);
    if (!res.ok && res.status !== 404) { setProblem(errorMessage(res.status, res.data)); return; }
    setSaved((list) => (list ?? []).filter((x) => x.id !== a.id));
    if (value && 'saved' in value && value.saved.id === a.id) onChange(null);
  }

  const chosen = value ? destinationAddress(value) : null;
  const edit = (patch: Partial<AddressDraft>) => setDraft((d) => (d ? { ...d, ...patch } : d));

  return (
    <section className="deliver-to" aria-label="Deliver to">
      <h3 className="group-title">Deliver to</h3>

      {chosen && !open && (
        <div className="form-group chosen">
          <div className="chosen-row">
            <span className="row-main">
              <span className="row-title">{chosen.label || addressLine(chosen)}</span>
              {chosen.label && <span className="row-sub">{addressLine(chosen)}</span>}
              {chosen.landmark && <span className="row-sub">{chosen.landmark}</span>}
            </span>
            <button type="button" className="text-btn strong" onClick={() => setOpen(true)}>Change</button>
          </div>
        </div>
      )}

      {open && (
        <div className="deliver-options">
          {allowSaved && saved !== null && saved.length > 0 && (
            <ul className="form-group saved-list" aria-label="Saved addresses">
              {saved.map((a) => (
                <li key={a.id} className="pick-row">
                  <button type="button" className="saved-pick" aria-label={`Deliver to ${a.label || a.line1}`} onClick={() => use({ saved: a })}>
                    <span className="row-title">{a.label || a.line1}</span>
                    <span className="row-sub">{addressLine(a)}{a.landmark ? ` · ${a.landmark}` : ''}</span>
                  </button>
                  <button type="button" className="text-btn" aria-label={`Remove ${a.label || a.line1}`} onClick={() => void remove(a)}>Remove</button>
                </li>
              ))}
            </ul>
          )}

          <form onSubmit={search} noValidate className="address-search">
            <div className="form-group">
              <label className="field"><span>Address</span>
                <input type="search" value={query} autoComplete="off" placeholder="Street, area or landmark" aria-label="Search an address"
                  onChange={(e) => setQuery(e.target.value)} />
              </label>
            </div>
            <div className="find-tools">
              <button type="submit" className="chip" disabled={searching}>{searching ? 'Searching…' : 'Search'}</button>
              <button type="button" className="chip" disabled={where.status === 'asking'} onClick={where.ask}>
                {where.status === 'asking' ? 'Finding you…' : 'Deliver to where I am'}
              </button>
              <button type="button" className="chip" onClick={() => { setDraft({ ...EMPTY }); setManual(true); setCandidates(null); setErrors({}); }}>Type it in</button>
            </div>
          </form>
          {where.status === 'problem' && <p className="form-error" role="alert">{where.message}</p>}
          {problem && <p className="form-error" role="alert">{problem}</p>}

          {candidates !== null && candidates.length === 0 && <p className="why">No address found for that. Try fewer words, or type it in.</p>}
          {candidates !== null && candidates.length > 0 && (
            <ul className="form-group candidate-list" aria-label="Addresses found">
              {candidates.map((c, i) => (
                <li key={`${c.latitude},${c.longitude},${i}`}>
                  <button type="button" className="saved-pick" onClick={() => choose(c)}>
                    <span className="row-title">{c.line1 || c.label}</span>
                    <span className="row-sub">{c.label}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}

          {draft && (
            <form onSubmit={confirm} noValidate className="address-draft" aria-label="Confirm the address">
              <div className="form-group">
                <label className="field"><span>Street</span>
                  <input value={draft.line1} autoComplete="off" onChange={(e) => edit({ line1: e.target.value })} aria-invalid={Boolean(errors.line1)} />
                </label>
                <label className="field"><span>Landmark</span>
                  <input value={draft.landmark} autoComplete="off" placeholder="A landmark, a floor, how to find the door" onChange={(e) => edit({ landmark: e.target.value })} aria-invalid={Boolean(errors.landmark)} />
                </label>
                <label className="field"><span>City</span>
                  <input value={draft.city} autoComplete="off" onChange={(e) => edit({ city: e.target.value })} aria-invalid={Boolean(errors.city)} />
                </label>
                <label className="field"><span>Postal code</span>
                  <input value={draft.postalCode} autoComplete="off" onChange={(e) => edit({ postalCode: e.target.value })} aria-invalid={Boolean(errors.postalCode)} />
                </label>
                {manual && (
                  <>
                    <label className="field"><span>Latitude</span>
                      <input inputMode="decimal" value={draft.latitude} placeholder="12.9719" onChange={(e) => edit({ latitude: e.target.value })} aria-invalid={Boolean(errors.latitude)} />
                    </label>
                    <label className="field"><span>Longitude</span>
                      <input inputMode="decimal" value={draft.longitude} placeholder="77.6412" onChange={(e) => edit({ longitude: e.target.value })} aria-invalid={Boolean(errors.longitude)} />
                    </label>
                  </>
                )}
                {allowSaved && saveIt && (
                  <label className="field"><span>Name</span>
                    <input value={draft.label} autoComplete="off" placeholder="Home, Work" onChange={(e) => edit({ label: e.target.value })} aria-invalid={Boolean(errors.label)} />
                  </label>
                )}
              </div>
              {allowSaved && (
                <label className="check-line"><input type="checkbox" checked={saveIt} onChange={(e) => setSaveIt(e.target.checked)} /> Save this address for next time</label>
              )}
              {Object.entries(errors).map(([k, t]) => <p key={k} className="form-error">{t}</p>)}
              {!manual && draft.line1 === '' && <p className="hint">We found where you are but not the street: type it above.</p>}
              <div className="action-stack">
                <button type="submit" className="btn primary wide" disabled={busy}>Deliver here</button>
                <button type="button" className="text-btn" onClick={() => { setDraft(null); setErrors({}); }}>Cancel</button>
              </div>
            </form>
          )}

          {chosen && <button type="button" className="text-btn" onClick={() => setOpen(false)}>Keep {chosen.label || chosen.line1}</button>}
          <p className="hint attribution">Address search may use OpenStreetMap data (© OpenStreetMap contributors). What you type in the search box is sent to the address search service.</p>
        </div>
      )}
    </section>
  );
}
