[CmdletBinding()]
param(
    [switch]$DownloadGoDependencies,
    [switch]$SkipGoDownload,
    [switch]$SkipInstall,
    [switch]$SkipBuild,
    [string]$SqlitePath
)

$ErrorActionPreference = 'Stop'

trap {
    Write-Host ''
    Write-Host 'Startup failed. The window is being kept open so you can read the error.' -ForegroundColor Red
    Write-Host $_.Exception.Message -ForegroundColor Red
    Read-Host 'Press Enter to close this window'
    exit 1
}

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$webRoot = Join-Path $root 'web'
$agencyWebRoot = Join-Path $root 'agency-web'
if ([string]::IsNullOrWhiteSpace($SqlitePath)) {
    $SqlitePath = Join-Path $root 'one-api.db'
}
else {
    $SqlitePath = [System.IO.Path]::GetFullPath($SqlitePath)
}
$sqliteParent = Split-Path -Parent $SqlitePath
if (-not [string]::IsNullOrWhiteSpace($sqliteParent)) {
    New-Item -ItemType Directory -Force -Path $sqliteParent | Out-Null
}
$distIndex = Join-Path $webRoot 'dist\index.html'
$frontendDeps = Join-Path $webRoot 'node_modules\@rsbuild\core'
$agencyFrontendDeps = Join-Path $agencyWebRoot 'node_modules\vite'
$localDevRoot = Join-Path $root '.local-dev'
$agencySSOPrivateKey = Join-Path $localDevRoot 'agency-sso-private.pem'
$agencySSOPublicKey = Join-Path $localDevRoot 'agency-sso-public.pem'
$agencyCommandPrivateKey = Join-Path $localDevRoot 'agency-command-private.pem'
$agencyCommandPublicKey = Join-Path $localDevRoot 'agency-command-public.pem'
$agencyDeliveryKey = Join-Path $localDevRoot 'agency-delivery.key'
$agencyPayoutKey = Join-Path $localDevRoot 'agency-payout.key'
$goCacheRoot = Join-Path $localDevRoot 'go-cache'
$goTempRoot = Join-Path $localDevRoot 'go-tmp'
New-Item -ItemType Directory -Force -Path $localDevRoot, $goCacheRoot, $goTempRoot | Out-Null
$env:GOCACHE = $goCacheRoot
$env:GOTMPDIR = $goTempRoot

function Use-NvsNode20 {
    $nvsHome = $env:NVS_HOME
    if ([string]::IsNullOrWhiteSpace($nvsHome)) {
        $nvsHome = Join-Path $env:LOCALAPPDATA 'nvs'
    }

    $nodeRoot = Join-Path $nvsHome 'node'
    if (-not (Test-Path -LiteralPath $nodeRoot -PathType Container)) {
        throw "NVS Node directory not found: $nodeRoot"
    }

    $node20 = Get-ChildItem -LiteralPath $nodeRoot -Directory -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -match '^20\.' } |
        Sort-Object { try { [version]$_.Name } catch { [version]'0.0.0' } } -Descending |
        Select-Object -First 1

    if ($null -eq $node20) {
        throw "No Node.js 20 installation was found in NVS. Run: nvs add 20"
    }

    $nodeBin = Join-Path $node20.FullName 'x64'
    $nodeExe = Join-Path $nodeBin 'node.exe'
    if (-not (Test-Path -LiteralPath $nodeExe -PathType Leaf)) {
        throw "NVS Node 20 executable not found: $nodeExe"
    }

    $env:Path = "$nodeBin;$env:Path"
    Write-Host "Using NVS Node.js $($node20.Name): $nodeBin" -ForegroundColor Cyan
}

function Require-Command {
    param([Parameter(Mandatory = $true)][string]$Name)

    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Command not found: $Name. Install it and add it to PATH, then reopen PowerShell."
    }
}

function Test-ListenPort {
    param([Parameter(Mandatory = $true)][int]$Port)

    try {
        return $null -ne (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction Stop | Select-Object -First 1)
    }
    catch {
        return $false
    }
}

