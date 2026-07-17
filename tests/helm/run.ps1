[CmdletBinding()]
param([switch]$Cluster, [string]$Namespace = 'ajiasu-phase8')
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
foreach ($tool in @('helm')) { if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) { throw "$tool is required" } }
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$chart = Join-Path $root 'deploy\helm\ajiasu'
$out = Join-Path ([IO.Path]::GetTempPath()) ('ajiasu-helm-' + [Guid]::NewGuid().ToString('N') + '.yaml')
$digest = 'sha256:' + ('a' * 64)
try {
    & helm template phase8 $chart --namespace $Namespace --set "images.controlPlane.digest=$digest" --set "images.gateway.digest=$digest" --set "images.agent.digest=$digest" --set "images.runner.digest=$digest" --set "migrations.image.digest=$digest" --set secrets.existingSecret=ajiasu-runtime --set gateway.config.certificateFingerprint=fixture --set postgres.external.host=postgres.example.test --set redis.external.host=redis.example.test > $out
    if ($LASTEXITCODE -ne 0) { throw 'helm template failed' }
    & (Join-Path $PSScriptRoot 'security.ps1') -ManifestPath $out
    if ($Cluster) {
        foreach ($tool in @('kubectl', 'kind')) { if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) { throw "$tool is required for -Cluster" } }
        $clusterName = 'ajiasu-phase8-' + [Guid]::NewGuid().ToString('N').Substring(0, 8)
        & kind create cluster --name $clusterName --wait 120s
        try {
            & kubectl create namespace $Namespace --dry-run=client -o yaml | kubectl apply -f -
            & kubectl -n $Namespace create secret generic ajiasu-runtime --from-literal=database-normal-dsn=fixture --from-literal=database-platform-dsn=fixture --from-literal=database-migration-dsn=fixture --from-literal=redis-password=fixture --from-literal=oidc-client-secret=fixture --from-literal=control-plane-keyring=fixture --from-literal=route-signing-key=fixture --from-literal=route-verifying-key=fixture --from-literal=platform-ca=fixture --from-literal=control-plane-cert=fixture --from-literal=control-plane-key=fixture --from-literal=gateway-cert=fixture --from-literal=gateway-key=fixture --from-literal=agent-cert=fixture --from-literal=agent-key=fixture --from-literal=agent-relay-cert=fixture --from-literal=agent-relay-key=fixture
            & kubectl apply --dry-run=server -f $out
            if ($LASTEXITCODE -ne 0) { throw 'Kubernetes server-side dry-run failed' }
        } finally { & kind delete cluster --name $clusterName }
    }
} finally { if (Test-Path -LiteralPath $out) { Remove-Item -LiteralPath $out -Force } }
