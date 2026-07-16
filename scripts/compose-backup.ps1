[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$EnvFile, [Parameter(Mandatory = $true)][ValidateSet('development','single-host')][string]$Mode, [Parameter(Mandatory = $true)][string]$Destination)
. (Join-Path $PSScriptRoot 'compose-common.ps1')
if (Test-Path -LiteralPath $Destination) { if (@(Get-ChildItem -LiteralPath $Destination -Force).Count -ne 0) { throw 'Backup destination must be new or empty' } } else { New-Item -ItemType Directory -Path $Destination | Out-Null }
Set-ComposePrivatePath -Path $Destination
$values = Read-ComposeEnvironmentFile -Path $EnvFile
Assert-ComposeGeneratedState -Directory ([string]$values.AJIASU_GENERATED_DIR) -EnvironmentId ([string]$values.AJIASU_ENVIRONMENT_ID)
$composeDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) 'deploy/compose'; $files = Get-ComposeFiles -ComposeDirectory $composeDirectory -Mode $Mode; $arguments = Get-ComposeDockerArguments -EnvFile $EnvFile -Files $files
$postgresId = (& docker @arguments ps -q postgres | Out-String).Trim(); if (-not $postgresId) { throw 'PostgreSQL must be running for a consistent backup' }
$temporaryDump = '/tmp/ajiasu-backup-' + [Guid]::NewGuid().ToString('N') + '.dump'
try {
    & docker @arguments exec -T postgres sh -ec ('umask 077; pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc -f ' + $temporaryDump)
    if ($LASTEXITCODE -ne 0) { throw 'PostgreSQL backup failed' }
    & docker cp ($postgresId + ':' + $temporaryDump) (Join-Path $Destination 'database.dump')
    if ($LASTEXITCODE -ne 0) { throw 'Copying PostgreSQL backup failed' }
} finally { & docker @arguments exec -T postgres rm -f $temporaryDump 2>$null | Out-Null }
$keyringDirectory = Join-Path $Destination 'keyring'; New-Item -ItemType Directory -Path $keyringDirectory | Out-Null; Set-ComposePrivatePath -Path $keyringDirectory
Copy-Item -LiteralPath (Join-Path ([string]$values.AJIASU_GENERATED_DIR) 'control-plane-keyring') -Destination (Join-Path $keyringDirectory 'control-plane-keyring')
Set-ComposePrivatePath -Path (Join-Path $keyringDirectory 'control-plane-keyring')
Copy-Item -LiteralPath (Join-Path ([string]$values.AJIASU_GENERATED_DIR) 'platform-ca.pem') -Destination (Join-Path $Destination 'platform-ca.pem')
Copy-Item -LiteralPath $EnvFile -Destination (Join-Path $Destination 'compose.env')
$database = Get-Item (Join-Path $Destination 'database.dump'); $keyring = Get-Item (Join-Path $keyringDirectory 'control-plane-keyring'); $configuration=Get-Item (Join-Path $Destination 'compose.env'); $ca=Get-Item (Join-Path $Destination 'platform-ca.pem')
$release = [ordered]@{}; foreach ($name in @('AJIASU_RELEASE_VERSION','AJIASU_CONTROL_PLANE_IMAGE','AJIASU_GATEWAY_IMAGE','AJIASU_AGENT_IMAGE','AJIASU_RUNNER_IMAGE')) { $release[$name] = [string]$values[$name] }
$manifest = [ordered]@{
    backup_version=1; environment_id=[string]$values.AJIASU_ENVIRONMENT_ID; created_at=[DateTime]::UtcNow.ToString('o'); schema_version=11; release=$release;
    database=[ordered]@{file='database.dump';sha256=(Get-FileHash $database.FullName -Algorithm SHA256).Hash.ToLowerInvariant();size=$database.Length};
    keyring=[ordered]@{file='keyring/control-plane-keyring';sha256=(Get-FileHash $keyring.FullName -Algorithm SHA256).Hash.ToLowerInvariant();size=$keyring.Length};
    configuration=[ordered]@{file='compose.env';sha256=(Get-FileHash $configuration.FullName -Algorithm SHA256).Hash.ToLowerInvariant();size=$configuration.Length};
    ca=[ordered]@{file='platform-ca.pem';sha256=(Get-FileHash $ca.FullName -Algorithm SHA256).Hash.ToLowerInvariant();size=$ca.Length};
    excluded=@('redis','leases','sessions','runner-state','route-cache')
}
[IO.File]::WriteAllText((Join-Path $Destination 'backup-manifest.json'), ($manifest | ConvertTo-Json -Depth 6), (New-Object Text.UTF8Encoding($false)))
Set-ComposePrivatePath -Path (Join-Path $Destination 'backup-manifest.json')
[void](Assert-ComposeBackup -Directory $Destination -EnvironmentId ([string]$values.AJIASU_ENVIRONMENT_ID))
Write-Host 'Backup verified. Store the database and keyring artifacts with separate off-host encryption and retention controls.'