function Wait-HttpReady {
    param(
        [Parameter(Mandatory = $true)][string]$Url,
        [Parameter(Mandatory = $true)][string]$ServiceName,
        [int]$TimeoutSeconds = 180
    )

    Add-Type -AssemblyName System.Net.Http
    $handler = [System.Net.Http.HttpClientHandler]::new()
    $handler.UseProxy = $false
    $client = [System.Net.Http.HttpClient]::new($handler)
    $client.Timeout = [TimeSpan]::FromSeconds(2)
    $client.DefaultRequestHeaders.Accept.ParseAdd('text/html, application/json')
    try {
        $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
        do {
            try {
                $response = $client.GetAsync($Url).GetAwaiter().GetResult()
                if ([int]$response.StatusCode -ge 200 -and [int]$response.StatusCode -lt 300) {
                    Write-Host "$ServiceName is ready: $Url" -ForegroundColor Green
                    return
                }
            }
            catch {
                # The service can refuse connections while Go is compiling or migrations run.
            }
            Start-Sleep -Seconds 1
        } while ([DateTime]::UtcNow -lt $deadline)
    }
    finally {
        $client.Dispose()
        $handler.Dispose()
    }

    throw "$ServiceName did not become ready within $TimeoutSeconds seconds. Check its PowerShell log window."
}

Require-Command 'go'
Require-Command 'bun'
Use-NvsNode20
Require-Command 'node'

if (-not (Test-Path -LiteralPath $webRoot -PathType Container)) {
    throw "Frontend directory not found: $webRoot"
}
if (-not (Test-Path -LiteralPath $agencyWebRoot -PathType Container)) {
    throw "Agency frontend directory not found: $agencyWebRoot"
}

$nodeVersionText = (& node --version 2>$null | Select-Object -First 1).Trim()
if ($nodeVersionText -notmatch '^v(\d+)\.(\d+)\.(\d+)') {
    throw "Unable to read Node.js version: $nodeVersionText"
}

$nodeMajor = [int]$Matches[1]
$nodeMinor = [int]$Matches[2]
$nodeSupported = ($nodeMajor -eq 20 -and $nodeMinor -ge 19) -or
    ($nodeMajor -eq 22 -and $nodeMinor -ge 12) -or
    ($nodeMajor -ge 23)

if (-not $nodeSupported) {
    throw "Unsupported Node.js version $nodeVersionText. Rspack requires Node.js 20.19+, 22.12+, or a newer supported release."
}

if ($DownloadGoDependencies -and $SkipGoDownload) {
    throw 'Use either -DownloadGoDependencies or -SkipGoDownload, not both.'
}

if ($DownloadGoDependencies) {
    Write-Host 'Preparing Go dependencies on request (cached modules are reused)...' -ForegroundColor Yellow
    Push-Location $root
    try {
        & go mod download
        if ($LASTEXITCODE -ne 0) {
            throw "Go dependency download failed (exit code $LASTEXITCODE). Check network/GOPROXY and try again."
        }

        & go mod verify
        if ($LASTEXITCODE -ne 0) {
            throw "Go module verification failed (exit code $LASTEXITCODE)."
        }
    }
    finally {
        Pop-Location
    }
}
else {
    Write-Host 'Using cached Go dependencies. go run downloads missing modules if needed.' -ForegroundColor Cyan
    Write-Host 'To pre-download and verify modules: .\start-dev.ps1 -DownloadGoDependencies'
}

if (-not $SkipInstall -and -not (Test-Path -LiteralPath $frontendDeps -PathType Container)) {
    Write-Host 'Frontend dependencies not found; running bun install --frozen-lockfile...' -ForegroundColor Yellow
    Push-Location $webRoot
    try {
        & bun install --frozen-lockfile
        if ($LASTEXITCODE -ne 0) {
            throw "Frontend dependency installation failed (exit code $LASTEXITCODE)."
        }
    }
    finally {
        Pop-Location
    }
}

if (-not $SkipInstall -and -not (Test-Path -LiteralPath $agencyFrontendDeps -PathType Container)) {
    Write-Host 'Agency frontend dependencies not found; running bun install --frozen-lockfile...' -ForegroundColor Yellow
    Push-Location $agencyWebRoot
    try {
        & bun install --frozen-lockfile
        if ($LASTEXITCODE -ne 0) {
            throw "Agency frontend dependency installation failed (exit code $LASTEXITCODE)."
        }
    }
    finally {
        Pop-Location
    }
}

