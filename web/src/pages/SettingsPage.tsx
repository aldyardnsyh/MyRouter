import { useCallback, useEffect, useRef, useState } from 'react';
import { Database, Download, KeyRound, Lock, MonitorSmartphone, Route as RouteIcon, Server, Settings2, ShieldCheck, Sun, Moon, Upload } from 'lucide-react';
import { PageHeader, Card, CardHeader, Callout, ErrorState, SkeletonCard } from '../components/ui/Layout';
import { ConfirmDialog } from '../components/ui/Overlay';
import { Button } from '../components/ui/Button';
import { Input, Select, Field, Switch } from '../components/ui/Field';
import { CopyField } from '../components/ui/CopyField';
import { useToast } from '../components/ui/Toast';
import { admin, ApiError, type OpenCodeStatus } from '../lib/api';
import { useTheme } from '../lib/theme';
import type { SettingsMap } from '../lib/types';
import { classNames } from '../lib/format';

const sections = [
  { key: 'general', label: 'General', icon: Settings2 },
  { key: 'api', label: 'API', icon: Server },
  { key: 'security', label: 'Security', icon: Lock },
  { key: 'routing', label: 'Routing', icon: RouteIcon },
  { key: 'guard', label: 'Guard defaults', icon: ShieldCheck },
  { key: 'storage', label: 'Storage', icon: Database },
  { key: 'system', label: 'System', icon: MonitorSmartphone },
];

