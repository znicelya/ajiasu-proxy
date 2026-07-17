[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$ManifestPath)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$manifest = [IO.File]::ReadAllText((Resolve-Path -LiteralPath $ManifestPath))
foreach ($forbidden in @('privileged: true', 'hostNetwork: true', 'hostPID: true', 'hostIPC: true', 'AJIASU_DATABASE_NORMAL_DSN:')) {
    if ($manifest -match [regex]::Escape($forbidden)) { throw "Rendered Helm manifest contains forbidden security setting: $forbidden" }
}
$daemonSet = [regex]::Match($manifest, '(?ms)kind: DaemonSet.*?(?=\n---|\z)').Value
if (-not $daemonSet -or ([regex]::Matches($daemonSet, 'hostPath:').Count -ne 2)) { throw 'Agent must own exactly the runtime socket and node state hostPath mounts' }
$nonAgent = $manifest -replace [regex]::Escape($daemonSet), ''
if ($nonAgent -match 'hostPath:') { throw 'non-Agent workload contains a hostPath mount' }
if ($manifest -notmatch 'readOnlyRootFilesystem: true') { throw 'Runner read-only root filesystem assertion is missing' }
if ($manifest -notmatch 'automountServiceAccountToken: false') { throw 'Runner service-account token assertion is missing' }
if ($manifest -notmatch 'capabilities:\s*\{ drop: \[ALL\] \}') { throw 'Runner capability drop assertion is missing' }
Write-Host 'Phase 8 rendered manifest security checks passed.'
