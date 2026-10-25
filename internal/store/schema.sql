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

CREATE TABLE IF NOT EXISTS system_setting (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
