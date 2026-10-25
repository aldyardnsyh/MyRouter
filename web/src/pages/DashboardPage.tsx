import { useState, type KeyboardEvent } from 'react';
import { Link } from 'react-router-dom';
import { Activity, AlertTriangle, KeyRound, RefreshCw, Server, Route as RouteIcon } from 'lucide-react';
import { PageHeader, StatCard, Card, CardHeader, SkeletonCard } from '../components/ui/Layout';
import { ConnectPanel } from '../components/ConnectPanel';
import { LiveRoutingCard } from '../components/LiveRouting';
import { SecretReveal } from '../components/ui/CopyField';
import { Badge, StatusBadge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { admin, ApiError } from '../lib/api';
import { useQuery } from '../lib/useQuery';

import {
  activityBadge,
  classNames,
  formatCompact,
  formatLatency,
  formatNumber,
  formatTime,
  providerInitial,
  providerHue,
  providerTypeLabel,
  relativeTime,
  statusSpec,
} from '../lib/format';

const CHART_W = 680;
const CHART_PAD_R = 26;
const CHART_H = 168;

function hourLabel(d: Date) {
  return `${String(d.getHours()).padStart(2, '0')}:00`;
}

// Catmull-Rom to cubic bezier: hourly buckets are samples of a continuous
// trend, and a smooth curve keeps small deltas readable instead of spiky.
function smoothPath(pts: readonly (readonly [number, number])[]) {
  if (pts.length < 2) return pts.length ? `M ${pts[0][0]},${pts[0][1]}` : '';
  let d = `M ${pts[0][0].toFixed(1)},${pts[0][1].toFixed(1)}`;
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[Math.max(0, i - 1)];
    const p1 = pts[i];
    const p2 = pts[i + 1];
    const p3 = pts[Math.min(pts.length - 1, i + 2)];
    const c1x = p1[0] + (p2[0] - p0[0]) / 6;
    const c1y = p1[1] + (p2[1] - p0[1]) / 6;
    const c2x = p2[0] - (p3[0] - p1[0]) / 6;
    const c2y = p2[1] - (p3[1] - p1[1]) / 6;
    d += ` C ${c1x.toFixed(1)},${c1y.toFixed(1)} ${c2x.toFixed(1)},${c2y.toFixed(1)} ${p2[0].toFixed(1)},${p2[1].toFixed(1)}`;
  }
  return d;
}

// Motion purpose (R-19): one draw-in on mount/refresh to lead the eye along
// the trend, then rest. No loops. Disabled under prefers-reduced-motion.
const chartStyle = `
@keyframes mrc-draw { from { stroke-dashoffset: 1; } to { stroke-dashoffset: 0; } }
@keyframes mrc-fade { from { opacity: 0; } to { opacity: 1; } }
.mrc-line { stroke-dasharray: 1; stroke-dashoffset: 1; animation: mrc-draw 1.1s cubic-bezier(0.22, 1, 0.36, 1) 0.15s forwards; }
.mrc-area { opacity: 0; animation: mrc-fade 0.9s ease-out 0.55s forwards; }
.mrc-plot { border-radius: 6px; border: 1px solid transparent; }
.mrc-plot:has(:focus-visible) { border-color: rgb(var(--c-accent)); box-shadow: 0 0 0 3px rgb(var(--c-accent) / 0.16); }
@media (prefers-reduced-motion: reduce) {
  .mrc-line { animation: none; stroke-dashoffset: 0; }
  .mrc-area { animation: none; opacity: 1; }
}`;

