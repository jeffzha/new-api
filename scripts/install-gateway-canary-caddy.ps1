<#
.SYNOPSIS
Install the Gateway Caddy blue/green block with 100% Blue traffic.

.DESCRIPTION
This is a one-time, backward-compatible wrapper around
set-gateway-canary.ps1. It replaces only the direct new-api reverse_proxy and
preserves all existing TLS, API docs, material, and liveness routes.
#>

[CmdletBinding()]
param(
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
    [string]$DomainStatusUrl = "https://gateway.nexus-reach.com/api/status",
    [string]$IpStatusUrl = "https://124.174.0.221/api/status",
    [switch]$PreflightOnly,
    [switch]$DryRun,
    [switch]$Yes
)

$parameters = @{
    CandidateSlot     = "Green"
    CandidateWeight   = 0
    RemoteHost        = $RemoteHost
    SshKeyPath        = $SshKeyPath
    RemoteDir         = $RemoteDir
    Caddyfile         = $Caddyfile
    ComposeProject    = $ComposeProject
    CaddyServiceName  = $CaddyServiceName
    BlueServiceName   = $BlueServiceName
    GreenServiceName  = $GreenServiceName
    BlueUpstream      = $BlueUpstream
    GreenUpstream     = $GreenUpstream
    DomainStatusUrl   = $DomainStatusUrl
    IpStatusUrl       = $IpStatusUrl
    Install           = $true
    PreflightOnly     = $PreflightOnly.IsPresent
    DryRun            = $DryRun.IsPresent
    Yes               = $Yes.IsPresent
}

& (Join-Path $PSScriptRoot "set-gateway-canary.ps1") @parameters
