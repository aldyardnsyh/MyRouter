import { useEffect, useRef, useState } from 'react';
import {
  LayoutDashboard,
  Server,
  Boxes,
  Route as RouteIcon,
  Sparkles,
  KeyRound,
  ShieldCheck,
  Activity,
  Settings,
  Copy,
  Check,
  ChevronDown,
  LogOut,
  Moon,
  Sun,
  FlaskConical,
  Landmark,
  Menu,
  X,
} from 'lucide-react';
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { LogoWordmark } from './Logo';
import { classNames } from '../lib/format';
import { useAuth } from '../lib/auth';
import { useTheme } from '../lib/theme';
import { admin } from '../lib/api';
import { useQuery } from '../lib/useQuery';
import { ErrorBoundary } from './ErrorBoundary';
import { useCopy } from './ui/Toast';

const nav = [
  { to: '/', label: 'Dashboard', icon: LayoutDashboard, end: true },
  { to: '/providers', label: 'Providers', icon: Server },
  { to: '/models', label: 'Models', icon: Boxes },
  { to: '/routes', label: 'Routes', icon: RouteIcon },
  { to: '/skills', label: 'Skills', icon: Sparkles },
  { to: '/api-keys', label: 'API Keys', icon: KeyRound },
  { to: '/guard', label: 'Guard', icon: ShieldCheck },
  { to: '/activity', label: 'Activity', icon: Activity },
  { to: '/playground', label: 'Test Lab', icon: FlaskConical },
  { to: '/settings', label: 'Settings', icon: Settings },
];

const titles: Record<string, string> = {
  '/': 'Dashboard',
  '/providers': 'Providers',
  '/models': 'Models',
  '/routes': 'Routes',
  '/skills': 'Skills',
  '/api-keys': 'API Keys',
  '/guard': 'Guard',
  '/activity': 'Activity',
  '/playground': 'Test Lab',
  '/settings': 'Settings',
};

function EndpointChip({ baseUrl }: { baseUrl: string }) {
  const { copied, copy } = useCopy();
  return (
    <div className="hidden md:flex items-center gap-1.5 rounded-control border border-border bg-surface pl-2.5 pr-1 py-1">
      <code className="max-w-[260px] truncate font-mono text-[11.5px] text-muted select-all">{baseUrl}</code>
      <button
        type="button"
        onClick={() => copy(baseUrl)}
        className="rounded-[5px] p-1 text-muted hover:bg-surface-2 hover:text-text transition-colors"
        aria-label={copied ? 'Endpoint copied' : 'Copy endpoint'}
        title="Copy endpoint"
      >
        {copied ? <Check size={14} className="text-success" /> : <Copy size={14} />}
      </button>
    </div>
  );
}

function ThemeToggle() {
  const { resolved, setMode } = useTheme();
  const next = resolved === 'dark' ? 'light' : 'dark';
  return (
    <button
      type="button"
      onClick={() => setMode(next)}
      className="shrink-0 rounded-control p-1.5 text-muted hover:bg-surface-2 hover:text-text transition-colors"
      title={resolved === 'dark' ? 'Switch to light mode' : 'Switch to dark soft mode'}
      aria-label="Toggle theme"
    >
      {resolved === 'dark' ? <Sun size={17} /> : <Moon size={17} />}
    </button>
  );
}

function AdminMenu() {
  const { username, logout } = useAuth();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onClick);
    return () => document.removeEventListener('mousedown', onClick);
  }, [open]);

  return (
    <div className="relative flex items-center gap-1" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-1 rounded-control p-0.5 hover:bg-surface-2 transition-colors"
        aria-label="Admin menu"
        aria-expanded={open}
      >
        <span className="flex h-8 w-8 items-center justify-center rounded-full bg-accent text-[12px] font-semibold text-white">
          {(username || 'A').slice(0, 1).toUpperCase()}
        </span>
        <ChevronDown size={15} className="text-muted" />
      </button>
      {open && (
        <div className="absolute right-0 top-full z-30 mt-1 w-48 overflow-hidden rounded-control border border-border bg-bg py-1 shadow-[var(--shadow-overlay)]">
          <div className="border-b border-border px-3 py-2">
            <p className="text-[12.5px] font-medium text-text truncate">{username || 'admin'}</p>
            <p className="text-[11px] text-muted">Administrator</p>
          </div>
          <NavLink
            to="/settings"
            className="flex items-center gap-2 px-3 py-1.5 text-[12.5px] text-text hover:bg-surface-2"
            onClick={() => setOpen(false)}
          >
            <Settings size={14} className="text-muted" /> Settings
          </NavLink>
          <button
            type="button"
            onClick={() => void logout()}
            className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[12.5px] text-danger hover:bg-danger-soft"
          >
            <LogOut size={14} /> Sign out
          </button>
        </div>
      )}
    </div>
  );
}

