[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$EnvFile,
    [Parameter(Mandatory = $true)][ValidateSet('development', 'single-host', 'external')][string]$Mode,
    [TimeSpan]$Timeout = [TimeSpan]::FromMinutes(5),
    [string]$SmokeConfigurationFile,
    [switch]$SkipSmoke
)
. (Join-Path $PSScriptRoot 'compose-common.ps1')
$composeDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) 'deploy/compose'
$files = Get-ComposeFiles -ComposeDirectory $composeDirectory -Mode $Mode
$arguments = Get-ComposeDockerArguments -EnvFile $EnvFile -Files $files
$environmentValues = @{}
foreach ($line in Get-Content -LiteralPath $EnvFile) { if ($line -match '^([^#=]+)=(.*)$') { $environmentValues[$matches[1]] = $matches[2] } }
foreach ($name in @('AJIASU_CONTROL_PLANE_IMAGE','AJIASU_GATEWAY_IMAGE','AJIASU_AGENT_IMAGE','AJIASU_RUNNER_IMAGE','AJIASU_POSTGRES_IMAGE','AJIASU_REDIS_IMAGE','AJIASU_KEYCLOAK_IMAGE')) {
    Assert-ComposeImmutableImage -Name $name -Value ([string]$environmentValues[$name])
}
Assert-ComposeGeneratedState -Directory ([string]$environmentValues.AJIASU_GENERATED_DIR) -EnvironmentId ([string]$environmentValues.AJIASU_ENVIRONMENT_ID)

& docker version --format '{{.Server.Version}}' | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Docker Engine is unavailable' }
& docker @arguments config --quiet
if ($LASTEXITCODE -ne 0) { throw 'Compose release or configuration validation failed' }

if ($Mode -ne 'external') {
    & docker @arguments up -d postgres redis
    if ($LASTEXITCODE -ne 0) { throw 'Dependency startup failed; diagnostic containers were preserved' }
    Wait-ComposeServiceHealthy -DockerArguments $arguments -Service postgres -Timeout $Timeout
    Wait-ComposeServiceHealthy -DockerArguments $arguments -Service redis -Timeout $Timeout
}

& docker @arguments run --rm migration
if ($LASTEXITCODE -ne 0) { throw 'Migration failed; the stack was not advanced and diagnostics were preserved' }
& docker @arguments up -d --no-deps control-plane
if ($LASTEXITCODE -ne 0) { throw 'Control Plane startup failed' }
Wait-ComposeServiceHealthy -DockerArguments $arguments -Service control-plane -Timeout $Timeout

$generated = [string]$environmentValues.AJIASU_GENERATED_DIR
if (-not (Test-Path -LiteralPath (Join-Path $generated 'agent-enrollment-token'))) {
    & (Join-Path $PSScriptRoot 'compose-agent-enroll.ps1') -EnvFile $EnvFile -Mode $Mode -Name ('agent-' + [string]$environmentValues.AJIASU_ENVIRONMENT_ID) -GeneratedDir $generated
}
& docker @arguments up -d --no-deps agent
if ($LASTEXITCODE -ne 0) { throw 'Agent startup failed' }
Wait-ComposeServiceHealthy -DockerArguments $arguments -Service agent -Timeout $Timeout

if (-not (Test-Path -LiteralPath (Join-Path $generated 'gateway-enrollment-token'))) {
    & (Join-Path $PSScriptRoot 'compose-gateway-enroll.ps1') -EnvFile $EnvFile -Mode $Mode -Name ('gateway-' + [string]$environmentValues.AJIASU_ENVIRONMENT_ID) -GeneratedDir $generated
}
& docker @arguments up -d --no-deps gateway
if ($LASTEXITCODE -ne 0) { throw 'Gateway startup failed' }
Wait-ComposeServiceHealthy -DockerArguments $arguments -Service gateway -Timeout $Timeout

if ($SmokeConfigurationFile) { Invoke-ComposeSmokeProbes -ConfigurationFile $SmokeConfigurationFile }
elseif (-not $SkipSmoke) { throw 'Fixed and pool smoke configuration is required; the ready stack was preserved for configuration' }

& (Join-Path $PSScriptRoot 'compose-status.ps1') -EnvFile $EnvFile -Mode $Mode | Out-Host
