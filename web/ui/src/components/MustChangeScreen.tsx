import { useState, type FormEvent } from 'react';
import { useSession } from '../session';
import { Icon } from './Icon';

// Shown instead of the app while the password is a temporary one that somebody else chose
// and knows. Nothing else works until it is replaced (the server refuses it, too).
export function MustChangeScreen() {
  const { user, changePassword, signOut } = useSession();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [show, setShow] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problems, setProblems] = useState<Record<string, string>>({});
  const [message, setMessage] = useState('');

  async function submit(e: FormEvent) {
    e.preventDefault();
    const found: Record<string, string> = {};
    if (!current) found.currentPassword = 'Enter the temporary password you were given';
    if ([...next].length < 10) found.newPassword = 'Use at least 10 characters';
    setProblems(found);
    setMessage('');
    if (Object.keys(found).length) return;

    setBusy(true);
    const res = await changePassword(current, next);
    setBusy(false);
    if (res.ok) return; // the session now says the password is its own, and the app opens
    if (res.fields) setProblems(res.fields);
    else if (res.message) setMessage(res.message);
  }

  return (
    <main className="auth-wrap">
      <section className="auth-card" aria-labelledby="change-title">
        <div className="brand">
          <svg className="mark" viewBox="0 0 32 32" aria-hidden="true">
            <rect width="32" height="32" rx="8" fill="currentColor" />
            <path d="M7 11h13M5 16h17M9 21h12" stroke="#fff" strokeWidth="3" strokeLinecap="round" />
            <path d="M22 9l6 7-6 7" fill="none" stroke="#fff" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </div>
        <h1 id="change-title">Choose your own password</h1>
        <p className="sub">{user ? `${user.name}, ` : ''}the password you signed in with is a temporary one that somebody else knows. Pick one only you know to continue.</p>

        <form onSubmit={submit} noValidate className="auth-form">
          <div className="form-group">
            <label className="field">
              <span>Temporary</span>
              <input type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)}
                aria-invalid={Boolean(problems.currentPassword)} />
            </label>
            <label className="field">
              <span>New</span>
              <span className="with-action">
                <input type={show ? 'text' : 'password'} autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)}
                  aria-invalid={Boolean(problems.newPassword)} />
                <button type="button" className="icon-btn" aria-label={show ? 'Hide password' : 'Show password'} aria-pressed={show}
                  onClick={() => setShow((s) => !s)}><Icon name={show ? 'eyeSlash' : 'eye'} size={20} /></button>
              </span>
            </label>
          </div>
          {Object.entries(problems).map(([k, t]) => <p key={k} className="form-error">{t}</p>)}
          {message && <p className="form-error" role="alert">{message}</p>}
          <p className="hint">At least 10 characters. Other devices signed in to this account are signed out.</p>
          <button className="btn primary wide" type="submit" disabled={busy}>Save and continue</button>
        </form>
        <button type="button" className="text-btn" onClick={() => void signOut()}>Sign out</button>
      </section>
    </main>
  );
}
