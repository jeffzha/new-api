[CmdletBinding()]
param(
    [string]$BaseUrl = "https://124.174.0.221",
    [string]$SshHost = "124.174.0.221",
    [string]$SshUser = "root",
    [string]$SshKeyPath = "D:\codex\BPlatform.pem",
    [int]$TokenId = 1,
    [string]$TokenName = "seedance-assets-production",
    [string]$ImageUrl = "https://images.clipsafari.com/6rhxknsi0s4gqoqot0z9u2593bf6?filename=cartoon-woman.png",
    [string]$InvalidImageUrl = "https://httpbin.org/image/png",
    [string]$ExpectedInvalidAssetErrorCode = "InvalidParameter.WidthTooSmall",
    [string]$ResultRoot = ".deploy\seedance-public-ip-e2e",
    [int]$PollIntervalSeconds = 10,
    [int]$TimeoutSeconds = 900
)

$ErrorActionPreference = "Stop"
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$runName = (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
$runDir = Join-Path $ResultRoot $runName
New-Item -ItemType Directory -Force -Path $runDir | Out-Null

function Write-Utf8Json {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)]$Value
    )

    $json = $Value | ConvertTo-Json -Depth 30
    [IO.File]::WriteAllText($Path, $json, $utf8NoBom)
}

function Invoke-JsonEndpoint {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][ValidateSet("GET", "POST")][string]$Method,
        [Parameter(Mandatory = $true)][string]$Path,
        $Body = $null,
        [switch]$RedactVisualSessionResponse
    )

    $headersPath = Join-Path $runDir "$Name.response.headers"
    $responsePath = Join-Path $runDir "$Name.response.json"
    $arguments = @(
        "-sS",
        "--connect-timeout", "15",
        "--max-time", "120",
        "-D", $headersPath,
        "-X", $Method
    )
    if (-not $RedactVisualSessionResponse) {
        $arguments += @("-o", $responsePath)
    }
    $arguments += @(
        "-w", "%{http_code}",
        "$BaseUrl$Path",
        "-H", "Authorization: Bearer $apiKey"
    )

    $requestPath = $null
    if ($null -ne $Body) {
        $requestPath = Join-Path $runDir "$Name.request.json"
        Write-Utf8Json -Path $requestPath -Value $Body
        $arguments += @("-H", "Content-Type: application/json", "--data-binary", "@$requestPath")
    }

    $transportOutput = (& curl.exe @arguments | Out-String).TrimEnd()
    if ($LASTEXITCODE -ne 0) {
        throw "$Name failed at the TLS/HTTP transport layer (curl exit $LASTEXITCODE)."
    }
    if ($transportOutput.Length -lt 3 -or $transportOutput.Substring($transportOutput.Length - 3) -notmatch '^[0-9]{3}$') {
        throw "$Name did not return a valid curl HTTP status marker."
    }
    $httpStatus = $transportOutput.Substring($transportOutput.Length - 3)
    if ($RedactVisualSessionResponse) {
        $rawBody = $transportOutput.Substring(0, $transportOutput.Length - 3)
    }
    else {
        $rawBody = [IO.File]::ReadAllText($responsePath, [Text.Encoding]::UTF8)
    }
    $parsedBody = $null
    if (-not [string]::IsNullOrWhiteSpace($rawBody)) {
        try {
            $parsedBody = $rawBody | ConvertFrom-Json
        }
        catch {
            if ($RedactVisualSessionResponse) {
                throw "$Name returned invalid JSON; the sensitive response was not persisted."
            }
            throw "$Name returned non-JSON content: $rawBody"
        }
    }
    if (Select-String -LiteralPath $headersPath -Pattern '^Set-Cookie:' -CaseSensitive:$false -Quiet) {
        throw "$Name exposed an upstream Set-Cookie response header."
    }
    if ($RedactVisualSessionResponse) {
        if ($null -ne $parsedBody.data.Result) {
            $parsedBody.data.Result.BytedToken = "<redacted>"
            $parsedBody.data.Result.H5Link = "<redacted>"
        }
        if ($null -ne $parsedBody.data.ResponseMetadata.RequestId) {
            $parsedBody.data.ResponseMetadata.RequestId = "<redacted>"
        }
        Write-Utf8Json -Path $responsePath -Value $parsedBody
        $rawBody = $null
        $transportOutput = $null
    }

    Write-Host "$Name http=$httpStatus"
    return [pscustomobject]@{
        Name = $Name
        Method = $Method
        Url = "$BaseUrl$Path"
        HttpStatus = [int]$httpStatus
        RequestPath = $requestPath
        ResponsePath = $responsePath
        HeadersPath = $headersPath
        Body = $parsedBody
    }
}

