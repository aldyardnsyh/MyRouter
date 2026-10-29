# MyRouter

**One endpoint. Every model.**

[![Release](https://img.shields.io/github/v/release/aldyardnsyh/MyRouter?sort=semver&label=release)](https://github.com/aldyardnsyh/MyRouter/releases/latest)
[![Build](https://img.shields.io/badge/build-go%20build%20.%2F...-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Tests](https://img.shields.io/badge/tests-go%20test%20.%2F...-success)](https://github.com/aldyardnsyh/MyRouter)
[![Go](https://img.shields.io/badge/Go-1.23%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![React](https://img.shields.io/badge/React-18-61DAFB?logo=react&logoColor=white)](https://react.dev)
[![TypeScript](https://img.shields.io/badge/TypeScript-5.6-3178C6?logo=typescript&logoColor=white)](https://www.typescriptlang.org)
[![Tailwind CSS](https://img.shields.io/badge/Tailwind-3.4-06B6D4?logo=tailwindcss&logoColor=white)](https://tailwindcss.com)
[![SQLite](https://img.shields.io/badge/SQLite-embedded-003B57?logo=sqlite&logoColor=white)](https://sqlite.org)
[![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20Linux%20%7C%20macOS-lightgrey)](#desktop-app-windows)
[![License](https://img.shields.io/badge/license-see%20LICENSE.md-lightgrey)](LICENSE.md)

Self-hosted AI routing gateway: a single OpenAI-compatible endpoint for many upstream providers, with automatic fallback, per-key access limits, and a dashboard. Ships as one binary with the web UI embedded.

**[Download the latest release](https://github.com/aldyardnsyh/MyRouter/releases/latest)** &middot; Windows users can run `MyRouter.exe` directly (tray app, no installer).

![MyRouter dashboard](assets/screenshots/dashboard.png)

## Features

| | |
| --- | --- |
| **One endpoint, many providers** | OpenAI, Anthropic, Gemini, DeepSeek and any OpenAI-compatible gateway behind a single `/v1` base URL. |
| **Routes with fallback** | Virtual model aliases with an ordered primary → fallback chain and per-category triggers (429, timeout, 5xx, network, billing). |
| **Skills** | Server-side context injected automatically: instructions, memory, retrieved knowledge, past exchanges, vision bridging. |
| **Guard** | RPM/TPM, daily and monthly token caps, concurrency, IP and device limits, auto-blocking, per-key and per-route policies. |
| **Observability** | Live routing map, hourly/daily charts, per-request trace with every attempt, token and cost accounting from official catalog prices. |
| **Single binary** | Go server with the React console embedded; SQLite storage, no external services. |

## Screenshots

Every screen is captured from a real instance (`tests/seed_demo.py` builds the demo state through the public admin API, so nothing is mocked in the UI).

### Dashboard

Live routing, provider health, throughput chart with 24h/3d/7d/30d ranges, and recent activity.

| Requests (24h) | Tokens | Latency |
| --- | --- | --- |
| ![Dashboard requests](assets/screenshots/dashboard.png) | ![Dashboard tokens](assets/screenshots/dashboard-tokens.png) | ![Dashboard latency](assets/screenshots/dashboard-latency.png) |

| 3 days | 7 days | 30 days |
| --- | --- | --- |
| ![3 day range](assets/screenshots/dashboard-range-3d.png) | ![7 day range](assets/screenshots/dashboard-range-7d.png) | ![30 day range](assets/screenshots/dashboard-range-30d.png) |

Mobile layout: [dashboard-mobile.png](assets/screenshots/dashboard-mobile.png) &middot; Sign-in: [login.png](assets/screenshots/login.png)

### Providers

Upstream endpoints, credentials, health, and the per-provider tabs (Overview, Models, Guard Policy).

| Providers | Detail | Models | Guard Policy |
| --- | --- | --- | --- |
| ![Providers](assets/screenshots/providers.png) | ![Provider detail](assets/screenshots/provider-detail.png) | ![Provider models](assets/screenshots/provider-detail-models.png) | ![Provider guard policy](assets/screenshots/provider-detail-guard-policy.png) |

### Models and Routes

Discovered models with context windows, official prices and capabilities; routes with primary and fallback chains.

| Models | Routes |
| --- | --- |
| ![Models](assets/screenshots/models.png) | ![Routes](assets/screenshots/routes.png) |

### Skills

Built-in and custom skills, bindings, and the knowledge drawer for a skill's stored items.

| Skills | Knowledge items |
| --- | --- |
| ![Skills](assets/screenshots/skills.png) | ![Skill knowledge](assets/screenshots/skills-knowledge.png) |

### API Keys and Guard

Client keys with per-key route allow-lists and policies; Guard covers Policies, IP Rules, Devices and Blocked.

| API Keys | Policies | IP Rules |
| --- | --- | --- |
| ![API keys](assets/screenshots/api-keys.png) | ![Guard policies](assets/screenshots/guard-policies.png) | ![Guard IP rules](assets/screenshots/guard-ip-rules.png) |

| Guard overview | Devices | Blocked |
| --- | --- | --- |
| ![Guard](assets/screenshots/guard.png) | ![Guard devices](assets/screenshots/guard-devices.png) | ![Guard blocked](assets/screenshots/guard-blocked.png) |

### Activity and Test Lab

Request log with filters and per-request trace, usage rollups, and the playground for sending real prompts through a route.

| Activity | Usage | Request detail |
| --- | --- | --- |
| ![Activity](assets/screenshots/activity.png) | ![Usage](assets/screenshots/activity-usage.png) | ![Request detail](assets/screenshots/activity-detail.png) |

![Test Lab](assets/screenshots/playground.png)

### Settings

| General | API | Security |
| --- | --- | --- |
| ![Settings](assets/screenshots/settings.png) | ![Settings API](assets/screenshots/settings-api.png) | ![Settings security](assets/screenshots/settings-security.png) |

| Routing | Guard defaults | Storage | System |
| --- | --- | --- | --- |
| ![Settings routing](assets/screenshots/settings-routing.png) | ![Settings guard](assets/screenshots/settings-guard.png) | ![Settings storage](assets/screenshots/settings-storage.png) | ![Settings system](assets/screenshots/settings-system.png) |

Screenshots are reproducible:

```bash
python tests/demo_upstream.py --port 20401          # 3-protocol demo upstream
MYROUTER_DATA=./data-demo ./myrouter -addr :20299 -no-tray
python tests/seed_demo.py --base http://127.0.0.1:20299
python tests/capture_screenshots.py --base http://127.0.0.1:20299
```


## Requirements

- Go 1.23+ (the module declares a newer toolchain; `GOTOOLCHAIN=auto` works)
- Node.js 20+ and npm

## Build and run

```bash
cd web && npm install && npm run build && cd ..
go build -o myrouter ./cmd/myrouter
./myrouter
```

Open <http://localhost:20128>. On first start the server prints a one-time setup token to the log; setup from localhost does not require it.

## Desktop app (Windows)

```powershell
.\scripts\build-windows.ps1 -Gui
.\dist\MyRouter.exe -open
```

- The system-tray icon is enabled by default on Windows, whether the app is
  launched from Explorer, a shortcut, or a terminal. Use `-no-tray` (or
  `-tray=false`) for a headless server/container mode.
- A console build launched by double-click hides its own console window once
  the tray icon is ready; a console started from an existing terminal (CLI)
  keeps its window and logs. Desktop tray mode always mirrors logs to
  `data/myrouter.log`.
- If the tray cannot be created in a build without a console, MyRouter shows an
  error dialog and exits instead of leaving an invisible background process.
- Tray menu: **Open dashboard**, **Copy gateway URL**, **Quit MyRouter**.
- Install a Desktop shortcut: `.\dist\MyRouter.exe -install-only`
- Autostart at login: `.\dist\MyRouter.exe -install-shortcut -autostart`
- Desktop logs: `data/myrouter.log`
- Start from scratch: `.\dist\MyRouter.exe -reset-data` (or `MYROUTER_RESET_DATA=1`)
  moves the live database, its WAL files, and `master.key` into
  `data/backups/*.reset.bak` first, then boots into first-run setup. Nothing is
  deleted, and the reset is never automatic — an existing gateway is far too
  valuable to wipe without an explicit request.

> Windows 11 puts newly registered tray icons in the notification-area overflow
> (the `^` chevron) by default. Drag the MyRouter icon to the taskbar once and
> Windows remembers it; the app itself always registers the icon.

## Configuration

| Env | Default | Description |
| --- | --- | --- |
| `MYROUTER_ADDR` | `:20128` | Listen address |
| `MYROUTER_DATA` | `./data` | SQLite database, master key, desktop log |
| `MYROUTER_MASTER_KEY` | — | Base64 32-byte key or passphrase; generated when empty |
| `MYROUTER_MASTER_KEY_SALT` | — | Extra salt for passphrase-derived keys |
| `MYROUTER_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `MYROUTER_LOG_RETENTION_DAYS` | `30` | Request-log retention |
| `MYROUTER_RESET_DATA` | — | `1`/`true` = back up and clear the local database at startup |

## Connect a client

Two different credentials exist:

1. **Provider key** (upstream, e.g. your vendor API key) — Providers → Add Provider. The key is encrypted at rest and never shown again. Saving automatically tests the connection and imports the provider's models (context windows and official prices are filled in when the model is in the models.dev catalog), so Routes can be created right away. **Test Connection** and **Refresh models** stay available for manual re-checks.
2. **Client key** (`mr-sk-…`, issued by MyRouter) — Dashboard → **Generate API key**, or API Keys → Create Key. It is shown once.

Point any OpenAI-compatible SDK at the gateway base URL (`http://localhost:20128/v1` by default). The dashboard shows ready-to-copy cURL, JavaScript, and Python snippets with the generated key.

For a public deployment set **Settings → API → Base URL** to your public URL, e.g. `https://router.example.com/v1`.

## Model metadata and cost estimates

- When models are discovered, MyRouter fills **context window**, **input price**
  and **output price** from the official [models.dev](https://models.dev) catalog
  (cached locally, refreshed daily, with an embedded offline fallback for common
  models). Prices are USD per 1M tokens and are shown on the Models and Provider
  detail pages.
- Every successful request records an estimated cost from those prices. Activity
  → **Usage** shows totals grouped by route, provider, API key or day, plus a
  **cost by model** breakdown (per provider and per model) for 7/30/90 days. The
  Dashboard also shows an estimated spend card.
- Estimates can differ slightly from provider invoices (caching discounts,
  batch pricing, negotiated rates, or models missing from the catalog are not
  modelled). Unknown models show "Cost unavailable" instead of a fake number.

## Skills (automatic model context)

Skills add knowledge and instructions to every model alias automatically. They
run server-side on the gateway, before the request is forwarded upstream, and are
inserted as extra system context; clients do not need to change anything.

- **Global bindings** are what make every model — including routes created later —
  inherit a skill. Per-route bindings opt a skill into one alias only.
- Built-ins: `xerj` (retrieval over a local code/library tree), `ai-memory`
  (retrieval over an ai-memory markdown wiki), `chat-knowledge` (captures past
  Q/A and retrieves similar exchanges), `vision` (describes images with a
  vision-capable model when the target cannot see them), `memory` (manual
  notes), `core` (house rules, disabled by default).
- Retrieval skills ingest `config.sync_paths` (local files) on **Sync now**,
  every `sync_interval_min` in the background, **and automatically on the first
  request that needs them** (on-request sync: never-synced skills sync inline
  once, stale ones refresh in the background without slowing the request).
  Point `xerj` at a folder of cloned libraries and `ai-memory` at
  `<ai-memory data dir>/wiki`. The Skills page shows **Last sync** per skill so
  you can verify freshness.
- Ranking is hybrid BM25 (rare terms weigh more, short dense docs beat long
  diluted ones) with field bonuses (title/source) and slight chat freshness.
- Chat ↔ memory complement each other: repeated preference patterns in
  `chat-knowledge` captures (3x in 7 days) are auto-promoted to permanent
  `memory` items. Injection budget prioritises prompt → memory → rest.
- Vision skills also inject their capability text (not only via the image
  bridge), so the model knows the visual capability on text-only requests.
- The response header `X-MyRouter-Skills` lists the skills that contributed, and
  the Activity detail shows a `skills` trace step with retrieval counts.
- Privacy: `chat-knowledge` stores captured question/answer pairs in the local
  SQLite database. Disable it or bind it to selected routes when prompts may
  contain secrets.

Configure everything from the dashboard **Skills** page: enable/disable, edit
config, manage knowledge items, and bind to all models or specific routes. Use
**Test Lab** to send a real prompt through a route - skills are applied
automatically and the result shows which provider/model answered. **Import from
skills.sh** accepts a pasted skills.sh link (for example
`https://skills.sh/owner/repo/skill`), a GitHub repo URL, an `owner/repo/skill`
shorthand, or even the `npx skills add <url> --skill <name>` command form -
MyRouter fetches the `SKILL.md` from the public GitHub repo, infers the skill
type and capability, seeds starter content, and binds it to all models by
default. Fuzzy name matching resolves skills whose directory name differs
(e.g. `--skill hyperframes-animation` → `skills/hyperframes/SKILL.md`).

## Docker

```bash
docker build -t myrouter .
docker run -d --name myrouter --restart unless-stopped \
  -p 127.0.0.1:20128:20128 \
  -v myrouter-data:/data \
  -e MYROUTER_MASTER_KEY='<base64 32-byte>' \
  myrouter
```

Put it behind a TLS reverse proxy (Caddy/nginx) and do not expose the port directly.

## Test

```bash
go test ./...
cd web && npm run typecheck
```

End-to-end API harness (starts nothing by itself; needs a running instance and the mock upstream):

```bash
python tests/mock_upstream.py --port 20400 &
go build -o myrouter ./cmd/myrouter
MYROUTER_ENV=development MYROUTER_DATA=./data-e2e ./myrouter -addr :20299 &
python tests/e2e.py --base-url http://127.0.0.1:20299 --mock-url http://127.0.0.1:20400/v1
```

Demo and documentation screenshots use a separate, richer harness. `demo_upstream.py`
speaks the OpenAI, Anthropic and Gemini wire formats at once with real model ids,
`seed_demo.py` builds the demo state through the admin API and the gateway (so
fallbacks, blocked rows and cost estimates are real), and `capture_screenshots.py`
writes the images referenced above:

```bash
python tests/demo_upstream.py --port 20401 &
MYROUTER_DATA=./data-demo ./myrouter -addr :20299 -no-tray &
python tests/seed_demo.py --base http://127.0.0.1:20299
python tests/capture_screenshots.py --base http://127.0.0.1:20299
```

Before committing, scan for accidental credentials:

```powershell
.\scripts\check-secrets.ps1
.\scripts\check-secrets.ps1 -StagedOnly
```

## Backup

Copy `data/myrouter.db` and `data/master.key`. Without the master key the stored provider credentials cannot be decrypted.
