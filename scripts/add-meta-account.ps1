[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Name,
    [string]$ApiKey = "",
    [string]$BaseUrl = "https://api.meta.ai/v1",
    [string]$MyRouterUrl = "http://127.0.0.1:20128",
    [string]$AdminUser = "admin",
    [string]$AdminPassword = $env:MYROUTER_ADMIN_PASSWORD,
    [ValidateSet("", "priority", "round_robin")][string]$Strategy = "",
    [switch]$Test
)

$ErrorActionPreference = "Stop"

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

if (-not $ApiKey) {
    $secure = Read-Host "Meta API key" -AsSecureString
    $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try {
        $ApiKey = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)
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
    throw "Login failed: no CSRF token returned"
}

$providers = (Invoke-MyRouter -Method Get -Path "/admin/providers").data
$provider = $providers | Where-Object { $_.name -eq $Name } | Select-Object -First 1
if ($provider) {
    $providerId = $provider.id
    Invoke-MyRouter -Method Patch -Path "/admin/providers/$providerId" -Csrf $csrf -Body @{
        name     = $Name
        base_url = $BaseUrl
        api_key  = $ApiKey
    } | Out-Null
    Write-Host "Updated provider '$Name' ($providerId)"
}
else {
    $created = Invoke-MyRouter -Method Post -Path "/admin/providers" -Csrf $csrf -Body @{
        name     = $Name
        type     = "openai"
        base_url = $BaseUrl
        api_key  = $ApiKey
    }
    $providerId = $created.data.id
    Write-Host "Created provider '$Name' ($providerId)"
}

$refresh = Invoke-MyRouter -Method Post -Path "/admin/providers/$providerId/refresh-models" -Csrf $csrf
$models = @($refresh.data)
if ($models.Count -eq 0) {
    throw "No models were discovered for provider '$Name'"
}
$modelByAlias = @{}
foreach ($model in $models) {
    $modelByAlias[$model.model_id] = $model.id
}
Write-Host "Discovered $($models.Count) model(s) for '$Name'"

$routes = (Invoke-MyRouter -Method Get -Path "/admin/routes").data
$updated = 0
foreach ($route in $routes) {
    $lookupName = $route.alias
    if (@($route.targets).Count -gt 0) {
        $lookupName = $route.targets[0].model_name
    }
    $newModelId = $modelByAlias[$lookupName]
    if (-not $newModelId) {
        continue
    }
    $existing = @($route.targets | Sort-Object position | ForEach-Object { $_.model_id })
    if ($existing -contains $newModelId) {
        Write-Host "Route '$($route.alias)': account already present"
        continue
    }
    $all = @($existing + $newModelId)
    $primary = $all[0]
    $fallbacks = @()
    if ($all.Count -gt 1) {
        $fallbacks = @($all[1..($all.Count - 1)])
    }
    $body = @{
        primary_model_id   = $primary
        fallback_model_ids = $fallbacks
    }
    if ($Strategy) {
        $body.strategy = $Strategy
    }
    Invoke-MyRouter -Method Patch -Path "/admin/routes/$($route.id)" -Csrf $csrf -Body $body | Out-Null
    Write-Host "Route '$($route.alias)': appended '$($route.alias)' from '$Name'"
    $updated++
}

if ($updated -eq 0) {
    Write-Host "No route aliases matched this provider's model IDs"
}

if ($Test) {
    foreach ($route in $routes) {
        $lookupName = $route.alias
        if (@($route.targets).Count -gt 0) {
            $lookupName = $route.targets[0].model_name
        }
        if (-not $modelByAlias[$lookupName]) {
            continue
        }
        $result = Invoke-MyRouter -Method Post -Path "/admin/playground/chat" -Csrf $csrf -Body @{
            route      = $route.alias
            messages   = @(@{ role = "user"; content = "Reply with exactly: OK" })
            max_tokens = 32
        }
        $data = $result.data
        Write-Host ("Test '{0}': status={1} provider={2} fallback={3}" -f $route.alias, $data.status, $data.provider, $data.fallback)
        foreach ($step in @($data.trace)) {
            Write-Host ("  attempt {0}: {1}/{2} {3} {4}" -f $step.attempt, $step.provider, $step.target, $step.outcome, $step.category)
        }
    }
}
