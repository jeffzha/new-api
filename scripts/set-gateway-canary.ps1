<#
.SYNOPSIS
Change the Gateway production blue/green traffic split in Caddy.

.DESCRIPTION
This script manages only the final new-api reverse_proxy block inside the
existing Caddyfile. Domain/IP TLS, API docs, and asset routes are preserved.
Traffic is sticky for cookie-aware clients and falls back to weighted round
robin for API clients that do not retain cookies.
#>

[CmdletBinding()]
param(
    [ValidateSet("Blue", "Green")][string]$CandidateSlot = "Green",
    [ValidateRange(0, 100)][int]$CandidateWeight = 0,
    [string]$RemoteHost = "root@124.174.0.221",
    [string]$SshKeyPath = "D:\codex\BPlatform.pem",
    [string]$RemoteDir = "/opt/new-api/deploy",
    [string]$Caddyfile = "/opt/new-api/deploy/Caddyfile",
    [string]$ComposeProject = "new-api-seedance",
    [string]$CaddyServiceName = "caddy",
    [string]$BlueServiceName = "new-api",
    [string]$GreenServiceName = "new-api-green",
    [string]$BlueUpstream = "new-api:3000",
    [string]$GreenUpstream = "new-api-green:3000",
    [string]$ExpectedVersion = "",
    [string]$DomainStatusUrl = "https://gateway.nexus-reach.com/api/status",
    [string]$IpStatusUrl = "https://124.174.0.221/api/status",
    [ValidateRange(0, 120)][int]$SshConnectionCooldownSeconds = 30,
    [ValidateRange(1, 5)][int]$SshReadRetryCount = 3,
    [switch]$Promote,
    [switch]$Rollback,
    [switch]$Install,
    [switch]$PreflightOnly,
    [switch]$DryRun,
    [switch]$Yes
)

$ErrorActionPreference = "Stop"

$candidateWeightWasExplicit = $PSBoundParameters.ContainsKey("CandidateWeight")
if ($Install) {
    if ($Promote -or $Rollback -or $CandidateSlot -ne "Green" -or $CandidateWeight -ne 0) {
        throw "-Install only supports the initial Green candidate at weight 0."
    }
} else {
    $operationCount = [int]$candidateWeightWasExplicit + [int]$Promote.IsPresent + [int]$Rollback.IsPresent
    if ($operationCount -ne 1) {
        throw "Specify exactly one operation: -CandidateWeight, -Promote, or -Rollback."
    }
}
if ($Promote) {
    $CandidateWeight = 100
}
if ($Rollback) {
    $CandidateWeight = 0
}
if (-not $Install -and -not $Rollback -and $CandidateWeight -gt 0 -and -not $ExpectedVersion) {
    throw "-ExpectedVersion is required before assigning positive traffic to a candidate."
}

$blueWeight = if ($CandidateSlot -eq "Blue") { $CandidateWeight } else { 100 - $CandidateWeight }
$greenWeight = if ($CandidateSlot -eq "Green") { $CandidateWeight } else { 100 - $CandidateWeight }
$cookieSecretBytes = New-Object byte[] 32
$cookieSecretGenerator = [Security.Cryptography.RandomNumberGenerator]::Create()
try {
    $cookieSecretGenerator.GetBytes($cookieSecretBytes)
}
finally {
    $cookieSecretGenerator.Dispose()
}
$cookieSecret = ([BitConverter]::ToString($cookieSecretBytes)).Replace("-", "").ToLowerInvariant()

$managedBlock = @"
# BEGIN NEW-API BLUE-GREEN
# weights blue=$blueWeight green=$greenWeight
reverse_proxy $BlueUpstream $GreenUpstream {
	lb_policy cookie new_api_slot $cookieSecret {
		fallback weighted_round_robin $blueWeight $greenWeight
	}
	health_uri /api/status
	health_interval 10s
	health_timeout 5s
	health_status 200
	fail_duration 30s
	max_fails 2
	lb_try_duration 5s
	lb_try_interval 250ms
	stream_close_delay 5m
}
# END NEW-API BLUE-GREEN
"@.Trim()

