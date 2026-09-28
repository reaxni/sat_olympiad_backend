param(
    [string]$DatabaseName = 'sat_olympiad',
    [string]$PgUser = 'postgres',
    [ValidateSet('127.0.0.1', 'localhost')][string]$PgHost = '127.0.0.1',
    [ValidateRange(1, 65535)][int]$PgPort = 5432,
    [ValidateRange(1, 65535)][int]$BackendPort = 8080,
    [string]$PostgresBin,
    [string]$StateDirectory = (Join-Path $PSScriptRoot '.local'),
    [switch]$ResetCredentials,
    [switch]$ResetSchedule,
    [switch]$SetupOnly
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Require-Success([string]$Description) {
    if ($LASTEXITCODE -ne 0) { throw "$Description failed. Check the PostgreSQL username, password, and port." }
}
function Read-Encrypted([string]$Path) {
    $encrypted = Get-Content -LiteralPath $Path -Raw
    $secure = ConvertTo-SecureString $encrypted
    return [System.Net.NetworkCredential]::new('', $secure).Password
}
function Write-Encrypted([string]$Path, [string]$Value) {
    $secure = ConvertTo-SecureString -String $Value -AsPlainText -Force
    ConvertFrom-SecureString $secure | Set-Content -LiteralPath $Path -NoNewline
}

if ($DatabaseName -notmatch '^[a-z][a-z0-9_]{0,62}$') { throw 'DatabaseName must use lowercase letters, digits, and underscores.' }
if ($PgUser -notmatch '^[A-Za-z_][A-Za-z0-9_]{0,62}$') { throw 'PgUser contains unsupported characters.' }
if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw 'Go is not installed or is not on PATH.' }

if (-not $PostgresBin) {
    $service = Get-Service -Name 'postgresql-x64-*' -ErrorAction SilentlyContinue | Sort-Object Name -Descending | Select-Object -First 1
    if (-not $service) { throw 'No PostgreSQL Windows service was found. Install PostgreSQL or pass -PostgresBin.' }
    $version = $service.Name -replace '^postgresql-x64-', ''
    $PostgresBin = Join-Path (Join-Path 'C:\Program Files\PostgreSQL' $version) 'bin'
    if ($service.Status -ne 'Running') {
        Write-Host "Starting PostgreSQL service $($service.Name)..."
        try { Start-Service -Name $service.Name } catch { throw "Could not start $($service.Name). Open PowerShell as Administrator, start the service, then run this script again." }
    }
}
$psql = Join-Path $PostgresBin 'psql.exe'
$createdb = Join-Path $PostgresBin 'createdb.exe'
if (-not (Test-Path -LiteralPath $psql) -or -not (Test-Path -LiteralPath $createdb)) { throw "PostgreSQL command-line tools were not found in $PostgresBin." }

New-Item -ItemType Directory -Force -Path $StateDirectory | Out-Null
$passwordFile = Join-Path $StateDirectory 'pg-password.dpapi'
$secretFile = Join-Path $StateDirectory 'auth-secret.dpapi'
$scheduleFile = Join-Path $StateDirectory 'schedule.json'

if ($ResetCredentials -and (Test-Path -LiteralPath $passwordFile)) { Remove-Item -LiteralPath $passwordFile -Force }
if (Test-Path Env:SAT_LOCAL_PG_PASSWORD) {
    $password = $env:SAT_LOCAL_PG_PASSWORD
    Write-Encrypted $passwordFile $password
} elseif (Test-Path -LiteralPath $passwordFile) {
    try { $password = Read-Encrypted $passwordFile } catch { throw "The saved password at $passwordFile cannot be decrypted by this Windows account. Run again with -ResetCredentials. ($($_.Exception.Message))" }
} else {
    $entered = Read-Host 'PostgreSQL password (saved encrypted for this Windows account)' -AsSecureString
    $password = [System.Net.NetworkCredential]::new('', $entered).Password
    Write-Encrypted $passwordFile $password
}

$previousPgPassword = $env:PGPASSWORD
try {
    $env:PGPASSWORD = $password
    $databaseExists = & $psql -X -w -h $PgHost -p $PgPort -U $PgUser -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = '$DatabaseName'"
    Require-Success 'PostgreSQL connection'
    if (($databaseExists | Out-String).Trim() -ne '1') {
        Write-Host "Creating local database $DatabaseName..."
        & $createdb -h $PgHost -p $PgPort -U $PgUser $DatabaseName
        Require-Success 'Database creation'
    }
    & $psql -X -w -h $PgHost -p $PgPort -U $PgUser -d $DatabaseName -tAc 'SELECT 1' | Out-Null
    Require-Success 'Database verification'
} finally {
    if ($null -eq $previousPgPassword) { Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue }
    else { $env:PGPASSWORD = $previousPgPassword }
}

if (Test-Path -LiteralPath $secretFile) {
    try { $authSecret = Read-Encrypted $secretFile } catch { throw 'The saved auth secret cannot be decrypted by this Windows account. Remove only .local/auth-secret.dpapi and retry.' }
} else {
    $bytes = New-Object byte[] 32
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    $authSecret = [BitConverter]::ToString($bytes).Replace('-', '')
    Write-Encrypted $secretFile $authSecret
}

$now = (Get-Date).ToUniversalTime()
if ($ResetSchedule -or -not (Test-Path -LiteralPath $scheduleFile)) {
    $schedule = @{ opensAt = $now.AddMinutes(-1).ToString('yyyy-MM-ddTHH:mm:ssZ'); entryClosesAt = $now.AddDays(7).ToString('yyyy-MM-ddTHH:mm:ssZ') }
    $schedule | ConvertTo-Json | Set-Content -LiteralPath $scheduleFile
} else {
    $schedule = Get-Content -LiteralPath $scheduleFile -Raw | ConvertFrom-Json
}

$encodedUser = [uri]::EscapeDataString($PgUser)
$encodedPassword = [uri]::EscapeDataString($password)
$env:DATABASE_URL = "postgres://${encodedUser}:${encodedPassword}@${PgHost}:${PgPort}/${DatabaseName}?sslmode=disable"
$env:AUTH_SECRET = $authSecret
$env:APP_ENV = 'development'
$env:DEV_EMAIL_LOG = 'false'
$env:COOKIE_SAMESITE = 'lax'
$env:PORT = [string]$BackendPort
$env:ALLOWED_ORIGINS = 'http://127.0.0.1:5173,http://localhost:5173,http://127.0.0.1:5176,http://localhost:5176'
$env:EXAM_ID = '1609-olympiad'
$env:EXAM_TITLE = '1609 SAT Olympiad'
$env:EXAM_OPEN_AT = [string]$schedule.opensAt
$env:EXAM_ENTRY_CLOSE_AT = [string]$schedule.entryClosesAt
Remove-Variable password, encodedPassword -ErrorAction SilentlyContinue

Write-Host "Local PostgreSQL is ready: $DatabaseName on ${PgHost}:${PgPort}."
Write-Host "Backend URL: http://127.0.0.1:${BackendPort}"
Write-Host 'Accounts use email and password; no verification email is sent.'
if ($now -gt [datetime]::Parse($env:EXAM_ENTRY_CLOSE_AT).ToUniversalTime()) { Write-Warning 'The saved entry window has closed. Run with -ResetSchedule if you need a new local window.' }
if ($SetupOnly) { return }

Push-Location $PSScriptRoot
try {
    & go run ./cmd/server
    Require-Success 'Backend startup'
} finally { Pop-Location }