export function SettingsPage() {
  const toast = useToast();
  const { mode, setMode } = useTheme();
  const [section, setSection] = useState('general');
  const [settings, setSettings] = useState<SettingsMap | null>(null);
  const [version, setVersion] = useState('0.1.0');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [password, setPassword] = useState({ current: '', next: '', confirm: '' });
  const [changing, setChanging] = useState(false);
  const [configBusy, setConfigBusy] = useState<'export' | 'import' | null>(null);
  const [pendingImport, setPendingImport] = useState<{ name: string; parsed: unknown } | null>(null);
  const [importing, setImporting] = useState(false);
  const importInput = useRef<HTMLInputElement | null>(null);
  const [opencode, setOpencode] = useState<OpenCodeStatus | null>(null);
  const [opencodeError, setOpencodeError] = useState('');
  const [syncKey, setSyncKey] = useState('');
  const [syncing, setSyncing] = useState(false);

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [res, oc] = await Promise.all([
        admin.settings.get(),
        admin.opencode.status().catch(() => null),
      ]);
      setSettings(res.data);
      setVersion(res.version);
      setOpencode(oc?.data ?? null);
      setOpencodeError(oc?.error ?? '');
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to load settings');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function save(patch: Partial<SettingsMap>) {
    const next = { ...settings, ...patch } as SettingsMap;
    setSettings(next);
    try {
      await admin.settings.update(patch as Record<string, string>);
      toast.success('Settings saved');
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Save failed');
      await load(true);
    } finally {
    }
  }

  async function syncOpencode() {
    setSyncing(true);
    try {
      const res = await admin.opencode.sync({ api_key: syncKey.trim() || undefined });
      const added = res.data.added.length;
      toast.success(
        added > 0
          ? `${added} model ditambahkan ke OpenCode. Restart OpenCode agar muncul di picker.`
          : 'OpenCode sudah memuat semua route. Restart OpenCode kalau baru mengganti key.',
      );
      setSyncKey('');
      const st = await admin.opencode.status();
      setOpencode(st.data);
      setOpencodeError(st.error ?? '');
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Sync ke OpenCode gagal');
    } finally {
      setSyncing(false);
    }
  }

  async function changePassword() {
    if (password.next.length < 8) {
      toast.error('New password must be at least 8 characters');
      return;
    }
    if (password.next !== password.confirm) {
      toast.error('Passwords do not match');
      return;
    }
    setChanging(true);
    try {
      await admin.changePassword(password.current, password.next);
      toast.success('Password updated');
      setPassword({ current: '', next: '', confirm: '' });
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Could not change password');
    } finally {
      setChanging(false);
    }
  }

  function exportBackup() {
    if (!settings) return;
    const blob = new Blob([JSON.stringify({ version, exported_at: new Date().toISOString(), settings }, null, 2)], {
      type: 'application/json',
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'myrouter-settings-backup.json';
    a.click();
    URL.revokeObjectURL(url);
  }

  async function exportConfig() {
    setConfigBusy('export');
    try {
      const cfg = await admin.config.export();
      toast.success(
        `Exported ${cfg.providers.length} providers, ${cfg.routes.length} routes, ${cfg.policies.length} policies`,
      );
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Export failed');
    } finally {
      setConfigBusy(null);
    }
  }

  // Read + validate the file first, then ask for confirmation: importing
  // overwrites providers/routes/policies matched by name/alias.
  async function handleImportFile(file: File) {
    let parsed: unknown;
    try {
      parsed = JSON.parse(await file.text());
    } catch {
      toast.error('File yang dipilih bukan JSON yang valid.');
      return;
    }
    const obj = parsed as Record<string, unknown> | null;
    if (!obj || typeof obj !== 'object') {
      toast.error('Format file tidak dikenali.');
      return;
    }
    if ('settings' in obj) {
      toast.error('Ini file backup settings, bukan config export. Gunakan Export config untuk memindahkan provider/route/policy.');
      return;
    }
    if (!Array.isArray(obj.providers) && !Array.isArray(obj.routes)) {
      toast.error('File tidak memuat providers/routes. Gunakan file dari tombol Export config.');
      return;
    }
    setPendingImport({ name: file.name, parsed });
  }

  async function importConfig(parsed: unknown) {
    setImporting(true);
    setConfigBusy('import');
    try {
      const res = await admin.config.import(parsed);
      toast.success(
        `Import selesai: ${res.providers} provider, ${res.models} model, ${res.policies} policy, ${res.routes} route.` +
          (res.updated.routes > 0 ? ` ${res.updated.routes} route diperbarui.` : ''),
      );
      const conflicts = res.conflicts ?? [];
      if (conflicts.length > 0) {
        toast.warning(
          `Base URL ${conflicts.length} provider tidak diubah: ${conflicts.join(', ')}. ` +
            'Ubah manual di halaman Providers.',
        );
      }
      setPendingImport(null);
      await load(true);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : 'Gagal mengimpor konfigurasi. Periksa format file lalu coba lagi.');
    } finally {
      setConfigBusy(null);
      setImporting(false);
      if (importInput.current) importInput.current.value = '';
    }
  }

  if (loading && !settings) {
    return (
      <div className="flex flex-col gap-4">
        <SkeletonCard className="h-16" />
        <SkeletonCard className="h-64" />
      </div>
    );
  }
  if (!settings) {
    return <ErrorState message={error ?? 'Failed to load settings'} onRetry={() => void load()} />;
  }

  return (
    <div className="flex flex-col gap-5">
      <PageHeader title="Settings" description="Gateway configuration, security, routing defaults, and storage." />

      <div className="grid grid-cols-1 gap-5 lg:grid-cols-[200px_1fr]">
        <nav className="flex flex-row gap-1 overflow-x-auto lg:flex-col" aria-label="Settings sections">
          {sections.map((s) => (
            <button
              key={s.key}
              onClick={() => setSection(s.key)}
              className={classNames(
                'flex shrink-0 items-center gap-2 rounded-control px-3 py-2 text-[12.5px] transition-colors',
                section === s.key ? 'bg-accent-soft font-semibold text-accent' : 'text-muted hover:bg-surface-2 hover:text-text',
              )}
            >
              <s.icon size={15} /> {s.label}
            </button>
          ))}
        </nav>

        <div className="flex flex-col gap-4">
          {section === 'general' && (
            <Card>
              <CardHeader title="General" description="Identity shown across the console." />
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <Field label="Product name"><Input value="MyRouter" readOnly /></Field>
                <Field label="Timezone" hint="Used for daily quota resets.">
                  <Select value={settings.timezone} onChange={(e) => void save({ timezone: e.target.value })}>
                    <option value="UTC">UTC</option>
                    <option value="Asia/Jakarta">Asia/Jakarta</option>
                    <option value="Asia/Singapore">Asia/Singapore</option>
                    <option value="Europe/London">Europe/London</option>
                    <option value="America/New_York">America/New_York</option>
                  </Select>
                </Field>
              </div>
            </Card>
          )}

          {section === 'api' && (
            <>
              <Card>
                <CardHeader title="API endpoint" description="Where clients send OpenAI-compatible requests." />
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <Field label="Port" hint="Takes effect after restart.">
                    <Input mono value={settings.api_port} onChange={(e) => setSettings({ ...settings, api_port: e.target.value })} onBlur={(e) => void save({ api_port: e.target.value })} />
                  </Field>
                  <Field label="Base URL" hint="Overrides the displayed endpoint when behind a proxy.">
                    <Input mono value={settings.base_url} onChange={(e) => setSettings({ ...settings, base_url: e.target.value })} onBlur={(e) => void save({ base_url: e.target.value })} placeholder="http://localhost:20128/v1" />
                  </Field>
                </div>
                <div className="mt-3">
                  <CopyField label="Effective base URL" value={settings.base_url || `http://localhost:${settings.api_port}/v1`} />
                </div>
              </Card>
              <Card>
                <CardHeader title="CORS & proxies" description="Control browser origins and trusted forwarding proxies." />
                <div className="flex flex-col gap-3">
                  <Field label="CORS origins" hint="Comma separated full origins. Leave empty to disable browser calls.">
                    <Input value={settings.cors_origins} onChange={(e) => setSettings({ ...settings, cors_origins: e.target.value })} onBlur={(e) => void save({ cors_origins: e.target.value })} placeholder="https://app.example.com" />
                  </Field>
                  <Field label="Trusted proxies" hint="IPs or CIDRs allowed to set X-Forwarded-For. Use * only when all traffic is behind a trusted proxy.">
                    <Input mono value={settings.trusted_proxies} onChange={(e) => setSettings({ ...settings, trusted_proxies: e.target.value })} onBlur={(e) => void save({ trusted_proxies: e.target.value })} placeholder="10.0.0.0/8, 172.17.0.1" />
                  </Field>
                  <Callout tone="warning">
                    If the direct peer is not a trusted proxy, X-Forwarded-For is ignored, this prevents IP spoofing and Guard bypass.
                  </Callout>
                </div>
              </Card>
              <Card>
                <CardHeader
                  title="OpenCode integration"
                  description="Write every enabled route into the OpenCode config so new models show up in its picker."
                />
                <div className="flex flex-col gap-3">
                  <div className="rounded-control border border-border bg-surface px-3 py-2.5">
                    <p className="text-[11.5px] text-muted">Config file</p>
                    <p className="mt-0.5 break-all font-mono text-[11.5px] text-text">
                      {opencode?.path || 'Not detected'}
                    </p>
                  </div>
                  {opencode && (
                    <>
                      <dl className="grid grid-cols-2 gap-3 text-[12.5px] sm:grid-cols-3">
                        <div>
                          <dt className="text-muted">Routes ready</dt>
                          <dd className="mt-0.5 font-mono text-text">{opencode.route_models.length}</dd>
                        </div>
                        <div>
                          <dt className="text-muted">In OpenCode</dt>
                          <dd className="mt-0.5 font-mono text-text">{opencode.config_models.length}</dd>
                        </div>
                        <div>
                          <dt className="text-muted">API key</dt>
                          <dd className="mt-0.5 text-text">{opencode.api_key_configured ? 'configured' : 'missing'}</dd>
                        </div>
                      </dl>
                      <p className="text-[11.5px] text-muted">
                        Instance {opencode.listen_addr || 'unknown'}, data dir {opencode.data_dir || 'unknown'}
                      </p>
                    </>
                  )}
                  {opencodeError && <Callout tone="warning">{opencodeError}</Callout>}
                  {opencode && opencode.missing.length > 0 && (
                    <Callout tone="warning">
                      {opencode.missing.length} route belum ada di OpenCode:{' '}
                      <span className="font-mono">{opencode.missing.join(', ')}</span>
                    </Callout>
                  )}
                  <Field
                    label="API key (optional)"
                    hint="Only fill this to replace the apiKey in the config. A secret is shown once when the key is created."
                  >
                    <Input
                      type="password"
                      value={syncKey}
                      onChange={(e) => setSyncKey(e.target.value)}
                      placeholder="mr-sk-..."
                    />
                  </Field>
                  <div className="flex flex-wrap items-center gap-2">
                    <Button
                      variant="primary"
                      size="compact"
                      loading={syncing}
                      disabled={!opencode?.writable}
                      onClick={() => void syncOpencode()}
                    >
                      Sync to OpenCode
                    </Button>
                    <span className="text-[11.5px] text-muted">Restart OpenCode after a sync to load new models.</span>
                  </div>
                </div>
              </Card>
            </>
          )}

          {section === 'security' && (
            <>
              <Card>
                <CardHeader title="Session" description="Admin session lifetime." />
                <Field label="Session TTL (hours)">
                  <Input type="number" min={1} value={settings.session_ttl_hours} onChange={(e) => setSettings({ ...settings, session_ttl_hours: e.target.value })} onBlur={(e) => void save({ session_ttl_hours: e.target.value })} />
                </Field>
              </Card>
              <Card>
                <CardHeader title="Change password" description="Updates the admin password immediately." />
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                  <Field label="Current"><Input type="password" value={password.current} onChange={(e) => setPassword({ ...password, current: e.target.value })} /></Field>
                  <Field label="New"><Input type="password" value={password.next} onChange={(e) => setPassword({ ...password, next: e.target.value })} /></Field>
                  <Field label="Confirm"><Input type="password" value={password.confirm} onChange={(e) => setPassword({ ...password, confirm: e.target.value })} /></Field>
                </div>
                <div className="mt-3">
                  <Button variant="primary" size="compact" loading={changing} onClick={() => void changePassword()}>Update password</Button>
                </div>
              </Card>
              <Card>
                <CardHeader title="Master key" description="Encrypts provider credentials at rest (AES-256-GCM)." />
                <div className="flex items-center gap-2 rounded-control border border-border bg-surface px-3 py-2.5">
                  <KeyRound size={15} className="text-muted" />
                  <span className="text-[12.5px] text-text">Master key is configured and never exposed via the API.</span>
                </div>
              </Card>
            </>
          )}

          {section === 'routing' && (
            <Card>
              <CardHeader title="Routing defaults" description="Applied to new routes unless overridden." />
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <Field label="Default retry per target">
                  <Input type="number" min={0} max={5} value={settings.default_retry} onChange={(e) => setSettings({ ...settings, default_retry: e.target.value })} onBlur={(e) => void save({ default_retry: e.target.value })} />
                </Field>
                <Field label="Default timeout (ms)">
                  <Input type="number" min={1000} step={1000} value={settings.default_timeout_ms} onChange={(e) => setSettings({ ...settings, default_timeout_ms: e.target.value })} onBlur={(e) => void save({ default_timeout_ms: e.target.value })} />
                </Field>
              </div>
              <Callout tone="info">Fallback triggers only on 429, timeout, 5xx and connection failures. 400/401/403 from providers are returned to the client.</Callout>
            </Card>
          )}

          {section === 'guard' && (
            <Card>
              <CardHeader title="Guard defaults" description="Global behaviours for access control." />
              <div className="flex flex-col gap-3">
                <div className="flex items-center justify-between rounded-control border border-border px-3 py-2.5">
                  <div>
                    <p className="text-[12.5px] font-medium text-text">Allow private upstreams</p>
                    <p className="text-[11.5px] text-muted">Permit provider base URLs that resolve to private or loopback addresses (e.g. local vLLM).</p>
                  </div>
                  <Switch checked={settings.allow_private_upstreams === 'true'} onChange={(v) => void save({ allow_private_upstreams: v ? 'true' : 'false' })} label="Allow private upstreams" />
                </div>
                <Callout tone="neutral">
                  Provider-level policies protect the total quota of a single upstream account, even when many client keys share it.
                </Callout>
              </div>
            </Card>
          )}

          {section === 'storage' && (
            <Card>
              <CardHeader title="Storage" description="Request log retention and data storage." />
              <div className="flex flex-col gap-3">
                <Field label="Log retention (days)" hint="Request logs older than this are purged hourly.">
                  <Input type="number" min={1} value={settings.log_retention_days} onChange={(e) => setSettings({ ...settings, log_retention_days: e.target.value })} onBlur={(e) => void save({ log_retention_days: e.target.value })} />
                </Field>
                <div className="flex items-center justify-between rounded-control border border-border px-3 py-2.5">
                  <div>
                    <p className="text-[12.5px] font-medium text-text">Store request bodies</p>
                    <p className="text-[11.5px] text-muted">Off by default. Enabling increases storage and privacy exposure.</p>
                  </div>
                  <Switch checked={settings.store_request_body === 'true'} onChange={(v) => void save({ store_request_body: v ? 'true' : 'false' })} label="Store request bodies" />
                </div>
              </div>
            </Card>
          )}

          {section === 'system' && (
            <>
              <Card>
                <CardHeader title="Appearance" description="Light is the primary theme; dark mode is a soft palette." />
                <div className="flex flex-wrap gap-2">
                  {[
                    { key: 'light', label: 'Light', icon: Sun },
                    { key: 'dark', label: 'Dark Soft', icon: Moon },
                    { key: 'system', label: 'System', icon: MonitorSmartphone },
                  ].map((opt) => (
                    <button
                      key={opt.key}
                      onClick={() => setMode(opt.key as 'light' | 'dark' | 'system')}
                      className={classNames(
                        'flex items-center gap-2 rounded-control border px-3 py-2 text-[12.5px] transition-colors',
                        mode === opt.key ? 'border-accent bg-accent-soft text-accent' : 'border-border bg-bg text-muted hover:bg-surface-2',
                      )}
                    >
                      <opt.icon size={15} /> {opt.label}
                    </button>
                  ))}
                </div>
              </Card>
              <Card>
                <CardHeader title="System" description="Version, backups, and diagnostics." />
                <dl className="grid grid-cols-2 gap-3 text-[12.5px]">
                  <div><dt className="text-muted">Version</dt><dd className="mt-0.5 font-mono text-text">v{version}</dd></div>
                  <div><dt className="text-muted">Database</dt><dd className="mt-0.5 text-text">SQLite (WAL)</dd></div>
                </dl>
                <div className="mt-3 flex gap-2">
                  <Button variant="secondary" size="compact" onClick={exportBackup}>Download settings backup</Button>
                  <Button variant="secondary" size="compact" onClick={() => void load()} loading={loading}>Reload</Button>
                </div>
              </Card>
              <Card>
                <CardHeader
                  title="Configuration import / export"
                  description="Move providers, models, routes and Guard policies between instances."
                />
                <p className="text-[11.5px] text-muted">
                  The export never contains credentials, API keys or hashes — re-enter provider keys after importing
                  into a new instance. Existing providers, policies and routes are updated in place (matched by name
                  and route alias).
                </p>
                <div className="mt-3 flex flex-wrap gap-2">
                  <Button
                    variant="secondary"
                    size="compact"
                    loading={configBusy === 'export'}
                    onClick={() => void exportConfig()}
                  >
                    <Download size={14} /> Export config
                  </Button>
                  <Button
                    variant="secondary"
                    size="compact"
                    loading={configBusy === 'import'}
                    onClick={() => importInput.current?.click()}
                  >
                    <Upload size={14} /> Import config
                  </Button>
                  <input
                    ref={importInput}
                    type="file"
                    accept="application/json,.json"
                    className="hidden"
                    aria-label="Import configuration file"
                    onChange={(e) => {
                      const file = e.target.files?.[0];
                      if (file) void handleImportFile(file);
                    }}
                  />
                </div>
              </Card>
            </>
          )}
        </div>
      </div>

      <ConfirmDialog
        open={!!pendingImport}
        onClose={() => {
          setPendingImport(null);
          if (importInput.current) importInput.current.value = '';
        }}
        onConfirm={() => pendingImport && void importConfig(pendingImport.parsed)}
        title="Import configuration?"
        message={`Import "${pendingImport?.name}"? Providers, routes and policies with the same name/alias will be updated in place. Credentials are never imported.`}
        confirmLabel="Import"
        loading={importing}
      />
    </div>
  );
}
