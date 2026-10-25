import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';
import { Loader2 } from 'lucide-react';
import { classNames } from '../../lib/format';

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger';
type Size = 'default' | 'compact' | 'icon';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
  icon?: ReactNode;
}

const variants: Record<Variant, string> = {
  // Primary is the default look. On hover it settles into the secondary
  // (neutral) surface, and on press it moves down a hair so the click is
  // felt. Disabled buttons keep their primary look.
  primary:
    'bg-accent text-white border border-accent ' +
    'hover:bg-bg hover:text-text hover:border-border ' +
    'active:bg-bg active:text-text active:border-border active:translate-y-px active:scale-[0.99] ' +
    'disabled:hover:bg-accent disabled:hover:text-white disabled:hover:border-accent',
  secondary:
    'bg-bg text-text border border-border hover:bg-surface-2 ' +
    'active:bg-surface-2 active:translate-y-px active:scale-[0.99] disabled:hover:bg-bg',
  ghost:
    'bg-transparent text-muted border border-transparent hover:bg-surface-2 hover:text-text ' +
    'active:bg-surface-2 active:translate-y-px',
  danger:
    'bg-danger text-white border border-danger hover:brightness-95 ' +
    'active:brightness-90 active:translate-y-px active:scale-[0.99] disabled:hover:bg-danger',
};

const sizes: Record<Size, string> = {
  default: 'h-9 px-4 text-[13px] gap-2',
  compact: 'h-8 px-3 text-[12.5px] gap-1.5',
  icon: 'h-9 w-9 justify-center',
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'default', loading, icon, className, children, disabled, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      disabled={disabled || loading}
      className={classNames(
        'inline-flex items-center justify-center rounded-control font-medium transition-all duration-[120ms] select-none',
        'disabled:cursor-not-allowed disabled:opacity-60 whitespace-nowrap',
        variants[variant],
        sizes[size],
        className,
      )}
      {...rest}
    >
      {loading ? <Loader2 size={size === 'icon' ? 16 : 15} className="animate-spin" /> : icon}
      {children}
    </button>
  );
});
