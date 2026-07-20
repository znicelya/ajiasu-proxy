[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$console = Join-Path $root 'console'
foreach ($file in @('package.json', 'package-lock.json', 'tsconfig.json', 'vite.config.ts', 'src\main.tsx', 'src\api.ts', 'src\shell.tsx', 'src\styles.css')) {
    if (-not (Test-Path -LiteralPath (Join-Path $console $file) -PathType Leaf)) { throw "Console file missing: $file" }
}
$package = Get-Content -Raw (Join-Path $console 'package.json') | ConvertFrom-Json
if (-not $package.dependencies.'@fluentui/react-components') { throw 'Console must use Fluent UI React' }
$api = Get-Content -Raw (Join-Path $console 'src\api.ts')
foreach ($forbidden in @('localStorage', 'sessionStorage', 'document.cookie =')) { if ($api.Contains($forbidden)) { throw "Console API must not persist sensitive session state: $forbidden" } }
foreach ($route in @('accounts', 'account-pools', 'endpoints', 'operations', 'nodes', 'quota', 'audit-events')) { if (-not $api.Contains($route)) { throw "Console API contract missing route mapping: $route" } }
$shell = Get-Content -Raw (Join-Path $console 'src\shell.tsx')
foreach ($state in @('LoadingState', 'EmptyState', 'ErrorState', 'If-Match', 'Idempotency-Key')) { if (-not ($api + $shell).Contains($state)) { throw "Console contract missing state or concurrency behavior: $state" } }
if (-not $shell.Contains('!key.includes("secret")') -or -not $shell.Contains('key !== "credential"')) { throw 'Console table must filter secret-shaped fields' }
& (Join-Path $PSScriptRoot 'accessibility.ps1')
Write-Host 'Phase 9 Console contract passed.'
