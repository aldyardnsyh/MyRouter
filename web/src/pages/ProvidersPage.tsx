import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus, RefreshCw, Server, ShieldCheck, Zap } from 'lucide-react';
import { PageHeader, StatCard, Card, CardHeader } from '../components/ui/Layout';
import { Callout } from '../components/ui/Layout';
import { Button } from '../components/ui/Button';
import { Badge, StatusBadge } from '../components/ui/Badge';
import { DataTable, RowActions, type Column } from '../components/ui/DataTable';
import { Modal, ConfirmDialog } from '../components/ui/Overlay';
import { Field, Input, Select } from '../components/ui/Field';
import { useToast } from '../components/ui/Toast';
import { admin, ApiError } from '../lib/api';
import type { Provider, ProviderType, Status } from '../lib/types';
import {
  classNames,
  formatNumber,
  providerHue,
  providerInitial,
  providerTypeLabel,
  relativeTime,
  statusSpec,
} from '../lib/format';

const typeOptions: { value: ProviderType; label: string; hint: string }[] = [
  { value: 'openai', label: 'OpenAI', hint: 'api.openai.com/v1' },
  { value: 'anthropic', label: 'Anthropic', hint: 'api.anthropic.com/v1' },
  { value: 'gemini', label: 'Google Gemini', hint: 'generativelanguage.googleapis.com' },
  { value: 'deepseek', label: 'DeepSeek', hint: 'api.deepseek.com/v1' },
  { value: 'openai-compatible', label: 'OpenAI-compatible', hint: 'Custom base URL' },
];

const defaultBase: Record<ProviderType, string> = {
  openai: 'https://api.openai.com/v1',
  anthropic: 'https://api.anthropic.com/v1',
  gemini: 'https://generativelanguage.googleapis.com/v1beta',
  deepseek: 'https://api.deepseek.com/v1',
  'openai-compatible': '',
};

interface ProviderForm {
  type: ProviderType;
  name: string;
  base_url: string;
  api_key: string;
}

const emptyForm: ProviderForm = { type: 'openai', name: '', base_url: '', api_key: '' };

// Host shown in the base URL warning. Falls back to the raw string so an
// unparseable draft never leaves the message blank.
function hostOf(rawUrl: string): string {
  try {
    return new URL(rawUrl).host || rawUrl;
  } catch {
    return rawUrl;
  }
}

// The backend stores base URLs without a trailing slash, so compare the same way.
function trimTrailingSlash(value: string): string {
  return value.replace(/\/+$/, '');
}

const BASE_URL_CONFIRM_REQUIRED = 'base_url_change_requires_confirmation';

