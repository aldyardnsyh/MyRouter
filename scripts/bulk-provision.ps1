[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$CsvPath,
    [string]$MyRouterUrl = "http://127.0.0.1:20128",
    [string]$AdminUser = "admin",
    [string]$AdminPassword = $env:MYROUTER_ADMIN_PASSWORD,
    [ValidateSet("round_robin", "priority")][string]$Strategy = "round_robin",
    [int]$Retry = 0,
    [int]$TimeoutMs = 8000,
    [int]$MaxAccounts = 0,
    [switch]$Apply,
    [switch]$SkipTest,
    [switch]$RemoveFailed,
    [string]$ReportPath = "bulk-report.csv"
)

$ErrorActionPreference = "Stop"

function Mask-Key([string]$Key) {
    if ($Key.Length -le 10) {
        return "***"
    }
    return $Key.Substring(0, 6) + "..." + $Key.Substring($Key.Length - 4)
}

$rows = @(Import-Csv -LiteralPath $CsvPath)
if ($rows.Count -eq 0) {
    throw "CSV is empty"
}
$columns = $rows[0].PSObject.Properties.Name
foreach ($required in @("name", "base_url", "api_key")) {
    if ($columns -notcontains $required) {
        throw "CSV must contain column '$required'"
    }
}

$plan = @()
$names = @{}
foreach ($row in $rows) {
    if ($MaxAccounts -gt 0 -and $plan.Count -ge $MaxAccounts) {
        break
    }
    $name = ([string]$row.name).Trim()
    $baseUrl = ([string]$row.base_url).Trim().TrimEnd("/")
    $apiKey = ([string]$row.api_key).Trim()
    $model = if ($columns -contains "model") { ([string]$row.model).Trim() } else { "" }
    $type = if ($columns -contains "type" -and ([string]$row.type).Trim()) { ([string]$row.type).Trim() } else { "openai" }
    if (-not $name -or -not $baseUrl -or -not $apiKey) {
        throw "Row with empty name/base_url/api_key"
    }
    if ($names.ContainsKey($name)) {
        throw "Duplicate provider name: $name"
    }
    $names[$name] = $true
    $uri = [Uri]$baseUrl
    $isLoopback = $uri.Host -in @("localhost", "127.0.0.1", "::1")
    if ($uri.Scheme -ne "https" -and -not $isLoopback) {
        throw "base_url must use https: $baseUrl"
    }
    $plan += [pscustomobject]@{
        Name    = $name
        BaseUrl = $baseUrl
        ApiKey  = $apiKey
        Model   = $model
        Type    = $type
    }
}

Write-Host "Loaded $($plan.Count) account(s) from $CsvPath"
foreach ($item in $plan) {
    Write-Host ("  {0}  {1}  key={2}  model={3}" -f $item.Name, $item.BaseUrl, (Mask-Key $item.ApiKey), $item.Model)
}

if (-not $Apply) {
    Write-Host "Dry run only. Re-run with -Apply to provision and test."
    return
}

if (-not $AdminPassword) {
    $secure = Read-Host "MyRouter admin password" -AsSecureString
    $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try {
        $AdminPassword = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)
    }
    finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr)
    }
}

$base = $MyRouterUrl.TrimEnd("/")
$session = New-Object Microsoft.PowerShell.Commands.WebRequestSession

function Invoke-MyRouter {
    param(
        [string]$Method,
        [string]$Path,
        $Body,
        [string]$Csrf
    )
    $headers = @{}
    if ($Csrf) {
        $headers["X-CSRF-Token"] = $Csrf
    }
    $params = @{
        Method      = $Method
        Uri         = "$base$Path"
        WebSession  = $session
        Headers     = $headers
        ContentType = "application/json"
    }
    if ($null -ne $Body) {
        $params["Body"] = ($Body | ConvertTo-Json -Depth 12 -Compress)
    }
    try {
        return Invoke-RestMethod @params
    }
    catch {
        $text = $_.ErrorDetails.Message
        $status = 0
        $response = $_.Exception.Response
        if ($response) {
            $status = [int]$response.StatusCode
            if (-not $text) {
                $reader = New-Object System.IO.StreamReader($response.GetResponseStream())
                $text = $reader.ReadToEnd()
            }
        }
        if ($text) {
            throw "HTTP $status $Path : $text"
        }
        throw
    }
}

