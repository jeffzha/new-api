<#
.SYNOPSIS
Deploy a Workbench new-api candidate while preserving the upstream version.

.DESCRIPTION
The generic Gateway deployment script intentionally accepts an explicit image
tag. This wrapper resolves only an upstream semantic-version tag, appends the
Workbench release metadata, and delegates the guarded Blue/Green deployment to
that script. Operational tags such as claw-workbench-release-* cannot replace
the upstream version shown by /new-api --version.
#>

[CmdletBinding()]
param(
    [ValidateSet("Auto", "Blue", "Green")][string]$Slot = "Auto",
    [string]$RemoteHost = "root@124.174.0.221",
    [string]$SshKeyPath = "D:\codex\BPlatform.pem",
    [ValidateRange(0, 120)][int]$SshConnectionCooldownSeconds = 30,
    [switch]$PreflightOnly,
    [switch]$AllowDirty,
    [switch]$ShowFailureLogs,
    [switch]$Yes
)

$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..\..")).Path
$gatewayScript = Join-Path $repoRoot "scripts\deploy-gateway-slot.ps1"
if (-not (Test-Path -LiteralPath $gatewayScript -PathType Leaf)) {
    throw "Gateway deployment script is missing: $gatewayScript"
}

$upstreamVersion = ((& git -C $repoRoot describe --tags --abbrev=0 "--match=v[0-9]*" HEAD) -join "`n").Trim()
if ($LASTEXITCODE -ne 0 -or $upstreamVersion -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') {
    throw "Cannot resolve a valid upstream semantic version tag for HEAD."
}

$revision = ((& git -C $repoRoot rev-parse --short=12 HEAD) -join "`n").Trim()
if ($LASTEXITCODE -ne 0 -or $revision -notmatch '^[0-9a-f]{12}$') {
    throw "Cannot resolve the current new-api revision."
}

$timestamp = (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
$imageTag = "$upstreamVersion.gateway.$timestamp.g$revision"
Write-Host "Upstream new-api version: $upstreamVersion"
Write-Host "Workbench Gateway version: $imageTag"

$parameters = @{
    Slot                         = $Slot
    RemoteHost                   = $RemoteHost
    SshKeyPath                   = $SshKeyPath
    SshConnectionCooldownSeconds = $SshConnectionCooldownSeconds
    ImageTag                     = $imageTag
}
if ($ShowFailureLogs) {
    $parameters.ShowFailureLogs = $true
}
if ($PreflightOnly) {
    $parameters.PreflightOnly = $true
}
if ($AllowDirty) {
    $parameters.AllowDirty = $true
}
if ($Yes) {
    $parameters.Yes = $true
}

& $gatewayScript @parameters
