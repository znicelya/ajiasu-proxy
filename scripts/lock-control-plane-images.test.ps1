$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lock-control-plane-images.ps1')

$expectedByTag = @{
    'postgres:17.6-alpine3.22' = 'sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94'
    'quay.io/keycloak/keycloak:26.3.2' = 'sha256:98fab020a3a490aba0978f237e2a06cd0ea42bf149c6cf10f11c0aaf27728ff2'
    'golang:1.25.12-alpine3.23' = 'sha256:cc985ef6f9c3bf9ece7488129c9abe0a150388ccdfa428d886fc709dca0b230a'
    'alpine:3.22' = 'sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce'
}
$manifestInspections = 0
$fixtureRawMode = 'valid'

function Assert-Throws {
    param(
        [Parameter(Mandatory = $true)][scriptblock] $Action,
        [Parameter(Mandatory = $true)][string] $MessagePattern
    )

    try {
        & $Action
    }
    catch {
        if ($_.Exception.Message -notmatch $MessagePattern) {
            throw "unexpected error message: $($_.Exception.Message)"
        }
        return
    }
    throw "expected failure matching: $MessagePattern"
}

function Invoke-NativeCommand {
    param(
        [Parameter(Mandatory = $true)][string] $FilePath,
        [Parameter(Mandatory = $true)][string[]] $Arguments
    )

    if ($FilePath -cne 'docker') {
        throw "unexpected fixture command: $FilePath"
    }
    $reference = $Arguments[-1]
    if ($reference -match '@sha256:') {
        $script:manifestInspections++
        return "Name: $reference"
    }
    if (-not $expectedByTag.ContainsKey($reference)) {
        throw "unexpected fixture image: $reference"
    }
    if ($Arguments -contains '--raw') {
		$manifests = @(
			@{
				digest = 'sha256:' + ('a' * 64)
				platform = @{ os = 'linux'; architecture = 'amd64' }
			},
			@{
				digest = 'sha256:' + ('b' * 64)
				platform = @{ os = 'linux'; architecture = 'arm64' }
			}
		)
		if ($fixtureRawMode -eq 'missing-arm64') {
			$manifests = @($manifests | Where-Object { $_.platform.architecture -ne 'arm64' })
		}
		$mediaType = if ($fixtureRawMode -eq 'invalid-index') {
			'application/vnd.oci.image.manifest.v1+json'
		}
		else {
			'application/vnd.oci.image.index.v1+json'
		}
        return (@{
            schemaVersion = 2
            mediaType = $mediaType
            manifests = $manifests
        } | ConvertTo-Json -Depth 10)
    }
    return "Digest: $($expectedByTag[$reference])"
}

$lockLines = foreach ($entry in $expectedByTag.GetEnumerator()) {
    $resolved = Resolve-LockedImage -Tag $entry.Key -ExpectedDigest $entry.Value
    if ($resolved -cne "$($entry.Key)@$($entry.Value)") {
        throw "fixture resolved unexpected image: $resolved"
    }
    $name = switch -Regex ($entry.Key) {
        '^postgres:' { 'POSTGRES_IMAGE'; break }
        '^quay\.io/keycloak/' { 'KEYCLOAK_IMAGE'; break }
        '^golang:' { 'GO_BUILD_IMAGE'; break }
        '^alpine:' { 'CONTROL_PLANE_RUNTIME_IMAGE'; break }
        default { throw "unmapped fixture image: $($entry.Key)" }
    }
    "$name=$resolved"
}
if ($manifestInspections -ne 8) {
    throw "fixture manifest inspections = $manifestInspections, want 8"
}

Assert-Throws -Action {
	Resolve-LockedImage -Tag 'postgres:latest' -ExpectedDigest ('sha256:' + ('c' * 64))
} -MessagePattern 'latest'

Assert-Throws -Action {
	Resolve-LockedImage -Tag 'postgres:17.6-alpine3.22' -ExpectedDigest ('sha256:' + ('c' * 64))
} -MessagePattern 'expected reviewed digest'

$fixtureRawMode = 'missing-arm64'
Assert-Throws -Action {
	Resolve-LockedImage -Tag 'postgres:17.6-alpine3.22' -ExpectedDigest $expectedByTag['postgres:17.6-alpine3.22']
} -MessagePattern 'no active linux/arm64 manifest'

$fixtureRawMode = 'invalid-index'
Assert-Throws -Action {
	Resolve-LockedImage -Tag 'postgres:17.6-alpine3.22' -ExpectedDigest $expectedByTag['postgres:17.6-alpine3.22']
} -MessagePattern 'not a schema-v2 multiarch image index'
$fixtureRawMode = 'valid'

$orderedLines = @(
    $lockLines | Sort-Object {
        switch -Regex ($_) {
            '^POSTGRES_IMAGE=' { 0; break }
            '^KEYCLOAK_IMAGE=' { 1; break }
            '^GO_BUILD_IMAGE=' { 2; break }
            '^CONTROL_PLANE_RUNTIME_IMAGE=' { 3; break }
        }
    }
)
$temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) "ajiasu-lock-test-$([guid]::NewGuid().ToString('N'))"
$lockPath = Join-Path $temporaryDirectory 'control-plane-images.lock'
New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
try {
    Write-ControlPlaneImageLock -Path $lockPath -LockLines $orderedLines
    $actual = [System.IO.File]::ReadAllText($lockPath, [System.Text.Encoding]::ASCII).Replace("`r`n", "`n")
    $expected = ($orderedLines -join "`n") + "`n"
    if ($actual -cne $expected) {
        throw 'fixture lock file content did not match validated lines'
    }

	$replacementLines = @('REPLACED=1')
	Write-ControlPlaneImageLock -Path $lockPath -LockLines $replacementLines
	$replacement = [System.IO.File]::ReadAllText($lockPath, [System.Text.Encoding]::ASCII).Replace("`r`n", "`n")
	if ($replacement -cne "REPLACED=1`n") {
		throw 'fixture lock file replacement was not atomic and exact'
	}
}
finally {
    Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force
}

$checkedInLockPath = Join-Path $PSScriptRoot '..\build\control-plane-images.lock'
$checkedInLock = [System.IO.File]::ReadAllText($checkedInLockPath, [System.Text.Encoding]::ASCII).Replace("`r`n", "`n")
$expectedCheckedInLock = ($orderedLines -join "`n") + "`n"
if ($checkedInLock -cne $expectedCheckedInLock) {
	throw 'checked-in control-plane image lock does not match the reviewed image set'
}

Write-Output 'lock-control-plane-images fixture tests passed'
