[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$temporary = Join-Path ([IO.Path]::GetTempPath()) ('ajiasu-recovery-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
    $backup = Join-Path $temporary 'backup'
    New-Item -ItemType Directory -Path $backup | Out-Null
    [IO.File]::WriteAllText((Join-Path $backup 'backup-manifest.json'), '{"created_at":"2026-07-17T00:00:00Z"}')
    $evidence = Join-Path $temporary 'evidence.json'
    & (Join-Path $root 'scripts\phase9-restore-rehearsal.ps1') -BackupDirectory $backup -EvidencePath $evidence -RecoveryPointAt '2026-07-17T00:10:00Z' -RestoreStartedAt '2026-07-17T00:12:00Z' -RestoreCompletedAt '2026-07-17T00:42:00Z' -DatabaseVerified -KeyringVerified -SmokeVerified
    $result = Get-Content -Raw $evidence | ConvertFrom-Json
    if ($result.rpo_minutes -gt 15 -or $result.rto_minutes -gt 60 -or $result.redis_restored) { throw 'Recovery evidence violates Phase 9 targets' }
} finally { Remove-Item -LiteralPath $temporary -Recurse -Force }
Write-Host 'Phase 9 recovery contract passed.'
