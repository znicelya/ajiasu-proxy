[CmdletBinding(SupportsShouldProcess=$true, ConfirmImpact='High')]
param(
    [Parameter(Mandatory = $true)][string]$PreviousEnvFile,
    [Parameter(Mandatory = $true)][ValidateSet('development','single-host')][string]$Mode,
    [Parameter(Mandatory = $true)][string]$PreUpgradeBackup,
    [Parameter(Mandatory = $true)][string]$SmokeConfigurationFile
)
. (Join-Path $PSScriptRoot 'compose-common.ps1')
$previous = Read-ComposeEnvironmentFile -Path $PreviousEnvFile
foreach ($name in @('AJIASU_CONTROL_PLANE_IMAGE','AJIASU_GATEWAY_IMAGE','AJIASU_AGENT_IMAGE','AJIASU_RUNNER_IMAGE')) { Assert-ComposeImmutableImage -Name $name -Value ([string]$previous[$name]) }
$metadata = Assert-ComposeBackup -Directory $PreUpgradeBackup -EnvironmentId ([string]$previous.AJIASU_ENVIRONMENT_ID)
if ($metadata.schema_version -ne 11) { throw 'Rollback requires a compatible database restore, not image changes alone' }
if (-not $PSCmdlet.ShouldProcess([string]$previous.AJIASU_ENVIRONMENT_ID, 'restore pre-upgrade database and previous release')) { return }
& (Join-Path $PSScriptRoot 'compose-restore.ps1') -EnvFile $PreviousEnvFile -Mode $Mode -BackupDirectory $PreUpgradeBackup -Disposable -Confirm:$false
& (Join-Path $PSScriptRoot 'compose-up.ps1') -EnvFile $PreviousEnvFile -Mode $Mode -SmokeConfigurationFile $SmokeConfigurationFile
Write-Host 'Rollback accepted only after database restore and full readiness/smoke validation.'
