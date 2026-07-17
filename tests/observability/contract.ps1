[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
foreach ($file in @('deploy\observability\prometheus\recording-rules.yaml','deploy\observability\prometheus\alert-rules.yaml','deploy\observability\grafana\ajiasu-overview.json','deploy\observability\otel\collector-config.yaml','deploy\observability\siem\audit-export.schema.json')) {
    if (-not (Test-Path -LiteralPath (Join-Path $root $file) -PathType Leaf)) { throw "Observability asset missing: $file" }
}
$combined = (Get-Content -Raw (Join-Path $root 'deploy\observability\prometheus\recording-rules.yaml')) + (Get-Content -Raw (Join-Path $root 'deploy\observability\prometheus\alert-rules.yaml')) + (Get-Content -Raw (Join-Path $root 'deploy\observability\otel\collector-config.yaml'))
foreach ($forbidden in @('tenant_id', 'account_id', 'password', 'secret')) { if ($combined -match [regex]::Escape($forbidden)) { throw "Observability labels or config contain forbidden sensitive key: $forbidden" } }
$schema = Get-Content -Raw (Join-Path $root 'deploy\observability\siem\audit-export.schema.json') | ConvertFrom-Json
if ($schema.properties.export_version.const -ne 1) { throw 'Audit export schema revision must be 1' }
$dashboard = Get-Content -Raw (Join-Path $root 'deploy\observability\grafana\ajiasu-overview.json') | ConvertFrom-Json
if ($dashboard.panels.Count -lt 3) { throw 'Grafana dashboard must contain operational panels' }
Write-Host 'Phase 9 observability contract passed.'