if ($DryRun) {
    Write-Host "Candidate: $CandidateSlot weight=$CandidateWeight"
    Write-Host "Result: blue=$blueWeight green=$greenWeight"
    Write-Host ($managedBlock -replace '(lb_policy cookie new_api_slot)\s+\S+', '$1 <random-secret>')
    return
}

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
            return $output.TrimEnd()
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
        throw "Remote script failed on $RemoteHost."
    }
}

function Add-BlockIndent {
    param(
        [Parameter(Mandatory = $true)][string]$Block,
        [Parameter(Mandatory = $true)][string]$Indent
    )

    return (($Block -split "`n" | ForEach-Object { "$Indent$_" }) -join "`n")
}

function Wait-ForNextSshConnection {
    if ($SshConnectionCooldownSeconds -gt 0) {
        Start-Sleep -Seconds $SshConnectionCooldownSeconds
    }
}

$currentCaddyfileBase64 = (Get-RemoteOutput "set -eu; test -f '$Caddyfile'; base64 -w 0 '$Caddyfile'").Trim()
$currentCaddyfileBytes = [Convert]::FromBase64String($currentCaddyfileBase64)
$currentCaddyfile = [Text.Encoding]::UTF8.GetString($currentCaddyfileBytes)
$sha256 = [Security.Cryptography.SHA256]::Create()
try {
    $currentHash = ([BitConverter]::ToString($sha256.ComputeHash($currentCaddyfileBytes))).Replace("-", "").ToLowerInvariant()
}
finally {
    $sha256.Dispose()
}
$managedPattern = '(?ms)^(?<indent>[\t ]*)# BEGIN NEW-API BLUE-GREEN\s*$.*?^[\t ]*# END NEW-API BLUE-GREEN\s*$'
$managedMatches = [regex]::Matches($currentCaddyfile, $managedPattern)

$managedMatch = if ($managedMatches.Count -eq 1) { $managedMatches[0] } else { $null }
if ($managedMatches.Count -gt 1) {
    throw "Expected one managed blue/green block, found $($managedMatches.Count)."
}

