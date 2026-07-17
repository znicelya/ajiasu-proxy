param(
    [ValidateSet('control-plane', 'gateway', 'agent', 'runner', 'fake-runner', 'fake-target')]
    [string[]] $Image = @('control-plane', 'gateway', 'agent', 'runner', 'fake-runner', 'fake-target')
)

$ErrorActionPreference = 'Stop'

$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$script:trivyCache = $null
$lock = @{}
foreach ($line in Get-Content -LiteralPath (Join-Path $root 'build/compose-images.lock')) {
    if ($line -notmatch '^([A-Z_]+)=([^\s]+@sha256:[0-9a-f]{64})$') {
        throw "invalid compose image lock line: $line"
    }
    $lock[$Matches[1]] = $Matches[2]
}
foreach ($required in @('GO_BUILD_IMAGE', 'RUST_BUILD_IMAGE', 'RUNTIME_IMAGE', 'SBOM_SCANNER_IMAGE')) {
    if (-not $lock.ContainsKey($required)) { throw "missing image lock $required" }
}
$buildImage = @{}
foreach ($entry in $lock.GetEnumerator()) {
    $value = $entry.Value
    if ($env:AJIASU_COMPOSE_REGISTRY_MIRROR -and $value -match '^([a-z0-9._-]+)(?::[^@]+)?@(sha256:[0-9a-f]{64})$') {
        $mirror = $env:AJIASU_COMPOSE_REGISTRY_MIRROR.TrimEnd('/')
        $value = "$mirror/library/$($Matches[1])@$($Matches[2])"
    }
    $buildImage[$entry.Key] = $value
}
$sbomAttestation = "type=sbom,generator=$($buildImage.SBOM_SCANNER_IMAGE)"
if ($env:AJIASU_COMPOSE_SBOM_SCANNER) {
    if ($env:AJIASU_COMPOSE_SBOM_SCANNER -notmatch '^[^\s@]+@sha256:[0-9a-f]{64}$') {
        throw 'AJIASU_COMPOSE_SBOM_SCANNER must be an image pinned by sha256 digest'
    }
    $sbomAttestation = "type=sbom,generator=$($env:AJIASU_COMPOSE_SBOM_SCANNER)"
}
$goBuildArguments = @()
if ($env:AJIASU_COMPOSE_GO_PROXY) {
    if ($env:AJIASU_COMPOSE_GO_PROXY -match '\s') {
        throw 'AJIASU_COMPOSE_GO_PROXY must not contain whitespace'
    }
    $goBuildArguments += "GOPROXY=$($env:AJIASU_COMPOSE_GO_PROXY)"
}
$rustBuildArguments = @()
if ($env:AJIASU_COMPOSE_CARGO_REGISTRY_INDEX) {
    if ($env:AJIASU_COMPOSE_CARGO_REGISTRY_INDEX -notmatch '^sparse\+https://[^\s]+/$') {
        throw 'AJIASU_COMPOSE_CARGO_REGISTRY_INDEX must be an HTTPS sparse registry URL ending in /'
    }
    $rustBuildArguments += "CARGO_REGISTRY_MIRROR=$($env:AJIASU_COMPOSE_CARGO_REGISTRY_INDEX)"
}
$trivyCacheDirectory = $null
if ($env:AJIASU_COMPOSE_TRIVY_CACHE_DIRECTORY) {
    if (-not (Test-Path -LiteralPath $env:AJIASU_COMPOSE_TRIVY_CACHE_DIRECTORY -PathType Container)) {
        throw 'AJIASU_COMPOSE_TRIVY_CACHE_DIRECTORY must be an existing directory'
    }
    $trivyCacheDirectory = (Resolve-Path -LiteralPath $env:AJIASU_COMPOSE_TRIVY_CACHE_DIRECTORY).Path
}

