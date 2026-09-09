[CmdletBinding()]
param(
    [string]$BaseUrl = "https://124.174.0.221",
    [string]$SshHost = "124.174.0.221",
    [string]$SshUser = "root",
    [string]$SshKeyPath = "D:\codex\BPlatform.pem",
    [int]$TokenId = 1,
    [string]$TokenName = "seedance-assets-production",
    [string[]]$AllowedGatewayHosts = @("124.174.0.221", "gateway.nexus-reach.com"),
    [ValidateSet("zh", "en", "zh-Hant")][string]$Language = "zh",
    [string]$AllowedH5Host = "h5-v2.kych5.com",
    [string]$BrowserExecutable = "msedge.exe",
    [string]$BrowserPrivateArgument = "--inprivate",
    [ValidateRange(30, 105)][int]$SafeActionWindowSeconds = 100,
    [string]$ResultRoot = ".deploy\seedance-visual-e2e",
    [switch]$PreflightOnly,
    [switch]$AuthorizedPersonReady
)

$ErrorActionPreference = "Stop"
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

if (-not $AuthorizedPersonReady -and -not $PreflightOnly) {
    throw "An authorized person with a working camera must be ready. Re-run with -AuthorizedPersonReady only after they consent to the real-time liveness check."
}

try {
    $gatewayUri = [Uri]$BaseUrl
}
catch {
    throw "BaseUrl must be an absolute HTTPS gateway URL."
}
if (-not $gatewayUri.IsAbsoluteUri -or
    $gatewayUri.Scheme -ne "https" -or
    $gatewayUri.Port -ne 443 -or
    $AllowedGatewayHosts -notcontains $gatewayUri.Host -or
    -not [string]::IsNullOrEmpty($gatewayUri.UserInfo) -or
    -not [string]::IsNullOrEmpty($gatewayUri.Query) -or
    -not [string]::IsNullOrEmpty($gatewayUri.Fragment) -or
    $gatewayUri.AbsolutePath -ne "/") {
    throw "BaseUrl must be an approved HTTPS gateway origin with no path, credentials, query, or fragment."
}
$BaseUrl = $gatewayUri.GetLeftPart([UriPartial]::Authority)
if (-not (Test-Path -LiteralPath $SshKeyPath -PathType Leaf)) {
    throw "The SSH private key file does not exist."
}

$browserPath = $BrowserExecutable
if (-not (Test-Path -LiteralPath $browserPath -PathType Leaf)) {
    $browserCommand = Get-Command $BrowserExecutable -ErrorAction SilentlyContinue
    if ($null -ne $browserCommand) {
        $browserPath = $browserCommand.Source
    }
    elseif ($BrowserExecutable -eq "msedge.exe") {
        $edgeCandidates = @(
            "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe",
            "$env:ProgramFiles\Microsoft\Edge\Application\msedge.exe",
            "$env:LOCALAPPDATA\Microsoft\Edge\Application\msedge.exe"
        )
        $browserPath = $edgeCandidates |
            Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
            Select-Object -First 1
    }
}
if ([string]::IsNullOrWhiteSpace([string]$browserPath) -or
    -not (Test-Path -LiteralPath $browserPath -PathType Leaf)) {
    throw "The private browser executable could not be resolved."
}

function Invoke-GatewayJson {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$Url,
        [Parameter(Mandatory = $true)][hashtable]$Headers,
        [Parameter(Mandatory = $true)]$Body,
        [ValidateRange(5, 30)][int]$TimeoutSeconds = 30
    )

    $requestJson = $Body | ConvertTo-Json -Depth 20 -Compress
    try {
        $response = Invoke-WebRequest `
            -UseBasicParsing `
            -Method POST `
            -Uri $Url `
            -Headers $Headers `
            -ContentType "application/json" `
            -Body ([Text.Encoding]::UTF8.GetBytes($requestJson)) `
            -MaximumRedirection 0 `
            -TimeoutSec $TimeoutSeconds
    }
    catch {
        throw "$Name failed at the HTTPS layer; no sensitive response was persisted."
    }

    try {
        $responseBody = [string]$response.Content | ConvertFrom-Json
    }
    catch {
        throw "$Name returned invalid JSON; no sensitive response was persisted."
    }
    finally {
        $requestJson = $null
    }

    return [pscustomobject]@{
        HttpStatus = [int]$response.StatusCode
        Body = $responseBody
    }
}

