param(
    [string]$ComposeDir = "../gitea",
    [string]$EnvFile = "env"
)

$ErrorActionPreference = "Stop"

$composePath = Resolve-Path -LiteralPath $ComposeDir
$envPath = Join-Path $composePath $EnvFile
if (!(Test-Path -LiteralPath $envPath)) {
    throw "env file not found: $envPath"
}

$values = @{}
Get-Content -LiteralPath $envPath | ForEach-Object {
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

$dbUser = if ($values.ContainsKey("POSTGRES_USER")) { $values["POSTGRES_USER"] } else { "gitea" }
$dbName = if ($values.ContainsKey("POSTGRES_DB")) { $values["POSTGRES_DB"] } else { "gitea" }

$query = @"
WITH cols AS (
    SELECT COUNT(*) AS n
    FROM information_schema.columns
    WHERE table_name = 'lfs_meta_object'
      AND column_name IN ('oid', 'size', 'repository_id', 'created_unix', 'updated_unix')
), uniq AS (
    SELECT COUNT(*) AS n
    FROM pg_indexes
    WHERE tablename = 'lfs_meta_object'
      AND indexdef ILIKE '%UNIQUE%'
      AND indexdef ILIKE '%repository_id%'
      AND indexdef ILIKE '%oid%'
)
SELECT
    (SELECT n FROM cols) AS required_columns,
    (SELECT n FROM uniq) AS unique_indexes;
"@

$result = try {
    Push-Location $composePath
    docker compose --env-file $EnvFile exec -T postgres `
        psql -U $dbUser -d $dbName -At -F "," -c $query
} finally {
    Pop-Location
}

if ($LASTEXITCODE -ne 0) {
    throw "failed to query postgres"
}

$parts = $result.Trim().Split(",")
if ($parts.Count -ne 2) {
    throw "unexpected psql output: $result"
}

$columns = [int]$parts[0]
$indexes = [int]$parts[1]

if ($columns -ne 5) {
    throw "lfs_meta_object schema check failed: required_columns=$columns"
}
if ($indexes -lt 1) {
    throw "lfs_meta_object schema check failed: missing unique index on repository_id + oid"
}

"lfs_meta_object schema ok"
