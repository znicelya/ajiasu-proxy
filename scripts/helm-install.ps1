[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Release,
    [Parameter(Mandatory = $true)][string]$Namespace,
    [Parameter(Mandatory = $true)][string]$ValuesFile,
    [Parameter(Mandatory = $true)][string]$SecretName,
    [Parameter(Mandatory = $true)][string]$ControlPlaneDigest,
    [Parameter(Mandatory = $true)][string]$GatewayDigest,
    [Parameter(Mandatory = $true)][string]$AgentDigest,
    [Parameter(Mandatory = $true)][string]$RunnerDigest
)
. (Join-Path $PSScriptRoot 'helm-common.ps1')
Assert-HelmTooling
Invoke-Helm @('upgrade', '--install', $Release, (Join-Path $PSScriptRoot '..\deploy\helm\ajiasu'), '--namespace', $Namespace, '--create-namespace', '--values', $ValuesFile, '--set', "images.controlPlane.digest=$ControlPlaneDigest", '--set', "images.gateway.digest=$GatewayDigest", '--set', "images.agent.digest=$AgentDigest", '--set', "images.runner.digest=$RunnerDigest", '--set', "migrations.image.digest=$ControlPlaneDigest", '--set', "secrets.existingSecret=$SecretName", '--wait', '--timeout', '15m')
Wait-HelmDeployment -Namespace $Namespace -Name "$Release-ajiasu-control-plane"
Wait-HelmDeployment -Namespace $Namespace -Name "$Release-ajiasu-gateway"
Write-Host 'Phase 8 Helm installation accepted after readiness convergence.'
