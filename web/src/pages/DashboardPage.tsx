import { useState, type KeyboardEvent } from 'react';
import { Link } from 'react-router-dom';
import { Activity, AlertTriangle, KeyRound, RefreshCw, Server, Route as RouteIcon } from 'lucide-react';
import { PageHeader, StatCard, Card, CardHeader, SkeletonCard } from '../components/ui/Layout';
import { ConnectPanel } from '../components/ConnectPanel';
import { LiveRoutingCard } from '../components/LiveRouting';
import { LiveLogCard } from '../components/LiveLog';
import { SecretReveal } from '../components/ui/CopyField';
import { Badge, StatusBadge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { admin, ApiError } from '../lib/api';
import { useQuery } from '../lib/useQuery';
import type { SeriesPoint } from '../lib/types';

import {
  activityBadge,
  formatCompact,
  formatCost,
  formatLatency,
  formatNumber,
  formatTime,
} from '../lib/format';

const CHART_W = 680;
const CHART_H = 168;
const CHART_PAD_R = 26;

const RANGES = [
  { id: '24h', label: '24h', bucket: 3600 },
  { id: '3d', label: '3d', bucket: 86400 },
  { id: '7d', label: '7d', bucket: 86400 },
  { id: '30d', label: '30d', bucket: 86400 },
] as const;

type RangeId = (typeof RANGES)[number]['id'];

// The chart plots one of three metrics per bucket; the hover tooltip always
// shows the full breakdown regardless of which metric is on the line.
const METRICS = [
  { id: 'requests', label: 'Requests' },
  { id: 'tokens', label: 'Tokens' },
  { id: 'latency', label: 'Latency' },
] as const;

type MetricId = (typeof METRICS)[number]['id'];

// Bucket label depends on the width: hours for a single day, calendar days for
// multi-day ranges, so the axis never claims more precision than it has.
function bucketLabel(sec: number, bucketSec: number, isLast: boolean) {
  const d = new Date(sec * 1000);
  if (bucketSec >= 86400) {
    return d.toLocaleDateString(undefined, { day: '2-digit', month: 'short' });
  }
  if (isLast) return 'Now';
  return `${String(d.getHours()).padStart(2, '0')}:00`;
}

function bucketRangeLabel(sec: number, bucketSec: number) {
  const from = new Date(sec * 1000);
  const to = new Date((sec + bucketSec) * 1000);
  const day = (d: Date) => d.toLocaleDateString(undefined, { day: '2-digit', month: 'short' });
  const clock = (d: Date) => `${String(d.getHours()).padStart(2, '0')}:00`;
  if (bucketSec >= 86400) return day(from);
  return `${clock(from)} - ${clock(to)}`;
}

// Catmull-Rom to cubic bezier: buckets sample a continuous trend, and a smooth
// curve keeps small deltas readable instead of spiky.
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

// Motion purpose: one draw-in on mount and on range change to lead the eye
// along the trend, then rest. No loops. Off under prefers-reduced-motion.
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

function SeriesChart({
  points,
  bucketSec,
  range,
  metric,
  loading,
}: {
  points: SeriesPoint[];
  bucketSec: number;
  range: RangeId;
  metric: MetricId;
  loading: boolean;
}) {
  const empty: SeriesPoint = {
    start: Math.floor(Date.now() / 1000),
    requests: 0,
    ok: 0,
    failed: 0,
    blocked: 0,
    fallbacks: 0,
    in_tokens: 0,
    out_tokens: 0,
    avg_latency_ms: 0,
    max_latency_ms: 0,
  };
  const data = points.length ? points : [empty];
  const [active, setActive] = useState<number | null>(null);
  // Value plotted for the active metric. Latency buckets with no traffic plot
  // as 0 so gaps in traffic read as gaps, not as fake zero-latency dips.
  const val = (d: SeriesPoint) =>
    metric === 'requests' ? d.requests : metric === 'tokens' ? d.in_tokens + d.out_tokens : d.requests ? d.avg_latency_ms : 0;
  const yLabel = metric === 'latency' ? formatLatency : formatCompact;
  const totals = data.reduce(
    (a, d) => ({
      requests: a.requests + d.requests,
      ok: a.ok + d.ok,
      failed: a.failed + d.failed,
      blocked: a.blocked + d.blocked,
      fallbacks: a.fallbacks + d.fallbacks,
      tokens: a.tokens + d.in_tokens + d.out_tokens,
    }),
    { requests: 0, ok: 0, failed: 0, blocked: 0, fallbacks: 0, tokens: 0 },
  );
  const peak = Math.max(...data.map(val), 0);
  const peakIdx = peak > 0 ? data.findIndex((d) => val(d) === peak) : -1;
  const niceMax = Math.max(4, Math.ceil(Math.max(peak, 1) / 4) * 4);
  const width = CHART_W;
  const height = CHART_H;
  const padTop = 14;
  const plotW = width - CHART_PAD_R;
  const step = plotW / Math.max(1, data.length - 1);
  const y = (v: number) => height - (v / niceMax) * (height - padTop - 6) - 6;
  const pts = data.map((d, i) => [i * step, y(val(d))] as const);
  const line = smoothPath(pts);
  const area = `${line} L ${width - CHART_PAD_R},${height} L 0,${height} Z`;
  const activeX = active !== null ? pts[active][0] : 0;
  const activeY = active !== null ? pts[active][1] : 0;
  const tooltipLeft = active !== null ? Math.min(Math.max(activeX, 96), width - CHART_PAD_R - 96) : 0;
  const last = data.length - 1;
  const tickIdx =
    data.length <= 5
      ? data.map((_, i) => i)
      : [0, Math.round(last / 4), Math.round(last / 2), Math.round((last * 3) / 4), last];
  const perLabel = bucketSec >= 86400 ? '/day' : '/hr';
  const badTotal = totals.failed + totals.blocked;
  // Request-weighted average latency across buckets that actually had traffic.
  const latBuckets = data.filter((d) => d.requests > 0);
  const avgLat = latBuckets.length
    ? latBuckets.reduce((a, d) => a + d.avg_latency_ms * d.requests, 0) / latBuckets.reduce((a, d) => a + d.requests, 0)
    : 0;

  function onKey(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === 'ArrowRight') {
      e.preventDefault();
      setActive((a) => (a === null ? last : Math.min(last, a + 1)));
    } else if (e.key === 'ArrowLeft') {
      e.preventDefault();
      setActive((a) => (a === null ? 0 : Math.max(0, a - 1)));
    } else if (e.key === 'Home') {
      e.preventDefault();
      setActive(0);
    } else if (e.key === 'End') {
      e.preventDefault();
      setActive(last);
    } else if (e.key === 'Escape') {
      setActive(null);
    }
  }

  const act = active !== null ? data[active] : null;

  return (
    <div className="flex flex-col">
      <style>{chartStyle}</style>
      <div className="mrc-plot relative">
        <div
          tabIndex={0}
          role="img"
          aria-label={`${METRICS.find((m) => m.id === metric)!.label} per ${bucketSec >= 86400 ? 'day' : 'hour'} over the last ${range}. Total ${formatNumber(
            totals.requests,
          )} requests, ${formatCompact(totals.tokens)} tokens, peak ${yLabel(peak)} per bucket.`}
          onKeyDown={onKey}
          onPointerMove={(e) => {
            const box = e.currentTarget.getBoundingClientRect();
            if (box.width === 0) return;
            const ratio = (e.clientX - box.left) / box.width;
            const idx = Math.round(ratio * last);
            setActive(Math.min(last, Math.max(0, idx)));
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
                    {yLabel(v)}
                  </text>
                </g>
              );
            })}
            <line x1="0" x2={width - CHART_PAD_R} y1={height} y2={height} stroke="rgb(var(--c-border))" />
            <path key={`a-${range}`} d={area} fill="url(#reqGradient)" className="mrc-area" />
            <path
              key={`l-${range}`}
              d={line}
              fill="none"
              stroke="rgb(var(--c-accent))"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
              pathLength={1}
              className="mrc-line"
            />
            {peakIdx >= 0 && (
              <circle cx={pts[peakIdx][0]} cy={pts[peakIdx][1]} r="4" fill="rgb(var(--c-bg))" stroke="rgb(var(--c-accent))" strokeWidth="2" />
            )}
            {metric === 'requests' &&
              data.map((d, i) => {
                if (d.failed <= 0 && d.blocked <= 0) return null;
                // Failed/blocked mini-bars sit on the baseline under the trend
                // line, so error spikes are visible without leaving the chart.
                const barW = Math.min(6, Math.max(2, step * 0.45));
                const scale = height - padTop - 6;
                const failedH = (d.failed / niceMax) * scale;
                const blockedH = (d.blocked / niceMax) * scale;
                const x = pts[i][0] - barW / 2;
                return (
                  <g key={`bar-${i}`}>
                    {d.failed > 0 && (
                      <rect x={x} y={height - 6 - failedH} width={barW} height={failedH} rx="1.5" fill="rgb(var(--c-danger))" opacity="0.55" />
                    )}
                    {d.blocked > 0 && (
                      <rect
                        x={x}
                        y={height - 6 - failedH - blockedH}
                        width={barW}
                        height={blockedH}
                        rx="1.5"
                        fill="rgb(var(--c-warning))"
                        opacity="0.55"
                      />
                    )}
                  </g>
                );
              })}
            {active !== null && (
              <g>
                <line x1={activeX} x2={activeX} y1={padTop - 6} y2={height} stroke="rgb(var(--c-accent))" strokeOpacity="0.35" />
                <circle cx={activeX} cy={activeY} r="4.5" fill="rgb(var(--c-accent))" stroke="rgb(var(--c-bg))" strokeWidth="2" />
              </g>
            )}
          </svg>
          {act && (
            <div
              className="pointer-events-none absolute z-10 w-[190px] -translate-x-1/2 rounded-lg border border-border bg-surface px-2.5 py-2 shadow-md"
              style={{ left: `${(tooltipLeft / width) * 100}%`, top: 6 }}
            >
              <p className="font-mono text-[10px] text-muted">{bucketRangeLabel(act.start, bucketSec)}</p>
              <p className="mt-0.5 text-[12.5px] font-semibold text-text">
                {metric === 'latency'
                  ? act.requests
                    ? formatLatency(act.avg_latency_ms)
                    : '-'
                  : formatNumber(act.requests)}
                <span className="font-normal text-muted">
                  {' '}
                  {metric === 'latency' ? 'avg latency' : metric === 'tokens' ? `tokens (${formatCompact(act.in_tokens + act.out_tokens)})` : 'requests'}
                </span>
              </p>
              <dl className="mt-1 grid grid-cols-2 gap-x-2 gap-y-0.5 text-[10.5px]">
                <dt className="text-muted">ok</dt>
                <dd className="text-right font-mono text-text">{formatNumber(act.ok)}</dd>
                <dt className="text-muted">failed</dt>
                <dd className="text-right font-mono text-text">{formatNumber(act.failed)}</dd>
                <dt className="text-muted">blocked</dt>
                <dd className="text-right font-mono text-text">{formatNumber(act.blocked)}</dd>
                <dt className="text-muted">fallback</dt>
                <dd className="text-right font-mono text-text">{formatNumber(act.fallbacks)}</dd>
                <dt className="text-muted">tokens</dt>
                <dd className="text-right font-mono text-text">{formatCompact(act.in_tokens + act.out_tokens)}</dd>
                <dt className="text-muted">avg latency</dt>
                <dd className="text-right font-mono text-text">
                  {act.requests ? formatLatency(act.avg_latency_ms) : '-'}
                </dd>
              </dl>
            </div>
          )}
        </div>
      </div>
      <div className="mt-1 flex justify-between font-mono text-[10.5px] text-muted">
        {tickIdx.map((i) => (
          <span key={i}>{bucketLabel(data[i].start, bucketSec, i === last)}</span>
        ))}
      </div>
      {totals.requests === 0 ? (
        <p className="mt-2 text-[11.5px] text-muted">
          No traffic in the last {range}. Send a request through the gateway and it will appear here.
        </p>
      ) : (
        <div className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 text-[11.5px] text-muted sm:grid-cols-5">
          <span>
            Total <span className="font-semibold text-text">{formatNumber(totals.requests)}</span>
          </span>
          <span>
            Peak <span className="font-semibold text-text">{yLabel(peak)}</span>
            {peakIdx >= 0 ? ` at ${bucketLabel(data[peakIdx].start, bucketSec, false)}` : ''}
          </span>
          <span>
            Avg{' '}
            <span className="font-semibold text-text">
              {metric === 'latency' ? formatLatency(avgLat) : (totals.requests / Math.max(1, data.length)).toFixed(bucketSec >= 86400 ? 0 : 1)}
            </span>
            {perLabel}
          </span>
          <span>
            Tokens <span className="font-semibold text-text">{formatCompact(totals.tokens)}</span>
          </span>
          <span className="flex items-center gap-1.5">
            {badTotal > 0 && (
              <>
                <span className="inline-block h-2 w-2 rounded-full bg-[rgb(var(--c-danger))] opacity-70" />
                <span className="font-semibold text-text">{formatNumber(totals.failed)}</span>
                <span className="inline-block h-2 w-2 rounded-full bg-[rgb(var(--c-warning))] opacity-70" />
                <span className="font-semibold text-text">{formatNumber(totals.blocked)}</span>
                <span>bad</span>
              </>
            )}
            {badTotal === 0 && (
              <span>
                Failed <span className="font-semibold text-success">0</span>
              </span>
            )}
          </span>
        </div>
      )}
      {loading && <p className="mt-1 text-[10.5px] text-muted">Refreshing {range}…</p>}
    </div>
  );
}

