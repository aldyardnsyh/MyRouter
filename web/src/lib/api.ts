// Minimal fetch wrapper for the admin API. Session cookies are HttpOnly; the
// CSRF token comes from the mr_csrf cookie (double submit) and never touches
// localStorage (PRD §11 A5).

import { bumpRevision } from './cache';

export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

function readCookie(name: string): string {
  const match = document.cookie.match(new RegExp(`(?:^|; )${name}=([^;]*)`));
  return match ? decodeURIComponent(match[1]) : '';
}

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'DELETE';
  body?: unknown;
  signal?: AbortSignal;
}

export async function api<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? 'GET';
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET') {
    const csrf = readCookie('mr_csrf');
    if (csrf) headers['X-CSRF-Token'] = csrf;
  }
  const res = await fetch(`/admin${path}`, {
    method,
    headers,
    credentials: 'same-origin',
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    signal: opts.signal,
  });

  if (res.status === 204) return undefined as T;

  let payload: unknown = null;
  const text = await res.text();
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = null;
    }
  }

  if (!res.ok) {
    const err = (payload as { error?: { code?: string; message?: string } })?.error;
    if (res.status === 401 && !path.startsWith('/auth/login')) {
      window.dispatchEvent(new CustomEvent('myrouter:unauthorized'));
    }
    throw new ApiError(res.status, err?.code ?? 'request_failed', err?.message ?? `Request failed (${res.status})`);
  }
  // A successful mutation invalidates every mounted query so other pages
  // (Models, Routes, Dashboard, ...) reflect the change without a reload.
  if (method !== 'GET') bumpRevision();
  return payload as T;
}

// Portable, credential-free configuration snapshot (GET /admin/config/export).
export interface ConfigExport {
  version: string;
  exported_at: string;
  providers: { name: string; type: string; base_url: string; enabled: boolean }[];
  models: {
    provider_name: string;
    model_id: string;
    display_name: string;
    context_window: number;
    capabilities: string;
  }[];
  routes: {
    alias: string;
    description: string;
    enabled: boolean;
    retry: number;
    timeout_ms: number;
    fallback_on: string[];
    primary_model: string;
    fallback_models: string[];
  }[];
  policies: Record<string, unknown>[];
}

export interface ConfigImportResult {
  providers: number;
  models: number;
  policies: number;
  routes: number;
  updated: { providers: number; models?: number; policies: number; routes: number };
  /**
   * Names of providers whose stored base_url differs from the imported one.
   * The backend keeps the existing base_url for security reasons, so these must
   * be changed manually on the Providers page.
   */
  conflicts?: string[];
}

/** Triggers a browser download of an in-memory JSON blob. */
function downloadJSON(filename: string, data: unknown): void {
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

/**
 * Streams a Test Lab call. The admin endpoint emits SSE frames of
 * PlaygroundEvent; this helper parses them incrementally so the UI can show
 * tokens as they arrive and measure time-to-first-token.
 */
export async function streamPlayground(
  body: import('./types').PlaygroundCall,
  onEvent: (ev: import('./types').PlaygroundEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch('/admin/playground/stream', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'text/event-stream',
      'X-CSRF-Token': readCookie('mr_csrf'),
    },
    credentials: 'same-origin',
    body: JSON.stringify(body),
    signal,
  });
  if (!res.ok) {
    let message = `Request failed (${res.status})`;
    let code = 'request_failed';
    try {
      const payload = (await res.json()) as { error?: { code?: string; message?: string } };
      code = payload.error?.code ?? code;
      message = payload.error?.message ?? message;
    } catch {
      /* non-JSON error */
    }
    if (res.status === 401) window.dispatchEvent(new CustomEvent('myrouter:unauthorized'));
    throw new ApiError(res.status, code, message);
  }
  const reader = res.body?.getReader();
  if (!reader) throw new ApiError(500, 'stream_unsupported', 'Streaming is not supported by this browser');
  const decoder = new TextDecoder();
  let buffer = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let idx = buffer.indexOf('\n');
    while (idx >= 0) {
      const line = buffer.slice(0, idx).trim();
      buffer = buffer.slice(idx + 1);
      idx = buffer.indexOf('\n');
      if (!line.startsWith('data:')) continue;
      const data = line.slice(5).trim();
      if (data === '[DONE]') return;
      try {
        onEvent(JSON.parse(data) as import('./types').PlaygroundEvent);
      } catch {
        /* ignore malformed frame */
      }
    }
  }
}

