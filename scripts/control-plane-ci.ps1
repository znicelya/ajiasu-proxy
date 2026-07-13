$ErrorActionPreference = 'Stop'

function Format-Command {
    param(
        [Parameter(Mandatory = $true)][string] $FilePath,
        [Parameter(Mandatory = $true)][string[]] $Arguments
    )

    return (@($FilePath) + $Arguments) -join ' '
}

function Invoke-NativeCommand {
    param(
        [Parameter(Mandatory = $true)][string] $FilePath,
        [Parameter(Mandatory = $true)][string[]] $Arguments
    )

    $resolvedCommands = @(Get-Command -Name $FilePath -CommandType Application -ErrorAction Stop)
    $resolvedPath = $resolvedCommands[0].Source
    if ([string]::IsNullOrWhiteSpace($resolvedPath)) {
        $resolvedPath = $resolvedCommands[0].Path
    }
    if ([string]::IsNullOrWhiteSpace($resolvedPath)) {
        throw "Resolved command has no executable path: $FilePath"
    }

    $command = Format-Command -FilePath $resolvedPath -Arguments $Arguments
    Write-Host "[control-plane-ci] command: $command"
    $previousErrorActionPreference = $ErrorActionPreference
    $nativeErrorPreference = Get-Variable -Name PSNativeCommandUseErrorActionPreference -ErrorAction SilentlyContinue
    $previousNativeErrorPreference = if ($null -ne $nativeErrorPreference) { $nativeErrorPreference.Value } else { $null }
    try {
        $ErrorActionPreference = 'Continue'
        if ($null -ne $nativeErrorPreference) {
            Set-Variable -Name PSNativeCommandUseErrorActionPreference -Value $false
        }
        $global:LASTEXITCODE = $null
        & $resolvedPath @Arguments
        $exitCode = $global:LASTEXITCODE
    }
    finally {
        if ($null -ne $nativeErrorPreference) {
            Set-Variable -Name PSNativeCommandUseErrorActionPreference -Value $previousNativeErrorPreference
        }
        $ErrorActionPreference = $previousErrorActionPreference
    }

    $exitDisplay = if ($null -eq $exitCode) { 'not-started' } else { [string] $exitCode }
    Write-Host "[control-plane-ci] exit: $exitDisplay"
    if ($null -eq $exitCode) {
        throw "Command did not provide a native exit code: $command"
    }
    if ($exitCode -ne 0) {
        throw "Command failed with exit code ${exitCode}: $command"
    }
}

function Invoke-PowerShellScript {
    param([Parameter(Mandatory = $true)][string] $Path)

    Write-Host "[control-plane-ci] command: & $Path"
    try {
        & $Path
        Write-Host '[control-plane-ci] exit: 0'
    }
    catch {
        Write-Host '[control-plane-ci] exit: 1'
        throw
    }
}

$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$previousRequireDocker = $env:AJIASU_REQUIRE_DOCKER

Push-Location -LiteralPath $repoRoot
try {
    Invoke-NativeCommand -FilePath 'go' -Arguments @('mod', 'tidy', '-diff')
    Invoke-PowerShellScript -Path (Join-Path $repoRoot 'scripts/control-plane-ci.test.ps1')
    Invoke-PowerShellScript -Path (Join-Path $repoRoot 'scripts/lock-control-plane-images.test.ps1')
    Invoke-NativeCommand -FilePath 'go' -Arguments @('tool', 'sqlc', 'vet')
    Invoke-NativeCommand -FilePath 'go' -Arguments @('tool', 'sqlc', 'diff')

    $env:AJIASU_REQUIRE_DOCKER = '1'
    Invoke-NativeCommand -FilePath 'go' -Arguments @('test', '-race', './...')
    Invoke-NativeCommand -FilePath 'go' -Arguments @('vet', './...')
    Invoke-NativeCommand -FilePath 'go' -Arguments @('tool', 'staticcheck', './...')
}
finally {
    if ($null -eq $previousRequireDocker) {
        Remove-Item Env:AJIASU_REQUIRE_DOCKER -ErrorAction SilentlyContinue
    }
    else {
        $env:AJIASU_REQUIRE_DOCKER = $previousRequireDocker
    }
    Pop-Location
}
