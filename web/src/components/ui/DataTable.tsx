import { type ReactNode } from 'react';
import { MoreHorizontal } from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { classNames } from '../../lib/format';
import { SkeletonTable } from './Layout';
import { EmptyState, ErrorState } from './Layout';

export interface Column<T> {
  key: string;
  header: string;
  align?: 'left' | 'right' | 'center';
  width?: string;
  className?: string;
  render: (row: T) => ReactNode;
}

export interface DataTableProps<T> {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  onRowClick?: (row: T) => void;
  loading?: boolean;
  error?: string | null;
  onRetry?: () => void;
  empty?: ReactNode;
  footer?: ReactNode;
  actions?: (row: T) => ReactNode;
}

const alignClass = { left: 'text-left', right: 'text-right', center: 'text-center' };

// DataTable: sticky header, 32px rows, hover state, row actions (PRD §4).
export function DataTable<T>({
  columns,
  rows,
  rowKey,
  onRowClick,
  loading,
  error,
  onRetry,
  empty,
  footer,
  actions,
}: DataTableProps<T>) {
  const totalCols = columns.length + (actions ? 1 : 0);
  // Never trust the caller: a null list from the API used to crash .map here.
  const list = rows ?? [];
  // Keep stale rows visible during a refetch instead of replacing the table
  // with a skeleton (that swap was the "blink" on every toggle/search).
  const showSkeleton = Boolean(loading) && list.length === 0;
  const showError = !showSkeleton && Boolean(error) && list.length === 0;
  return (
    <div className="overflow-hidden rounded-card border border-border bg-bg" aria-busy={loading || undefined}>
      {error && list.length > 0 && (
        <div role="alert" className="border-b border-danger/30 bg-danger-soft px-4 py-2 text-[12px] text-danger">
          {error} — showing the last loaded data.
        </div>
      )}
      <div className="overflow-x-auto">
        <table className="w-full border-collapse">
          <thead className="sticky top-0 z-10 bg-surface">
            <tr>
              {columns.map((c) => (
                <th
                  key={c.key}
                  style={{ width: c.width }}
                  className={classNames(
                    'whitespace-nowrap border-b border-border px-4 py-2.5 text-small uppercase tracking-wide text-muted',
                    alignClass[c.align ?? 'left'],
                  )}
                >
                  {c.header}
                </th>
              ))}
              {actions && (
                <th className="w-12 border-b border-border px-4 py-2.5">
                  <span className="sr-only">Actions</span>
                </th>
              )}
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {showSkeleton && (
              <tr>
                <td colSpan={totalCols} className="p-0">
                  <SkeletonTable rows={5} cols={Math.max(3, columns.length)} />
                </td>
              </tr>
            )}
            {showError && (
              <tr>
                <td colSpan={totalCols}>
                  <ErrorState message={error ?? 'Request failed'} onRetry={onRetry ?? (() => {})} />
                </td>
              </tr>
            )}
            {!showSkeleton && !showError && list.length === 0 && (
              <tr>
                <td colSpan={totalCols}>{empty ?? <EmptyState icon={<MoreHorizontal size={18} />} title="No data" description="Nothing to show yet." />}</td>
              </tr>
            )}
            {!showSkeleton &&
              !showError &&
              list.map((row) => (
                <tr
                  key={rowKey(row)}
                  onClick={onRowClick ? () => onRowClick(row) : undefined}
                  className={classNames('group transition-colors duration-[120ms]', onRowClick && 'cursor-pointer', 'hover:bg-surface-2')}
                >
                  {columns.map((c) => (
                    <td
                      key={c.key}
                      style={{ height: 32 + 16 }}
                      className={classNames('px-4 py-2 text-[13px] text-text align-middle', alignClass[c.align ?? 'left'], c.className)}
                    >
                      {c.render(row)}
                    </td>
                  ))}
                  {actions && (
                    <td className="px-2 py-2 text-right" onClick={(e) => e.stopPropagation()}>
                      {actions(row)}
                    </td>
                  )}
                </tr>
              ))}
          </tbody>
        </table>
      </div>
      {footer && <div className="border-t border-border bg-surface px-4 py-2.5">{footer}</div>}
    </div>
  );
}

export interface MenuItem {
  label: string;
  onClick: () => void;
  destructive?: boolean;
  disabled?: boolean;
}

