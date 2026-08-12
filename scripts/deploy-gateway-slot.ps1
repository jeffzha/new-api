<#
.SYNOPSIS
Deploy the current checkout to the inactive Gateway Blue or Green slot.

.DESCRIPTION
The script reads the Caddy-managed blue/green weights, refuses to replace a
slot that receives traffic, builds an immutable image on the Gateway server
(or verifies an explicitly prebuilt image), and starts only the inactive
new-api service. It does not change traffic and does not recreate Caddy,
PostgreSQL, Redis, API docs, or hwdrama-proxy.

Versions follow:
<nearest-git-tag>.gateway.<UTC yyyyMMddTHHmmssZ>.g<12-char-commit>
#>

[CmdletBinding()]
param(
    [ValidateSet("Auto", "Blue", "Green")][string]$Slot = "Auto",
    [string]$RemoteHost = "root@124.174.0.221",
    [string]$SshKeyPath = "D:\codex\BPlatform.pem",
    [string]$RemoteDir = "/opt/new-api/deploy",
    [string]$ComposeFile = "/opt/new-api/deploy/compose.yml",
    [string]$Caddyfile = "/opt/new-api/deploy/Caddyfile",
    [string]$ComposeProject = "new-api-seedance",
    [string]$BlueServiceName = "new-api",
    [string]$GreenServiceName = "new-api-green",
    [string]$BlueUpstream = "new-api:3000",
    [string]$GreenUpstream = "new-api-green:3000",
    [string]$PostgresServiceName = "postgres",
    [string]$PostgresUser = "newapi",
    [string]$PostgresDatabase = "newapi",
    [string]$ImageRepository = "new-api-seedance",
    [string]$ImageTag = "",
    [string]$ImageCommit = "",
    [string]$VersionNamespace = "gateway",
    [string]$Platform = "linux/amd64",
    [string]$BunRegistry = "https://registry.npmmirror.com",
    [ValidateRange(1, 64)][int]$BunMaxHttpRequests = 8,
    [ValidateRange(1, 5)][int]$DockerBuildAttempts = 3,
    [string]$GoProxy = "https://goproxy.cn,direct",
    [ValidateRange(0, 120)][int]$SshConnectionCooldownSeconds = 30,
    [ValidateRange(1, 5)][int]$SshReadRetryCount = 3,
    [int]$HealthTimeoutSeconds = 240,
    [ValidateSet("Direct", "Batch")][string]$BatchUpdateMode = "Direct",
    [ValidateRange(1, 300)][int]$BatchUpdateInterval = 5,
    [switch]$SkipDatabaseBackup,
    [switch]$UseExistingImage,
    [switch]$AllowDirty,
    [switch]$PreflightOnly,
    [switch]$KeepLocalSourceTar,
    [switch]$ShowFailureLogs,
    [switch]$Yes
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path -LiteralPath $SshKeyPath -PathType Leaf)) {
    throw "SSH key not found: $SshKeyPath"
}

$sshArguments = @(
    "-i", $SshKeyPath,
    "-o", "BatchMode=yes",
    "-o", "ConnectTimeout=15",
    "-o", "ConnectionAttempts=1",
    "-o", "ServerAliveInterval=30",
    "-o", "ServerAliveCountMax=6",
    $RemoteHost
)

function Get-CommandOutput {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments
    )

    $output = & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Command failed ($LASTEXITCODE): $FilePath $($Arguments -join ' ')"
    }
    return ($output -join "`n").Trim()
}