function Assert-Http200 {
    param([Parameter(Mandatory = $true)]$Result)
    if ($Result.HttpStatus -ne 200) {
        throw "$($Result.Name) returned HTTP $($Result.HttpStatus). See $($Result.ResponsePath)."
    }
}

function Assert-BusinessSuccess {
    param([Parameter(Mandatory = $true)]$Result)
    Assert-Http200 -Result $Result
    if ($Result.Body.state -ne 1) {
        throw "$($Result.Name) returned state=$($Result.Body.state). See $($Result.ResponsePath)."
    }
}

function Get-ResponseHeader {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Name
    )

    $pattern = '^' + [Regex]::Escape($Name) + ':\s*(.+)$'
    $match = Select-String -LiteralPath $Path -Pattern $pattern -CaseSensitive:$false |
        Select-Object -Last 1
    if ($null -eq $match) {
        return ""
    }
    return $match.Matches[0].Groups[1].Value.Trim()
}

$sshArgs = @(
    "-o", "BatchMode=yes",
    "-o", "StrictHostKeyChecking=accept-new",
    "-i", $SshKeyPath,
    "$SshUser@$SshHost",
    "docker exec -i new-api-seedance-postgres-1 psql -U newapi -d newapi -X -q -t -A -v ON_ERROR_STOP=1"
)
$escapedTokenName = $TokenName.Replace("'", "''")
$tokenSql = "select key from tokens where id = $TokenId and name = '$escapedTokenName' and deleted_at is null;"
$tokenValue = ($tokenSql | & ssh @sshArgs | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($tokenValue)) {
    throw "Unable to load the E2E API token from the production database."
}
$apiKey = if ($tokenValue.StartsWith("sk-")) { $tokenValue } else { "sk-$tokenValue" }

