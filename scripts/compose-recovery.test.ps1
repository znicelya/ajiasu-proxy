$ErrorActionPreference='Stop'; Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'compose-common.ps1')
$root=Join-Path ([IO.Path]::GetTempPath()) ('ajiasu-recovery-'+[Guid]::NewGuid().ToString('N'))
try {
    New-Item -ItemType Directory -Path (Join-Path $root 'keyring') -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $root 'database.dump'),'database-canary')
    [IO.File]::WriteAllText((Join-Path $root 'keyring/control-plane-keyring'),'keyring-canary')
    [IO.File]::WriteAllText((Join-Path $root 'compose.env'),'AJIASU_ENVIRONMENT_ID=recovery-test'); [IO.File]::WriteAllText((Join-Path $root 'platform-ca.pem'),'ca-canary')
    $db=Get-Item (Join-Path $root 'database.dump'); $key=Get-Item (Join-Path $root 'keyring/control-plane-keyring'); $config=Get-Item (Join-Path $root 'compose.env'); $ca=Get-Item (Join-Path $root 'platform-ca.pem')
    $manifest=[ordered]@{backup_version=1;environment_id='recovery-test';created_at='2026-07-16T00:00:00Z';schema_version=11;release=@{};database=@{file='database.dump';sha256=(Get-FileHash $db.FullName -Algorithm SHA256).Hash.ToLowerInvariant();size=$db.Length};keyring=@{file='keyring/control-plane-keyring';sha256=(Get-FileHash $key.FullName -Algorithm SHA256).Hash.ToLowerInvariant();size=$key.Length};configuration=@{file='compose.env';sha256=(Get-FileHash $config.FullName -Algorithm SHA256).Hash.ToLowerInvariant();size=$config.Length};ca=@{file='platform-ca.pem';sha256=(Get-FileHash $ca.FullName -Algorithm SHA256).Hash.ToLowerInvariant();size=$ca.Length};excluded=@('redis','leases','sessions','runner-state','route-cache')}
    [IO.File]::WriteAllText((Join-Path $root 'backup-manifest.json'),($manifest|ConvertTo-Json -Depth 6))
    [void](Assert-ComposeBackup -Directory $root -EnvironmentId recovery-test)
    if(([IO.File]::ReadAllText((Join-Path $root 'backup-manifest.json'))).Contains('keyring-canary')){throw 'Backup manifest exposed keyring contents'}
    [IO.File]::WriteAllText((Join-Path $root 'database.dump'),'corrupt')
    $corrupt=$false; try {[void](Assert-ComposeBackup -Directory $root -EnvironmentId recovery-test)} catch {$corrupt=$true}
    if(-not $corrupt){throw 'Corrupt dump was accepted'}
    [IO.File]::WriteAllText((Join-Path $root 'database.dump'),'database-canary')
    [IO.File]::WriteAllText((Join-Path $root 'keyring/control-plane-keyring'),'wrong-key')
    $wrong=$false; try {[void](Assert-ComposeBackup -Directory $root -EnvironmentId recovery-test)} catch {$wrong=$true}
    if(-not $wrong){throw 'Wrong keyring was accepted'}
    $manifest.schema_version=10; [IO.File]::WriteAllText((Join-Path $root 'backup-manifest.json'),($manifest|ConvertTo-Json -Depth 6))
    $stale=$false; try {[void](Assert-ComposeBackup -Directory $root -EnvironmentId recovery-test)} catch {$stale=$true}
    if(-not $stale){throw 'Stale schema backup was accepted'}
    foreach($script in @('compose-restore.ps1','compose-rollback.ps1')){if(-not ([IO.File]::ReadAllText((Join-Path $PSScriptRoot $script)) -match 'Disposable')){throw "$script lacks destructive authorization"}}
    'compose recovery verification tests passed'
} finally {if(Test-Path $root){Remove-Item -LiteralPath $root -Recurse -Force}}
