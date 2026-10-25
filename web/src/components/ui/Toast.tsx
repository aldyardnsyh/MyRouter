import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { CheckCircle2, AlertTriangle, Info, XCircle, X } from 'lucide-react';
import { classNames } from '../../lib/format';

type ToastKind = 'success' | 'error' | 'info' | 'warning';

interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
}

interface ToastApi {
  success: (message: string) => void;
  error: (message: string) => void;
  info: (message: string) => void;
  warning: (message: string) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

const kindStyles: Record<ToastKind, { icon: ReactNode; ring: string }> = {
  success: { icon: <CheckCircle2 size={16} className="text-success" />, ring: 'border-success/30' },
  error: { icon: <XCircle size={16} className="text-danger" />, ring: 'border-danger/30' },
  warning: { icon: <AlertTriangle size={16} className="text-warning" />, ring: 'border-warning/30' },
  info: { icon: <Info size={16} className="text-info" />, ring: 'border-info/30' },
};

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const counter = useRef(0);

  const remove = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const push = useCallback(
    (kind: ToastKind, message: string) => {
      const id = ++counter.current;
      setToasts((prev) => [...prev.slice(-3), { id, kind, message }]);
      window.setTimeout(() => remove(id), 4200);
    },
    [remove],
  );

  const api: ToastApi = {
    success: (m) => push('success', m),
    error: (m) => push('error', m),
    info: (m) => push('info', m),
    warning: (m) => push('warning', m),
  };

  return (
    <ToastContext.Provider value={api}>
      {children}
      {createPortal(
        <div className="fixed bottom-4 right-4 z-[200] flex flex-col gap-2 w-[340px]">
          {toasts.map((t) => (
            <div
              key={t.id}
              role={t.kind === 'error' ? 'alert' : 'status'}
              aria-live={t.kind === 'error' ? 'assertive' : 'polite'}
              className={classNames(
                'flex items-start gap-2.5 rounded-card border bg-bg px-3.5 py-3 shadow-[var(--shadow-overlay)] animate-fade-in',
                kindStyles[t.kind].ring,
              )}
            >
              <span className="mt-0.5 shrink-0">{kindStyles[t.kind].icon}</span>
              <p className="flex-1 text-[12.5px] text-text leading-snug">{t.message}</p>
              <button
                onClick={() => remove(t.id)}
                className="text-muted hover:text-text transition-colors shrink-0"
                aria-label="Dismiss"
              >
                <X size={14} />
              </button>
            </div>
          ))}
        </div>,
        document.body,
      )}
    </ToastContext.Provider>
  );
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error('useToast must be used inside ToastProvider');
  return ctx;
}

// useCopy provides clipboard copy with a transient "copied" flag.
export function useCopy(timeout = 1600) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = window.setTimeout(() => setCopied(false), timeout);
    return () => window.clearTimeout(t);
  }, [copied, timeout]);
  const copy = useCallback(async (value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
    } catch {
      // Clipboard may be blocked (http origins); fall back to a hidden textarea.
      const ta = document.createElement('textarea');
      ta.value = value;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      try {
        document.execCommand('copy');
        setCopied(true);
      } finally {
        document.body.removeChild(ta);
      }
    }
  }, []);
  return { copied, copy };
}