function Get-RemoteOutput {
    param([Parameter(Mandatory = $true)][string]$Command)

    for ($attempt = 1; $attempt -le $SshReadRetryCount; $attempt++) {
        $normalized = $Command.TrimStart([char]0xFEFF) -replace "`r`n", "`n"
        $processInfo = New-Object Diagnostics.ProcessStartInfo
        $processInfo.FileName = "ssh"
        $remoteCommand = "sed '1s/^\xEF\xBB\xBF//' | bash -s"
        $processInfo.Arguments = (($sshArguments + @($remoteCommand)) | ForEach-Object { '"' + ($_ -replace '"', '\"') + '"' }) -join " "
        $processInfo.UseShellExecute = $false
        $processInfo.RedirectStandardInput = $true
        $processInfo.RedirectStandardOutput = $true
        $processInfo.RedirectStandardError = $true
        $process = [Diagnostics.Process]::Start($processInfo)
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $scriptBytes = (New-Object Text.UTF8Encoding($false)).GetBytes($normalized)
        $process.StandardInput.BaseStream.Write($scriptBytes, 0, $scriptBytes.Length)
        $process.StandardInput.Close()
        $process.WaitForExit()
        $output = $stdoutTask.Result
        $errorOutput = $stderrTask.Result
        if ($process.ExitCode -eq 0) {
            if ($errorOutput) {
                Write-Verbose $errorOutput.Trim()
            }
            return $output.Trim()
        }
        if ($errorOutput) {
            Write-Warning $errorOutput.Trim()
        }
        if ($attempt -lt $SshReadRetryCount -and $SshConnectionCooldownSeconds -gt 0) {
            Start-Sleep -Seconds $SshConnectionCooldownSeconds
        }
    }
    throw "Remote command failed on $RemoteHost after $SshReadRetryCount attempts."
}

function Invoke-RemoteScript {
    param([Parameter(Mandatory = $true)][string]$Script)

    $normalized = $Script.TrimStart([char]0xFEFF) -replace "`r`n", "`n"
    $processInfo = New-Object Diagnostics.ProcessStartInfo
    $processInfo.FileName = "ssh"
    $remoteCommand = "sed '1s/^\xEF\xBB\xBF//' | bash -s"
    $processInfo.Arguments = (($sshArguments + @($remoteCommand)) | ForEach-Object { '"' + ($_ -replace '"', '\"') + '"' }) -join " "
    $processInfo.UseShellExecute = $false
    $processInfo.RedirectStandardInput = $true
    $process = [Diagnostics.Process]::Start($processInfo)
    $scriptBytes = (New-Object Text.UTF8Encoding($false)).GetBytes($normalized)
    $process.StandardInput.BaseStream.Write($scriptBytes, 0, $scriptBytes.Length)
    $process.StandardInput.Close()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) {
        throw "Remote deployment failed on $RemoteHost."
    }
}

function Invoke-Scp {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination
    )

    $scpArguments = @(
        "-i", $SshKeyPath,
        "-o", "BatchMode=yes",
        "-o", "ConnectTimeout=15",
        "-o", "ConnectionAttempts=1",
        "-o", "ServerAliveInterval=30",
        "-o", "ServerAliveCountMax=6",
        "-C", $Source, $Destination
    )
    for ($attempt = 1; $attempt -le $SshReadRetryCount; $attempt++) {
        & scp @scpArguments
        if ($LASTEXITCODE -eq 0) {
            return
        }
        if ($attempt -lt $SshReadRetryCount -and $SshConnectionCooldownSeconds -gt 0) {
            Start-Sleep -Seconds $SshConnectionCooldownSeconds
        }
    }
    throw "Source upload failed after $SshReadRetryCount attempts: $Source"
}

