import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { AlertTriangle, CheckCircle2, FlaskConical, Gauge, Loader2, Play, RefreshCw, Timer, XCircle, Zap } from 'lucide-react';
import { PageHeader, Card, CardHeader, Callout, StatCard } from '../components/ui/Layout';
import { Button } from '../components/ui/Button';
import { Badge, StatusBadge } from '../components/ui/Badge';
import { DataTable, type Column } from '../components/ui/DataTable';
import { Field, Input, Select } from '../components/ui/Field';
import { useToast } from '../components/ui/Toast';
import { admin, ApiError, streamPlayground } from '../lib/api';
import { formatNumber, primaryOf, fallbacksOf, routeHasUsableTarget } from '../lib/format';
import type { PlaygroundResult, Provider, Route, Status } from '../lib/types';

interface UpstreamHealth {
  status: string;
  message: string;
  latency_ms: number;
}

function statusTone(status: string): 'success' | 'danger' | 'warning' | 'neutral' {
  if (status === 'ok') return 'success';
  if (status === 'error' || status === 'blocked') return 'danger';
  if (status === 'partial') return 'warning';
  return 'neutral';
}

// A route alias is usually derived from its model id, so appending the model
// again produced labels like "glm-5.3 · glm-5.3". Show the model only when it
// adds information, and collapse the alias when the data is duplicated.
function routeOptionLabel(alias: string, model: string): string {
  const a = (alias || '').trim();
  let m = (model || '').trim();
  if (!m) return a;
  if (!a) return m;
  if (m === a) return a;
  if (m.startsWith(a)) {
    const rest = m.slice(a.length).replace(/^[\s·:/-]+/, '');
    if (!rest || rest === a) return a;
    m = rest;
  }
  // Collapse "a · a" style values produced by duplicated target metadata.
  if (m === a) return a;
  return `${a} · ${m}`;
}

function dedupeModel(alias: string, model: string): string {
  const a = (alias || '').trim();
  const m = (model || '').trim();
  return m === a ? '' : m;
}

