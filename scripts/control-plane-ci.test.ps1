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

if (-not $content.Contains("Invoke-PowerShellScript -Path (Join-Path `$repoRoot 'scripts/lock-control-plane-images.test.ps1')")) {
    throw 'control-plane CI must execute the image-lock fixture tests'
}

if (-not $content.Contains("Invoke-NativeCommand -FilePath 'go' -Arguments @('tool', 'sqlc', 'diff')")) {
    throw 'control-plane CI must reject stale sqlc-generated files'
}

Write-Output 'control-plane CI fixture tests passed'