function RequestChart({ series }: { series: number[] }) {
  const data = series.length ? series : new Array(24).fill(0);
  const [active, setActive] = useState<number | null>(null);
  const total = data.reduce((a, b) => a + b, 0);
  const peak = Math.max(...data);
  const peakIdx = data.indexOf(peak);
  const niceMax = Math.max(4, Math.ceil(Math.max(peak, 1) / 4) * 4);
  const width = CHART_W;
  const height = CHART_H;
  const padTop = 14;
  const plotW = width - CHART_PAD_R;
  const step = plotW / Math.max(1, data.length - 1);
  const y = (v: number) => height - (v / niceMax) * (height - padTop - 6) - 6;
  const pts = data.map((v, i) => [i * step, y(v)] as const);
  const line = smoothPath(pts);
  const area = `${line} L ${width - CHART_PAD_R},${height} L 0,${height} Z`;
  const bucketStart = (i: number) => {
    const d = new Date();
    d.setMinutes(0, 0, 0);
    d.setHours(d.getHours() - (data.length - 1 - i));
    return d;
  };
  const activeX = active !== null ? pts[active][0] : 0;
  const activeY = active !== null ? pts[active][1] : 0;
  const tooltipLeft = active !== null ? Math.min(Math.max(activeX, 82), width - CHART_PAD_R - 82) : 0;
  const ticks = [0, 6, 12, 18, data.length - 1];
  const avgPerHour = total / data.length;

  function onKey(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === 'ArrowRight') {
      e.preventDefault();
      setActive((a) => (a === null ? data.length - 1 : Math.min(data.length - 1, a + 1)));
    } else if (e.key === 'ArrowLeft') {
      e.preventDefault();
      setActive((a) => (a === null ? 0 : Math.max(0, a - 1)));
    } else if (e.key === 'Home') {
      e.preventDefault();
      setActive(0);
    } else if (e.key === 'End') {
      e.preventDefault();
      setActive(data.length - 1);
    } else if (e.key === 'Escape') {
      setActive(null);
    }
  }

  return (
    <div className="flex flex-col">
      <style>{chartStyle}</style>
      <div className="mrc-plot relative">
      <div
        tabIndex={0}
        role="img"
        aria-label={`Hourly requests, last 24 hours. Total ${formatNumber(total)}, peak ${formatNumber(peak)} per hour.`}
        onKeyDown={onKey}
        onPointerMove={(e) => {
          const box = e.currentTarget.getBoundingClientRect();
          if (box.width === 0) return;
          const ratio = (e.clientX - box.left) / box.width;
          const idx = Math.round(ratio * (data.length - 1));
          setActive(Math.min(data.length - 1, Math.max(0, idx)));
        }}
        onPointerLeave={() => setActive(null)}
      >
        <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" className="h-[168px] w-full overflow-visible">
          <defs>
            <linearGradient id="reqGradient" x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stopColor="rgb(var(--c-accent))" stopOpacity="0.22" />
              <stop offset="100%" stopColor="rgb(var(--c-accent))" stopOpacity="0" />
            </linearGradient>
          </defs>
          {[0.25, 0.5, 0.75].map((p) => {
            const v = Math.round(niceMax * (1 - p));
            return (
              <g key={p}>
                <line x1="0" x2={width - CHART_PAD_R} y1={height * p} y2={height * p} stroke="rgb(var(--c-border))" strokeDasharray="3 3" />
                <text x={width} y={height * p + 3} textAnchor="end" className="fill-[rgb(var(--c-muted))]" fontSize="9">
                  {formatCompact(v)}
                </text>
              </g>
            );
          })}
          <line x1="0" x2={width - CHART_PAD_R} y1={height} y2={height} stroke="rgb(var(--c-border))" />
          <path d={area} fill="url(#reqGradient)" className="mrc-area" />
          <path
            d={line}
            fill="none"
            stroke="rgb(var(--c-accent))"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            pathLength={1}
            className="mrc-line"
          />
          {pts[peakIdx] && peak > 0 && (
            <circle cx={pts[peakIdx][0]} cy={pts[peakIdx][1]} r="4" fill="rgb(var(--c-bg))" stroke="rgb(var(--c-accent))" strokeWidth="2" />
          )}
          {active !== null && (
            <g>
              <line x1={activeX} x2={activeX} y1={padTop - 6} y2={height} stroke="rgb(var(--c-accent))" strokeOpacity="0.35" />
              <circle cx={activeX} cy={activeY} r="4.5" fill="rgb(var(--c-accent))" stroke="rgb(var(--c-bg))" strokeWidth="2" />
            </g>
          )}
        </svg>
        {active !== null && (
          <div
            className="pointer-events-none absolute z-10 -translate-x-1/2 rounded-lg border border-border bg-surface px-2.5 py-1.5 text-center shadow-md"
            style={{ left: `${(tooltipLeft / width) * 100}%`, top: 6 }}
          >
            <p className="font-mono text-[10.5px] text-muted">
              {hourLabel(bucketStart(active))} – {hourLabel(new Date(bucketStart(active).getTime() + 3_600_000))}
            </p>
            <p className="text-[12px] font-semibold text-text">
              {formatNumber(data[active])} <span className="font-normal text-muted">requests</span>
            </p>
          </div>
        )}
      </div>
      </div>
      <div className="mt-1 flex justify-between font-mono text-[10.5px] text-muted">
        {ticks.map((i) => (
          <span key={i}>{i === data.length - 1 ? 'Now' : hourLabel(bucketStart(i))}</span>
        ))}
      </div>
      {total === 0 ? (
        <p className="mt-2 text-[11.5px] text-muted">
          No traffic in the last 24 hours. Send a request through the gateway and it will appear here.
        </p>
      ) : (
        <p className="mt-2 flex flex-wrap gap-x-3 gap-y-0.5 text-[11.5px] text-muted">
          <span>
            Total <span className="font-semibold text-text">{formatNumber(total)}</span>
          </span>
          <span aria-hidden>·</span>
          <span>
            Peak <span className="font-semibold text-text">{formatNumber(peak)}</span> at {hourLabel(bucketStart(peakIdx))}
          </span>
          <span aria-hidden>·</span>
          <span>
            Avg <span className="font-semibold text-text">{avgPerHour.toFixed(1)}</span>/hr
          </span>
        </p>
      )}
    </div>
  );
}