export function PlaygroundPage() {
  const toast = useToast();
  const [routes, setRoutes] = useState<Route[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');

  const [route, setRoute] = useState('');
  const [prompt, setPrompt] = useState('Jelaskan dalam satu kalimat apa fungsi API gateway.');
  const [system, setSystem] = useState('');
  const [maxTokens, setMaxTokens] = useState('256');
  const [temperature, setTemperature] = useState('0.7');
  const [stream, setStream] = useState(true);

  const [running, setRunning] = useState(false);
  const [liveText, setLiveText] = useState('');
  const [result, setResult] = useState<PlaygroundResult | null>(null);
  const [runError, setRunError] = useState('');
  const [elapsed, setElapsed] = useState(0);

  const [comparing, setComparing] = useState(false);
  const [compareRows, setCompareRows] = useState<PlaygroundResult[]>([]);

  const [health, setHealth] = useState<Record<string, UpstreamHealth>>({});
  const [testingAll, setTestingAll] = useState(false);

  const abortRef = useRef<AbortController | null>(null);
  const tickRef = useRef<number | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [r, p] = await Promise.all([admin.routes.list(), admin.providers.list()]);
      const list = r.data ?? [];
      setRoutes(list);
      setProviders(p.data ?? []);
      setRoute((current) => current || list.find((x) => x.enabled && routeHasUsableTarget(x))?.alias || list[0]?.alias || '');
      setLoadError('');
    } catch (err) {
      setLoadError(err instanceof ApiError ? err.message : 'Could not load routes and providers');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => () => {
    abortRef.current?.abort();
    if (tickRef.current) window.clearInterval(tickRef.current);
  }, []);

  const callBody = useMemo(
    () => ({
      route,
      system: system.trim() || undefined,
      messages: [{ role: 'user' as const, content: prompt }],
      max_tokens: Math.max(1, Math.min(4096, Number(maxTokens) || 256)),
      temperature: Math.max(0, Math.min(2, Number(temperature) || 0)),
    }),
    [route, system, prompt, maxTokens, temperature],
  );

  // Only routes that can actually serve are offered here: a route whose
  // providers are all disabled would just fail, and it no longer exists for
  // clients either.
  const runnableRoutes = useMemo(
    () => routes.filter((r) => r.enabled && routeHasUsableTarget(r)),
    [routes],
  );

  // Group aliases by the provider behind their primary target. With many
  // providers a flat list of aliases is unreadable, because the alias alone
  // does not say which upstream and model it will hit.
  const routeGroups = useMemo(() => {
    const groups = new Map<string, { id: string; label: string; routes: Route[] }>();
    for (const r of runnableRoutes) {
      const primary = primaryOf(r);
      const id = primary?.provider_id || 'unassigned';
      const label = primary?.provider_name || 'Tanpa provider';
      const group = groups.get(id) ?? { id, label, routes: [] };
      group.routes.push(r);
      groups.set(id, group);
    }
    return Array.from(groups.values())
      .map((g) => ({ ...g, routes: [...g.routes].sort((a, b) => a.alias.localeCompare(b.alias)) }))
      .sort((a, b) => a.label.localeCompare(b.label));
  }, [runnableRoutes]);

  const selectedRoute = useMemo(() => routes.find((r) => r.alias === route) ?? null, [routes, route]);
  const selectedPrimary = selectedRoute ? primaryOf(selectedRoute) : undefined;
  const selectedModel = selectedPrimary?.model_name || selectedPrimary?.model_id || '';
  const selectedFallbacks = selectedRoute ? fallbacksOf(selectedRoute).length : 0;

  function startTimer() {
    const started = performance.now();
    setElapsed(0);
    if (tickRef.current) window.clearInterval(tickRef.current);
    tickRef.current = window.setInterval(() => setElapsed(Math.round(performance.now() - started)), 100);
  }
  function stopTimer() {
    if (tickRef.current) window.clearInterval(tickRef.current);
    tickRef.current = null;
  }

  async function runOnce() {
    if (!route) {
      setRunError('Pilih route terlebih dahulu.');
      return;
    }
    if (!prompt.trim()) {
      setRunError('Prompt tidak boleh kosong.');
      return;
    }
    setRunError('');
    setResult(null);
    setLiveText('');
    setRunning(true);
    startTimer();
    try {
      if (stream) {
        const controller = new AbortController();
        abortRef.current = controller;
        await streamPlayground(
          callBody,
          (ev) => {
            if (ev.type === 'delta' && ev.text) setLiveText((prev) => prev + ev.text);
            if (ev.type === 'error' && ev.error) setRunError(ev.error);
            if (ev.type === 'done' && ev.result) setResult(ev.result);
          },
          controller.signal,
        );
      } else {
        const res = await admin.playground.chat(callBody);
        setResult(res.data);
      }
    } catch (err) {
      if ((err as Error)?.name !== 'AbortError') {
        setRunError(err instanceof ApiError ? err.message : 'Test call failed');
      }
    } finally {
      setRunning(false);
      stopTimer();
      abortRef.current = null;
    }
  }

  function cancel() {
    abortRef.current?.abort();
    setRunning(false);
    stopTimer();
  }

  async function compareAll() {
    const aliases = runnableRoutes.map((r) => r.alias);
    if (aliases.length === 0) {
      setRunError('Tidak ada route aktif dengan provider yang siap untuk dibandingkan.');
      return;
    }
    setRunError('');
    setComparing(true);
    setCompareRows([]);
    const rows: PlaygroundResult[] = [];
    for (const alias of aliases) {
      try {
        const res = await admin.playground.chat({ ...callBody, route: alias });
        rows.push(res.data);
      } catch (err) {
        rows.push({
          route: alias, request_id: '', status: 'error', provider: '', provider_id: '', model: '',
          model_id: '', content: '', latency_ms: 0, ttft_ms: 0, in_tokens: 0, out_tokens: 0,
          fallback: false, error: err instanceof ApiError ? err.message : 'Request failed', trace: [],
        });
      }
      setCompareRows([...rows]);
    }
    setComparing(false);
    toast.success(`Tested ${rows.length} route(s)`);
  }

  async function testAllUpstreams() {
    // Disabled providers are not in rotation, so health-checking them is noise
    // (and the API rejects it).
    const targets = providers.filter((p) => p.enabled);
    if (targets.length === 0) {
      setRunError('Tidak ada provider aktif untuk diuji. Aktifkan provider dulu.');
      return;
    }
    setTestingAll(true);
    for (const p of targets) {
      try {
        const res = await admin.providers.test(p.id);
        setHealth((prev) => ({ ...prev, [p.id]: { status: res.status, message: res.message, latency_ms: res.latency_ms } }));
      } catch (err) {
        setHealth((prev) => ({
          ...prev,
          [p.id]: { status: 'unavailable', message: err instanceof ApiError ? err.message : 'Test failed', latency_ms: 0 },
        }));
      }
    }
    setTestingAll(false);
    toast.success('Upstream health checked');
  }

  const okCount = compareRows.filter((r) => r.status === 'ok').length;
  const fastest = compareRows
    .filter((r) => r.status === 'ok' && r.latency_ms > 0)
    .reduce<PlaygroundResult | null>((best, cur) => (!best || cur.latency_ms < best.latency_ms ? cur : best), null);

  const compareColumns: Column<PlaygroundResult>[] = [
    {
      key: 'route',
      header: 'Route',
      render: (r) => (
        <div className="flex items-center gap-2">
          <span className="font-mono text-[12px] text-text">{r.route}</span>
          {fastest?.route === r.route && <Badge tone="success" dot={false}>fastest</Badge>}
          {r.fallback && <Badge tone="info" dot={false}>fallback</Badge>}
        </div>
      ),
    },
    { key: 'status', header: 'Status', render: (r) => <Badge tone={statusTone(r.status)}>{r.status}</Badge> },
    {
      key: 'latency', header: 'Latency', align: 'right',
      render: (r) => <span className="tabular text-text">{r.latency_ms ? `${r.latency_ms} ms` : '—'}</span>,
    },
    {
      key: 'ttft', header: 'TTFT', align: 'right',
      render: (r) => <span className="tabular text-muted">{r.ttft_ms ? `${r.ttft_ms} ms` : '—'}</span>,
    },
    { key: 'provider', header: 'Provider', render: (r) => <span className="text-text">{r.provider || '—'}</span> },
    { key: 'model', header: 'Model', render: (r) => <span className="font-mono text-[11.5px] text-muted">{r.model || '—'}</span> },
    {
      key: 'tokens', header: 'Tokens', align: 'right',
      render: (r) => <span className="tabular text-muted">{r.in_tokens || r.out_tokens ? `${r.in_tokens} / ${r.out_tokens}` : '—'}</span>,
    },
    {
      key: 'preview', header: 'Response', className: 'max-w-[280px]',
      render: (r) => (
        <span className="block truncate text-[11.5px] text-muted" title={r.error || r.content}>
          {r.error || r.content || '—'}
        </span>
      ),
    },
  ];

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Test Lab"
        description="Kirim prompt lewat pipeline yang sama dengan gateway, ukur latency/TTFT, dan pastikan setiap route benar-benar menjawab."
        actions={
          <>
            <Button variant="secondary" size="compact" icon={<RefreshCw size={14} />} onClick={() => void load()} loading={loading}>
              Reload
            </Button>
            <Button variant="primary" size="compact" icon={<Play size={14} />} onClick={() => void runOnce()} loading={running}>
              Run test
            </Button>
          </>
        }
      />

      {loadError && <Callout tone="danger">{loadError}</Callout>}
      {routes.length === 0 && !loading && (
        <Callout tone="warning">
          Belum ada route. Tambahkan provider, refresh model, lalu buat route sebelum memakai Test Lab.
        </Callout>
      )}
      {routes.length > 0 && runnableRoutes.length === 0 && !loading && (
        <Callout tone="warning">
          Tidak ada route yang bisa diuji: semua provider targetnya sedang nonaktif. Aktifkan provider di halaman Providers.
        </Callout>
      )}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
        <Card className="lg:col-span-5">
          <CardHeader title="Request" description="Pilih route (alias), lalu kirim prompt." />
          <div className="flex flex-col gap-3">
            <Field label="Route alias" required hint="Dikelompokkan per provider, dengan model yang dipakai, supaya route mirip tetap bisa dibedakan.">
              <Select value={route} onChange={(e) => setRoute(e.target.value)}>
                {runnableRoutes.length === 0 && <option value="">No runnable routes</option>}
                {routeGroups.map((g) => (
                  <optgroup key={g.id} label={g.label}>
                    {g.routes.map((r) => {
                      const primary = primaryOf(r);
                      const model = primary?.model_name || primary?.model_id || '';
                      return (
                        <option key={r.id} value={r.alias}>
                          {routeOptionLabel(r.alias, model)}{r.enabled ? '' : ' (disabled)'}
                        </option>
                      );
                    })}
                  </optgroup>
                ))}
              </Select>
            </Field>
            {selectedRoute && (
              <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted">
                <span className="rounded-control border border-border bg-surface px-1.5 py-0.5 font-mono">
                  {selectedPrimary?.provider_name || 'Tanpa provider'}
                </span>
                {selectedModel && dedupeModel(selectedRoute.alias, selectedModel) && (
                  <span className="rounded-control border border-border bg-surface px-1.5 py-0.5 font-mono">{dedupeModel(selectedRoute.alias, selectedModel)}</span>
                )}
                <span>{selectedFallbacks ? `${selectedFallbacks} fallback` : 'tanpa fallback'}</span>
                {!selectedRoute.enabled && <span className="font-medium text-warning">route nonaktif</span>}
              </div>
            )}
            <Field label="Prompt" required>
              <textarea
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                rows={5}
                className="w-full rounded-control border border-border bg-bg px-3 py-2 text-[13px] text-text transition-colors placeholder:text-muted hover:border-muted/50 focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent/20"
                placeholder="Tulis prompt uji…"
              />
            </Field>
            <Field label="System prompt" hint="Opsional.">
              <Input value={system} onChange={(e) => setSystem(e.target.value)} placeholder="You are a concise assistant." />
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Max tokens">
                <Input type="number" min={1} max={4096} value={maxTokens} onChange={(e) => setMaxTokens(e.target.value)} />
              </Field>
              <Field label="Temperature">
                <Input type="number" min={0} max={2} step={0.1} value={temperature} onChange={(e) => setTemperature(e.target.value)} />
              </Field>
            </div>
            <label className="flex items-center gap-2 text-[12.5px] text-text">
              <input
                type="checkbox"
                checked={stream}
                onChange={(e) => setStream(e.target.checked)}
                className="h-4 w-4 rounded border-border text-accent focus:ring-accent"
              />
              Streaming (ukur time-to-first-token)
            </label>
            <div className="flex flex-wrap items-center gap-2">
              <Button variant="primary" icon={<Zap size={14} />} onClick={() => void runOnce()} loading={running}>
                Run test
              </Button>
              {running && (
                <Button variant="secondary" onClick={cancel}>
                  Stop
                </Button>
              )}
              <Button variant="ghost" icon={<Gauge size={14} />} onClick={() => void compareAll()} loading={comparing}>
                Compare all routes
              </Button>
            </div>
            {runError && <p className="text-[12px] text-danger" role="alert">{runError}</p>}
            <p className="text-[11px] text-muted">
              Panggilan Test Lab memakai key sintetis: tidak memakai kuota API key client dan tidak dicatat di Activity.
              Kuota level provider tetap dihitung.
            </p>
          </div>
        </Card>

        <Card className="lg:col-span-7">
          <CardHeader
            title="Result"
            description={result ? `Request ${result.request_id}` : 'Hasil pengujian muncul di sini.'}
            actions={
              result ? <Badge tone={statusTone(result.status)} dot>{result.status}</Badge> : undefined
            }
          />
          {running && !result && (
            <div className="mb-3 flex items-center gap-2 text-[12.5px] text-muted">
              <Loader2 size={14} className="animate-spin" />
              Menunggu respons… {(elapsed / 1000).toFixed(1)}s
            </div>
          )}
          {result ? (
            <div className="flex flex-col gap-3">
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                <StatCard label="Latency" value={result.latency_ms ? `${result.latency_ms} ms` : '—'} hint="total" icon={<Timer size={14} />} />
                <StatCard label="TTFT" value={result.ttft_ms ? `${result.ttft_ms} ms` : '—'} hint="first token" />
                <StatCard label="Tokens" value={formatNumber(result.in_tokens + result.out_tokens)} hint={`${result.in_tokens} in / ${result.out_tokens} out`} />
                <StatCard label="Provider" value={result.provider || '—'} hint={result.model || ''} tone={result.status === 'ok' ? 'success' : 'danger'} />
              </div>
              {result.error && (
                <Callout tone="danger" icon={<AlertTriangle size={14} />}>
                  {result.error}{result.category ? ` (${result.category})` : ''}
                </Callout>
              )}
              <div className="rounded-control border border-border bg-surface p-3">
                <div className="mb-1 flex items-center justify-between text-[11px] text-muted">
                  <span className="font-medium uppercase tracking-wide">Response</span>
                  {result.fallback && <Badge tone="info" dot={false}>fallback used</Badge>}
                </div>
                <pre className="max-h-[280px] overflow-auto whitespace-pre-wrap break-words font-mono text-[12px] leading-relaxed text-text">
                  {result.content || liveText || (result.status === 'ok' ? '(empty response)' : '—')}
                </pre>
              </div>
              {result.trace.length > 0 && (
                <div>
                  <p className="mb-1.5 text-[11px] font-medium uppercase tracking-wide text-muted">Routing trace</p>
                  <ol className="flex flex-col gap-1.5">
                    {result.trace.map((step, i) => (
                      <li key={`${step.attempt}-${i}`} className="flex items-start gap-2 rounded-control border border-border bg-bg px-2.5 py-1.5 text-[11.5px]">
                        {step.outcome === 'success' ? (
                          <CheckCircle2 size={13} className="mt-0.5 shrink-0 text-success" />
                        ) : (
                          <XCircle size={13} className="mt-0.5 shrink-0 text-danger" />
                        )}
                        <div className="min-w-0">
                          <span className="font-mono text-text">#{step.attempt} {step.target}</span>
                          <span className="text-muted"> · {step.provider || '—'} · {step.outcome}</span>
                          {step.latency_ms > 0 && <span className="text-muted"> · {step.latency_ms} ms</span>}
                          {step.message && <p className="truncate text-muted">{step.message}</p>}
                        </div>
                      </li>
                    ))}
                  </ol>
                </div>
              )}
            </div>
          ) : (
            <div className="flex flex-col items-center gap-2 py-14 text-center">
              <FlaskConical size={22} className="text-muted" />
              <p className="text-[13px] font-medium text-text">Belum ada hasil</p>
              <p className="max-w-sm text-[12px] text-muted">
                Pilih route lalu klik <strong>Run test</strong>. Hasil berisi latency, TTFT, token, provider/model yang dipakai, dan trace fallback.
              </p>
            </div>
          )}
        </Card>
      </div>

      <Card>
        <CardHeader
          title="Route comparison"
          description={compareRows.length ? `${okCount}/${compareRows.length} route berhasil` : 'Jalankan semua route aktif dengan prompt yang sama.'}
          actions={
            <Button variant="secondary" size="compact" icon={<Gauge size={14} />} onClick={() => void compareAll()} loading={comparing}>
              Compare all
            </Button>
          }
        />
        <DataTable
          columns={compareColumns}
          rows={compareRows}
          rowKey={(r) => `${r.route}-${r.request_id}`}
          loading={comparing && compareRows.length === 0}
          empty={<p className="py-8 text-center text-[12.5px] text-muted">Belum ada perbandingan.</p>}
        />
      </Card>

      <Card>
        <CardHeader
          title="Upstream health"
          description="Tes koneksi setiap provider (endpoint + credential) dan lihat latency-nya."
          actions={
            <Button variant="secondary" size="compact" icon={<RefreshCw size={14} />} onClick={() => void testAllUpstreams()} loading={testingAll}>
              Test all upstreams
            </Button>
          }
        />
        {providers.length === 0 ? (
          <p className="py-6 text-center text-[12.5px] text-muted">Belum ada provider.</p>
        ) : (
          <ul className="divide-y divide-border">
            {providers.map((p) => {
              const h = health[p.id];
              return (
                <li key={p.id} className="flex flex-wrap items-center justify-between gap-2 py-2.5">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-[13px] font-medium text-text">{p.name}</span>
                    <span className="rounded-control border border-border bg-surface px-1.5 py-0.5 text-[10.5px] text-muted">{p.type}</span>
                  </div>
                  <div className="flex items-center gap-3">
                    {!p.enabled ? (
                      <Badge tone="neutral" dot={false}>Disabled</Badge>
                    ) : h ? (
                      <>
                        <StatusBadge spec={{ label: h.status.replace('_', ' '), tone: h.status === 'connected' ? 'success' : h.status === 'rate_limited' || h.status === 'degraded' ? 'warning' : 'danger' }} />
                        <span className="font-mono text-[11px] text-muted">{h.latency_ms} ms</span>
                        {h.message && <span className="max-w-[220px] truncate text-[11px] text-muted" title={h.message}>{h.message}</span>}
                      </>
                    ) : (
                      <StatusBadge spec={{ label: p.status as Status, tone: p.status === 'connected' ? 'success' : p.status === 'degraded' ? 'warning' : 'neutral' }} />
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </Card>
    </div>
  );
}
