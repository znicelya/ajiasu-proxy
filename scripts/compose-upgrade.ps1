[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$CurrentEnvFile,
    [Parameter(Mandatory = $true)][string]$TargetEnvFile,
    [Parameter(Mandatory = $true)][ValidateSet('development','single-host')][string]$Mode,
    [Parameter(Mandatory = $true)][string]$BackupDestination,
    [Parameter(Mandatory = $true)][string]$SmokeConfigurationFile
)
. (Join-Path $PSScriptRoot 'compose-common.ps1')
$target = Read-ComposeEnvironmentFile -Path $TargetEnvFile
foreach ($name in @('AJIASU_CONTROL_PLANE_IMAGE','AJIASU_GATEWAY_IMAGE','AJIASU_AGENT_IMAGE','AJIASU_RUNNER_IMAGE','AJIASU_POSTGRES_IMAGE','AJIASU_REDIS_IMAGE','AJIASU_KEYCLOAK_IMAGE')) { Assert-ComposeImmutableImage -Name $name -Value ([string]$target[$name]) }
$versionText = (& docker run --rm ([string]$target.AJIASU_CONTROL_PLANE_IMAGE) version | Out-String).Trim()
if ($LASTEXITCODE -ne 0) { throw 'Target Control Plane compatibility metadata is unavailable' }
$version = $versionText | ConvertFrom-Json
if ($version.schema_version -ne 11) { throw 'Target Control Plane schema is incompatible with this upgrade path' }
& (Join-Path $PSScriptRoot 'compose-backup.ps1') -EnvFile $CurrentEnvFile -Mode $Mode -Destination $BackupDestination
[void](Assert-ComposeBackup -Directory $BackupDestination)
& (Join-Path $PSScriptRoot 'compose-down.ps1') -EnvFile $CurrentEnvFile -Mode $Mode -KeepDependencies
& (Join-Path $PSScriptRoot 'compose-up.ps1') -EnvFile $TargetEnvFile -Mode $Mode -SmokeConfigurationFile $SmokeConfigurationFile
$accepted = [ordered]@{ accepted_at=[DateTime]::UtcNow.ToString('o'); release_version=[string]$target.AJIASU_RELEASE_VERSION; environment_file_sha256=(Get-FileHash -LiteralPath $TargetEnvFile -Algorithm SHA256).Hash.ToLowerInvariant(); schema_version=11 }
[IO.File]::WriteAllText((Join-Path $BackupDestination 'accepted-release.json'), ($accepted | ConvertTo-Json), (New-Object Text.UTF8Encoding($false)))
Write-Host 'Upgrade accepted after migration, readiness, session convergence, and fixed/pool smoke probes.'
