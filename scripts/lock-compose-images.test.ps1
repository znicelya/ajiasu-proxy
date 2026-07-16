$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lock-compose-images.ps1')

$specifications = @(Get-ComposeImageSpecifications)
$expectedByTag = @{}
foreach ($specification in $specifications) {
    $expectedByTag[$specification.Tag] = $specification.Digest
}
$rawMode = 'valid'
$failTag = ''

function Assert-Throws {
    param([scriptblock] $Action, [string] $MessagePattern)
    try { & $Action } catch {
        if ($_.Exception.Message -notmatch $MessagePattern) {
            throw "unexpected error: $($_.Exception.Message)"
        }
        return
    }
    throw "expected failure matching $MessagePattern"
}

function Invoke-NativeCommand {
    param([string] $FilePath, [string[]] $Arguments)
    if ($FilePath -cne 'docker') { throw "unexpected command: $FilePath" }
    $reference = $Arguments[-1]
    if ($reference -match '@sha256:') { return "Name: $reference" }
    if ($reference -ceq $failTag) { throw "fixture registry failure for $reference" }
    if (-not $expectedByTag.ContainsKey($reference)) { throw "unexpected image: $reference" }
    if ($Arguments -contains '--raw') {
        $manifests = @(
            @{ digest = 'sha256:' + ('a' * 64); platform = @{ os = 'linux'; architecture = 'amd64' } },
            @{ digest = 'sha256:' + ('b' * 64); platform = @{ os = 'linux'; architecture = 'arm64' } }
        )
        if ($rawMode -eq 'missing-arm64') {
            $manifests = @($manifests | Where-Object { $_.platform.architecture -ne 'arm64' })
        }
        return (@{ schemaVersion = 2; mediaType = 'application/vnd.oci.image.index.v1+json'; manifests = $manifests } | ConvertTo-Json -Depth 8)
    }
    return "Digest: $($expectedByTag[$reference])"
}

$temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) "ajiasu-compose-lock-$([guid]::NewGuid().ToString('N'))"
$lockPath = Join-Path $temporaryDirectory 'compose-images.lock'
New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
try {
    $lines = @(Invoke-ComposeImageLock -Path $lockPath)
    $expected = (($specifications | ForEach-Object { "$($_.Name)=$($_.Tag)@$($_.Digest)" }) -join "`n") + "`n"
    $actual = [System.IO.File]::ReadAllText($lockPath, [System.Text.Encoding]::ASCII).Replace("`r`n", "`n")
    if ($actual -cne $expected -or ($lines -join "`n") + "`n" -cne $expected) {
        throw 'compose lock content is not exact and deterministic'
    }

    $rawMode = 'missing-arm64'
    Assert-Throws -Action { Invoke-ComposeImageLock -Path $lockPath } -MessagePattern 'no active linux/arm64 manifest'
    $rawMode = 'valid'

    [System.IO.File]::WriteAllText($lockPath, "ORIGINAL=1`n", [System.Text.Encoding]::ASCII)
    $failTag = 'redis:8.2.3-alpine3.22'
    Assert-Throws -Action { Invoke-ComposeImageLock -Path $lockPath } -MessagePattern 'fixture registry failure'
    $afterFailure = [System.IO.File]::ReadAllText($lockPath, [System.Text.Encoding]::ASCII).Replace("`r`n", "`n")
    if ($afterFailure -cne "ORIGINAL=1`n") {
        throw 'failed lock update did not roll back to the original file'
    }
}
finally {
    Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force
}

$checkedIn = [System.IO.File]::ReadAllText((Join-Path $PSScriptRoot '..\build\compose-images.lock'), [System.Text.Encoding]::ASCII).Replace("`r`n", "`n")
$expectedCheckedIn = (($specifications | ForEach-Object { "$($_.Name)=$($_.Tag)@$($_.Digest)" }) -join "`n") + "`n"
if ($checkedIn -cne $expectedCheckedIn) {
    throw 'checked-in compose image lock differs from reviewed specifications'
}

Write-Output 'lock-compose-images fixture tests passed'