$login = Invoke-MyRouter -Method Post -Path "/admin/auth/login" -Body @{
    username = $AdminUser
    password = $AdminPassword
}
$csrf = $login.csrf_token
if (-not $csrf) {
    throw "Login failed: no CSRF token"
}

$providers = (Invoke-MyRouter -Method Get -Path "/admin/providers").data
$existingProviders = @{}
foreach ($provider in $providers) {
    $existingProviders[$provider.name] = $provider
}

$tempAlias = "pool-test-temp"
$tempRoute = (Invoke-MyRouter -Method Get -Path "/admin/routes").data | Where-Object { $_.alias -eq $tempAlias } | Select-Object -First 1
if ($tempRoute) {
    Invoke-MyRouter -Method Delete -Path "/admin/routes/$($tempRoute.id)" -Csrf $csrf | Out-Null
}
$tempRouteId = $null

$results = @()
$healthyByModel = @{}

foreach ($item in $plan) {
    $providerId = $null
    $created = $false
    $chosen = $null
    $status = "error"
    $category = ""
    $latency = 0
    $fallback = $false
    $errorMessage = ""
    $masked = Mask-Key $item.ApiKey

    try {
        if ($existingProviders.ContainsKey($item.Name)) {
            $providerId = $existingProviders[$item.Name].id
            Invoke-MyRouter -Method Patch -Path "/admin/providers/$providerId" -Csrf $csrf -Body @{
                name     = $item.Name
                base_url = $item.BaseUrl
                api_key  = $item.ApiKey
            } | Out-Null
        }
        else {
            $response = Invoke-MyRouter -Method Post -Path "/admin/providers" -Csrf $csrf -Body @{
                name     = $item.Name
                type     = $item.Type
                base_url = $item.BaseUrl
                api_key  = $item.ApiKey
            }
            $providerId = $response.data.id
            $created = $true
        }

        $refresh = Invoke-MyRouter -Method Post -Path "/admin/providers/$providerId/refresh-models" -Csrf $csrf
        $models = @($refresh.data)
        if ($models.Count -eq 0) {
            throw "No models discovered"
        }
        if ($item.Model) {
            $chosen = $models | Where-Object { $_.model_id -eq $item.Model -or $_.display_name -eq $item.Model } | Select-Object -First 1
        }
        else {
            $chosen = $models | Select-Object -First 1
        }
        if (-not $chosen) {
            throw "Model '$($item.Model)' not found"
        }

        if (-not $SkipTest) {
            if (-not $tempRouteId) {
                $createdRoute = Invoke-MyRouter -Method Post -Path "/admin/routes" -Csrf $csrf -Body @{
                    alias              = $tempAlias
                    primary_model_id   = $chosen.id
                    fallback_model_ids = @()
                    strategy           = "priority"
                    retry              = 0
                    timeout_ms         = 15000
                    fallback_on        = @("rate_limit", "timeout", "upstream_5xx", "network", "billing")
                }
                $tempRouteId = $createdRoute.data.id
            }
            else {
                Invoke-MyRouter -Method Patch -Path "/admin/routes/$tempRouteId" -Csrf $csrf -Body @{
                    primary_model_id   = $chosen.id
                    fallback_model_ids = @()
                } | Out-Null
            }
            $test = Invoke-MyRouter -Method Post -Path "/admin/playground/chat" -Csrf $csrf -Body @{
                route      = $tempAlias
                messages   = @(@{ role = "user"; content = "Reply with exactly: OK" })
                max_tokens = 64
            }
            $data = $test.data
            $status = [string]$data.status
            $category = [string]$data.category
            $latency = [int]$data.latency_ms
            $fallback = [bool]$data.fallback
            if ($status -ne "ok") {
                $errorMessage = [string]$data.error
            }
        }
        else {
            $status = "untested"
        }

        if ($status -eq "ok" -or $status -eq "untested") {
            $logical = if ($item.Model) { $item.Model } else { [string]$chosen.model_id }
            if (-not $healthyByModel.ContainsKey($logical)) {
                $healthyByModel[$logical] = @()
            }
            $healthyByModel[$logical] += [string]$chosen.id
        }
    }
    catch {
        $errorMessage = $_.Exception.Message
        if ($created -and $providerId -and $RemoveFailed) {
            try {
                Invoke-MyRouter -Method Delete -Path "/admin/providers/$providerId" -Csrf $csrf | Out-Null
            }
            catch {}
        }
    }

    $results += [pscustomobject]@{
        Name       = $item.Name
        BaseUrl    = $item.BaseUrl
        Model      = $item.Model
        ProviderId = $providerId
        Status     = $status
        Category   = $category
        LatencyMs  = $latency
        Fallback   = $fallback
        KeyHint    = $masked
        Error      = $errorMessage
    }
    Write-Host ("{0}: {1} {2} ({3} ms)" -f $item.Name, $status, $category, $latency)
    Start-Sleep -Milliseconds 150
}

