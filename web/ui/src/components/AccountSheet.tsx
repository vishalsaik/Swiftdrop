import { useState, type FormEvent } from 'react';
import type { Theme } from '../theme';
import type { User } from '../lib/types';
import { useSession } from '../session';
import { useToast } from '../toasts';
import { Avatar, Segmented, Sheet } from './ui';

interface Props {
  open: boolean;
  user: User;
  theme: Theme;
  onTheme(t: Theme): void;
  onClose(): void;
}

export function AccountSheet(props: Props) {
  return (
    <Sheet open={props.open} onClose={props.onClose} title="Account" className="form-sheet"
      trailing={<button type="button" className="text-btn strong" onClick={props.onClose}>Done</button>}>
      <AccountBody {...props} />
    </Sheet>
  );
}

function AccountBody({ user, theme, onTheme, onClose }: Props) {
  const { changePassword, signOut } = useSession();
  const toast = useToast();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [problems, setProblems] = useState<Record<string, string>>({});
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const found: Record<string, string> = {};
    if (!current) found.currentPassword = 'Enter your current password';
    if ([...next].length < 10) found.newPassword = 'Use at least 10 characters';
    setProblems(found);
    setMessage('');
    if (Object.keys(found).length) return;

    setBusy(true);
    const res = await changePassword(current, next);
    setBusy(false);
    if (res.ok) {
      setCurrent(''); setNext('');
      onClose();
      toast('Password changed. Other devices were signed out.', 'ok');
    } else if (res.fields) {
      setProblems(res.fields);
    } else if (res.message) {
      setMessage(res.message);
    }
  }

  return (
    <div className="sheet-form">
      <div className="who">
        <Avatar name={user.name} size="lg" />
        <div>
          <strong>{user.name}</strong>
          <span className="sub">{user.email}</span>
          <span className="pill neutral role">{user.role}</span>
        </div>
      </div>

      <h3 className="group-title">Appearance</h3>
      <Segmented<Theme> label="Appearance" value={theme} onChange={onTheme}
        options={[{ value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }, { value: 'auto', label: 'Auto' }]} />

      <form onSubmit={submit} noValidate>
        <h3 className="group-title">Change password</h3>
        <div className="form-group">
          <label className="field"><span>Current</span>
            <input type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} aria-invalid={Boolean(problems.currentPassword)} />
          </label>
          <label className="field"><span>New</span>
            <input type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} aria-invalid={Boolean(problems.newPassword)} />
          </label>
        </div>
        {Object.entries(problems).map(([k, t]) => <p key={k} className="form-error">{t}</p>)}
        {message && <p className="form-error" role="alert">{message}</p>}
        <p className="hint">At least 10 characters. Changing it signs you out everywhere else.</p>
        <button className="btn tinted wide" type="submit" disabled={busy}>Change password</button>
      </form>

      <button className="btn danger wide spaced" type="button" onClick={() => void signOut()}>Sign out</button>
    </div>
  );
}
