import { useEffect, useRef, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { AlertTriangle, CheckCircle2, Info, X, XCircle } from 'lucide-react';
import { classNames } from '../../lib/format';
import { Button } from './Button';

const focusable =
  'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// Moves focus into the dialog on open, keeps Tab inside it, and returns focus
// to the element that opened it on close (R-32). Without this, keyboard users
// were left on the page behind the dialog.
function useDialogFocus(open: boolean) {
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!open) return;
    const node = ref.current;
    if (!node) return;
    const opener = document.activeElement as HTMLElement | null;
    const target =
      node.querySelector<HTMLElement>('[data-autofocus]') ?? node.querySelector<HTMLElement>(focusable);
    target?.focus();
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Tab') return;
      const items = Array.from(node.querySelectorAll<HTMLElement>(focusable)).filter(
        (el) => el.offsetParent !== null || el === document.activeElement,
      );
      if (items.length === 0) return;
      const firstItem = items[0];
      const lastItem = items[items.length - 1];
      if (e.shiftKey && document.activeElement === firstItem) {
        e.preventDefault();
        lastItem.focus();
      } else if (!e.shiftKey && document.activeElement === lastItem) {
        e.preventDefault();
        firstItem.focus();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
      // Only pull focus back if the opener is still on the page; a dialog
      // replaced by another dialog must not steal focus from its successor.
      if (opener?.isConnected) opener.focus();
    };
  }, [open]);
  return ref;
}

function useOverlay(open: boolean, onClose: () => void) {
  // Keep the latest callback in a ref: an inline onClose changes identity every
  // render, which used to tear down and re-run this effect (and could leave
  // body overflow hidden when two overlays overlapped).
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeRef.current();
    };
    document.addEventListener('keydown', handler);
    // Lock page scroll only while an overlay is actually open, and always
    // restore it on cleanup — the previous unconditional lock on mount left
    // body overflow "hidden" after closing, which froze page scrolling.
    const prev = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', handler);
      document.body.style.overflow = prev;
    };
  }, [open]);
}

export interface ModalProps {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: string;
  children: ReactNode;
  footer?: ReactNode;
  size?: 'sm' | 'md' | 'lg';
}

const modalWidths = { sm: 'max-w-[420px]', md: 'max-w-[560px]', lg: 'max-w-[720px]' };

export function Modal({ open, onClose, title, description, children, footer, size = 'md' }: ModalProps) {
  useOverlay(open, onClose);
  const dialogRef = useDialogFocus(open);
  if (!open) return null;
  return createPortal(
    <div className="fixed inset-0 z-[100] flex items-start justify-center overflow-y-auto p-4 sm:p-8 animate-fade-in">
      <div className="fixed inset-0 bg-[#111827]/40" onClick={onClose} aria-hidden />
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={classNames(
          'relative w-full rounded-card border border-border bg-bg shadow-[var(--shadow-overlay)] animate-fade-in my-auto',
          modalWidths[size],
        )}
      >
        <div className="flex items-start justify-between gap-4 border-b border-border px-5 py-4">
          <div>
            <h2 className="text-h3 text-text font-semibold">{title}</h2>
            {description && <p className="mt-0.5 text-[12.5px] text-muted">{description}</p>}
          </div>
          <button
            onClick={onClose}
            className="rounded-control p-1 text-muted hover:bg-surface-2 hover:text-text transition-colors"
            aria-label="Close dialog"
          >
            <X size={18} />
          </button>
        </div>
        <div className="px-5 py-4">{children}</div>
        {footer && <div className="flex items-center justify-end gap-2 border-t border-border px-5 py-3.5 bg-surface rounded-b-card">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}

export interface DrawerProps {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: string;
  children: ReactNode;
  footer?: ReactNode;
}

// Right-side drawer, 560px (PRD §4).
export function Drawer({ open, onClose, title, description, children, footer }: DrawerProps) {
  useOverlay(open, onClose);
  const dialogRef = useDialogFocus(open);
  if (!open) return null;
  return createPortal(
    <div className="fixed inset-0 z-[100] flex justify-end">
      <div className="fixed inset-0 bg-[#111827]/40 animate-fade-in" onClick={onClose} aria-hidden />
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="relative h-full w-full max-w-[560px] bg-bg border-l border-border shadow-[var(--shadow-overlay)] flex flex-col animate-slide-in"
      >
        <div className="flex items-start justify-between gap-4 border-b border-border px-5 py-4 shrink-0">
          <div className="min-w-0">
            <h2 className="text-h3 text-text font-semibold truncate">{title}</h2>
            {description && <p className="mt-0.5 text-[12.5px] text-muted">{description}</p>}
          </div>
          <button
            onClick={onClose}
            className="rounded-control p-1 text-muted hover:bg-surface-2 hover:text-text transition-colors"
            aria-label="Close drawer"
          >
            <X size={18} />
          </button>
        </div>
        <div className="flex-1 overflow-y-auto px-5 py-4">{children}</div>
        {footer && (
          <div className="flex items-center justify-end gap-2 border-t border-border px-5 py-3.5 bg-surface shrink-0">
            {footer}
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}

export type ConfirmTone = 'danger' | 'warning' | 'info' | 'success';

export interface ConfirmDialogProps {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  title: string;
  message: string;
  /** Extra context, e.g. the aliases that will be affected. */
  details?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  loading?: boolean;
  destructive?: boolean;
  tone?: ConfirmTone;
}

const confirmTones: Record<ConfirmTone, { icon: ReactNode; wrap: string }> = {
  danger: { icon: <XCircle size={18} />, wrap: 'bg-danger-soft text-danger' },
  warning: { icon: <AlertTriangle size={18} />, wrap: 'bg-warning-soft text-warning' },
  info: { icon: <Info size={18} />, wrap: 'bg-info-soft text-info' },
  success: { icon: <CheckCircle2 size={18} />, wrap: 'bg-success-soft text-success' },
};

export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  message,
  details,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  loading,
  destructive = true,
  tone,
}: ConfirmDialogProps) {
  // Deletion is destructive unless the caller says otherwise; other confirms
  // stay informative so the dialog is not red just because it asks a question.
  const resolvedTone: ConfirmTone = tone ?? (destructive ? 'danger' : 'info');
  const style = confirmTones[resolvedTone];
  return (
    <Modal
      open={open}
      onClose={loading ? () => {} : onClose}
      title={title}
      size="sm"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={loading} data-autofocus>
            {cancelLabel}
          </Button>
          <Button variant={destructive ? 'danger' : 'primary'} onClick={onConfirm} loading={loading}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="flex items-start gap-3">
        <span className={classNames('flex h-9 w-9 shrink-0 items-center justify-center rounded-full', style.wrap)}>
          {style.icon}
        </span>
        <div className="min-w-0 flex-1">
          <p className="text-[13px] leading-relaxed text-muted">{message}</p>
          {details && <div className="mt-2.5">{details}</div>}
        </div>
      </div>
    </Modal>
  );
}