// RowActions renders a kebab menu with the supplied items.
// The menu is portalled to <body> and positioned with `fixed` coordinates:
// rendering it inside the table would clip it against the scroll container
// (`overflow-x-auto` + rounded `overflow-hidden`), especially for the last
// rows. It flips above the trigger when there is no room below.
export function RowActions({ items, label = 'Row actions' }: { items: MenuItem[]; label?: string }) {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; left: number; width: number } | null>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  const place = useCallback(() => {
    const el = btnRef.current;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    const width = 190;
    const height = items.length * 34 + 10;
    const gap = 4;
    let top = rect.bottom + gap;
    if (top + height > window.innerHeight - 8) {
      top = Math.max(8, rect.top - height - gap);
    }
    const left = Math.min(Math.max(8, rect.right - width), Math.max(8, window.innerWidth - width - 8));
    setPos({ top, left, width });
  }, [items.length]);

  useEffect(() => {
    if (!open) {
      setPos(null);
      return;
    }
    place();
    const onPointerDown = (e: MouseEvent) => {
      const target = e.target as Node;
      if (btnRef.current?.contains(target) || menuRef.current?.contains(target)) return;
      setOpen(false);
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false);
        btnRef.current?.focus();
      }
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    window.addEventListener('resize', place);
    // Capture phase catches scrolling of the table's own overflow container.
    window.addEventListener('scroll', place, true);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
      window.removeEventListener('resize', place);
      window.removeEventListener('scroll', place, true);
    };
  }, [open, place]);

  return (
    <div className="inline-block">
      <button
        ref={btnRef}
        type="button"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="rounded-control p-1.5 text-muted transition-colors hover:bg-surface-2 hover:text-text"
      >
        <MoreHorizontal size={16} />
      </button>
      {open &&
        pos &&
        createPortal(
          <div
            ref={menuRef}
            role="menu"
            style={{ position: 'fixed', top: pos.top, left: pos.left, width: pos.width }}
            className="z-[100] max-h-[min(70vh,320px)] overflow-y-auto overscroll-contain rounded-control border border-border bg-bg py-1 shadow-[var(--shadow-overlay)]"
          >
            {items.map((item) => (
              <button
                key={item.label}
                role="menuitem"
                disabled={item.disabled}
                onClick={() => {
                  setOpen(false);
                  item.onClick();
                }}
                className={classNames(
                  'block w-full px-3 py-1.5 text-left text-[12.5px] transition-colors disabled:opacity-40',
                  item.destructive ? 'text-danger hover:bg-danger-soft' : 'text-text hover:bg-surface-2',
                )}
              >
                {item.label}
              </button>
            ))}
          </div>,
          document.body,
        )}
    </div>
  );
}

export interface PaginationProps {
  total: number;
  limit: number;
  offset: number;
  onChange: (offset: number) => void;
  label?: string;
}

export function Pagination({ total, limit, offset, onChange, label = 'items' }: PaginationProps) {
  const safeTotal = Number.isFinite(total) ? Math.max(0, total) : 0;
  const safeLimit = Number.isFinite(limit) && limit > 0 ? limit : 1;
  const safeOffset = Number.isFinite(offset) && offset > 0 ? offset : 0;
  const page = Math.floor(safeOffset / safeLimit) + 1;
  const pages = Math.max(1, Math.ceil(safeTotal / safeLimit));
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 text-[12px] text-muted">
      <span className="tabular">
        Showing <span className="text-text font-medium">{safeTotal === 0 ? 0 : safeOffset + 1}</span>–
        <span className="text-text font-medium">{Math.min(safeOffset + safeLimit, safeTotal)}</span> of{' '}
        <span className="text-text font-medium">{safeTotal}</span> {label}
      </span>
      <div className="flex items-center gap-1">
        <button
          onClick={() => onChange(Math.max(0, safeOffset - safeLimit))}
          disabled={page <= 1}
          className="rounded-control border border-border bg-bg px-2.5 py-1 text-[12px] hover:bg-surface-2 disabled:opacity-40"
        >
          Previous
        </button>
        <span className="px-2 tabular">
          {page} / {pages}
        </span>
        <button
          onClick={() => onChange(safeOffset + safeLimit)}
          disabled={page >= pages}
          className="rounded-control border border-border bg-bg px-2.5 py-1 text-[12px] hover:bg-surface-2 disabled:opacity-40"
        >
          Next
        </button>
      </div>
    </div>
  );
}

export interface TabsProps {
  tabs: { key: string; label: string; badge?: string }[];
  active: string;
  onChange: (key: string) => void;
}

// Underline-style tabs (PRD §4).
export function Tabs({ tabs, active, onChange }: TabsProps) {
  return (
    <div className="flex items-center gap-1 border-b border-border" role="tablist">
      {tabs.map((t) => {
        const isActive = t.key === active;
        return (
          <button
            key={t.key}
            role="tab"
            aria-selected={isActive}
            onClick={() => onChange(t.key)}
            className={classNames(
              'relative flex items-center gap-1.5 px-3 py-2 text-[13px] font-medium transition-colors -mb-px border-b-2',
              isActive
                ? 'border-accent text-accent'
                : 'border-transparent text-muted hover:text-text',
            )}
          >
            {t.label}
            {t.badge && (
              <span className="rounded-pill bg-surface-2 px-1.5 py-px font-mono text-[10px] text-muted">{t.badge}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}
