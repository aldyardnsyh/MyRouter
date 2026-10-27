import { useEffect, useMemo, useRef } from 'react';
import { Link } from 'react-router-dom';
import { Terminal } from 'lucide-react';
import { admin } from '../lib/api';
import { useQuery } from '../lib/useQuery';
import { formatLatency, formatNumber } from '../lib/format';
import type { ActivityEntry, LiveRequest, LiveSnapshot } from '../lib/types';

type Kind = 'stream' | 'req' | 'ok' | 'error' | 'blocked';

interface LogLine {
  id: string;
  at: number;
  kind: Kind;
  route: string;
  model: string;
  provider: string;
  statusCode?: number;
  latencyMs?: number;
  inTokens?: number;
  outTokens?: number;
  note?: string;
}

const KIND_STYLE: Record<Kind, { label: string; className: string }> = {
  stream: { label: 'STREAM', className: 'text-[#58a6ff]' },
  req: { label: 'REQ   ', className: 'text-[#d29922]' },
  ok: { label: 'OK    ', className: 'text-[#3fb950]' },
  error: { label: 'FAIL  ', className: 'text-[#f85149]' },
  blocked: { label: 'BLOCK ', className: 'text-[#d29922]' },
};

function clock(ms: number): string {
  const d = new Date(ms);
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  const ss = String(d.getSeconds()).padStart(2, '0');
  return `${hh}:${mm}:${ss}`;
}

// The route alias and the upstream model id are often identical. Showing both
// would read as a duplicated name, so collapse the model when it adds nothing.
function targetLabel(route: string, model: string): string {
  const r = (route || '').trim();
  const m = (model || '').trim();
  if (!m || m === r) return r || m || '-';
  if (!r) return m;
  if (m.startsWith(r)) {
    const rest = m.slice(r.length).replace(/^[\s·:/-]+/, '');
    if (!rest || rest === r) return r;
    return `${r} · ${rest}`;
  }
  return `${r} · ${m}`;
}

function lineFromLive(req: LiveRequest): LogLine {
  return {
    id: `live-${req.request_id}`,
    at: req.started_at,
    kind: req.status === 'streaming' ? 'stream' : 'req',
    route: req.route,
    model: req.requested_model,
    provider: req.provider_name,
    latencyMs: req.elapsed_ms,
  };
}

function lineFromActivity(entry: ActivityEntry): LogLine {
  const kind: Kind = entry.status === 'ok' ? 'ok' : entry.status === 'blocked' ? 'blocked' : 'error';
  return {
    id: entry.id,
    at: entry.ts * 1000,
    kind,
    route: entry.route_alias || entry.requested_model || '-',
    model: entry.model_name || entry.requested_model || '',
    provider: entry.provider_name || '',
    statusCode: entry.http_status,
    latencyMs: entry.latency_ms,
    inTokens: entry.in_tokens,
    outTokens: entry.out_tokens,
    note: entry.error_code || entry.guard_reason || undefined,
  };
}

// LiveLogCard is the dashboard terminal: in-flight requests (including streams)
// plus the latest finished outcomes, updated by polling the admin activity feed.
export function LiveLogCard({ live }: { live?: LiveSnapshot | null }) {
  const { data: activity } = useQuery(() => admin.activity.list({ limit: 14 }), {
    cacheKey: 'dashboard-log',
    refreshMs: 3000,
  });
  const boxRef = useRef<HTMLDivElement>(null);

  const lines = useMemo(() => {
    const finished = (activity?.data ?? []).map(lineFromActivity);
    // The feed is newest first; render like a terminal, oldest at the top.
    finished.sort((a, b) => a.at - b.at);
    const active = (live?.active ?? []).map(lineFromLive);
    active.sort((a, b) => a.at - b.at);
    return [...finished.slice(-12), ...active];
  }, [activity, live]);

  useEffect(() => {
    const el = boxRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines.length]);

  const activeCount = live?.active?.length ?? 0;

  return (
    <div className="overflow-hidden rounded-card border border-border bg-[#0b0f14] shadow-sm lg:col-span-3">
      <div className="flex items-center justify-between gap-3 border-b border-white/10 px-3.5 py-2.5">
        <div className="flex min-w-0 items-center gap-2.5">
          <span className="flex items-center gap-1" aria-hidden="true">
            <span className="h-2.5 w-2.5 rounded-full bg-[#ff5f57]" />
            <span className="h-2.5 w-2.5 rounded-full bg-[#febc2e]" />
            <span className="h-2.5 w-2.5 rounded-full bg-[#28c840]" />
          </span>
          <Terminal size={14} className="shrink-0 text-[#8b949e]" />
          <div className="min-w-0">
            <p className="truncate font-mono text-[12px] font-medium text-[#e6edf3]">Gateway log</p>
            <p className="truncate text-[10.5px] text-[#8b949e]">
              live requests and recent outcomes{activeCount > 0 ? ` · ${activeCount} in flight` : ''}
            </p>
          </div>
        </div>
        <Link to="/activity" className="shrink-0 font-mono text-[11px] text-[#7cb7ff] hover:underline">
          view all
        </Link>
      </div>
      <div
        ref={boxRef}
        className="h-[264px] overflow-y-auto px-3.5 py-2.5 font-mono text-[11.5px] leading-[1.55] text-[#c9d1d9]"
        role="log"
        aria-live="polite"
      >
        {lines.length === 0 ? (
          <p className="text-[#6e7681]">$ myrouter: waiting for gateway requests...</p>
        ) : (
          lines.map((line) => {
            const kind = KIND_STYLE[line.kind];
            return (
              <div key={line.id} className="flex items-baseline gap-2 whitespace-nowrap">
                <span className="shrink-0 text-[#6e7681]">{clock(line.at)}</span>
                <span className={`shrink-0 ${kind.className}`}>{kind.label}</span>
                <span className="min-w-0 flex-1 truncate text-[#e6edf3]">
                  {targetLabel(line.route, line.model)}
                  {line.provider ? <span className="text-[#8b949e]"> @ {line.provider}</span> : null}
                  {line.note ? <span className="text-[#d29922]"> · {line.note}</span> : null}
                </span>
                <span className="shrink-0 text-[#8b949e]">
                  {line.kind === 'stream' || line.kind === 'req'
                    ? `${formatLatency(line.latencyMs ?? 0)}…`
                    : `${line.statusCode ? `${line.statusCode} ` : ''}${formatLatency(line.latencyMs ?? 0)}`}
                </span>
                {line.inTokens !== undefined && (
                  <span className="shrink-0 text-[#6e7681]">
                    {formatNumber(line.inTokens)}+{formatNumber(line.outTokens ?? 0)}t
                  </span>
                )}
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}