// AppShell: 232px sidebar + 56px topbar + 1200px content (PRD §4/§5).
export function AppShell() {
  const location = useLocation();
  const { version } = useAuth();
  const title = titles[location.pathname] ?? 'MyRouter';
  // Settings → API may override the public base URL (VPS / reverse proxy);
  // otherwise fall back to the origin the dashboard is served from.
  const { data: overview } = useQuery(() => admin.overview(), { cacheKey: 'overview' });
  const baseUrl = overview?.base_url || `${window.location.origin}/v1`;
  const [mobileNav, setMobileNav] = useState(false);
  const { data: health, error: healthError } = useQuery(() => admin.health(), { cacheKey: 'health', refreshMs: 30000 });
  // Quitting via the system tray (or Ctrl+C) stops the whole process: server,
  // gateway and every background worker. An unreachable health endpoint means
  // the system is fully stopped, not merely degraded.
  const offline = healthError !== null;
  const running = !offline && (!health || health.status === 'ok');

  return (
    <div className="min-h-screen bg-bg">
      <aside className="fixed left-0 top-0 z-50 hidden h-full w-sidebar select-none flex-col justify-between border-r border-border bg-surface lg:flex">
        <div className="flex min-h-0 flex-1 flex-col">
          <div className="flex h-topbar items-center gap-2 border-b border-border px-4">
            <LogoWordmark />
          </div>
          <nav className="flex-1 space-y-0.5 overflow-y-auto px-2 py-3" aria-label="Main navigation">
            {nav.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.end}
                className={({ isActive }) =>
                  classNames(
                    'flex items-center gap-2.5 rounded-control px-3 py-2 text-[13px] transition-colors',
                    isActive
                      ? 'bg-accent-soft font-semibold text-accent'
                      : 'text-muted hover:bg-surface-2 hover:text-text',
                  )
                }
              >
                {({ isActive }) => (
                  <>
                    <item.icon size={17} strokeWidth={1.75} className={isActive ? 'text-accent' : 'text-muted'} />
                    <span>{item.label}</span>
                  </>
                )}
              </NavLink>
            ))}
          </nav>
        </div>
        <div className="flex items-center justify-between gap-2 border-t border-border bg-surface px-3 py-3">
          <div className="flex min-w-0 items-center gap-2">
            <span className={classNames('h-2 w-2 shrink-0 rounded-full', offline ? 'bg-danger' : running ? 'bg-success' : 'bg-warning')} aria-hidden />
            <div className="flex min-w-0 flex-col">
              <span className="text-[11.5px] leading-none font-medium text-text">{offline ? 'Stopped' : running ? 'Running' : 'Degraded'}</span>
              <span className="mt-1 font-mono text-[10.5px] leading-none text-muted">v{version}</span>
            </div>
          </div>
          <ThemeToggle />
        </div>
      </aside>

      <div className="lg:pl-sidebar">
        <header className="fixed left-0 right-0 top-0 z-40 flex h-topbar items-center justify-between border-b border-border bg-bg px-4 lg:left-sidebar lg:px-5">
          <div className="flex items-center gap-1.5 text-[12.5px] text-muted">
            <button
              type="button"
              onClick={() => setMobileNav(true)}
              className="mr-1 rounded-control p-1.5 text-muted hover:bg-surface-2 hover:text-text lg:hidden"
              aria-label="Open navigation"
            >
              <Menu size={18} />
            </button>
            <Landmark size={15} className="text-muted hidden sm:block" />
            <span>Console</span>
            <span className="text-border">/</span>
            <span className="font-medium text-text">{title}</span>
          </div>
          <div className="flex items-center gap-3">
            <EndpointChip baseUrl={baseUrl} />
            <AdminMenu />
          </div>
        </header>

        <main className="min-h-screen pt-topbar">
          <div className="mx-auto max-w-content p-6 lg:p-8">
            {/* Reset the boundary on navigation: a broken page never traps the
                rest of the console, and the next page gets a clean tree. */}
            <ErrorBoundary key={location.pathname} scope={title}>
              <Outlet />
            </ErrorBoundary>
          </div>
        </main>
      </div>
      {mobileNav && (
        <div className="fixed inset-0 z-[70] lg:hidden">
          <div className="absolute inset-0 bg-[#111827]/40" onClick={() => setMobileNav(false)} aria-hidden />
          <aside className="absolute left-0 top-0 flex h-full w-sidebar flex-col border-r border-border bg-surface">
            <div className="flex h-topbar items-center justify-between border-b border-border px-4">
              <LogoWordmark />
              <button
                type="button"
                onClick={() => setMobileNav(false)}
                className="rounded-control p-1.5 text-muted hover:bg-surface-2 hover:text-text"
                aria-label="Close navigation"
              >
                <X size={18} />
              </button>
            </div>
            <nav className="flex-1 space-y-0.5 overflow-y-auto px-2 py-3" aria-label="Mobile navigation">
              {nav.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={item.end}
                  onClick={() => setMobileNav(false)}
                  className={({ isActive }) =>
                    classNames(
                      'flex items-center gap-2.5 rounded-control px-3 py-2 text-[13px] transition-colors',
                      isActive ? 'bg-accent-soft font-semibold text-accent' : 'text-muted hover:bg-surface-2 hover:text-text',
                    )
                  }
                >
                  <item.icon size={17} strokeWidth={1.75} />
                  <span>{item.label}</span>
                </NavLink>
              ))}
            </nav>
            <div className="flex items-center justify-between border-t border-border px-3 py-3">
              <span className="text-[11.5px] text-muted">v{version}</span>
              <ThemeToggle />
            </div>
          </aside>
        </div>
      )}
    </div>
  );
}
