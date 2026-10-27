# Tidy the repo after a new build lands.
# Keeps the current binary + runtime data, drops stale build/QA leftovers.
#   .\scripts\clean-artifacts.ps1 -DryRun   # preview only
#   .\scripts\clean-artifacts.ps1           # actually remove
param(
  [switch]$DryRun,
  [int]$KeepQaDirs = 3,
  [int]$KeepDbBackups = 3
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$removed = 0
$skipped = 0
$freed = 0

function Remove-Target {
  param([string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) { return }
  $item = Get-Item -LiteralPath $Path -Force
  if ($item.PSIsContainer) {
    $size = (Get-ChildItem -LiteralPath $Path -Recurse -Force -File -ErrorAction SilentlyContinue | Measure-Object Length -Sum).Sum
  } else {
    $size = $item.Length
  }
  if ($null -eq $size) { $size = 0 }
  if ($DryRun) {
    Write-Host ("[dry] {0}  ({1:N1} MB)" -f $item.FullName, ($size / 1MB))
  } else {
    Remove-Item -LiteralPath $Path -Recurse -Force -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $Path) {
      Write-Host ("[skip] {0}  (in use / access denied)" -f $item.FullName) -ForegroundColor Yellow
      $script:skipped++
      return
    }
    Write-Host ("[del] {0}  ({1:N1} MB)" -f $item.FullName, ($size / 1MB))
  }
  $script:removed++
  $script:freed += $size
}

# 1) dist: keep the current desktop build, drop older binaries/backups.
$dist = Join-Path $root "dist"
if (Test-Path -LiteralPath $dist) {
  Get-ChildItem -LiteralPath $dist -Force -File |
    Where-Object { $_.Name -ne "MyRouter.exe" -and ($_.Extension -in @(".exe", ".dll") -or $_.Name -like "*.exe~") } |
    ForEach-Object { Remove-Target $_.FullName }
}

# 2) Root patch backups: keep only the most recent one.
Get-ChildItem -LiteralPath $root -Force -Directory -Filter ".backup-patch-*" |
  Sort-Object LastWriteTime -Descending |
  Select-Object -Skip 1 |
  ForEach-Object { Remove-Target $_.FullName }

# 3) qa-artifacts: keep the newest runs, drop old runs and loose scratch scripts.
$qa = Join-Path $root "qa-artifacts"
if (Test-Path -LiteralPath $qa) {
  Get-ChildItem -LiteralPath $qa -Force -Directory |
    Sort-Object LastWriteTime -Descending |
    Select-Object -Skip $KeepQaDirs |
    ForEach-Object { Remove-Target $_.FullName }
  Get-ChildItem -LiteralPath $qa -Force -File |
    ForEach-Object { Remove-Target $_.FullName }
}

# 4) Stale screenshot DB from an old QA session.
Remove-Target (Join-Path $root "data-shots")

# 5) Keep only the newest DB backups.
$dbBackups = Join-Path $root "data\backups"
if (Test-Path -LiteralPath $dbBackups) {
  Get-ChildItem -LiteralPath $dbBackups -Force -File -Filter "*.db" |
    Sort-Object LastWriteTime -Descending |
    Select-Object -Skip $KeepDbBackups |
    ForEach-Object { Remove-Target $_.FullName }
}

$verb = if ($DryRun) { "would free" } else { "freed" }
Write-Host ""
Write-Host ("Cleanup done: {0} item(s), {1} {2:N1} MB." -f $removed, $verb, ($freed / 1MB)) -ForegroundColor Green
if ($skipped -gt 0) {
  Write-Host ("Skipped {0} item(s) still locked; stop the test mock/server and rerun." -f $skipped) -ForegroundColor Yellow
}
