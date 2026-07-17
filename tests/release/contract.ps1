[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
foreach ($file in @('build\compatibility-matrix.yaml','build\release-policy.yaml','docs\releases\release-notes-template.md','docs\operations\phase9-runbooks.md','.github\workflows\release-hardening.yml','scripts\phase9-release-verify.ps1')) {
    if (-not (Test-Path -LiteralPath (Join-Path $root $file) -PathType Leaf)) { throw "Release hardening file missing: $file" }
}
$policy = Get-Content -Raw (Join-Path $root 'build\release-policy.yaml')
foreach ($required in @('immutable_images: true', 'keyless_signing: true', 'source-sbom.spdx.json', 'provenance.intoto.jsonl', 'signatures.bundle.json')) { if (-not $policy.Contains($required)) { throw "Release policy missing $required" } }
$matrix = Get-Content -Raw (Join-Path $root 'build\compatibility-matrix.yaml')
foreach ($required in @('schema_versions: [11]', 'control_plane_zero_downtime: true', 'rollback_requires_backup: true', 'linux/amd64', 'linux/arm64')) { if (-not $matrix.Contains($required)) { throw "Compatibility matrix missing $required" } }
Write-Host 'Phase 9 release contract passed.'
