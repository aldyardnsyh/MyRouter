import { useCallback, useEffect, useMemo, useState } from 'react';
import { AlertTriangle, KeyRound, Laptop, Plus } from 'lucide-react';
import { PageHeader, StatCard, Callout } from '../components/ui/Layout';
import { Button } from '../components/ui/Button';
import { Badge, StatusBadge } from '../components/ui/Badge';
import { DataTable, RowActions, type Column } from '../components/ui/DataTable';
import { Modal, ConfirmDialog } from '../components/ui/Overlay';
import { Field, Input, Select } from '../components/ui/Field';
import { SecretReveal } from '../components/ui/CopyField';
import { ConnectPanel } from '../components/ConnectPanel';
import { useToast } from '../components/ui/Toast';
import { admin, ApiError } from '../lib/api';
import { useQuery } from '../lib/useQuery';
import type { APIKey, Policy, Route } from '../lib/types';
import { formatDate, keyStatus, relativeTime, splitCSV } from '../lib/format';

interface KeyForm {
  name: string;
  policy_id: string;
  allowed_routes: string[];
  expires_days: string;
}

const emptyForm: KeyForm = { name: '', policy_id: '', allowed_routes: [], expires_days: '' };

export function ApiKeysPage() {
  const toast = useToast();
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [routes, setRoutes] = useState<Route[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [modalOpen, setModalOpen] = useState(false);
  const [form, setForm] = useState<KeyForm>(emptyForm);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState('');
  const [secret, setSecret] = useState<string | null>(null);
  const [assignFor, setAssignFor] = useState<APIKey | null>(null);
  const [assignPolicy, setAssignPolicy] = useState('');
  const [confirmRevoke, setConfirmRevoke] = useState<APIKey | null>(null);
  const [revoking, setRevoking] = useState(false);
  const [syncingOpencode, setSyncingOpencode] = useState(false);

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [k, p, r] = await Promise.all([admin.keys.list(), admin.policies.list(), admin.routes.list()]);
      setKeys(k.data ?? []);
      setPolicies(p.data ?? []);
      setRoutes(r.data ?? []);
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to load API keys');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // Base URL for the connect panel (custom domain from Settings, else origin).
  const { data: overview } = useQuery(() => admin.overview(), { cacheKey: 'overview' });

  const stats = useMemo(() => {
    const active = keys.filter((k) => k.enabled).length;
    const scoped = keys.filter((k) => k.policy_id).length;
    return { active, scoped };
  }, [keys]);

  async function createKey() {
    setFormError('');
    if (!form.name.trim()) {
      setFormError('Key name is required.');
      return;
    }
    setSaving(true);
    try {
      const expires = form.expires_days
        ? Math.floor(Date.now() / 1000) + Number(form.expires_days) * 86400
        : undefined;
      const res = await admin.keys.create({
        name: form.name.trim(),
        policy_id: form.policy_id || undefined,
        allowed_routes: form.allowed_routes,
        expires_at: expires,
      });
      setSecret(res.secret);
      await load(true);
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : 'Could not create key');
    } finally {
      setSaving(false);
    }
  }

  async function toggleKey(k: APIKey) {
    try {
      await admin.keys.update(k.id, { name: k.name, enabled: !k.enabled, allowed_routes: splitCSV(k.allowed_routes) });
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Update failed');
    }
  }

  // Optional convenience: writes this key plus every enabled route into the
  // OpenCode config. Never auto-called — sync tanpa api_key tetap jalan
  // (hanya update models), jadi tidak ada key baru yang terbuat sia-sia.
  async function syncNewKeyToOpencode() {
    if (!secret) return;
    setSyncingOpencode(true);
    try {
      const res = await admin.opencode.sync({ api_key: secret });
      toast.success(
        `OpenCode diperbarui: key ditulis, ${res.data.added.length} model baru ditambahkan. Restart OpenCode.`,
      );
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Sync ke OpenCode gagal');
    } finally {
      setSyncingOpencode(false);
    }
  }

  async function revoke() {
    if (!confirmRevoke) return;
    setRevoking(true);
    try {
      await admin.keys.revoke(confirmRevoke.id);
      toast.success('Key revoked');
      setConfirmRevoke(null);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Revoke failed');
    } finally {
      setRevoking(false);
    }
  }

  async function savePolicyAssignment() {
    if (!assignFor) return;
    try {
      await admin.keys.update(assignFor.id, {
        name: assignFor.name,
        enabled: assignFor.enabled,
        policy_id: assignPolicy,
        allowed_routes: splitCSV(assignFor.allowed_routes),
      });
      toast.success('Policy assigned');
      setAssignFor(null);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Update failed');
    }
  }

  const columns: Column<APIKey>[] = [
    { key: 'name', header: 'Name', render: (k) => <span className="font-medium text-text">{k.name}</span> },
    { key: 'prefix', header: 'Key prefix', render: (k) => <code className="font-mono text-[11.5px] text-muted">{k.prefix}****</code> },
    { key: 'status', header: 'Status', render: (k) => <StatusBadge spec={keyStatus(k)} /> },
    { key: 'last', header: 'Last used', render: (k) => <span className="font-mono text-[11.5px] text-muted">{relativeTime(k.last_used_at)}</span> },
    { key: 'expires', header: 'Expires', render: (k) => <span className="font-mono text-[11.5px] text-muted">{k.expires_at ? formatDate(k.expires_at) : 'Never'}</span> },
    { key: 'policy', header: 'Policy', render: (k) => <Badge tone={k.policy_name ? 'accent' : 'neutral'} dot={false}>{k.policy_name || 'Global'}</Badge> },
    {
      key: 'routes',
      header: 'Allowed routes',
      render: (k) => {
        const list = splitCSV(k.allowed_routes);
        return list.length ? (
          <div className="flex flex-wrap gap-1">
            {list.map((r) => (
              <span key={r} className="rounded-pill bg-surface-2 px-1.5 py-0.5 font-mono text-[10.5px] text-muted">{r}</span>
            ))}
          </div>
        ) : (
          <span className="text-[11.5px] text-muted">All routes</span>
        );
      },
    },
  ];

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="API Keys"
        description="Client credentials for the gateway. Secrets are shown once and stored only as hashes."
        actions={
          <Button variant="primary" size="compact" icon={<Plus size={14} />} onClick={() => { setForm(emptyForm); setSecret(null); setFormError(''); setModalOpen(true); }}>
            Create Key
          </Button>
        }
      />

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-3">
        <StatCard label="Total Keys" value={String(keys.length)} icon={<KeyRound size={15} />} />
        <StatCard label="Active" value={String(stats.active)} tone="success" hint="enabled and unexpired" />
        <StatCard label="With Policy" value={String(stats.scoped)} hint="Guard-limited keys" />
      </div>

      <DataTable
        columns={columns}
        rows={keys}
        rowKey={(k) => k.id}
        loading={loading}
        error={error}
        onRetry={() => void load()}
        empty={
          <div className="flex flex-col items-center gap-2 py-12">
            <KeyRound size={20} className="text-muted" />
            <p className="text-[13px] font-medium text-text">No API keys yet</p>
            <p className="text-[12px] text-muted">Create a key to let a client call the gateway.</p>
            <Button variant="primary" size="compact" className="mt-2" onClick={() => setModalOpen(true)}>Create Key</Button>
          </div>
        }
        actions={(k) => (
          <RowActions
            items={[
              { label: 'Assign policy', onClick: () => { setAssignFor(k); setAssignPolicy(k.policy_id ?? ''); } },
              { label: k.enabled ? 'Disable' : 'Enable', onClick: () => void toggleKey(k) },
              { label: 'Revoke', destructive: true, onClick: () => setConfirmRevoke(k) },
            ]}
          />
        )}
      />

      <Modal
        open={modalOpen}
        onClose={() => setModalOpen(false)}
        title={secret ? 'API key created' : 'Create API key'}
        description={secret ? undefined : 'The secret is displayed exactly once.'}
        footer={
          secret ? (
            <Button variant="primary" onClick={() => setModalOpen(false)}>Done</Button>
          ) : (
            <>
              <Button variant="secondary" onClick={() => setModalOpen(false)} disabled={saving}>Cancel</Button>
              <Button variant="primary" onClick={() => void createKey()} loading={saving}>Create key</Button>
            </>
          )
        }
      >
        {secret ? (
          <div className="flex flex-col gap-3">
            <Callout tone="warning" icon={<AlertTriangle size={14} />}>
              Copy this key now. You will not see it again.
            </Callout>
            <SecretReveal value={secret} hint="Send it as: Authorization: Bearer <key>" />
            <ConnectPanel baseUrl={overview?.base_url ?? `${window.location.origin}/v1`} apiKey={secret} compact />
            <div className="flex flex-wrap items-center gap-2 border-t border-border pt-3">
              <Button
                variant="secondary"
                size="compact"
                loading={syncingOpencode}
                onClick={() => void syncNewKeyToOpencode()}
              >
                <Laptop size={14} /> Write key + models to OpenCode
              </Button>
              <span className="text-[11.5px] text-muted">Updates the myrouter provider in opencode.json.</span>
            </div>
          </div>
        ) : (
          <div className="flex flex-col gap-3.5">
            <Field label="Key name" required>
              <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="team-laptop" />
            </Field>
            <Field label="Guard policy" hint="Controls rate limits for this key.">
              <Select value={form.policy_id} onChange={(e) => setForm({ ...form, policy_id: e.target.value })}>
                <option value="">Global / inherit</option>
                {policies.map((p) => (
                  <option key={p.id} value={p.id}>{p.name}</option>
                ))}
              </Select>
            </Field>
            <Field label="Allowed routes" hint="Leave empty to allow every route.">
              <div className="flex flex-wrap gap-1.5">
                {routes.map((r) => {
                  const active = form.allowed_routes.includes(r.alias);
                  return (
                    <button
                      key={r.id}
                      type="button"
                      onClick={() =>
                        setForm({
                          ...form,
                          allowed_routes: active ? form.allowed_routes.filter((a) => a !== r.alias) : [...form.allowed_routes, r.alias],
                        })
                      }
                      className={
                        active
                          ? 'rounded-pill border border-accent bg-accent-soft px-2.5 py-1 font-mono text-[11px] text-accent'
                          : 'rounded-pill border border-border bg-bg px-2.5 py-1 font-mono text-[11px] text-muted hover:bg-surface-2'
                      }
                    >
                      {r.alias}
                    </button>
                  );
                })}
                {routes.length === 0 && <span className="text-[11.5px] text-muted">No routes defined yet.</span>}
              </div>
            </Field>
            <Field label="Expires in" hint="Leave empty for a non-expiring key.">
              <Select value={form.expires_days} onChange={(e) => setForm({ ...form, expires_days: e.target.value })}>
                <option value="">Never</option>
                <option value="7">7 days</option>
                <option value="30">30 days</option>
                <option value="90">90 days</option>
                <option value="365">1 year</option>
              </Select>
            </Field>
            {formError && <p className="text-[12px] text-danger">{formError}</p>}
          </div>
        )}
      </Modal>

      <Modal
        open={!!assignFor}
        onClose={() => setAssignFor(null)}
        title="Assign policy"
        description={assignFor?.name}
        size="sm"
        footer={<><Button variant="secondary" onClick={() => setAssignFor(null)}>Cancel</Button><Button variant="primary" onClick={() => void savePolicyAssignment()}>Save</Button></>}
      >
        <Field label="Guard policy">
          <Select value={assignPolicy} onChange={(e) => setAssignPolicy(e.target.value)}>
            <option value="">Global / inherit</option>
            {policies.map((p) => (
              <option key={p.id} value={p.id}>{p.name}</option>
            ))}
          </Select>
        </Field>
      </Modal>

      <ConfirmDialog
        open={!!confirmRevoke}
        onClose={() => setConfirmRevoke(null)}
        onConfirm={() => void revoke()}
        title="Revoke API key"
        message={`Revoke "${confirmRevoke?.name}"? Clients using this key will immediately receive 401 invalid_api_key.`}
        confirmLabel="Revoke key"
        loading={revoking}
      />
    </div>
  );
}

