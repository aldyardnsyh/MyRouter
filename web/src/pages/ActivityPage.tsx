import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Download, Filter, ScrollText, Timer, Zap } from 'lucide-react';
import { PageHeader, Card, CardHeader, StatCard } from '../components/ui/Layout';
import { Badge, StatusBadge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { DataTable, Pagination, Tabs, type Column } from '../components/ui/DataTable';
import { Drawer } from '../components/ui/Overlay';
import { Field, Input, Select } from '../components/ui/Field';
import { Callout } from '../components/ui/Layout';
import { admin, ApiError } from '../lib/api';
import type { ActivityEntry, Metrics, Provider, Route, UsageRow } from '../lib/types';
import {
  activityBadge,
  formatCompact,
  formatCost,
  formatDateTime,
  formatLatency,
  formatNumber,
} from '../lib/format';

const PAGE_SIZE = 25;

export function ActivityPage() {
  const [tab, setTab] = useState('requests');
  const [routes, setRoutes] = useState<Route[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);

  useEffect(() => {
    void admin.routes.list().then((r) => setRoutes(r.data ?? [])).catch(() => setRoutes([]));
    void admin.providers.list().then((p) => setProviders(p.data ?? [])).catch(() => setProviders([]));
  }, []);

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Activity"
        description="Gateway request logs, fallback traces, and token consumption telemetry."
        actions={
          <Badge tone="success" dot>
            live
          </Badge>
        }
      />
      <Tabs
        tabs={[{ key: 'requests', label: 'Requests' }, { key: 'usage', label: 'Usage' }]}
        active={tab}
        onChange={setTab}
      />
      {tab === 'requests' ? <RequestsTab routes={routes} providers={providers} /> : <UsageTab />}
    </div>
  );
}

