[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$BackupDirectory,
    [Parameter(Mandatory = $true)][string]$EvidencePath,
    [Parameter(Mandatory = $true)][DateTime]$RecoveryPointAt,
    [Parameter(Mandatory = $true)][DateTime]$RestoreStartedAt,
    [Parameter(Mandatory = $true)][DateTime]$RestoreCompletedAt,
    [switch]$DatabaseVerified,
    [switch]$KeyringVerified,
    [switch]$SmokeVerified
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (-not (Test-Path -LiteralPath $BackupDirectory -PathType Container)) { throw 'Backup directory is unavailable' }
$manifest = Join-Path $BackupDirectory 'backup-manifest.json'
if (-not (Test-Path -LiteralPath $manifest -PathType Leaf)) { throw 'backup-manifest.json is required' }
$backup = Get-Content -Raw $manifest | ConvertFrom-Json
$created = [DateTime]::Parse([string]$backup.created_at).ToUniversalTime()
$recovery = $RecoveryPointAt.ToUniversalTime()
$started = $RestoreStartedAt.ToUniversalTime()
$completed = $RestoreCompletedAt.ToUniversalTime()
if ($recovery -lt $created -or $started -gt $completed) { throw 'Recovery timestamps are inconsistent' }
$rpo = ($recovery - $created).TotalMinutes
$rto = ($completed - $started).TotalMinutes
if ($rpo -gt 15) { throw "RPO target exceeded: $rpo minutes" }
if ($rto -gt 60) { throw "RTO target exceeded: $rto minutes" }
if (-not $DatabaseVerified -or -not $KeyringVerified -or -not $SmokeVerified) { throw 'Database, keyring, and smoke verification are required' }
$evidence = [ordered]@{
    evidence_version = 1
    exercise_id = [Guid]::NewGuid().ToString()
    backup_created_at = $created.ToString('o')
    recovery_point_at = $recovery.ToString('o')
    restore_started_at = $started.ToString('o')
    restore_completed_at = $completed.ToString('o')
    rpo_minutes = [Math]::Round($rpo, 3)
    rto_minutes = [Math]::Round($rto, 3)
    database_verified = $true
    keyring_verified = $true
    redis_restored = $false
    smoke_verified = $true
    result = 'passed'
}
$directory = Split-Path -Parent $EvidencePath
if ($directory -and -not (Test-Path -LiteralPath $directory)) { New-Item -ItemType Directory -Path $directory | Out-Null }
[IO.File]::WriteAllText($EvidencePath, ($evidence | ConvertTo-Json), (New-Object Text.UTF8Encoding($false)))
Write-Host "Restore exercise passed: RPO=$($evidence.rpo_minutes)m RTO=$($evidence.rto_minutes)m"
