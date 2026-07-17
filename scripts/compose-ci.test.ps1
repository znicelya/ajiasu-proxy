$ErrorActionPreference='Stop'
$root=Split-Path -Parent $PSScriptRoot
$content=[IO.File]::ReadAllText((Join-Path $PSScriptRoot 'compose-ci.ps1'))
foreach($required in @(
    "@('compose','version')","@('buildx','version')","compose-model.test.ps1","compose-init.test.ps1",
    "@('tool','sqlc','vet')","@('tool','sqlc','diff')","@('test','-race','-p','1','./...')","@('vet','./...')",
    "@('tool','staticcheck','./...')","@('clippy','--workspace','--all-targets','--all-features'","@('test','--workspace','--all-features')",
    "@('deny','check')","tests/compose/run.ps1 -Repeat 2","compose-image-ci.ps1","git status --porcelain"
)){if(-not $content.Contains($required)){throw "compose CI misses $required"}}
if($content -match 'Skip(Image|Scan|Test|E2E)'){throw 'compose CI contains a release-gate bypass'}
$workflow=[IO.File]::ReadAllText((Join-Path $root '.github/workflows/compose-ci.yml'))
foreach($required in @('ubuntu-24.04','setup-qemu-action@','setup-buildx-action@','cargo install cargo-deny','./scripts/compose-ci.ps1')){if(-not $workflow.Contains($required)){throw "compose workflow misses $required"}}
$image=[IO.File]::ReadAllText((Join-Path $PSScriptRoot 'compose-image-ci.ps1'))
foreach($required in @('linux/amd64,linux/arm64','type=sbom','type=provenance,mode=max','Dockerfile.fake-target','--scanners vuln,secret')){if(-not $image.Contains($required)){throw "image gate misses $required"}}
if(-not $image.Contains('AJIASU_COMPOSE_REGISTRY_MIRROR') -or -not $image.Contains('@(sha256:[0-9a-f]{64})')){throw 'image gate does not preserve locked digests through an optional registry mirror'}
foreach($required in @('AJIASU_COMPOSE_SBOM_SCANNER','AJIASU_COMPOSE_GO_PROXY','AJIASU_COMPOSE_CARGO_REGISTRY_INDEX','AJIASU_COMPOSE_TRIVY_CACHE_DIRECTORY','generator=','image scan failed after 3 attempts')){if(-not $image.Contains($required)){throw "image gate misses resilient external dependency control $required"}}
foreach($required in @("[ValidateSet('control-plane', 'gateway', 'agent', 'runner', 'fake-runner', 'fake-target')]","[string[]] `$Image = @('control-plane', 'gateway', 'agent', 'runner', 'fake-runner', 'fake-target')")){if(-not $image.Contains($required)){throw "image gate does not preserve the complete default image set: $required"}}
if($image -match 'Skip(Image|Scan|SBOM|Vulnerability)'){throw 'image gate contains a release-gate bypass'}
foreach($dockerfile in @('Dockerfile.gateway','Dockerfile.agent')){$dockerfileContent=[IO.File]::ReadAllText((Join-Path $root $dockerfile));foreach($required in @('ENV RUSTUP_TOOLCHAIN=1.95.0','ARG TARGETARCH','CARGO_REGISTRY_MIRROR','ajiasu-cargo-registry-${TARGETARCH}','target-${TARGETARCH}','replace-with = "ajiasu-mirror"','cargo build --locked')){if(-not $dockerfileContent.Contains($required)){throw "$dockerfile misses deterministic Rust image input $required"}}}
if(-not $image.Contains('SBOM_SCANNER_IMAGE') -or -not $image.Contains('type=sbom,generator=')){throw 'image gate does not use the digest-pinned default SBOM generator'}
'compose CI fixture tests passed'
