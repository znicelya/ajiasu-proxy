[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$shell = Get-Content -Raw (Join-Path $root 'console\src\shell.tsx')
$styles = Get-Content -Raw (Join-Path $root 'console\src\styles.css')
$html = Get-Content -Raw (Join-Path $root 'console\index.html')

foreach ($required in @(
    '<html lang="en">',
    'aria-label="Primary navigation"',
    'href="#main-content"',
    'id="main-content"',
    'tabIndex={-1}'
)) {
    if (-not ($html + $shell).Contains($required)) {
        throw "Console accessibility contract missing: $required"
    }
}

foreach ($requiredStyle in @(':focus-visible', '.skip-link:focus', '.sr-only', 'prefers-reduced-motion')) {
    if (-not $styles.Contains($requiredStyle)) {
        throw "Console accessibility style missing: $requiredStyle"
    }
}

Write-Host 'Phase 9 Console accessibility contract passed.'
