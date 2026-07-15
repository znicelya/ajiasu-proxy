$ErrorActionPreference = 'Stop'

$scriptPath = Join-Path $PSScriptRoot 'control-plane-ci.ps1'
$content = Get-Content -LiteralPath $scriptPath -Raw

$tidyDiffCommand = "Invoke-NativeCommand -FilePath 'go' -Arguments @('mod', 'tidy', '-diff')"
if (-not $content.Contains($tidyDiffCommand)) {
    throw 'control-plane CI must validate module tidiness with go mod tidy -diff'
}

if ($content -match "Invoke-NativeCommand -FilePath 'git' -Arguments @\('diff'.*'go\.mod'.*'go\.sum'\)") {
    throw 'control-plane CI must not compare task-local module changes against HEAD'
}

if (-not $content.Contains("Invoke-PowerShellScript -Path (Join-Path `$repoRoot 'scripts/control-plane-ci.test.ps1')")) {
    throw 'control-plane CI must execute its own fixture tests'
}

if ($content -notmatch "AJIASU_REQUIRE_DOCKER\s*=\s*'1'") {
    throw 'control-plane CI must require Docker-backed integration tests'
}

if (-not $content.Contains("Invoke-NativeCommand -FilePath 'go' -Arguments @('test', '-race', '-p', '1', './...')")) {
    throw 'control-plane CI must serialize Docker-backed package tests on Windows'
}

if (-not $content.Contains("Invoke-PowerShellScript -Path (Join-Path `$repoRoot 'scripts/lock-control-plane-images.test.ps1')")) {
    throw 'control-plane CI must execute the image-lock fixture tests'
}

if (-not $content.Contains("Invoke-NativeCommand -FilePath 'go' -Arguments @('tool', 'sqlc', 'diff')")) {
    throw 'control-plane CI must reject stale sqlc-generated files'
}

if (-not $content.Contains("'--file', 'Dockerfile.control-plane'")) {
    throw 'control-plane CI must build the control-plane image'
}

if (-not $content.Contains("'--platform', 'linux/amd64,linux/arm64'")) {
    throw 'control-plane CI must build both supported architectures'
}

if (-not $content.Contains("'--output', 'type=cacheonly', '.'")) {
    throw 'control-plane CI must verify the multiarch image without publishing it'
}

Write-Output 'control-plane CI fixture tests passed'
