[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$EnvFile,
    [Parameter(Mandatory = $true)][ValidateSet('development', 'single-host', 'external')][string]$Mode,
    [int]$TimeoutSeconds = 30,
    [switch]$KeepDependencies
)
. (Join-Path $PSScriptRoot 'compose-common.ps1')
$composeDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) 'deploy/compose'
$files = Get-ComposeFiles -ComposeDirectory $composeDirectory -Mode $Mode
$arguments = Get-ComposeDockerArguments -EnvFile $EnvFile -Files $files

$controlId = (& docker @arguments ps -q control-plane 2>$null | Out-String).Trim()
if ($controlId) {
    & docker @arguments exec -T control-plane /usr/local/bin/control-plane compose drain
    if ($LASTEXITCODE -ne 0) { throw 'Drain request failed; diagnostic state was preserved' }
}
& docker @arguments stop -t $TimeoutSeconds gateway
if ($LASTEXITCODE -ne 0) { throw 'Gateway shutdown timed out; diagnostic state was preserved' }

$nodeId = [Guid]::Empty
$agentId = (& docker @arguments ps -q agent 2>$null | Out-String).Trim()
if ($agentId) {
    $statusText = (& docker @arguments exec -T agent /usr/local/bin/ajiasu-agent status 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -eq 0 -and $statusText) { $parsed = $statusText | ConvertFrom-Json; [void][Guid]::TryParse([string]$parsed.node_id, [ref]$nodeId) }
    & docker @arguments stop -t $TimeoutSeconds agent
    if ($LASTEXITCODE -ne 0) { throw 'Agent shutdown timed out; diagnostic state was preserved' }
}

if ($nodeId -ne [Guid]::Empty) {
    $candidateIds = @(& docker ps -aq --filter 'label=ajiasu.owner=control-plane')
    $inspections = @()
    foreach ($id in $candidateIds) { if ($id) { $inspections += ((& docker inspect $id | ConvertFrom-Json)[0]) } }
    $classification = Get-ComposeRunnerClassification -Containers $inspections -NodeId $nodeId
    if ($classification.Orphans.Count -gt 0) { throw 'Ambiguous or malformed AJiaSu Runner ownership detected; nothing further was removed' }
    foreach ($id in $classification.Owned) {
        & docker rm -f $id | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Owned Runner cleanup failed; remaining diagnostic state was preserved' }
    }
}

& docker @arguments stop -t $TimeoutSeconds control-plane
if ($LASTEXITCODE -ne 0) { throw 'Control Plane shutdown timed out' }
if (-not $KeepDependencies -and $Mode -ne 'external') {
    & docker @arguments stop -t $TimeoutSeconds redis postgres
    if ($LASTEXITCODE -ne 0) { throw 'Dependency shutdown timed out' }
}
Write-Host 'Compose stack stopped without removing persistent volumes.'
