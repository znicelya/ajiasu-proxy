[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Release,
    [Parameter(Mandatory = $true)][string]$Namespace,
    [Parameter(Mandatory = $true)][string]$ValuesFile,
    [Parameter(Mandatory = $true)][string]$ControlPlaneDigest,
    [Parameter(Mandatory = $true)][string]$GatewayDigest,
    [Parameter(Mandatory = $true)][string]$AgentDigest,
    [Parameter(Mandatory = $true)][string]$RunnerDigest,
    [Parameter(Mandatory = $true)][string]$SecretName
)
. (Join-Path $PSScriptRoot 'helm-common.ps1')
Assert-HelmTooling
foreach ($item in @{'control-plane'=$ControlPlaneDigest; 'gateway'=$GatewayDigest; 'agent'=$AgentDigest; 'runner'=$RunnerDigest}.GetEnumerator()) { Assert-HelmDigest -Name $item.Key -Value $item.Value }
if (-not (Test-Path -LiteralPath $ValuesFile -PathType Leaf)) { throw "Values file is unavailable: $ValuesFile" }
Invoke-Kubectl @('get', 'namespace', $Namespace)
Invoke-Kubectl @('-n', $Namespace, 'get', 'secret', $SecretName)
Invoke-Helm @('lint', (Join-Path $PSScriptRoot '..\deploy\helm\ajiasu'), '-f', $ValuesFile, '--set', "images.controlPlane.digest=$ControlPlaneDigest", '--set', "images.gateway.digest=$GatewayDigest", '--set', "images.agent.digest=$AgentDigest", '--set', "images.runner.digest=$RunnerDigest", '--set', "migrations.image.digest=$ControlPlaneDigest", '--set', "secrets.existingSecret=$SecretName")
Write-Host "Phase 8 Helm preflight passed for $Release/$Namespace"
