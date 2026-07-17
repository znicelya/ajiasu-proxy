$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lock-control-plane-images.ps1')

function Get-ComposeImageSpecifications {
    return @(
        @{ Name = 'POSTGRES_IMAGE'; Tag = 'postgres:17.6-alpine3.22'; Digest = 'sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94' },
        @{ Name = 'REDIS_IMAGE'; Tag = 'redis:8.2.3-alpine3.22'; Digest = 'sha256:08ad0b1d280850169a790dba1393ff7a90aef951fc19632cf4d3ce4f78e679ba' },
        @{ Name = 'KEYCLOAK_IMAGE'; Tag = 'quay.io/keycloak/keycloak:26.3.2'; Digest = 'sha256:98fab020a3a490aba0978f237e2a06cd0ea42bf149c6cf10f11c0aaf27728ff2' },
        @{ Name = 'GO_BUILD_IMAGE'; Tag = 'golang:1.25.12-alpine3.23'; Digest = 'sha256:cc985ef6f9c3bf9ece7488129c9abe0a150388ccdfa428d886fc709dca0b230a' },
        @{ Name = 'RUST_BUILD_IMAGE'; Tag = 'rust:1.95-alpine3.23'; Digest = 'sha256:606fd313a0f49743ee2a7bd49a0914bab7deedb12791f3a846a34a4711db7ed2' },
        @{ Name = 'RUNTIME_IMAGE'; Tag = 'alpine:3.22'; Digest = 'sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce' },
        @{ Name = 'SBOM_SCANNER_IMAGE'; Tag = 'docker/buildkit-syft-scanner:stable-1'; Digest = 'sha256:79e7b013cbec16bbb436f312819a49a4a57752b2270c1a9332ae1a10fcc82a68' }
    )
}

function Invoke-ComposeImageLock {
    param([string] $Path = (Join-Path $PSScriptRoot '..\build\compose-images.lock'))

    $lockLines = foreach ($image in Get-ComposeImageSpecifications) {
        $locked = Resolve-LockedImage -Tag $image.Tag -ExpectedDigest $image.Digest
        "$($image.Name)=$locked"
    }
    Write-ControlPlaneImageLock -Path $Path -LockLines $lockLines
    return $lockLines
}

if ($MyInvocation.InvocationName -ne '.') {
    Invoke-ComposeImageLock
}