if (-not $SkipBuild -and -not (Test-Path -LiteralPath $distIndex -PathType Leaf)) {
    Write-Host 'web/dist not found; building the frontend for Go embed...' -ForegroundColor Yellow
    Push-Location $webRoot
    try {
        & bun run build
        if ($LASTEXITCODE -ne 0) {
            throw "Frontend build failed (exit code $LASTEXITCODE)."
        }
    }
    finally {
        Pop-Location
    }
}

Write-Host 'Preparing the local Agency Hub SSO key pair...' -ForegroundColor Cyan
Push-Location $root
try {
    & go run ./cmd/agency-keygen --private $agencySSOPrivateKey --public $agencySSOPublicKey --secret $agencyDeliveryKey
    if ($LASTEXITCODE -ne 0) {
        throw "Agency Hub local SSO key preparation failed (exit code $LASTEXITCODE)."
    }
    & go run ./cmd/agency-keygen --private $agencyCommandPrivateKey --public $agencyCommandPublicKey --secret $agencyPayoutKey
    if ($LASTEXITCODE -ne 0) {
        throw "Agency Hub local command key preparation failed (exit code $LASTEXITCODE)."
    }
}
finally {
    Pop-Location
}

$rootLiteral = $root.Replace("'", "''")
$webLiteral = $webRoot.Replace("'", "''")
$agencyWebLiteral = $agencyWebRoot.Replace("'", "''")
$sqliteLiteral = $SqlitePath.Replace("'", "''")
$agencySSOPrivateLiteral = $agencySSOPrivateKey.Replace("'", "''")
$agencySSOPublicLiteral = $agencySSOPublicKey.Replace("'", "''")
$agencyCommandPrivateLiteral = $agencyCommandPrivateKey.Replace("'", "''")
$agencyCommandPublicLiteral = $agencyCommandPublicKey.Replace("'", "''")
$agencyDeliveryLiteral = $agencyDeliveryKey.Replace("'", "''")
$agencyPayoutLiteral = $agencyPayoutKey.Replace("'", "''")
$nodeBin = Split-Path -Parent (Get-Command node).Source
$nodeBinLiteral = $nodeBin.Replace("'", "''")

if (Test-ListenPort -Port 3000) {
    Write-Warning 'Port 3000 is already in use; backend startup was skipped.'
}
else {
    $backendCommand = "Set-Location -LiteralPath '$rootLiteral'; `$Host.UI.RawUI.WindowTitle = 'New API Backend :3000'; Remove-Item Env:SQL_DSN -ErrorAction SilentlyContinue; Remove-Item Env:LOG_SQL_DSN -ErrorAction SilentlyContinue; `$env:SQLITE_PATH = '$sqliteLiteral'; `$env:PORT = '3000'; `$env:AGENCY_ONBOARDING_ENABLED = 'true'; `$env:AGENCY_SSO_PRIVATE_KEY_FILE = '$agencySSOPrivateLiteral'; `$env:AGENCY_SSO_KEY_ID = 'agency-local-v1'; `$env:AGENCY_SSO_ALLOWED_ORIGIN = 'http://127.0.0.1:3001,http://localhost:3001,http://127.0.0.1:3202,http://localhost:3202'; `$env:AGENCY_HUB_COMMAND_SERVICE_PUBLIC_KEY_FILE = '$agencyCommandPublicLiteral'; go run ."
    Start-Process -FilePath 'powershell.exe' `
        -ArgumentList @('-NoLogo', '-NoProfile', '-NoExit', '-ExecutionPolicy', 'Bypass', '-Command', $backendCommand) `
        -WorkingDirectory $root -WindowStyle Hidden | Out-Null
}
Wait-HttpReady -Url 'http://localhost:3000/api/status' -ServiceName 'New API backend'

