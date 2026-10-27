# MyRouter

**One endpoint. Every model.**

Self-hosted AI routing gateway: a single [OI]-compatible endpoint for many upstream providers, with automatic fallback, per-key access limits, and a dashboard. Ships as one binary with the web UI embedded.

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

Point any [OI]-compatible SDK at the gateway base URL (`http://localhost:20128/v1` by default). The dashboard shows ready-to-copy cURL, JavaScript, and Python snippets with the generated key.

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
- Retrieval skills ingest `config.sync_paths` (local files) on **Sync now** or
  every `sync_interval_min` in the background. Point `xerj` at a folder of
  cloned libraries and `ai-memory` at `<ai-memory data dir>/wiki`.
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
`https://skills.sh/owner/repo/skill`) - MyRouter fetches the `SKILL.md` from the
public GitHub repo and installs it as a prompt skill, bound to all models by
default.

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

Before committing, scan for accidental credentials:

```powershell
.\scripts\check-secrets.ps1
.\scripts\check-secrets.ps1 -StagedOnly
```

## Backup

Copy `data/myrouter.db` and `data/master.key`. Without the master key the stored provider credentials cannot be decrypted.
