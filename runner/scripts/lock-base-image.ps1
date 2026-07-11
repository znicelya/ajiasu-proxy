$ErrorActionPreference = 'Stop'

$image = 'alpine:3.22'
$tagApi = 'https://hub.docker.com/v2/repositories/library/alpine/tags/3.22'
$digest = $null

function Get-ValidatedAlpineDigest {
    param(
        [Parameter(Mandatory = $true)] $Tag,
        [string] $PrimaryDigest
    )

    if ($Tag.tag_status -ne 'active') {
        throw "Docker Hub reports alpine:3.22 tag status '$($Tag.tag_status)' instead of active"
    }

    $apiDigest = [string] $Tag.digest
    if ($apiDigest -notmatch '^sha256:[0-9a-fA-F]{64}$') {
        throw "Docker Hub returned an invalid multi-arch digest for alpine:3.22: $apiDigest"
    }
    $apiDigest = $apiDigest.ToLowerInvariant()

    $activeLinuxAmd64 = @($Tag.images | Where-Object {
        $_.status -eq 'active' -and $_.os -eq 'linux' -and $_.architecture -eq 'amd64'
    })
    $activeLinuxArm64 = @($Tag.images | Where-Object {
        $_.status -eq 'active' -and $_.os -eq 'linux' -and $_.architecture -eq 'arm64'
    })
    if ($activeLinuxAmd64.Count -eq 0 -or $activeLinuxArm64.Count -eq 0) {
        throw 'Docker Hub did not return active linux/amd64 and linux/arm64 images for alpine:3.22'
    }

    if ($PrimaryDigest) {
        $normalizedPrimaryDigest = $PrimaryDigest.ToLowerInvariant()
        if ($normalizedPrimaryDigest -cne $apiDigest) {
            throw "official registry digest $normalizedPrimaryDigest did not match Docker Hub Tag API digest $apiDigest"
        }
        return $normalizedPrimaryDigest
    }

    return $apiDigest
}

for ($attempt = 1; $attempt -le 3; $attempt++) {
    try {
        $inspection = docker buildx imagetools inspect $image 2>&1
        if ($LASTEXITCODE -ne 0) {
            throw "docker buildx imagetools inspect failed with exit code $LASTEXITCODE`n$($inspection -join "`n")"
        }

        $digestLines = @($inspection | Where-Object { $_ -match '^Digest:\s+(sha256:[0-9a-fA-F]{64})\s*$' })
        if ($digestLines.Count -ne 1) {
            throw "expected exactly one multi-arch manifest digest for ${image}, found $($digestLines.Count)"
        }

        $digest = [regex]::Match($digestLines[0], '^Digest:\s+(sha256:[0-9a-fA-F]{64})\s*$').Groups[1].Value.ToLowerInvariant()
        break
    }
    catch {
        if ($attempt -eq 3) {
            Write-Warning "official registry inspection failed after $attempt attempts; using the official Docker Hub Tag API: $($_.Exception.Message)"
            break
        }
        Start-Sleep -Seconds ([math]::Pow(2, $attempt - 1))
    }
}

$tag = Invoke-RestMethod -Uri $tagApi -Method Get
$digest = Get-ValidatedAlpineDigest -Tag $tag -PrimaryDigest $digest

$lockLine = "ALPINE_IMAGE=${image}@${digest}"
if ($lockLine -notmatch '^ALPINE_IMAGE=alpine:3\.22@sha256:[0-9a-f]{64}$') {
    throw "refusing to write invalid Alpine image lock: $lockLine"
}

$artifactDirectory = Join-Path $PSScriptRoot '..\artifacts'
$lockPath = Join-Path $artifactDirectory 'alpine-3.22.lock'
$temporaryPath = "$lockPath.tmp.$PID.$([guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Force -Path $artifactDirectory | Out-Null
try {
    Set-Content -LiteralPath $temporaryPath -Value $lockLine -Encoding ascii

    $writtenContent = Get-Content -LiteralPath $temporaryPath -Raw
    if ($writtenContent -notmatch '^ALPINE_IMAGE=alpine:3\.22@sha256:[0-9a-f]{64}\r?\n$') {
        throw "temporary Alpine image lock did not contain exactly one valid line"
    }
    if ($writtenContent.TrimEnd("`r", "`n") -cne $lockLine) {
        throw "temporary Alpine image lock did not match the resolved value"
    }

    Move-Item -LiteralPath $temporaryPath -Destination $lockPath -Force
}
finally {
    if (Test-Path -LiteralPath $temporaryPath) {
        Remove-Item -LiteralPath $temporaryPath -Force
    }
}

Write-Output $lockLine
