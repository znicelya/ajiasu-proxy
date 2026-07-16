$ErrorActionPreference = 'Stop'

$path = Join-Path $PSScriptRoot 'scheduler-ci.ps1'
$content = Get-Content -LiteralPath $path -Raw

foreach ($required in @(
    "@('mod', 'tidy', '-diff')",
    "@('list', '-mod=readonly', './...')",
    "@('tool', 'sqlc', 'generate')",
    "@('tool', 'sqlc', 'diff')",
    "@('test', './tests/contract', '-run', 'Phase6'",
    "@('test', '-race', '-p', '1', './tests/integration'",
    "AJIASU_REQUIRE_DOCKER = '1'",
    "@('tool', 'staticcheck', './...')",
    "@('clippy', '--workspace', '--all-targets', '--all-features'",
    "@('test', '--workspace', '--all-features')",
    "@('diff', '--exit-code', '--', 'api/proto', 'internal/gen', ':(glob)internal/**/*dbgen/*.go')"
)) {
    if (-not $content.Contains($required)) {
        throw "scheduler CI is missing required gate: $required"
    }
}

if ($content -notmatch "Get-Command buf") {
    throw 'scheduler CI must explicitly handle Buf availability'
}
if ($content -notmatch "Get-Command cargo-deny") {
    throw 'scheduler CI must explicitly handle cargo-deny availability'
}

Write-Output 'scheduler CI fixture tests passed'
