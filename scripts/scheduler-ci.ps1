param(
    [switch] $SkipDocker
)

$ErrorActionPreference = 'Stop'

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)][string] $FilePath,
        [Parameter(Mandatory = $true)][string[]] $Arguments
    )

    Write-Host "[scheduler-ci] $FilePath $($Arguments -join ' ')"
    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "scheduler CI command failed: $FilePath $($Arguments -join ' ')"
    }
}

$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$previousRequireDocker = $env:AJIASU_REQUIRE_DOCKER
Push-Location -LiteralPath $repoRoot
try {
    $runningOnWindows = [System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT
    if ($runningOnWindows) {
        Write-Warning 'Windows CRLF makes go mod tidy -diff report a whole-file go.sum diff; using readonly module loading locally'
        Invoke-Checked 'go' @('list', '-mod=readonly', './...')
    }
    else {
        Invoke-Checked 'go' @('mod', 'tidy', '-diff')
    }
    if ($runningOnWindows) {
        Write-Warning 'sqlc generate rewrites all generated Go files with LF on Windows; relying on vet and diff locally'
    }
    else {
        Invoke-Checked 'go' @('tool', 'sqlc', 'generate')
    }
    Invoke-Checked 'go' @('tool', 'sqlc', 'vet')
    Invoke-Checked 'go' @('tool', 'sqlc', 'diff')

    if (Get-Command buf -ErrorAction SilentlyContinue) {
        Invoke-Checked 'buf' @('lint', 'api/proto')
        Invoke-Checked 'buf' @('breaking', 'api/proto', '--against', '.git#branch=main,subdir=api/proto')
        Invoke-Checked 'buf' @('generate', 'api/proto')
    }
    else {
        Write-Warning 'buf is unavailable; protobuf fixture and contract tests remain mandatory'
    }

    Invoke-Checked 'go' @('test', './tests/contract', '-run', 'Phase6', '-count=1')
    Invoke-Checked 'go' @('test', './internal/scheduler', './internal/health', './internal/gateways', '-count=1')
    if ($SkipDocker) {
        Invoke-Checked 'go' @('test', '-race', './tests/integration', '-run', 'TestPhase6(CompetingSchedulers|HealthMigration)', '-count=1')
    }
    else {
        $env:AJIASU_REQUIRE_DOCKER = '1'
        Invoke-Checked 'go' @('test', '-race', '-p', '1', './tests/integration', '-run', 'TestPhase(5Gateway|5Schema|6)', '-count=1')
    }
    Invoke-Checked 'go' @('vet', './...')
    Invoke-Checked 'go' @('tool', 'staticcheck', './...')

    Invoke-Checked 'cargo' @('fmt', '--all', '--check')
    Invoke-Checked 'cargo' @('clippy', '--workspace', '--all-targets', '--all-features', '--', '-D', 'warnings')
    Invoke-Checked 'cargo' @('test', '--workspace', '--all-features')
    if (Get-Command cargo-deny -ErrorAction SilentlyContinue) {
        Invoke-Checked 'cargo' @('deny', 'check')
    }
    else {
        Write-Warning 'cargo-deny is unavailable; dependency policy must run in the release environment'
    }

    & (Join-Path $PSScriptRoot 'scheduler-ci.test.ps1')
    Invoke-Checked 'git' @('diff', '--check')
    Invoke-Checked 'git' @('diff', '--exit-code', '--', 'api/proto', 'internal/gen', ':(glob)internal/**/*dbgen/*.go')
}
finally {
    if ($null -eq $previousRequireDocker) {
        Remove-Item Env:AJIASU_REQUIRE_DOCKER -ErrorAction SilentlyContinue
    }
    else {
        $env:AJIASU_REQUIRE_DOCKER = $previousRequireDocker
    }
    Pop-Location
}
