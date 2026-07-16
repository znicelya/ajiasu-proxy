$ErrorActionPreference = 'Stop'

$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$lock = @{}
foreach ($line in Get-Content -LiteralPath (Join-Path $root 'build/compose-images.lock')) {
    if ($line -notmatch '^([A-Z_]+)=([^\s]+@sha256:[0-9a-f]{64})$') {
        throw "invalid compose image lock line: $line"
    }
    $lock[$Matches[1]] = $Matches[2]
}
foreach ($required in @('GO_BUILD_IMAGE', 'RUST_BUILD_IMAGE', 'RUNTIME_IMAGE')) {
    if (-not $lock.ContainsKey($required)) { throw "missing image lock $required" }
}

function Invoke-ImageBuild {
    param([string] $Dockerfile, [string] $Tag, [string[]] $BuildArguments)
    $arguments = @(
        'buildx', 'build', '--no-cache', '--pull=false',
        '--platform', 'linux/amd64,linux/arm64',
        '--file', $Dockerfile,
        '--attest', 'type=sbom',
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
    if (-not (Get-Command trivy -ErrorAction SilentlyContinue)) {
        throw 'trivy is required for release image vulnerability and secret scans'
    }
    & trivy image --scanners vuln,secret --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 $Tag
    if ($LASTEXITCODE -ne 0) { throw "image scan failed: $Tag" }
}

Push-Location $root
try {
    & (Join-Path $root 'scripts/lock-compose-images.test.ps1')
    Invoke-ImageBuild 'Dockerfile.control-plane' 'ajiasu/control-plane:ci' @(
        "GO_BUILD_IMAGE=$($lock.GO_BUILD_IMAGE)", "RUNTIME_IMAGE=$($lock.RUNTIME_IMAGE)"
    )
    foreach ($dockerfile in @('Dockerfile.gateway', 'Dockerfile.agent')) {
        $tag = if ($dockerfile -eq 'Dockerfile.gateway') { 'ajiasu/gateway:ci' } else { 'ajiasu/agent:ci' }
        Invoke-ImageBuild $dockerfile $tag @(
            "RUST_BUILD_IMAGE=$($lock.RUST_BUILD_IMAGE)", "RUNTIME_IMAGE=$($lock.RUNTIME_IMAGE)"
        )
    }
    Invoke-ImageBuild 'Dockerfile' 'ajiasu/runner:ci' @("ALPINE_IMAGE=$($lock.RUNTIME_IMAGE)")
    Invoke-ImageBuild 'tests/e2e/Dockerfile.fake-runner' 'ajiasu/fake-runner:ci' @(
        "GO_BUILD_IMAGE=$($lock.GO_BUILD_IMAGE)", "RUNTIME_IMAGE=$($lock.RUNTIME_IMAGE)"
    )
}
finally {
    Pop-Location
}