export const admin = {
  setupStatus: () => api<{ needs_setup: boolean; has_admin: boolean; version: string }>('/setup/status'),
  setupComplete: (body: import('./types').SetupCompleteRequest) =>
    api<{ ok: boolean }>('/setup/complete', { method: 'POST', body }),

  login: (username: string, password: string) =>
    api<{ username: string; csrf_token: string }>('/auth/login', { method: 'POST', body: { username, password } }),
  logout: () => api<{ ok: boolean }>('/auth/logout', { method: 'POST' }),
  me: () => api<{ username: string; csrf_token: string; version: string }>('/auth/me'),
  changePassword: (current_password: string, new_password: string) =>
    api<{ ok: boolean }>('/auth/password', { method: 'POST', body: { current_password, new_password } }),

  overview: () => api<import('./types').Overview>('/overview'),
  series: (range: string) => api<import('./types').SeriesResponse>(`/overview/series?range=${range}`),
  live: () => api<import('./types').LiveSnapshot>('/live'),

  playground: {
    chat: (body: import('./types').PlaygroundCall) =>
      api<{ data: import('./types').PlaygroundResult }>('/playground/chat', { method: 'POST', body }),
    stream: streamPlayground,
  },
  health: () => api<{ status: string; version: string; providers: number; degraded: number; trusted_proxies: number }>('/health'),

  providers: {
    list: () => api<{ data: import('./types').Provider[] }>('/providers'),
    get: (id: string) => api<{ data: import('./types').Provider }>(`/providers/${id}`),
    create: (body: unknown) => api<{ data: import('./types').Provider }>('/providers', { method: 'POST', body }),
    update: (id: string, body: unknown) => api<{ data: import('./types').Provider }>(`/providers/${id}`, { method: 'PATCH', body }),
    remove: (id: string) => api<{ ok: boolean }>(`/providers/${id}`, { method: 'DELETE' }),
    test: (id: string) => api<{ status: string; message: string; latency_ms: number }>(`/providers/${id}/test`, { method: 'POST' }),
    testDraft: (body: { type: string; base_url: string; api_key: string; provider_id?: string }) =>
      api<{ status: string; message: string; latency_ms: number }>('/providers/test', { method: 'POST', body }),
    refresh: (id: string) => api<{ data: import('./types').Model[]; count: number }>(`/providers/${id}/refresh-models`, { method: 'POST' }),
    models: (id: string) => api<{ data: import('./types').Model[] }>(`/providers/${id}/models`),
  },

  models: {
    list: () => api<{ data: import('./types').Model[] }>('/models'),
  },

  routes: {
    list: () => api<{ data: import('./types').Route[] }>('/routes'),
    create: (body: unknown) => api<{ data: import('./types').Route }>('/routes', { method: 'POST', body }),
    update: (id: string, body: unknown) => api<{ data: import('./types').Route }>(`/routes/${id}`, { method: 'PATCH', body }),
    remove: (id: string) => api<{ ok: boolean }>(`/routes/${id}`, { method: 'DELETE' }),
  },

  keys: {
    list: () => api<{ data: import('./types').APIKey[] }>('/api-keys'),
    create: (body: unknown) => api<{ data: import('./types').APIKey; secret: string }>('/api-keys', { method: 'POST', body }),
    update: (id: string, body: unknown) => api<{ data: import('./types').APIKey }>(`/api-keys/${id}`, { method: 'PATCH', body }),
    revoke: (id: string) => api<{ ok: boolean }>(`/api-keys/${id}/revoke`, { method: 'POST' }),
  },

  policies: {
    list: () => api<{ data: import('./types').Policy[] }>('/policies'),
    create: (body: unknown) => api<{ data: import('./types').Policy }>('/policies', { method: 'POST', body }),
    update: (id: string, body: unknown) => api<{ data: import('./types').Policy }>(`/policies/${id}`, { method: 'PATCH', body }),
    remove: (id: string) => api<{ ok: boolean }>(`/policies/${id}`, { method: 'DELETE' }),
  },

  ipRules: {
    list: () => api<{ data: import('./types').IPRule[] }>('/ip-rules'),
    create: (body: unknown) => api<{ data: import('./types').IPRule }>('/ip-rules', { method: 'POST', body }),
    remove: (id: string) => api<{ ok: boolean }>(`/ip-rules/${id}`, { method: 'DELETE' }),
  },

  devices: {
    list: () => api<{ data: import('./types').Device[] }>('/devices'),
    block: (id: string, blocked: boolean) =>
      api<{ blocked: boolean }>(`/devices/${id}/block`, { method: 'POST', body: { blocked } }),
    rename: (id: string, label: string) => api<{ ok: boolean }>(`/devices/${id}/rename`, { method: 'POST', body: { label } }),
  },

  blocked: {
    list: () => api<{ data: import('./types').BlockEntry[] }>('/blocked'),
    unblock: (id: string) => api<{ ok: boolean }>(`/blocked/${id}`, { method: 'DELETE' }),
  },

  activity: {
    list: (params: Record<string, string | number>) => {
      const qs = new URLSearchParams(Object.entries(params).map(([k, v]) => [k, String(v)])).toString();
      return api<{ data: import('./types').ActivityEntry[]; total: number; limit: number; offset: number }>(
        `/activity?${qs}`,
      );
    },
    get: (id: string) => api<{ data: import('./types').ActivityEntry }>(`/activity/${id}`),
  },

  usage: (groupBy: string, days: number) =>
    api<{
      data: import('./types').UsageRow[];
      summary: import('./types').Metrics;
      group_by: string;
      days: number;
    }>(`/usage?group_by=${groupBy}&days=${days}`),

  settings: {
    get: () =>
      api<{ data: import('./types').SettingsMap; version: string; master_key_set: boolean }>('/settings'),
    update: (body: Record<string, string>) => api<{ ok: boolean }>('/settings', { method: 'PATCH', body }),
  },

  config: {
    /** Fetches the credential-free config snapshot and downloads it as JSON. */
    export: async (): Promise<ConfigExport> => {
      const cfg = await api<ConfigExport>('/config/export');
      const stamp = (cfg.exported_at || new Date().toISOString()).replace(/[:.]/g, '-');
      downloadJSON(`myrouter-config-${stamp}.json`, cfg);
      return cfg;
    },
    /** Uploads a previously exported snapshot; the backend upserts by name/alias. */
    import: (file: unknown) => api<ConfigImportResult>('/config/import', { method: 'POST', body: file }),
  },
};
