import { useMemo, useState } from 'react';
import { Check, ChevronRight, Loader2 } from 'lucide-react';
import { LogoWordmark } from '../components/Logo';
import { Button } from '../components/ui/Button';
import { Field, Input, Select } from '../components/ui/Field';
import { CopyField } from '../components/ui/CopyField';
import { Callout } from '../components/ui/Layout';
import { admin, ApiError } from '../lib/api';
import { useAuth } from '../lib/auth';
import { classNames } from '../lib/format';

const steps = ['Admin', 'Endpoint', 'Provider', 'Test', 'Route', 'Finish'] as const;

interface ProviderDraft {
  name: string;
  type: string;
  base_url: string;
  api_key: string;
}

export function SetupPage() {
  const { refresh, login } = useAuth();
  const [step, setStep] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [adminUser, setAdminUser] = useState({ username: 'admin', password: '', confirm: '' });
  const [setupToken, setSetupToken] = useState('');
  const [setupTokenError, setSetupTokenError] = useState('');
  const [baseUrl, setBaseUrl] = useState(() => `${window.location.origin}/v1`);
  const [provider, setProvider] = useState<ProviderDraft>({ name: '', type: 'openai', base_url: '', api_key: '' });
  const [providerId, setProviderId] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<{ ok: boolean; message: string } | null>(null);
  const [routeAlias, setRouteAlias] = useState('coding');
  const [modelCount, setModelCount] = useState(0);

  const passwordValid = adminUser.password.length >= 8 && adminUser.password === adminUser.confirm;
  const firstKeyHint = useMemo(() => 'mr-sk-… (create in API Keys after setup)', []);

  async function createAdmin() {
    setError('');
    setSetupTokenError('');
    setBusy(true);
    try {
      const token = setupToken.trim();
      await admin.setupComplete({
        username: adminUser.username.trim(),
        password: adminUser.password,
        port: Number(window.location.port) || (window.location.protocol === 'https:' ? 443 : 80),
        // Only send the field when the user actually typed something; the
        // backend only requires it for non-localhost (LAN) requests.
        ...(token ? { setup_token: token } : {}),
      });
      // Log in immediately so the remaining steps can call the admin API.
      await login(adminUser.username.trim(), adminUser.password);
      setStep(1);
    } catch (err) {
      if (err instanceof ApiError && err.status === 403 && err.code === 'setup_token_required') {
        setSetupTokenError('Setup token salah atau belum diisi. Periksa log server.');
        return;
      }
      setError(err instanceof ApiError ? err.message : 'Setup failed');
    } finally {
      setBusy(false);
    }
  }

  async function saveEndpoint() {
    setError('');
    const value = baseUrl.trim().replace(/\/+$/, '');
    if (!/^https?:\/\/[^\s/]+/i.test(value)) {
      setError('Base URL must start with http:// or https://');
      return;
    }
    setBusy(true);
    try {
      await admin.settings.update({ base_url: value });
      setBaseUrl(value);
      setStep(2);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not save the base URL');
    } finally {
      setBusy(false);
    }
  }

  // Test the draft against the upstream before writing anything. Only a
  // successful test saves the provider and advances to the result step.
  async function testAndSaveProvider() {
    setError('');
    setTestResult(null);
    if (!provider.api_key.trim()) {
      setError('API key is required to test the connection.');
      return;
    }
    setBusy(true);
    try {
      const test = await admin.providers.testDraft({
        type: provider.type,
        base_url: provider.base_url.trim(),
        api_key: provider.api_key,
      });
      if (test.status !== 'connected') {
        const message = test.message || test.status;
        setTestResult({ ok: false, message });
        setError(`Connection test failed: ${message}`);
        return;
      }
      const res = await admin.providers.create({
        name: provider.name.trim() || provider.type,
        type: provider.type,
        base_url: provider.base_url.trim(),
        api_key: provider.api_key,
      });
      setProviderId(res.data.id);
      setTestResult({ ok: true, message: `Connected in ${test.latency_ms}ms` });
      try {
        const models = await admin.providers.refresh(res.data.id);
        setModelCount(models.count);
      } catch {
        setModelCount(0);
      }
      setStep(3);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not test or save provider');
    } finally {
      setBusy(false);
    }
  }

  async function testProvider() {
    if (!providerId) return;
    setError('');
    setBusy(true);
    try {
      const res = await admin.providers.test(providerId);
      setTestResult({ ok: res.status === 'connected', message: res.status === 'connected' ? `Connected in ${res.latency_ms}ms` : res.message });
      if (res.status === 'connected') {
        try {
          const models = await admin.providers.refresh(providerId);
          setModelCount(models.count);
        } catch {
          setModelCount(0);
        }
      }
    } catch (err) {
      setTestResult({ ok: false, message: err instanceof ApiError ? err.message : 'Test failed' });
    } finally {
      setBusy(false);
    }
  }

  async function refreshDiscoveredModels() {
    if (!providerId) return;
    setBusy(true);
    setError('');
    try {
      const res = await admin.providers.refresh(providerId);
      setModelCount(res.count);
      if (res.count === 0) setError('Upstream returned no models. Check the provider base URL and credential.');
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not refresh models');
    } finally {
      setBusy(false);
    }
  }

  async function createRoute() {
    setError('');
    if (!providerId) {
      setStep(5);
      return;
    }
    setBusy(true);
    try {
      const models = await admin.providers.models(providerId);
      if (models.data.length === 0) {
        setError('No models discovered yet. Refresh models on the Providers page, then create the route.');
        return;
      }
      await admin.routes.create({
        alias: routeAlias.trim() || 'coding',
        description: 'First route created by the setup wizard',
        primary_model_id: models.data[0].id,
      });
      setStep(5);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not create route');
    } finally {
      setBusy(false);
    }
  }

  async function finish() {
    await refresh();
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-surface px-4 py-10">
      <div className="w-full max-w-[640px]">
        <div className="mb-5 flex items-center justify-between">
          <LogoWordmark size={30} />
          {step > 0 && (
            <button onClick={() => void refresh()} className="text-[12px] text-muted hover:text-text">
              Skip setup
            </button>
          )}
        </div>

        <div className="rounded-card border border-border bg-bg">
          <div className="border-b border-border px-6 py-4">
            <ol className="flex flex-wrap items-center gap-x-3 gap-y-2">
              {steps.map((label, i) => (
                <li key={label} className="flex items-center gap-1.5">
                  <span
                    className={classNames(
                      'flex h-5 w-5 items-center justify-center rounded-full text-[10.5px] font-semibold',
                      i < step ? 'bg-success-soft text-success' : i === step ? 'bg-accent text-white' : 'bg-surface-2 text-muted',
                    )}
                  >
                    {i < step ? <Check size={11} /> : i + 1}
                  </span>
                  <span className={classNames('text-[11.5px]', i === step ? 'font-semibold text-text' : 'text-muted')}>{label}</span>
                  {i < steps.length - 1 && <ChevronRight size={12} className="text-border" />}
                </li>
              ))}
            </ol>
          </div>

          <div className="px-6 py-5">
            {step === 0 && (
              <div className="flex flex-col gap-3.5">
                <div>
                  <h1 className="text-h2 text-text">Create your admin account</h1>
                  <p className="mt-1 text-[12.5px] text-muted">This account manages providers, routes and Guard policies.</p>
                </div>
                <Field label="Username" required>
                  <Input value={adminUser.username} onChange={(e) => setAdminUser({ ...adminUser, username: e.target.value })} placeholder="admin" />
                </Field>
                <Field label="Password" required hint="At least 8 characters.">
                  <Input type="password" value={adminUser.password} onChange={(e) => setAdminUser({ ...adminUser, password: e.target.value })} />
                </Field>
                <Field label="Confirm password" required error={adminUser.confirm && adminUser.password !== adminUser.confirm ? 'Passwords do not match' : undefined}>
                  <Input type="password" value={adminUser.confirm} onChange={(e) => setAdminUser({ ...adminUser, confirm: e.target.value })} />
                </Field>
                <Field
                  label="Setup token"
                  htmlFor="setup-token"
                  hint="Diperlukan hanya jika Anda membuka halaman ini dari perangkat lain. Lihat log server untuk token."
                  error={setupTokenError || undefined}
                >
                  <Input
                    id="setup-token"
                    name="setup_token"
                    mono
                    invalid={Boolean(setupTokenError)}
                    value={setupToken}
                    onChange={(e) => {
                      setSetupToken(e.target.value);
                      if (setupTokenError) setSetupTokenError('');
                    }}
                    placeholder="mr-setup-..."
                    autoComplete="off"
                    spellCheck={false}
                  />
                </Field>
                {error && <p className="text-[12px] text-danger">{error}</p>}
                <div className="mt-1 flex justify-end">
                  <Button variant="primary" disabled={!passwordValid || !adminUser.username.trim()} loading={busy} onClick={() => void createAdmin()}>
                    Continue
                  </Button>
                </div>
              </div>
            )}

            {step === 1 && (
              <div className="flex flex-col gap-3.5">
                <div>
                  <h1 className="text-h2 text-text">Endpoint & master key</h1>
                  <p className="mt-1 text-[12.5px] text-muted">
                    This is the base URL clients will use. On a VPS behind a reverse proxy, enter your public URL
                    (for example <code className="font-mono text-[11.5px]">https://router.example.com/v1</code>).
                  </p>
                </div>
                <Field label="Gateway base URL" hint="Stored in Settings → API. Leave the local URL for a local install.">
                  <Input mono value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="https://router.example.com/v1" />
                </Field>
                <Callout tone="neutral">
                  Credentials are encrypted with AES-256-GCM and never returned to clients. To change the actual listen
                  port, set <code className="font-mono text-[11.5px]">MYROUTER_ADDR</code> or start with <code className="font-mono text-[11.5px]">-addr</code>.
                </Callout>
                <div className="mt-1 flex justify-end gap-2">
                  <Button variant="secondary" onClick={() => setStep(2)}>Skip</Button>
                  <Button variant="primary" loading={busy} onClick={() => void saveEndpoint()}>Continue</Button>
                </div>
              </div>
            )}

            {step === 2 && (
              <div className="flex flex-col gap-3.5">
                <div>
                  <h1 className="text-h2 text-text">Add a provider</h1>
                  <p className="mt-1 text-[12.5px] text-muted">Connect the first upstream LLM API.</p>
                </div>
                <Field label="Provider type">
                  <Select value={provider.type} onChange={(e) => setProvider({ ...provider, type: e.target.value })}>
                    <option value="openai">OpenAI</option>
                    <option value="anthropic">Anthropic</option>
                    <option value="gemini">Google Gemini</option>
                    <option value="deepseek">DeepSeek</option>
                    <option value="openai-compatible">OpenAI-compatible</option>
                  </Select>
                </Field>
                <Field label="Display name">
                  <Input value={provider.name} onChange={(e) => setProvider({ ...provider, name: e.target.value })} placeholder="OpenAI Global" />
                </Field>
                <Field label="Base URL" hint="Leave blank to use the provider default.">
                  <Input mono value={provider.base_url} onChange={(e) => setProvider({ ...provider, base_url: e.target.value })} placeholder="https://api.openai.com/v1" />
                </Field>
                <Field label="API key" hint="Encrypted at rest; never shown again.">
                  <Input type="password" mono value={provider.api_key} onChange={(e) => setProvider({ ...provider, api_key: e.target.value })} placeholder="sk-..." />
                </Field>
                {testResult && <Callout tone={testResult.ok ? 'info' : 'danger'}>{testResult.message}</Callout>}
                {error && <p className="text-[12px] text-danger">{error}</p>}
                <div className="mt-1 flex justify-end gap-2">
                  <Button variant="ghost" onClick={() => setStep(5)}>Skip provider</Button>
                  <Button variant="primary" loading={busy} onClick={() => void testAndSaveProvider()}>Test &amp; save</Button>
                </div>
              </div>
            )}

            {step === 3 && (
              <div className="flex flex-col gap-3.5">
                <div>
                  <h1 className="text-h2 text-text">Test connection</h1>
                  <p className="mt-1 text-[12.5px] text-muted">Validates the credential and discovers models.</p>
                </div>
                <div className="rounded-control border border-border bg-surface px-3 py-2.5">
                  <p className="font-mono text-[12px] text-text">{provider.base_url || 'provider default'}</p>
                  <p className="mt-0.5 text-[11.5px] text-muted">{provider.name || provider.type}</p>
                </div>
                {testResult && (
                  <Callout tone={testResult.ok ? 'info' : 'danger'}>{testResult.message}</Callout>
                )}
                <div className="mt-1 flex justify-end gap-2">
                  <Button variant="secondary" loading={busy} onClick={() => void testProvider()}>Test again</Button>
                  <Button variant="primary" disabled={!testResult?.ok} onClick={() => setStep(4)}>Continue</Button>
                </div>
              </div>
            )}

            {step === 4 && (
              <div className="flex flex-col gap-3.5">
                <div>
                  <h1 className="text-h2 text-text">Create your first route</h1>
                  <p className="mt-1 text-[12.5px] text-muted">
                    Clients call <code className="font-mono text-[11.5px]">model=coding</code>. MyRouter maps it to a real model and can fail over.
                  </p>
                </div>
                <Field label="Route alias">
                  <Input mono value={routeAlias} onChange={(e) => setRouteAlias(e.target.value)} />
                </Field>
                <p className="text-[11.5px] text-muted">{modelCount} model(s) discovered for this provider.</p>
                {modelCount === 0 && (
                  <div className="flex items-center gap-2">
                    <Button variant="secondary" size="compact" loading={busy} onClick={() => void refreshDiscoveredModels()}>
                      Refresh models
                    </Button>
                    <span className="text-[11.5px] text-muted">Model discovery can be retried without leaving setup.</span>
                  </div>
                )}
                {error && <p className="text-[12px] text-danger">{error}</p>}
                <div className="mt-1 flex justify-end gap-2">
                  <Button variant="ghost" onClick={() => setStep(5)}>Skip</Button>
                  <Button variant="primary" loading={busy} onClick={() => void createRoute()}>Create route</Button>
                </div>
              </div>
            )}

            {step === 5 && (
              <div className="flex flex-col gap-3.5">
                <div>
                  <h1 className="text-h2 text-text">You’re ready</h1>
                  <p className="mt-1 text-[12.5px] text-muted">Point any OpenAI-compatible SDK at this base URL.</p>
                </div>
                <CopyField label="Base URL" value={baseUrl} />
                <p className="text-[11.5px] text-muted">Create a client API key in API Keys to start sending requests. {firstKeyHint}</p>
                <div className="mt-1 flex justify-end">
                  <Button variant="primary" onClick={() => void finish()}>
                    {busy ? <Loader2 size={15} className="animate-spin" /> : null}
                    Open dashboard
                  </Button>
                </div>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
