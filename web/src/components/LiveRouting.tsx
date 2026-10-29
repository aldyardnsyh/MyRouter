import { useEffect, useMemo, useRef, useState } from 'react';
import type { LiveRequest, LiveSnapshot, Provider } from '../lib/types';
import { classNames } from '../lib/format';

interface Props {
  providers: Provider[];
  live?: LiveSnapshot;
}

function usePrefersReducedMotion() {
  const [reduced, setReduced] = useState(false);
  useEffect(() => {
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)');
    const sync = () => setReduced(mq.matches);
    sync();
    mq.addEventListener('change', sync);
    return () => mq.removeEventListener('change', sync);
  }, []);
  return reduced;
}

function statusColor(status: string) {
  switch (status) {
    case 'ok': return 'rgb(var(--c-success))';
    case 'error':
    case 'blocked': return 'rgb(var(--c-danger))';
    default: return 'rgb(var(--c-accent))';
  }
}

interface Node { p: Provider; angle: number; radius: number; x: number; y: number; count: number }
interface Particle { id: string; nodeIdx: number; t: number; speed: number; status: string; wobble: number }
interface Spark { x: number; y: number; vx: number; vy: number; life: number; max: number; color: string }
interface Dust { x: number; y: number; vx: number; vy: number; r: number }

const liveStyle = `
@keyframes mrl-recent-in { from { transform: translateX(-8px); opacity: 0; } to { transform: translateX(0); opacity: 1; } }
.mrl-recent-first { animation: mrl-recent-in 250ms ease-out; }
@media (prefers-reduced-motion: reduce) { .mrl-recent-first { animation: none; } }`;

