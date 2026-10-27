import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { GripVertical, Plus, Route as RouteIcon, Search, X, Zap } from 'lucide-react';
import { PageHeader, StatCard, Card, CardHeader, EmptyState, Callout } from '../components/ui/Layout';
import { Button } from '../components/ui/Button';
import { Badge } from '../components/ui/Badge';
import { DataTable, RowActions, type Column } from '../components/ui/DataTable';
import { Drawer, ConfirmDialog } from '../components/ui/Overlay';
import { Field, Input, Select, Switch, Checkbox, Textarea } from '../components/ui/Field';
import { CodeBlock } from '../components/ui/CopyField';
import { useToast } from '../components/ui/Toast';
import { admin, ApiError } from '../lib/api';
import type { Model, Policy, Provider, Route } from '../lib/types';
import { classNames, fallbacksOf, formatLatency, isValidAlias, primaryOf, routeHasUsableTarget, routeIsOrphaned } from '../lib/format';

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

// Small tri-state checkbox used for bulk selection. It lives outside the row's
// click target so selecting a route never opens its editor drawer.
function SelectionBox({
  checked,
  indeterminate,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  indeterminate?: boolean;
  onChange: (checked: boolean) => void;
  label: string;
  disabled?: boolean;
}) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = !!indeterminate && !checked;
  }, [indeterminate, checked]);
  return (
    <span
      className="inline-flex items-center justify-center"
      onClick={(e) => e.stopPropagation()}
      onPointerDown={(e) => e.stopPropagation()}
    >
      <input
        ref={ref}
        type="checkbox"
        checked={checked}
        disabled={disabled}
        aria-label={label}
        onChange={(e) => onChange(e.target.checked)}
        className={classNames(
          'h-3.5 w-3.5 rounded-[3px] border-border',
          disabled ? 'cursor-not-allowed opacity-40' : 'cursor-pointer',
        )}
        style={{ accentColor: 'rgb(var(--c-accent))' }}
      />
    </span>
  );
}

