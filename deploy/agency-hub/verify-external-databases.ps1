[CmdletBinding()]
param(
    [string]$TestRoot = 'E:\new-api-test-cache',
    [string]$MySqlBin = 'D:\FileRuntimeEnvironment\MySQL5.7.43\mysql-5.7.43-winx64\bin',
    [string]$PostgresBin = 'E:\new-api-test-cache\database-tools\pgsql\bin',
    [int]$MySqlPort = 13316,
    [int]$PostgresPort = 15436
)
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$TestRoot = [IO.Path]::GetFullPath($TestRoot)
if ([IO.Path]::GetPathRoot($TestRoot) -eq ($env:SystemDrive + '\')) {
    throw 'Use a data drive for the disposable database instances.'
}
$runDir = Join-Path $TestRoot ('external-db\' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
$mysqlData = Join-Path $runDir 'mysql-data'
$pgData = Join-Path $runDir 'postgres-data'
$dbTemp = Join-Path $runDir 'temp'
$mysqlProcess = $null
$postgresProcess = $null
$previousEnvironment = @{}
$settings = @{
    GOCACHE = Join-Path $TestRoot 'go-cache'
    GOMODCACHE = Join-Path $TestRoot 'go-mod'
    GOTMPDIR = Join-Path $TestRoot 'go-tmp'
    TEMP = $dbTemp
    TMP = $dbTemp
    AGENCY_HUB_RUN_EXTERNAL_DB_TESTS = '1'
    AGENCY_HUB_BACKUP_PHASE = ''
    AGENCY_HUB_TEST_MYSQL_DSN = "root@tcp(127.0.0.1:$MySqlPort)/agency_hub_verify?charset=utf8mb4&parseTime=True&loc=Local"
    AGENCY_HUB_TEST_POSTGRES_DSN = "host=127.0.0.1 port=$PostgresPort user=postgres dbname=agency_hub_verify sslmode=disable"
}

function Invoke-DatabaseCheck {
    param([string]$Name, [string]$Command, [string[]]$CommandArgs)
    $log = Join-Path $runDir ($Name + '.log')
    $ErrorActionPreference = 'Continue'
    & $Command @CommandArgs 2>&1 | Tee-Object -FilePath $log | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Get-Content -LiteralPath $log -Tail 35 | Out-Host
        throw "$Name failed; evidence: $log"
    }
}

function Wait-DatabasePort {
    param([int]$Port, [Diagnostics.Process]$Process)
    $deadline = (Get-Date).AddSeconds(60)
    while ((Get-Date) -lt $deadline) {
        if ($Process.HasExited) { throw "Test database exited; inspect $runDir" }
        $client = [Net.Sockets.TcpClient]::new()
        try {
            $client.Connect('127.0.0.1', $Port)
            return
        } catch { Start-Sleep -Milliseconds 200 } finally { $client.Dispose() }
    }
    throw "Test database did not listen on loopback port $Port"
}

foreach ($port in @($MySqlPort, $PostgresPort)) {
    if ($port -lt 1024 -or $port -gt 65535) { throw 'Choose unprivileged test ports.' }
    if (Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue) {
        throw "Port $port is already occupied; existing services are left running."
    }
}
foreach ($binary in @((Join-Path $MySqlBin 'mysqld.exe'), (Join-Path $MySqlBin 'mysql.exe'), (Join-Path $PostgresBin 'initdb.exe'), (Join-Path $PostgresBin 'pg_ctl.exe'), (Join-Path $PostgresBin 'pg_isready.exe'))) {
    if (!(Test-Path -LiteralPath $binary -PathType Leaf)) { throw "Missing database executable: $binary" }
}
New-Item -ItemType Directory -Path $mysqlData, $pgData, $dbTemp -Force | Out-Null
try {
    foreach ($key in $settings.Keys) {
        $previousEnvironment[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
        [Environment]::SetEnvironmentVariable($key, $settings[$key], 'Process')
    }
    Write-Host "Disposable database evidence and data: $runDir"
    $mysqlBase = Split-Path $MySqlBin -Parent
    Invoke-DatabaseCheck 'mysql-initialize' (Join-Path $MySqlBin 'mysqld.exe') @('--no-defaults', '--initialize-insecure', "--basedir=$mysqlBase", "--datadir=$mysqlData", "--tmpdir=$dbTemp", '--innodb-buffer-pool-size=64M')
    $mysqlArgs = @('--no-defaults', "--basedir=`"$mysqlBase`"", "--datadir=`"$mysqlData`"", "--tmpdir=`"$dbTemp`"", "--port=$MySqlPort", '--bind-address=127.0.0.1', '--innodb-buffer-pool-size=128M', '--max-connections=40', "--log-error=`"$(Join-Path $runDir 'mysql-server.log')`"")
    $mysqlProcess = Start-Process (Join-Path $MySqlBin 'mysqld.exe') -ArgumentList $mysqlArgs -WindowStyle Hidden -PassThru
    Wait-DatabasePort $MySqlPort $mysqlProcess
    Invoke-DatabaseCheck 'mysql-create' (Join-Path $MySqlBin 'mysql.exe') @('--no-defaults', '--host=127.0.0.1', "--port=$MySqlPort", '--user=root', '--execute=CREATE DATABASE agency_hub_verify CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci')

    Invoke-DatabaseCheck 'postgres-initialize' (Join-Path $PostgresBin 'initdb.exe') @('-D', $pgData, '-U', 'postgres', '-A', 'trust', '--encoding=UTF8', '--no-locale')
    $pgArgs = @('-D', "`"$pgData`"", '-h', '127.0.0.1', '-p', "$PostgresPort", '-c', 'shared_buffers=64MB', '-c', 'max_connections=40')
    $postgresProcess = Start-Process (Join-Path $PostgresBin 'postgres.exe') -ArgumentList $pgArgs -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $runDir 'postgres.stdout.log') -RedirectStandardError (Join-Path $runDir 'postgres.stderr.log')
    Wait-DatabasePort $PostgresPort $postgresProcess
    # PostgreSQL opens its TCP socket before crash/startup recovery finishes.
    # Wait for its actual admission status before executing database commands.
    $postgresReadyDeadline = (Get-Date).AddSeconds(60)
    while ($true) {
        if ($postgresProcess.HasExited) { throw "Test PostgreSQL exited; inspect $runDir" }
        & (Join-Path $PostgresBin 'pg_isready.exe') -q -h 127.0.0.1 -p $PostgresPort -U postgres
        if ($LASTEXITCODE -eq 0) { break }
        if ((Get-Date) -ge $postgresReadyDeadline) { throw "Test PostgreSQL did not become ready; inspect $runDir" }
        Start-Sleep -Milliseconds 200
    }
    Invoke-DatabaseCheck 'postgres-create' (Join-Path $PostgresBin 'createdb.exe') @('-h', '127.0.0.1', '-p', "$PostgresPort", '-U', 'postgres', 'agency_hub_verify')

    Push-Location $repo
    try {
        Invoke-DatabaseCheck 'cross-database-tests' 'go' @('test', '-json', '-count=1', './model', '-run', '^(TestMigrateAgencyExternalDatabaseCompatibility|TestAgencyFundingConcurrentReserveIsConservedAcrossDialects|TestAgencyTopupConcurrentCreditsAcrossDialects|TestAgencyReconciliationLegacyMigrationPreservesHistoryAcrossDialects|TestAgencyComponentSettlementAcrossDialects|TestAgencyComponentSettlementPreservesExactIDsAcrossDialects|TestAgencyComponentIdentityMigrationAcrossDialects|TestAgencyComponentRefundAcrossDialects|TestAgencyRefundDebtOffsetsAcrossDialects|TestAgencyChargebackDebtIdentityAcrossDialects|TestAgencyFundingReversalImmutableEvidenceAcrossDialects|TestAgencyTaskLifecycleAcrossDialects|TestAgencyTaskReconciliationComponentRefundAcrossDialects|TestTaskBillingJSONPersistenceAcrossDialects)$')
        Invoke-DatabaseCheck 'command-atomic-tests' 'go' @('test', '-json', '-count=1', './service', '-run', '^TestAgencyCommandAtomicExecutionAcrossDialects$')
        Invoke-DatabaseCheck 'export-concurrency-tests' 'go' @('test', '-json', '-count=1', './pkg/agencyhub', '-run', '^(TestExportConcurrentDownloadAdmissionSharesActorBudgetAcrossSessions|TestReconciliationEvidenceAcrossDialects|TestConcurrentReconciliationCreatesOneActiveIssueAcrossDialects|TestConcurrentReconciliationRepairReplaysOneAtomicResultAcrossDialects|TestReconciliationScanRejectsConservingNegativeBucketsAcrossDialects|TestReconciliationScanProductionEvidenceAcrossDialects|TestReconciliationScanFindsCorruptionBeyondFirstPageAcrossDialects|TestComponentBillingProjectsOneReceiptAndRequestAcrossDialects|TestComponentGatewayRefundAndProjectionAcrossDialects|TestReconciliationHistoricalCutoffCannotCertifyCurrentBalancesAcrossDialects)$')
        $env:AGENCY_HUB_BACKUP_PHASE = 'seed'
        Invoke-DatabaseCheck 'postgres-backup-seed' 'go' @('test', '-json', '-count=1', './model', '-run', '^TestAgencyPostgresBackupRestorePaymentReceipt$')
        # Both fixture writers have exited. Dump the entire primary test DB;
        # restore into a different newly created database without --clean.
        $backupFile = Join-Path $runDir 'primary-postgres.dump'
        Invoke-DatabaseCheck 'postgres-backup' (Join-Path $PostgresBin 'pg_dump.exe') @('-h', '127.0.0.1', '-p', "$PostgresPort", '-U', 'postgres', '--format=custom', '--file', $backupFile, 'agency_hub_verify')
        Invoke-DatabaseCheck 'postgres-restore-create' (Join-Path $PostgresBin 'createdb.exe') @('-h', '127.0.0.1', '-p', "$PostgresPort", '-U', 'postgres', 'agency_hub_restore')
        Invoke-DatabaseCheck 'postgres-restore' (Join-Path $PostgresBin 'pg_restore.exe') @('-h', '127.0.0.1', '-p', "$PostgresPort", '-U', 'postgres', '--exit-on-error', '--no-owner', '--no-privileges', '-d', 'agency_hub_restore', $backupFile)
        $env:AGENCY_HUB_TEST_POSTGRES_DSN = "host=127.0.0.1 port=$PostgresPort user=postgres dbname=agency_hub_restore sslmode=disable"
        $env:AGENCY_HUB_BACKUP_PHASE = 'verify'
        Invoke-DatabaseCheck 'postgres-restored-receipts' 'go' @('test', '-json', '-count=1', './model', '-run', '^TestAgencyPostgresBackupRestorePaymentReceipt$')
    } finally { Pop-Location }
    Write-Host "External database migration, concurrency and isolated PostgreSQL backup/restore checks passed: $runDir"
} finally {
    $ErrorActionPreference = 'Continue'
    if ($postgresProcess -and !$postgresProcess.HasExited) {
        & (Join-Path $PostgresBin 'pg_ctl.exe') -D $pgData -m fast -w stop 2>&1 | Out-Null
        if (!$postgresProcess.WaitForExit(10000)) { Stop-Process -Id $postgresProcess.Id }
    }
    if ($mysqlProcess -and !$mysqlProcess.HasExited) {
        & (Join-Path $MySqlBin 'mysqladmin.exe') --no-defaults --host=127.0.0.1 "--port=$MySqlPort" --user=root shutdown 2>&1 | Out-Null
        if (!$mysqlProcess.WaitForExit(10000)) { Stop-Process -Id $mysqlProcess.Id }
    }
    foreach ($key in $previousEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($key, $previousEnvironment[$key], 'Process')
    }
    Write-Host 'Only the two test processes were stopped; test data and logs are retained.'
}