function Invoke-ImageBuild {
    param([string] $Dockerfile, [string] $Tag, [string[]] $BuildArguments)
    $arguments = @(
        'buildx', 'build', '--no-cache', '--pull=false',
        '--platform', 'linux/amd64,linux/arm64',
        '--file', $Dockerfile,
        '--attest', $sbomAttestation,
        '--attest', 'type=provenance,mode=max'
    )
    foreach ($argument in $BuildArguments) { $arguments += @('--build-arg', $argument) }
    $arguments += @('--output', 'type=cacheonly', '.')
    & docker @arguments
    if ($LASTEXITCODE -ne 0) { throw "image build failed: $Dockerfile" }

    $scanArguments = @('buildx', 'build', '--pull=false', '--platform', 'linux/amd64', '--file', $Dockerfile)
    foreach ($argument in $BuildArguments) { $scanArguments += @('--build-arg', $argument) }
    $scanArguments += @('--tag', $Tag, '--load', '.')
    & docker @scanArguments
    if ($LASTEXITCODE -ne 0) { throw "scan image build failed: $Dockerfile" }
    $inspection = @(docker image inspect $Tag 2>&1)
    $history = @(docker history --no-trunc $Tag 2>&1)
    $metadata = @($inspection + $history) -join "`n"
    if ($metadata -match '(?i)(password|enrollment[_-]?token|client[_-]?secret|keyring)\s*[:=]\s*[^\s"'']{8,}') {
        throw "secret-like value found in image metadata: $Tag"
    }
    $nativeTrivy = Get-Command trivy -ErrorAction SilentlyContinue
    if (-not $nativeTrivy) {
        $scanner = 'aquasec/trivy@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f'
        if (-not $trivyCacheDirectory -and -not $script:trivyCache) {
            $script:trivyCache = "ajiasu-compose-trivy-$PID-$([Guid]::NewGuid().ToString('N'))"
            & docker volume create $script:trivyCache | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'create Trivy cache volume' }
        }
    }
    $scanSucceeded = $false
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        if ($nativeTrivy) {
            $trivyArguments = @('image')
            if ($trivyCacheDirectory) { $trivyArguments += @('--cache-dir', $trivyCacheDirectory) }
            $trivyArguments += @('--scanners', 'vuln,secret', '--severity', 'HIGH,CRITICAL', '--ignore-unfixed', '--exit-code', '1', $Tag)
            & trivy @trivyArguments
        } else {
            $cacheMount = if ($trivyCacheDirectory) {
                "type=bind,source=$trivyCacheDirectory,target=/root/.cache/trivy"
            } else {
                "type=volume,source=$script:trivyCache,target=/root/.cache/trivy"
            }
            & docker run --rm --pull=missing --mount 'type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock,readonly' --mount $cacheMount $scanner image --image-src docker --scanners vuln,secret --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 $Tag
        }
        if ($LASTEXITCODE -eq 0) {
            $scanSucceeded = $true
            break
        }
        if ($attempt -lt 3) { Start-Sleep -Seconds (2 * $attempt) }
    }
    if (-not $scanSucceeded) { throw "image scan failed after 3 attempts: $Tag" }
}

Push-Location $root
try {
    & (Join-Path $root 'scripts/lock-compose-images.test.ps1')
    if ($Image -contains 'control-plane') {
        Invoke-ImageBuild 'Dockerfile.control-plane' 'ajiasu/control-plane:ci' (@(
            "GO_BUILD_IMAGE=$($buildImage.GO_BUILD_IMAGE)", "RUNTIME_IMAGE=$($buildImage.RUNTIME_IMAGE)"
        ) + $goBuildArguments)
    }
    foreach ($application in @(
        @{ Name = 'gateway'; Dockerfile = 'Dockerfile.gateway'; Tag = 'ajiasu/gateway:ci' },
        @{ Name = 'agent'; Dockerfile = 'Dockerfile.agent'; Tag = 'ajiasu/agent:ci' }
    )) {
        if ($Image -notcontains $application.Name) { continue }
        Invoke-ImageBuild $application.Dockerfile $application.Tag (@(
            "RUST_BUILD_IMAGE=$($buildImage.RUST_BUILD_IMAGE)", "RUNTIME_IMAGE=$($buildImage.RUNTIME_IMAGE)"
        ) + $rustBuildArguments)
    }
    if ($Image -contains 'runner') {
        Invoke-ImageBuild 'Dockerfile' 'ajiasu/runner:ci' @("ALPINE_IMAGE=$($buildImage.RUNTIME_IMAGE)")
    }
    if ($Image -contains 'fake-runner') {
        Invoke-ImageBuild 'tests/e2e/Dockerfile.fake-runner' 'ajiasu/fake-runner:ci' (@(
            "GO_BUILD_IMAGE=$($buildImage.GO_BUILD_IMAGE)", "RUNTIME_IMAGE=$($buildImage.RUNTIME_IMAGE)"
        ) + $goBuildArguments)
    }
    if ($Image -contains 'fake-target') {
        Invoke-ImageBuild 'tests/compose/Dockerfile.fake-target' 'ajiasu/fake-target:ci' (@(
            "GO_BUILD_IMAGE=$($buildImage.GO_BUILD_IMAGE)", "RUNTIME_IMAGE=$($buildImage.RUNTIME_IMAGE)"
        ) + $goBuildArguments)
    }
}
finally {
    if ($script:trivyCache) { & docker volume rm -f $script:trivyCache 2>$null | Out-Null }
    Pop-Location
}
