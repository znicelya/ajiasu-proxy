[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$ArtifactDirectory)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (-not (Test-Path -LiteralPath $ArtifactDirectory -PathType Container)) { throw 'Release artifact directory is unavailable' }
foreach ($name in @('release-manifest.json','source-sbom.spdx.json','provenance.intoto.jsonl','signatures.bundle.json')) {
    $path = Join-Path $ArtifactDirectory $name
    if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or (Get-Item -LiteralPath $path).Length -eq 0) { throw "Required release artifact missing or empty: $name" }
}
$manifest = Get-Content -Raw (Join-Path $ArtifactDirectory 'release-manifest.json') | ConvertFrom-Json
if (-not $manifest.images) { throw 'Release manifest has no images' }
foreach ($property in $manifest.images.PSObject.Properties) {
    $value = [string]$property.Value
    if ($value -notmatch '^[^\s:@]+(?:/[^\s:@]+)+@sha256:[0-9a-f]{64}$') { throw "Image $($property.Name) is not immutable" }
}
foreach ($file in Get-ChildItem -LiteralPath $ArtifactDirectory -File) {
    if ($file.Name -match '(?i)(\.pem$|\.key$|password|secret)') { throw "Secret-bearing filename is forbidden in release artifacts: $($file.Name)" }
}
$sbom = Get-Content -Raw (Join-Path $ArtifactDirectory 'source-sbom.spdx.json') | ConvertFrom-Json
if (-not $sbom.spdxVersion -or -not $sbom.packages) { throw 'SPDX SBOM is incomplete' }
$bundle = Get-Content -Raw (Join-Path $ArtifactDirectory 'signatures.bundle.json') | ConvertFrom-Json
if (-not $bundle.mediaType -or -not $bundle.verificationMaterial) { throw 'Signature bundle is incomplete' }
Write-Host 'Phase 9 release artifacts passed immutable image, SBOM, provenance, signature, and redaction checks.'