if ($managedMatch) {
    $currentManagedBlock = $managedMatch.Value
    $currentWeightMatches = [regex]::Matches($currentManagedBlock, '(?m)^[\t ]*# weights blue=(\d+) green=(\d+)[\t ]*\r?$')
    $currentProxyPattern = "(?m)^[\t ]*reverse_proxy[\t ]+$([regex]::Escape($BlueUpstream))[\t ]+$([regex]::Escape($GreenUpstream))[\t ]*\{[\t ]*\r?$"
    $currentProxyMatches = [regex]::Matches($currentManagedBlock, $currentProxyPattern)
    $currentCookieMatches = [regex]::Matches($currentManagedBlock, '(?m)^[\t ]*lb_policy[\t ]+cookie[\t ]+new_api_slot[\t ]+[0-9a-f]{64}[\t ]*\{[\t ]*\r?$')
    $currentFallbackMatches = [regex]::Matches($currentManagedBlock, '(?m)^[\t ]*fallback[\t ]+weighted_round_robin[\t ]+(\d+)[\t ]+(\d+)[\t ]*\r?$')
    if ($currentWeightMatches.Count -ne 1 -or $currentProxyMatches.Count -ne 1 -or $currentCookieMatches.Count -ne 1 -or $currentFallbackMatches.Count -ne 1) {
        throw "The existing managed Gateway block is malformed or uses an unexpected load-balancing policy."
    }
    $currentBlueWeight = [int]$currentWeightMatches[0].Groups[1].Value
    $currentGreenWeight = [int]$currentWeightMatches[0].Groups[2].Value
    if ($currentBlueWeight -ne [int]$currentFallbackMatches[0].Groups[1].Value -or $currentGreenWeight -ne [int]$currentFallbackMatches[0].Groups[2].Value -or ($currentBlueWeight + $currentGreenWeight) -ne 100) {
        throw "The existing Gateway weight comment and fallback weights are inconsistent."
    }

    $newlyEnabledSlot = ""
    if ($currentBlueWeight -eq 0 -and $blueWeight -gt 0) {
        $newlyEnabledSlot = "Blue"
    }
    if ($currentGreenWeight -eq 0 -and $greenWeight -gt 0) {
        if ($newlyEnabledSlot) {
            throw "A single traffic update cannot enable both zero-weight slots."
        }
        $newlyEnabledSlot = "Green"
    }
    if ($Rollback -and $currentBlueWeight -eq 0 -and $currentGreenWeight -eq 0) {
        throw "Rollback source cannot be determined from an invalid zero/zero state."
    }
    if ($Rollback) {
        $currentCandidateWeight = if ($CandidateSlot -eq "Blue") { $currentBlueWeight } else { $currentGreenWeight }
        if ($currentCandidateWeight -le 0) {
            throw "Cannot roll back $CandidateSlot because it currently receives no traffic."
        }
    } elseif ($newlyEnabledSlot -and $newlyEnabledSlot -ne $CandidateSlot) {
        throw "This update would enable $newlyEnabledSlot from zero weight, but CandidateSlot is $CandidateSlot. Use an explicit rollback for that transition."
    }

    $releaseValidationSlot = if ($newlyEnabledSlot) { $newlyEnabledSlot } elseif ($CandidateWeight -gt 0) { $CandidateSlot } else { "" }
    $replacement = Add-BlockIndent -Block $managedBlock -Indent $managedMatch.Groups["indent"].Value
    $managedRegex = [regex]::new($managedPattern)
    $updatedCaddyfile = $managedRegex.Replace($currentCaddyfile, [System.Text.RegularExpressions.MatchEvaluator]{ param($match) $replacement }, 1)
} else {
    if (-not $Install) {
        throw "Managed blue/green block not found. Run install-gateway-canary-caddy.ps1 once first."
    }

    $directPattern = "(?m)^(?<indent>[\t ]*)reverse_proxy[\t ]+$([regex]::Escape($BlueUpstream))[\t ]*$"
    $directMatches = [regex]::Matches($currentCaddyfile, $directPattern)
    if ($directMatches.Count -ne 1) {
        throw "Expected exactly one direct reverse_proxy to $BlueUpstream, found $($directMatches.Count)."
    }
    $replacement = Add-BlockIndent -Block $managedBlock -Indent $directMatches[0].Groups["indent"].Value
    $directRegex = [regex]::new($directPattern)
    $updatedCaddyfile = $directRegex.Replace($currentCaddyfile, [System.Text.RegularExpressions.MatchEvaluator]{ param($match) $replacement }, 1)
    $releaseValidationSlot = ""
}

$configBase64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($updatedCaddyfile + "`n"))

if ($PreflightOnly) {
    $preflightScript = @"
set -Eeuo pipefail
compose_project="$ComposeProject"
caddy_service="$CaddyServiceName"
config_b64="$configBase64"
caddy_container="`$(docker ps \
    --filter "label=com.docker.compose.project=`$compose_project" \
    --filter "label=com.docker.compose.service=`$caddy_service" \
    --format '{{.ID}}' | head -n 1)"
test -n "`$caddy_container"
printf '%s' "`$config_b64" | base64 -d | docker exec -i "`$caddy_container" caddy validate --config - --adapter caddyfile
$(if ($blueWeight -gt 0) { 'docker exec "$caddy_container" wget -q -O - http://' + $BlueUpstream + '/api/status | grep -q success' })
$(if ($greenWeight -gt 0) { 'docker exec "$caddy_container" wget -q -O - http://' + $GreenUpstream + '/api/status | grep -q success' })
echo "Gateway Caddy preflight OK: blue=$blueWeight green=$greenWeight. No configuration was changed."
"@
    Wait-ForNextSshConnection
    Invoke-RemoteScript $preflightScript
    return
}

