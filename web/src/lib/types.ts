// Shared API types mirroring docs/api/openapi.yaml.
export type ProviderType = 'openai' | 'anthropic' | 'gemini' | 'deepseek' | 'openai-compatible';

export type Status =
  | 'connected'
  | 'degraded'
  | 'rate_limited'
  | 'unavailable'
  | 'invalid_credentials'
  | 'unknown';

export interface Provider {
  id: string;
  name: string;
  type: ProviderType;
  base_url: string;
  status: Status;
  enabled: boolean;
  last_check: number | null;
  last_error: string;
  created_at: number;
  model_count: number;
  credential_hint?: string;
}

// Result of the connection probe run automatically when a provider is created
// or updated. `status` mirrors the provider status vocabulary.
export interface ProviderConnection {
  status: Status;
  message: string;
  latency_ms: number;
}

// POST /admin/providers and PATCH /admin/providers/{id} now save, test, and
// import models in one request.
export interface ProviderSaveResult {
  data: Provider;
  connection: ProviderConnection;
  models: number;
}

export interface Model {
  id: string;
  provider_id: string;
  provider_name?: string;
  model_id: string;
  display_name: string;
  context_window: number;
  price_in: number | null;
  price_out: number | null;
  capabilities: string;
  available: boolean;
  status?: Status;
}

export interface RouteTarget {
  id: string;
  route_id: string;
  model_id: string;
  position: number;
  role: 'primary' | 'fallback';
  model_name?: string;
  provider_id?: string;
  provider_name?: string;
  provider_type?: string;
  available: boolean;
}

export interface Route {
  id: string;
  alias: string;
  description: string;
  enabled: boolean;
  strategy: string;
  retry: number;
  timeout_ms: number;
  effort: 'low' | 'medium' | 'high';
  policy_id: string | null;
  policy_name?: string;
  fallback_on: string;
  created_at: number;
  targets: RouteTarget[];
}

export interface APIKey {
  id: string;
  name: string;
  prefix: string;
  enabled: boolean;
  created_at: number;
  expires_at: number | null;
  last_used_at: number | null;
  policy_id: string | null;
  policy_name?: string;
  allowed_routes: string;
}

export interface Policy {
  id: string;
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
  allowed_models: string;
  on_exceed: 'reject' | 'queue' | 'fallback';
  queue_max_s: number;
  cooldown_s: number;
  auto_block_n: number;
  auto_block_m: number;
  auto_block_s: number;
  created_at: number;
  bound_count: number;
}

export interface IPRule {
  id: string;
  scope_type: 'global' | 'api_key';
  scope_id: string;
  scope_name?: string;
  cidr: string;
  action: 'allow' | 'deny';
  note: string;
  expires_at: number | null;
  created_at: number;
}

export interface Device {
  id: string;
  api_key_id: string;
  api_key_name?: string;
  fingerprint: string;
  label: string;
  last_ip: string;
  user_agent: string;
  first_seen: number;
  last_seen: number;
  blocked: boolean;
  requests: number;
}

export interface BlockEntry {
  id: string;
  kind: 'ip' | 'device' | 'key';
  value: string;
  scope_id: string;
  reason: string;
  until: number | null;
  created_at: number;
}

export interface TraceStep {
  attempt: number;
  target: string;
  provider: string;
  outcome: 'success' | 'error' | 'skipped' | 'guard_blocked';
  category?: string;
  status_code?: number;
  latency_ms: number;
  message?: string;
}

export interface ActivityEntry {
  id: string;
  request_id: string;
  ts: number;
  route_id: string;
  route_alias?: string;
  requested_model: string;
  model_id: string;
  model_name?: string;
  provider_id: string;
  provider_name?: string;
  api_key_id: string;
  api_key_name?: string;
  api_key_prefix?: string;
  ip: string;
  device_id: string;
  status: 'ok' | 'error' | 'blocked';
  http_status: number;
  latency_ms: number;
  in_tokens: number;
  out_tokens: number;
  fallback_from: string;
  error_code: string;
  guard_reason: string;
  trace: TraceStep[];
}

export interface UsageRow {
  key: string;
  label: string;
  requests: number;
  ok: number;
  failed: number;
  fallbacks: number;
  in_tokens: number;
  out_tokens: number;
  est_cost: number | null;
}

// GET /admin/usage/models?days=N: per-provider-model usage with the official
// catalog price used for the estimate. Prices and cost are null when the model
// is missing from the catalog.
export interface UsageModelRow {
  provider_id: string;
  provider_name: string;
  provider_type: string;
  model_id: string;
  model_name: string;
  requests: number;
  ok: number;
  failed: number;
  in_tokens: number;
  out_tokens: number;
  est_cost: number | null;
  price_in: number | null;
  price_out: number | null;
}

