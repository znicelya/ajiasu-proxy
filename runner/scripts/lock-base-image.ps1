$ErrorActionPreference = 'Stop'

$image = 'alpine:3.22'
$digest = $null

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
        if ($attempt -eq 3) { throw }
        Start-Sleep -Seconds ([math]::Pow(2, $attempt - 1))
    }
}

$lockLine = "ALPINE_IMAGE=${image}@${digest}"
if ($lockLine -notmatch '^ALPINE_IMAGE=alpine:3\.22@sha256:[0-9a-f]{64}$') {
    throw "refusing to write invalid Alpine image lock: $lockLine"
}

$artifactDirectory = Join-Path $PSScriptRoot '..\artifacts'
$lockPath = Join-Path $artifactDirectory 'alpine-3.22.lock'
New-Item -ItemType Directory -Force -Path $artifactDirectory | Out-Null
Set-Content -LiteralPath $lockPath -Value $lockLine -Encoding ascii

$writtenLine = (Get-Content -LiteralPath $lockPath -Raw).TrimEnd("`r", "`n")
if ($writtenLine -cne $lockLine) {
    throw "written Alpine image lock did not match the resolved value"
}

Write-Output $lockLine
