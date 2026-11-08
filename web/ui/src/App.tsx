import { AuthScreen } from './components/AuthScreen';
import { MustChangeScreen } from './components/MustChangeScreen';
import { Shell } from './components/Shell';
import { Waiting } from './components/ui';
import { SessionProvider, useSession } from './session';
import { useTheme } from './theme';
import { ToastProvider } from './toasts';

function Gate() {
  const { status, user } = useSession();
  const [theme, setTheme] = useTheme();
  if (status === 'loading') return <div className="boot"><Waiting label="Loading" /></div>;
  if (status === 'out') return <AuthScreen />;
  if (user?.mustChangePassword) return <MustChangeScreen />;
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
