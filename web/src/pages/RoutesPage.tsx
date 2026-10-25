import { useCallback, useEffect, useState } from 'react';
import { GripVertical, Plus, Route as RouteIcon, X, Zap } from 'lucide-react';
import { PageHeader, StatCard, Card, CardHeader, EmptyState } from '../components/ui/Layout';
import { Button } from '../components/ui/Button';
import { Badge } from '../components/ui/Badge';
import { DataTable, RowActions, type Column } from '../components/ui/DataTable';
import { Drawer, ConfirmDialog } from '../components/ui/Overlay';
import { Field, Input, Select, Switch, Checkbox, Textarea } from '../components/ui/Field';
import { CodeBlock } from '../components/ui/CopyField';
import { useToast } from '../components/ui/Toast';
import { admin, ApiError } from '../lib/api';
import type { Model, Policy, Route } from '../lib/types';
import { fallbacksOf, formatLatency, isValidAlias, primaryOf } from '../lib/format';

const fallbackConditions = [
  { key: 'rate_limit', label: 'Rate limit (429)' },
  { key: 'timeout', label: 'Timeout' },
  { key: 'upstream_5xx', label: 'Server error (5xx)' },
  { key: 'network', label: 'Connection failure' },
];

interface EditorState {
  id?: string;
  alias: string;
  /** True while `alias` is an auto-suggestion from the picked model. */
  aliasAuto: boolean;
  description: string;
  primary: string;
  fallbacks: string[];
  retry: number;
  timeout_ms: number;
  policy_id: string;
  fallback_on: string[];
}

const emptyEditor: EditorState = {
  alias: '',
  aliasAuto: false,
  description: '',
  primary: '',
  fallbacks: [],
  retry: 1,
  timeout_ms: 30000,
  policy_id: '',
  fallback_on: ['rate_limit', 'timeout', 'upstream_5xx', 'network'],
};

// Same normalization as the backend autoAlias: lowercase, invalid chars to
// dashes, trimmed — keeps manual and auto-created routes visually consistent.
function suggestAlias(modelID: string) {
  const a = modelID
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^[-.]+|[-.]+$/g, '')
    .slice(0, 64)
    .replace(/[-.]+$/, '');
  return a || modelID.toLowerCase().slice(0, 64) || '';
}

