import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react';

export type ToastKind = 'ok' | 'bad' | '';
interface Toast { id: number; message: string; kind: ToastKind }

const Ctx = createContext<(message: string, kind?: ToastKind) => void>(() => {});

export function useToast() {
  return useContext(Ctx);
}

let nextId = 1;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);

  const push = useCallback((message: string, kind: ToastKind = '') => {
    const id = nextId++;
    setToasts((t) => [...t.slice(-3), { id, message, kind }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), kind === 'bad' ? 7000 : 4000);
  }, []);

  const value = useMemo(() => push, [push]);
  return (
    <Ctx.Provider value={value}>
      {children}
      <div className="toasts" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className={`toast ${t.kind}`} role={t.kind === 'bad' ? 'alert' : 'status'}>{t.message}</div>
        ))}
      </div>
    </Ctx.Provider>
  );
}
