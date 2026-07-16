[CmdletBinding(SupportsShouldProcess=$true, ConfirmImpact='High')]
param([Parameter(Mandatory = $true)][string]$EnvFile, [Parameter(Mandatory = $true)][ValidateSet('development','single-host')][string]$Mode, [Parameter(Mandatory = $true)][string]$BackupDirectory, [Parameter(Mandatory = $true)][switch]$Disposable)
. (Join-Path $PSScriptRoot 'compose-common.ps1')
if (-not $Disposable) { throw 'Restore requires explicit -Disposable authorization' }
$values = Read-ComposeEnvironmentFile -Path $EnvFile; $manifest = Assert-ComposeBackup -Directory $BackupDirectory -EnvironmentId ([string]$values.AJIASU_ENVIRONMENT_ID)
if (-not $PSCmdlet.ShouldProcess([string]$values.AJIASU_ENVIRONMENT_ID, 'destroy current Compose data and restore backup')) { return }
$composeDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) 'deploy/compose'; $files = Get-ComposeFiles -ComposeDirectory $composeDirectory -Mode $Mode; $arguments = Get-ComposeDockerArguments -EnvFile $EnvFile -Files $files
& (Join-Path $PSScriptRoot 'compose-down.ps1') -EnvFile $EnvFile -Mode $Mode -KeepDependencies
& docker @arguments down --remove-orphans
if ($LASTEXITCODE -ne 0) { throw 'Disposable stack teardown failed' }
$remainingRunners = @(& docker ps -aq --filter 'label=ajiasu.owner=control-plane')
if (@($remainingRunners | Where-Object { $_ }).Count -ne 0) { throw 'Owned Runner containers remain; restore will not delete ambiguous runtime state' }
& docker volume rm ajiasu_postgres-data 2>$null | Out-Null
Copy-Item -LiteralPath (Join-Path $BackupDirectory ([string]$manifest.keyring.file)) -Destination (Join-Path ([string]$values.AJIASU_GENERATED_DIR) 'control-plane-keyring') -Force
Set-ComposePrivatePath -Path (Join-Path ([string]$values.AJIASU_GENERATED_DIR) 'control-plane-keyring')
& docker @arguments up -d postgres
if ($LASTEXITCODE -ne 0) { throw 'Restored PostgreSQL startup failed' }
Wait-ComposeServiceHealthy -DockerArguments $arguments -Service postgres
$postgresId = (& docker @arguments ps -q postgres | Out-String).Trim(); $containerDump='/tmp/ajiasu-restore.dump'
try {
    & docker cp (Join-Path $BackupDirectory ([string]$manifest.database.file)) ($postgresId + ':' + $containerDump)
    if ($LASTEXITCODE -ne 0) { throw 'Restore dump staging failed' }
    & docker @arguments exec -T postgres pg_restore -U ajiasu_admin -d ajiasu --clean --if-exists $containerDump
    if ($LASTEXITCODE -ne 0) { throw 'PostgreSQL restore failed; restored stack remains stopped for diagnosis' }
} finally { & docker @arguments exec -T postgres rm -f $containerDump 2>$null | Out-Null }
& docker @arguments run --rm migration migrate status | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Restored schema is incompatible' }
Write-Host 'Restore completed into the disposable database. Run compose-up with fixed and pool smoke probes before acceptance.'
