# Fail if any tracked/untracked file looks like it contains a real credential.
# Usage:
#   .\scripts\check-secrets.ps1              # scan working tree
#   .\scripts\check-secrets.ps1 -StagedOnly  # scan staged files before commit
param([switch]$StagedOnly)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$files = if ($StagedOnly) {
  @(git diff --cached --name-only --diff-filter=ACMR)
} else {
  @(git ls-files; git ls-files --others --exclude-standard) | Sort-Object -Unique
}

$patterns = [ordered]@{
  "Google API key"      = 'AIza[0-9A-Za-z_\-]{30,}'
  "OpenAI key"          = 'sk-(proj-)?[A-Za-z0-9_\-]{20,}'
  "Anthropic key"       = 'sk-ant-[A-Za-z0-9_\-]{20,}'
  "AWS access key"      = 'AKIA[0-9A-Z]{16}'
  "Groq key"            = 'gsk_[A-Za-z0-9]{20,}'
  "xAI key"             = 'xai-[A-Za-z0-9]{20,}'
  "GitHub token"        = 'gh[pousr]_[A-Za-z0-9]{20,}'
  "Slack token"         = 'xox[baprs]-[A-Za-z0-9\-]{10,}'
  "Private key"         = 'BEGIN (RSA|OPENSSH|EC|PGP) PRIVATE KEY'
  "MyRouter setup token"= 'mr-setup-[0-9a-f]{32}'
  "MyRouter API key"    = 'mr-sk-[0-9a-f]{40,}'
}

$hits = @()
foreach ($f in $files) {
  if (-not $f -or -not (Test-Path -LiteralPath $f)) { continue }
  $item = Get-Item -LiteralPath $f
  if ($item.PSIsContainer -or $item.Length -gt 2MB) { continue }
  try { $text = Get-Content -LiteralPath $f -Raw -Encoding UTF8 } catch { continue }
  foreach ($name in $patterns.Keys) {
    if ([regex]::IsMatch($text, $patterns[$name])) {
      $hits += [pscustomobject]@{ File = $f; Pattern = $name }
    }
  }
}

if ($hits.Count -gt 0) {
  Write-Host "Secret scan FAILED:" -ForegroundColor Red
  $hits | Sort-Object File, Pattern | Format-Table -AutoSize | Out-String | Write-Host
  Write-Host "Remove the value (use runtime-built test fixtures), then retry." -ForegroundColor Yellow
  exit 1
}

Write-Host "Secret scan clean ($($files.Count) files)." -ForegroundColor Green
exit 0
