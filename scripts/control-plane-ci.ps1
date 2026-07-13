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

$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path

Push-Location -LiteralPath $repoRoot
try {
    Invoke-NativeCommand -FilePath 'go' -Arguments @('mod', 'tidy')
    Invoke-NativeCommand -FilePath 'git' -Arguments @('diff', '--exit-code', '--', 'go.mod', 'go.sum')

    Invoke-NativeCommand -FilePath 'go' -Arguments @('test', '-race', './...')
    Invoke-NativeCommand -FilePath 'go' -Arguments @('vet', './...')
    Invoke-NativeCommand -FilePath 'go' -Arguments @('tool', 'staticcheck', './...')
}
finally {
    Pop-Location
}