try {
    $callbackUrl = "$BaseUrl/apidocs/apidocs-example-callback.html"

    $callbackProbeToken = "non-secret-header-probe-$runName"
    $callbackHeadersPath = Join-Path $runDir "00-visual-callback.response.headers"
    $callbackBodyPath = Join-Path $runDir "00-visual-callback.response.html"
    $callbackHttp = (& curl.exe `
        -sS `
        --connect-timeout 15 `
        --max-time 120 `
        -D $callbackHeadersPath `
        -o $callbackBodyPath `
        -w "%{http_code}" `
        "$callbackUrl`?bytedToken=$callbackProbeToken&resultCode=10000").Trim()
    if ($LASTEXITCODE -ne 0 -or [int]$callbackHttp -ne 200) {
        throw "The visual callback security probe failed with HTTP $callbackHttp and curl exit $LASTEXITCODE."
    }
    $callbackCacheControl = Get-ResponseHeader -Path $callbackHeadersPath -Name "Cache-Control"
    $callbackReferrerPolicy = Get-ResponseHeader -Path $callbackHeadersPath -Name "Referrer-Policy"
    $callbackContentTypeOptions = Get-ResponseHeader -Path $callbackHeadersPath -Name "X-Content-Type-Options"
    $callbackContentSecurityPolicy = Get-ResponseHeader -Path $callbackHeadersPath -Name "Content-Security-Policy"
    if ($callbackCacheControl -ne "no-store" -or
        $callbackReferrerPolicy -ne "no-referrer" -or
        $callbackContentTypeOptions -ne "nosniff" -or
        $callbackContentSecurityPolicy -notmatch "default-src 'none'") {
        throw "The visual callback did not return all required security headers."
    }
    if (Select-String -LiteralPath $callbackHeadersPath -Pattern '^Set-Cookie:' -CaseSensitive:$false -Quiet) {
        throw "The visual callback unexpectedly returned Set-Cookie."
    }
    if (Select-String -LiteralPath $callbackBodyPath -SimpleMatch $callbackProbeToken -Quiet) {
        throw "The visual callback response body reflected the query token."
    }

    $visualSession = Invoke-JsonEndpoint `
        -Name "01-create-visual-session" `
        -Method POST `
        -Path "/api/v3/open/CreateVisualValidateSession" `
        -Body ([ordered]@{ CallbackURL = $callbackUrl }) `
        -RedactVisualSessionResponse
    Assert-BusinessSuccess -Result $visualSession

    $invalidVisualResult = Invoke-JsonEndpoint `
        -Name "02-get-visual-result-invalid-token" `
        -Method POST `
        -Path "/api/v3/open/GetVisualValidateResult" `
        -Body ([ordered]@{ BytedToken = "invalid-e2e-$runName" })
    Assert-Http200 -Result $invalidVisualResult
    if ($invalidVisualResult.Body.state -ne 0) {
        throw "The invalid-token visual-result check unexpectedly returned state=$($invalidVisualResult.Body.state)."
    }

    $assetGroup = Invoke-JsonEndpoint `
        -Name "03-create-asset-group" `
        -Method POST `
        -Path "/api/v3/open/CreateAssetGroup" `
        -Body ([ordered]@{
            Name = "nexus-ip-e2e-$runName"
            Description = "Public IP Seedance 2.0 E2E validation"
        })
    Assert-BusinessSuccess -Result $assetGroup
    $groupId = [string]$assetGroup.Body.data.Id
    if ([string]::IsNullOrWhiteSpace($groupId)) {
        throw "CreateAssetGroup did not return data.Id."
    }

    $asset = Invoke-JsonEndpoint `
        -Name "04-create-image-asset" `
        -Method POST `
        -Path "/api/v3/open/CreateAsset" `
        -Body ([ordered]@{
            GroupId = $groupId
            URL = $ImageUrl
            Name = "nexus-ip-e2e-virtual-avatar-$runName"
            AssetType = "Image"
        })
    Assert-BusinessSuccess -Result $asset
    $assetNamespace = Get-ResponseHeader -Path $asset.HeadersPath -Name "X-New-Api-Asset-Namespace"
    if ($assetNamespace -ne "seedance-domestic") {
        throw "CreateAsset did not return the expected asset namespace header."
    }
    $assetId = [string]$asset.Body.data.Id
    if ([string]::IsNullOrWhiteSpace($assetId)) {
        throw "CreateAsset did not return data.Id."
    }

    $assetDeadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $assetPoll = 0
    $assetStatus = ""
    do {
        $assetPoll++
        $assetResult = Invoke-JsonEndpoint `
            -Name ("05-get-asset-{0:d3}" -f $assetPoll) `
            -Method POST `
            -Path "/api/v3/open/GetAsset" `
            -Body ([ordered]@{ Id = $assetId })
        Assert-BusinessSuccess -Result $assetResult
        $assetStatus = [string]$assetResult.Body.data.Status
        Write-Output "asset_status=$assetStatus"
        if ($assetStatus -eq "Failed") {
            throw "The E2E image asset failed processing."
        }
        if ($assetStatus -ne "Active") {
            Start-Sleep -Seconds $PollIntervalSeconds
        }
    } while ($assetStatus -ne "Active" -and (Get-Date) -lt $assetDeadline)
    if ($assetStatus -ne "Active") {
        throw "The E2E image asset did not become Active before timeout."
    }

    $invalidAsset = Invoke-JsonEndpoint `
        -Name "05b-create-image-asset-invalid" `
        -Method POST `
        -Path "/api/v3/open/CreateAsset" `
        -Body ([ordered]@{
            GroupId = $groupId
            URL = $InvalidImageUrl
            Name = "nexus-ip-e2e-invalid-$runName"
            AssetType = "Image"
        })
    if ($invalidAsset.HttpStatus -ne 400 -or [string]$invalidAsset.Body.error.code -ne $ExpectedInvalidAssetErrorCode) {
        throw "The invalid CreateAsset check did not preserve HTTP 400 / $ExpectedInvalidAssetErrorCode."
    }

    $privateBillRoute = Invoke-JsonEndpoint `
        -Name "06-private-bill-route-must-not-exist" `
        -Method POST `
        -Path "/api/v3/open/ListSplitBillDetail" `
        -Body ([ordered]@{})
    if ($privateBillRoute.HttpStatus -ne 404) {
        throw "ListSplitBillDetail must remain private, but the public route returned HTTP $($privateBillRoute.HttpStatus)."
    }

    $videoPrompt = [Text.Encoding]::UTF8.GetString(
        [Convert]::FromBase64String("5Zu+54mHMeS4reeahOiZmuaLn+S6uueJqeiHqueEtuecqOecvOW5tui9u+W+ruWRvOWQuO+8jOmVnOWktOWbuuWumu+8jOaXoOaWh+Wtl+OAgg==")
    )
    $videoBody = [ordered]@{
        model = "doubao-seedance-2-0-260128"
        content = @(
            [ordered]@{
                type = "image_url"
                image_url = [ordered]@{ url = "asset://$assetId" }
                role = "reference_image"
            },
            [ordered]@{
                type = "text"
                text = $videoPrompt
            }
        )
        audio_status = 0
        resolution = "720p"
        ratio = "16:9"
        dur = 4
    }
    $videoSubmit = Invoke-JsonEndpoint `
        -Name "07-create-video" `
        -Method POST `
        -Path "/v1/video/generations" `
        -Body $videoBody
    Assert-Http200 -Result $videoSubmit
    $taskId = [string]$videoSubmit.Body.task_id
    if ([string]::IsNullOrWhiteSpace($taskId)) {
        $taskId = [string]$videoSubmit.Body.id
    }
    if ([string]::IsNullOrWhiteSpace($taskId)) {
        throw "Video submission did not return a public task ID."
    }

    $taskDeadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $taskPoll = 0
    $taskStatus = ""
    do {
        $taskPoll++
        $taskResult = Invoke-JsonEndpoint `
            -Name ("08-get-video-task-{0:d3}" -f $taskPoll) `
            -Method GET `
            -Path "/v1/video/generations/$taskId"
        Assert-Http200 -Result $taskResult
        $taskStatus = [string]$taskResult.Body.data.status
        Write-Output "video_status=$taskStatus progress=$($taskResult.Body.data.progress)"
        if ($taskStatus -eq "FAILURE") {
            throw "The E2E video task failed: $($taskResult.Body.data.fail_reason)"
        }
        if ($taskStatus -ne "SUCCESS") {
            Start-Sleep -Seconds $PollIntervalSeconds
        }
    } while ($taskStatus -ne "SUCCESS" -and (Get-Date) -lt $taskDeadline)
    if ($taskStatus -ne "SUCCESS") {
        throw "The E2E video task did not succeed before timeout."
    }

    $openAiTask = Invoke-JsonEndpoint `
        -Name "09-get-video-task-openai" `
        -Method GET `
        -Path "/v1/videos/$taskId"
    Assert-Http200 -Result $openAiTask
    if ([string]$openAiTask.Body.status -ne "completed") {
        throw "The OpenAI-compatible task query did not return completed."
    }

    $contentHeadersPath = Join-Path $runDir "10-download-video.response.headers"
    $contentPath = Join-Path $runDir "10-download-video.mp4"
    $contentHttp = (& curl.exe `
        -sS `
        --connect-timeout 15 `
        --max-time 240 `
        -D $contentHeadersPath `
        -o $contentPath `
        -w "%{http_code}" `
        "$BaseUrl/v1/videos/$taskId/content" `
        -H "Authorization: Bearer $apiKey").Trim()
    if ($LASTEXITCODE -ne 0 -or [int]$contentHttp -ne 200) {
        throw "Video content download failed with HTTP $contentHttp and curl exit $LASTEXITCODE."
    }
    $contentInfo = Get-Item -LiteralPath $contentPath
    if ($contentInfo.Length -le 0) {
        throw "Video content download returned an empty file."
    }
    $firstBytes = [IO.File]::ReadAllBytes($contentPath)
    if ($firstBytes.Length -lt 1024) {
        throw "Video content is too short to validate its MP4 header and Range prefix."
    }
    $prefixLength = [Math]::Min(16, $firstBytes.Length)
    $prefixHex = -join ($firstBytes[0..($prefixLength - 1)] | ForEach-Object { $_.ToString("x2") })
    if ($prefixHex -notmatch '^[0-9a-f]{8}66747970') {
        throw "Video content does not contain the expected MP4 ftyp box."
    }
    $contentHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $contentPath).Hash.ToLowerInvariant()
    $contentCacheControl = Get-ResponseHeader -Path $contentHeadersPath -Name "Cache-Control"
    $contentType = Get-ResponseHeader -Path $contentHeadersPath -Name "Content-Type"
    if ($contentCacheControl -ne "private, no-store") {
        throw "Full video download did not enforce Cache-Control: private, no-store."
    }

    $rangeHeadersPath = Join-Path $runDir "11-download-video-range.response.headers"
    $rangePath = Join-Path $runDir "11-download-video-range.bin"
    $rangeHttp = (& curl.exe `
        -sS `
        --connect-timeout 15 `
        --max-time 120 `
        -D $rangeHeadersPath `
        -o $rangePath `
        -w "%{http_code}" `
        "$BaseUrl/v1/videos/$taskId/content" `
        -H "Authorization: Bearer $apiKey" `
        -H "Range: bytes=0-1023").Trim()
    if ($LASTEXITCODE -ne 0 -or [int]$rangeHttp -ne 206) {
        throw "Video Range download failed with HTTP $rangeHttp and curl exit $LASTEXITCODE."
    }
    $rangeInfo = Get-Item -LiteralPath $rangePath
    $rangeContentRange = Get-ResponseHeader -Path $rangeHeadersPath -Name "Content-Range"
    $rangeCacheControl = Get-ResponseHeader -Path $rangeHeadersPath -Name "Cache-Control"
    if ($rangeInfo.Length -ne 1024 -or $rangeContentRange -notmatch '^bytes 0-1023/[0-9]+$') {
        throw "Video Range response returned an unexpected byte count or Content-Range."
    }
    if ($rangeCacheControl -ne "private, no-store") {
        throw "Video Range response did not enforce Cache-Control: private, no-store."
    }
    $rangeHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $rangePath).Hash.ToLowerInvariant()
    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
        $fullPrefixHash = -join ($sha256.ComputeHash($firstBytes, 0, 1024) | ForEach-Object { $_.ToString("x2") })
    }
    finally {
        $sha256.Dispose()
    }
    if ($fullPrefixHash -ne $rangeHash) {
        throw "Video Range bytes do not match the first 1024 bytes of the full download."
    }

    $reconciliationDeadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $reconciliation = $null
    do {
        $reconciliationSql = @"
select json_build_object(
    'status', r.status,
    'attempts', r.attempts,
    'upstream_task_id', r.upstream_task_id,
    'total_tokens', r.total_tokens,
    'supplier_price', r.supplier_price,
    'supplier_discount', r.supplier_discount,
    'supplier_amount_paid', r.supplier_amount_paid,
    'expense_time', r.expense_time,
    'pre_consumed_quota', r.pre_consumed_quota,
    'actual_quota', r.actual_quota,
    'quota_delta', r.quota_delta,
    'unit_price_per_million_tokens', t.private_data::jsonb #>> '{billing_context,provider_billing,unit_price_per_million_tokens}',
    'cny_per_usd', t.private_data::jsonb #>> '{billing_context,provider_billing,cny_per_usd}',
    'group_ratio', t.private_data::jsonb #>> '{billing_context,provider_billing,group_ratio}',
    'quota_per_unit', coalesce((select o.value from options o where o.key = 'QuotaPerUnit' limit 1), '500000'),
    'resolution', t.private_data::jsonb #>> '{billing_context,provider_billing,resolution}',
    'has_video_input', t.private_data::jsonb #>> '{billing_context,provider_billing,has_video_input}'
)::text
from task_billing_reconciliations r
join tasks t on t.id = r.task_id
where t.task_id = '$taskId';
"@
        $reconciliationRaw = ($reconciliationSql | & ssh @sshArgs | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) {
            throw "Unable to query the task billing reconciliation record."
        }
        if (-not [string]::IsNullOrWhiteSpace($reconciliationRaw)) {
            $reconciliation = $reconciliationRaw | ConvertFrom-Json
        }
        if ($null -eq $reconciliation -or [string]$reconciliation.status -ne "settled") {
            Start-Sleep -Seconds $PollIntervalSeconds
        }
    } while (($null -eq $reconciliation -or [string]$reconciliation.status -ne "settled") -and (Get-Date) -lt $reconciliationDeadline)
    if ($null -eq $reconciliation -or [string]$reconciliation.status -ne "settled" -or [long]$reconciliation.total_tokens -le 0) {
        throw "The task billing reconciliation did not settle before timeout."
    }
    $culture = [Globalization.CultureInfo]::InvariantCulture
    $unitPriceCny = [decimal]::Parse([string]$reconciliation.unit_price_per_million_tokens, $culture)
    $supplierPriceCny = [decimal]::Parse([string]$reconciliation.supplier_price, $culture)
    $cnyPerUsd = [decimal]::Parse([string]$reconciliation.cny_per_usd, $culture)
    $groupRatio = [decimal]::Parse([string]$reconciliation.group_ratio, $culture)
    $quotaPerUnit = [decimal]::Parse([string]$reconciliation.quota_per_unit, $culture)
    if ($unitPriceCny -ne [decimal]46 -or
        $supplierPriceCny -ne $unitPriceCny -or
        $cnyPerUsd -le 0 -or
        $groupRatio -le 0 -or
        $quotaPerUnit -le 0) {
        throw "The settled task did not preserve the expected 720p/no-video CNY price snapshot."
    }
    if ([string]$reconciliation.resolution -ne "720p" -or [string]$reconciliation.has_video_input -ne "false") {
        throw "The settled task billing snapshot does not match the submitted video specification."
    }
    $actualCostCny = [decimal]$reconciliation.total_tokens / [decimal]1000000 * $unitPriceCny
    $actualQuotaExact = $actualCostCny / $cnyPerUsd * $quotaPerUnit * $groupRatio
    $expectedActualQuota = [int][decimal]::Floor($actualQuotaExact + [decimal]0.5)
    $estimatedCostCny = [decimal]86400 / [decimal]1000000 * $unitPriceCny
    $preConsumedQuotaExact = $estimatedCostCny / $cnyPerUsd * $quotaPerUnit * $groupRatio
    $expectedPreConsumedQuota = [int][decimal]::Floor($preConsumedQuotaExact + [decimal]0.5)
    if ([int]$reconciliation.actual_quota -ne $expectedActualQuota) {
        throw "Actual quota does not match the frozen CNY price, exchange rate, group ratio, and total_tokens."
    }
    if ([int]$reconciliation.pre_consumed_quota -ne $expectedPreConsumedQuota) {
        throw "Pre-consumed quota does not match the frozen video estimate and billing snapshot."
    }
    if (([int]$reconciliation.actual_quota - [int]$reconciliation.pre_consumed_quota) -ne [int]$reconciliation.quota_delta) {
        throw "Quota delta does not equal actual_quota minus pre_consumed_quota."
    }
    $actualCostCny = [Math]::Round($actualCostCny, 6, [MidpointRounding]::AwayFromZero)

    $summary = [ordered]@{
        run_name = $runName
        base_url = $BaseUrl
        token_id = $TokenId
        group_id = $groupId
        asset_id = $assetId
        asset_status = $assetStatus
        task_id = $taskId
        task_status = $taskStatus
        callback_security = [ordered]@{
            cache_control = $callbackCacheControl
            referrer_policy = $callbackReferrerPolicy
            content_type_options = $callbackContentTypeOptions
            content_security_policy = $callbackContentSecurityPolicy
            query_token_reflected = $false
        }
        video_spec = [ordered]@{
            resolution = "720p"
            duration_seconds = 4
            ratio = "16:9"
            audio_status = 0
            has_video_input = $false
            estimated_tokens = 86400
            cny_per_million_tokens = 46
            estimated_cost_cny = 3.9744
            selection_basis = "lowest estimated token usage and pre-consume among public no-input-video examples"
        }
        content = [ordered]@{
            bytes = $contentInfo.Length
            sha256 = $contentHash
            first_16_bytes_hex = $prefixHex
            content_type = $contentType
            cache_control = $contentCacheControl
            headers_path = $contentHeadersPath
            file_path = $contentPath
        }
        range_content = [ordered]@{
            http_status = [int]$rangeHttp
            bytes = $rangeInfo.Length
            content_range = $rangeContentRange
            cache_control = $rangeCacheControl
            sha256 = $rangeHash
            headers_path = $rangeHeadersPath
            file_path = $rangePath
        }
        asset_negative = [ordered]@{
            http_status = $invalidAsset.HttpStatus
            error_code = [string]$invalidAsset.Body.error.code
            expected_error_code = $ExpectedInvalidAssetErrorCode
            set_cookie_exposed = $false
        }
        billing_reconciliation = [ordered]@{
            status = [string]$reconciliation.status
            attempts = [int]$reconciliation.attempts
            upstream_task_id = [string]$reconciliation.upstream_task_id
            total_tokens = [long]$reconciliation.total_tokens
            supplier_price = [string]$reconciliation.supplier_price
            supplier_discount = [string]$reconciliation.supplier_discount
            supplier_amount_paid = [string]$reconciliation.supplier_amount_paid
            expense_time = [string]$reconciliation.expense_time
            pre_consumed_quota = [int]$reconciliation.pre_consumed_quota
            actual_quota = [int]$reconciliation.actual_quota
            quota_delta = [int]$reconciliation.quota_delta
            unit_price_per_million_tokens = [string]$reconciliation.unit_price_per_million_tokens
            cny_per_usd = [string]$reconciliation.cny_per_usd
            group_ratio = [string]$reconciliation.group_ratio
            quota_per_unit = [string]$reconciliation.quota_per_unit
            expected_pre_consumed_quota = $expectedPreConsumedQuota
            expected_actual_quota = $expectedActualQuota
            actual_cost_cny = $actualCostCny
        }
        result_directory = $runDir
    }
    $summaryPath = Join-Path $runDir "summary.json"
    Write-Utf8Json -Path $summaryPath -Value $summary
    Write-Output "E2E_SUCCESS summary=$summaryPath task_id=$taskId asset_id=$assetId video_bytes=$($contentInfo.Length) sha256=$contentHash"
}
finally {
    $apiKey = $null
    $tokenValue = $null
}
