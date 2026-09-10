[CmdletBinding()]
param(
    [switch]$DownloadGoDependencies,
    [switch]$SkipGoDownload,
    [switch]$SkipInstall,
    [switch]$SkipBuild
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
$distIndex = Join-Path $webRoot 'dist\index.html'
$frontendDeps = Join-Path $webRoot 'node_modules\@rsbuild\core'

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

Require-Command 'go'
Require-Command 'bun'
Use-NvsNode20
Require-Command 'node'

if (-not (Test-Path -LiteralPath $webRoot -PathType Container)) {
    throw "Frontend directory not found: $webRoot"
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

$rootLiteral = $root.Replace("'", "''")
$webLiteral = $webRoot.Replace("'", "''")

if (Test-ListenPort -Port 3000) {
    Write-Warning 'Port 3000 is already in use; backend startup was skipped.'
}
else {
    $backendCommand = "Set-Location -LiteralPath '$rootLiteral'; `$Host.UI.RawUI.WindowTitle = 'New API Backend :3000'; `$env:PORT = '3000'; go run ."
    Start-Process -FilePath 'powershell.exe' `
        -ArgumentList @('-NoLogo', '-NoProfile', '-NoExit', '-ExecutionPolicy', 'Bypass', '-Command', $backendCommand) `
        -WorkingDirectory $root | Out-Null
}

if (Test-ListenPort -Port 3001) {
    Write-Warning 'Port 3001 is already in use; frontend startup was skipped.'
}
else {
    $frontendCommand = "Set-Location -LiteralPath '$webLiteral'; `$Host.UI.RawUI.WindowTitle = 'New API Frontend :3001'; bun run dev -- --host 0.0.0.0 --port 3001"
    Start-Process -FilePath 'powershell.exe' `
        -ArgumentList @('-NoLogo', '-NoProfile', '-NoExit', '-ExecutionPolicy', 'Bypass', '-Command', $frontendCommand) `
        -WorkingDirectory $webRoot | Out-Null
}

Write-Host ''
Write-Host 'Startup commands sent.' -ForegroundColor Green
Write-Host 'Backend:  http://localhost:3000'
Write-Host 'Frontend: http://localhost:3001'
Write-Host 'Two PowerShell windows show the service logs; close a window to stop that service.'
