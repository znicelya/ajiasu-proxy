[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$EnvFile, [Parameter(Mandatory = $true)][ValidateSet('development', 'single-host', 'external')][string]$Mode, [Parameter(Mandatory = $true)][string]$Name, [string]$GeneratedDir)
. (Join-Path $PSScriptRoot 'compose-common.ps1')
$composeDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) 'deploy/compose'
if (-not $GeneratedDir) { $GeneratedDir = Join-Path $composeDirectory 'generated' }
$output = Join-Path $GeneratedDir 'agent-enrollment-token'
if (Test-Path -LiteralPath $output) { throw 'An Agent enrollment token already exists; it will not be rotated implicitly' }
$files = Get-ComposeFiles -ComposeDirectory $composeDirectory -Mode $Mode
$arguments = Get-ComposeDockerArguments -EnvFile $EnvFile -Files $files
$token = (& docker @arguments run --rm --no-deps control-plane compose enroll-agent --name $Name --output - | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $token -notmatch '^nse_[A-Za-z0-9_-]+$') { throw 'Agent enrollment failed without exposing its token' }
Write-ComposePrivateFile -Path $output -Value $token
Write-Host 'Agent enrollment token was written to generated state and was not displayed.'
