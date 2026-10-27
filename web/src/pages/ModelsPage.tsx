import { useCallback, useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { ArrowLeft, KeyRound, RefreshCw, ShieldCheck } from 'lucide-react';
import { Card, CardHeader, Callout } from '../components/ui/Layout';
import { Button } from '../components/ui/Button';
import { Badge, StatusBadge } from '../components/ui/Badge';
import { DataTable, Tabs, type Column } from '../components/ui/DataTable';
import { Field, Input, Select } from '../components/ui/Field';
import { Modal } from '../components/ui/Overlay';
import { useToast } from '../components/ui/Toast';
import { admin, ApiError } from '../lib/api';
import type { Model, Provider } from '../lib/types';
import {
  classNames,
  formatContext,
  formatPrice,
  providerHue,
  providerInitial,
  providerTypeLabel,
  relativeTime,
  statusSpec,
  capabilities,
} from '../lib/format';

export function ProviderDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const [provider, setProvider] = useState<Provider | null>(null);
  const [models, setModels] = useState<Model[]>([]);
  const [tab, setTab] = useState('overview');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [replaceOpen, setReplaceOpen] = useState(false);
  const [newKey, setNewKey] = useState('');
  const [saving, setSaving] = useState(false);

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [p, m] = await Promise.all([
        admin.providers.get(id),
        admin.providers.models(id),
      ]);
      setProvider(p.data);
      setModels(m.data ?? []);
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to load provider');
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);

  async function refreshModels() {
    try {
      const res = await admin.providers.refresh(id);
      toast.success(`Discovered ${res.count} models`);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Refresh failed');
    }
  }

  async function replaceCredential() {
    if (!newKey.trim()) return;
    setSaving(true);
    try {
      await admin.providers.update(id, { api_key: newKey.trim() });
      toast.success('Credential replaced');
      setReplaceOpen(false);
      setNewKey('');
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Update failed');
    } finally {
      setSaving(false);
    }
  }

  if (error) {
    return (
      <div className="flex flex-col items-center gap-2 py-24 text-center">
        <p className="text-[13px] text-muted">{error}</p>
        <Button variant="secondary" size="compact" onClick={() => void load()}>Retry</Button>
      </div>
    );
  }

  const modelCols: Column<Model>[] = [
    { key: 'model', header: 'Model', render: (m) => <span className="font-mono text-[12px]">{m.model_id}</span> },
    { key: 'name', header: 'Display name', render: (m) => m.display_name },
    { key: 'ctx', header: 'Context', align: 'right', render: (m) => <span className="tabular text-muted">{formatContext(m.context_window)}</span> },
    {
      key: 'in',
      header: 'Input',
      align: 'right',
      render: (m) => {
        const v = formatPrice(m.price_in);
        return v ? <span className="tabular">{v}</span> : <span className="text-muted italic">Cost unavailable</span>;
      },
    },
    {
      key: 'out',
      header: 'Output',
      align: 'right',
      render: (m) => {
        const v = formatPrice(m.price_out);
        return v ? <span className="tabular">{v}</span> : <span className="text-muted italic">Cost unavailable</span>;
      },
    },
    { key: 'status', header: 'Status', render: (m) => <Badge tone={m.available ? 'success' : 'danger'}>{m.available ? 'Available' : 'Unavailable'}</Badge> },
  ];

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-2">
        <button onClick={() => navigate('/providers')} className="flex w-fit items-center gap-1.5 text-[12px] text-muted hover:text-text">
          <ArrowLeft size={14} /> Providers
        </button>
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="flex items-center gap-3">
            <span className={classNames('flex h-10 w-10 items-center justify-center rounded-[10px] text-[15px] font-bold', providerHue(provider?.name ?? '?'))}>
              {providerInitial(provider?.name ?? '?')}
            </span>
            <div>
              <div className="flex items-center gap-2">
                <h1 className="text-h2 text-text">{provider?.name ?? '…'}</h1>
                {provider && !provider.enabled && <Badge tone="neutral" dot={false}>Disabled</Badge>}
                {provider && <StatusBadge spec={statusSpec[provider.status] ?? statusSpec.unknown} />}
              </div>
              <p className="font-mono text-[11.5px] text-muted">{provider?.base_url}</p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="secondary"
              size="compact"
              icon={<RefreshCw size={14} />}
              onClick={() => void refreshModels()}
              disabled={provider ? !provider.enabled : false}
              title={provider && !provider.enabled ? 'Aktifkan provider dulu untuk refresh model.' : undefined}
            >
              Refresh Models
            </Button>
          </div>
        </div>
      </div>

      <Tabs
        tabs={[
          { key: 'overview', label: 'Overview' },
          { key: 'models', label: 'Models', badge: String(models.length) },
          { key: 'guard', label: 'Guard Policy' },
        ]}
        active={tab}
        onChange={setTab}
      />

      {tab === 'overview' && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <Card>
            <CardHeader title="Provider info" />
            <dl className="grid grid-cols-2 gap-3 text-[12.5px]">
              <div><dt className="text-muted">Type</dt><dd className="mt-0.5 text-text">{provider ? providerTypeLabel[provider.type] ?? provider.type : '—'}</dd></div>
              <div><dt className="text-muted">Models</dt><dd className="mt-0.5 text-text tabular">{provider?.model_count ?? 0}</dd></div>
              <div><dt className="text-muted">Status</dt><dd className="mt-0.5 text-text">{provider ? statusSpec[provider.status]?.label ?? provider.status : '—'}</dd></div>
              <div><dt className="text-muted">Last check</dt><dd className="mt-0.5 text-text">{relativeTime(provider?.last_check ?? null)}</dd></div>
              <div className="col-span-2"><dt className="text-muted">Base URL</dt><dd className="mt-0.5 font-mono text-[11.5px] text-text break-all">{provider?.base_url}</dd></div>
              {provider?.last_error && (
                <div className="col-span-2"><dt className="text-muted">Last error</dt><dd className="mt-0.5 text-danger">{provider.last_error}</dd></div>
              )}
            </dl>
          </Card>
          <Card>
            <CardHeader title="Credential" description="Stored encrypted; only a masked hint is shown." />
            <div className="flex items-center justify-between gap-2 rounded-control border border-border bg-surface px-3 py-2.5">
              <div className="flex items-center gap-2">
                <KeyRound size={15} className="text-muted" />
                <code className="font-mono text-[12px] text-text">{provider?.credential_hint || 'No credential set'}</code>
              </div>
              <Button variant="secondary" size="compact" onClick={() => setReplaceOpen(true)}>Replace</Button>
            </div>
            <Callout tone="neutral">
              Provider credentials never leave the server. Requests are signed inside MyRouter.
            </Callout>
          </Card>
        </div>
      )}

      {tab === 'models' && (
        <DataTable
          columns={modelCols}
          rows={models}
          rowKey={(m) => m.id}
          loading={loading}
          empty={<p className="py-8 text-center text-[12.5px] text-muted">No models discovered. Use Refresh Models.</p>}
        />
      )}

      {tab === 'guard' && (
        <Card>
          <CardHeader title="Guard policy" description="Provider-level bindings are not exposed by the API yet." />
          <div className="flex items-start gap-2 text-[12.5px] text-muted">
            <ShieldCheck size={15} className="mt-0.5 shrink-0" />
            <span>
              Bind a policy to the <strong className="text-text">API key</strong> or <strong className="text-text">route</strong> from
              their pages. The Guard hierarchy still evaluates global and provider scopes internally.
            </span>
          </div>
        </Card>
      )}

      <Modal
        open={replaceOpen}
        onClose={() => setReplaceOpen(false)}
        title="Replace credential"
        size="sm"
        footer={<><Button variant="secondary" onClick={() => setReplaceOpen(false)}>Cancel</Button><Button variant="primary" loading={saving} disabled={!newKey.trim()} onClick={() => void replaceCredential()}>Save</Button></>}
      >
        <Field label="New API key" hint="The previous credential is overwritten and cannot be recovered.">
          <Input type="password" mono value={newKey} onChange={(e) => setNewKey(e.target.value)} placeholder="sk-…" />
        </Field>
      </Modal>
    </div>
  );
}

