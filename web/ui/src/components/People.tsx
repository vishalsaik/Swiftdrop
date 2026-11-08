import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { api } from '../api';
import { accountBadges, describeUserEvent, errorMessage, fieldErrors, timeAgo, validateNewAccount } from '../lib/logic';
import type { AccountEvent, User } from '../lib/types';
import { useToast } from '../toasts';
import { Icon } from './Icon';
import { Avatar, Pill, Segmented, Sheet } from './ui';

type Filter = 'all' | 'staff' | 'customer' | 'disabled';

// The people the service knows: staff and customers. Only staff see this, and the server checks.
export function PeopleView({ me }: { me: User }) {
  const [filter, setFilter] = useState<Filter>('all');
  const [search, setSearch] = useState('');
  const [accounts, setAccounts] = useState<User[] | null>(null);
  const [failed, setFailed] = useState('');
  const [selected, setSelected] = useState<User | null>(null);
  const [adding, setAdding] = useState(false);
  const latest = useRef(0);

  const load = useCallback(async () => {
    const mine = ++latest.current; // an older, slower answer must not replace a newer one
    const params = new URLSearchParams({ limit: '200' });
    if (filter === 'staff' || filter === 'customer') params.set('role', filter);
    if (filter === 'disabled') params.set('status', 'disabled');
    if (search.trim()) params.set('q', search.trim());
    const res = await api<{ accounts: User[] | null }>('GET', `/users?${params}`);
    if (mine !== latest.current) return;
    if (!res.ok) { setFailed(errorMessage(res.status, res.data)); return; }
    setFailed('');
    setAccounts(res.data?.accounts ?? []);
  }, [filter, search]);

  useEffect(() => {
    const t = setTimeout(() => void load(), search ? 250 : 0); // typing waits a moment
    return () => clearTimeout(t);
  }, [load, search]);

  // what is open shows the latest of its account after a change
  const onChanged = useCallback(async () => { await load(); }, [load]);
  const open = selected ? accounts?.find((a) => a.id === selected.id) ?? selected : null;

  return (
    <div className="groups">
      <div className="people-tools">
        <Segmented<Filter> label="Which people" value={filter} onChange={setFilter}
          options={[{ value: 'all', label: 'All' }, { value: 'staff', label: 'Staff' }, { value: 'customer', label: 'Customers' }, { value: 'disabled', label: 'Disabled' }]} />
        <input className="search" type="search" placeholder="Search by name or email" aria-label="Search people" value={search}
          onChange={(e) => setSearch(e.target.value)} />
        <button className="btn primary" type="button" onClick={() => setAdding(true)}><Icon name="plus" size={18} /> Add a person</button>
      </div>

      {failed && <div className="banner" role="alert">{failed}</div>}
      {accounts === null && !failed && <div className="empty"><p>Loading people</p></div>}
      {accounts !== null && accounts.length === 0 && (
        <div className="empty"><Icon name="person" size={44} /><h3>Nobody here</h3><p>No one matches that.</p></div>
      )}
      {accounts !== null && accounts.length > 0 && (
        <ul className="list">
          {accounts.map((u) => (
            <li key={u.id}>
              <button type="button" className="row person" aria-label={`${u.name}, ${u.email}`} onClick={() => setSelected(u)}>
                <Avatar name={u.name} />
                <span className="row-main">
                  <span className="row-title">{u.name}</span>
                  <span className="row-sub">{u.email}</span>
                </span>
                <span className="row-trail badges">
                  {accountBadges(u, me.id).map((b) => <Pill key={b.text} tone={b.tone}>{b.text}</Pill>)}
                </span>
                <span className="chev"><Icon name="chevron" size={16} /></span>
              </button>
            </li>
          ))}
        </ul>
      )}

      <PersonSheet person={open} me={me} onClose={() => setSelected(null)} onChanged={onChanged} />
      <AddPersonSheet open={adding} onClose={() => setAdding(false)} onChanged={onChanged} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// a temporary password, shown once
// ---------------------------------------------------------------------------

function TempPassword({ password, name }: { password: string; name: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(password);
      setCopied(true);
    } catch { /* no clipboard here: the password is on screen to copy by hand */ }
  }
  return (
    <section className="callout good secret-box" aria-label="Temporary password">
      <small>Temporary password, shown only now</small>
      <div className="secret"><code>{password}</code><button type="button" className="text-btn strong" onClick={() => void copy()}>{copied ? 'Copied' : 'Copy'}</button></div>
      <p>Give it to {name}. They must choose their own password the first time they sign in. It is not stored anywhere, so if it is lost, reset it again.</p>
    </section>
  );
}

// ---------------------------------------------------------------------------
// one person
// ---------------------------------------------------------------------------

function PersonSheet(props: { person: User | null; me: User; onClose(): void; onChanged(): Promise<void> }) {
  const { person } = props;
  return (
    <Sheet open={person !== null} onClose={props.onClose} title={person?.name ?? ''} className="form-sheet"
      trailing={<button type="button" className="text-btn strong" onClick={props.onClose}>Done</button>}>
      {person && <PersonBody key={person.id} person={person} me={props.me} onChanged={props.onChanged} />}
    </Sheet>
  );
}

function PersonBody({ person, me, onChanged }: { person: User; me: User; onChanged(): Promise<void> }) {
  const toast = useToast();
  const [events, setEvents] = useState<AccountEvent[] | null>(null);
  const [confirming, setConfirming] = useState<'disable' | 'reset' | null>(null);
  const [busy, setBusy] = useState(false);
  const [secret, setSecret] = useState('');
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isMe = person.id === me.id;
  const disabled = Boolean(person.disabledAt);

  const loadEvents = useCallback(async () => {
    const res = await api<{ events: AccountEvent[] | null }>('GET', `/users/${encodeURIComponent(person.id)}`);
    if (res.ok) setEvents(res.data?.events ?? []);
  }, [person.id]);
  useEffect(() => { void loadEvents(); }, [loadEvents]);
  useEffect(() => () => { if (timer.current) clearTimeout(timer.current); }, []);

  function ask(kind: 'disable' | 'reset') {
    setConfirming(kind); // two taps, with no browser dialog
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setConfirming((c) => (c === kind ? null : c)), 4000);
  }

  async function act(kind: 'disable' | 'enable' | 'reset') {
    setBusy(true);
    setConfirming(null);
    const res = await api<{ temporaryPassword?: string }>('POST', `/users/${encodeURIComponent(person.id)}/${kind === 'reset' ? 'reset-password' : kind}`);
    setBusy(false);
    if (!res.ok) { toast(errorMessage(res.status, res.data), 'bad'); return; }
    if (kind === 'reset') setSecret(res.data?.temporaryPassword ?? '');
    toast({ disable: 'Account disabled and signed out everywhere', enable: 'Account enabled', reset: 'Password reset' }[kind], 'ok');
    await Promise.all([onChanged(), loadEvents()]);
  }

  return (
    <div className="sheet-form">
      <div className="who">
        <Avatar name={person.name} size="lg" />
        <div>
          <strong>{person.name}</strong>
          <span className="sub">{person.email}</span>
          <span className="badges">{accountBadges(person, me.id).map((b) => <Pill key={b.text} tone={b.tone}>{b.text}</Pill>)}</span>
        </div>
      </div>

      {secret && <TempPassword password={secret} name={person.name} />}

      <h3 className="group-title">Actions</h3>
      {isMe ? (
        <p className="hint">This is your own account. To change your password use Account; you cannot disable yourself.</p>
      ) : (
        <div className="action-stack">
          {disabled ? (
            <button type="button" className="btn tinted wide" disabled={busy} onClick={() => void act('enable')}>Enable this account</button>
          ) : (
            <button type="button" className="btn danger wide" disabled={busy}
              onClick={() => (confirming === 'disable' ? void act('disable') : ask('disable'))}>
              {confirming === 'disable' ? 'Confirm: disable and sign out everywhere' : 'Disable this account'}
            </button>
          )}
          <button type="button" className="btn tinted wide" disabled={busy}
            onClick={() => (confirming === 'reset' ? void act('reset') : ask('reset'))}>
            {confirming === 'reset' ? 'Confirm: reset to a temporary password' : 'Reset password'}
          </button>
          <p className="hint">Disabling signs them out at once. Resetting signs them out and gives a temporary password they must replace.</p>
        </div>
      )}

      <h3 className="group-title">History</h3>
      {events === null && <p className="why">Loading</p>}
      {events !== null && events.length === 0 && <p className="why">Nothing recorded yet.</p>}
      {events !== null && events.length > 0 && (
        <ol className="list history">
          {events.map((ev) => {
            const d = describeUserEvent(ev);
            return (
              <li key={ev.id}>
                <time dateTime={ev.at}>{timeAgo(ev.at)}</time>
                <span className="what">{d.label}</span>
                <span className="note">{d.by}</span>
              </li>
            );
          })}
        </ol>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// adding a person
// ---------------------------------------------------------------------------

function AddPersonSheet({ open, onClose, onChanged }: { open: boolean; onClose(): void; onChanged(): Promise<void> }) {
  return (
    <Sheet open={open} onClose={onClose} title="Add a person" className="form-sheet"
      leading={<button type="button" className="text-btn" onClick={onClose}>Close</button>}>
      <AddPersonForm onChanged={onChanged} onClose={onClose} />
    </Sheet>
  );
}

function AddPersonForm({ onChanged, onClose }: { onChanged(): Promise<void>; onClose(): void }) {
  const toast = useToast();
  const [name, setName] = useState('');
  const [email, setEmail] = useState('');
  const [role, setRole] = useState<'staff' | 'customer'>('customer');
  const [problems, setProblems] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [made, setMade] = useState<{ user: User; password: string } | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const check = validateNewAccount({ email, name, role });
    setProblems(check.errors);
    if (!check.ok) return;
    setBusy(true);
    const res = await api<{ user: User; temporaryPassword: string }>('POST', '/users', check.payload);
    setBusy(false);
    if (res.status === 422) { setProblems(fieldErrors(res.data)); return; }
    if (res.status === 409) { setProblems({ email: errorMessage(res.status, res.data) }); return; }
    if (!res.ok || !res.data) { toast(errorMessage(res.status, res.data), 'bad'); return; }
    setMade({ user: res.data.user, password: res.data.temporaryPassword });
    await onChanged();
  }

  if (made) {
    return (
      <div className="sheet-form">
        <div className="who">
          <Avatar name={made.user.name} size="lg" />
          <div><strong>{made.user.name}</strong><span className="sub">{made.user.email}</span><span className="pill neutral role">{made.user.role}</span></div>
        </div>
        <TempPassword password={made.password} name={made.user.name} />
        <button type="button" className="btn primary wide" onClick={onClose}>Done</button>
      </div>
    );
  }

  return (
    <form onSubmit={submit} noValidate className="sheet-form">
      <div className="form-group">
        <label className="field"><span>Name</span>
          <input value={name} autoComplete="off" spellCheck={false} onChange={(e) => setName(e.target.value)} aria-invalid={Boolean(problems.name)} />
        </label>
        <label className="field"><span>Email</span>
          <input type="email" value={email} autoComplete="off" spellCheck={false} onChange={(e) => setEmail(e.target.value)} aria-invalid={Boolean(problems.email)} />
        </label>
      </div>
      <h3 className="group-title">What they can do</h3>
      <Segmented<'staff' | 'customer'> label="Role" value={role} onChange={setRole}
        options={[{ value: 'customer', label: 'Customer' }, { value: 'staff', label: 'Staff' }]} />
      <p className="hint">{role === 'staff' ? 'Staff run the kitchen and delivery, see every order, and manage people.' : 'A customer places and follows their own orders.'}</p>
      {Object.entries(problems).map(([k, t]) => <p key={k} className="form-error">{t}</p>)}
      <button className="btn primary wide" type="submit" disabled={busy}>Create account</button>
      <p className="hint">They get a temporary password, shown once, and choose their own when they first sign in.</p>
    </form>
  );
}
