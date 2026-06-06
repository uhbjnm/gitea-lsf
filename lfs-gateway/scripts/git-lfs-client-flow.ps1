$ErrorActionPreference = "Stop"

$port = 18081
$repo = Join-Path $env:TEMP ("git-lfs-client-test-" + [guid]::NewGuid().ToString("N"))
$stdoutLog = Join-Path $env:TEMP "git-lfs-fake-gateway.stdout.log"
$stderrLog = Join-Path $env:TEMP "git-lfs-fake-gateway.stderr.log"
$server = $null

New-Item -ItemType Directory -Force -Path $repo | Out-Null
Remove-Item -LiteralPath $stdoutLog -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $stderrLog -Force -ErrorAction SilentlyContinue
Get-NetTCPConnection -LocalPort $port -ErrorAction SilentlyContinue |
    Select-Object -ExpandProperty OwningProcess -Unique |
    ForEach-Object { Stop-Process -Id $_ -Force -ErrorAction SilentlyContinue }

try {
    $server = Start-Process `
        -FilePath "go" `
        -ArgumentList @("run", "./testdata/fake-git-lfs-server", "-addr", "127.0.0.1:$port") `
        -PassThru `
        -WindowStyle Hidden `
        -RedirectStandardOutput $stdoutLog `
        -RedirectStandardError $stderrLog

    Start-Sleep -Seconds 3

    git -C $repo init | Out-Null
    git -C $repo config user.email test@example.com
    git -C $repo config user.name Test
    git -C $repo remote add origin "http://127.0.0.1:$port/acme/demo.git"
    git -C $repo lfs install --local | Out-Null
    git -C $repo config lfs.url "http://127.0.0.1:$port/acme/demo.git/info/lfs"
    git -C $repo lfs track "*.bin" | Out-Null

    Set-Content `
        -Encoding ASCII `
        -NoNewline `
        -LiteralPath (Join-Path $repo "large.bin") `
        -Value ("hello git lfs direct flow" * 1000)

    git -C $repo add .gitattributes large.bin
    git -C $repo commit -m "lfs test" | Out-Null

    $pointer = git -C $repo lfs pointer --file large.bin
    $oid = ($pointer | Select-String -Pattern "oid sha256:([0-9a-f]+)").Matches[0].Groups[1].Value
    if (-not $oid) {
        throw "failed to parse LFS oid"
    }

    git -C $repo lfs push --object-id origin $oid
    if ($LASTEXITCODE -ne 0) {
        throw "git lfs push failed"
    }

    $objectDir = Join-Path $repo (".git\lfs\objects\" + $oid.Substring(0, 2) + "\" + $oid.Substring(2, 2))
    Remove-Item -LiteralPath $objectDir -Recurse -Force

    git -C $repo lfs fetch origin HEAD
    if ($LASTEXITCODE -ne 0) {
        git -C $repo lfs logs last
        throw "git lfs fetch failed"
    }

    $objectPath = Join-Path $objectDir $oid
    if (!(Test-Path -LiteralPath $objectPath)) {
        throw "downloaded LFS object missing"
    }

    $length = (Get-Item -LiteralPath $objectPath).Length
    "client lfs upload/download flow ok oid=$oid length=$length"
    Get-Content -Raw -LiteralPath $stdoutLog -ErrorAction SilentlyContinue
    Get-Content -Raw -LiteralPath $stderrLog -ErrorAction SilentlyContinue
} finally {
    if ($server -and !$server.HasExited) {
        Stop-Process -Id $server.Id -Force
    }
    Remove-Item -LiteralPath $repo -Recurse -Force -ErrorAction SilentlyContinue
}
