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
    Write-Host "[lock-control-plane-images] command: $command"
    $previousErrorActionPreference = $ErrorActionPreference
    $nativeErrorPreference = Get-Variable -Name PSNativeCommandUseErrorActionPreference -ErrorAction SilentlyContinue
    $previousNativeErrorPreference = if ($null -ne $nativeErrorPreference) { $nativeErrorPreference.Value } else { $null }
    try {
        $ErrorActionPreference = 'Continue'
        if ($null -ne $nativeErrorPreference) {
            Set-Variable -Name PSNativeCommandUseErrorActionPreference -Value $false
        }
        $global:LASTEXITCODE = $null
        $output = @(& $resolvedPath @Arguments 2>&1)
        $exitCode = $global:LASTEXITCODE
    }
    finally {
        if ($null -ne $nativeErrorPreference) {
            Set-Variable -Name PSNativeCommandUseErrorActionPreference -Value $previousNativeErrorPreference
        }
        $ErrorActionPreference = $previousErrorActionPreference
    }

    $exitDisplay = if ($null -eq $exitCode) { 'not-started' } else { [string] $exitCode }
    Write-Host "[lock-control-plane-images] exit: $exitDisplay"
    if ($null -eq $exitCode) {
        throw "Command did not provide a native exit code: $command"
    }
    if ($exitCode -ne 0) {
        throw "Command failed with exit code ${exitCode}: $command`n$($output -join "`n")"
    }
    return $output
}

function Get-RepositoryName {
    param([Parameter(Mandatory = $true)][string] $Tag)

    $lastSlash = $Tag.LastIndexOf('/')
    $lastColon = $Tag.LastIndexOf(':')
    if ($lastColon -gt $lastSlash) {
        return $Tag.Substring(0, $lastColon)
    }
    return $Tag
}

function Resolve-LockedImage {
    param(
        [Parameter(Mandatory = $true)][string] $Tag,
        [Parameter(Mandatory = $true)][string] $ExpectedDigest
    )

    if ($Tag -match '(^|:)latest$') {
        throw "Refusing to lock an unversioned latest tag: $Tag"
    }
    if ($ExpectedDigest -notmatch '^sha256:[0-9a-f]{64}$') {
        throw "Invalid expected digest for ${Tag}: $ExpectedDigest"
    }

    $inspection = Invoke-NativeCommand -FilePath 'docker' -Arguments @('buildx', 'imagetools', 'inspect', $Tag)
    $digestLines = @($inspection | Where-Object { $_ -match '^Digest:\s+(sha256:[0-9a-fA-F]{64})\s*$' })
    if ($digestLines.Count -ne 1) {
        throw "Expected exactly one top-level multiarch digest for ${Tag}, found $($digestLines.Count)"
    }
    $resolvedDigest = [regex]::Match($digestLines[0], '^Digest:\s+(sha256:[0-9a-fA-F]{64})\s*$').Groups[1].Value.ToLowerInvariant()
    if ($resolvedDigest -cne $ExpectedDigest) {
        throw "Tag ${Tag} resolved to ${resolvedDigest}, expected reviewed digest ${ExpectedDigest}"
    }

    $rawOutput = Invoke-NativeCommand -FilePath 'docker' -Arguments @('buildx', 'imagetools', 'inspect', '--raw', $Tag)
    try {
        $index = ($rawOutput -join "`n") | ConvertFrom-Json
    }
    catch {
        throw "Registry returned invalid manifest JSON for ${Tag}: $($_.Exception.Message)"
    }
    if ($index.schemaVersion -ne 2 -or $index.mediaType -notmatch '(image\.index|manifest\.list)') {
        throw "Tag ${Tag} is not a schema-v2 multiarch image index"
    }

    $repository = Get-RepositoryName -Tag $Tag
    foreach ($architecture in @('amd64', 'arm64')) {
        $manifests = @($index.manifests | Where-Object {
            $_.platform.os -eq 'linux' -and $_.platform.architecture -eq $architecture
        })
        if ($manifests.Count -eq 0) {
            throw "Tag ${Tag} has no active linux/${architecture} manifest"
        }
        foreach ($manifest in $manifests) {
            $manifestDigest = [string] $manifest.digest
            if ($manifestDigest -notmatch '^sha256:[0-9a-fA-F]{64}$') {
                throw "Tag ${Tag} returned an invalid linux/${architecture} manifest digest: $manifestDigest"
            }
            $null = Invoke-NativeCommand -FilePath 'docker' -Arguments @(
                'buildx', 'imagetools', 'inspect', "${repository}@$($manifestDigest.ToLowerInvariant())"
            )
        }
    }

    return "${Tag}@${resolvedDigest}"
}

function Write-ControlPlaneImageLock {
    param(
        [Parameter(Mandatory = $true)][string] $Path,
        [Parameter(Mandatory = $true)][string[]] $LockLines
    )

    $expectedContent = ($LockLines -join "`n") + "`n"
    $directory = Split-Path -Parent $Path
    $temporaryPath = "$Path.tmp.$PID.$([guid]::NewGuid().ToString('N'))"
    $backupPath = "$Path.bak.$PID.$([guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
    try {
        [System.IO.File]::WriteAllText($temporaryPath, $expectedContent, [System.Text.Encoding]::ASCII)
        $writtenContent = [System.IO.File]::ReadAllText($temporaryPath, [System.Text.Encoding]::ASCII).Replace("`r`n", "`n")
        if ($writtenContent -cne $expectedContent) {
            throw 'Temporary control-plane image lock did not match the validated image set'
        }
        if (Test-Path -LiteralPath $Path) {
            [System.IO.File]::Replace($temporaryPath, $Path, $backupPath, $true)
            Remove-Item -LiteralPath $backupPath -Force
        }
        else {
            [System.IO.File]::Move($temporaryPath, $Path)
        }
    }
    finally {
        if (Test-Path -LiteralPath $temporaryPath) {
            Remove-Item -LiteralPath $temporaryPath -Force
        }
        if (Test-Path -LiteralPath $backupPath) {
            Remove-Item -LiteralPath $backupPath -Force
        }
    }
}

function Invoke-ControlPlaneImageLock {
    $images = @(
        @{
            Name = 'POSTGRES_IMAGE'
            Tag = 'postgres:17.6-alpine3.22'
            Digest = 'sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94'
        },
        @{
            Name = 'KEYCLOAK_IMAGE'
            Tag = 'quay.io/keycloak/keycloak:26.3.2'
            Digest = 'sha256:98fab020a3a490aba0978f237e2a06cd0ea42bf149c6cf10f11c0aaf27728ff2'
        },
        @{
            Name = 'GO_BUILD_IMAGE'
            Tag = 'golang:1.25.12-alpine3.23'
            Digest = 'sha256:cc985ef6f9c3bf9ece7488129c9abe0a150388ccdfa428d886fc709dca0b230a'
        },
        @{
            Name = 'CONTROL_PLANE_RUNTIME_IMAGE'
            Tag = 'alpine:3.22'
            Digest = 'sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce'
        }
    )

    $lockLines = foreach ($image in $images) {
        $lockedImage = Resolve-LockedImage -Tag $image.Tag -ExpectedDigest $image.Digest
        "$($image.Name)=$lockedImage"
    }
    $lockPath = Join-Path $PSScriptRoot '..\build\control-plane-images.lock'
    Write-ControlPlaneImageLock -Path $lockPath -LockLines $lockLines
    $lockLines | Write-Output
}

if ($MyInvocation.InvocationName -ne '.') {
    Invoke-ControlPlaneImageLock
}