function Get-GatewayCanaryState {
    param([Parameter(Mandatory = $true)][string]$Config)

    $managedPattern = '(?ms)^[\t ]*# BEGIN NEW-API BLUE-GREEN\s*$.*?^[\t ]*# END NEW-API BLUE-GREEN\s*$'
    $managedMatches = [regex]::Matches($Config, $managedPattern)
    if ($managedMatches.Count -ne 1) {
        throw "Expected exactly one managed Gateway blue/green block, found $($managedMatches.Count)."
    }

    $managedBlock = $managedMatches[0].Value
    $weightMatches = [regex]::Matches($managedBlock, '(?m)^[\t ]*# weights blue=(\d+) green=(\d+)[\t ]*\r?$')
    $proxyPattern = "(?m)^[\t ]*reverse_proxy[\t ]+$([regex]::Escape($BlueUpstream))[\t ]+$([regex]::Escape($GreenUpstream))[\t ]*\{[\t ]*\r?$"
    $proxyMatches = [regex]::Matches($managedBlock, $proxyPattern)
    $fallbackMatches = [regex]::Matches($managedBlock, '(?m)^[\t ]*fallback[\t ]+weighted_round_robin[\t ]+(\d+)[\t ]+(\d+)[\t ]*\r?$')
    $cookieMatches = [regex]::Matches($managedBlock, '(?m)^[\t ]*lb_policy[\t ]+cookie[\t ]+new_api_slot[\t ]+[0-9a-f]{64}[\t ]*\{[\t ]*\r?$')
    if ($weightMatches.Count -ne 1 -or $proxyMatches.Count -ne 1 -or $fallbackMatches.Count -ne 1 -or $cookieMatches.Count -ne 1) {
        throw "The managed Gateway blue/green block does not match the expected upstream and weight structure."
    }

    $blueWeight = [int]$weightMatches[0].Groups[1].Value
    $greenWeight = [int]$weightMatches[0].Groups[2].Value
    if ($blueWeight -ne [int]$fallbackMatches[0].Groups[1].Value -or $greenWeight -ne [int]$fallbackMatches[0].Groups[2].Value) {
        throw "The Gateway weight comment and Caddy fallback weights are inconsistent."
    }
    if ($blueWeight -lt 0 -or $greenWeight -lt 0 -or ($blueWeight + $greenWeight) -ne 100) {
        throw "Gateway blue/green weights must be non-negative and total 100."
    }

    return [pscustomobject]@{ Blue = $blueWeight; Green = $greenWeight }
}

