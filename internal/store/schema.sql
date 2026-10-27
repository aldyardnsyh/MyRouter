-- MyRouter — SQLite schema (contract, PRD §9)
-- Applied by internal/store migrations on startup. WAL mode.
-- All timestamps are Unix epoch seconds (INTEGER) or RFC3339 TEXT where noted.

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS user (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS session (
  id         TEXT PRIMARY KEY,          -- sha256 of the session cookie value
  user_id    TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  csrf_token TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS provider (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  type       TEXT NOT NULL,             -- openai|anthropic|gemini|deepseek|openai-compatible
  base_url   TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'unknown',
  enabled    INTEGER NOT NULL DEFAULT 1,
  last_check INTEGER,
  last_error TEXT,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS credential (
  id          TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES provider(id) ON DELETE CASCADE,
  ciphertext  BLOB NOT NULL,
  nonce       BLOB NOT NULL,
  key_version INTEGER NOT NULL DEFAULT 1,
  hint        TEXT                        -- masked hint e.g. sk-****a9f2
);

CREATE TABLE IF NOT EXISTS model (
  id             TEXT PRIMARY KEY,
  provider_id    TEXT NOT NULL REFERENCES provider(id) ON DELETE CASCADE,
  model_id       TEXT NOT NULL,
  display_name   TEXT NOT NULL,
  context_window INTEGER NOT NULL DEFAULT 0,
  price_in       REAL,                    -- NULL = cost unavailable
  price_out      REAL,
  capabilities   TEXT NOT NULL DEFAULT '', -- comma separated: chat,vision,tools
  available      INTEGER NOT NULL DEFAULT 1,
  created_at     INTEGER NOT NULL,
  UNIQUE (provider_id, model_id)
);

CREATE TABLE IF NOT EXISTS route (
  id          TEXT PRIMARY KEY,
  alias       TEXT NOT NULL UNIQUE,       -- canonical lowercase alias clients send as `model`
  description TEXT NOT NULL DEFAULT '',
  enabled     INTEGER NOT NULL DEFAULT 1,
  strategy    TEXT NOT NULL DEFAULT 'priority',
  retry       INTEGER NOT NULL DEFAULT 1,
  timeout_ms  INTEGER NOT NULL DEFAULT 30000,
  policy_id   TEXT REFERENCES policy(id) ON DELETE SET NULL,
  fallback_on TEXT NOT NULL DEFAULT 'rate_limit,timeout,upstream_5xx,network',
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS route_target (
  id       TEXT PRIMARY KEY,
  route_id TEXT NOT NULL REFERENCES route(id) ON DELETE CASCADE,
  model_id TEXT NOT NULL REFERENCES model(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  role     TEXT NOT NULL DEFAULT 'fallback'  -- primary|fallback
);

CREATE TABLE IF NOT EXISTS api_key (
  id             TEXT PRIMARY KEY,
  name           TEXT NOT NULL,
  prefix         TEXT NOT NULL,             -- mr-sk-1a2b (displayable)
  hash           TEXT NOT NULL UNIQUE,      -- sha256(secret)
  enabled        INTEGER NOT NULL DEFAULT 1,
  created_at     INTEGER NOT NULL,
  expires_at     INTEGER,
  last_used_at   INTEGER,
  policy_id      TEXT REFERENCES policy(id) ON DELETE SET NULL,
  allowed_routes TEXT NOT NULL DEFAULT ''   -- comma separated aliases, empty = all
);

CREATE TABLE IF NOT EXISTS policy (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL,
  rpm             INTEGER NOT NULL DEFAULT 0,   -- 0 = unlimited
  tpm             INTEGER NOT NULL DEFAULT 0,
  burst           INTEGER NOT NULL DEFAULT 0,   -- 0 = same as rpm
  daily_tokens    INTEGER NOT NULL DEFAULT 0,
  monthly_tokens  INTEGER NOT NULL DEFAULT 0,
  max_concurrent  INTEGER NOT NULL DEFAULT 0,
  max_tokens_req  INTEGER NOT NULL DEFAULT 0,
  max_ips         INTEGER NOT NULL DEFAULT 0,
  max_devices     INTEGER NOT NULL DEFAULT 0,
  allowed_models  TEXT NOT NULL DEFAULT '',
  on_exceed       TEXT NOT NULL DEFAULT 'reject', -- reject|queue|fallback
  queue_max_s     INTEGER NOT NULL DEFAULT 0,
  cooldown_s      INTEGER NOT NULL DEFAULT 0,
  auto_block_n    INTEGER NOT NULL DEFAULT 0,     -- after N violations
  auto_block_m    INTEGER NOT NULL DEFAULT 0,     -- within M minutes
  auto_block_s    INTEGER NOT NULL DEFAULT 0,     -- block lasts S seconds
  created_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS policy_binding (
  id         TEXT PRIMARY KEY,             -- == policy_id (one binding per object)
  policy_id  TEXT NOT NULL REFERENCES policy(id) ON DELETE CASCADE,
  scope_type TEXT NOT NULL,                -- global|provider|route|api_key|ip|device
  scope_id   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS ip_rule (
  id         TEXT PRIMARY KEY,
  scope_type TEXT NOT NULL DEFAULT 'global', -- global|api_key
  scope_id   TEXT NOT NULL DEFAULT '',
  cidr       TEXT NOT NULL,
  action     TEXT NOT NULL,                  -- allow|deny
  note       TEXT NOT NULL DEFAULT '',
  expires_at INTEGER,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS device (
  id          TEXT PRIMARY KEY,
  api_key_id  TEXT NOT NULL REFERENCES api_key(id) ON DELETE CASCADE,
  fingerprint TEXT NOT NULL,
  label       TEXT NOT NULL DEFAULT '',
  last_ip     TEXT NOT NULL DEFAULT '',
  user_agent  TEXT NOT NULL DEFAULT '',
  first_seen  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL,
  blocked     INTEGER NOT NULL DEFAULT 0,
  requests    INTEGER NOT NULL DEFAULT 0,
  UNIQUE (api_key_id, fingerprint)
);

CREATE TABLE IF NOT EXISTS block_entry (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,                -- ip|device|key
  value      TEXT NOT NULL,
  scope_id   TEXT NOT NULL DEFAULT '',     -- api_key id when relevant
  reason     TEXT NOT NULL,
  until      INTEGER,                      -- NULL = permanent
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS rate_counter (
  scope_key    TEXT NOT NULL,
  window_start INTEGER NOT NULL,
  tokens       REAL NOT NULL DEFAULT 0,
  count        REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (scope_key, window_start)
);

CREATE TABLE IF NOT EXISTS request_log (
  id              TEXT PRIMARY KEY,
  request_id      TEXT NOT NULL,
  ts              INTEGER NOT NULL,
  route_id        TEXT,
  requested_model TEXT NOT NULL DEFAULT '',
  model_id        TEXT,
  provider_id     TEXT,
  api_key_id      TEXT,
  ip              TEXT NOT NULL DEFAULT '',
  device_id       TEXT,
  status          TEXT NOT NULL,           -- ok|error|blocked|fallback
  http_status     INTEGER NOT NULL DEFAULT 200,
  latency_ms      INTEGER NOT NULL DEFAULT 0,
  in_tokens       INTEGER NOT NULL DEFAULT 0,
  out_tokens      INTEGER NOT NULL DEFAULT 0,
  fallback_from   TEXT,
  error_code      TEXT,
  guard_reason    TEXT,
  trace           TEXT NOT NULL DEFAULT '[]' -- JSON array of routing attempts
);

CREATE INDEX IF NOT EXISTS idx_request_log_ts ON request_log(ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_log_request_id ON request_log(request_id);

CREATE TABLE IF NOT EXISTS usage_daily (
  date        TEXT NOT NULL,               -- YYYY-MM-DD (admin timezone)
  route_id    TEXT NOT NULL DEFAULT '',
  provider_id TEXT NOT NULL DEFAULT '',
  api_key_id  TEXT NOT NULL DEFAULT '',
  requests    INTEGER NOT NULL DEFAULT 0,
  ok          INTEGER NOT NULL DEFAULT 0,
  failed      INTEGER NOT NULL DEFAULT 0,
  fallbacks   INTEGER NOT NULL DEFAULT 0,
  in_tokens   INTEGER NOT NULL DEFAULT 0,
  out_tokens  INTEGER NOT NULL DEFAULT 0,
  est_cost    REAL,
  PRIMARY KEY (date, route_id, provider_id, api_key_id)
);

-- Per-model rollup so the dashboard can show which model/provider spent what.
-- usage_daily stays provider/route/key scoped for backward compatibility.
CREATE TABLE IF NOT EXISTS usage_model_daily (
  date        TEXT NOT NULL,               -- YYYY-MM-DD (admin timezone)
  provider_id TEXT NOT NULL DEFAULT '',
  model_id    TEXT NOT NULL DEFAULT '',    -- model.id (mdl_...)
  requests    INTEGER NOT NULL DEFAULT 0,
  ok          INTEGER NOT NULL DEFAULT 0,
  failed      INTEGER NOT NULL DEFAULT 0,
  in_tokens   INTEGER NOT NULL DEFAULT 0,
  out_tokens  INTEGER NOT NULL DEFAULT 0,
  est_cost    REAL,
  PRIMARY KEY (date, provider_id, model_id)
);

CREATE INDEX IF NOT EXISTS idx_usage_model_date ON usage_model_daily(date);

CREATE TABLE IF NOT EXISTS system_setting (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS skill (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  type        TEXT NOT NULL,                 -- prompt|memory|retrieval
  enabled     INTEGER NOT NULL DEFAULT 1,
  config      TEXT NOT NULL DEFAULT '{}',    -- JSON: prompt,max_chars,top_k,limit
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS skill_item (
  id         TEXT PRIMARY KEY,
  skill_id   TEXT NOT NULL REFERENCES skill(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL DEFAULT 'note',   -- note|doc
  source     TEXT NOT NULL DEFAULT '',
  title      TEXT NOT NULL DEFAULT '',
  content    TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_skill_item_skill ON skill_item(skill_id, created_at DESC);

-- A skill applies to every route when bound globally, or only to the named
-- route aliases. Global bindings are what make every model inherit skills
-- automatically (including routes created later).
CREATE TABLE IF NOT EXISTS skill_binding (
  id         TEXT PRIMARY KEY,
  skill_id   TEXT NOT NULL REFERENCES skill(id) ON DELETE CASCADE,
  scope_type TEXT NOT NULL,                -- global|route
  scope_id   TEXT NOT NULL DEFAULT '',     -- route alias when scope_type=route
  UNIQUE (skill_id, scope_type, scope_id)
);

CREATE INDEX IF NOT EXISTS idx_skill_binding_skill ON skill_binding(skill_id);
CREATE INDEX IF NOT EXISTS idx_skill_binding_scope ON skill_binding(scope_type, scope_id);

-- Last ingest result per skill. Kept in its own table so the original skill
-- contract (PRD) does not need an ALTER on existing databases.
CREATE TABLE IF NOT EXISTS skill_state (
  skill_id        TEXT PRIMARY KEY REFERENCES skill(id) ON DELETE CASCADE,
  last_sync_at    INTEGER NOT NULL DEFAULT 0,
  last_sync_error TEXT NOT NULL DEFAULT '',
  last_sync_items INTEGER NOT NULL DEFAULT 0
);

-- Vision bridge cache: an image URL/data payload hashed to a text description.
-- New table only, so existing databases upgrade without ALTER TABLE.
CREATE TABLE IF NOT EXISTS skill_vision_cache (
  hash        TEXT PRIMARY KEY,
  description TEXT NOT NULL,
  created_at  INTEGER NOT NULL
);