export function ProvidersPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const [providers, setProviders] = useState<Provider[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [statusFilter, setStatusFilter] = useState('all');
  const [typeFilter, setTypeFilter] = useState('all');

  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<Provider | null>(null);
  const [form, setForm] = useState<ProviderForm>(emptyForm);
  const [formError, setFormError] = useState('');
  const [saving, setSaving] = useState(false);
  const [testLine, setTestLine] = useState<{ ok: boolean; text: string } | null>(null);
  const [testing, setTesting] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<Provider | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [pendingBaseUrl, setPendingBaseUrl] = useState<string | null>(null);

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const res = await admin.providers.list();
      setProviders(res.data ?? []);
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to load providers');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const filtered = useMemo(() => {
    return providers.filter((p) => {
      if (search && !`${p.name} ${p.type} ${p.base_url}`.toLowerCase().includes(search.toLowerCase())) return false;
      if (statusFilter !== 'all' && p.status !== statusFilter) return false;
      if (typeFilter !== 'all' && p.type !== typeFilter) return false;
      return true;
    });
  }, [providers, search, statusFilter, typeFilter]);

  const stats = useMemo(() => {
    const active = providers.filter((p) => p.enabled).length;
    const disabled = providers.length - active;
    const models = providers.reduce((acc, p) => acc + (p.model_count ?? 0), 0);
    const healthy = providers.filter((p) => p.status === 'connected').length;
    const healthPct = providers.length ? (healthy / providers.length) * 100 : 0;
    return { active, disabled, models, healthy, healthPct };
  }, [providers]);

  async function openCreate() {
    setEditing(null);
    setForm(emptyForm);
    setFormError('');
    setTestLine(null);
    setPendingBaseUrl(null);
    setModalOpen(true);
  }

  function openEdit(p: Provider) {
    setEditing(p);
    setForm({ type: p.type, name: p.name, base_url: p.base_url, api_key: '' });
    setFormError('');
    setTestLine(null);
    setPendingBaseUrl(null);
    setModalOpen(true);
  }

  function closeForm() {
    setModalOpen(false);
    setPendingBaseUrl(null);
  }

  // True when the draft moves a provider that already holds a stored
  // credential to a different host. The backend rejects that PATCH unless the
  // request carries confirm_base_url, because the stored key would be sent to
  // the new host.
  function needsBaseUrlConfirmation(provider: Provider, nextBaseUrl: string): boolean {
    if (!nextBaseUrl) return false;
    if (!provider.credential_hint) return false;
    // A replacement key is supplied in the same request, so the old credential
    // is not sent anywhere: no confirmation needed.
    if (form.api_key.trim()) return false;
    return trimTrailingSlash(nextBaseUrl) !== trimTrailingSlash(provider.base_url);
  }

  async function save(confirmBaseUrl = false) {
    setFormError('');
    if (!form.name.trim()) {
      setFormError('Display name is required');
      return;
    }
    if (!editing && !form.api_key.trim()) {
      setFormError('API key is required for a new provider');
      return;
    }
    const nextBaseUrl = form.base_url.trim() || defaultBase[form.type];
    if (editing && !confirmBaseUrl && needsBaseUrlConfirmation(editing, nextBaseUrl)) {
      setPendingBaseUrl(nextBaseUrl);
      return;
    }
    setSaving(true);
    try {
      // The backend now tests the connection and imports models as part of the
      // save, so the response tells us whether the provider is usable without
      // a separate Test / Refresh Models round trip.
      const result = editing
        ? await admin.providers.update(editing.id, {
            name: form.name.trim(),
            type: form.type,
            base_url: nextBaseUrl,
            ...(form.api_key ? { api_key: form.api_key } : {}),
            ...(confirmBaseUrl ? { confirm_base_url: true } : {}),
          })
        : await admin.providers.create({
            name: form.name.trim(),
            type: form.type,
            base_url: nextBaseUrl,
            api_key: form.api_key,
          });
      setPendingBaseUrl(null);
      setModalOpen(false);
      await load(true);
      if (result.connection.status === 'connected') {
        if (result.models > 0) {
          toast.success(`Connected · ${result.models} models discovered`);
        } else {
          toast.success('Connected, but no models were returned');
        }
      } else {
        toast.warning(`Saved, but connection failed: ${result.connection.message}`);
      }
    } catch (err) {
      // Race or a second client: the server still refuses the base URL change.
      // Drop back to the form with the exact reason instead of a generic toast,
      // so the draft is kept and the host can be re-checked.
      if (err instanceof ApiError && err.status === 400 && err.code === BASE_URL_CONFIRM_REQUIRED) {
        setPendingBaseUrl(null);
        setFormError(
          'Server menolak perubahan base URL: kredensial tersimpan belum dikonfirmasi. Periksa kembali host tujuan lalu simpan ulang.',
        );
        return;
      }
      setFormError(err instanceof ApiError ? err.message : 'Could not save provider');
    } finally {
      setSaving(false);
    }
  }

  async function testDraft() {
    setTesting(true);
    setTestLine(null);
    setFormError('');
    try {
      const nextBaseUrl = form.base_url.trim() || defaultBase[form.type];
      const baseChanged = editing ? trimTrailingSlash(nextBaseUrl) !== trimTrailingSlash(editing.base_url) : true;
      if (!form.api_key.trim() && !editing) {
        setTestLine({ ok: false, text: 'Enter the API key, then test the connection.' });
        return;
      }
      if (!form.api_key.trim() && baseChanged) {
        setTestLine({ ok: false, text: 'Enter the API key to test the new host.' });
        return;
      }
      const res = await admin.providers.testDraft({
        type: form.type,
        base_url: nextBaseUrl,
        api_key: form.api_key.trim(),
        ...(editing && !baseChanged ? { provider_id: editing.id } : {}),
      });
      const ok = res.status === 'connected';
      setTestLine({ ok, text: ok ? `Connected in ${res.latency_ms}ms` : res.message || res.status });
    } catch (err) {
      setTestLine({ ok: false, text: err instanceof ApiError ? err.message : 'Connection test failed' });
    } finally {
      setTesting(false);
    }
  }

  async function testProvider(p: Provider) {
    setBusyId(p.id);
    try {
      const res = await admin.providers.test(p.id);
      toast.info(res.status === 'connected' ? `Connected in ${res.latency_ms}ms` : res.message || res.status);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Test failed');
    } finally {
      setBusyId(null);
    }
  }

  async function refreshModels(p: Provider) {
    setBusyId(p.id);
    try {
      const res = await admin.providers.refresh(p.id);
      toast.success(`Discovered ${res.count} models`);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Model refresh failed');
    } finally {
      setBusyId(null);
    }
  }

  async function toggleEnabled(p: Provider) {
    setBusyId(p.id);
    try {
      await admin.providers.update(p.id, { enabled: !p.enabled });
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Update failed');
    } finally {
      setBusyId(null);
    }
  }

  async function remove() {
    if (!confirmDelete) return;
    setDeleting(true);
    try {
      await admin.providers.remove(confirmDelete.id);
      toast.success('Provider deleted');
      setConfirmDelete(null);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Delete failed');
    } finally {
      setDeleting(false);
    }
  }

  const columns: Column<Provider>[] = [
    {
      key: 'provider',
      header: 'Provider',
      render: (p) => (
        <div className="flex min-w-0 items-center gap-2.5">
          <span
            className={classNames(
              'flex h-8 w-8 shrink-0 items-center justify-center rounded-[8px] text-[12px] font-bold',
              p.enabled ? providerHue(p.name) : 'bg-surface-2 text-muted',
            )}
          >
            {providerInitial(p.name)}
          </span>
          <div className="min-w-0">
            <p className={classNames('truncate font-medium', p.enabled ? 'text-text' : 'text-muted')}>{p.name}</p>
            <p className="truncate font-mono text-[11px] text-muted" title={p.base_url}>{p.base_url}</p>
          </div>
        </div>
      ),
    },
    {
      key: 'type',
      header: 'Type',
      render: (p) => (
        <span className="rounded-control border border-border bg-surface px-2 py-0.5 text-[11.5px] text-muted">
          {providerTypeLabel[p.type] ?? p.type}
        </span>
      ),
    },
    {
      key: 'status',
      header: 'Status',
      render: (p) => (
        <div className="flex flex-col items-start gap-1">
          <div className="flex flex-wrap items-center gap-1.5">
            {!p.enabled && <Badge tone="neutral" dot={false}>Disabled</Badge>}
            <span className={p.enabled ? undefined : 'opacity-60'}>
              <StatusBadge spec={statusSpec[p.status as Status] ?? statusSpec.unknown} />
            </span>
          </div>
          {p.last_error && <span className="max-w-[180px] truncate text-[10.5px] text-danger">{p.last_error}</span>}
        </div>
      ),
    },
    { key: 'models', header: 'Models', align: 'right', render: (p) => <span className="tabular text-muted">{formatNumber(p.model_count)}</span> },
    { key: 'last', header: 'Last check', align: 'right', render: (p) => <span className="font-mono text-[11.5px] text-muted">{relativeTime(p.last_check)}</span> },
  ];

  // Saving now also tests the connection and imports models, so no test gate:
  // a name plus (for a new provider) an API key is enough.
  const canSave = form.name.trim().length > 0 && (!!editing || form.api_key.trim().length > 0);

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Providers"
        description="Configure upstream LLM APIs, endpoints, and credentials."
        actions={
          <>
            <Button variant="secondary" size="compact" icon={<RefreshCw size={14} />} onClick={() => void load()} loading={loading}>
              Refresh
            </Button>
            <Button variant="primary" size="compact" icon={<Plus size={14} />} onClick={() => void openCreate()}>
              Add Provider
            </Button>
          </>
        }
      />

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard label="Active Upstreams" value={`${stats.active}`} hint={stats.disabled ? `${stats.disabled} disabled of ${providers.length}` : `${providers.length} configured`} icon={<Server size={15} />} />
        <StatCard label="Aggregate Models" value={formatNumber(stats.models)} hint="discovered" />
        <StatCard label="Healthy" value={`${stats.healthy}`} hint="connected status" tone="success" icon={<ShieldCheck size={15} />} />
        <StatCard label="Health State" value={`${stats.healthPct.toFixed(1)}%`} hint={providers.length - stats.healthy ? `${providers.length - stats.healthy} need attention` : 'all good'} tone={stats.healthPct === 100 ? 'success' : 'warning'} />
      </div>

      <div className="flex flex-col gap-2 rounded-card border border-border bg-surface p-3 sm:flex-row sm:items-center">
        <div className="flex-1">
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Filter providers…" />
        </div>
        <Select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)} className="sm:w-44">
          <option value="all">All statuses</option>
          <option value="connected">Connected</option>
          <option value="degraded">Degraded</option>
          <option value="unavailable">Unavailable</option>
          <option value="invalid_credentials">Invalid credentials</option>
          <option value="unknown">Unknown</option>
        </Select>
        <Select value={typeFilter} onChange={(e) => setTypeFilter(e.target.value)} className="sm:w-48">
          <option value="all">All types</option>
          {typeOptions.map((t) => (
            <option key={t.value} value={t.value}>{t.label}</option>
          ))}
        </Select>
        <span className="flex items-center gap-1.5 whitespace-nowrap rounded-pill bg-bg px-2.5 py-1 text-[11.5px] text-muted border border-border">
          <span className="h-1.5 w-1.5 rounded-full bg-success" />
          {stats.active} active
        </span>
      </div>

      <DataTable
        columns={columns}
        rows={filtered}
        rowKey={(p) => p.id}
        loading={loading}
        error={error}
        onRetry={() => void load()}
        onRowClick={(p) => navigate(`/providers/${p.id}`)}
        rowClassName={(p) => (p.enabled ? undefined : 'bg-surface/60 [&>td]:opacity-70')}
        empty={
          <div className="flex flex-col items-center gap-2 py-12">
            <Server size={20} className="text-muted" />
            <p className="text-[13px] font-medium text-text">No providers yet</p>
            <p className="text-[12px] text-muted">Add your first upstream LLM API to start routing.</p>
            <Button variant="primary" size="compact" className="mt-2" onClick={() => void openCreate()}>Add Provider</Button>
          </div>
        }
        footer={<span className="text-[12px] text-muted">Showing {filtered.length} of {providers.length} providers</span>}
        actions={(p) => (
          <RowActions
            items={[
              { label: busyId === p.id ? 'Testing…' : 'Test connection', onClick: () => void testProvider(p), disabled: busyId === p.id || !p.enabled },
              { label: 'Refresh models', onClick: () => void refreshModels(p), disabled: busyId === p.id || !p.enabled },
              { label: p.enabled ? 'Disable' : 'Enable', onClick: () => void toggleEnabled(p) },
              { label: 'Edit provider', onClick: () => openEdit(p) },
              { label: 'View details', onClick: () => navigate(`/providers/${p.id}`) },
              { label: 'Delete', destructive: true, onClick: () => setConfirmDelete(p) },
            ]}
          />
        )}
      />

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        <Card>
          <CardHeader title="Auto-Routing Policy" />
          <p className="text-[12px] text-muted leading-relaxed">
            Routes try the primary target first. Fallback triggers on 429, timeout, 5xx, and connection failures only.
          </p>
          <div className="mt-3 flex items-center justify-between border-t border-border pt-2.5">
            <span className="font-mono text-[11px] text-muted">STRATEGY: PRIORITY</span>
            <Zap size={14} className="text-accent" />
          </div>
        </Card>
        <Card>
          <CardHeader title="Upstream Key Vault" />
          <p className="text-[12px] text-muted leading-relaxed">
            Provider credentials are encrypted with AES-256-GCM and decrypted only in memory per request.
          </p>
          <div className="mt-3 flex items-center justify-between border-t border-border pt-2.5">
            <span className="font-mono text-[11px] text-muted">VAULT STATUS: SECURE</span>
            <ShieldCheck size={14} className="text-success" />
          </div>
        </Card>
        <Card>
          <CardHeader title="Internal Gateway" />
          <p className="text-[12px] text-muted leading-relaxed">
            Point OpenAI-compatible SDKs at the loopback gateway base URL to route through MyRouter.
          </p>
          <div className="mt-3 flex items-center justify-between border-t border-border pt-2.5">
            <span className="font-mono text-[11px] text-muted">GATEWAY: /v1</span>
          </div>
        </Card>
      </div>

      <Modal
        open={modalOpen && pendingBaseUrl === null}
        onClose={saving ? () => {} : closeForm}
        title={editing ? 'Edit Provider' : 'Add Provider'}
        description={editing ? 'Update the upstream endpoint or replace its credential.' : 'Connect an upstream LLM API.'}
        footer={
          <>
            <Button variant="secondary" onClick={closeForm} disabled={saving}>Cancel</Button>
            <Button variant="primary" onClick={() => void save()} loading={saving} disabled={!canSave}>
              {saving ? 'Saving & checking...' : 'Save'}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-3.5">
          <div>
            <p className="mb-1.5 text-small font-medium text-text">Provider type</p>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              {typeOptions.map((t) => (
                <button
                  key={t.value}
                  type="button"
                  onClick={() => setForm((f) => ({ ...f, type: t.value, base_url: '' }))}
                  className={classNames(
                    'rounded-control border px-2.5 py-2 text-left transition-colors',
                    form.type === t.value ? 'border-accent bg-accent-soft' : 'border-border bg-bg hover:bg-surface-2',
                  )}
                >
                  <span className={classNames('block text-[12.5px] font-medium', form.type === t.value ? 'text-accent' : 'text-text')}>{t.label}</span>
                  <span className="block truncate text-[10.5px] text-muted">{t.hint}</span>
                </button>
              ))}
            </div>
          </div>
          <Field label="Display name" required>
            <Input data-autofocus value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="OpenAI Global" />
          </Field>
          <Field label="Base URL" hint="Leave blank to use the provider default.">
            <Input mono value={form.base_url} onChange={(e) => setForm({ ...form, base_url: e.target.value })} placeholder={defaultBase[form.type] || 'https://…/v1'} />
          </Field>
          <Field
            label={editing ? 'Replace API key' : 'API key'}
            hint={editing ? 'Leave blank to keep the stored credential. Encrypted at rest, never displayed again.' : 'Encrypted at rest. Never displayed again.'}
          >
            <Input type="password" mono value={form.api_key} onChange={(e) => setForm({ ...form, api_key: e.target.value })} placeholder="sk-…" />
          </Field>
          {testLine && <Callout tone={testLine.ok ? 'info' : 'warning'}>{testLine.text}</Callout>}
          {formError && <p className="text-[12px] text-danger" role="alert">{formError}</p>}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <Button variant="primary" size="compact" onClick={() => void testDraft()} loading={testing} icon={<ShieldCheck size={14} />}>
              Test Connection
            </Button>
            <p className="text-[11.5px] text-muted">Saving tests the connection and imports models automatically.</p>
          </div>
        </div>
      </Modal>

      <ConfirmDialog
        open={pendingBaseUrl !== null}
        onClose={() => setPendingBaseUrl(null)}
        onConfirm={() => void save(true)}
        title="Ubah base URL provider?"
        message={`Kredensial tersimpan akan dikirim ke ${hostOf(pendingBaseUrl ?? '')}. Lanjutkan hanya jika Anda memindahkan provider ini ke server yang Anda percaya.`}
        confirmLabel="Ya, ubah"
        cancelLabel="Batal"
        destructive={false}
        loading={saving}
      />

      <ConfirmDialog
        open={!!confirmDelete}
        onClose={() => setConfirmDelete(null)}
        onConfirm={() => void remove()}
        title="Delete provider"
        message={`Delete “${confirmDelete?.name}”? Its models, credentials, and route targets will be removed. Routes that lose every target are deleted too. Disable the provider instead if you only want to pause it. This cannot be undone.`}
        confirmLabel="Delete provider"
        loading={deleting}
      />
    </div>
  );
}