export function RoutesPage() {
  const toast = useToast();
  const [routes, setRoutes] = useState<Route[]>([]);
  const [models, setModels] = useState<Model[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editor, setEditor] = useState<EditorState>(emptyEditor);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState('');
  const [confirmDelete, setConfirmDelete] = useState<Route | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [autoOpen, setAutoOpen] = useState(false);
  const [autoAdding, setAutoAdding] = useState(false);
  const [autoQuery, setAutoQuery] = useState('');
  const [autoSelected, setAutoSelected] = useState<Set<string>>(() => new Set());
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [bulkBusy, setBulkBusy] = useState<'enable' | 'disable' | 'delete' | null>(null);
  const [bulkDeleteOpen, setBulkDeleteOpen] = useState(false);
  // Route ids with an in-flight enable/disable PATCH. A set (not a single id)
  // so several rows can be toggled while the first request is still open.
  const [togglingIds, setTogglingIds] = useState<Set<string>>(() => new Set());

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [r, m, p, pr] = await Promise.all([
        admin.routes.list(),
        admin.models.list(),
        admin.policies.list(),
        admin.providers.list(),
      ]);
      setRoutes(r.data ?? []);
      setModels(m.data ?? []);
      setPolicies(p.data ?? []);
      setProviders(pr.data ?? []);
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

  // Auto-add must offer the same set the backend will accept: an enable-able
  // model reachable through an active provider. Models already used as a
  // primary are marked so the default selection only covers missing routes.
  const providerEnabled = useMemo(
    () => new Map(providers.map((p) => [p.id, p.enabled])),
    [providers],
  );
  const routedModelIds = useMemo(() => {
    const ids = new Set<string>();
    for (const r of routes) {
      // Include fallbacks: a model already reachable through another alias
      // does not need an auto-created duplicate route.
      for (const target of r.targets) ids.add(target.model_id);
    }
    return ids;
  }, [routes]);
  const eligibleModels = useMemo(
    () =>
      models
        .filter((m) => m.available && providerEnabled.get(m.provider_id) !== false)
        .sort((a, b) =>
          (a.provider_name ?? a.provider_id).localeCompare(b.provider_name ?? b.provider_id) ||
          a.model_id.localeCompare(b.model_id),
        ),
    [models, providerEnabled],
  );
  const missingModels = useMemo(
    () => eligibleModels.filter((m) => !routedModelIds.has(m.id)),
    [eligibleModels, routedModelIds],
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

  // Optimistic enable toggle: the switch flips immediately, the PATCH runs in
  // the background, and only a failure reverts the row. No list reload here, so
  // the switch never flickers or jumps mid-request.
  async function toggle(route: Route) {
    if (togglingIds.has(route.id)) return;
    const next = !route.enabled;
    setRoutes((prev) => prev.map((r) => (r.id === route.id ? { ...r, enabled: next } : r)));
    setTogglingIds((prev) => {
      const set = new Set(prev);
      set.add(route.id);
      return set;
    });
    try {
      await admin.routes.update(route.id, { enabled: next });
    } catch (err) {
      setRoutes((prev) => prev.map((r) => (r.id === route.id ? { ...r, enabled: route.enabled } : r)));
      toast.error(err instanceof ApiError ? err.message : 'Update failed');
    } finally {
      setTogglingIds((prev) => {
        const set = new Set(prev);
        set.delete(route.id);
        return set;
      });
    }
  }

  function openAuto() {
    setAutoQuery('');
    // Free models are the safe default on this gateway: they are the routes
    // the operator asked to keep. Paid models stay one explicit click away.
    setAutoSelected(new Set(missingModels.filter((m) => m.model_id.toLowerCase().includes('free')).map((m) => m.id)));
    setAutoOpen(true);
  }

  function toggleAutoModel(id: string, checked: boolean) {
    setAutoSelected((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  function toggleAutoGroup(ids: string[], checked: boolean) {
    setAutoSelected((prev) => {
      const next = new Set(prev);
      for (const id of ids) {
        if (checked) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  }

  // Selected-models setup: one route per model with defaults (alias = model
  // id, no fallbacks, global policy). Idempotent — the backend still skips
  // anything that gained a route meanwhile, so re-clicking is safe.
  async function autoAdd() {
    if (autoSelected.size === 0) return;
    setAutoAdding(true);
    try {
      const res = await admin.routes.auto({ model_ids: [...autoSelected] });
      const parts = [`${res.created} route${res.created === 1 ? '' : 's'} created`];
      if (res.skipped) parts.push(`${res.skipped} skipped`);
      toast.success(parts.join(', '), {
        title: res.created ? 'Routes added' : 'Nothing new to add',
        description: res.created
          ? 'Each selected model now has its own alias. Edit them to add fallbacks.'
          : 'Every selected model already has a route or is not eligible right now.',
        ...(res.created ? {} : { duration: 6500 }),
      });
      setAutoOpen(false);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Auto-add failed', {
        title: 'Could not add routes',
      });
    } finally {
      setAutoAdding(false);
    }
  }

  async function bulk(action: 'enable' | 'disable' | 'delete') {
    const ids = [...selected];
    if (ids.length === 0) return;
    setBulkBusy(action);
    try {
      const res = await admin.routes.bulk({ action, ids });
      const changed = action === 'delete' ? (res.deleted ?? ids.length) : (res.updated ?? ids.length);
      const noun = `route${changed === 1 ? '' : 's'}`;
      if (action === 'delete') {
        toast.success(`${changed} ${noun} deleted`, {
          title: 'Routes deleted',
          description: 'Clients calling a removed alias now receive 404 model_not_found.',
        });
      } else {
        toast.success(`${changed} ${noun} ${action}d`, {
          title: action === 'enable' ? 'Routes enabled' : 'Routes disabled',
          description:
            action === 'enable'
              ? 'They are immediately available to clients again.'
              : 'They stay in place but no longer serve requests.',
        });
      }
      setSelected(new Set());
      setBulkDeleteOpen(false);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : `Bulk ${action} failed`, {
        title: 'Bulk action failed',
      });
    } finally {
      setBulkBusy(null);
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

  // A route is only shown when at least one target can actually serve: the API
  // marks a target unavailable when its provider is disabled, so disabling a
  // provider takes its routes out of this page without deleting them.
  const hiddenRoutes = routes.filter(routeIsOrphaned);
  const visibleRoutes = routes.filter((r) => !routeIsOrphaned(r));
  const enabledCount = visibleRoutes.filter((r) => r.enabled && routeHasUsableTarget(r)).length;
  const totalFallbacks = visibleRoutes.reduce((acc, r) => acc + fallbacksOf(r).length, 0);

  // Drop ids that no longer exist (deleted elsewhere or hidden because their
  // provider went down) so the bulk toolbar never acts on a stale row.
  useEffect(() => {
    setSelected((prev) => {
      if (prev.size === 0) return prev;
      const live = new Set(routes.filter((r) => !routeIsOrphaned(r)).map((r) => r.id));
      const next = new Set([...prev].filter((id) => live.has(id)));
      return next.size === prev.size ? prev : next;
    });
  }, [routes]);

  const allVisibleSelected = visibleRoutes.length > 0 && visibleRoutes.every((r) => selected.has(r.id));
  const someVisibleSelected = visibleRoutes.some((r) => selected.has(r.id));
  const selectedRoutes = visibleRoutes.filter((r) => selected.has(r.id));
  const autoFiltered = useMemo(() => {
    const q = autoQuery.trim().toLowerCase();
    if (!q) return eligibleModels;
    return eligibleModels.filter(
      (m) =>
        m.model_id.toLowerCase().includes(q) ||
        (m.provider_name ?? '').toLowerCase().includes(q) ||
        m.display_name.toLowerCase().includes(q),
    );
  }, [eligibleModels, autoQuery]);
  const autoGroups = useMemo(() => {
    const groups = new Map<string, Model[]>();
    for (const m of autoFiltered) {
      const name = m.provider_name || m.provider_id;
      const list = groups.get(name);
      if (list) list.push(m);
      else groups.set(name, [m]);
    }
    return [...groups.entries()];
  }, [autoFiltered]);

  function toggleRow(id: string, checked: boolean) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  function toggleAllVisible(checked: boolean) {
    setSelected((prev) => {
      const next = new Set(prev);
      for (const r of visibleRoutes) {
        if (checked) next.add(r.id);
        else next.delete(r.id);
      }
      return next;
    });
  }

  const columns: Column<Route>[] = [
    {
      key: 'select',
      width: '36px',
      header: (
        <SelectionBox
          checked={allVisibleSelected}
          indeterminate={someVisibleSelected}
          onChange={toggleAllVisible}
          label="Select all routes"
        />
      ),
      render: (r) => (
        <SelectionBox
          checked={selected.has(r.id)}
          onChange={(checked) => toggleRow(r.id, checked)}
          label={`Select ${r.alias}`}
        />
      ),
    },
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
          <span className="text-[11.5px] text-muted">None</span>
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
      render: (r) => (
        // The row has an onRowClick that opens the editor drawer. Stop both
        // click and pointer-down here so flipping the switch never opens it.
        <span
          className="inline-flex"
          onClick={(e) => e.stopPropagation()}
          onPointerDown={(e) => e.stopPropagation()}
        >
          <Switch
            size="sm"
            checked={r.enabled}
            disabled={togglingIds.has(r.id)}
            onChange={() => void toggle(r)}
            label={`Toggle ${r.alias}`}
          />
        </span>
      ),
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
              onClick={openAuto}
              disabled={missingModels.length === 0}
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
        <StatCard label="Active Routes" value={String(enabledCount)} hint={`${routes.length} total${hiddenRoutes.length ? `, ${hiddenRoutes.length} inactive` : ''}`} tone="success" icon={<RouteIcon size={15} />} />
        <StatCard label="Fallback Targets" value={String(totalFallbacks)} hint="across visible routes" />
        <StatCard label="Strategies" value="Priority" hint="fallback on 429/5xx/timeout" />
        <StatCard label="Default Timeout" value={formatLatency(30000)} hint="per route, configurable" />
      </div>

      {hiddenRoutes.length > 0 && (
        <Callout tone="warning">
          {hiddenRoutes.length} route disembunyikan karena semua provider targetnya nonaktif. Aktifkan provider di halaman Providers untuk memunculkannya lagi.
        </Callout>
      )}

      {selected.size > 0 && (
        <div
          role="region"
          aria-label="Bulk route actions"
          className="flex flex-wrap items-center gap-2 rounded-card border border-accent/30 bg-accent-soft px-3 py-2"
        >
          <span className="text-[12.5px] font-semibold text-accent">
            {selected.size} dipilih
          </span>
          <span className="hidden h-4 w-px bg-accent/25 sm:block" aria-hidden />
          <Button
            size="compact"
            variant="secondary"
            disabled={bulkBusy !== null}
            loading={bulkBusy === 'enable'}
            onClick={() => void bulk('enable')}
          >
            Enable
          </Button>
          <Button
            size="compact"
            variant="secondary"
            disabled={bulkBusy !== null}
            loading={bulkBusy === 'disable'}
            onClick={() => void bulk('disable')}
          >
            Disable
          </Button>
          <Button
            size="compact"
            variant="danger"
            disabled={bulkBusy !== null}
            loading={bulkBusy === 'delete'}
            onClick={() => setBulkDeleteOpen(true)}
          >
            Delete
          </Button>
          <button
            type="button"
            onClick={() => setSelected(new Set())}
            disabled={bulkBusy !== null}
            className="ml-auto rounded-control px-2 py-1 text-[12px] text-muted transition-colors hover:bg-bg/60 hover:text-text disabled:opacity-50"
          >
            Clear selection
          </button>
        </div>
      )}

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
          rows={visibleRoutes}
          rowKey={(r) => r.id}
          loading={loading}
          error={error}
          onRetry={() => void load()}
          onRowClick={openEdit}
          empty={
            <div className="flex flex-col items-center gap-2 py-10">
              <RouteIcon size={20} className="text-muted" />
              <p className="text-[13px] font-medium text-text">Tidak ada route yang bisa dipakai</p>
              <p className="text-[12px] text-muted">Semua route menunjuk ke provider yang nonaktif.</p>
            </div>
          }
          footer={<span className="text-[12px] text-muted">Showing {visibleRoutes.length} of {routes.length} routes</span>}
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

      <Drawer
        open={autoOpen}
        onClose={() => setAutoOpen(false)}
        title="Auto-add routes"
        description="Free models are preselected. Pick any other model to expose it too. Only active providers are listed."
        footer={
          <>
            <span className="mr-auto text-[12px] text-muted">
              {autoSelected.size} selected · {missingModels.length} without a route
            </span>
            <Button variant="secondary" onClick={() => setAutoOpen(false)} disabled={autoAdding}>Cancel</Button>
            <Button
              variant="primary"
              onClick={() => void autoAdd()}
              loading={autoAdding}
              disabled={autoSelected.size === 0}
            >
              {autoSelected.size > 0
                ? `Add ${autoSelected.size} route${autoSelected.size === 1 ? '' : 's'}`
                : 'Add routes'}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-3">
          <div className="relative">
            <Search size={14} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
            <Input
              value={autoQuery}
              onChange={(e) => setAutoQuery(e.target.value)}
              placeholder="Search model, display name, or provider…"
              className="pl-8"
              aria-label="Search models to auto-add"
            />
          </div>

          <div className="flex items-center justify-between gap-2 text-[11.5px] text-muted">
            <span>{autoFiltered.length} eligible model(s)</span>
            <div className="flex items-center gap-3">
              <button
                type="button"
                onClick={() =>
                  setAutoSelected(new Set(autoFiltered.filter((m) => !routedModelIds.has(m.id)).map((m) => m.id)))
                }
                className="text-accent transition-colors hover:text-accent-hover"
              >
                Select all
              </button>
              <button
                type="button"
                onClick={() => setAutoSelected(new Set())}
                className="text-muted transition-colors hover:text-text"
              >
                Clear
              </button>
            </div>
          </div>

          {autoFiltered.length === 0 ? (
            <div className="rounded-card border border-border px-4 py-8 text-center">
              <p className="text-[13px] font-medium text-text">No matching models</p>
              <p className="mt-0.5 text-[12px] text-muted">
                Try a different search, or add/enable a provider first.
              </p>
            </div>
          ) : (
            autoGroups.map(([provider, list]) => {
              const selectable = list.filter((m) => !routedModelIds.has(m.id));
              const selectedCount = selectable.filter((m) => autoSelected.has(m.id)).length;
              return (
                <section key={provider} className="overflow-hidden rounded-card border border-border">
                  <header className="flex items-center justify-between gap-2 border-b border-border bg-surface px-3 py-2">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="truncate text-[12.5px] font-semibold text-text">{provider}</span>
                      <span className="rounded-pill bg-bg px-1.5 py-px font-mono text-[10px] text-muted">
                        {list.length}
                      </span>
                    </div>
                    <div className="flex items-center gap-2">
                      <span className="text-[11px] text-muted">{selectedCount}/{selectable.length}</span>
                      <SelectionBox
                        checked={selectable.length > 0 && selectedCount === selectable.length}
                        indeterminate={selectedCount > 0}
                        onChange={(checked) => toggleAutoGroup(selectable.map((m) => m.id), checked)}
                        label={`Select all ${provider} models`}
                        disabled={selectable.length === 0}
                      />
                    </div>
                  </header>
                  <ul className="divide-y divide-border">
                    {list.map((m) => {
                      const routed = routedModelIds.has(m.id);
                      const checked = autoSelected.has(m.id);
                      return (
                        <li key={m.id}>
                          <label
                            className={classNames(
                              'flex items-center gap-2.5 px-3 py-2 transition-colors',
                              routed ? 'cursor-not-allowed opacity-60' : 'cursor-pointer hover:bg-surface-2',
                            )}
                          >
                            <SelectionBox
                              checked={checked}
                              onChange={(next) => toggleAutoModel(m.id, next)}
                              label={`Add route for ${m.model_id}`}
                              disabled={routed}
                            />
                            <span className="min-w-0 flex-1">
                              <span className="block truncate font-mono text-[12px] text-text">{m.model_id}</span>
                              <span className="block truncate text-[10.5px] text-muted">
                                {m.display_name && m.display_name !== m.model_id ? `${m.display_name} · ` : ''}
                                {m.context_window ? `${m.context_window.toLocaleString()} ctx` : 'No context metadata'}
                              </span>
                            </span>
                            {routed && <Badge tone="neutral" dot={false}>Has route</Badge>}
                          </label>
                        </li>
                      );
                    })}
                  </ul>
                </section>
              );
            })
          )}
        </div>
      </Drawer>

      <ConfirmDialog
        open={bulkDeleteOpen}
        onClose={() => (bulkBusy ? undefined : setBulkDeleteOpen(false))}
        onConfirm={() => void bulk('delete')}
        title={`Delete ${selected.size} route${selected.size === 1 ? '' : 's'}`}
        message={`Clients calling ${selected.size === 1 ? 'this alias' : 'these aliases'} will receive 404 model_not_found. This cannot be undone.`}
        details={
          <ul className="flex flex-wrap gap-1.5" aria-label="Routes to delete">
            {selectedRoutes.slice(0, 6).map((r) => (
              <li key={r.id} className="rounded-control border border-border bg-surface px-1.5 py-0.5 font-mono text-[11px] text-text">
                {r.alias}
              </li>
            ))}
            {selectedRoutes.length > 6 && (
              <li className="px-1 py-0.5 text-[11px] text-muted">+{selectedRoutes.length - 6} more</li>
            )}
          </ul>
        }
        confirmLabel={`Delete ${selected.size} route${selected.size === 1 ? '' : 's'}`}
        loading={bulkBusy === 'delete'}
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


