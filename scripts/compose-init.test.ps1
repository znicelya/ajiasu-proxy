$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot 'compose-common.ps1')

$repository = Split-Path -Parent $PSScriptRoot
$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) ('ajiasu-compose-init-' + [Guid]::NewGuid().ToString('N'))
$binary = Join-Path $temporaryRoot 'control-plane.exe'
$generated = Join-Path $temporaryRoot 'generated'
$registry = Join-Path $temporaryRoot 'registry'
$envFile = Join-Path $temporaryRoot 'compose.env'
$imageDigest = 'example.invalid/ajiasu/test@sha256:' + ('1' * 64)

try {
    New-Item -ItemType Directory -Path $temporaryRoot | Out-Null
    $mutableRejected = $false
    try { Assert-ComposeImmutableImage -Name 'test image' -Value 'example.invalid/app:latest' } catch { $mutableRejected = $true }
    if (-not $mutableRejected) { throw 'Mutable image reference was accepted' }
    & go build -o $binary ./cmd/control-plane
    if ($LASTEXITCODE -ne 0) { throw 'Failed to build Control Plane fixture binary' }

    $arguments = @{
        EnvironmentId = 'phase7-script-test'; Mode = 'single-host'; ControlPlaneImage = $imageDigest; GatewayImage = $imageDigest;
        AgentImage = $imageDigest; RunnerImage = $imageDigest; GeneratedDir = $generated; EnvFile = $envFile;
        EnvironmentRegistry = $registry; ControlPlaneExecutable = $binary
    }
    $firstOutput = (& (Join-Path $PSScriptRoot 'compose-init.ps1') @arguments | Out-String)
    $manifestBefore = [IO.File]::ReadAllText((Join-Path $generated 'generated-state.json'))
    $keyBefore = [Convert]::ToBase64String([IO.File]::ReadAllBytes((Join-Path $generated 'control-plane-keyring')))
    $secondOutput = (& (Join-Path $PSScriptRoot 'compose-init.ps1') @arguments | Out-String)
    $manifestAfter = [IO.File]::ReadAllText((Join-Path $generated 'generated-state.json'))
    $keyAfter = [Convert]::ToBase64String([IO.File]::ReadAllBytes((Join-Path $generated 'control-plane-keyring')))
    if ($manifestBefore -cne $manifestAfter -or $keyBefore -cne $keyAfter) { throw 'Re-running compose-init rotated generated state' }
    if (($firstOutput + $secondOutput).Contains($keyBefore)) { throw 'compose-init displayed secret material' }
    if (@(Get-ChildItem -LiteralPath $generated -Force | Where-Object Name -Like '.partial-*').Count -ne 0) { throw 'Interrupted partial file survived initialization' }
    if (([IO.File]::ReadAllBytes((Join-Path $generated 'control-plane-keyring'))).Length -ne 32) { throw 'Control Plane keyring entropy length is invalid' }
    if (([IO.File]::ReadAllText($envFile)) -match '(postgresql://|very-secret|nse_|gwe_)') { throw 'Rendered environment file contains secret material' }

    $ignored = (& git check-ignore 'deploy/compose/generated/generated-state.json' | Out-String).Trim()
    if (-not $ignored) { throw 'Generated Compose state is not excluded from Git' }

    $collision = Join-Path $temporaryRoot 'collision'
    $collisionFailed = $false
    try {
        & $binary compose materialize --output $collision --environment-id phase7-script-test --mode single-host --registry $registry 2>$null
    } catch { $collisionFailed = $true }
    if (-not $collisionFailed -and $LASTEXITCODE -eq 0) { throw 'Reused environment ID was accepted in another directory' }

    $secretMarker = 'inspect-canary-' + [Guid]::NewGuid().ToString('N')
    $canary = Join-Path $temporaryRoot 'inspect-canary'
    Write-ComposePrivateFile -Path $canary -Value $secretMarker
    $container = 'ajiasu-compose-secret-canary-' + [Guid]::NewGuid().ToString('N')
    try {
        & docker create --name $container --mount ('type=bind,src=' + $canary + ',dst=/run/secrets/canary,readonly') ajiasu-control-plane:phase7-task4 health live | Out-Null
        if ($LASTEXITCODE -eq 0) {
            $inspection = (& docker inspect $container | Out-String)
            if ($inspection.Contains($secretMarker)) { throw 'docker inspect exposed mounted secret contents' }
        } else {
            Write-Warning 'Local Task 4 image is unavailable; docker inspect canary was skipped.'
        }
    } finally { & docker rm -f $container 2>$null | Out-Null }

    'compose initialization security tests passed'
} finally {
    if (Test-Path -LiteralPath $temporaryRoot) { Remove-Item -LiteralPath $temporaryRoot -Recurse -Force }
}
