import { useMemo } from 'react';
import type { LiveRequest, LiveSnapshot, Provider } from '../lib/types';
import { classNames } from '../lib/format';

interface Props {
  providers: Provider[];
  live?: LiveSnapshot;
}

const W = 760;
const H = 300;
const CENTER = { x: 380, y: 150 };

function nodePositions(count: number) {
  // Split providers into a left and right column so edges read as a fan-out
  // from the router, like a routing topology rather than a table.
  const positions: { x: number; y: number }[] = [];
  const left = Math.ceil(count / 2);
  for (let i = 0; i < count; i++) {
    const isLeft = i < left;
    const idx = isLeft ? i : i - left;
    const total = isLeft ? left : count - left;
    const step = H / (total + 1);
    positions.push({ x: isLeft ? 118 : W - 118, y: step * (idx + 1) });
  }
  return positions;
}

function shortName(name: string, max = 17) {
  return name.length > max ? `${name.slice(0, max - 1)}…` : name;
}

function statusColor(status: string) {
  switch (status) {
    case 'ok':
      return 'rgb(var(--c-success))';
    case 'error':
    case 'blocked':
      return 'rgb(var(--c-danger))';
    default:
      return 'rgb(var(--c-accent))';
  }
}

export function LiveRoutingCard({ providers, live }: Props) {
  const active = live?.active ?? [];
  const recent = live?.recent ?? [];
  const nodes = useMemo(() => providers.slice(0, 6), [providers]);
  const positions = useMemo(() => nodePositions(nodes.length), [nodes.length]);
  const byId = useMemo(() => new Map(nodes.map((p, i) => [p.id, { provider: p, pos: positions[i] }])), [nodes, positions]);

  const activeByProvider = useMemo(() => {
    const map = new Map<string, number>();
    for (const req of active) {
      if (req.provider_id) map.set(req.provider_id, (map.get(req.provider_id) ?? 0) + 1);
    }
    return map;
  }, [active]);

  // Draw one animated packet per in-flight request, capped so the map stays
  // readable under load.
  const packets = active.slice(0, 8).flatMap((req) => {
    const target = byId.get(req.provider_id);
    if (!target) return [];
    const path = `M ${CENTER.x} ${CENTER.y} C ${CENTER.x + (target.pos.x - CENTER.x) * 0.45} ${CENTER.y}, ${
      CENTER.x + (target.pos.x - CENTER.x) * 0.55
    } ${target.pos.y}, ${target.pos.x} ${target.pos.y}`;
    return [{ req, path }];
  });

  return (
    <div className="overflow-hidden rounded-card border border-border bg-bg">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-4 py-3">
        <div>
          <div className="flex items-center gap-2">
            <span className="relative flex h-2 w-2">
              <span className={classNames('absolute inline-flex h-full w-full rounded-full', active.length ? 'animate-ping bg-success opacity-70' : 'bg-muted opacity-40')} />
              <span className={classNames('relative inline-flex h-2 w-2 rounded-full', active.length ? 'bg-success' : 'bg-muted')} />
            </span>
            <h2 className="text-h3 text-text">Live routing</h2>
            <span className="rounded-pill border border-border bg-surface px-2 py-0.5 font-mono text-[10.5px] text-muted">
              {active.length ? `${active.length} in flight` : 'idle'}
            </span>
          </div>
          <p className="mt-0.5 text-[11.5px] text-muted">
            Setiap paket = satu request yang sedang memakai API key provider.
          </p>
        </div>
        <div className="flex items-center gap-3 text-[10.5px] text-muted">
          <span className="flex items-center gap-1"><span className="h-1.5 w-1.5 rounded-full bg-accent" /> request</span>
          <span className="flex items-center gap-1"><span className="h-1.5 w-1.5 rounded-full bg-success" /> sukses</span>
          <span className="flex items-center gap-1"><span className="h-1.5 w-1.5 rounded-full bg-danger" /> error</span>
        </div>
      </div>

      <div className="relative bg-surface/40">
        <svg viewBox={`0 0 ${W} ${H}`} className="h-[260px] w-full sm:h-[300px]" role="img" aria-label="Peta routing provider">
          <defs>
            <pattern id="live-dots" width="18" height="18" patternUnits="userSpaceOnUse">
              <circle cx="1" cy="1" r="1" fill="rgb(var(--c-border))" opacity="0.55" />
            </pattern>
          </defs>
          <rect width={W} height={H} fill="url(#live-dots)" />

          {nodes.map((p, i) => {
            const pos = positions[i];
            const count = activeByProvider.get(p.id) ?? 0;
            const hot = count > 0;
            return (
              <g key={p.id}>
                <path
                  d={`M ${CENTER.x} ${CENTER.y} C ${CENTER.x + (pos.x - CENTER.x) * 0.45} ${CENTER.y}, ${
                    CENTER.x + (pos.x - CENTER.x) * 0.55
                  } ${pos.y}, ${pos.x} ${pos.y}`}
                  fill="none"
                  stroke={hot ? 'rgb(var(--c-accent))' : 'rgb(var(--c-border))'}
                  strokeWidth={hot ? 1.8 : 1.2}
                  strokeDasharray={hot ? undefined : '4 4'}
                  opacity={hot ? 0.85 : 0.7}
                />
                {hot && (
                  <circle cx={pos.x} cy={pos.y} r={26} fill="none" stroke="rgb(var(--c-accent))" strokeWidth="1.2" opacity="0.5">
                    <animate attributeName="r" values="24;34;24" dur="2s" repeatCount="indefinite" />
                    <animate attributeName="opacity" values="0.45;0;0.45" dur="2s" repeatCount="indefinite" />
                  </circle>
                )}
                <g transform={`translate(${pos.x - 82}, ${pos.y - 24})`}>
                  <rect width="164" height="48" rx="10" fill="rgb(var(--c-bg))" stroke={hot ? 'rgb(var(--c-accent))' : 'rgb(var(--c-border))'} strokeWidth="1" />
                  <circle cx="14" cy="16" r="4" fill={p.status === 'connected' ? 'rgb(var(--c-success))' : p.status === 'degraded' || p.status === 'rate_limited' ? 'rgb(var(--c-warning))' : 'rgb(var(--c-muted))'} />
                  <text x="25" y="20" fontSize="11.5" fontWeight="600" fill="rgb(var(--c-text))">
                    {shortName(p.name)}
                  </text>
                  <text x="14" y="36" fontSize="9.5" fill="rgb(var(--c-muted))">
                    {p.type} · {p.enabled ? 'enabled' : 'disabled'}
                  </text>
                  {count > 0 && (
                    <g transform="translate(140, 10)">
                      <circle r="9" fill="rgb(var(--c-accent))" />
                      <text y="3.2" textAnchor="middle" fontSize="9.5" fontWeight="700" fill="#fff">
                        {count}
                      </text>
                    </g>
                  )}
                </g>
              </g>
            );
          })}

          {packets.map(({ req, path }) => (
            <circle key={req.request_id} r="4" fill={statusColor(req.status)} opacity="0.95">
              <animateMotion dur="1.5s" repeatCount="indefinite" path={path} rotate="auto" />
              <animate attributeName="opacity" values="0.25;1;0.25" dur="1.5s" repeatCount="indefinite" />
            </circle>
          ))}

          {/* Router node */}
          <g transform={`translate(${CENTER.x - 72}, ${CENTER.y - 30})`}>
            <rect width="144" height="60" rx="14" fill="rgb(var(--c-accent-soft))" stroke="rgb(var(--c-accent))" strokeWidth="1.4" />
            <text x="72" y="26" textAnchor="middle" fontSize="13" fontWeight="700" fill="rgb(var(--c-accent))">
              MyRouter
            </text>
            <text x="72" y="43" textAnchor="middle" fontSize="9.5" fill="rgb(var(--c-muted))">
              {active.length ? `${active.length} request aktif` : 'menunggu request'}
            </text>
          </g>

          {nodes.length === 0 && (
            <text x={W / 2} y={H - 26} textAnchor="middle" fontSize="11.5" fill="rgb(var(--c-muted))">
              Belum ada provider — tambahkan satu untuk melihat topologi.
            </text>
          )}
        </svg>
      </div>

      <div className="border-t border-border">
        <div className="flex items-center justify-between px-4 py-2">
          <span className="text-[11px] font-medium uppercase tracking-wide text-muted">Recent requests</span>
          <span className="font-mono text-[10.5px] text-muted">auto-refresh 2s</span>
        </div>
        {recent.length === 0 ? (
          <p className="px-4 pb-3 text-[12px] text-muted">Belum ada request. Panggil gateway untuk melihat animasi di atas.</p>
        ) : (
          <ul className="divide-y divide-border">
            {recent.slice(0, 5).map((r: LiveRequest) => (
              <li key={r.request_id} className="flex items-center justify-between gap-3 px-4 py-2 text-[12px]">
                <div className="flex min-w-0 items-center gap-2">
                  <span className="h-1.5 w-1.5 shrink-0 rounded-full" style={{ background: statusColor(r.status) }} />
                  <span className="truncate font-mono text-[11.5px] text-text">{r.route}</span>
                  <span className="text-muted">→</span>
                  <span className="truncate text-text">{r.provider_name || '—'}</span>
                </div>
                <div className="flex shrink-0 items-center gap-3 font-mono text-[10.5px] text-muted">
                  <span>{r.key_prefix || '—'}</span>
                  <span>{r.elapsed_ms}ms</span>
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
