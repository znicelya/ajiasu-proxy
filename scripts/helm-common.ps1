[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-HelmTooling {
    foreach ($name in @('helm', 'kubectl')) {
        if (-not (Get-Command $name -ErrorAction SilentlyContinue)) { throw "$name is required for Phase 8 operations" }
    }
}

function Assert-HelmDigest {
    param([Parameter(Mandatory = $true)][string]$Name, [Parameter(Mandatory = $true)][string]$Value)
    if ($Value -notmatch '^sha256:[0-9a-f]{64}$') { throw "$Name must be a sha256 digest" }
}

function Invoke-Helm {
    param([Parameter(Mandatory = $true)][string[]]$Arguments)
    & helm @Arguments
    if ($LASTEXITCODE -ne 0) { throw "helm failed with exit code $LASTEXITCODE" }
}

function Invoke-Kubectl {
    param([Parameter(Mandatory = $true)][string[]]$Arguments)
    & kubectl @Arguments
    if ($LASTEXITCODE -ne 0) { throw "kubectl failed with exit code $LASTEXITCODE" }
}

function Wait-HelmDeployment {
    param([Parameter(Mandatory = $true)][string]$Namespace, [Parameter(Mandatory = $true)][string]$Name, [int]$TimeoutSeconds = 600)
    Invoke-Kubectl @('-n', $Namespace, 'rollout', 'status', "deployment/$Name", "--timeout=${TimeoutSeconds}s")
}