if (-not $Yes) {
    Write-Host "Target: $RemoteHost"
    Write-Host "Caddyfile: $Caddyfile"
    Write-Host "Candidate: $CandidateSlot weight=$CandidateWeight"
    Write-Host "Traffic split: blue=$blueWeight green=$greenWeight"
    $answer = Read-Host "Type CANARY to continue"
    if ($answer -ne "CANARY") {
        throw "Gateway canary update cancelled."
    }
}

$remoteScript = @"
set -Eeuo pipefail
umask 077

remote_dir="$RemoteDir"
caddyfile="$Caddyfile"
compose_project="$ComposeProject"
caddy_service="$CaddyServiceName"
blue_service="$BlueServiceName"
green_service="$GreenServiceName"
validation_slot="$($releaseValidationSlot.ToLowerInvariant())"
validation_service="$(if ($releaseValidationSlot -eq 'Blue') { $BlueServiceName } elseif ($releaseValidationSlot -eq 'Green') { $GreenServiceName } else { '' })"
install_mode="$(if ($Install) { '1' } else { '0' })"
expected_version="$ExpectedVersion"
domain_status_url="$DomainStatusUrl"
ip_status_url="$IpStatusUrl"
blue_weight="$blueWeight"
green_weight="$greenWeight"
expected_hash="$currentHash"
config_b64="$configBase64"
lock_file="`$remote_dir/.gateway-blue-green.lock"
backup_dir="`$remote_dir/backups/caddy-canary-`$(date -u +%Y%m%dT%H%M%SZ)"

service_container() {
    docker ps \
        --filter "label=com.docker.compose.project=`$compose_project" \
        --filter "label=com.docker.compose.service=`$1" \
        --format '{{.ID}}' | head -n 1
}

check_service() {
    service_name="`$1"
    container_id="`$(service_container "`$service_name")"
    if [ -z "`$container_id" ]; then
        echo "Compose service `$service_name is not running." >&2
        return 1
    fi
    docker exec "`$container_id" wget -q -O - http://127.0.0.1:3000/api/status | grep -Eq '"success"[[:space:]]*:[[:space:]]*true'
}

