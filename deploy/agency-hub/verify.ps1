[CmdletBinding()]
param(
    [string]$TestRoot = 'E:\new-api-test-cache',
    [switch]$Full,
    [switch]$SkipFrontend
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$TestRoot = [System.IO.Path]::GetFullPath($TestRoot)
$runDir = Join-Path $TestRoot ('runs/' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
$testEnvironment = @{
    GOCACHE = Join-Path $TestRoot 'go-cache'
    GOMODCACHE = Join-Path $TestRoot 'go-mod'
    GOTMPDIR = Join-Path $TestRoot 'go-tmp'
    TEMP = Join-Path $TestRoot 'runtime-tmp'
    TMP = Join-Path $TestRoot 'runtime-tmp'
    BUN_INSTALL_CACHE_DIR = Join-Path $TestRoot 'bun-cache'
}
$previousEnvironment = @{}
$checks = [System.Collections.Generic.List[object]]::new()

function Invoke-Verification {
    param([string]$Name, [string]$Command, [string[]]$CommandArgs)
    $logPath = Join-Path $runDir ($Name + '.log')
    Write-Host "Running $Name; log: $logPath"
    # Windows PowerShell wraps native stderr in ErrorRecord objects. Keep the
    # native exit code authoritative, including when a tool prints warnings.
    $ErrorActionPreference = 'Continue'
    & $Command @CommandArgs 2>&1 | Tee-Object -FilePath $logPath | Out-Null
    $nativeExitCode = $LASTEXITCODE
    $checks.Add([pscustomobject]@{ name = $Name; exit_code = $nativeExitCode; log = $logPath })
    Write-Host "$Name finished with exit code $nativeExitCode"
    if ($nativeExitCode -ne 0) { Get-Content -LiteralPath $logPath -Tail 40 | Out-Host }
}

try {
    Get-Command go -ErrorAction Stop | Out-Null
    if (-not $SkipFrontend) { Get-Command bun -ErrorAction Stop | Out-Null }
    New-Item -ItemType Directory -Force -Path $runDir | Out-Null
    foreach ($name in $testEnvironment.Keys) {
        $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        New-Item -ItemType Directory -Force -Path $testEnvironment[$name] | Out-Null
        [Environment]::SetEnvironmentVariable($name, $testEnvironment[$name], 'Process')
    }
    Push-Location $repoRoot
    try {
        [ordered]@{
            started_at = (Get-Date).ToString('o')
            test_root = $TestRoot
            runtime_temp = [System.IO.Path]::GetTempPath()
            environment = $testEnvironment
            full = [bool]$Full
            frontend = -not [bool]$SkipFrontend
        } | ConvertTo-Json -Depth 3 | Set-Content -Encoding UTF8 (Join-Path $runDir 'environment.json')
        Write-Host "Test runtime temporary directory: $([System.IO.Path]::GetTempPath())"
        $testPackages = @('./pkg/agencyhub', './model', './service', './controller', './relay')
        if ($Full) { $testPackages = @('./...') }
        Invoke-Verification 'go-tests' 'go' (@('test', '-json', '-count=1') + $testPackages)
        Invoke-Verification 'agency-contract-audit' 'go' @('test', '-json', '-tags', 'agency_audit', './service', './pkg/agencyhub', '-run', '^(TestAgencyDesign|TestAgencyAudit)', '-count=1')
        Invoke-Verification 'go-build' 'go' @('build', './...')
        Invoke-Verification 'go-vet' 'go' @('vet', './pkg/agencyhub', './cmd/agency-hub')
        if (-not $SkipFrontend) {
            Push-Location (Join-Path $repoRoot 'agency-web')
            try {
                Invoke-Verification 'frontend-types' 'bun' @('--bun', 'run', 'tsc', '--noEmit', '--project', 'tsconfig.json')
                Invoke-Verification 'frontend-contracts' 'bun' @('test', 'src')
                Invoke-Verification 'frontend-build' 'bun' @('--bun', 'run', 'vite', 'build', '--outDir', (Join-Path $runDir 'agency-web'))
            } finally { Pop-Location }
        }
        $checks | ConvertTo-Json -Depth 3 | Set-Content -Encoding UTF8 (Join-Path $runDir 'checks.json')
        Write-Host "Verification evidence: $runDir"
        Write-Host 'Skipped database/live/load tests are not acceptance passes. Inspect the JSON test logs.'
        $failed = @($checks | Where-Object { $_.exit_code -ne 0 })
        if ($failed.Count -gt 0) {
            throw ('Verification failed: ' + (($failed | ForEach-Object { $_.name }) -join ', '))
        }
    } finally { Pop-Location }
} finally {
    foreach ($name in $previousEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], 'Process')
    }
}
