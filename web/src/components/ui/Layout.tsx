import type { ReactNode } from 'react';
import { classNames } from '../../lib/format';
import { Button } from './Button';

// PageHeader: title, one-line description, primary action on the right (PRD §4).
export interface PageHeaderProps {
  title: string;
  description?: string;
  actions?: ReactNode;
  meta?: ReactNode;
}

export function PageHeader({ title, description, actions, meta }: PageHeaderProps) {
  return (
    <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
      <div className="min-w-0">
        <div className="flex items-center gap-2.5 flex-wrap">
          <h1 className="text-display text-text">{title}</h1>
          {meta}
        </div>
        {description && <p className="mt-1 max-w-2xl text-[13px] text-muted leading-relaxed">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2 flex-wrap">{actions}</div>}
    </div>
  );
}

export interface CardProps {
  children: ReactNode;
  className?: string;
  padded?: boolean;
}

export function Card({ children, className, padded = true }: CardProps) {
  return (
    <div className={classNames('rounded-card border border-border bg-bg', padded && 'p-5', className)}>{children}</div>
  );
}

export interface CardHeaderProps {
  title: string;
  description?: string;
  actions?: ReactNode;
}

export function CardHeader({ title, description, actions }: CardHeaderProps) {
  return (
    <div className="flex items-start justify-between gap-3 pb-3">
      <div>
        <h2 className="text-h3 text-text font-semibold">{title}</h2>
        {description && <p className="mt-0.5 text-[12px] text-muted">{description}</p>}
      </div>
      {actions}
    </div>
  );
}

export interface StatCardProps {
  label: string;
  value: string;
  hint?: string;
  tone?: 'default' | 'success' | 'warning' | 'danger' | 'accent';
  icon?: ReactNode;
}

const statTone = {
  default: 'text-muted',
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
  accent: 'text-accent',
};

export function StatCard({ label, value, hint, tone = 'default', icon }: StatCardProps) {
  return (
    <div className="flex min-w-0 flex-col justify-between rounded-card border border-border bg-bg p-4">
      <div className="flex items-center justify-between gap-2">
        <span className="text-small text-muted">{label}</span>
        {icon && <span className="text-muted">{icon}</span>}
      </div>
      <div className="mt-3 flex min-w-0 flex-col gap-1">
        <span className={classNames('break-words font-bold leading-none tracking-[-0.02em] text-text tabular [overflow-wrap:anywhere]', value.length > 14 ? 'text-[16px]' : value.length > 8 ? 'text-[20px]' : 'text-[24px]')}>{value}</span>
        {hint && <span className={classNames('break-words text-[11.5px] font-medium [overflow-wrap:anywhere]', statTone[tone])}>{hint}</span>}
      </div>
    </div>
  );
}

export interface EmptyStateProps {
  icon: ReactNode;
  title: string;
  description: string;
  action?: string;
  onAction?: () => void;
  compact?: boolean;
}

// EmptyState: icon + one sentence + one CTA (PRD §4).
export function EmptyState({ icon, title, description, action, onAction, compact }: EmptyStateProps) {
  return (
    <div className={classNames('flex flex-col items-center justify-center text-center', compact ? 'py-10' : 'py-16')}>
      <div className="flex h-11 w-11 items-center justify-center rounded-card border border-border bg-surface text-muted">
        {icon}
      </div>
      <h3 className="mt-3 text-[14px] font-semibold text-text">{title}</h3>
      <p className="mt-1 max-w-sm text-[12.5px] text-muted">{description}</p>
      {action && onAction && (
        <Button variant="primary" size="compact" className="mt-4" onClick={onAction}>
          {action}
        </Button>
      )}
    </div>
  );
}

export interface ErrorStateProps {
  message: string;
  onRetry: () => void;
}

// ErrorState: message + Retry (PRD §4).
export function ErrorState({ message, onRetry }: ErrorStateProps) {
  return (
    <div className="flex flex-col items-center justify-center py-14 text-center">
      <div className="flex h-11 w-11 items-center justify-center rounded-card border border-danger/30 bg-danger-soft text-danger">
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75">
          <path d="M12 9v4M12 17h.01" strokeLinecap="round" />
          <path d="M10.3 3.6 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.6a2 2 0 0 0-3.4 0Z" strokeLinejoin="round" />
        </svg>
      </div>
      <h3 className="mt-3 text-[14px] font-semibold text-text">Couldn’t load this page</h3>
      <p className="mt-1 max-w-md text-[12.5px] text-muted">{message}</p>
      <Button variant="secondary" size="compact" className="mt-4" onClick={onRetry}>
        Retry
      </Button>
    </div>
  );
}

// Skeleton rows for loading tables.
export function SkeletonTable({ rows = 5, cols = 5 }: { rows?: number; cols?: number }) {
  return (
    <div className="divide-y divide-border" aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }).map((_, r) => (
        <div key={r} className="flex items-center gap-4 px-4 py-3.5">
          {Array.from({ length: cols }).map((_, c) => (
            <div
              key={c}
              className="h-3 rounded bg-surface-2 animate-pulse"
              style={{ width: `${[22, 16, 14, 12, 16, 10][c % 6]}%` }}
            />
          ))}
        </div>
      ))}
    </div>
  );
}

export function SkeletonCard({ className }: { className?: string }) {
  return <div className={classNames('rounded-card border border-border bg-surface animate-pulse', className)} />;
}

export interface FormSectionProps {
  title: string;
  hint?: string;
  children: ReactNode;
}

// Grouped form section used by the policy editor.
export function FormSection({ title, hint, children }: FormSectionProps) {
  return (
    <section className="border-b border-border pb-4 last:border-0 last:pb-0">
      <h3 className="text-[12.5px] font-semibold uppercase tracking-wide text-muted">{title}</h3>
      {hint && <p className="mt-0.5 text-[11.5px] text-muted">{hint}</p>}
      <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">{children}</div>
    </section>
  );
}

export function Callout({
  tone = 'info',
  children,
  icon,
}: {
  tone?: 'info' | 'warning' | 'danger' | 'neutral';
  children: ReactNode;
  icon?: ReactNode;
}) {
  const tones = {
    info: 'border-info/30 bg-info-soft text-info',
    warning: 'border-warning/30 bg-warning-soft text-warning',
    danger: 'border-danger/30 bg-danger-soft text-danger',
    neutral: 'border-border bg-surface text-muted',
  };
  return (
    <div className={classNames('flex items-start gap-2 rounded-control border px-3 py-2.5 text-[12px]', tones[tone])}>
      {icon && <span className="mt-px shrink-0">{icon}</span>}
      <div className="leading-relaxed">{children}</div>
    </div>
  );
}
