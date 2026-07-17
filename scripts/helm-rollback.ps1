[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$Release, [Parameter(Mandatory = $true)][string]$Namespace, [int]$Revision = 0)
. (Join-Path $PSScriptRoot 'helm-common.ps1')
Assert-HelmTooling
if ($Revision -le 0) { throw 'An explicit compatible Helm revision is required for rollback' }
Invoke-Helm @('history', $Release, '--namespace', $Namespace)
Invoke-Helm @('rollback', $Release, $Revision.ToString(), '--namespace', $Namespace, '--wait', '--timeout', '15m')
Write-Host "Rollback to Helm revision $Revision completed; verify schema, keyring, and smoke probes before accepting traffic."