export function DashboardPage() {

  const { data, error, loading, reload } = useQuery(
    () => admin.overview(),
    { cacheKey: 'overview', refreshMs: 15000 },
  );
  const { data: live } = useQuery(() => admin.live(), { cacheKey: 'live', refreshMs: 2000 });
  // Est. spend for the last 24h: reuse the provider-grouped usage rows and sum
  // the catalog-priced estimates (null when no row has a price).
  const { data: spendData } = useQuery(() => admin.usage('provider', 1), {
    cacheKey: 'spend-24h',
    refreshMs: 60000,
  });
  // Chart range switcher (24h / 3d / 7d). Each range has its own cache entry,
  // so switching back is instant and the per-bucket aggregates (ok/failed/
  // blocked/fallbacks/tokens/latency) come from /overview/series for the range.
  const [rangeId, setRangeId] = useState<RangeId>('24h');
  const [metricId, setMetricId] = useState<MetricId>('requests');
  const { data: seriesRes, loading: seriesLoading } = useQuery(() => admin.series(rangeId), {
    cacheKey: `series-${rangeId}`,
    refreshMs: 30000,
  });
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
  const pricedSpend = (spendData?.data ?? []).filter((row) => row.est_cost !== null);
  const spendTotal = pricedSpend.length
    ? pricedSpend.reduce((acc, row) => acc + (row.est_cost ?? 0), 0)
    : null;

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

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-7">
        <StatCard label="Providers" value={String(m.providers)} hint={m.providers ? 'All upstreams' : 'None configured'} tone="default" icon={<Server size={15} />} />
        <StatCard label="Active Routes" value={String(m.active_routes)} hint="enabled aliases" icon={<RouteIcon size={15} />} />
        <StatCard label="Requests (24h)" value={formatNumber(m.requests)} hint={m.requests ? 'last 24 hours' : 'no traffic yet'} tone={m.requests ? 'success' : 'default'} />
        <StatCard label="Success Rate" value={`${successRate.toFixed(1)}%`} hint={`Avg latency ${formatLatency(m.avg_latency_ms)}`} tone={successRate >= 99 ? 'success' : successRate >= 95 ? 'warning' : 'danger'} />
        <StatCard label="Fallbacks" value={formatNumber(m.fallbacks)} hint={`${fallbackPct.toFixed(1)}% triggered`} tone={m.fallbacks ? 'warning' : 'default'} />
        <StatCard label="Tokens" value={formatCompact(m.in_tokens + m.out_tokens)} hint={`${formatCompact(m.in_tokens)} in / ${formatCompact(m.out_tokens)} out`} />
        <StatCard
          label="Est. spend (24h)"
          value={spendTotal === null ? 'Unavailable' : formatCost(spendTotal)}
          hint={spendTotal === null ? 'no priced usage' : 'estimated from catalog prices'}
          tone={spendTotal === null ? 'default' : 'accent'}
        />
      </div>

      <LiveRoutingCard providers={data.providers} live={live ?? undefined} />

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
        <Card className="lg:col-span-7">
          <CardHeader
            title={`Requests (${rangeId})`}
            description={
              rangeId === '24h'
                ? 'Hourly request throughput across active routes. Hover or use arrow keys for per-hour detail.'
                : 'Daily request throughput across active routes. Hover or use arrow keys for per-day detail.'
            }
            actions={
              <div className="flex items-center gap-2">
                <div className="inline-flex rounded-control border border-border bg-surface p-0.5" role="tablist" aria-label="Chart metric">
                  {METRICS.map((mt) => (
                    <button
                      key={mt.id}
                      type="button"
                      role="tab"
                      aria-selected={metricId === mt.id}
                      onClick={() => setMetricId(mt.id)}
                      className={
                        'rounded-[6px] px-2.5 py-0.5 text-[11px] font-semibold transition-colors ' +
                        (metricId === mt.id ? 'bg-bg text-text' : 'text-muted hover:text-text')
                      }
                    >
                      {mt.label}
                    </button>
                  ))}
                </div>
                <div className="inline-flex rounded-control border border-border bg-surface p-0.5" role="tablist" aria-label="Chart range">
                  {RANGES.map((r) => (
                    <button
                      key={r.id}
                      type="button"
                      role="tab"
                      aria-selected={rangeId === r.id}
                      onClick={() => setRangeId(r.id)}
                      className={
                        'rounded-[6px] px-2.5 py-0.5 text-[11px] font-semibold transition-colors ' +
                        (rangeId === r.id ? 'bg-bg text-text' : 'text-muted hover:text-text')
                      }
                    >
                      {r.label}
                    </button>
                  ))}
                </div>
              </div>
            }
          />
          <SeriesChart
            points={seriesRes?.data ?? []}
            bucketSec={seriesRes?.bucket_sec ?? RANGES.find((r) => r.id === rangeId)!.bucket}
            range={rangeId}
            metric={metricId}
            loading={seriesLoading}
          />
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

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-5">
        <Card className="lg:col-span-2">
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

        <LiveLogCard live={live} />
      </div>
    </div>
  );
}