export interface Metrics {
  requests: number;
  ok: number;
  failed: number;
  blocked: number;
  fallbacks: number;
  in_tokens: number;
  out_tokens: number;
  avg_latency_ms: number;
  success_rate: number;
  providers: number;
  active_routes: number;
}

export interface SeriesPoint {
  start: number;
  requests: number;
  ok: number;
  failed: number;
  blocked: number;
  fallbacks: number;
  in_tokens: number;
  out_tokens: number;
  avg_latency_ms: number;
  max_latency_ms: number;
}

export interface SeriesResponse {
  data: SeriesPoint[];
  summary: SeriesPoint;
  range: string;
  bucket_sec: number;
}

export interface Overview {
  metrics: Metrics;
  series: number[];
  recent: ActivityEntry[];
  providers: Provider[];
  base_url: string;
  version: string;
}

export interface LiveRequest {
  request_id: string;
  route: string;
  requested_model: string;
  provider_id: string;
  provider_name: string;
  key_prefix: string;
  ip: string;
  status: 'active' | 'ok' | 'error' | 'blocked' | string;
  started_at: number;
  elapsed_ms: number;
}

export interface LiveProvider {
  id: string;
  name: string;
  type: ProviderType;
  status: Status;
  enabled: boolean;
  active: number;
}

export interface LiveSnapshot {
  active: LiveRequest[];
  recent: LiveRequest[];
  providers: LiveProvider[];
  server_time: number;
}

export interface SettingsMap {
  base_url: string;
  api_port: string;
  cors_origins: string;
  trusted_proxies: string;
  session_ttl_hours: string;
  default_retry: string;
  default_timeout_ms: string;
  log_retention_days: string;
  store_request_body: string;
  allow_private_upstreams: string;
  timezone: string;
  theme: string;
}

export interface SetupStatus {
  needs_setup: boolean;
  has_admin: boolean;
  version: string;
}

/**
 * Body for POST /admin/setup/complete.
 *
 * `setup_token` is only required when the request does not come from
 * localhost (e.g. another device on the LAN). The token is printed in the
 * server log on first run and looks like `mr-setup-xxxx`.
 */
export interface SetupCompleteRequest {
  username: string;
  password: string;
  port: number;
  setup_token?: string;
}

export interface LoginResult {
  username: string;
  csrf_token: string;
}

export interface Page<T> {
  data: T[];
  total?: number;
  limit?: number;
  offset?: number;
}

export interface PlaygroundResult {
  route: string;
  request_id: string;
  status: 'ok' | 'error' | 'blocked' | string;
  provider: string;
  provider_id: string;
  model: string;
  model_id: string;
  content: string;
  latency_ms: number;
  ttft_ms: number;
  in_tokens: number;
  out_tokens: number;
  fallback: boolean;
  category?: string;
  error?: string;
  trace: TraceStep[];
}

export interface PlaygroundEvent {
  type: 'start' | 'delta' | 'error' | 'done';
  text?: string;
  error?: string;
  result?: PlaygroundResult;
}

export interface PlaygroundCall {
  route: string;
  system?: string;
  messages: { role: 'user' | 'assistant' | 'system'; content: string }[];
  max_tokens?: number;
  temperature?: number;
}

// ---------------------------------------------------------------------------
// Skills
// ---------------------------------------------------------------------------

export type SkillType = 'prompt' | 'memory' | 'retrieval' | 'chat' | 'vision';

// Which routes a skill applies to. `global` applies it to every model alias;
// `routes` holds route aliases for narrower bindings.
export interface SkillBindings {
  global: boolean;
  routes: string[];
}

export interface Skill {
  id: string;
  name: string;
  description: string;
  type: SkillType;
  enabled: boolean;
  // Per-type options (prompt, sync_paths, top_k, ...). The API returns this as
  // a JSON object, not a string.
  config: Record<string, unknown>;
  item_count: number;
  will_inject: boolean;
  created_at: number;
  bindings: SkillBindings;
  last_sync_at: number;
  last_sync_error: string;
  last_sync_items: number;
}

export interface SkillItem {
  id: string;
  skill_id: string;
  kind: string;
  source: string;
  title: string;
  content: string;
  created_at: number;
}

export interface SkillPreview {
  context: string;
  applied: { skill: string; kind: string; detail: string }[];
}

export interface SkillSyncResult {
  files: number;
  items: number;
  skipped: number;
  error: string;
}

// POST /admin/skills/import/preview: a parsed SKILL.md fetched from skills.sh
// (or GitHub). `truncated` means the stored content was capped by the backend.
export interface SkillImportPreview {
  name: string;
  description: string;
  content: string;
  source_url: string;
  owner: string;
  repo: string;
  path: string;
  truncated: boolean;
}

// POST /admin/skills/import. Name and description default to the parsed
// SKILL.md values; bind_global applies the new skill to every model.
export interface SkillImportRequest {
  url: string;
  name?: string;
  description?: string;
  bind_global?: boolean;
}
