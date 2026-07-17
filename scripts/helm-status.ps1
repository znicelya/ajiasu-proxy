[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$Release, [Parameter(Mandatory = $true)][string]$Namespace)
. (Join-Path $PSScriptRoot 'helm-common.ps1')
Assert-HelmTooling
Invoke-Helm @('status', $Release, '--namespace', $Namespace)
Invoke-Kubectl @('-n', $Namespace, 'get', 'deploy,daemonset,job,pod', '-l', "app.kubernetes.io/instance=$Release", '-o', 'wide')