if ($tempRouteId) {
    Invoke-MyRouter -Method Delete -Path "/admin/routes/$tempRouteId" -Csrf $csrf | Out-Null
}

$routes = (Invoke-MyRouter -Method Get -Path "/admin/routes").data
foreach ($logical in $healthyByModel.Keys) {
    $ids = @($healthyByModel[$logical] | Select-Object -Unique)
    if ($ids.Count -eq 0) {
        continue
    }
    $primary = $ids[0]
    $fallbacks = @()
    if ($ids.Count -gt 1) {
        $fallbacks = @($ids[1..($ids.Count - 1)])
    }
    $route = $routes | Where-Object { $_.alias -eq $logical } | Select-Object -First 1
    if ($route) {
        $existing = @($route.targets | Sort-Object position | ForEach-Object { [string]$_.model_id })
        $all = @($existing + $ids | Select-Object -Unique)
        $primary = $all[0]
        $fallbacks = @()
        if ($all.Count -gt 1) {
            $fallbacks = @($all[1..($all.Count - 1)])
        }
        Invoke-MyRouter -Method Patch -Path "/admin/routes/$($route.id)" -Csrf $csrf -Body @{
            primary_model_id   = $primary
            fallback_model_ids = $fallbacks
            strategy           = $Strategy
            retry              = $Retry
            timeout_ms         = $TimeoutMs
        } | Out-Null
        Write-Host "Route '$logical': $($all.Count) target(s), strategy=$Strategy"
    }
    else {
        Invoke-MyRouter -Method Post -Path "/admin/routes" -Csrf $csrf -Body @{
            alias              = $logical
            primary_model_id   = $primary
            fallback_model_ids = $fallbacks
            strategy           = $Strategy
            retry              = $Retry
            timeout_ms         = $TimeoutMs
            fallback_on        = @("rate_limit", "timeout", "upstream_5xx", "network", "billing")
        } | Out-Null
        Write-Host "Route '$logical': created with $($ids.Count) target(s), strategy=$Strategy"
    }
}

$results | Export-Csv -LiteralPath $ReportPath -NoTypeInformation -Encoding UTF8
$healthy = @($results | Where-Object { $_.Status -eq "ok" -or $_.Status -eq "untested" }).Count
$failed = $results.Count - $healthy
Write-Host "Done. Healthy=$healthy Failed=$failed Report=$ReportPath"
$results | Group-Object Category | Where-Object { $_.Name } | ForEach-Object {
    Write-Host ("  {0}: {1}" -f $_.Name, $_.Count)
}