check_release() {
    release_slot="`$1"
    release_service="`$2"
    manifest_file="`$remote_dir/releases/gateway-`$release_slot.json"
    if [ ! -f "`$manifest_file" ]; then
        echo "Missing release manifest for slot `$release_slot: `$manifest_file" >&2
        return 1
    fi

    manifest_service="`$(sed -n 's/.*"service":"\([^"]*\)".*/\1/p' "`$manifest_file")"
    manifest_image="`$(sed -n 's/.*"image":"\([^"]*\)".*/\1/p' "`$manifest_file")"
    manifest_version="`$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' "`$manifest_file")"
    if [ "`$manifest_service" != "`$release_service" ] || [ -z "`$manifest_image" ] || [ -z "`$manifest_version" ]; then
        echo "Invalid release manifest for slot `$release_slot." >&2
        return 1
    fi
    if [ -n "`$expected_version" ] && [ "`$manifest_version" != "`$expected_version" ]; then
        echo "Candidate version mismatch: expected `$expected_version, manifest has `$manifest_version." >&2
        return 1
    fi

    release_id="`$(service_container "`$release_service")"
    runtime_version="`$(docker exec "`$release_id" /new-api --version 2>/dev/null || true)"
    container_image="`$(docker inspect -f '{{.Config.Image}}' "`$release_id" 2>/dev/null || true)"
    if [ "`$runtime_version" != "`$manifest_version" ] || [ "`$container_image" != "`$manifest_image" ]; then
        echo "Slot `$release_slot does not match its release manifest." >&2
        return 1
    fi
}

check_caddy_upstream() {
    upstream="`$1"
    docker exec "`$caddy_container" wget -q -O - "http://`$upstream/api/status" | grep -Eq '"success"[[:space:]]*:[[:space:]]*true'
}

check_public_status() {
    for status_url in "`$domain_status_url" "`$ip_status_url"; do
        if ! curl --fail --show-error --silent --max-time 20 "`$status_url" | grep -Eq '"success"[[:space:]]*:[[:space:]]*true'; then
            echo "Public status check failed: `$status_url" >&2
            return 1
        fi
    done
}

restore_previous_config() {
    echo "Restoring the previous Caddy configuration..." >&2
    cp "`$backup_dir/Caddyfile" "`$caddyfile" || return 1
    docker exec "`$caddy_container" caddy validate --config /etc/caddy/Caddyfile || return 1
    docker exec "`$caddy_container" caddy reload --config /etc/caddy/Caddyfile || return 1
    check_public_status || return 1
}

fail_with_rollback() {
    failure_message="`$1"
    echo "`$failure_message" >&2
    if restore_previous_config; then
        echo "The previous Caddy configuration is active and its public endpoints are healthy." >&2
    else
        echo "CRITICAL: automatic Caddy rollback could not be verified; manual intervention is required." >&2
    fi
    exit 1
}

command -v flock >/dev/null
exec 9>"`$lock_file"
if ! flock -n 9; then
    echo "Another Gateway deployment or traffic change is in progress." >&2
    exit 1
fi

actual_hash="`$(sha256sum "`$caddyfile" | awk '{print `$1}')"
if [ "`$actual_hash" != "`$expected_hash" ]; then
    echo "Caddyfile changed after it was read; refusing to overwrite a concurrent update." >&2
    exit 1
fi

caddy_container="`$(service_container "`$caddy_service")"
if [ -z "`$caddy_container" ]; then
    echo "Caddy compose service `$caddy_service is not running." >&2
    exit 1
fi

if [ "`$blue_weight" -gt 0 ]; then
    check_service "`$blue_service"
    check_caddy_upstream "$BlueUpstream"
fi
if [ "`$green_weight" -gt 0 ]; then
    check_service "`$green_service"
    check_caddy_upstream "$GreenUpstream"
fi
if [ -n "`$validation_slot" ]; then
    check_release "`$validation_slot" "`$validation_service"
fi

mkdir -p "`$backup_dir"
cp "`$caddyfile" "`$backup_dir/Caddyfile"
tmp_file="`$(mktemp "`$remote_dir/.Caddyfile.canary.XXXXXX")"
trap 'rm -f "`$tmp_file"' EXIT
printf '%s' "`$config_b64" | base64 -d > "`$tmp_file"
chmod --reference="`$caddyfile" "`$tmp_file"

# The Caddyfile is bind-mounted as a file. Copy into the existing inode so the
# running container sees the update; replacing it with mv would leave the bind
# mount attached to the old inode.
cp "`$tmp_file" "`$caddyfile"
if ! docker exec "`$caddy_container" caddy validate --config /etc/caddy/Caddyfile; then
    fail_with_rollback "Caddy validation failed."
fi

if ! docker exec "`$caddy_container" caddy reload --config /etc/caddy/Caddyfile; then
    fail_with_rollback "Caddy reload failed."
fi

if ! check_public_status; then
    fail_with_rollback "Public status validation failed after the Caddy reload."
fi

if [ "`$install_mode" = "1" ]; then
    blue_id="`$(service_container "`$blue_service")"
    blue_version="`$(docker exec "`$blue_id" /new-api --version)"
    blue_image="`$(docker inspect -f '{{.Config.Image}}' "`$blue_id")"
    mkdir -p "`$remote_dir/releases"
    manifest_tmp="`$(mktemp "`$remote_dir/releases/.gateway-blue.XXXXXX")"
    printf '{"slot":"blue","service":"%s","image":"%s","version":"%s","commit":"bootstrap","deployed_at":"%s"}\n' \
        "`$blue_service" "`$blue_image" "`$blue_version" "`$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "`$manifest_tmp"
    chmod 600 "`$manifest_tmp"
    mv "`$manifest_tmp" "`$remote_dir/releases/gateway-blue.json"
fi

echo "Gateway canary traffic updated: blue=`$blue_weight green=`$green_weight"
echo "Backup: `$backup_dir"
"@

Wait-ForNextSshConnection
Invoke-RemoteScript $remoteScript
