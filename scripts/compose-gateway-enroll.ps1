[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$EnvFile, [Parameter(Mandatory = $true)][ValidateSet('development', 'single-host', 'external')][string]$Mode, [Parameter(Mandatory = $true)][string]$Name, [string]$GeneratedDir)
. (Join-Path $PSScriptRoot 'compose-common.ps1')
$composeDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) 'deploy/compose'
if (-not $GeneratedDir) { $GeneratedDir = Join-Path $composeDirectory 'generated' }
$output = Join-Path $GeneratedDir 'gateway-enrollment-token'
if (Test-Path -LiteralPath $output) { throw 'A Gateway enrollment token already exists; it will not be rotated implicitly' }
$files = Get-ComposeFiles -ComposeDirectory $composeDirectory -Mode $Mode
$arguments = Get-ComposeDockerArguments -EnvFile $EnvFile -Files $files
$fingerprintFile = (Resolve-Path -LiteralPath (Join-Path $GeneratedDir 'gateway-certificate-fingerprint')).Path
$token = (& docker @arguments run --rm --no-deps -v ($fingerprintFile + ':/run/ajiasu-gateway-fingerprint:ro') control-plane compose enroll-gateway --name $Name --fingerprint-file /run/ajiasu-gateway-fingerprint --output - | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $token -notmatch '^gwe_[A-Za-z0-9_-]+$') { throw 'Gateway enrollment failed without exposing its token' }
Write-ComposePrivateFile -Path $output -Value $token
Write-Host 'Gateway enrollment token was written to generated state and was not displayed.'
