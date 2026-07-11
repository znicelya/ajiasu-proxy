$ErrorActionPreference = 'Stop'

function Format-Command {
    param(
        [Parameter(Mandatory = $true)][string] $FilePath,
        [Parameter(Mandatory = $true)][string[]] $Arguments
    )

    $displayArguments = foreach ($argument in $Arguments) {
        if ($argument -match '[\s"]') {
            '"{0}"' -f ($argument -replace '"', '\"')
        }
        else {
            $argument
        }
    }

    return (@($FilePath) + $displayArguments) -join ' '
}

function Invoke-NativeCommand {
    param(
        [Parameter(Mandatory = $true)][string] $FilePath,
        [Parameter(Mandatory = $true)][string[]] $Arguments,
        [ValidateRange(1, 10)][int] $Attempts = 1
    )

    $command = Format-Command -FilePath $FilePath -Arguments $Arguments
    for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
        Write-Host "[ci] command: $command"
        if ($Attempts -gt 1) {
            Write-Host "[ci] attempt: $attempt/$Attempts"
        }

        $exitCode = $null
        $invocationError = $null
        $previousErrorActionPreference = $ErrorActionPreference
        $nativeErrorPreference = Get-Variable -Name PSNativeCommandUseErrorActionPreference -ErrorAction SilentlyContinue
        $previousNativeErrorPreference = if ($null -ne $nativeErrorPreference) { $nativeErrorPreference.Value } else { $null }

        try {
            $ErrorActionPreference = 'Continue'
            if ($null -ne $nativeErrorPreference) {
                Set-Variable -Name PSNativeCommandUseErrorActionPreference -Value $false
            }

            & $FilePath @Arguments
            $exitCode = $LASTEXITCODE
        }
        catch {
            $invocationError = $_
        }
        finally {
            if ($null -ne $nativeErrorPreference) {
                Set-Variable -Name PSNativeCommandUseErrorActionPreference -Value $previousNativeErrorPreference
            }
            $ErrorActionPreference = $previousErrorActionPreference
        }

        $exitDisplay = if ($null -eq $exitCode) { 'not-started' } else { [string] $exitCode }
        Write-Host "[ci] exit: $exitDisplay"

        if ($null -eq $invocationError -and $exitCode -eq 0) {
            return
        }

        if ($attempt -lt $Attempts) {
            Write-Warning "Command failed; retrying without changing verification requirements."
            Start-Sleep -Seconds ([Math]::Min(2 * $attempt, 5))
            continue
        }

        if ($null -ne $invocationError) {
            throw "Command failed to start: $command`n$($invocationError.Exception.Message)"
        }
        throw "Command failed with exit code ${exitCode}: $command"
    }
}

function Invoke-PowerShellScript {
    param([Parameter(Mandatory = $true)][string] $Path)

    Write-Host "[ci] command: & $Path"
    try {
        & $Path
        Write-Host '[ci] exit: 0'
    }
    catch {
        Write-Host '[ci] exit: 1'
        throw
    }
}

$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$lockPath = Join-Path $repoRoot 'runner/artifacts/alpine-3.22.lock'
$lockLines = @(
    Get-Content -LiteralPath $lockPath |
        Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
)

if ($lockLines.Count -ne 1) {
    throw "Expected exactly one nonempty line in $lockPath"
}
if ($lockLines[0] -notmatch '^ALPINE_IMAGE=(alpine:3\.22@sha256:[0-9a-f]{64})$') {
    throw "Invalid Alpine image lock in ${lockPath}: expected ALPINE_IMAGE=alpine:3.22@sha256:<64 lowercase hex characters>"
}
$alpineImage = $Matches[1]

$mount = "type=bind,source=$repoRoot,target=/workspace,readonly"
$shellTests = @'
set -eu
apk add --no-cache curl tar coreutils
/bin/sh runner/tests/fetch-ajiasu.test.sh
/bin/sh runner/tests/entrypoint.test.sh
'@
$fakeContract = @'
set -eu
umask 077
printf 'user example\npass secret\n' >/tmp/ajiasu.conf
AJIASU_BIN=/workspace/runner/testdata/fake-ajiasu.sh \
AJIASU_CONFIG=/tmp/ajiasu.conf \
    /bin/sh /workspace/tests/contract/ajiasu-contract.sh
'@

Push-Location -LiteralPath $repoRoot
try {
    Invoke-NativeCommand -FilePath 'docker' -Arguments @('pull', $alpineImage) -Attempts 3

    Invoke-NativeCommand -FilePath 'docker' -Arguments @(
        'run', '--rm', '--pull=never', '--mount', $mount,
        '--workdir', '/workspace', $alpineImage,
        '/bin/sh', '-c', $shellTests
    )

    Invoke-NativeCommand -FilePath 'docker' -Arguments @(
        'build', '--pull=false',
        '--build-arg', "ALPINE_IMAGE=$alpineImage",
        '--tag', 'ajiasu-runner:test', '.'
    )

    Invoke-PowerShellScript -Path (Join-Path $repoRoot 'runner/tests/docker-smoke.test.ps1')

    Invoke-NativeCommand -FilePath 'docker' -Arguments @(
        'run', '--rm', '--pull=never', '--mount', $mount,
        '--tmpfs', '/tmp:rw,noexec,nosuid,size=1m',
        '--workdir', '/workspace', $alpineImage,
        '/bin/sh', '-c', $fakeContract
    )
}
finally {
    Pop-Location
}
