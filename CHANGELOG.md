# Changelog

All notable changes to MyRouter are documented in this file.

<!-- changelog:next -->

## [v0.1.3] — 2026-10-28

### Fixes

- **HTML error pages no longer leak into client responses** — `extractMessage` now detects when an upstream returns an HTML document (typical of Cloudflare, nginx, or other CDNs on 5xx), extracts the `<title>` instead, and returns a single-line message like `upstream sent an HTML error page: opencode.ai | 502: Bad gateway`. Previously the raw markup, truncated to 300 characters, was sent to the SDK and rendered as gibberish in opencode / Claude Code / any OpenAI-compatible client.

- **Fallback chains now actually try other models** — the circuit-breaker cooldown was keyed by provider ID only, so a single 5xx on one model cooled down every other model on the same provider and blocked the entire fallback chain. Cooldown for `upstream_5xx` and `timeout` is now scoped to `(provider, model)`; account-wide failures (`rate_limit`, `billing`, `network`) remain provider-wide, which is the correct behaviour.

- **Restarting the app now asks for login again** — admin sessions were not cleared on boot, so a stale cookie from the previous run let you straight into the dashboard after a quit/restart. Startup now calls `DeleteAllSessions()` on the database so every process start begins with an empty session table.

### Added

- **OpenCode config sync** — two new admin endpoints: `GET /admin/opencode/status` inspects `~/.config/opencode/opencode.json` (or the path set via `OPENCODE_CONFIG`) and reports which routes are missing from OpenCode's model list. `POST /admin/opencode/sync` merges every enabled, servable route into the OpenCode provider block, writes a `.bak-YYYYMMDD-HHMMSS` backup before overwriting, and uses atomic write + rename. A fresh API key can be passed in the request body so the `provider.myrouter.options.apiKey` field is updated at the same time.

- **UI: OpenCode card in Settings → API** — shows the detected config path, the number of ready routes vs declared models, which aliases are missing, and a "Sync to OpenCode" button with an optional API key input.

- **UI: sync button on the key-created modal** — after a new API key is generated, the modal now offers "Write key + models to OpenCode" so the secret and the current route list land in `opencode.json` in one click, instead of having to paste the key manually and sync separately.

### Under the hood

- New file `internal/api/opencode.go` (508 lines) with the status/sync handlers, JSONC-aware reader, atomic writer, and provider-body merger.
- New tests: `internal/api/opencode_test.go`, `internal/gateway/breaker_scope_test.go`, `internal/provider/error_message_test.go`.
- `internal/gateway/breaker.go` refactored to use scoped keys; `internal/gateway/gateway.go` updated to pass `mdl.ModelID` through the allow/fail/success path.
- `internal/provider/openai.go` — `extractMessage` now sanitizes HTML bodies; `errorMessage` is called from `MapError` for the OpenAI adapter (previously the message field was left raw).
- `cmd/myrouter/main.go` — session cleanup inserted after DB open, before the gateway starts.

---

## [v0.1.2] — 2026-10-27

First tagged release. One OpenAI-compatible endpoint in front of OpenAI, Anthropic, Gemini, DeepSeek and any OpenAI-compatible gateway, with automatic fallback, per-key Guard limits, server-side Skills, and a bundled dashboard.

### Download

`MyRouter.exe` (Windows, single binary, no installer)

1. Double-click `MyRouter.exe`. The tray icon appears and the dashboard opens at http://127.0.0.1:20128.
2. Create the admin account on first run.
3. Providers -> Add Provider (your upstream key), then create a Route alias.
4. API Keys -> Create Key, and point any OpenAI-compatible SDK at `http://127.0.0.1:20128/v1`.

Useful flags: `-no-tray` (headless server), `-open` (open the dashboard), `-reset-data` (back up and start fresh). Logs live in `data/myrouter.log`.

### Highlights

- **Routes with fallback** - ordered primary/fallback chains across providers, triggered per error category (429, timeout, 5xx, network, billing).
- **Skills** - instructions, memory, retrieved knowledge, past exchanges and vision bridging injected server-side before the request leaves the gateway.
- **Guard** - RPM/TPM, daily/monthly token caps, concurrency, IP and device limits, auto-blocking, per-key and per-route policies.
- **Observability** - live routing map, 24h/3d/7d/30d charts, per-request trace with every attempt, and cost estimates from official models.dev prices.
- **Single binary** - Go server with the React console embedded, SQLite storage, no external services.

### Fixes in this release

- Activity now records `fallback_from`, so fallback rows are marked again; previously the value was written into the Guard column and the fallback badge never appeared.
- Activity now records `model_id`, so the "Used model" column is populated and per-model usage can be attributed.
- Successful requests are counted once in the daily rollups instead of twice.

### Documentation

README now carries a screenshot gallery covering every page and every in-page menu (dashboard metrics and ranges, provider tabs, Guard tabs, activity/usage/detail, all settings sections, mobile, login). The screenshots are reproducible: `tests/demo_upstream.py`, `tests/seed_demo.py` and `tests/capture_screenshots.py` build and capture a real demo instance through the public admin API.

---

[Unreleased]: https://github.com/aldyardnsyh/MyRouter/compare/v0.1.3...HEAD
[v0.1.3]: https://github.com/aldyardnsyh/MyRouter/compare/v0.1.2...v0.1.3
[v0.1.2]: https://github.com/aldyardnsyh/MyRouter/releases/tag/v0.1.2
