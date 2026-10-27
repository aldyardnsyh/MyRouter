import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { AlertTriangle, CheckCircle2, Info, X, XCircle } from 'lucide-react';
import { classNames } from '../../lib/format';

type ToastKind = 'success' | 'error' | 'info' | 'warning';

interface ToastOptions {
  /** Overrides the default kind label ("Success", "Something went wrong", …). */
  title?: string;
  /** Extra context shown under the main line, e.g. what was skipped and why. */
  description?: string;
  /** Auto-dismiss delay in ms. `0` keeps the toast until dismissed. */
  duration?: number;
}

interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
  title?: string;
  description?: string;
  duration: number;
}

interface ToastApi {
  success: (message: string, options?: ToastOptions) => void;
  error: (message: string, options?: ToastOptions) => void;
  info: (message: string, options?: ToastOptions) => void;
  warning: (message: string, options?: ToastOptions) => void;
  dismiss: (id: number) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

const kindStyles: Record<
  ToastKind,
  { icon: ReactNode; wrap: string; bar: string; defaultTitle: string }
> = {
  success: {
    icon: <CheckCircle2 size={17} />,
    wrap: 'bg-success-soft text-success',
    bar: 'bg-success',
    defaultTitle: 'Success',
  },
  error: {
    icon: <XCircle size={17} />,
    wrap: 'bg-danger-soft text-danger',
    bar: 'bg-danger',
    defaultTitle: 'Something went wrong',
  },
  warning: {
    icon: <AlertTriangle size={17} />,
    wrap: 'bg-warning-soft text-warning',
    bar: 'bg-warning',
    defaultTitle: 'Heads up',
  },
  info: {
    icon: <Info size={17} />,
    wrap: 'bg-info-soft text-info',
    bar: 'bg-info',
    defaultTitle: 'Info',
  },
};

const defaultDuration: Record<ToastKind, number> = {
  success: 4600,
  info: 5200,
  warning: 6200,
  error: 8000,
};

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const counter = useRef(0);
  const timers = useRef(new Map<number, number>());

  const remove = useCallback((id: number) => {
    const timer = timers.current.get(id);
    if (timer) window.clearTimeout(timer);
    timers.current.delete(id);
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  // Clear timers when the provider unmounts (HMR or route-level remount) so a
  // pending dismissal never calls setState on a dead component.
  useEffect(() => {
    const pending = timers.current;
    return () => {
      pending.forEach((timer) => window.clearTimeout(timer));
      pending.clear();
    };
  }, []);

  const push = useCallback(
    (kind: ToastKind, message: string, options?: ToastOptions) => {
      const id = ++counter.current;
      const duration = options?.duration ?? defaultDuration[kind];
      const toast: Toast = {
        id,
        kind,
        message,
        title: options?.title,
        description: options?.description,
        duration,
      };
      // Keep at most four; drop the oldest so a burst of bulk errors never
      // covers the page (and its close buttons stay reachable).
      setToasts((prev) => [...prev.slice(-3), toast]);
      if (duration > 0) {
        timers.current.set(id, window.setTimeout(() => remove(id), duration));
      }
    },
    [remove],
  );

  const api: ToastApi = {
    success: (message, options) => push('success', message, options),
    error: (message, options) => push('error', message, options),
    info: (message, options) => push('info', message, options),
    warning: (message, options) => push('warning', message, options),
    dismiss: remove,
  };

  return (
    <ToastContext.Provider value={api}>
      {children}
      {createPortal(
        <div
          className="fixed bottom-4 right-4 z-[200] flex w-[360px] max-w-[calc(100vw-2rem)] flex-col gap-2.5"
          aria-label="Notifications"
        >
          {toasts.map((t) => {
            const style = kindStyles[t.kind];
            return (
              <div
                key={t.id}
                role={t.kind === 'error' ? 'alert' : 'status'}
                aria-live={t.kind === 'error' ? 'assertive' : 'polite'}
                className="relative overflow-hidden rounded-card border border-border bg-bg shadow-[var(--shadow-overlay)] animate-fade-in"
              >
                <div className="flex items-start gap-3 px-3.5 py-3">
                  <span
                    className={classNames(
                      'mt-px flex h-8 w-8 shrink-0 items-center justify-center rounded-full',
                      style.wrap,
                    )}
                  >
                    {style.icon}
                  </span>
                  <div className="min-w-0 flex-1">
                    <p className="text-[12.5px] font-semibold leading-snug text-text">
                      {t.title ?? style.defaultTitle}
                    </p>
                    <p className="mt-0.5 text-[12.5px] leading-snug text-muted">{t.message}</p>
                    {t.description && (
                      <p className="mt-1 text-[11.5px] leading-snug text-muted/90">{t.description}</p>
                    )}
                  </div>
                  <button
                    onClick={() => remove(t.id)}
                    className="shrink-0 rounded-control p-1 text-muted transition-colors hover:bg-surface-2 hover:text-text"
                    aria-label="Dismiss notification"
                  >
                    <X size={14} />
                  </button>
                </div>
                {t.duration > 0 && (
                  <span
                    aria-hidden
                    className={classNames('toast-progress absolute inset-x-0 bottom-0 h-[2px]', style.bar)}
                    style={{ animationDuration: `${t.duration}ms` }}
                  />
                )}
              </div>
            );
          })}
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
