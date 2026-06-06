param(
    [Parameter(Mandatory = $true)]
    [string]$RemoteUrl,

    [string]$Branch = "lfs-gateway-test",
    [string]$EnvFile = "../gitea/env",
    [string]$ExpectedUploadHost = "",
    [string]$ExpectedDownloadHost = "",
    [string]$LfsUrl = "",
    [int]$SizeKB = 1024,
    [switch]$KeepRepo
)

$ErrorActionPreference = "Stop"

function Read-EnvFile([string]$Path) {
    $values = @{}
    if (!(Test-Path -LiteralPath $Path)) {
        return $values
    }
    Get-Content -LiteralPath $Path | ForEach-Object {
        $line = $_.Trim()
        if ($line -eq "" -or $line.StartsWith("#")) {
            return
        }
        $idx = $line.IndexOf("=")
        if ($idx -lt 1) {
            return
        }
        $values[$line.Substring(0, $idx)] = $line.Substring($idx + 1)
    }
    return $values
}

function Host-FromUrl([string]$Value) {
    if ([string]::IsNullOrWhiteSpace($Value)) {
        return ""
    }
    try {
        return ([Uri]$Value).Host
    } catch {
        return ""
    }
}

function Assert-TraceHostMethod([string]$Log, [string]$Method, [string]$Host, [string]$Label, [string]$LogPath) {
    $methodPattern = [regex]::Escape($Method)
    $hostPattern = [regex]::Escape($Host)
    $patterns = @(
        "$methodPattern\s+https?://$hostPattern[:/]",
        ">\s*$methodPattern\s+/.+`r?`n>\s*Host:\s*$hostPattern",
        "=> Send header: $methodPattern\s+/.+`r?`n=> Send header: Host:\s*$hostPattern"
    )
    foreach ($pattern in $patterns) {
        if ($Log -match $pattern) {
            return
        }
    }
    throw "trace did not contain $Label data-plane request: $Method $Host. Log: $LogPath"
}

function Invoke-Logged([string]$Title, [scriptblock]$Command, [string]$LogPath) {
    "===== $Title =====" | Tee-Object -FilePath $LogPath -Append | Out-Null
    $oldTrace = $env:GIT_TRACE
    $oldCurl = $env:GIT_CURL_VERBOSE
    try {
        $env:GIT_TRACE = "1"
        $env:GIT_CURL_VERBOSE = "1"
        & $Command 2>&1 | Tee-Object -FilePath $LogPath -Append
        if ($LASTEXITCODE -ne 0) {
            throw "$Title failed with exit code $LASTEXITCODE"
        }
    } finally {
        $env:GIT_TRACE = $oldTrace
        $env:GIT_CURL_VERBOSE = $oldCurl
    }
}

$envValues = Read-EnvFile $EnvFile
if ($ExpectedDownloadHost -eq "" -and $envValues.ContainsKey("CDN_BASE_URL")) {
    $ExpectedDownloadHost = Host-FromUrl $envValues["CDN_BASE_URL"]
}
if ($ExpectedUploadHost -eq "" -and $envValues.ContainsKey("OSS_ENDPOINT")) {
    $endpointHost = Host-FromUrl $envValues["OSS_ENDPOINT"]
    if ($envValues.ContainsKey("OSS_BUCKET") -and $envValues["OSS_BUCKET"] -notmatch "change-me|your-") {
        $ExpectedUploadHost = "$($envValues["OSS_BUCKET"]).$endpointHost"
    } else {
        $ExpectedUploadHost = $endpointHost
    }
}

if ($ExpectedUploadHost -match "change-me|your-|example\.com" -or [string]::IsNullOrWhiteSpace($ExpectedUploadHost)) {
    throw "ExpectedUploadHost is required. Fill OSS settings in env or pass -ExpectedUploadHost."
}
if ($ExpectedDownloadHost -match "change-me|your-|example\.com" -or [string]::IsNullOrWhiteSpace($ExpectedDownloadHost)) {
    throw "ExpectedDownloadHost is required. Fill CDN_BASE_URL in env or pass -ExpectedDownloadHost."
}

$repo = Join-Path $env:TEMP ("gitea-lfs-live-" + [guid]::NewGuid().ToString("N"))
$logPath = Join-Path $repo "git-lfs-live-flow.log"

try {
    New-Item -ItemType Directory -Force -Path $repo | Out-Null
    git -C $repo init | Out-Null
    git -C $repo config user.email "lfs-gateway-test@example.com"
    git -C $repo config user.name "LFS Gateway Test"
    git -C $repo remote add origin $RemoteUrl
    git -C $repo lfs install --local | Out-Null
    if ($LfsUrl -ne "") {
        git -C $repo config lfs.url $LfsUrl
    }
    git -C $repo lfs track "*.bin" | Out-Null

    $objectPath = Join-Path $repo "lfs-gateway-live.bin"
    $block = "gitea-lfs-gateway-live-check`n"
    $repeatCount = [Math]::Ceiling(($SizeKB * 1024) / $block.Length)
    Set-Content -Encoding ASCII -NoNewline -LiteralPath $objectPath -Value ($block * $repeatCount)

    git -C $repo add .gitattributes lfs-gateway-live.bin
    git -C $repo commit -m "test lfs gateway direct flow" | Out-Null

    Invoke-Logged "git push" {
        git -C $repo push origin "HEAD:refs/heads/$Branch"
    } $logPath

    $pointer = git -C $repo lfs pointer --file lfs-gateway-live.bin
    $oid = ($pointer | Select-String -Pattern "oid sha256:([0-9a-f]+)").Matches[0].Groups[1].Value
    if (-not $oid) {
        throw "failed to parse LFS oid"
    }

    $objectDir = Join-Path $repo (".git\lfs\objects\" + $oid.Substring(0, 2) + "\" + $oid.Substring(2, 2))
    Remove-Item -LiteralPath $objectDir -Recurse -Force -ErrorAction SilentlyContinue

    Invoke-Logged "git lfs fetch" {
        git -C $repo lfs fetch origin $Branch
    } $logPath

    $log = Get-Content -Raw -LiteralPath $logPath
    Assert-TraceHostMethod $log "PUT" $ExpectedUploadHost "OSS upload" $logPath
    Assert-TraceHostMethod $log "GET" $ExpectedDownloadHost "CDN download" $logPath
    if ($log -notmatch "auth_key=") {
        throw "trace did not contain CDN auth_key. Log: $logPath"
    }

    "live lfs flow ok branch=$Branch oid=$oid log=$logPath"
} finally {
    if ($KeepRepo) {
        "kept repo: $repo"
    } else {
        Remove-Item -LiteralPath $repo -Recurse -Force -ErrorAction SilentlyContinue
    }
}
