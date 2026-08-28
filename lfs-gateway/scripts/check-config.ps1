param(
    [string]$EnvFile = "../gitea/env"
)

$ErrorActionPreference = "Stop"

if (!(Test-Path -LiteralPath $EnvFile)) {
    throw "env file not found: $EnvFile"
}

$values = @{}
Get-Content -LiteralPath $EnvFile | ForEach-Object {
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

$required = @(
    "GITEA_SECRET_KEY",
    "LFS_PUBLIC_URL",
    "LFS_VERIFY_SECRET",
    "LFS_META_DB_DSN",
    "OSS_ENDPOINT",
    "OSS_BUCKET",
    "OSS_KEY_PREFIX",
    "OSS_KEY_STYLE",
    "CDN_BASE_URL",
    "CDN_AUTH_KEY",
    "GITEA_ATTACHMENT_OSS_ENDPOINT",
    "GITEA_ATTACHMENT_OSS_LOCATION",
    "RELEASE_DIRECT_UPLOAD",
    "RELEASE_ATTACHMENT_OSS_PREFIX",
    "RELEASE_PENDING_OSS_PREFIX",
    "RELEASE_MAX_FILE_SIZE_MB",
    "RELEASE_MAX_FILES"
)

$failed = $false
foreach ($key in $required) {
    if (!$values.ContainsKey($key) -or [string]::IsNullOrWhiteSpace($values[$key])) {
        Write-Host "missing: $key"
        $failed = $true
        continue
    }
    if ($values[$key] -match "change-me|change-this|your-|example\.com|replace-with") {
        Write-Host "placeholder: $key=$($values[$key])"
        $failed = $true
    }
}

if ($values.ContainsKey("OSS_KEY_STYLE") -and $values["OSS_KEY_STYLE"] -notin @("repo", "gitea")) {
    Write-Host "invalid: OSS_KEY_STYLE must be repo or gitea"
    $failed = $true
}

if ($values.ContainsKey("GITEA_ATTACHMENT_OSS_ENDPOINT") -and $values["GITEA_ATTACHMENT_OSS_ENDPOINT"] -notmatch '^s3\.oss-[a-z0-9-]+\.aliyuncs\.com$') {
    Write-Host "invalid: GITEA_ATTACHMENT_OSS_ENDPOINT must be an Aliyun S3-compatible endpoint without scheme"
    $failed = $true
}

if ($values.ContainsKey("RELEASE_DIRECT_UPLOAD") -and $values["RELEASE_DIRECT_UPLOAD"] -notin @("true", "false")) {
    Write-Host "invalid: RELEASE_DIRECT_UPLOAD must be true or false"
    $failed = $true
}

foreach ($key in @("RELEASE_MAX_FILE_SIZE_MB", "RELEASE_MAX_FILES")) {
    if ($values.ContainsKey($key)) {
        $number = 0L
        if (![int64]::TryParse($values[$key], [ref]$number) -or $number -le 0) {
            Write-Host "invalid: $key must be a positive integer"
            $failed = $true
        }
    }
}

$composePath = Join-Path (Split-Path -Parent $EnvFile) "docker-compose.yml"
if (Test-Path -LiteralPath $composePath) {
    $compose = Get-Content -Raw -LiteralPath $composePath
    if ($compose -notmatch '\$\{ALIYUN_OSS_ACCESS_KEY_ID\}') {
        Write-Host "missing compose passthrough: ALIYUN_OSS_ACCESS_KEY_ID"
        $failed = $true
    }
    if ($compose -notmatch '\$\{ALIYUN_OSS_ACCESS_KEY_SECRET\}') {
        Write-Host "missing compose passthrough: ALIYUN_OSS_ACCESS_KEY_SECRET"
        $failed = $true
    }
    if ($compose -notmatch 'MINIO_ACCESS_KEY:\s*\$\{ALIYUN_OSS_ACCESS_KEY_ID\}') {
        Write-Host "missing Gitea credential mapping: MINIO_ACCESS_KEY"
        $failed = $true
    }
    if ($compose -notmatch 'MINIO_SECRET_KEY:\s*\$\{ALIYUN_OSS_ACCESS_KEY_SECRET\}') {
        Write-Host "missing Gitea credential mapping: MINIO_SECRET_KEY"
        $failed = $true
    }
}

if ($failed) {
    throw "configuration check failed"
}

"configuration check ok; OSS AccessKey values are expected from host env ALIYUN_OSS_ACCESS_KEY_ID/ALIYUN_OSS_ACCESS_KEY_SECRET"
