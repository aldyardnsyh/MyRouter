import { useCallback, useEffect, useMemo, useState } from 'react';
import { Globe, MonitorSmartphone, Plus, Shield, ShieldAlert, ShieldCheck } from 'lucide-react';
import { PageHeader, StatCard, Card, CardHeader, EmptyState, Callout, FormSection } from '../components/ui/Layout';
import { Button } from '../components/ui/Button';
import { Badge, StatusBadge } from '../components/ui/Badge';
import { DataTable, RowActions, Tabs, type Column } from '../components/ui/DataTable';
import { Drawer, ConfirmDialog, Modal } from '../components/ui/Overlay';
import { Field, Input, Select } from '../components/ui/Field';
import { useToast } from '../components/ui/Toast';
import { admin, ApiError } from '../lib/api';
import type { BlockEntry, Device, IPRule, Policy } from '../lib/types';
import { countdown, formatCompact, formatDateTime, formatNumber, isUnlimited, relativeTime } from '../lib/format';

const presets: { name: string; hint: string; value: Partial<Policy> }[] = [
  { name: 'Personal', hint: '60 RPM · 120K TPM', value: { rpm: 60, tpm: 120000, burst: 30 } },
  { name: 'Team Member', hint: '120 RPM · 240K TPM', value: { rpm: 120, tpm: 240000, burst: 60 } },
  { name: 'Strict', hint: '10 RPM · 20K TPM · 1 IP/device', value: { rpm: 10, tpm: 20000, burst: 5, max_ips: 1, max_devices: 1, max_concurrent: 2 } },
  { name: 'Unlimited', hint: 'No limits', value: { rpm: 0, tpm: 0, burst: 0, max_ips: 0, max_devices: 0, daily_tokens: 0, monthly_tokens: 0, max_concurrent: 0 } },
];

interface PolicyForm {
  id?: string;
  name: string;
  rpm: number;
  tpm: number;
  burst: number;
  daily_tokens: number;
  monthly_tokens: number;
  max_concurrent: number;
  max_tokens_per_request: number;
  max_ips: number;
  max_devices: number;
  on_exceed: 'reject' | 'queue' | 'fallback';
  queue_max_s: number;
  cooldown_s: number;
  auto_block_n: number;
  auto_block_m: number;
  auto_block_s: number;
}

const blankPolicy: PolicyForm = {
  name: '',
  rpm: 0,
  tpm: 0,
  burst: 0,
  daily_tokens: 0,
  monthly_tokens: 0,
  max_concurrent: 0,
  max_tokens_per_request: 0,
  max_ips: 0,
  max_devices: 0,
  on_exceed: 'reject',
  queue_max_s: 5,
  cooldown_s: 0,
  auto_block_n: 0,
  auto_block_m: 0,
  auto_block_s: 0,
};

