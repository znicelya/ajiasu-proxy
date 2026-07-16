[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$EnvFile, [Parameter(Mandatory = $true)][ValidateSet('development', 'single-host', 'external')][string]$Mode)
. (Join-Path $PSScriptRoot 'compose-common.ps1')
$composeDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) 'deploy/compose'
$files = Get-ComposeFiles -ComposeDirectory $composeDirectory -Mode $Mode
$arguments = Get-ComposeDockerArguments -EnvFile $EnvFile -Files $files
$components = [ordered]@{}
foreach ($service in @('postgres', 'redis', 'migration', 'control-plane', 'agent', 'gateway')) {
    if ($Mode -eq 'external' -and $service -in @('postgres', 'redis')) { $components[$service] = 'external'; continue }
    $id = (& docker @arguments ps -q $service 2>$null | Out-String).Trim()
    if (-not $id) { $components[$service] = 'stopped'; continue }
    $components[$service] = (& docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' $id 2>$null | Out-String).Trim()
}
$runtime = $null
if ($components['control-plane'] -eq 'healthy' -or $components['control-plane'] -eq 'running') {
    $runtimeText = (& docker @arguments exec -T control-plane /usr/local/bin/control-plane compose runtime-status 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -eq 0 -and $runtimeText) { $runtime = $runtimeText | ConvertFrom-Json }
}
$ready = $components['control-plane'] -eq 'healthy' -and $components['agent'] -eq 'healthy' -and $components['gateway'] -eq 'healthy' -and
    $runtime -and $runtime.nodes.online -ge 1 -and $runtime.nodes.sessions -ge 1 -and $runtime.gateways.online -ge 1 -and $runtime.gateways.sessions -ge 1 -and
    $runtime.assignments.fixed_assigned -ge 1 -and $runtime.assignments.pool_assigned -ge 1
$output = [ordered]@{ components = $components; runtime = $runtime; status = if ($ready) { 'ready' } elseif ($components['control-plane'] -eq 'healthy') { 'degraded' } else { 'not_ready' } }
$output | ConvertTo-Json -Depth 6 -Compress
