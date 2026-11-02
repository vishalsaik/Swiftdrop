import { useState, type FormEvent } from 'react';
import { authErrorMessage, validateSignup } from '../lib/logic';
import { useSession } from '../session';
import { Icon } from './Icon';
import { Segmented } from './ui';

type Mode = 'in' | 'up';

export function AuthScreen() {
  const { signIn, signUp, notice } = useSession();
  const [mode, setMode] = useState<Mode>('in');
  const [email, setEmail] = useState('');
  const [name, setName] = useState('');
  const [password, setPassword] = useState('');
  const [show, setShow] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problems, setProblems] = useState<Record<string, string>>({});
  const [message, setMessage] = useState('');

  const up = mode === 'up';

  function switchTo(next: Mode) {
    setMode(next);
    setProblems({});
    setMessage('');
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setProblems({});
    setMessage('');

    if (up) {
      const check = validateSignup({ email, name, password });
      if (!check.ok) { setProblems(check.errors); return; }
      setBusy(true);
      const res = await signUp(check.payload);
      setBusy(false);
      if (!res.ok) { if (res.fields) setProblems(res.fields); else setMessage(res.message ?? authErrorMessage(0, null)); }
      return;
    }
    if (!email.trim() || !password) { setMessage('Enter your email and password.'); return; }
    setBusy(true);
    const res = await signIn(email, password);
    setBusy(false);
    if (!res.ok) { if (res.fields) setProblems(res.fields); else setMessage(res.message ?? authErrorMessage(0, null)); }
  }

  const shown = message || notice;

  return (
    <main className="auth-wrap">
      <section className="auth-card" aria-labelledby="auth-title">
        <div className="brand">
          <svg className="mark" viewBox="0 0 32 32" aria-hidden="true">
            <rect width="32" height="32" rx="8" fill="currentColor" />
            <path d="M7 11h13M5 16h17M9 21h12" stroke="#fff" strokeWidth="3" strokeLinecap="round" />
            <path d="M22 9l6 7-6 7" fill="none" stroke="#fff" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </div>
        <h1 id="auth-title">{up ? 'Create your account' : 'Welcome to SwiftDrop'}</h1>
        <p className="sub">{up ? 'Order food and follow it to your door.' : 'Sign in to see your orders.'}</p>

        <Segmented<Mode> label="Sign in or create an account" value={mode} onChange={switchTo}
          options={[{ value: 'in', label: 'Sign in' }, { value: 'up', label: 'Create account' }]} />

        <form onSubmit={submit} noValidate className="auth-form">
          <div className="form-group">
            {up && (
              <label className="field">
                <span>Name</span>
                <input name="name" autoComplete="name" spellCheck={false} value={name} onChange={(e) => setName(e.target.value)}
                  aria-invalid={Boolean(problems.name)} />
              </label>
            )}
            <label className="field">
              <span>Email</span>
              <input name="email" type="email" inputMode="email" autoComplete="username" spellCheck={false} value={email}
                onChange={(e) => setEmail(e.target.value)} aria-invalid={Boolean(problems.email)} />
            </label>
            <label className="field">
              <span>Password</span>
              <span className="with-action">
                <input name="password" type={show ? 'text' : 'password'} autoComplete={up ? 'new-password' : 'current-password'}
                  spellCheck={false} value={password} onChange={(e) => setPassword(e.target.value)} aria-invalid={Boolean(problems.password)} />
                <button type="button" className="icon-btn" aria-label={show ? 'Hide password' : 'Show password'} aria-pressed={show}
                  onClick={() => setShow((s) => !s)}><Icon name={show ? 'eyeSlash' : 'eye'} size={20} /></button>
              </span>
            </label>
          </div>
          {Object.entries(problems).map(([field, text]) => <p key={field} className="form-error" data-field={field}>{text}</p>)}
          {up && !problems.password && <p className="hint">Use at least 10 characters. A few words in a row make a strong one.</p>}
          {shown && <p className="form-error" role="alert">{shown}</p>}
          <button className="btn primary wide" type="submit" disabled={busy}>{up ? 'Create account' : 'Sign in'}</button>
        </form>
        {!up && <p className="hint center">Staff accounts are made by an administrator.</p>}
      </section>
    </main>
  );
}
