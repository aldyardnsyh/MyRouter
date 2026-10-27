# Put the newest build on the Desktop as a shortcut (points at the built exe,
# so future rebuilds update the app without touching the shortcut).
#   .\scripts\install-desktop.ps1
#   .\scripts\install-desktop.ps1 -Exe dist\MyRouter.exe
param(
  [string]$Exe = "dist\MyRouter.exe"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$path = Join-Path $root $Exe
if (-not (Test-Path -LiteralPath $path)) {
  throw "build not found: $path (run .\scripts\build-windows.ps1 -Gui first)"
}

$item = Get-Item -LiteralPath $path
Write-Host ("Installing desktop shortcut for {0} ({1:N0} bytes, {2})..." -f $item.FullName, $item.Length, $item.LastWriteTime) -ForegroundColor Cyan

# The GUI binary has no console, so PowerShell will not wait for it: use
# Start-Process -Wait or the shortcut file may be checked before it exists.
$proc = Start-Process -FilePath $item.FullName -ArgumentList "-install-only" -Wait -PassThru
if ($proc.ExitCode -ne 0) { throw "shortcut install failed (exit $($proc.ExitCode))" }

$link = Join-Path ([Environment]::GetFolderPath("Desktop")) "MyRouter.lnk"
if (-not (Test-Path -LiteralPath $link)) { throw "shortcut was not created at $link" }
$ws = New-Object -ComObject WScript.Shell
$sc = $ws.CreateShortcut($link)
Write-Host ("Desktop shortcut ready: {0}" -f $link) -ForegroundColor Green
Write-Host ("  target: {0} {1}" -f $sc.TargetPath, $sc.Arguments)