export function DashboardPage() {

  const { data, error, loading, reload } = useQuery(
    () => admin.overview(),
    { cacheKey: 'overview', refreshMs: 15000 },
  );
  const { data: live } = useQuery(() => admin.live(), { cacheKey: 'live', refreshMs: 2000 });
  const load = reload;
  const [quickKey, setQuickKey] = useState('');
  const [generating, setGenerating] = useState(false);
  const [keyError, setKeyError] = useState('');

  // One-click client key for the dashboard. The secret is shown once, exactly
  // like the API Keys page; nothing is stored in the browser.
  async function generateKey() {
    setKeyError('');
    setGenerating(true);
    try {
      const res = await admin.keys.create({ name: `dashboard-${new Date().toISOString().slice(0, 10)}` });
      setQuickKey(res.secret);
    } catch (err) {
      setKeyError(err instanceof ApiError ? err.message : 'Could not generate a key');
    } finally {
      setGenerating(false);
    }
  }

  if (loading && !data) {
    return (
      <div className="flex flex-col gap-5">
        <SkeletonCard className="h-16" />
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-6">
          {Array.from({ length: 6 }).map((_, i) => (
            <SkeletonCard key={i} className="h-[92px]" />
          ))}
        </div>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
          <SkeletonCard className="h-[280px] lg:col-span-7" />
          <SkeletonCard className="h-[280px] lg:col-span-5" />
        </div>
      </div>
    );
  }

  if (error && !data) {
    return (
      <div className="flex flex-col items-center justify-center py-24 text-center">
        <p className="text-[13px] text-muted">{error}</p>
        <Button variant="secondary" size="compact" className="mt-3" onClick={() => void load()}>
          Retry
        </Button>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="flex flex-col items-center gap-2 py-24 text-center">
        <p className="text-[13px] text-muted">Dashboard data is unavailable.</p>
        <Button variant="secondary" size="compact" onClick={() => void load()}>Retry</Button>
      </div>
    );
  }
  const m = data.metrics;
  const successRate = m.requests ? m.success_rate : 0;
  const fallbackPct = m.requests ? (m.fallbacks / m.requests) * 100 : 0;

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Dashboard"
        description="System overview, gateway performance, and routing health across providers."
        meta={
          <Badge tone="success" dot>
            live
          </Badge>
        }
        actions={
          <>
            <Button variant="secondary" size="compact" icon={<RefreshCw size={14} />} onClick={() => void load()} loading={loading}>
              Refresh
            </Button>
            <Link to="/providers">
              <Button variant="secondary" size="compact" icon={<Server size={14} />}>
                Add Provider
              </Button>
            </Link>
            <Link to="/routes">
              <Button variant="primary" size="compact" icon={<RouteIcon size={14} />}>
                Create Route
              </Button>
            </Link>
          </>
        }
      />

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <StatCard label="Providers" value={String(m.providers)} hint={m.providers ? 'All upstreams' : 'None configured'} tone="default" icon={<Server size={15} />} />
        <StatCard label="Active Routes" value={String(m.active_routes)} hint="enabled aliases" icon={<RouteIcon size={15} />} />
        <StatCard label="Requests (24h)" value={formatNumber(m.requests)} hint={m.requests ? 'last 24 hours' : 'no traffic yet'} tone={m.requests ? 'success' : 'default'} />
        <StatCard label="Success Rate" value={`${successRate.toFixed(1)}%`} hint={`Avg latency ${formatLatency(m.avg_latency_ms)}`} tone={successRate >= 99 ? 'success' : successRate >= 95 ? 'warning' : 'danger'} />
        <StatCard label="Fallbacks" value={formatNumber(m.fallbacks)} hint={`${fallbackPct.toFixed(1)}% triggered`} tone={m.fallbacks ? 'warning' : 'default'} />
        <StatCard label="Tokens" value={formatCompact(m.in_tokens + m.out_tokens)} hint={`${formatCompact(m.in_tokens)} in / ${formatCompact(m.out_tokens)} out`} />
      </div>

      <LiveRoutingCard providers={data.providers} live={live ?? undefined} />

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
        <Card className="lg:col-span-7">
          <CardHeader
            title="Requests (24h)"
            description="Hourly request throughput across active routes. Hover or use arrow keys for per-hour detail."
            actions={
              <div className="inline-flex rounded-control border border-border bg-surface p-0.5">
                <span className="rounded-[6px] bg-bg px-2.5 py-0.5 text-[11px] font-semibold text-text">24h</span>
              </div>
            }
          />
          <RequestChart series={data.series} />
        </Card>

        <Card className="lg:col-span-5">
          <CardHeader
            title="Connect a client"
            description="Provider key = kredensial upstream (Providers). Key di bawah = kredensial client MyRouter."
            actions={<Badge tone="neutral" dot={false}>HTTP / SSE</Badge>}
          />
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <Button variant="primary" size="compact" icon={<KeyRound size={14} />} loading={generating} onClick={() => void generateKey()}>
              Generate API key
            </Button>
            <Link to="/api-keys" className="text-[11.5px] font-medium text-accent hover:underline">
              Manage keys
            </Link>
            <Link to="/providers" className="text-[11.5px] text-muted hover:text-text">
              Configure upstream
            </Link>
          </div>
          {quickKey && (
            <div className="mb-3 flex flex-col gap-2 rounded-card border border-warning/40 bg-warning-soft p-3">
              <div className="flex items-start gap-2">
                <AlertTriangle size={15} className="mt-0.5 shrink-0 text-warning" />
                <div>
                  <p className="text-[12px] font-semibold text-text">Simpan key ini sekarang</p>
                  <p className="text-[11px] text-muted">Key hanya ditampilkan sekali. Salin lalu tempel ke client Anda.</p>
                </div>
              </div>
              <SecretReveal
                value={quickKey}
                label="Client API key"
                onDismiss={() => setQuickKey('')}
                hint="Gunakan sebagai apiKey / Bearer token pada client."
              />
            </div>
          )}
          {keyError && <p className="mb-3 text-[12px] text-danger" role="alert">{keyError}</p>}
          <ConnectPanel baseUrl={data.base_url} apiKey={quickKey || undefined} />
        </Card>
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader
            title="Recent Activity"
            description="Latest gateway requests"
            actions={
              <Link to="/activity" className="text-[12px] font-medium text-accent hover:underline">
                View all
              </Link>
            }
          />
          {data.recent.length === 0 ? (
            <div className="flex items-center gap-2 py-6 text-[12.5px] text-muted">
              <Activity size={15} /> No requests yet. Send your first call to the gateway.
            </div>
          ) : (
            <table className="w-full">
              <thead>
                <tr className="text-[11px] uppercase tracking-wide text-muted">
                  <th className="py-1.5 text-left font-medium">Time</th>
                  <th className="py-1.5 text-left font-medium">Route</th>
                  <th className="py-1.5 text-left font-medium">Status</th>
                  <th className="py-1.5 text-right font-medium">Latency</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {data.recent.map((r) => (
                  <tr key={r.id} className="text-[12.5px]">
                    <td className="py-2 font-mono text-[11.5px] text-muted">{formatTime(r.ts)}</td>
                    <td className="py-2">
                      <span className="rounded-[5px] border border-border bg-surface px-1.5 py-0.5 font-mono text-[11px] text-text">
                        {r.route_alias || r.requested_model || '-'}
                      </span>
                    </td>
                    <td className="py-2">
                      <StatusBadge spec={activityBadge(r)} />
                    </td>
                    <td className="py-2 text-right font-mono text-[11.5px] text-muted">{formatLatency(r.latency_ms)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>

        <Card>
          <CardHeader title="Provider Health" description="Status of configured upstreams" />
          {data.providers.length === 0 ? (
            <div className="flex flex-col items-start gap-2 py-6">
              <p className="text-[12.5px] text-muted">No providers configured yet.</p>
              <Link to="/providers">
                <Button variant="primary" size="compact">Add Provider</Button>
              </Link>
            </div>
          ) : (
            <ul className="flex flex-col divide-y divide-border">
              {data.providers.map((p) => (
                <li key={p.id} className="flex items-center justify-between gap-3 py-2.5">
                  <div className="flex min-w-0 items-center gap-2.5">
                    <span className={classNames('flex h-8 w-8 items-center justify-center rounded-[8px] text-[12px] font-bold', providerHue(p.name))}>
                      {providerInitial(p.name)}
                    </span>
                    <div className="min-w-0">
                      <p className="truncate text-[12.5px] font-medium text-text">{p.name}</p>
                      <p className="truncate text-[11px] text-muted">{providerTypeLabel[p.type] ?? p.type} · {p.model_count} models</p>
                    </div>
                  </div>
                  <div className="flex shrink-0 flex-col items-end gap-1">
                    <StatusBadge spec={statusSpec[p.status]} />
                    <span className="font-mono text-[10.5px] text-muted">{relativeTime(p.last_check)}</span>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>
    </div>
  );
}