export function ModelsPage() {
  const [models, setModels] = useState<Model[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [providerFilter, setProviderFilter] = useState('all');

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [m, p] = await Promise.all([admin.models.list(), admin.providers.list()]);
      setModels(m.data ?? []);
      setProviders(p.data ?? []);
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to load models');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const filtered = models.filter((m) => {
    if (providerFilter !== 'all' && m.provider_id !== providerFilter) return false;
    if (search && !`${m.display_name} ${m.model_id} ${m.provider_name ?? ''}`.toLowerCase().includes(search.toLowerCase())) return false;
    return true;
  });

  const columns: Column<Model>[] = [
    {
      key: 'model',
      header: 'Model',
      render: (m) => (
        <div>
          <p className="font-mono text-[12.5px] text-text">{m.model_id}</p>
          <p className="text-[11px] text-muted">{m.display_name}</p>
        </div>
      ),
    },
    { key: 'provider', header: 'Provider', render: (m) => <span className="text-muted">{m.provider_name ?? '—'}</span> },
    { key: 'ctx', header: 'Context window', align: 'right', render: (m) => <span className="tabular text-muted">{formatContext(m.context_window)}</span> },
    {
      key: 'in',
      header: 'Input price',
      align: 'right',
      render: (m) => {
        const v = formatPrice(m.price_in);
        return v ? <span className="tabular">{v}</span> : <span className="text-[11.5px] italic text-muted">Cost unavailable</span>;
      },
    },
    {
      key: 'out',
      header: 'Output price',
      align: 'right',
      render: (m) => {
        const v = formatPrice(m.price_out);
        return v ? <span className="tabular">{v}</span> : <span className="text-[11.5px] italic text-muted">Cost unavailable</span>;
      },
    },
    {
      key: 'caps',
      header: 'Capabilities',
      render: (m) => (
        <div className="flex flex-wrap gap-1">
          {capabilities(m.capabilities).length === 0 ? (
            <span className="text-[11.5px] text-muted">—</span>
          ) : (
            capabilities(m.capabilities).map((c) => (
              <span key={c} className="rounded-pill bg-surface-2 px-1.5 py-0.5 text-[10.5px] text-muted">{c}</span>
            ))
          )}
        </div>
      ),
    },
    {
      key: 'status',
      header: 'Status',
      render: (m) => <Badge tone={m.available ? 'success' : 'danger'}>{m.available ? 'Available' : 'Unavailable'}</Badge>,
    },
  ];

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col items-start justify-between gap-3 sm:flex-row sm:items-end">
        <div>
          <h1 className="text-display text-text">Models</h1>
          <p className="mt-1 text-[13px] text-muted">Every upstream model discovered across your providers. Prices are per 1M tokens.</p>
          <p className="mt-1 text-[12px] text-muted">
            Context windows and prices are filled from the official models.dev catalog when available.
          </p>
        </div>
        <Button variant="secondary" size="compact" onClick={() => void load()} loading={loading}>Refresh</Button>
      </div>

      <div className="flex flex-col gap-2 rounded-card border border-border bg-surface p-3 sm:flex-row sm:items-center">
        <div className="flex-1"><Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search models…" /></div>
        <Select value={providerFilter} onChange={(e) => setProviderFilter(e.target.value)} className="sm:w-56">
          <option value="all">All providers</option>
          {providers.map((p) => (
            <option key={p.id} value={p.id}>{p.name}</option>
          ))}
        </Select>
        <span className="whitespace-nowrap rounded-pill border border-border bg-bg px-2.5 py-1 text-[11.5px] text-muted">{filtered.length} models</span>
      </div>

      <DataTable
        columns={columns}
        rows={filtered}
        rowKey={(m) => m.id}
        loading={loading}
        error={error}
        onRetry={() => void load()}
        empty={<p className="py-10 text-center text-[12.5px] text-muted">No models discovered yet. Add a provider and refresh its models.</p>}
      />
    </div>
  );
}

