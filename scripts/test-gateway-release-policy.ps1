$ErrorActionPreference = 'Stop'
$BlueUpstream = 'new-api:3000'
$GreenUpstream = 'new-api-green:3000'
$blueWeight = 100
$greenWeight = 0
$CandidateWeight = 0
$CandidateSlot = 'Green'
$Rollback = $false
$Install = $false
$source = Get-Content (Join-Path $PSScriptRoot 'set-gateway-canary.ps1') -Raw
$tokens = $null
$errors = $null
$ast = [Management.Automation.Language.Parser]::ParseInput($source, [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw $errors[0] }
$indentFunction = $ast.Find({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Add-BlockIndent' }, $true)
Invoke-Expression $indentFunction.Extent.Text
$start = $source.IndexOf('$managedPattern =')
$end = $source.IndexOf('$configBase64 =', $start)
$update = $source.Substring($start, $end - $start)
$deploySource = Get-Content (Join-Path $PSScriptRoot 'deploy-gateway-slot.ps1') -Raw
$deployAst = [Management.Automation.Language.Parser]::ParseInput($deploySource, [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw $errors[0] }
$readFunction = $deployAst.Find({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Get-GatewayCanaryState' }, $true)
Invoke-Expression $readFunction.Extent.Text
foreach ($policy in @('lb_policy weighted_round_robin 50 50', ('lb_policy cookie new_api_slot ' + ('a' * 64) + ' {' + "`n            fallback weighted_round_robin 50 50`n        }"))) {
    $currentCaddyfile = @"
(new_api_backend) {
    # BEGIN NEW-API BLUE-GREEN
    # weights blue=50 green=50
    vars gateway_release_scope true
    reverse_proxy new-api:3000 new-api-green:3000 {
        $policy
        health_uri /healthz
        health_timeout 5s
        stream_close_delay 5m
    }
    # END NEW-API BLUE-GREEN
}
(unrelated_routes) {
    reverse_proxy new-api-pricing-reserve:3000
}
"@
    $state = Get-GatewayCanaryState $currentCaddyfile
    if ($state.Blue -ne 50 -or $state.Green -ne 50) { throw 'Original weights were not read' }
    Invoke-Expression $update
    $state = Get-GatewayCanaryState $updatedCaddyfile
    if ($state.Blue -ne 100 -or $state.Green -ne 0) { throw 'Incorrect traffic update' }
    $expected = $currentCaddyfile.Replace('# weights blue=50 green=50', '# weights blue=100 green=0').Replace('weighted_round_robin 50 50', 'weighted_round_robin 100 0')
    if ($updatedCaddyfile.TrimEnd() -cne $expected.TrimEnd()) { throw 'Traffic update changed unrelated configuration' }
    $rejected = $false
    try { Get-GatewayCanaryState ($updatedCaddyfile.Replace('# weights blue=100 green=0', '# weights blue=50 green=50')) | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw 'Inconsistent weights were accepted' }
}
Write-Output 'Gateway release policy tests passed: direct and cookie weights, fallback scope preservation, inconsistent weight rejection'