export function GuardPage() {
  const toast = useToast();
  const [tab, setTab] = useState('policies');
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [ipRules, setIpRules] = useState<IPRule[]>([]);
  const [devices, setDevices] = useState<Device[]>([]);
  const [blocked, setBlocked] = useState<BlockEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [policyDrawer, setPolicyDrawer] = useState(false);
  const [policyForm, setPolicyForm] = useState<PolicyForm>(blankPolicy);
  const [policyError, setPolicyError] = useState('');
  const [savingPolicy, setSavingPolicy] = useState(false);
  const [confirmPolicy, setConfirmPolicy] = useState<Policy | null>(null);
  const [deletingPolicy, setDeletingPolicy] = useState(false);

  const [ruleForm, setRuleForm] = useState({ cidr: '', action: 'deny', note: '', expires_days: '' });
  const [ruleError, setRuleError] = useState('');
  const [addingRule, setAddingRule] = useState(false);
  const [confirmRule, setConfirmRule] = useState<IPRule | null>(null);
  const [deletingRule, setDeletingRule] = useState(false);

  const [renameDevice, setRenameDevice] = useState<Device | null>(null);
  const [renameValue, setRenameValue] = useState('');
  const [confirmDevice, setConfirmDevice] = useState<Device | null>(null);
  const [togglingDevice, setTogglingDevice] = useState(false);

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [p, r, d, b] = await Promise.all([
        admin.policies.list(),
        admin.ipRules.list(),
        admin.devices.list(),
        admin.blocked.list(),
      ]);
      setPolicies(p.data ?? []);
      setIpRules(r.data ?? []);
      setDevices(d.data ?? []);
      setBlocked(b.data ?? []);
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to load Guard data');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const stats = useMemo(() => {
    const limited = policies.filter((p) => !isUnlimited(p)).length;
    const denied = ipRules.filter((r) => r.action === 'deny').length;
    return { limited, denied };
  }, [policies, ipRules]);

  function openCreatePolicy() {
    setPolicyForm(blankPolicy);
    setPolicyError('');
    setPolicyDrawer(true);
  }

  function openEditPolicy(p: Policy) {
    setPolicyForm({
      id: p.id,
      name: p.name,
      rpm: p.rpm,
      tpm: p.tpm,
      burst: p.burst,
      daily_tokens: p.daily_tokens,
      monthly_tokens: p.monthly_tokens,
      max_concurrent: p.max_concurrent,
      max_tokens_per_request: p.max_tokens_per_request,
      max_ips: p.max_ips,
      max_devices: p.max_devices,
      on_exceed: p.on_exceed,
      queue_max_s: p.queue_max_s,
      cooldown_s: p.cooldown_s,
      auto_block_n: p.auto_block_n,
      auto_block_m: p.auto_block_m,
      auto_block_s: p.auto_block_s,
    });
    setPolicyError('');
    setPolicyDrawer(true);
  }

  async function savePolicy() {
    setPolicyError('');
    if (!policyForm.name.trim()) {
      setPolicyError('Policy name is required.');
      return;
    }
    setSavingPolicy(true);
    try {
      // Never send `id` in the body: the backend decodes with
      // DisallowUnknownFields, so an extra key makes the whole PATCH fail.
      const { id: _id, ...fields } = policyForm;
      const body = { ...fields, name: policyForm.name.trim() };
      if (policyForm.id) {
        await admin.policies.update(policyForm.id, body);
        toast.success('Policy updated');
      } else {
        await admin.policies.create(body);
        toast.success('Policy created');
      }
      setPolicyDrawer(false);
      await load(true);
    } catch (err) {
      const message = err instanceof ApiError ? err.message : 'Could not save policy';
      setPolicyError(message);
      toast.error(message);
    } finally {
      setSavingPolicy(false);
    }
  }

  async function deletePolicy() {
    if (!confirmPolicy) return;
    setDeletingPolicy(true);
    try {
      await admin.policies.remove(confirmPolicy.id);
      toast.success('Policy deleted');
      setConfirmPolicy(null);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Delete failed');
    } finally {
      setDeletingPolicy(false);
    }
  }

  function applyPreset(value: Partial<Policy>) {
    setPolicyForm((prev) => ({ ...prev, ...value }));
  }

  async function addRule() {
    setRuleError('');
    if (!ruleForm.cidr.trim()) {
      setRuleError('Enter a CIDR or IP address.');
      return;
    }
    setAddingRule(true);
    try {
      await admin.ipRules.create({
        cidr: ruleForm.cidr.trim(),
        action: ruleForm.action,
        note: ruleForm.note.trim(),
        scope_type: 'global',
        expires_at: ruleForm.expires_days ? Math.floor(Date.now() / 1000) + Number(ruleForm.expires_days) * 86400 : undefined,
      });
      setRuleForm({ cidr: '', action: 'deny', note: '', expires_days: '' });
      toast.success('IP rule added');
      await load(true);
    } catch (err) {
      setRuleError(err instanceof ApiError ? err.message : 'Could not add rule');
    } finally {
      setAddingRule(false);
    }
  }

  async function removeRule() {
    if (!confirmRule) return;
    setDeletingRule(true);
    try {
      await admin.ipRules.remove(confirmRule.id);
      toast.success('IP rule deleted');
      setConfirmRule(null);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Delete failed');
    } finally {
      setDeletingRule(false);
    }
  }

  async function toggleDevice(d: Device) {
    setTogglingDevice(true);
    try {
      await admin.devices.block(d.id, !d.blocked);
      toast.success(d.blocked ? 'Device unblocked' : 'Device blocked');
      setConfirmDevice(null);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Update failed');
    } finally {
      setTogglingDevice(false);
    }
  }

  async function saveRename() {
    if (!renameDevice) return;
    try {
      await admin.devices.rename(renameDevice.id, renameValue.trim());
      setRenameDevice(null);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Rename failed');
    }
  }

  async function unblock(id: string) {
    try {
      await admin.blocked.unblock(id);
      toast.success('Unblocked');
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Unblock failed');
    }
  }

  const policyColumns: Column<Policy>[] = [
    { key: 'name', header: 'Policy', render: (p) => <span className="font-medium text-text">{p.name}</span> },
    { key: 'rpm', header: 'RPM', align: 'right', render: (p) => <span className="tabular text-muted">{p.rpm || '∞'}</span> },
    { key: 'tpm', header: 'TPM', align: 'right', render: (p) => <span className="tabular text-muted">{p.tpm ? formatCompact(p.tpm) : '∞'}</span> },
    { key: 'daily', header: 'Daily tokens', align: 'right', render: (p) => <span className="tabular text-muted">{p.daily_tokens ? formatCompact(p.daily_tokens) : '∞'}</span> },
    { key: 'conc', header: 'Concurrent', align: 'right', render: (p) => <span className="tabular text-muted">{p.max_concurrent || '∞'}</span> },
    { key: 'ips', header: 'Max IPs', align: 'right', render: (p) => <span className="tabular text-muted">{p.max_ips || '∞'}</span> },
    { key: 'devices', header: 'Max devices', align: 'right', render: (p) => <span className="tabular text-muted">{p.max_devices || '∞'}</span> },
    { key: 'on', header: 'On exceed', render: (p) => <Badge tone={p.on_exceed === 'reject' ? 'warning' : 'info'} dot={false}>{p.on_exceed}</Badge> },
    { key: 'bound', header: 'Bound', align: 'right', render: (p) => <span className="tabular text-muted">{p.bound_count}</span> },
  ];

  const ruleColumns: Column<IPRule>[] = [
    { key: 'cidr', header: 'CIDR', render: (r) => <code className="font-mono text-[12px] text-text">{r.cidr}</code> },
    { key: 'action', header: 'Action', render: (r) => <Badge tone={r.action === 'allow' ? 'success' : 'danger'}>{r.action === 'allow' ? 'Allow' : 'Deny'}</Badge> },
    { key: 'scope', header: 'Scope', render: (r) => <span className="text-muted">{r.scope_type === 'global' ? 'Global' : r.scope_name || r.scope_id}</span> },
    { key: 'note', header: 'Note', render: (r) => <span className="text-muted">{r.note || '—'}</span> },
    { key: 'expires', header: 'Expires', render: (r) => <span className="font-mono text-[11.5px] text-muted">{r.expires_at ? formatDateTime(r.expires_at) : 'Never'}</span> },
  ];

  const deviceColumns: Column<Device>[] = [
    { key: 'label', header: 'Device label', render: (d) => <span className="font-medium text-text">{d.label || 'Unnamed device'}</span> },
    { key: 'key', header: 'API key', render: (d) => <span className="text-muted">{d.api_key_name || '—'}</span> },
    { key: 'ip', header: 'Last IP', render: (d) => <code className="font-mono text-[11.5px] text-muted">{d.last_ip}</code> },
    { key: 'ua', header: 'User-Agent', render: (d) => <span className="block max-w-[220px] truncate text-[11.5px] text-muted">{d.user_agent || '—'}</span> },
    { key: 'first', header: 'First seen', render: (d) => <span className="font-mono text-[11px] text-muted">{relativeTime(d.first_seen)}</span> },
    { key: 'last', header: 'Last seen', render: (d) => <span className="font-mono text-[11px] text-muted">{relativeTime(d.last_seen)}</span> },
    { key: 'req', header: 'Requests', align: 'right', render: (d) => <span className="tabular text-muted">{formatNumber(d.requests)}</span> },
    { key: 'status', header: 'Status', render: (d) => <StatusBadge spec={d.blocked ? { label: 'Blocked', tone: 'danger' } : { label: 'Active', tone: 'success' }} /> },
  ];

  const blockedColumns: Column<BlockEntry>[] = [
    { key: 'kind', header: 'Type', render: (b) => <Badge tone="danger" dot={false}>{b.kind}</Badge> },
    { key: 'value', header: 'Value', render: (b) => <code className="font-mono text-[11.5px] text-text">{b.value}</code> },
    { key: 'reason', header: 'Reason', render: (b) => <span className="text-muted">{b.reason}</span> },
    { key: 'until', header: 'Until', render: (b) => <span className="font-mono text-[11.5px] text-warning">{countdown(b.until)}</span> },
  ];

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Guard"
        description="Access control and usage limits per key, IP, device, route, and provider. Most specific policy wins; all limits are checked."
        actions={
          tab === 'policies' ? (
            <Button variant="primary" size="compact" icon={<Plus size={14} />} onClick={openCreatePolicy}>
              Create Policy
            </Button>
          ) : undefined
        }
      />

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard label="Policies" value={String(policies.length)} hint={`${stats.limited} with limits`} icon={<ShieldCheck size={15} />} />
        <StatCard label="IP Rules" value={String(ipRules.length)} hint={`${stats.denied} deny rules`} icon={<Globe size={15} />} />
        <StatCard label="Devices" value={String(devices.length)} hint="soft-identified" icon={<MonitorSmartphone size={15} />} />
        <StatCard label="Blocked" value={String(blocked.length)} hint="active blocks" tone={blocked.length ? 'danger' : 'default'} icon={<ShieldAlert size={15} />} />
      </div>

      <Tabs
        tabs={[
          { key: 'policies', label: 'Policies' },
          { key: 'ip', label: 'IP Rules' },
          { key: 'devices', label: 'Devices' },
          { key: 'blocked', label: 'Blocked' },
        ]}
        active={tab}
        onChange={setTab}
      />

      {error && <Callout tone="danger">{error}</Callout>}

      {tab === 'policies' && (
        <DataTable
          columns={policyColumns}
          rows={policies}
          rowKey={(p) => p.id}
          loading={loading}
          error={error}
          onRetry={() => void load()}
          actions={(p) => (
            <RowActions
              items={[
                { label: 'Edit policy', onClick: () => openEditPolicy(p) },
                { label: 'Delete', destructive: true, onClick: () => setConfirmPolicy(p) },
              ]}
            />
          )}
        />
      )}

      {tab === 'ip' && (
        <div className="flex flex-col gap-4">
          <Card>
            <CardHeader title="Add IP rule" description="Allowlist or denylist a CIDR range. Use 0.0.0.0/0 to match everything." />
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <Field label="CIDR / IP">
                <Input mono value={ruleForm.cidr} onChange={(e) => setRuleForm({ ...ruleForm, cidr: e.target.value })} placeholder="203.0.113.0/24" />
              </Field>
              <Field label="Action">
                <Select value={ruleForm.action} onChange={(e) => setRuleForm({ ...ruleForm, action: e.target.value })}>
                  <option value="deny">Deny</option>
                  <option value="allow">Allow</option>
                </Select>
              </Field>
              <Field label="Note">
                <Input value={ruleForm.note} onChange={(e) => setRuleForm({ ...ruleForm, note: e.target.value })} placeholder="Office VPN" />
              </Field>
              <Field label="Expires">
                <div className="flex gap-2">
                  <Select value={ruleForm.expires_days} onChange={(e) => setRuleForm({ ...ruleForm, expires_days: e.target.value })}>
                    <option value="">Never</option>
                    <option value="1">1 day</option>
                    <option value="7">7 days</option>
                    <option value="30">30 days</option>
                  </Select>
                  <Button variant="primary" onClick={() => void addRule()} loading={addingRule}>Add</Button>
                </div>
              </Field>
            </div>
            {ruleError && <p className="mt-2 text-[12px] text-danger">{ruleError}</p>}
            <div className="mt-3">
              <Callout tone="info">
                X-Forwarded-For is trusted only from proxies listed under Settings → API → Trusted proxies. Otherwise the direct peer IP is used.
              </Callout>
            </div>
          </Card>

          <DataTable
            columns={ruleColumns}
            rows={ipRules}
            rowKey={(r) => r.id}
            loading={loading}
            empty={<p className="py-8 text-center text-[12.5px] text-muted">No IP rules. All IPs are permitted unless blocked.</p>}
            actions={(r) => (
              <RowActions items={[{ label: 'Delete rule', destructive: true, onClick: () => setConfirmRule(r) }]} />
            )}
          />
        </div>
      )}

      {tab === 'devices' && (
        <div className="flex flex-col gap-3">
          <Callout tone="neutral">
            Device identification is best-effort: a hash of API key + IP + User-Agent, plus an optional X-Device-Id header. It is not a guarantee of identity.
          </Callout>
          <DataTable
            columns={deviceColumns}
            rows={devices}
            rowKey={(d) => d.id}
            loading={loading}
            empty={<p className="py-8 text-center text-[12.5px] text-muted">No devices seen yet. Devices appear after a key is used.</p>}
            actions={(d) => (
              <RowActions
                items={[
                  { label: d.blocked ? 'Unblock device' : 'Block device', onClick: () => (d.blocked ? void toggleDevice(d) : setConfirmDevice(d)), destructive: !d.blocked },
                  { label: 'Rename', onClick: () => { setRenameDevice(d); setRenameValue(d.label); } },
                ]}
              />
            )}
          />
        </div>
      )}

      {tab === 'blocked' && (
        <DataTable
          columns={blockedColumns}
          rows={blocked}
          rowKey={(b) => b.id}
          loading={loading}
          empty={
            <EmptyState
              icon={<Shield size={20} />}
              title="Nothing is blocked"
              description="IPs, devices and keys blocked by Guard or by an admin will appear here."
            />
          }
          actions={(b) => (
            <Button variant="secondary" size="compact" onClick={() => void unblock(b.id)}>Unblock</Button>
          )}
        />
      )}

      <Drawer
        open={policyDrawer}
        onClose={savingPolicy ? () => {} : () => setPolicyDrawer(false)}
        title={policyForm.id ? 'Edit policy' : 'Create policy'}
        description="Reusable limit set. Bind it to a key, route, provider or global scope."
        footer={<><Button variant="secondary" onClick={() => setPolicyDrawer(false)} disabled={savingPolicy}>Cancel</Button><Button variant="primary" onClick={() => void savePolicy()} loading={savingPolicy}>Save policy</Button></>}
      >
        <div className="flex flex-col gap-5">
          <Field label="Policy name" required>
            <Input value={policyForm.name} onChange={(e) => setPolicyForm({ ...policyForm, name: e.target.value })} placeholder="Team Member" />
          </Field>

          {!policyForm.id && (
            <div>
              <p className="mb-1.5 text-small font-medium text-text">Start from a preset</p>
              <div className="grid grid-cols-2 gap-2">
                {presets.map((p) => (
                  <button
                    key={p.name}
                    type="button"
                    onClick={() => applyPreset(p.value)}
                    className="rounded-control border border-border bg-bg px-2.5 py-2 text-left hover:bg-surface-2"
                  >
                    <span className="block text-[12.5px] font-medium text-text">{p.name}</span>
                    <span className="block text-[10.5px] text-muted">{p.hint}</span>
                  </button>
                ))}
              </div>
            </div>
          )}

          <FormSection title="Rate limits" hint="0 means unlimited.">
            <Field label="Requests / minute"><Input type="number" min={0} value={policyForm.rpm} onChange={(e) => setPolicyForm({ ...policyForm, rpm: Number(e.target.value) })} /></Field>
            <Field label="Tokens / minute"><Input type="number" min={0} value={policyForm.tpm} onChange={(e) => setPolicyForm({ ...policyForm, tpm: Number(e.target.value) })} /></Field>
            <Field label="Burst"><Input type="number" min={0} value={policyForm.burst} onChange={(e) => setPolicyForm({ ...policyForm, burst: Number(e.target.value) })} /></Field>
          </FormSection>

          <FormSection title="Quotas" hint="Resets on the admin timezone.">
            <Field label="Daily tokens"><Input type="number" min={0} value={policyForm.daily_tokens} onChange={(e) => setPolicyForm({ ...policyForm, daily_tokens: Number(e.target.value) })} /></Field>
            <Field label="Monthly tokens"><Input type="number" min={0} value={policyForm.monthly_tokens} onChange={(e) => setPolicyForm({ ...policyForm, monthly_tokens: Number(e.target.value) })} /></Field>
          </FormSection>

          <FormSection title="Limits">
            <Field label="Max concurrent"><Input type="number" min={0} value={policyForm.max_concurrent} onChange={(e) => setPolicyForm({ ...policyForm, max_concurrent: Number(e.target.value) })} /></Field>
            <Field label="Max tokens per request"><Input type="number" min={0} value={policyForm.max_tokens_per_request} onChange={(e) => setPolicyForm({ ...policyForm, max_tokens_per_request: Number(e.target.value) })} /></Field>
          </FormSection>

          <FormSection title="Identity limits" hint="Unique clients per key over 24h.">
            <Field label="Max IPs per key"><Input type="number" min={0} value={policyForm.max_ips} onChange={(e) => setPolicyForm({ ...policyForm, max_ips: Number(e.target.value) })} /></Field>
            <Field label="Max devices per key"><Input type="number" min={0} value={policyForm.max_devices} onChange={(e) => setPolicyForm({ ...policyForm, max_devices: Number(e.target.value) })} /></Field>
          </FormSection>

          <FormSection title="Behavior">
            <Field label="On exceed">
              <Select value={policyForm.on_exceed} onChange={(e) => setPolicyForm({ ...policyForm, on_exceed: e.target.value as PolicyForm['on_exceed'] })}>
                <option value="reject">Reject (429)</option>
                <option value="queue">Queue (wait for capacity)</option>
              </Select>
            </Field>
            {policyForm.on_exceed === 'queue' && (
              <Field label="Max queue (s)" hint="0 = queue disabled (reject immediately).">
                <Input type="number" min={0} value={policyForm.queue_max_s} onChange={(e) => setPolicyForm({ ...policyForm, queue_max_s: Number(e.target.value) })} />
              </Field>
            )}
            <Field label="Cooldown (s)"><Input type="number" min={0} value={policyForm.cooldown_s} onChange={(e) => setPolicyForm({ ...policyForm, cooldown_s: Number(e.target.value) })} /></Field>
            <Field label="Auto-block after N violations"><Input type="number" min={0} value={policyForm.auto_block_n} onChange={(e) => setPolicyForm({ ...policyForm, auto_block_n: Number(e.target.value) })} /></Field>
            <Field label="…within M minutes"><Input type="number" min={0} value={policyForm.auto_block_m} onChange={(e) => setPolicyForm({ ...policyForm, auto_block_m: Number(e.target.value) })} /></Field>
            <Field label="Block duration (s)"><Input type="number" min={0} value={policyForm.auto_block_s} onChange={(e) => setPolicyForm({ ...policyForm, auto_block_s: Number(e.target.value) })} /></Field>
          </FormSection>

          {policyError && <p className="text-[12px] text-danger">{policyError}</p>}
        </div>
      </Drawer>

      <Modal
        open={!!renameDevice}
        onClose={() => setRenameDevice(null)}
        title="Rename device"
        size="sm"
        footer={<><Button variant="secondary" onClick={() => setRenameDevice(null)}>Cancel</Button><Button variant="primary" onClick={() => void saveRename()} disabled={!renameValue.trim()}>Save</Button></>}
      >
        <Field label="Device label">
          <Input value={renameValue} onChange={(e) => setRenameValue(e.target.value)} placeholder="Alice's laptop" />
        </Field>
      </Modal>

      <ConfirmDialog
        open={!!confirmPolicy}
        onClose={() => setConfirmPolicy(null)}
        onConfirm={() => void deletePolicy()}
        title="Delete policy"
        message={`Delete policy "${confirmPolicy?.name}"? Any objects bound to it will fall back to less specific policies.`}
        confirmLabel="Delete policy"
        loading={deletingPolicy}
      />

      <ConfirmDialog
        open={!!confirmRule}
        onClose={() => setConfirmRule(null)}
        onConfirm={() => void removeRule()}
        title="Delete IP rule"
        message={`Delete ${confirmRule?.action === 'allow' ? 'allow' : 'deny'} rule for ${confirmRule?.cidr}?`}
        confirmLabel="Delete rule"
        loading={deletingRule}
      />

      <ConfirmDialog
        open={!!confirmDevice}
        onClose={() => setConfirmDevice(null)}
        onConfirm={() => confirmDevice && void toggleDevice(confirmDevice)}
        title="Block device"
        message={`Block "${confirmDevice?.label || confirmDevice?.fingerprint || 'this device'}"? Requests from it will receive 403 until you unblock it.`}
        confirmLabel="Block device"
        loading={togglingDevice}
      />
    </div>
  );
}