function RequestsTab({ routes, providers }: { routes: Route[]; providers: Provider[] }) {
  const [rows, setRows] = useState<ActivityEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [routeFilter, setRouteFilter] = useState('');
  const [providerFilter, setProviderFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState('');
  const [range, setRange] = useState('24h');
  const [search, setSearch] = useState('');
  const [debouncedSearch, setDebouncedSearch] = useState('');
  const [selected, setSelected] = useState<ActivityEntry | null>(null);
  const [metrics, setMetrics] = useState<Metrics | null>(null);
  const seq = useRef(0);

  // Debounce typing so a 6-character query does not fire 6 requests and
  // repaint the table 6 times.
  useEffect(() => {
    const t = window.setTimeout(() => setDebouncedSearch(search), 300);
    return () => window.clearTimeout(t);
  }, [search]);

  const load = useCallback(async (silent = false) => {
    const requestId = ++seq.current;
    if (!silent) setLoading(true);
    try {
      const res = await admin.activity.list({
        route: routeFilter,
        provider: providerFilter,
        status: statusFilter,
        range,
        q: debouncedSearch,
        limit: PAGE_SIZE,
        offset,
      });
      // Drop out-of-order responses: typing fast must not show stale results.
      if (requestId !== seq.current) return;
      setRows(res.data ?? []);
      setTotal(res.total);
      setError(null);
    } catch (err) {
      if (requestId !== seq.current) return;
      setError(err instanceof ApiError ? err.message : 'Failed to load activity');
    } finally {
      if (requestId === seq.current) setLoading(false);
    }
  }, [routeFilter, providerFilter, statusFilter, range, debouncedSearch, offset]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    void admin.usage('route', 1).then((r) => setMetrics(r.summary)).catch(() => undefined);
  }, []);

  const detail = useCallback(async (row: ActivityEntry) => {
    setSelected(row);
    try {
      const res = await admin.activity.get(row.id);
      setSelected(res.data);
    } catch {
      /* keep the row-level data */
    }
  }, []);

  const columns: Column<ActivityEntry>[] = [
    { key: 'time', header: 'Time', render: (r) => <span className="font-mono text-[11.5px] text-muted">{formatDateTime(r.ts)}</span> },
    {
      key: 'route',
      header: 'Route',
      render: (r) => (
        <span className="rounded-control border border-border bg-surface px-1.5 py-0.5 font-mono text-[11px] font-semibold text-text">
          {r.route_alias || '—'}
        </span>
      ),
    },
    { key: 'requested', header: 'Requested model', render: (r) => <span className="font-mono text-[11.5px] text-text">{r.requested_model || '—'}</span> },
    {
      key: 'used',
      header: 'Used model',
      render: (r) =>
        r.model_name ? (
          <span className={r.fallback_from ? 'font-mono text-[11.5px] font-medium text-accent' : 'font-mono text-[11.5px] text-text'}>{r.model_name}</span>
        ) : (
          <span className="text-[11.5px] italic text-muted">None</span>
        ),
    },
    {
      key: 'provider',
      header: 'Provider',
      render: (r) => (
        <div className="flex items-center gap-1.5">
          <span className={`h-1.5 w-1.5 rounded-full ${r.status === 'ok' ? 'bg-success' : r.status === 'error' ? 'bg-danger' : 'bg-muted'}`} />
          <span className="text-[12px] text-text">{r.provider_name || '—'}</span>
        </div>
      ),
    },
    { key: 'status', header: 'Status', render: (r) => <StatusBadge spec={activityBadge(r)} /> },
    { key: 'latency', header: 'Latency', align: 'right', render: (r) => <span className="font-mono text-[11.5px] text-muted">{formatLatency(r.latency_ms)}</span> },
    { key: 'tokens', header: 'Tokens', align: 'right', render: (r) => <span className="font-mono text-[11.5px] text-text">{formatNumber(r.in_tokens + r.out_tokens)}</span> },
    { key: 'key', header: 'Key', render: (r) => <span className="font-mono text-[11px] text-muted">{r.api_key_prefix ? `${r.api_key_prefix}****` : '—'}</span> },
    { key: 'ip', header: 'IP', render: (r) => <span className="font-mono text-[11px] text-muted">{r.ip || '—'}</span> },
  ];

  function exportCSV() {
    const header = ['time', 'route', 'requested_model', 'used_model', 'provider', 'status', 'latency_ms', 'tokens', 'key', 'ip'];
    const lines = rows.map((r) =>
      [formatDateTime(r.ts), r.route_alias, r.requested_model, r.model_name, r.provider_name, r.status, r.latency_ms, r.in_tokens + r.out_tokens, r.api_key_prefix, r.ip]
        .map((v) => `"${String(v ?? '').replace(/"/g, '""')}"`)
        .join(','),
    );
    const blob = new Blob([[header.join(','), ...lines].join('\n')], { type: 'text/csv' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'myrouter-activity.csv';
    a.click();
    URL.revokeObjectURL(url);
  }

  return (
    <div className="flex flex-col gap-4">
      {metrics && (
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <StatCard label="Total Requests" value={formatNumber(metrics.requests)} hint={range === '1h' ? 'last hour' : range === '7d' ? 'last 7 days' : 'last 24 hours'} icon={<ScrollText size={15} />} />
          <StatCard label="Fallbacks" value={formatNumber(metrics.fallbacks)} hint={metrics.requests ? `${((metrics.fallbacks / metrics.requests) * 100).toFixed(1)}% of traffic` : 'none'} tone="warning" icon={<Zap size={15} />} />
          <StatCard label="Avg latency" value={formatLatency(metrics.avg_latency_ms)} hint="all providers" icon={<Timer size={15} />} />
          <StatCard label="Tokens" value={formatCompact(metrics.in_tokens + metrics.out_tokens)} hint={`${formatCompact(metrics.in_tokens)} in / ${formatCompact(metrics.out_tokens)} out`} />
        </div>
      )}

      <div className="rounded-card border border-border bg-surface p-3">
        <div className="mb-2 flex items-center gap-1.5 text-[11px] uppercase tracking-wide text-muted">
          <Filter size={12} /> Filters
        </div>
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-5">
          <Field label="Route">
            <Select value={routeFilter} onChange={(e) => { setOffset(0); setRouteFilter(e.target.value); }}>
              <option value="">All routes</option>
              {routes.map((r) => <option key={r.id} value={r.alias}>{r.alias}</option>)}
            </Select>
          </Field>
          <Field label="Provider">
            <Select value={providerFilter} onChange={(e) => { setOffset(0); setProviderFilter(e.target.value); }}>
              <option value="">All providers</option>
              {providers.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </Select>
          </Field>
          <Field label="Status">
            <Select value={statusFilter} onChange={(e) => { setOffset(0); setStatusFilter(e.target.value); }}>
              <option value="">All statuses</option>
              <option value="ok">Success</option>
              <option value="error">Error</option>
              <option value="blocked">Blocked</option>
            </Select>
          </Field>
          <Field label="Time range">
            <Select value={range} onChange={(e) => { setOffset(0); setRange(e.target.value); }}>
              <option value="1h">Last hour</option>
              <option value="24h">Last 24 hours</option>
              <option value="7d">Last 7 days</option>
              <option value="30d">Last 30 days</option>
            </Select>
          </Field>
          <Field label="Search">
            <Input value={search} onChange={(e) => { setOffset(0); setSearch(e.target.value); }} placeholder="Request ID, IP, key…" />
          </Field>
        </div>
      </div>

      <div className="flex items-center justify-between">
        <h2 className="text-h3 text-text">Gateway execution log</h2>
        <Button variant="secondary" size="compact" icon={<Download size={14} />} onClick={exportCSV}>Export CSV</Button>
      </div>

      <DataTable
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        loading={loading}
        error={error}
        onRetry={() => void load()}
        onRowClick={(r) => void detail(r)}
        empty={<p className="py-10 text-center text-[12.5px] text-muted">No requests in this window. Send a call to the gateway to populate activity.</p>}
        footer={<Pagination total={total} limit={PAGE_SIZE} offset={offset} onChange={setOffset} label="requests" />}
      />

      <Drawer
        open={!!selected}
        onClose={() => setSelected(null)}
        title="Request detail"
        description={selected ? selected.request_id : undefined}
        footer={<Button variant="secondary" onClick={() => setSelected(null)}>Close</Button>}
      >
        {selected && <RequestDetail entry={selected} />}
      </Drawer>
    </div>
  );
}

function RequestDetail({ entry }: { entry: ActivityEntry }) {
  const facts: [string, string][] = [
    ['Route', entry.route_alias || '—'],
    ['Requested model', entry.requested_model || '—'],
    ['Used model', entry.model_name || 'None'],
    ['Provider', entry.provider_name || '—'],
    ['API key', entry.api_key_name ? `${entry.api_key_name} (${entry.api_key_prefix}****)` : '—'],
    ['Client IP', entry.ip || '—'],
    ['Latency', formatLatency(entry.latency_ms)],
    ['Tokens', `${formatNumber(entry.in_tokens)} in / ${formatNumber(entry.out_tokens)} out`],
    ['HTTP status', String(entry.http_status)],
  ];

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge spec={activityBadge(entry)} />
        <code className="rounded-control border border-border bg-surface px-2 py-1 font-mono text-[11px] text-muted">{entry.request_id}</code>
        <span className="font-mono text-[11px] text-muted">{formatDateTime(entry.ts)}</span>
      </div>

      {entry.guard_reason && <Callout tone="danger">Guard: {entry.guard_reason}</Callout>}
      {entry.error_code && !entry.guard_reason && <Callout tone="warning">{entry.error_code}</Callout>}

      <dl className="grid grid-cols-2 gap-3">
        {facts.map(([k, v]) => (
          <div key={k}>
            <dt className="text-[11px] uppercase tracking-wide text-muted">{k}</dt>
            <dd className="mt-0.5 break-words text-[12.5px] text-text">{v}</dd>
          </div>
        ))}
      </dl>

      <div>
        <h3 className="mb-2 text-[12.5px] font-semibold uppercase tracking-wide text-muted">Routing timeline</h3>
        {entry.trace.length === 0 ? (
          <p className="text-[12px] text-muted">No routing attempts recorded.</p>
        ) : (
          <ol className="relative ml-2 border-l border-border pl-4">
            {(entry.trace ?? []).map((step, i) => (
              <li key={i} className="relative pb-4 last:pb-0">
                <span
                  className={`absolute -left-[21px] top-1 h-2.5 w-2.5 rounded-full border-2 border-bg ${
                    step.outcome === 'success' ? 'bg-success' : step.outcome === 'skipped' ? 'bg-muted' : 'bg-danger'
                  }`}
                />
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-[11px] text-muted">Attempt {step.attempt}</span>
                  <span className="font-mono text-[12px] font-medium text-text">{step.target}</span>
                  {step.provider && <span className="text-[11.5px] text-muted">· {step.provider}</span>}
                  {step.status_code ? <Badge tone={step.outcome === 'success' ? 'success' : 'danger'} dot={false}>{step.status_code}</Badge> : null}
                  <span className="font-mono text-[11px] text-muted">{formatLatency(step.latency_ms)}</span>
                </div>
                {step.message && <p className="mt-0.5 text-[11.5px] text-muted">{step.message}</p>}
                {step.category && <p className="text-[11px] text-muted">category: {step.category}</p>}
              </li>
            ))}
          </ol>
        )}
      </div>

      <Callout tone="neutral">Request content is not stored. Only metadata and routing outcomes are recorded.</Callout>
    </div>
  );
}

function UsageTab() {
  const [groupBy, setGroupBy] = useState('route');
  const [days, setDays] = useState(7);
  const [rows, setRows] = useState<UsageRow[]>([]);
  const [summary, setSummary] = useState<Metrics | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const res = await admin.usage(groupBy, days);
      setRows(res.data ?? []);
      setSummary(res.summary);
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to load usage');
    } finally {
      setLoading(false);
    }
  }, [groupBy, days]);

  useEffect(() => {
    void load();
  }, [load]);

  const totalCost = useMemo(() => {
    const withCost = rows.filter((r) => r.est_cost !== null);
    if (withCost.length === 0) return null;
    return withCost.reduce((acc, r) => acc + (r.est_cost ?? 0), 0);
  }, [rows]);

  const columns: Column<UsageRow>[] = [
    { key: 'label', header: groupBy === 'day' ? 'Date' : groupBy === 'provider' ? 'Provider' : groupBy === 'key' ? 'API key' : 'Route', render: (r) => <span className="font-medium text-text">{r.label}</span> },
    { key: 'requests', header: 'Requests', align: 'right', render: (r) => <span className="tabular">{formatNumber(r.requests)}</span> },
    { key: 'ok', header: 'Successful', align: 'right', render: (r) => <span className="tabular text-success">{formatNumber(r.ok)}</span> },
    { key: 'failed', header: 'Failed', align: 'right', render: (r) => <span className="tabular text-danger">{formatNumber(r.failed)}</span> },
    { key: 'fallbacks', header: 'Fallbacks', align: 'right', render: (r) => <span className="tabular text-muted">{formatNumber(r.fallbacks)}</span> },
    { key: 'in', header: 'Input tokens', align: 'right', render: (r) => <span className="tabular text-muted">{formatCompact(r.in_tokens)}</span> },
    { key: 'out', header: 'Output tokens', align: 'right', render: (r) => <span className="tabular text-muted">{formatCompact(r.out_tokens)}</span> },
    {
      key: 'cost',
      header: 'Est. cost',
      align: 'right',
      render: (r) => (r.est_cost === null ? <span className="text-[11.5px] italic text-muted">Cost unavailable</span> : <span className="tabular">{formatCost(r.est_cost)}</span>),
    },
  ];

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard label="Requests" value={formatNumber(summary?.requests ?? 0)} hint={`last ${days} days`} />
        <StatCard label="Successful" value={formatNumber(summary?.ok ?? 0)} tone="success" hint={`${(summary?.success_rate ?? 0).toFixed(1)}% success`} />
        <StatCard label="Failed" value={formatNumber(summary?.failed ?? 0)} tone="danger" hint="provider errors" />
        <StatCard label="Fallbacks" value={formatNumber(summary?.fallbacks ?? 0)} tone="warning" hint="triggered failover" />
        <StatCard label="Input tokens" value={formatCompact(summary?.in_tokens ?? 0)} />
        <StatCard label="Output tokens" value={formatCompact(summary?.out_tokens ?? 0)} />
        <StatCard label="Estimated cost" value={totalCost === null ? 'Unavailable' : formatCost(totalCost)} tone={totalCost === null ? 'default' : 'accent'} hint={totalCost === null ? 'prices not configured' : `last ${days} days`} />
        <StatCard label="Avg latency" value={formatLatency(summary?.avg_latency_ms ?? 0)} />
      </div>

      <Card padded={false}>
        <div className="flex flex-col gap-2 border-b border-border p-3 sm:flex-row sm:items-center sm:justify-between">
          <CardHeader title="Aggregate usage" description="Grouped totals from daily rollups." />
          <div className="flex items-center gap-2">
            <Select value={groupBy} onChange={(e) => setGroupBy(e.target.value)} className="w-40">
              <option value="route">Group by route</option>
              <option value="provider">Group by provider</option>
              <option value="key">Group by key</option>
              <option value="day">Group by day</option>
            </Select>
            <Select value={String(days)} onChange={(e) => setDays(Number(e.target.value))} className="w-32">
              <option value="1">1 day</option>
              <option value="7">7 days</option>
              <option value="30">30 days</option>
            </Select>
          </div>
        </div>
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(r) => r.key}
          loading={loading}
          error={error}
          onRetry={() => void load()}
          empty={<p className="py-10 text-center text-[12.5px] text-muted">No usage recorded for this period.</p>}
        />
      </Card>
    </div>
  );
}
