import { cloneElement, forwardRef, isValidElement, useId, type InputHTMLAttributes, type ReactElement, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react';
import { classNames } from '../../lib/format';

const baseControl =
  'w-full rounded-control border border-border bg-bg text-text text-[13px] transition-colors duration-[120ms] ' +
  'placeholder:text-muted hover:border-muted/50 focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent/20';

export interface FieldProps {
  label?: string;
  hint?: string;
  error?: string;
  required?: boolean;
  htmlFor?: string;
  children: ReactNode;
  className?: string;
}

export function Field({ label, hint, error, required, htmlFor, children, className }: FieldProps) {
  // Wire the label to the control automatically: most call sites never passed
  // htmlFor, so screen readers announced bare "edit text" with no name.
  const autoId = useId();
  const controlId = htmlFor ?? autoId;
  const errorId = `${controlId}-error`;
  const hintId = `${controlId}-hint`;
  let control = children;
  if (isValidElement(children)) {
    const existing = (children as ReactElement<{ id?: string }>).props?.id;
    control = cloneElement(children as ReactElement<Record<string, unknown>>, {
      id: existing ?? controlId,
      'aria-invalid': error ? true : undefined,
      'aria-describedby': error ? errorId : hint ? hintId : undefined,
      'aria-required': required || undefined,
    });
  }
  return (
    <div className={classNames('flex flex-col gap-1.5', className)}>
      {label && (
        <label htmlFor={controlId} className="text-small text-text font-medium">
          {label}
          {required && <span className="text-danger ml-0.5">*</span>}
        </label>
      )}
      {control}
      {error ? (
        <p id={errorId} role="alert" className="text-[11.5px] text-danger font-medium">{error}</p>
      ) : hint ? (
        <p id={hintId} className="text-[11.5px] text-muted leading-snug">{hint}</p>
      ) : null}
    </div>
  );
}

export interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'prefix'> {
  invalid?: boolean;
  mono?: boolean;
  prefix?: ReactNode;
  suffix?: ReactNode;
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { className, invalid, mono, prefix, suffix, ...rest },
  ref,
) {
  if (prefix || suffix) {
    return (
      <div
        className={classNames(
          'flex items-center gap-2 rounded-control border border-border bg-bg px-3 h-9 transition-colors',
          'focus-within:border-accent focus-within:ring-2 focus-within:ring-accent/20',
          invalid && 'border-danger',
          className,
        )}
      >
        {prefix && <span className="text-muted shrink-0">{prefix}</span>}
        <input
          ref={ref}
          className={classNames(
            'flex-1 min-w-0 bg-transparent text-[13px] text-text outline-none placeholder:text-muted',
            mono && 'font-mono',
          )}
          {...rest}
        />
        {suffix && <span className="shrink-0">{suffix}</span>}
      </div>
    );
  }
  return (
    <input
      ref={ref}
      className={classNames(baseControl, 'h-9 px-3', mono && 'font-mono text-[12.5px]', invalid && 'border-danger', className)}
      {...rest}
    />
  );
});

export interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  invalid?: boolean;
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { className, invalid, children, ...rest },
  ref,
) {
  return (
    <div className="relative">
      <select
        ref={ref}
        className={classNames(
          baseControl,
          'h-9 pl-3 pr-8 appearance-none cursor-pointer',
          invalid && 'border-danger',
          className,
        )}
        {...rest}
      >
        {children}
      </select>
      <svg
        className="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 text-muted"
        width="14"
        height="14"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
      >
        <path d="m6 9 6 6 6-6" />
      </svg>
    </div>
  );
});

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement>>(
  function Textarea({ className, ...rest }, ref) {
    return (
      <textarea
        ref={ref}
        className={classNames(baseControl, 'px-3 py-2 min-h-[80px] resize-y', className)}
        {...rest}
      />
    );
  },
);

export interface SwitchProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  label?: string;
  size?: 'sm' | 'md';
}

export function Switch({ checked, onChange, disabled, label, size = 'md' }: SwitchProps) {
  const dims = size === 'sm' ? { w: 32, h: 18, knob: 14 } : { w: 36, h: 20, knob: 16 };
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={classNames(
        'relative inline-flex shrink-0 items-center rounded-pill transition-colors duration-[120ms]',
        'disabled:cursor-not-allowed disabled:opacity-50',
        checked ? 'bg-accent' : 'bg-border',
      )}
      style={{ width: dims.w, height: dims.h }}
    >
      <span
        className="absolute rounded-full bg-white shadow-sm transition-transform duration-[120ms]"
        style={{
          width: dims.knob,
          height: dims.knob,
          transform: `translateX(${checked ? dims.w - dims.knob - 2 : 2}px)`,
        }}
      />
    </button>
  );
}

export interface CheckboxProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: string;
  description?: string;
  disabled?: boolean;
}

export function Checkbox({ checked, onChange, label, description, disabled }: CheckboxProps) {
  return (
    <label className="flex items-start gap-2.5 cursor-pointer select-none">
      <span
        className={classNames(
          'mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-[4px] border transition-colors',
          checked ? 'bg-accent border-accent text-white' : 'bg-bg border-border',
          disabled && 'opacity-50',
        )}
      >
        {checked && (
          <svg width="10" height="10" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="2.2">
            <path d="m2 6 3 3 5-6" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        )}
      </span>
      <input
        type="checkbox"
        className="sr-only"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="flex flex-col">
        <span className="text-[13px] text-text leading-tight">{label}</span>
        {description && <span className="text-[11.5px] text-muted mt-0.5">{description}</span>}
      </span>
    </label>
  );
}
