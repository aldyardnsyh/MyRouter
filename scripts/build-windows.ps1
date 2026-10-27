# Build the MyRouter single binary (web assets are embedded).
#   .\scripts\build-windows.ps1            # console build (server / VPS / debug)
#   .\scripts\build-windows.ps1 -Gui       # no console window (desktop + tray)
param(
  [switch]$Gui,
  [switch]$SkipSecrets,
  [switch]$NoDesktopShortcut,
  [switch]$NoCleanup,
  [switch]$NoVersionBump,
  [string]$Version = "",
  [string]$Out = ""
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

if (-not $SkipSecrets) {
  & (Join-Path $PSScriptRoot "check-secrets.ps1")
  if ($LASTEXITCODE -ne 0) { throw "secret scan failed; build aborted" }
}

# Every build is a new app version. The patch number bumps automatically and
# is written back only after a successful build; pass -NoVersionBump to keep
# the current one, or -Version 1.2.3 to jump explicitly.
$versionFile = Join-Path $root "VERSION"
$currentVersion = if (Test-Path -LiteralPath $versionFile) { (Get-Content -LiteralPath $versionFile -Raw).Trim() } else { "0.1.0" }
if ($Version) {
  $buildVersion = $Version.TrimStart("v")
} elseif ($NoVersionBump) {
  $buildVersion = $currentVersion
} elseif ($currentVersion -match '^(\d+)\.(\d+)\.(\d+)$') {
  $buildVersion = "{0}.{1}.{2}" -f $Matches[1], $Matches[2], ([int]$Matches[3] + 1)
} else {
  throw "VERSION must be MAJOR.MINOR.PATCH, got '$currentVersion'"
}
Write-Host "Version: $buildVersion" -ForegroundColor Cyan

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
$ldflags = "$ldflags -X main.version=$buildVersion"

Write-Host "Building $Out..." -ForegroundColor Cyan
go build -trimpath -ldflags $ldflags -o $Out ./cmd/myrouter
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

# Persist the version only once the binary exists.
Set-Content -LiteralPath $versionFile -Value $buildVersion -NoNewline
Write-Host "Done: $Out (v$buildVersion)" -ForegroundColor Green

# Desktop builds go straight onto the Desktop so the newest version is one
# double-click away. The shortcut points at $Out, so rebuilds refresh it.
if ($Gui -and -not $NoDesktopShortcut) {
  & (Join-Path $PSScriptRoot "install-desktop.ps1") -Exe $Out
  if ($LASTEXITCODE -ne 0) { throw "desktop shortcut install failed" }
}

# Drop stale binaries, patch backups and old QA runs after a successful build.
if (-not $NoCleanup) {
  & (Join-Path $PSScriptRoot "clean-artifacts.ps1")
  if ($LASTEXITCODE -ne 0) { throw "artifact cleanup failed" }
}