export function LiveRoutingCard({ providers, live }: Props) {
  const active = live?.active ?? [];
  const recent = live?.recent ?? [];
  const reducedMotion = usePrefersReducedMotion();
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [size, setSize] = useState({ w: 760, h: 300 });

  const { enabledProviders, hiddenCount } = useMemo(() => {
    const enabled = providers.filter((p) => p.enabled);
    return { enabledProviders: enabled, hiddenCount: providers.length - enabled.length };
  }, [providers]);
  const nodes0 = useMemo(() => enabledProviders.slice(0, 6), [enabledProviders]);

  const activeByProvider = useMemo(() => {
    const map = new Map<string, number>();
    for (const req of active) if (req.provider_id) map.set(req.provider_id, (map.get(req.provider_id) ?? 0) + 1);
    return map;
  }, [active]);

  // ResizeObserver + DPR
  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => {
      const r = el.getBoundingClientRect();
      setSize({ w: Math.max(280, r.width), h: window.innerWidth < 640 ? 260 : 300 });
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const nodesRef = useRef<Node[]>([]);
  const partsRef = useRef<Particle[]>([]);
  const sparksRef = useRef<Spark[]>([]);
  const dustRef = useRef<Dust[]>([]);
  const prevRecentRef = useRef<string>('');
  const selectedRef = useRef<string | null>(null);
  selectedRef.current = selected;

  // Init nodes layout (orbit angles evenly, radius by status)
  const layout = useMemo(() => nodes0.map((p, i) => ({
    p,
    angle: (i / Math.max(1, nodes0.length)) * Math.PI * 2 - Math.PI / 2,
    count: activeByProvider.get(p.id) ?? 0,
  })), [nodes0, activeByProvider]);

  // Sync particles from active requests
  useEffect(() => {
    const capped = active.slice(0, 8);
    partsRef.current = capped.map((req, i) => {
      const ni = Math.max(0, nodes0.findIndex((n) => n.id === req.provider_id));
      const existing = partsRef.current.find((pt) => pt.id === req.request_id);
      return existing ?? { id: req.request_id, nodeIdx: ni, t: (i * 0.13) % 1, speed: 0.008 + (i % 5) * 0.002, status: req.status, wobble: Math.random() * 6.28 };
    });
  }, [active, nodes0]);

  // Burst on new successful recent
  useEffect(() => {
    const first = recent[0];
    if (!first || first.status !== 'ok') { prevRecentRef.current = first?.request_id ?? ''; return; }
    if (prevRecentRef.current && prevRecentRef.current !== first.request_id) {
      const ni = nodes0.findIndex((n) => n.id === first.provider_id);
      if (ni >= 0 && nodesRef.current[ni]) {
        const { x, y } = nodesRef.current[ni];
        const n = 8 + Math.floor(Math.random() * 5);
        for (let k = 0; k < n; k++) {
          const a = (k / n) * Math.PI * 2 + Math.random() * 0.4;
          const sp = 0.6 + Math.random() * 1.4;
          sparksRef.current.push({ x, y, vx: Math.cos(a) * sp, vy: Math.sin(a) * sp, life: 0, max: 24, color: 'rgb(var(--c-success))' });
        }
      }
    }
    prevRecentRef.current = first?.request_id ?? '';
  }, [recent, nodes0]);

  // Canvas render loop
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    canvas.width = size.w * dpr;
    canvas.height = size.h * dpr;
    canvas.style.width = `${size.w}px`;
    canvas.style.height = `${size.h}px`;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

    const cx = size.w / 2, cy = size.h / 2;
    const baseR = Math.min(size.w, size.h) * 0.36;

    if (dustRef.current.length === 0) {
      for (let i = 0; i < 40; i++) dustRef.current.push({ x: Math.random() * size.w, y: Math.random() * size.h, vx: (Math.random() - 0.5) * 0.15, vy: (Math.random() - 0.5) * 0.15, r: 0.6 + Math.random() * 1.2 });
    }

    const css = (v: string) => getComputedStyle(document.documentElement).getPropertyValue(v).trim();
    const col = (v: string, fb: string) => { const c = css(v); return c ? `rgb(${c})` : fb; };

    const drawFrame = (time: number) => {
      // node positions
      nodesRef.current = layout.map((L) => {
        const far = L.p.status !== 'connected';
        const r = far ? baseR + 34 : baseR - 14 + (L.count > 0 ? -6 : 0);
        const drift = reducedMotion ? 0 : time * 0.00006 * (far ? 1 : -1);
        const a = L.angle + drift;
        return { p: L.p, angle: a, radius: r, x: cx + Math.cos(a) * r * 1.35, y: cy + Math.sin(a) * r * 0.72, count: L.count };
      });

      ctx.clearRect(0, 0, size.w, size.h);
      // bg dots (subtle, static)
      ctx.fillStyle = col('--c-border', '#e5e5e5');
      ctx.globalAlpha = 0.35;
      for (let gx = 8; gx < size.w; gx += 22) for (let gy = 8; gy < size.h; gy += 22) { ctx.fillRect(gx, gy, 1, 1); }
      ctx.globalAlpha = 1;

      const breathe = reducedMotion ? 0 : Math.sin(time * 0.0016);
      const coreR = 26 + breathe * 2.5;

      // orbit rings (2, opposite directions) — accent at low alpha, matte
      const rings = [
        { r: baseR, a0: reducedMotion ? 0 : time * 0.00022, alpha: 0.28 },
        { r: baseR + 34, a0: reducedMotion ? 1 : -time * 0.00015, alpha: 0.18 },
      ];
      for (const ring of rings) {
        ctx.strokeStyle = col('--c-accent', '#6366f1');
        ctx.globalAlpha = ring.alpha;
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.ellipse(cx, cy, ring.r * 1.35, ring.r * 0.72, 0, 0, Math.PI * 2);
        ctx.stroke();
        // small marker dot on ring to show rotation
        const mx = cx + Math.cos(ring.a0) * ring.r * 1.35, my = cy + Math.sin(ring.a0) * ring.r * 0.72;
        ctx.globalAlpha = 0.7;
        ctx.beginPath(); ctx.arc(mx, my, 2, 0, Math.PI * 2); ctx.fillStyle = col('--c-accent', '#6366f1'); ctx.fill();
        ctx.globalAlpha = 1;
      }

      // edges core -> node (selected highlighted, hot accent, idle faint)
      for (const n of nodesRef.current) {
        const isSel = selectedRef.current === n.p.id;
        const hot = n.count > 0;
        ctx.strokeStyle = isSel || hot ? col('--c-accent', '#6366f1') : col('--c-border', '#ccc');
        ctx.globalAlpha = isSel ? 0.9 : hot ? 0.55 : 0.4;
        ctx.lineWidth = isSel ? 1.8 : 1.1;
        ctx.setLineDash(hot || isSel ? [3, 7] : [3, 5]);
        ctx.beginPath(); ctx.moveTo(cx, cy); ctx.quadraticCurveTo(cx, n.y, n.x, n.y); ctx.stroke();
        ctx.setLineDash([]);
        ctx.globalAlpha = 1;
      }

      // core: solid accent disc + ring (single glow element only)
      ctx.save();
      ctx.shadowColor = col('--c-accent', '#6366f1');
      ctx.shadowBlur = 18;
      ctx.fillStyle = col('--c-accent', '#6366f1');
      ctx.beginPath(); ctx.arc(cx, cy, coreR * 0.55, 0, Math.PI * 2); ctx.fill();
      ctx.restore();
      ctx.strokeStyle = col('--c-accent', '#6366f1');
      ctx.lineWidth = 1.5;
      ctx.beginPath(); ctx.arc(cx, cy, coreR, 0, Math.PI * 2); ctx.stroke();
      ctx.fillStyle = col('--c-text', '#111');
      ctx.font = '700 12px system-ui';
      ctx.textAlign = 'center';
      ctx.fillText('MyRouter', cx, cy - coreR - 8);
      ctx.font = '10px system-ui';
      ctx.fillStyle = col('--c-muted', '#888');
      ctx.fillText(active.length ? `${active.length} aktif` : 'idle', cx, cy + coreR + 14);

      // particles: core -> node with trail
      if (!reducedMotion) {
        ctx.fillStyle = 'rgba(0,0,0,0)';
        // afterimage trail: fade previous frame slightly (works on light bg via low-alpha bg fill)
        ctx.fillStyle = getComputedStyle(document.body).backgroundColor || 'rgba(255,255,255,0.08)';
        ctx.globalAlpha = 0.18;
        // skip full fade; particles draw bright dots instead
        ctx.globalAlpha = 1;
      }
      for (const pt of partsRef.current) {
        if (!reducedMotion) { pt.t += pt.speed; if (pt.t > 1) pt.t = 0; }
        const n = nodesRef.current[Math.min(pt.nodeIdx, nodesRef.current.length - 1)];
        if (!n) continue;
        const t = pt.t;
        const px = (1 - t) * (1 - t) * cx + 2 * (1 - t) * t * cx + t * t * n.x;
        const py = (1 - t) * (1 - t) * cy + 2 * (1 - t) * t * (n.y) + t * t * n.y;
        let shakeX = 0;
        if (pt.status === 'error' || pt.status === 'blocked') shakeX = reducedMotion ? 0 : Math.sin(time * 0.05 + pt.wobble) * 2.5;
        const isOk = pt.status === 'ok';
        ctx.fillStyle = isOk ? col('--c-success', '#22c55e') : (pt.status === 'error' || pt.status === 'blocked') ? col('--c-danger', '#ef4444') : col('--c-accent', '#6366f1');
        ctx.beginPath(); ctx.arc(px + shakeX, py, isOk ? 3.4 : 3, 0, Math.PI * 2); ctx.fill();
        // trail dot
        const t2 = Math.max(0, t - 0.06);
        const qx = (1 - t2) * (1 - t2) * cx + 2 * (1 - t2) * t2 * cx + t2 * t2 * n.x;
        const qy = (1 - t2) * (1 - t2) * cy + 2 * (1 - t2) * t2 * n.y + t2 * t2 * n.y;
        ctx.globalAlpha = 0.35;
        ctx.beginPath(); ctx.arc(qx + shakeX, qy, 1.8, 0, Math.PI * 2); ctx.fill();
        ctx.globalAlpha = 1;
      }

      // sparks (burst fade 400ms ~ 24 frames)
      sparksRef.current = sparksRef.current.filter((s) => s.life < s.max);
      for (const s of sparksRef.current) {
        s.life++; s.x += s.vx; s.y += s.vy;
        ctx.globalAlpha = Math.max(0, 1 - s.life / s.max);
        ctx.fillStyle = s.color;
        ctx.beginPath(); ctx.arc(s.x, s.y, 2, 0, Math.PI * 2); ctx.fill();
        ctx.globalAlpha = 1;
      }

      // nodes (satellites)
      for (const n of nodesRef.current) {
        const isSel = selectedRef.current === n.p.id;
        ctx.fillStyle = col('--c-bg', '#fff');
        ctx.strokeStyle = isSel || n.count > 0 ? col('--c-accent', '#6366f1') : col('--c-border', '#ccc');
        ctx.lineWidth = isSel ? 2 : 1.2;
        const w = 118, h = 34;
        ctx.beginPath();
        if (typeof ctx.roundRect === 'function') ctx.roundRect(n.x - w / 2, n.y - h / 2, w, h, 8); else ctx.rect(n.x - w / 2, n.y - h / 2, w, h);
        ctx.fill(); ctx.stroke();
        // status dot
        const dot = n.p.status === 'connected' ? col('--c-success', '#22c55e') : (n.p.status === 'degraded' || n.p.status === 'rate_limited') ? col('--c-warning', '#f59e0b') : col('--c-muted', '#999');
        ctx.fillStyle = dot;
        ctx.beginPath(); ctx.arc(n.x - w / 2 + 12, n.y - 6, 3.5, 0, Math.PI * 2); ctx.fill();
        ctx.fillStyle = col('--c-text', '#111');
        ctx.font = '600 10.5px system-ui';
        const nm = n.p.name.length > 14 ? n.p.name.slice(0, 13) + '…' : n.p.name;
        ctx.fillText(nm, n.x + 4, n.y - 2);
        ctx.font = '9px system-ui';
        ctx.fillStyle = col('--c-muted', '#888');
        ctx.fillText(`${n.p.type} · ${n.count}`, n.x, n.y + 11);
        if (n.count > 0) {
          ctx.fillStyle = col('--c-accent', '#6366f1');
          ctx.beginPath(); ctx.arc(n.x + w / 2 - 8, n.y - h / 2 + 8, 8, 0, Math.PI * 2); ctx.fill();
          ctx.fillStyle = '#fff'; ctx.font = '700 9px system-ui';
          ctx.fillText(String(n.count), n.x + w / 2 - 8, n.y - h / 2 + 11);
        }
      }

      // ambient dust
      if (!reducedMotion) {
        ctx.fillStyle = col('--c-muted', '#999');
        for (const d of dustRef.current) {
          d.x += d.vx; d.y += d.vy;
          if (d.x < 0) d.x = size.w; if (d.x > size.w) d.x = 0;
          if (d.y < 0) d.y = size.h; if (d.y > size.h) d.y = 0;
          ctx.globalAlpha = 0.25;
          ctx.beginPath(); ctx.arc(d.x, d.y, d.r, 0, Math.PI * 2); ctx.fill();
        }
        ctx.globalAlpha = 1;
      }
    };

    if (reducedMotion) {
      drawFrame(0);
      return;
    }
    let raf = 0;
    const loop = (t: number) => { drawFrame(t); raf = requestAnimationFrame(loop); };
    raf = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(raf);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [size, layout, reducedMotion, active.length]);

  const onMove = (e: React.MouseEvent) => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const r = canvas.getBoundingClientRect();
    const mx = e.clientX - r.left, my = e.clientY - r.top;
    const hit = nodesRef.current.find((n) => Math.abs(mx - n.x) < 59 && Math.abs(my - n.y) < 17);
    setHover(hit ? hit.p.id : null);
    canvas.style.cursor = hit ? 'pointer' : 'default';
  };

  const hoverNode = hover ? nodesRef.current.find((n) => n.p.id === hover) : undefined;

  return (
    <div className="overflow-hidden rounded-card border border-border bg-bg">
      <style>{liveStyle}</style>
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
            {hiddenCount > 0 && (
              <span className="rounded-pill border border-border bg-surface px-2 py-0.5 text-[10.5px] text-muted" title="Provider nonaktif tidak menerima trafik, jadi tidak digambar di peta.">
                {hiddenCount} disabled disembunyikan
              </span>
            )}
          </div>
          <p className="mt-0.5 text-[11.5px] text-muted">Warp core: partikel = request aktif menuju satelit provider.</p>
        </div>
        <div className="flex items-center gap-3 text-[10.5px] text-muted">
          <span className="flex items-center gap-1"><span className="h-1.5 w-1.5 rounded-full bg-accent" /> request</span>
          <span className="flex items-center gap-1"><span className="h-1.5 w-1.5 rounded-full bg-success" /> sukses</span>
          <span className="flex items-center gap-1"><span className="h-1.5 w-1.5 rounded-full bg-danger" /> error</span>
        </div>
      </div>

      <div ref={wrapRef} className="relative bg-surface/40">
        <canvas ref={canvasRef} className="block w-full" role="img" aria-label="Warp core routing providers aktif" onMouseMove={onMove} onMouseLeave={() => setHover(null)} onClick={() => setSelected(hover)} />
        {hoverNode && (
          <div className="pointer-events-none absolute rounded-md border border-border bg-bg px-2 py-1 text-[11px] text-text shadow" style={{ left: Math.min(hoverNode.x + 14, size.w - 150), top: hoverNode.y - 34 }}>
            {hoverNode.p.name} · {hoverNode.count} aktif
          </div>
        )}
        {nodes0.length === 0 && (
          <p className="absolute inset-x-0 bottom-3 text-center text-[11.5px] text-muted">
            {providers.length === 0 ? 'Belum ada provider. Tambahkan satu untuk melihat warp core.' : 'Semua provider dinonaktifkan.'}
          </p>
        )}
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
            {recent.slice(0, 5).map((r: LiveRequest, ri: number) => (
              <li key={r.request_id} className={ri === 0 ? 'mrl-recent-first flex items-center justify-between gap-3 px-4 py-2 text-[12px]' : 'flex items-center justify-between gap-3 px-4 py-2 text-[12px]'}>
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