if (Test-ListenPort -Port 3201) {
    Write-Warning 'Port 3201 is already in use; Agency Hub backend startup was skipped.'
}
else {
    $agencyBackendCommand = "Set-Location -LiteralPath '$rootLiteral'; `$Host.UI.RawUI.WindowTitle = 'Agency Hub Backend :3201'; Remove-Item Env:SQL_DSN -ErrorAction SilentlyContinue; Remove-Item Env:LOG_SQL_DSN -ErrorAction SilentlyContinue; `$env:SQLITE_PATH = '$sqliteLiteral'; `$env:AGENCY_HUB_PORT = '3201'; `$env:AGENCY_HUB_BASE_PATH = '/agency'; `$env:AGENCY_HUB_PUBLIC_BASE_URL = 'http://127.0.0.1:3001'; `$env:AGENCY_HUB_PLATFORM_BASE_URL = 'http://127.0.0.1:3001'; `$env:AGENCY_HUB_BROWSER_ORIGIN = 'http://127.0.0.1:3202'; `$env:AGENCY_HUB_COOKIE_SECURE = 'false'; `$env:AGENCY_HUB_AUTO_MIGRATE = 'true'; `$env:AGENCY_HUB_FACT_PROJECTION_ENABLED = 'true'; `$env:AGENCY_ONBOARDING_ENABLED = 'true'; `$env:AGENCY_HUB_COMMISSION_PROCESSING_ENABLED = 'true'; `$env:AGENCY_HUB_WITHDRAWALS_ENABLED = 'true'; `$env:AGENCY_HUB_COMMAND_ALLOW_LOCAL_SQLITE = 'true'; `$env:AGENCY_HUB_SSO_PUBLIC_KEY_FILE = '$agencySSOPublicLiteral'; `$env:AGENCY_HUB_COMMAND_SERVICE_PUBLIC_KEY_FILE = '$agencyCommandPublicLiteral'; `$env:AGENCY_HUB_COMMAND_SERVICE_PRIVATE_KEY_FILE = '$agencyCommandPrivateLiteral'; `$env:AGENCY_HUB_DELIVERY_KEY_FILE = '$agencyDeliveryLiteral'; `$env:AGENCY_PAYOUT_KEY_FILE = '$agencyPayoutLiteral'; go run ./cmd/agency-hub"
    Start-Process -FilePath 'powershell.exe' `
        -ArgumentList @('-NoLogo', '-NoProfile', '-NoExit', '-ExecutionPolicy', 'Bypass', '-Command', $agencyBackendCommand) `
        -WorkingDirectory $root -WindowStyle Hidden | Out-Null
}
Wait-HttpReady -Url 'http://localhost:3201/agency/readyz' -ServiceName 'Agency Hub backend'

if (Test-ListenPort -Port 3202) {
    Write-Warning 'Port 3202 is already in use; Agency Hub frontend startup was skipped.'
}
else {
    $agencyFrontendCommand = "Set-Location -LiteralPath '$agencyWebLiteral'; `$env:Path = '$nodeBinLiteral;' + `$env:Path; `$Host.UI.RawUI.WindowTitle = 'Agency Hub Frontend :3202'; `$env:VITE_AGENCY_PLATFORM_BASE_URL = 'http://127.0.0.1:3001'; bun run dev -- --host 0.0.0.0 --port 3202"
    Start-Process -FilePath 'powershell.exe' `
        -ArgumentList @('-NoLogo', '-NoProfile', '-NoExit', '-ExecutionPolicy', 'Bypass', '-Command', $agencyFrontendCommand) `
        -WorkingDirectory $agencyWebRoot -WindowStyle Hidden | Out-Null
}
Wait-HttpReady -Url 'http://127.0.0.1:3202/agency/' -ServiceName 'Agency Hub frontend'

if (Test-ListenPort -Port 3001) {
    Write-Warning 'Port 3001 is already in use; frontend startup was skipped.'
}
else {
    $frontendCommand = "Set-Location -LiteralPath '$webLiteral'; `$env:Path = '$nodeBinLiteral;' + `$env:Path; `$Host.UI.RawUI.WindowTitle = 'New API Frontend :3001'; bun run dev -- --host 0.0.0.0 --port 3001"
    Start-Process -FilePath 'powershell.exe' `
        -ArgumentList @('-NoLogo', '-NoProfile', '-NoExit', '-ExecutionPolicy', 'Bypass', '-Command', $frontendCommand) `
        -WorkingDirectory $webRoot -WindowStyle Hidden | Out-Null
}
Wait-HttpReady -Url 'http://127.0.0.1:3001' -ServiceName 'New API frontend'

Write-Host ''
Write-Host 'Startup commands sent.' -ForegroundColor Green
Write-Host 'Backend:  http://localhost:3000'
Write-Host 'Frontend: http://localhost:3001'
Write-Host 'Agency Hub backend:  http://localhost:3201/agency/readyz'
Write-Host 'Agency Hub frontend: http://localhost:3202/agency/'
Write-Host "SQLite database: $SqlitePath"
Write-Host 'The four services run in hidden PowerShell processes; stop those processes to stop the services.'