export function RoutesPage() {
  const toast = useToast();
  const [routes, setRoutes] = useState<Route[]>([]);
  const [models, setModels] = useState<Model[]>([]);
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editor, setEditor] = useState<EditorState>(emptyEditor);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState('');
  const [confirmDelete, setConfirmDelete] = useState<Route | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [confirmAuto, setConfirmAuto] = useState(false);
  const [autoAdding, setAutoAdding] = useState(false);

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [r, m, p] = await Promise.all([admin.routes.list(), admin.models.list(), admin.policies.list()]);
      setRoutes(r.data ?? []);
      setModels(m.data ?? []);
      setPolicies(p.data ?? []);
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to load routes');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const modelLabel = useCallback(
    (id: string) => {
      const m = models.find((x) => x.id === id);
      return m ? m.model_id : id;
    },
    [models],
  );

  function openCreate() {
    const first = models[0];
    setEditor({
      ...emptyEditor,
      primary: first?.id ?? '',
      alias: first ? suggestAlias(first.model_id) : '',
      aliasAuto: !!first,
    });
    setFormError('');
    setDrawerOpen(true);
  }

  function openEdit(route: Route) {
    const primary = primaryOf(route);
    setEditor({
      id: route.id,
      alias: route.alias,
      aliasAuto: false,
      description: route.description,
      primary: primary?.model_id ?? '',
      fallbacks: fallbacksOf(route).map((f) => f.model_id),
      retry: route.retry,
      timeout_ms: route.timeout_ms,
      policy_id: route.policy_id ?? '',
      fallback_on: route.fallback_on ? route.fallback_on.split(',') : [],
    });
    setFormError('');
    setDrawerOpen(true);
  }

  async function save() {
    setFormError('');
    if (!isValidAlias(editor.alias)) {
      setFormError('Alias must start with a letter or digit and use only letters, digits, dot, dash, underscore.');
      return;
    }
    if (!editor.primary) {
      setFormError('Select a primary model.');
      return;
    }
    setSaving(true);
    try {
      const body = {
        alias: editor.alias,
        description: editor.description,
        primary_model_id: editor.primary,
        fallback_model_ids: editor.fallbacks,
        retry: editor.retry,
        timeout_ms: editor.timeout_ms,
        policy_id: editor.policy_id || '',
        fallback_on: editor.fallback_on,
      };
      if (editor.id) {
        await admin.routes.update(editor.id, body);
        toast.success('Route updated');
      } else {
        await admin.routes.create(body);
        toast.success('Route created');
      }
      setDrawerOpen(false);
      await load(true);
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : 'Could not save route');
    } finally {
      setSaving(false);
    }
  }

  async function toggle(route: Route) {
    try {
      await admin.routes.update(route.id, { enabled: !route.enabled });
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Update failed');
    }
  }

  // One-click setup: a route per discovered provider model with defaults
  // (alias = model id, no fallbacks, global policy). Idempotent — existing
  // aliases are skipped, so it is safe after every new provider/discovery.
  async function autoAdd() {
    setAutoAdding(true);
    try {
      const res = await admin.routes.auto();
      toast.success(
        res.created
          ? `${res.created} route${res.created > 1 ? 's' : ''} created${res.skipped ? `, ${res.skipped} skipped (existing)` : ''}`
          : 'No new routes to add — all discovered models already have routes',
      );
      setConfirmAuto(false);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Auto-add failed');
    } finally {
      setAutoAdding(false);
    }
  }

  async function remove() {
    if (!confirmDelete) return;
    setDeleting(true);
    try {
      await admin.routes.remove(confirmDelete.id);
      toast.success('Route deleted');
      setConfirmDelete(null);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Delete failed');
    } finally {
      setDeleting(false);
    }
  }

  function moveFallback(from: number, to: number) {
    setEditor((prev) => {
      const next = [...prev.fallbacks];
      const [item] = next.splice(from, 1);
      next.splice(to, 0, item);
      return { ...prev, fallbacks: next };
    });
  }

  const enabledCount = routes.filter((r) => r.enabled).length;
  const totalFallbacks = routes.reduce((acc, r) => acc + fallbacksOf(r).length, 0);

  const columns: Column<Route>[] = [
    {
      key: 'alias',
      header: 'Alias',
      render: (r) => (
        <div className="flex flex-col">
          <span className="font-mono text-[12.5px] font-semibold text-text">{r.alias}</span>
          <span className="truncate text-[11px] text-muted">{r.description || 'No description'}</span>
        </div>
      ),
    },
    {
      key: 'primary',
      header: 'Primary model',
      render: (r) => {
        const p = primaryOf(r);
        return p ? (
          <div>
            <p className="font-mono text-[12px] text-text">{p.model_name}</p>
            <p className="text-[11px] text-muted">{p.provider_name}</p>
          </div>
        ) : (
          <span className="text-[12px] text-muted">No target</span>
        );
      },
    },
    {
      key: 'fallbacks',
      header: 'Fallbacks',
      align: 'center',
      render: (r) => {
        const fbs = fallbacksOf(r);
        return fbs.length ? (
          <div className="flex flex-wrap justify-center gap-1">
            {fbs.slice(0, 2).map((f) => (
              <span key={f.id} className="rounded-control border border-border bg-surface px-1.5 py-0.5 font-mono text-[10.5px] text-muted">{f.model_name}</span>
            ))}
            {fbs.length > 2 && <span className="text-[10.5px] text-muted">+{fbs.length - 2}</span>}
          </div>
        ) : (
          <span className="text-[11.5px] text-muted">—</span>
        );
      },
    },
    {
      key: 'policy',
      header: 'Policy',
      render: (r) => <Badge tone={r.policy_name ? 'accent' : 'neutral'} dot={false}>{r.policy_name || 'Global'}</Badge>,
    },
    {
      key: 'enabled',
      header: 'Enabled',
      align: 'center',
      render: (r) => <Switch size="sm" checked={r.enabled} onChange={() => void toggle(r)} label={`Toggle ${r.alias}`} />,
    },
  ];

  const availableFallbacks = models.filter((m) => m.id !== editor.primary && !editor.fallbacks.includes(m.id));

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Routes"
        description="Virtual model aliases with ordered fallback chains across providers."
        actions={
          <>
            <Button
              variant="secondary"
              size="compact"
              icon={<Zap size={14} />}
              loading={autoAdding}
              onClick={() => setConfirmAuto(true)}
              disabled={models.length === 0}
            >
              Auto-add routes
            </Button>
            <Button variant="primary" size="compact" icon={<Plus size={14} />} onClick={openCreate}>
              Create Route
            </Button>
          </>
        }
      />

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard label="Active Routes" value={String(enabledCount)} hint={`${routes.length} total`} tone="success" icon={<RouteIcon size={15} />} />
        <StatCard label="Fallback Targets" value={String(totalFallbacks)} hint="across all routes" />
        <StatCard label="Strategies" value="Priority" hint="fallback on 429/5xx/timeout" />
        <StatCard label="Default Timeout" value={formatLatency(30000)} hint="per route, configurable" />
      </div>

      {!loading && !error && routes.length === 0 ? (
        <Card>
          <EmptyState
            icon={<RouteIcon size={20} />}
            title="No routes yet"
            description="Create a route alias so clients can call model=coding instead of a provider model."
            action="Create Route"
            onAction={openCreate}
          />
        </Card>
      ) : (
        <DataTable
          columns={columns}
          rows={routes}
          rowKey={(r) => r.id}
          loading={loading}
          error={error}
          onRetry={() => void load()}
          onRowClick={openEdit}
          footer={<span className="text-[12px] text-muted">Showing {routes.length} of {routes.length} routes</span>}
          actions={(r) => (
            <RowActions
              items={[
                { label: 'Edit route', onClick: () => openEdit(r) },
                { label: r.enabled ? 'Disable' : 'Enable', onClick: () => void toggle(r) },
                { label: 'Delete', destructive: true, onClick: () => setConfirmDelete(r) },
              ]}
            />
          )}
        />
      )}

      <Card>
        <CardHeader title="Route client usage" description="Replace the provider model string with your alias in any OpenAI-compatible client." />
        <CodeBlock
          language="python"
          code={`client.chat.completions.create(model="coding",\n    messages=[...])`}
        />
      </Card>

      <Drawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        title={editor.id ? `Edit route · ${editor.alias}` : 'Create route'}
        description="Alias, target chain, and Guard policy."
        footer={
          <>
            <Button variant="secondary" onClick={() => setDrawerOpen(false)} disabled={saving}>Cancel</Button>
            <Button variant="primary" onClick={() => void save()} loading={saving}>Save route</Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="Alias" required hint="Sent as `model` by clients.">
              <Input mono value={editor.alias} onChange={(e) => setEditor({ ...editor, alias: e.target.value, aliasAuto: false })} placeholder="coding" />
            </Field>
            <Field label="Guard policy">
              <Select value={editor.policy_id} onChange={(e) => setEditor({ ...editor, policy_id: e.target.value })}>
                <option value="">Global / inherit</option>
                {policies.map((p) => (
                  <option key={p.id} value={p.id}>{p.name}</option>
                ))}
              </Select>
            </Field>
          </div>

          <Field label="Description">
            <Textarea rows={2} value={editor.description} onChange={(e) => setEditor({ ...editor, description: e.target.value })} placeholder="High-precision code completion" />
          </Field>

          <Field label="Primary model" required>
            <Select
              value={editor.primary}
              onChange={(e) => {
                const m = models.find((x) => x.id === e.target.value);
                setEditor((prev) => ({
                  ...prev,
                  primary: e.target.value,
                  // Suggest the alias from the chosen model so creating a
                  // route is one pick instead of typing a name manually.
                  // Only when the alias is empty or was an earlier suggestion.
                  alias: !prev.alias || prev.aliasAuto ? (m ? suggestAlias(m.model_id) : prev.alias) : prev.alias,
                  aliasAuto: !!m && (!prev.alias || prev.aliasAuto),
                  fallbacks: prev.fallbacks.filter((f) => f !== e.target.value),
                }));
              }}
            >
              <option value="">Select a model…</option>
              {models.map((m) => (
                <option key={m.id} value={m.id}>{m.provider_name} · {m.model_id}</option>
              ))}
            </Select>
          </Field>

          <div>
            <div className="mb-1.5 flex items-center justify-between">
              <span className="text-small font-medium text-text">Fallback chain</span>
              <span className="text-[11px] text-muted">{editor.fallbacks.length} target(s)</span>
            </div>
            <ul className="flex flex-col gap-1.5">
              {editor.fallbacks.map((id, idx) => (
                <li
                  key={id}
                  draggable
                  onDragStart={(e) => e.dataTransfer.setData('text/plain', String(idx))}
                  onDragOver={(e) => e.preventDefault()}
                  onDrop={(e) => {
                    e.preventDefault();
                    const from = Number(e.dataTransfer.getData('text/plain'));
                    if (!Number.isNaN(from)) moveFallback(from, idx);
                  }}
                  className="flex items-center gap-2 rounded-control border border-border bg-bg px-2.5 py-1.5"
                >
                  <GripVertical size={14} className="cursor-grab text-muted" />
                  <span className="w-4 text-[11px] text-muted tabular">{idx + 1}</span>
                  <span className="flex-1 font-mono text-[12px] text-text">{modelLabel(id)}</span>
                  <button
                    type="button"
                    onClick={() => setEditor({ ...editor, fallbacks: editor.fallbacks.filter((f) => f !== id) })}
                    className="rounded p-1 text-muted hover:bg-danger-soft hover:text-danger"
                    aria-label="Remove fallback"
                  >
                    <X size={13} />
                  </button>
                </li>
              ))}
            </ul>
            <div className="mt-2 flex items-center gap-2">
              <Select
                value=""
                onChange={(e) => {
                  if (e.target.value) setEditor({ ...editor, fallbacks: [...editor.fallbacks, e.target.value] });
                }}
                className="flex-1"
              >
                <option value="">Add fallback…</option>
                {availableFallbacks.map((m) => (
                  <option key={m.id} value={m.id}>{m.provider_name} · {m.model_id}</option>
                ))}
              </Select>
            </div>
          </div>

          <div>
            <p className="mb-1.5 text-small font-medium text-text">Fallback conditions</p>
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
              {fallbackConditions.map((c) => (
                <Checkbox
                  key={c.key}
                  label={c.label}
                  checked={editor.fallback_on.includes(c.key)}
                  onChange={(checked) =>
                    setEditor({
                      ...editor,
                      fallback_on: checked ? [...editor.fallback_on, c.key] : editor.fallback_on.filter((k) => k !== c.key),
                    })
                  }
                />
              ))}
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <Field label="Retry per target" hint="0–5 attempts.">
              <Input type="number" min={0} max={5} value={editor.retry} onChange={(e) => setEditor({ ...editor, retry: Number(e.target.value) })} />
            </Field>
            <Field label="Timeout (ms)" hint="Total request deadline.">
              <Input type="number" min={1000} step={1000} value={editor.timeout_ms} onChange={(e) => setEditor({ ...editor, timeout_ms: Number(e.target.value) })} />
            </Field>
          </div>

          <CodeBlock
            language="bash"
            code={`curl http://localhost:20128/v1/chat/completions \\\n  -H "Authorization: Bearer mr-sk-..." \\\n  -d '{"model": "${editor.alias || 'coding'}", "messages": [{"role":"user","content":"Hello"}]}'`}
          />

          {formError && <p className="text-[12px] text-danger">{formError}</p>}
        </div>
      </Drawer>

      <ConfirmDialog
        open={confirmAuto}
        onClose={() => setConfirmAuto(false)}
        onConfirm={() => void autoAdd()}
        title="Auto-add routes"
        message={`Create one route per discovered model (${models.length} available) with defaults: alias = model id, no fallbacks, global policy. Existing routes are kept. You can edit or delete them later.`}
        confirmLabel={`Add ${models.length} routes`}
        loading={autoAdding}
      />

      <ConfirmDialog
        open={!!confirmDelete}
        onClose={() => setConfirmDelete(null)}
        onConfirm={() => void remove()}
        title="Delete route"
        message={`Delete route “${confirmDelete?.alias}”? Clients calling this alias will receive 404 model_not_found.`}
        confirmLabel="Delete route"
        loading={deleting}
      />
    </div>
  );
}


