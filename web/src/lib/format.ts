import type { ActivityEntry, Policy, Route, APIKey, Status } from './types';

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

export function formatNumber(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '0';
  return new Intl.NumberFormat('en-US').format(n);
}

export function formatCompact(n: number | null | undefined): string {
  if (!n) return '0';
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, '')}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1).replace(/\.0$/, '')}K`;
  return String(n);
}

export function formatCost(n: number | null | undefined): string {
  if (n === null || n === undefined) return 'Cost unavailable';
  if (n === 0) return '$0.00';
  if (n < 0.01) return `$${n.toFixed(4)}`;
  return `$${n.toFixed(2)}`;
}

export function formatPrice(n: number | null | undefined): string | null {
  if (n === null || n === undefined) return null;
  return `$${n.toFixed(2)}`;
}

export function formatLatency(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return '—';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}

export function formatContext(tokens: number | null | undefined): string {
  if (!tokens) return '—';
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(1).replace(/\.0$/, '')}M`;
  if (tokens >= 1000) return `${Math.round(tokens / 1000)}K`;
  return String(tokens);
}

export function formatTime(ts: number | null | undefined): string {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  return d.toLocaleTimeString('en-GB', { hour12: false });
}

export function formatDateTime(ts: number | null | undefined): string {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  return d.toLocaleString('en-GB', {
    day: '2-digit',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}

export function formatDate(ts: number | null | undefined): string {
  if (!ts) return '—';
  return new Date(ts * 1000).toLocaleDateString('en-GB', { day: '2-digit', month: 'short', year: 'numeric' });
}

export function relativeTime(ts: number | null | undefined): string {
  if (!ts) return 'Never';
  const diff = Math.floor(Date.now() / 1000) - ts;
  if (diff < 0) return 'just now';
  if (diff < 60) return `${diff}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)}d ago`;
  return formatDate(ts);
}

export function countdown(until: number | null): string {
  if (!until) return 'Permanent';
  const diff = until - Math.floor(Date.now() / 1000);
  if (diff <= 0) return 'Expired';
  const m = Math.floor(diff / 60);
  const s = diff % 60;
  if (m >= 60) {
    const h = Math.floor(m / 60);
    return `${h}h ${m % 60}m`;
  }
  return `${m}m ${String(s).padStart(2, '0')}s`;
}

// ---------------------------------------------------------------------------
// Status labels (PRD §4 StatusBadge)
// ---------------------------------------------------------------------------

type BadgeTone = 'success' | 'warning' | 'danger' | 'info' | 'neutral' | 'accent';

export interface BadgeSpec {
  label: string;
  tone: BadgeTone;
}

export const statusSpec: Record<Status, BadgeSpec> = {
  connected: { label: 'Connected', tone: 'success' },
  degraded: { label: 'Degraded', tone: 'warning' },
  rate_limited: { label: 'Rate Limited', tone: 'warning' },
  unavailable: { label: 'Unavailable', tone: 'danger' },
  invalid_credentials: { label: 'Invalid Credentials', tone: 'danger' },
  unknown: { label: 'Unknown', tone: 'neutral' },
};

export function activityBadge(entry: ActivityEntry): BadgeSpec {
  if (entry.status === 'blocked') {
    return { label: entry.guard_reason ? 'Blocked by Guard' : 'Blocked', tone: 'danger' };
  }
  if (entry.status === 'error') {
    return { label: entry.http_status === 429 ? 'Rate Limited 429' : `Error ${entry.http_status}`, tone: 'danger' };
  }
  if (entry.fallback_from) {
    return { label: 'Fallback', tone: 'info' };
  }
  return { label: 'Connected 200 OK', tone: 'success' };
}

export function providerTone(status: Status): BadgeTone {
  return statusSpec[status]?.tone ?? 'neutral';
}

// ---------------------------------------------------------------------------
// Domain helpers
// ---------------------------------------------------------------------------

export const providerTypeLabel: Record<string, string> = {
  openai: 'OpenAI Native',
  anthropic: 'Anthropic Native',
  gemini: 'Gemini Native',
  deepseek: 'OpenAI-compatible',
  'openai-compatible': 'OpenAI-compatible',
};

export const providerInitial = (name: string): string => (name.trim()[0] ?? '?').toUpperCase();

export const providerHue = (name: string): string => {
  // Deterministic pastel tone per provider name (no ad-hoc colors elsewhere).
  const tones = [
    'bg-accent-soft text-accent',
    'bg-success-soft text-success',
    'bg-info-soft text-info',
    'bg-warning-soft text-warning',
    'bg-surface-2 text-muted',
  ];
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return tones[h % tones.length];
};

export const capabilities = (csv: string | null | undefined): string[] =>
  (csv ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);

export const splitCSV = (csv: string | null | undefined): string[] =>
  (csv ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);

export const policySummary = (p: Policy): string => {
  const parts: string[] = [];
  if (p.rpm) parts.push(`${p.rpm} RPM`);
  if (p.tpm) parts.push(`${formatCompact(p.tpm)} TPM`);
  if (p.daily_tokens) parts.push(`${formatCompact(p.daily_tokens)} tok/day`);
  return parts.length ? parts.join(' · ') : 'Unlimited';
};

export const isUnlimited = (p: Policy): boolean =>
  !p.rpm && !p.tpm && !p.daily_tokens && !p.monthly_tokens && !p.max_concurrent && !p.max_ips && !p.max_devices;

export const primaryOf = (r: Route) => r.targets.find((t) => t.role === 'primary') ?? r.targets[0];
export const fallbacksOf = (r: Route) => r.targets.filter((t) => t.role === 'fallback');

export const keyStatus = (k: APIKey): BadgeSpec => {
  if (!k.enabled) return { label: 'Disabled', tone: 'neutral' };
  if (k.expires_at && k.expires_at * 1000 < Date.now()) return { label: 'Expired', tone: 'danger' };
  return { label: 'Active', tone: 'success' };
};

export const isValidAlias = (s: string): boolean => /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/.test(s);

export const classNames = (...parts: Array<string | false | null | undefined>): string =>
  parts.filter(Boolean).join(' ');
