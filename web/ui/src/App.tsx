import { AuthScreen } from './components/AuthScreen';
import { Shell } from './components/Shell';
import { Waiting } from './components/ui';
import { SessionProvider, useSession } from './session';
import { useTheme } from './theme';
import { ToastProvider } from './toasts';

function Gate() {
  const { status } = useSession();
  const [theme, setTheme] = useTheme();
  if (status === 'loading') return <div className="boot"><Waiting label="Loading" /></div>;
  if (status === 'out') return <AuthScreen />;
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
