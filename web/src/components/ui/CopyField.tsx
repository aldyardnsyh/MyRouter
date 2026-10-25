import { useState } from 'react';
import { Check, Copy, Eye, EyeOff, KeyRound } from 'lucide-react';
import { classNames } from '../../lib/format';
import { useCopy } from './Toast';

export interface CopyFieldProps {
  value: string;
  label?: string;
  className?: string;
  href?: boolean;
}

// CopyField: mono value with an integrated copy button (PRD §4).
export function CopyField({ value, label, className, href }: CopyFieldProps) {
  const { copied, copy } = useCopy();
  return (
    <div className={classNames('flex flex-col gap-1.5', className)}>
      {label && <span className="text-small text-muted font-medium">{label}</span>}
      <div className="flex items-center justify-between gap-2 rounded-control border border-border bg-surface px-3 py-2">
        {href ? (
          <a
            href={value}
            target="_blank"
            rel="noreferrer"
            className="font-mono text-[12.5px] text-accent truncate select-all hover:underline"
          >
            {value}
          </a>
        ) : (
          <code className="font-mono text-[12.5px] text-text truncate select-all">{value}</code>
        )}
        <button
          type="button"
          onClick={() => copy(value)}
          className="flex shrink-0 items-center gap-1 rounded-[6px] border border-border bg-bg px-2 py-1 text-[11.5px] font-medium text-muted hover:bg-surface-2 hover:text-text transition-colors"
          aria-label={copied ? 'Copied' : `Copy ${label ?? 'value'}`}
        >
          {copied ? <Check size={13} className="text-success" /> : <Copy size={13} />}
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
    </div>
  );
}

export interface CodeBlockProps {
  code: string;
  language?: string;
  className?: string;
}

// CodeBlock with a header bar and copy action.
export function CodeBlock({ code, language = 'bash / terminal', className }: CodeBlockProps) {
  const { copied, copy } = useCopy();
  return (
    <div className={classNames('flex flex-col overflow-hidden rounded-control border border-border bg-surface', className)}>
      <div className="flex items-center justify-between border-b border-border bg-surface-2 px-3 py-1.5">
        <span className="font-mono text-[11px] text-muted">{language}</span>
        <button
          type="button"
          onClick={() => copy(code)}
          className="flex items-center gap-1 text-[11.5px] font-medium text-accent hover:text-accent-hover"
        >
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? 'Copied' : 'Copy snippet'}
        </button>
      </div>
      <pre className="overflow-x-auto p-3 font-mono text-[11.5px] leading-relaxed text-text">
        <code>{code}</code>
      </pre>
    </div>
  );
}

export interface ShowSecretProps {
  value: string;
  hint?: string;
  /** Optional title override, e.g. "Client API key". */
  label?: string;
  /** Renders a dismiss control when provided (one-time reveal panels). */
  onDismiss?: () => void;
}

/**
 * One-time secret reveal. Icon buttons instead of bare text links, a clear
 * "shown once" badge, and a primary Copy action so the next step is obvious.
 * The value never leaves component state.
 */
export function SecretReveal({ value, hint, label = 'API key', onDismiss }: ShowSecretProps) {
  const [visible, setVisible] = useState(true);
  const { copied, copy } = useCopy();

  return (
    <div className="overflow-hidden rounded-control border border-success/40 bg-bg">
      <div className="flex items-center justify-between gap-2 border-b border-success/25 bg-success-soft px-3 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-success/15 text-success">
            <KeyRound size={13} />
          </span>
          <span className="truncate text-[12px] font-semibold text-text">{label}</span>
          <span className="hidden shrink-0 rounded-pill border border-success/30 bg-bg px-1.5 py-0.5 text-[10px] font-medium text-success sm:inline">
            shown once
          </span>
        </div>
        {onDismiss && (
          <button
            type="button"
            onClick={onDismiss}
            className="shrink-0 rounded-[5px] px-1.5 py-0.5 text-[11px] font-medium text-muted hover:bg-bg hover:text-text"
          >
            Dismiss
          </button>
        )}
      </div>

      <div className="flex flex-col gap-2 p-3">
        <div className="flex items-center gap-1.5">
          <code className="min-w-0 flex-1 break-all rounded-[6px] border border-border bg-surface px-2.5 py-2 font-mono text-[12px] text-text select-all">
            {visible ? value : '\u2022'.repeat(Math.min(value.length, 48))}
          </code>
          <button
            type="button"
            onClick={() => setVisible((v) => !v)}
            className="flex h-9 w-9 shrink-0 items-center justify-center rounded-control border border-border bg-bg text-muted transition-colors hover:bg-surface-2 hover:text-text"
            aria-label={visible ? 'Hide key' : 'Show key'}
            title={visible ? 'Hide key' : 'Show key'}
          >
            {visible ? <EyeOff size={15} /> : <Eye size={15} />}
          </button>
          <button
            type="button"
            onClick={() => copy(value)}
            className={classNames(
              'flex h-9 shrink-0 items-center gap-1.5 rounded-control border px-3 text-[12px] font-medium transition-colors',
              copied
                ? 'border-success bg-success-soft text-success'
                : 'border-border bg-bg text-text hover:bg-surface-2',
            )}
            aria-label={copied ? 'Key copied' : 'Copy key'}
          >
            {copied ? <Check size={14} /> : <Copy size={14} />}
            {copied ? 'Copied' : 'Copy'}
          </button>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-[11px] text-muted">
            {hint ?? 'Simpan sekarang. Setelah panel ini ditutup, key tidak bisa ditampilkan lagi.'}
          </p>
          <button
            type="button"
            onClick={() => copy(value)}
            className="flex items-center gap-1.5 rounded-control bg-accent px-3 py-1.5 text-[11.5px] font-semibold text-white transition-colors hover:bg-bg hover:text-text border border-accent hover:border-border"
          >
            <Copy size={13} />
            Copy key
          </button>
        </div>
      </div>
    </div>
  );
}
