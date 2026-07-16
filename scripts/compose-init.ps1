[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidatePattern('^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$')][string]$EnvironmentId,
    [Parameter(Mandatory = $true)][ValidateSet('development', 'single-host', 'external')][string]$Mode,
    [Parameter(Mandatory = $true)][string]$ControlPlaneImage,
    [Parameter(Mandatory = $true)][string]$GatewayImage,
    [Parameter(Mandatory = $true)][string]$AgentImage,
    [Parameter(Mandatory = $true)][string]$RunnerImage,
    [string]$PostgresImage = 'postgres:17.6-alpine3.22@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94',
    [string]$RedisImage = 'redis:8.2.3-alpine3.22@sha256:08ad0b1d280850169a790dba1393ff7a90aef951fc19632cf4d3ce4f78e679ba',
    [string]$KeycloakImage = 'quay.io/keycloak/keycloak:26.3.2@sha256:98fab020a3a490aba0978f237e2a06cd0ea42bf149c6cf10f11c0aaf27728ff2',
    [string]$GeneratedDir,
    [string]$EnvFile,
    [string]$EnvironmentRegistry,
    [string]$OidcIssuer = 'https://identity.example.test/realms/ajiasu',
    [string]$OidcRedirectUrl = 'https://proxy.example.test/api/v1/auth/oidc/callback',
    [string]$RedisAddress = 'redis:6379',
    [bool]$RedisTls = $false,
    [int]$DockerGid = 999,
    [string]$NormalDatabaseDsnFile,
    [string]$PlatformDatabaseDsnFile,
    [string]$MigrationDatabaseDsnFile,
    [string]$ControlPlaneExecutable
)

. (Join-Path $PSScriptRoot 'compose-common.ps1')

$repository = Split-Path -Parent $PSScriptRoot
$composeDirectory = Join-Path $repository 'deploy/compose'
if (-not $GeneratedDir) { $GeneratedDir = Join-Path $composeDirectory 'generated' }
if (-not $EnvFile) { $EnvFile = Join-Path $composeDirectory 'env/compose.env.local' }
if (-not $EnvironmentRegistry) { $EnvironmentRegistry = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'AJiaSu/environment-ids' }

foreach ($image in @{
    AJIASU_CONTROL_PLANE_IMAGE = $ControlPlaneImage; AJIASU_GATEWAY_IMAGE = $GatewayImage; AJIASU_AGENT_IMAGE = $AgentImage;
    AJIASU_RUNNER_IMAGE = $RunnerImage; AJIASU_POSTGRES_IMAGE = $PostgresImage; AJIASU_REDIS_IMAGE = $RedisImage; AJIASU_KEYCLOAK_IMAGE = $KeycloakImage
}.GetEnumerator()) { Assert-ComposeImmutableImage -Name $image.Key -Value $image.Value }

if ($Mode -eq 'external') {
    foreach ($path in @($NormalDatabaseDsnFile, $PlatformDatabaseDsnFile, $MigrationDatabaseDsnFile)) {
        if (-not $path -or -not (Test-Path -LiteralPath $path -PathType Leaf)) { throw 'External mode requires all three database DSN files' }
    }
    if (-not $RedisTls -or $RedisAddress -eq 'redis:6379') { throw 'External mode requires an explicit TLS-enabled Redis endpoint' }
}
if ($Mode -ne 'development') {
    if (([Uri]$OidcIssuer).Scheme -ne 'https' -or ([Uri]$OidcRedirectUrl).Scheme -ne 'https') { throw 'Production initialization requires HTTPS OIDC endpoints' }
}

$secretEnvironment = @{}
if ($Mode -eq 'external') {
    $secretEnvironment.AJIASU_DATABASE_NORMAL_DSN_FILE = (Resolve-Path -LiteralPath $NormalDatabaseDsnFile).Path
    $secretEnvironment.AJIASU_DATABASE_PLATFORM_DSN_FILE = (Resolve-Path -LiteralPath $PlatformDatabaseDsnFile).Path
    $secretEnvironment.AJIASU_DATABASE_MIGRATION_DSN_FILE = (Resolve-Path -LiteralPath $MigrationDatabaseDsnFile).Path
}

