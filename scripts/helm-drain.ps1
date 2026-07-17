[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$Node, [Parameter(Mandatory = $true)][string]$Namespace, [switch]$Force)
. (Join-Path $PSScriptRoot 'helm-common.ps1')
Assert-HelmTooling
Invoke-Kubectl @('cordon', $Node)
$args = @('drain', $Node, '--ignore-daemonsets', '--delete-emptydir-data', '--grace-period=90', '--timeout=15m')
if ($Force) { $args += '--force' }
Invoke-Kubectl $args
Write-Host "Node $Node drained; Agent DaemonSet will reconcile after it returns."
