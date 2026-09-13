param(
    [string]$TestRoot = 'E:\new-api-test-cache',
    [int]$Port = 4328
)
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$agencyWeb = Join-Path $repo 'agency-web'
$runID = [guid]::NewGuid().ToString('N')
$runDir = Join-Path $TestRoot ('agency-browser\' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + $runID.Substring(0, 8))
New-Item -ItemType Directory -Path $runDir -Force | Out-Null
$env:GOCACHE = Join-Path $TestRoot 'go-cache'
$env:GOMODCACHE = Join-Path $TestRoot 'go-mod'
$env:GOTMPDIR = Join-Path $TestRoot 'go-tmp'
$env:TEMP = Join-Path $TestRoot 'runtime-tmp'
$env:TMP = $env:TEMP
$env:BUN_INSTALL_CACHE_DIR = Join-Path $TestRoot 'bun-cache'
$env:PLAYWRIGHT_BROWSERS_PATH = Join-Path $TestRoot 'playwright-browsers'
$env:AGENCY_BROWSER_FIXTURE = '1'
$env:AGENCY_BROWSER_DIST = Join-Path $runDir 'webdist'
$env:AGENCY_BROWSER_TEST_ROOT = $runDir
$env:AGENCY_BROWSER_ADDR = '127.0.0.1:' + $Port
$env:AGENCY_BROWSER_URL = 'http://' + $env:AGENCY_BROWSER_ADDR
$env:AGENCY_BROWSER_RUN_ID = $runID
foreach ($directory in @($env:GOCACHE, $env:GOMODCACHE, $env:GOTMPDIR, $env:TEMP, $env:BUN_INSTALL_CACHE_DIR)) {
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
}
$fixture = $null
$ready = $false
Push-Location $agencyWeb
try {
    & bun x vite build --outDir $env:AGENCY_BROWSER_DIST
    if ($LASTEXITCODE -ne 0) { throw 'Browser acceptance frontend build failed.' }
    $fixture = Start-Process -FilePath 'go' -ArgumentList @('test', '-tags', 'agency_browser', './pkg/agencyhub', '-run', '^TestAgencyBrowserFixture$', '-count=1', '-timeout', '0', '-v') -WorkingDirectory $repo -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $runDir 'fixture.stdout.log') -RedirectStandardError (Join-Path $runDir 'fixture.stderr.log')
    $deadline = (Get-Date).AddSeconds(90)
    while ((Get-Date) -lt $deadline) {
        if ($fixture.HasExited) { throw ('Fixture exited. Inspect ' + $runDir) }
        try {
            $state = Invoke-RestMethod -Uri ($env:AGENCY_BROWSER_URL + '/__fixture/state') -TimeoutSec 2
            if ($state.run_id -eq $runID) { $ready = $true; break }
        } catch { }
        Start-Sleep -Milliseconds 300
    }
    if (!$ready) { throw ('Fixture did not become ready. Inspect ' + $runDir) }
    & bun x --bun playwright test --config e2e/playwright.config.ts
    if ($LASTEXITCODE -ne 0) { throw ('Browser acceptance failed. Inspect ' + $runDir) }
    Write-Host ('Browser acceptance passed. Evidence: ' + $runDir)
} finally {
    if ($ready) {
        try { Invoke-WebRequest -UseBasicParsing -Method Post -Uri ($env:AGENCY_BROWSER_URL + '/__fixture/shutdown') -TimeoutSec 5 | Out-Null } catch { }
    }
    if ($fixture -and !$fixture.HasExited) {
        if (!$fixture.WaitForExit(10000)) { Stop-Process -Id $fixture.Id }
    }
    Pop-Location
}