$materialize = @('compose', 'materialize', '--output', $GeneratedDir, '--environment-id', $EnvironmentId, '--mode', $Mode, '--registry', $EnvironmentRegistry)
if ($ControlPlaneExecutable) {
    Invoke-ComposeControlPlaneCLI -Arguments $materialize -ControlPlaneExecutable $ControlPlaneExecutable -ControlPlaneImage $ControlPlaneImage -Environment $secretEnvironment
} else {
    if (-not (Test-Path -LiteralPath $GeneratedDir)) { New-Item -ItemType Directory -Path $GeneratedDir | Out-Null }
    if (-not (Test-Path -LiteralPath $EnvironmentRegistry)) { New-Item -ItemType Directory -Path $EnvironmentRegistry | Out-Null }
    $dockerArgs = @('run', '--rm', '-v', ((Resolve-Path -LiteralPath $GeneratedDir).Path + ':/output'), '-v', ((Resolve-Path -LiteralPath $EnvironmentRegistry).Path + ':/registry'))
    if ($env:OS -ne 'Windows_NT') { $dockerArgs += @('--user', ((& id -u) + ':' + (& id -g))) }
    $index = 0
    foreach ($entry in $secretEnvironment.GetEnumerator()) {
        $containerPath = "/input/secret-$index"
        $dockerArgs += @('-e', ($entry.Key + '=' + $containerPath), '-v', ($entry.Value + ':' + $containerPath + ':ro'))
        $index++
    }
    $dockerArgs += @($ControlPlaneImage, 'compose', 'materialize', '--output', '/output', '--environment-id', $EnvironmentId, '--mode', $Mode, '--registry', '/registry')
    & docker @dockerArgs
    if ($LASTEXITCODE -ne 0) { throw 'Control Plane materialization failed without exposing secrets' }
}
Set-ComposePrivatePath -Path $GeneratedDir
Get-ChildItem -LiteralPath $GeneratedDir -Force | ForEach-Object { Set-ComposePrivatePath -Path $_.FullName }

$fingerprint = ([IO.File]::ReadAllText((Join-Path $GeneratedDir 'gateway-certificate-fingerprint'))).Trim()
$environmentName = if ($Mode -eq 'development') { 'development' } else { 'production' }
$generatedForCompose = (Resolve-Path -LiteralPath $GeneratedDir).Path -replace '\\', '/'
$lines = @(
    "AJIASU_ENVIRONMENT=$environmentName", "AJIASU_ENVIRONMENT_ID=$EnvironmentId", 'AJIASU_RELEASE_VERSION=0.7.0',
    "AJIASU_CONTROL_PLANE_IMAGE=$ControlPlaneImage", "AJIASU_GATEWAY_IMAGE=$GatewayImage", "AJIASU_AGENT_IMAGE=$AgentImage", "AJIASU_RUNNER_IMAGE=$RunnerImage",
    "AJIASU_POSTGRES_IMAGE=$PostgresImage", "AJIASU_REDIS_IMAGE=$RedisImage", "AJIASU_KEYCLOAK_IMAGE=$KeycloakImage", "AJIASU_GENERATED_DIR=$generatedForCompose",
    'AJIASU_DOCKER_SOCKET=/var/run/docker.sock', "AJIASU_DOCKER_GID=$DockerGid", 'AJIASU_MANAGEMENT_PORT=8081', 'AJIASU_GATEWAY_BIND=0.0.0.0',
    'AJIASU_GATEWAY_HTTP_PORT=8080', 'AJIASU_GATEWAY_SOCKS5_PORT=1080', "AJIASU_GATEWAY_CERTIFICATE_FINGERPRINT=$fingerprint",
    "AJIASU_REDIS_ADDRESS=$RedisAddress", 'AJIASU_REDIS_USERNAME=scheduler', ('AJIASU_REDIS_TLS=' + $RedisTls.ToString().ToLowerInvariant()), "AJIASU_OIDC_ISSUER=$OidcIssuer",
    'AJIASU_OIDC_CLIENT_ID=ajiasu-control-plane', "AJIASU_OIDC_REDIRECT_URL=$OidcRedirectUrl"
)
$envDirectory = Split-Path -Parent $EnvFile
if (-not (Test-Path -LiteralPath $envDirectory)) { New-Item -ItemType Directory -Path $envDirectory | Out-Null }
$temporaryEnv = Join-Path $envDirectory ('.compose-env-' + [Guid]::NewGuid().ToString('N'))
try {
    [IO.File]::WriteAllLines($temporaryEnv, $lines, (New-Object Text.UTF8Encoding($false)))
    Move-Item -LiteralPath $temporaryEnv -Destination $EnvFile -Force
} finally { if (Test-Path -LiteralPath $temporaryEnv) { Remove-Item -LiteralPath $temporaryEnv -Force } }

$files = Get-ComposeFiles -ComposeDirectory $composeDirectory -Mode $Mode
$dockerArgs = Get-ComposeDockerArguments -EnvFile $EnvFile -Files $files
& docker @dockerArgs config --quiet
if ($LASTEXITCODE -ne 0) { throw 'Rendered Compose configuration is invalid' }
Write-Host "Compose state initialized for '$EnvironmentId' ($Mode). Secrets were not displayed."
