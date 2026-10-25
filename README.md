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
.\dist\MyRouter.exe -tray -open
```

- Runs in the background with a system-tray icon.
- Tray menu: **Open dashboard**, **Copy gateway URL**, **Quit MyRouter**.
- Install a Desktop shortcut: `.\dist\MyRouter.exe -install-only`
- Autostart at login: `.\dist\MyRouter.exe -install-shortcut -autostart`
- Desktop logs: `data/myrouter.log`

## Configuration

| Env | Default | Description |
| --- | --- | --- |
| `MYROUTER_ADDR` | `:20128` | Listen address |
| `MYROUTER_DATA` | `./data` | SQLite database, master key, desktop log |
| `MYROUTER_MASTER_KEY` | — | Base64 32-byte key or passphrase; generated when empty |
| `MYROUTER_MASTER_KEY_SALT` | — | Extra salt for passphrase-derived keys |
| `MYROUTER_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `MYROUTER_LOG_RETENTION_DAYS` | `30` | Request-log retention |

## Connect a client

Two different credentials exist:

1. **Provider key** (upstream, e.g. your vendor API key) — Providers → Add Provider. The key is encrypted at rest and never shown again. **Test Connection** must succeed before **Save** is enabled.
2. **Client key** (`mr-sk-…`, issued by MyRouter) — Dashboard → **Generate API key**, or API Keys → Create Key. It is shown once.

Point any [OI]-compatible SDK at the gateway base URL (`http://localhost:20128/v1` by default). The dashboard shows ready-to-copy cURL, JavaScript, and Python snippets with the generated key.

For a public deployment set **Settings → API → Base URL** to your public URL, e.g. `https://router.example.com/v1`.

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
