# Build the MyRouter single binary (web assets are embedded).
#   .\scripts\build-windows.ps1            # console build (server / VPS / debug)
#   .\scripts\build-windows.ps1 -Gui       # no console window (desktop + tray)
param(
  [switch]$Gui,
  [switch]$SkipSecrets,
  [string]$Out = ""
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

if (-not $SkipSecrets) {
  & (Join-Path $PSScriptRoot "check-secrets.ps1")
  if ($LASTEXITCODE -ne 0) { throw "secret scan failed; build aborted" }
}

Write-Host "Building web assets..." -ForegroundColor Cyan
Push-Location (Join-Path $root "web")
try {
  npm run build
  if ($LASTEXITCODE -ne 0) { throw "web build failed" }
} finally {
  Pop-Location
}

if (-not $Out) {
  $Out = if ($Gui) { "dist\MyRouter.exe" } else { "myrouter.exe" }
}
$ldflags = "-s -w"
if ($Gui) { $ldflags = "-s -w -H windowsgui" }

Write-Host "Building $Out..." -ForegroundColor Cyan
go build -trimpath -ldflags $ldflags -o $Out ./cmd/myrouter
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

Write-Host "Done: $Out" -ForegroundColor Green
Write-Host "Install desktop shortcut: $Out -install-shortcut"
