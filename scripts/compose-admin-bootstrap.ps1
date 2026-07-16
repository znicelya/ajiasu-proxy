[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$EnvFile, [Parameter(Mandatory = $true)][ValidateSet('development', 'single-host', 'external')][string]$Mode)
. (Join-Path $PSScriptRoot 'compose-common.ps1')
$composeDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) 'deploy/compose'
$files = Get-ComposeFiles -ComposeDirectory $composeDirectory -Mode $Mode
$arguments = Get-ComposeDockerArguments -EnvFile $EnvFile -Files $files
& docker @arguments run --rm --no-deps -e AJIASU_LOCAL_AUTH_ENABLED=true control-plane admin bootstrap
if ($LASTEXITCODE -ne 0) { throw 'Interactive administrator bootstrap failed' }