function Wait-ForNextSshConnection {
    if ($SshConnectionCooldownSeconds -gt 0) {
        Start-Sleep -Seconds $SshConnectionCooldownSeconds
    }
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Push-Location $repoRoot
try {
    $branch = Get-CommandOutput git rev-parse --abbrev-ref HEAD
    $sha = Get-CommandOutput git rev-parse --short=12 HEAD
    $workingTreeDirty = Get-CommandOutput git status --porcelain
    if ($workingTreeDirty -and -not $AllowDirty) {
        throw "The working tree has uncommitted or untracked changes. Commit first, or use -AllowDirty only for preflight."
    }
    if ($workingTreeDirty -and $AllowDirty -and -not $PreflightOnly) {
        throw "Remote builds use git archive HEAD and cannot include working-tree changes. Commit before deployment."
    }

    $prepareDirectoriesCommand = if ($PreflightOnly) { ":" } else { "mkdir -p '$RemoteDir/releases' '$RemoteDir/builds' '$RemoteDir/backups'" }
    $remoteStateCommand = @"
set -eu
test -d '$RemoteDir'
test -f '$ComposeFile'
test -f '$Caddyfile'
command -v docker >/dev/null
command -v flock >/dev/null
command -v gzip >/dev/null
command -v sha256sum >/dev/null
docker compose version >/dev/null
blue_status=0
green_status=0
blue_id=`$(docker ps \
  --filter 'label=com.docker.compose.project=$ComposeProject' \
  --filter 'label=com.docker.compose.service=$BlueServiceName' \
  --format '{{.ID}}' | head -n 1)
green_id=`$(docker ps \
  --filter 'label=com.docker.compose.project=$ComposeProject' \
  --filter 'label=com.docker.compose.service=$GreenServiceName' \
  --format '{{.ID}}' | head -n 1)
if [ -n "`$blue_id" ] && docker exec "`$blue_id" wget -q -O /dev/null http://127.0.0.1:3000/api/status; then
  blue_status=1
fi
if [ -n "`$green_id" ] && docker exec "`$green_id" wget -q -O /dev/null http://127.0.0.1:3000/api/status; then
  green_status=1
fi
$prepareDirectoriesCommand
printf 'blue=%s green=%s\n' "`$blue_status" "`$green_status"
base64 -w 0 '$Caddyfile'
printf '\n'
"@
    $remoteState = Get-RemoteOutput $remoteStateCommand
    $remoteStateLines = @($remoteState -split "`n" | Where-Object { $_ })
    $statusMatch = if ($remoteStateLines.Count -ge 1) { [regex]::Match($remoteStateLines[0], '^blue=([01]) green=([01])$') } else { $null }
    if ($remoteStateLines.Count -ne 2 -or -not $statusMatch.Success) {
        throw "Gateway preflight returned an unexpected state payload."
    }
    $blueHealthy = $statusMatch.Groups[1].Value -eq "1"
    $greenHealthy = $statusMatch.Groups[2].Value -eq "1"
    Write-Verbose "Remote slot health: $($remoteStateLines[0])"
    $caddyConfigBytes = [Convert]::FromBase64String($remoteStateLines[1].Trim())
    $caddyConfig = [Text.Encoding]::UTF8.GetString($caddyConfigBytes)
    $canaryState = Get-GatewayCanaryState -Config $caddyConfig
    $blueWeight = $canaryState.Blue
    $greenWeight = $canaryState.Green

    if ($Slot -eq "Auto") {
        if ($blueWeight -eq 0 -and $greenWeight -gt 0) {
            $targetSlot = "Blue"
        } elseif ($greenWeight -eq 0 -and $blueWeight -gt 0) {
            $targetSlot = "Green"
        } else {
            throw "Cannot select an inactive slot while traffic is blue=$blueWeight green=$greenWeight. Promote or roll back the current canary first."
        }
    } else {
        $targetSlot = $Slot
        $targetWeight = if ($targetSlot -eq "Blue") { $blueWeight } else { $greenWeight }
        $sourceWeight = if ($targetSlot -eq "Blue") { $greenWeight } else { $blueWeight }
        if ($targetWeight -ne 0) {
            throw "Refusing to replace $targetSlot while it receives weight $targetWeight."
        }
        if ($sourceWeight -le 0) {
            throw "The other slot is not serving traffic."
        }
    }

    $sourceSlot = if ($targetSlot -eq "Blue") { "Green" } else { "Blue" }
    $targetService = if ($targetSlot -eq "Blue") { $BlueServiceName } else { $GreenServiceName }
    $sourceService = if ($sourceSlot -eq "Blue") { $BlueServiceName } else { $GreenServiceName }
    $slotName = $targetSlot.ToLowerInvariant()
    $nodeName = "gateway-production-$slotName"
    $batchUpdateEnabled = if ($BatchUpdateMode -eq "Batch") { "true" } else { "false" }
    $sourceHealthy = if ($sourceSlot -eq "Blue") { $blueHealthy } else { $greenHealthy }
    if (-not $sourceHealthy) {
        throw "Active source slot $sourceSlot ($sourceService) is not healthy."
    }

    if ($UseExistingImage -and -not $ImageTag) {
        throw "UseExistingImage requires an explicit ImageTag."
    }
    if (-not $ImageTag) {
        $baseVersion = Get-CommandOutput git describe --tags --abbrev=0 HEAD
        $timestamp = (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
        $dirtySuffix = if ($workingTreeDirty) { ".dirty" } else { "" }
        $ImageTag = "$baseVersion.$VersionNamespace.$timestamp.g$sha$dirtySuffix"
    }
    if ($ImageTag -notmatch '^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$') {
        throw "ImageTag must be Docker-safe and no longer than 128 characters."
    }
    $releaseCommit = $sha
    if ($UseExistingImage) {
        if (-not $ImageCommit) {
            $commitMatch = [regex]::Match($ImageTag, '\.g([0-9a-fA-F]{12})(?:\.dirty)?$')
            if (-not $commitMatch.Success) {
                throw "UseExistingImage requires ImageCommit when ImageTag does not end in .g<12-hex-commit>."
            }
            $ImageCommit = $commitMatch.Groups[1].Value
        }
        if ($ImageCommit -notmatch '^[0-9a-fA-F]{7,40}$') {
            throw "ImageCommit must contain 7 to 40 hexadecimal characters."
        }
        $releaseCommit = $ImageCommit.ToLowerInvariant()
    }
    $image = "${ImageRepository}:${ImageTag}"

    if ($PreflightOnly) {
        Write-Host "Preflight OK. Inactive slot: $targetSlot. Planned image: $image"
        return
    }

    if (-not $Yes) {
        Write-Host "Gateway deployment target: $RemoteHost $RemoteDir"
        Write-Host "Traffic: blue=$blueWeight green=$greenWeight"
        Write-Host "Inactive slot: $targetSlot ($targetService)"
        Write-Host "Active source: $sourceSlot ($sourceService)"
        Write-Host "Branch: $branch"
        Write-Host "Commit: $sha"
        Write-Host "Image/version: $image"
        Write-Host "Image source commit: $releaseCommit"
        if ($UseExistingImage) {
            Write-Host "Image source: existing immutable server image (verified before deployment)"
        } else {
            Write-Host "Package mirrors: Bun=$BunRegistry, Go=$GoProxy"
        }
        Write-Host "Traffic will NOT be changed by this script."
        $answer = Read-Host "Type $($targetSlot.ToUpperInvariant()) to continue"
        if ($answer -ne $targetSlot.ToUpperInvariant()) {
            throw "$targetSlot deployment cancelled."
        }
    }

    $deployDir = Join-Path $repoRoot ".deploy"
    New-Item -ItemType Directory -Force -Path $deployDir | Out-Null
    $safeTag = $ImageTag -replace '[^A-Za-z0-9_.-]', '_'
    $localSourceTar = Join-Path $deployDir "$safeTag-source.tar"
    $remoteSourceTar = "$RemoteDir/releases/$safeTag-source.tar"
    $remoteBuildDir = "$RemoteDir/builds/$slotName-$safeTag"
    $overrideFile = "$RemoteDir/compose.$slotName.override.yml"

    if (-not $UseExistingImage) {
        if (Test-Path -LiteralPath $localSourceTar) {
            Remove-Item -LiteralPath $localSourceTar -Force
        }
        Get-CommandOutput git archive --format=tar "--output=$localSourceTar" HEAD | Out-Null
        Wait-ForNextSshConnection
        Invoke-Scp -Source $localSourceTar -Destination "${RemoteHost}:$remoteSourceTar"
    }

    $skipBackupValue = if ($SkipDatabaseBackup) { "1" } else { "0" }
    $useExistingImageValue = if ($UseExistingImage) { "1" } else { "0" }
    $showFailureLogsValue = if ($ShowFailureLogs) { "1" } else { "0" }
    $remoteScript = @"
set -Eeuo pipefail
umask 077

remote_dir="$RemoteDir"
compose_file="$ComposeFile"
compose_project="$ComposeProject"
caddyfile="$Caddyfile"
target_slot="$slotName"
target_service="$targetService"
source_service="$sourceService"
postgres_service="$PostgresServiceName"
postgres_user="$PostgresUser"
postgres_database="$PostgresDatabase"
image="$image"
image_tag="$ImageTag"
source_tar="$remoteSourceTar"
build_dir="$remoteBuildDir"
override_file="$overrideFile"
platform="$Platform"
bun_registry="$BunRegistry"
bun_max_http_requests="$BunMaxHttpRequests"
docker_build_attempts="$DockerBuildAttempts"
go_proxy="$GoProxy"
node_name="$nodeName"
batch_update_enabled="$batchUpdateEnabled"
batch_update_interval="$BatchUpdateInterval"
health_timeout="$HealthTimeoutSeconds"
skip_backup="$skipBackupValue"
use_existing_image="$useExistingImageValue"
show_failure_logs="$showFailureLogsValue"
blue_upstream="$BlueUpstream"
green_upstream="$GreenUpstream"
release_manifest="`$remote_dir/releases/gateway-`$target_slot.json"
lock_file="`$remote_dir/.gateway-blue-green.lock"

service_container() {
    docker ps \
        --filter "label=com.docker.compose.project=`$compose_project" \
        --filter "label=com.docker.compose.service=`$1" \
        --format '{{.ID}}' | head -n 1
}

read_canary_weights() {
    begin_count="`$(grep -Ec '^[[:space:]]*# BEGIN NEW-API BLUE-GREEN[[:space:]]*`$' "`$caddyfile" || true)"
    end_count="`$(grep -Ec '^[[:space:]]*# END NEW-API BLUE-GREEN[[:space:]]*`$' "`$caddyfile" || true)"
    weight_count="`$(grep -Ec '^[[:space:]]*# weights blue=[0-9]+ green=[0-9]+[[:space:]]*`$' "`$caddyfile" || true)"
    proxy_count="`$(grep -Fxc "reverse_proxy `$blue_upstream `$green_upstream {" < <(sed -E 's/^[[:space:]]+//' "`$caddyfile") || true)"
    fallback_count="`$(grep -Ec '^[[:space:]]*fallback[[:space:]]+weighted_round_robin[[:space:]]+[0-9]+[[:space:]]+[0-9]+[[:space:]]*`$' "`$caddyfile" || true)"
    cookie_count="`$(grep -Ec '^[[:space:]]*lb_policy[[:space:]]+cookie[[:space:]]+new_api_slot[[:space:]]+[0-9a-f]{64}[[:space:]]*\{[[:space:]]*`$' "`$caddyfile" || true)"
    if [ "`$begin_count" -ne 1 ] || [ "`$end_count" -ne 1 ] || [ "`$weight_count" -ne 1 ] || [ "`$proxy_count" -ne 1 ] || [ "`$fallback_count" -ne 1 ] || [ "`$cookie_count" -ne 1 ]; then
        echo "The managed Gateway block is missing, duplicated, or malformed." >&2
        return 1
    fi

    weight_line="`$(grep -E '^[[:space:]]*# weights blue=[0-9]+ green=[0-9]+[[:space:]]*`$' "`$caddyfile")"
    fallback_line="`$(grep -E '^[[:space:]]*fallback[[:space:]]+weighted_round_robin[[:space:]]+[0-9]+[[:space:]]+[0-9]+[[:space:]]*`$' "`$caddyfile")"
    blue_weight="`$(printf '%s' "`$weight_line" | sed -E 's/.*blue=([0-9]+).*/\1/')"
    green_weight="`$(printf '%s' "`$weight_line" | sed -E 's/.*green=([0-9]+).*/\1/')"
    fallback_blue="`$(printf '%s' "`$fallback_line" | sed -E 's/.*weighted_round_robin[[:space:]]+([0-9]+)[[:space:]]+([0-9]+).*/\1/')"
    fallback_green="`$(printf '%s' "`$fallback_line" | sed -E 's/.*weighted_round_robin[[:space:]]+([0-9]+)[[:space:]]+([0-9]+).*/\2/')"
    if [ "`$blue_weight" -ne "`$fallback_blue" ] || [ "`$green_weight" -ne "`$fallback_green" ] || [ `$((blue_weight + green_weight)) -ne 100 ]; then
        echo "The managed Gateway weights are inconsistent." >&2
        return 1
    fi
}

read_canary_weights
target_weight="`$green_weight"
if [ "`$target_slot" = "blue" ]; then
    target_weight="`$blue_weight"
fi
if [ "`$target_weight" -ne 0 ]; then
    echo "Target slot `$target_slot now receives weight `$target_weight; refusing replacement." >&2
    exit 1
fi

cd "`$remote_dir"

if [ "`$use_existing_image" = "1" ]; then
    echo "Verifying existing immutable image `$image..."
    docker image inspect "`$image" >/dev/null
else
    echo "Building immutable image `$image with domestic package mirrors..."
    rm -rf "`$build_dir"
    mkdir -p "`$build_dir"
    tar -xf "`$source_tar" -C "`$build_dir"
    build_log="`$(mktemp "`$remote_dir/builds/.docker-build-`$target_slot.XXXXXX.log")"
    build_attempt=1
    while true; do
        : > "`$build_log"
        if docker build \
            --platform "`$platform" \
            --build-arg "BUILD_VERSION=`$image_tag" \
            --build-arg "BUN_REGISTRY=`$bun_registry" \
            --build-arg "BUN_MAX_HTTP_REQUESTS=`$bun_max_http_requests" \
            --build-arg "GO_PROXY=`$go_proxy" \
            -t "`$image" \
            "`$build_dir" 2>&1 | tee "`$build_log"; then
            rm -f "`$build_log"
            break
        fi

        if ! grep -Eq 'Integrity check failed|IntegrityCheckFailed' "`$build_log" || [ "`$build_attempt" -ge "`$docker_build_attempts" ]; then
            rm -f "`$build_log"
            echo "Docker build failed and is not eligible for another integrity retry." >&2
            exit 1
        fi

        echo "Domestic mirror integrity failure on build attempt `$build_attempt; retrying cached build layers..." >&2
        build_attempt=`$((build_attempt + 1))
        sleep 5
    done
fi

if [ "`$skip_backup" != "1" ]; then
    postgres_id="`$(service_container "`$postgres_service")"
    if [ -z "`$postgres_id" ]; then
        echo "PostgreSQL service `$postgres_service is not running." >&2
        exit 1
    fi
    backup_tmp="`$(mktemp "`$remote_dir/backups/postgres-before-`$target_slot-`$image_tag-XXXXXX.sql.gz.tmp")"
    backup_file="`${backup_tmp%.tmp}"
    echo "Creating database backup: `$backup_file"
    if ! docker exec "`$postgres_id" pg_dump -U "`$postgres_user" -d "`$postgres_database" | gzip -1 > "`$backup_tmp"; then
        rm -f "`$backup_tmp"
        echo "PostgreSQL backup failed." >&2
        exit 1
    fi
    if [ ! -s "`$backup_tmp" ] || ! gzip -t "`$backup_tmp"; then
        rm -f "`$backup_tmp"
        echo "PostgreSQL backup verification failed." >&2
        exit 1
    fi
    backup_checksum="`$(sha256sum "`$backup_tmp" | awk '{print `$1}')"
    mv "`$backup_tmp" "`$backup_file"
    checksum_tmp="`$(mktemp "`$backup_file.sha256.tmp.XXXXXX")"
    printf '%s  %s\n' "`$backup_checksum" "`$(basename "`$backup_file")" > "`$checksum_tmp"
    mv "`$checksum_tmp" "`$backup_file.sha256"
fi

# Building and backing up do not mutate either application slot, so emergency
# traffic changes remain available during those potentially long operations.
# Lock only the replacement phase, then re-read the effective Caddy weights.
exec 9>"`$lock_file"
if ! flock -n 9; then
    echo "Another Gateway deployment or traffic change is in progress." >&2
    exit 1
fi

read_canary_weights
target_weight="`$green_weight"
if [ "`$target_slot" = "blue" ]; then
    target_weight="`$blue_weight"
fi
if [ "`$target_weight" -ne 0 ]; then
    echo "Target slot `$target_slot now receives weight `$target_weight; refusing replacement." >&2
    exit 1
fi

source_id="`$(service_container "`$source_service")"
if [ -z "`$source_id" ]; then
    echo "Active source service `$source_service is not running." >&2
    exit 1
fi
docker exec "`$source_id" wget -q -O - http://127.0.0.1:3000/api/status | grep -Eq '"success"[[:space:]]*:[[:space:]]*true'

if [ "`$target_slot" = "green" ]; then
cat > "`$override_file" <<EOF
services:
  `$target_service:
    extends:
      file: $ComposeFile
      service: $BlueServiceName
    image: `$image
    environment:
      BATCH_UPDATE_ENABLED: "`$batch_update_enabled"
      BATCH_UPDATE_INTERVAL: "`$batch_update_interval"
      NODE_NAME: "`$node_name"
EOF
else
cat > "`$override_file" <<EOF
services:
  `$target_service:
    image: `$image
    environment:
      BATCH_UPDATE_ENABLED: "`$batch_update_enabled"
      BATCH_UPDATE_INTERVAL: "`$batch_update_interval"
      NODE_NAME: "`$node_name"
EOF
fi

docker compose -p "`$compose_project" -f "`$compose_file" -f "`$override_file" config --quiet
docker compose -p "`$compose_project" -f "`$compose_file" -f "`$override_file" up -d --no-deps "`$target_service"

deadline=`$((SECONDS + health_timeout))
while [ `$SECONDS -lt `$deadline ]; do
    target_id="`$(service_container "`$target_service")"
    if [ -n "`$target_id" ]; then
        status_body="`$(docker exec "`$target_id" wget -q -O - http://127.0.0.1:3000/api/status 2>/dev/null || true)"
        runtime_version="`$(docker exec "`$target_id" /new-api --version 2>/dev/null || true)"
        health="`$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "`$target_id" 2>/dev/null || true)"
        if printf '%s' "`$status_body" | grep -Eq '"success"[[:space:]]*:[[:space:]]*true' \
            && [ "`$runtime_version" = "`$image_tag" ] \
            && { [ "`$health" = "healthy" ] || [ "`$health" = "running" ]; }; then
            if ! printf '%s' "`$status_body" | grep -Eq "\"enable_batch_update\"[[:space:]]*:[[:space:]]*`$batch_update_enabled"; then
                echo "Slot is healthy but BATCH_UPDATE_ENABLED is not `$batch_update_enabled." >&2
                exit 1
            fi
            mkdir -p "`$remote_dir/releases"
            manifest_tmp="`$(mktemp "`$remote_dir/releases/.gateway-`$target_slot.XXXXXX")"
            printf '{"slot":"%s","service":"%s","image":"%s","version":"%s","commit":"%s","deployed_at":"%s"}\n' \
                "`$target_slot" "`$target_service" "`$image" "`$runtime_version" "$releaseCommit" "`$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
                > "`$manifest_tmp"
            chmod 600 "`$manifest_tmp"
            mv "`$manifest_tmp" "`$release_manifest"
            rm -rf "`$build_dir"
            echo "$targetSlot slot is healthy."
            echo "Service: `$target_service"
            echo "Version: `$runtime_version"
            echo "Traffic remains blue=`$blue_weight green=`$green_weight"
            exit 0
        fi
    fi
    sleep 3
done

echo "$targetSlot slot did not become healthy within `$health_timeout seconds." >&2
target_id="`$(service_container "`$target_service")"
if [ -n "`$target_id" ]; then
    docker inspect -f '{{.Name}} {{.Config.Image}} {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}' "`$target_id" >&2 || true
    if [ "`$show_failure_logs" = "1" ]; then
        docker logs --tail 120 "`$target_id" >&2 || true
    fi
fi
exit 1
"@

    Wait-ForNextSshConnection
    Invoke-RemoteScript $remoteScript

    if (-not $KeepLocalSourceTar -and (Test-Path -LiteralPath $localSourceTar)) {
        Remove-Item -LiteralPath $localSourceTar -Force
    }
}
finally {
    Pop-Location
}
