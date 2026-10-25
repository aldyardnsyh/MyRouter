import type { ReactNode } from 'react';
import { classNames } from '../../lib/format';
import type { BadgeSpec } from '../../lib/format';

type Tone = 'success' | 'warning' | 'danger' | 'info' | 'neutral' | 'accent';

const toneClasses: Record<Tone, string> = {
  success: 'bg-success-soft text-success',
  warning: 'bg-warning-soft text-warning',
  danger: 'bg-danger-soft text-danger',
  info: 'bg-info-soft text-info',
  accent: 'bg-accent-soft text-accent',
  neutral: 'bg-surface-2 text-muted',
};

const dotClasses: Record<Tone, string> = {
  success: 'bg-success',
  warning: 'bg-warning',
  danger: 'bg-danger',
  info: 'bg-info',
  accent: 'bg-accent',
  neutral: 'bg-muted',
};

export interface BadgeProps {
  tone?: Tone;
  children: ReactNode;
  dot?: boolean;
  className?: string;
}

// StatusBadge: fully rounded pill, dot + label (PRD §4).
export function Badge({ tone = 'neutral', children, dot = true, className }: BadgeProps) {
  return (
    <span
      className={classNames(
        'inline-flex items-center gap-1.5 rounded-pill px-2.5 py-[3px] text-[11.5px] font-semibold whitespace-nowrap',
        toneClasses[tone],
        className,
      )}
    >
      {dot && <span className={classNames('h-1.5 w-1.5 rounded-full shrink-0', dotClasses[tone])} />}
      {children}
    </span>
  );
}

export function StatusBadge({ spec, dot = true }: { spec: BadgeSpec; dot?: boolean }) {
  return (
    <Badge tone={spec.tone} dot={dot}>
      {spec.label}
    </Badge>
  );
}

export function Chip({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span
      className={classNames(
        'inline-flex items-center rounded-control border border-border bg-surface px-2 py-0.5 font-mono text-[11.5px] text-muted',
        className,
      )}
    >
      {children}
    </span>
  );
}
