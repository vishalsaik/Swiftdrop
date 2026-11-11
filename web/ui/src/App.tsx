import { AuthScreen } from './components/AuthScreen';
import { CustomerApp } from './components/CustomerApp';
import { MustChangeScreen } from './components/MustChangeScreen';
import { Shell } from './components/Shell';
import { Waiting } from './components/ui';
import { SessionProvider, useSession } from './session';
import { useTheme } from './theme';
import { ToastProvider } from './toasts';

// Exported so a test can put it inside its own session and switch accounts without signing out.
export function Gate() {
  const { status, user } = useSession();
  const [theme, setTheme] = useTheme();
  if (status === 'loading') return <div className="boot"><Waiting label="Loading" /></div>;
  if (status === 'out') return <AuthScreen />;
  if (user?.mustChangePassword) return <MustChangeScreen />;
  // One app, two shells: a customer orders food, staff run the dispatch dashboard.
  // Keyed by the user, so nothing of one account (a cart, an open order) survives into the next.
  if (user?.role === 'customer') return <CustomerApp key={user.id} theme={theme} onTheme={setTheme} />;
  return <Shell theme={theme} onTheme={setTheme} />;
}

export function App() {
  return (
    <SessionProvider>
      <ToastProvider>
        <Gate />
      </ToastProvider>
    </SessionProvider>
  );
}