function Open-SensitiveUrlInPrivateBrowser {
    param(
        [Parameter(Mandatory = $true)][string]$SensitiveUrl,
        [Parameter(Mandatory = $true)][string]$ExecutablePath,
        [Parameter(Mandatory = $true)][string]$PrivateArgument
    )

    if ($SensitiveUrl.IndexOfAny([char[]]"`r`n") -ge 0) {
        throw "The sensitive browser URL contains invalid header characters."
    }

    $listener = $null
    $client = $null
    $stream = $null
    $requestText = $null
    $responseText = $null
    $responseBytes = $null
    $navigationStopwatch = $null
    try {
        $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
        $listener.Start()
        $port = ([Net.IPEndPoint]$listener.LocalEndpoint).Port
        $noncePath = "/$([Guid]::NewGuid().ToString('N'))"
        $navigationUrl = "http://127.0.0.1:$port$noncePath"
        $navigationStopwatch = [Diagnostics.Stopwatch]::StartNew()

        Start-Process `
            -FilePath $ExecutablePath `
            -ArgumentList @($PrivateArgument, "--new-window", $navigationUrl)

        while ($navigationStopwatch.Elapsed.TotalSeconds -lt 10) {
            if (-not $listener.Pending()) {
                Start-Sleep -Milliseconds 50
                continue
            }

            $client = $listener.AcceptTcpClient()
            $client.ReceiveTimeout = 2000
            $client.SendTimeout = 2000
            $stream = $client.GetStream()
            $requestBuffer = New-Object byte[] 8192
            $requestLength = 0
            while ($requestLength -lt $requestBuffer.Length) {
                $read = $stream.Read(
                    $requestBuffer,
                    $requestLength,
                    $requestBuffer.Length - $requestLength
                )
                if ($read -le 0) {
                    break
                }
                $requestLength += $read
                $requestText = [Text.Encoding]::ASCII.GetString(
                    $requestBuffer,
                    0,
                    $requestLength
                )
                if ($requestText.Contains("`r`n`r`n")) {
                    break
                }
            }

            $expectedRequestLine = "GET $noncePath HTTP/1.1"
            $requestLine = if ($null -ne $requestText) {
                ($requestText -split "`r`n", 2)[0]
            }
            else {
                ""
            }
            if ($requestLine -eq $expectedRequestLine) {
                $responseText = "HTTP/1.1 302 Found`r`n" +
                    "Location: $SensitiveUrl`r`n" +
                    "Cache-Control: no-store, private`r`n" +
                    "Pragma: no-cache`r`n" +
                    "Referrer-Policy: no-referrer`r`n" +
                    "Content-Length: 0`r`n" +
                    "Connection: close`r`n`r`n"
                $responseBytes = [Text.Encoding]::ASCII.GetBytes($responseText)
                $stream.Write($responseBytes, 0, $responseBytes.Length)
                $stream.Flush()
                return
            }

            $responseText = "HTTP/1.1 404 Not Found`r`n" +
                "Cache-Control: no-store`r`n" +
                "Content-Length: 0`r`n" +
                "Connection: close`r`n`r`n"
            $responseBytes = [Text.Encoding]::ASCII.GetBytes($responseText)
            $stream.Write($responseBytes, 0, $responseBytes.Length)
            $stream.Flush()
            $stream.Dispose()
            $client.Dispose()
            $stream = $null
            $client = $null
            $requestText = $null
            $responseText = $null
            $responseBytes = $null
        }

        throw "The private browser did not request the one-time local navigation URL."
    }
    finally {
        if ($null -ne $navigationStopwatch) {
            $navigationStopwatch.Stop()
        }
        if ($null -ne $stream) {
            $stream.Dispose()
        }
        if ($null -ne $client) {
            $client.Dispose()
        }
        if ($null -ne $listener) {
            $listener.Stop()
        }
        if ($null -ne $responseBytes) {
            [Array]::Clear($responseBytes, 0, $responseBytes.Length)
        }
        if ($null -ne $requestBuffer) {
            [Array]::Clear($requestBuffer, 0, $requestBuffer.Length)
        }
        $SensitiveUrl = $null
        $requestText = $null
        $responseText = $null
        $responseBytes = $null
    }
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
$gatewayHeaders = @{ Authorization = "Bearer $apiKey" }
$callbackUrl = "$BaseUrl/apidocs/apidocs-example-callback.html"
$createUrl = "$BaseUrl/api/v3/open/CreateVisualValidateSession"
$resultUrl = "$BaseUrl/api/v3/open/GetVisualValidateResult"
$bytedToken = $null
$h5Link = $null
$resultRequest = $null
$session = $null
$visualResult = $null
$sessionStopwatch = $null
$groupId = $null

try {
    if ($PreflightOnly) {
        Write-Host "Running zero-cost callback and invalid-token preflight. No liveness session or video will be created."
    }
    else {
        Write-Host "This check opens the supplier's real-time liveness page. It does not create a video."
        Write-Host "Do not continue unless the person has consented and can finish with a camera immediately."
    }

    $callbackProbeToken = "non-secret-probe-$([Guid]::NewGuid().ToString('N'))"
    try {
        $callbackResponse = Invoke-WebRequest `
            -UseBasicParsing `
            -Method GET `
            -Uri "$callbackUrl`?bytedToken=$callbackProbeToken&resultCode=0" `
            -MaximumRedirection 0 `
            -TimeoutSec 15
    }
    catch {
        throw "The callback security preflight failed at the HTTPS layer."
    }
    if ([int]$callbackResponse.StatusCode -ne 200 -or
        [string]$callbackResponse.Headers["Cache-Control"] -ne "no-store" -or
        [string]$callbackResponse.Headers["Referrer-Policy"] -ne "no-referrer" -or
        [string]$callbackResponse.Headers["X-Content-Type-Options"] -ne "nosniff" -or
        [string]$callbackResponse.Headers["Content-Security-Policy"] -notmatch "default-src 'none'" -or
        $null -ne $callbackResponse.Headers["Set-Cookie"] -or
        [string]$callbackResponse.Content -match [Regex]::Escape($callbackProbeToken)) {
        throw "The callback security preflight did not meet the no-store, no-referrer, CSP, and non-reflection contract."
    }
    $callbackResponse = $null
    $callbackProbeToken = $null

    if ($PreflightOnly) {
        $invalidResult = Invoke-GatewayJson `
            -Name "GetVisualValidateResult preflight" `
            -Url $resultUrl `
            -Headers $gatewayHeaders `
            -Body ([ordered]@{ BytedToken = "invalid-preflight-$([Guid]::NewGuid().ToString('N'))" }) `
            -TimeoutSeconds 8
        if ($invalidResult.HttpStatus -ne 200 -or
            $invalidResult.Body.state -ne 0 -or
            $null -ne $invalidResult.Body.data -or
            $null -eq $invalidResult.Body.error) {
            throw "The invalid-token preflight did not preserve the expected HTTP 200 business-error contract."
        }
        $invalidResult = $null
        Write-Output "VISUAL_E2E_PREFLIGHT_OK callback_security=true invalid_token_contract=true"
        return
    }

    $sessionStopwatch = [Diagnostics.Stopwatch]::StartNew()
    $session = Invoke-GatewayJson `
        -Name "CreateVisualValidateSession" `
        -Url $createUrl `
        -Headers $gatewayHeaders `
        -Body ([ordered]@{ CallbackURL = $callbackUrl })
    if ($session.HttpStatus -ne 200 -or
        $session.Body.state -ne 1 -or
        $null -eq $session.Body.data.Result -or
        $null -ne $session.Body.error) {
        throw "CreateVisualValidateSession did not return a business-success response."
    }

    $bytedToken = [string]$session.Body.data.Result.BytedToken
    $h5Link = [string]$session.Body.data.Result.H5Link
    if ([string]::IsNullOrWhiteSpace($bytedToken) -or [string]::IsNullOrWhiteSpace($h5Link)) {
        throw "CreateVisualValidateSession did not return BytedToken and H5Link."
    }
    $returnedCallbackUrl = [string]$session.Body.data.Result.CallbackURL
    if ($returnedCallbackUrl -ne $callbackUrl) {
        throw "CreateVisualValidateSession returned an unexpected CallbackURL."
    }
    try {
        $h5Uri = [Uri]$h5Link
    }
    catch {
        throw "CreateVisualValidateSession returned an invalid H5Link."
    }
    if ($h5Link.IndexOfAny([char[]]"`r`n") -ge 0 -or
        -not $h5Uri.IsAbsoluteUri -or
        $h5Uri.Scheme -ne "https" -or
        $h5Uri.Port -ne 443 -or
        $h5Uri.Host -ne $AllowedH5Host -or
        -not [string]::IsNullOrEmpty($h5Uri.UserInfo)) {
        throw "CreateVisualValidateSession returned an H5Link outside the approved HTTPS host."
    }
    if ($h5Link -notmatch '([?&])lng=') {
        $separator = if ($h5Link.Contains("?")) { "&" } else { "?" }
        $h5Link = "$h5Link$separator`lng=$Language"
    }
    $session.Body.data.Result.BytedToken = $null

    Open-SensitiveUrlInPrivateBrowser `
        -SensitiveUrl $h5Link `
        -ExecutablePath $browserPath `
        -PrivateArgument $BrowserPrivateArgument
    $session.Body.data.Result.H5Link = $null
    $h5Link = $null
    $h5Uri = $null

    Write-Host "The liveness page has opened in a private browser window."
    Write-Host "Confirm resultCode=10000, algorithmBaseRespCode=0, and verify_type=real_time on the callback page."
    $confirmation = Read-Host "Type DONE only after all three callback checks pass"
    if ($confirmation.Trim().ToUpperInvariant() -ne "DONE") {
        throw "The authorized person did not confirm a successful callback. The one-time session will be allowed to expire."
    }
    if ($sessionStopwatch.Elapsed.TotalSeconds -ge $SafeActionWindowSeconds) {
        throw "The safe action window expired. Create a new session instead of using the old token."
    }

    $resultRequest = [ordered]@{ BytedToken = $bytedToken }
    $visualResult = Invoke-GatewayJson `
        -Name "GetVisualValidateResult" `
        -Url $resultUrl `
        -Headers $gatewayHeaders `
        -Body $resultRequest `
        -TimeoutSeconds 8
    if ($visualResult.HttpStatus -ne 200 -or
        $visualResult.Body.state -ne 1 -or
        $null -eq $visualResult.Body.data -or
        $null -ne $visualResult.Body.error) {
        throw "GetVisualValidateResult did not return a business-success response."
    }

    $groupId = [string]$visualResult.Body.data.GroupId
    if ([string]::IsNullOrWhiteSpace($groupId)) {
        throw "GetVisualValidateResult did not return data.GroupId."
    }
    $visualResult.Body.data.GroupId = $null

    $completedAt = Get-Date
    $sessionStopwatch.Stop()
    $runName = $completedAt.ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
    $runDir = Join-Path $ResultRoot $runName
    New-Item -ItemType Directory -Force -Path $runDir | Out-Null
    $summary = [ordered]@{
        completed_at_utc = $completedAt.ToUniversalTime().ToString("o")
        elapsed_seconds = [Math]::Round($sessionStopwatch.Elapsed.TotalSeconds, 3)
        base_url = $BaseUrl
        token_id = $TokenId
        create = [ordered]@{
            method = "POST"
            public_url = $createUrl
            upstream_url = "https://api.laomandi.com/asset/SdToolApi/CreateVisualValidateSession"
            request = [ordered]@{ CallbackURL = $callbackUrl }
            http_status = $session.HttpStatus
            response = [ordered]@{
                state = $session.Body.state
                data = [ordered]@{
                    ResponseMetadata = [ordered]@{
                        RequestId = "<redacted>"
                        Action = [string]$session.Body.data.ResponseMetadata.Action
                        Version = [string]$session.Body.data.ResponseMetadata.Version
                        Service = [string]$session.Body.data.ResponseMetadata.Service
                        Region = [string]$session.Body.data.ResponseMetadata.Region
                    }
                    Result = [ordered]@{
                        BytedToken = "<redacted>"
                        H5Link = "<redacted>"
                        CallbackURL = $returnedCallbackUrl
                    }
                }
                error = $null
            }
        }
        callback = [ordered]@{
            required_result_code = 10000
            credential_source = "CreateVisualValidateSession.data.Result.BytedToken retained in memory"
            credential_persisted = $false
            biometric_data_persisted = $false
        }
        get_result = [ordered]@{
            method = "POST"
            public_url = $resultUrl
            upstream_url = "https://api.laomandi.com/asset/SdToolApi/GetVisualValidateResult"
            request = [ordered]@{ BytedToken = "<redacted>" }
            http_status = $visualResult.HttpStatus
            response = [ordered]@{
                state = $visualResult.Body.state
                data = [ordered]@{
                    GroupId = "<redacted>"
                    GroupIdPresent = $true
                    GroupIdLength = $groupId.Length
                }
                error = $null
            }
        }
    }
    $summaryPath = Join-Path $runDir "summary.json"
    [IO.File]::WriteAllText(
        $summaryPath,
        ($summary | ConvertTo-Json -Depth 30),
        $utf8NoBom
    )

    Write-Output "VISUAL_E2E_SUCCESS summary=$summaryPath group_id_present=true"
}
finally {
    if ($null -ne $sessionStopwatch -and $sessionStopwatch.IsRunning) {
        $sessionStopwatch.Stop()
    }
    if ($null -ne $session -and $null -ne $session.Body.data.Result) {
        $session.Body.data.Result.BytedToken = $null
        $session.Body.data.Result.H5Link = $null
    }
    $resultRequest = $null
    $bytedToken = $null
    $h5Link = $null
    $h5Uri = $null
    $session = $null
    $visualResult = $null
    $groupId = $null
    $sessionStopwatch = $null
    $browserPath = $null
    $callbackResponse = $null
    $callbackProbeToken = $null
    $invalidResult = $null
    $gatewayHeaders = $null
    $apiKey = $null
    $tokenValue = $null
}
