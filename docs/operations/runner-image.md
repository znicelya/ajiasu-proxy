# Runner Image Operations

This guide covers controlled maintenance and operation of the AJiaSu Runner image. It does not replace the [AJiaSu usage gate](../compliance/ajiasu-usage-gate.md); real-account testing and production operation remain blocked until the required approval record exists.

## Update the AJiaSu artifact lock

Treat every upstream AJiaSu release as untrusted input until two genuinely independent acquisitions agree. Independence requires separate approved egress/network paths or DNS resolvers, with timestamps and HTTP response metadata retained for both requests. Two downloads through the same path, resolver, and CDN edge are only two fresh downloads; record them as such and treat the independence gate as unmet. Perform the following for both `linux/amd64` and `linux/arm64`:

1. Download the official archive twice in separate commands to separate clean directories outside the repository, using a no-cache request. Do not create the second file by copying the first download or by reusing a local cache.
2. Record the byte size of each download and calculate SHA-256 independently for each file. The two sizes and the two SHA-256 values must match exactly. Stop and investigate any mismatch.
3. Confirm the archive contains the expected executable and no unexpected files. Do not execute it before the size and SHA-256 checks succeed.
4. Update the version, official URLs, byte sizes, and SHA-256 values in `runner/artifacts/ajiasu-<version>.env`. Update every script and Dockerfile reference that intentionally selects that lock; do not leave an old architecture on an implicit or mutable version.
5. Run the fake artifact, entrypoint, image, and CLI contracts with `pwsh -File scripts/ci.ps1`.
6. After the usage gate permits protected testing, build the candidate image and run the real contract with `pwsh -File tests/contract/run-real-ajiasu.ps1`. Credentials may be mapped only from protected CI secrets into the job environment; never type, print, or persist literal values. Remove the environment variables after the job and rely on an ephemeral runner workspace.
7. Review `git diff --check`, `git diff -- runner/artifacts Dockerfile runner scripts tests`, and `git status --short`. A second reviewer must compare the recorded URLs, sizes, and both independently calculated hashes with the lock before approval.
8. Commit the reviewed lock, selector changes, and required contract updates together. Do not commit downloaded archives or credentials.

The commands below create both destinations under the operating-system temporary directory, outside the repository. Run acquisition A and acquisition B through the separately approved network paths or resolvers. If both commands run through one path, they remain useful fresh-download checks but do not satisfy independence.

PowerShell 7:

```powershell
$url = Read-Host 'Official AJiaSu archive URL'
$root = Join-Path ([IO.Path]::GetTempPath()) "ajiasu-verify-$([guid]::NewGuid().ToString('N'))"
$a = Join-Path $root 'acquisition-a'
$b = Join-Path $root 'acquisition-b'
New-Item -ItemType Directory -Path $a, $b | Out-Null
$headers = @{ 'Cache-Control' = 'no-cache'; 'Pragma' = 'no-cache' }

$startedA = Get-Date -Format o
$responseA = Invoke-WebRequest -Uri $url -Headers $headers -OutFile (Join-Path $a 'ajiasu.tar.gz') -PassThru
@("timestamp=$startedA", "status=$($responseA.StatusCode)", ($responseA.Headers | Out-String)) |
    Set-Content -LiteralPath (Join-Path $a 'response-metadata.txt')

$startedB = Get-Date -Format o
$responseB = Invoke-WebRequest -Uri $url -Headers $headers -OutFile (Join-Path $b 'ajiasu.tar.gz') -PassThru
@("timestamp=$startedB", "status=$($responseB.StatusCode)", ($responseB.Headers | Out-String)) |
    Set-Content -LiteralPath (Join-Path $b 'response-metadata.txt')

$fileA = Join-Path $a 'ajiasu.tar.gz'
$fileB = Join-Path $b 'ajiasu.tar.gz'
$sizeA = (Get-Item -LiteralPath $fileA).Length
$sizeB = (Get-Item -LiteralPath $fileB).Length
$shaA = (Get-FileHash -Algorithm SHA256 -LiteralPath $fileA).Hash.ToLowerInvariant()
$shaB = (Get-FileHash -Algorithm SHA256 -LiteralPath $fileB).Hash.ToLowerInvariant()
$listA = Join-Path $a 'archive-list.txt'
$listB = Join-Path $b 'archive-list.txt'
tar -tzf $fileA | Set-Content -LiteralPath $listA
if ($LASTEXITCODE -ne 0) { throw 'acquisition A is not a readable gzip tar archive' }
tar -tzf $fileB | Set-Content -LiteralPath $listB
if ($LASTEXITCODE -ne 0) { throw 'acquisition B is not a readable gzip tar archive' }
if ($sizeA -ne $sizeB -or $shaA -cne $shaB) { throw 'independent artifact measurements do not match' }
if (Compare-Object (Get-Content -LiteralPath $listA) (Get-Content -LiteralPath $listB)) { throw 'archive listings do not match' }
[pscustomobject]@{ Root = $root; Size = $sizeA; SHA256 = $shaA } | Format-List
```

Bash:

```bash
set -eu
printf 'Official AJiaSu archive URL: ' >&2
IFS= read -r url
root=$(mktemp -d "${TMPDIR:-/tmp}/ajiasu-verify.XXXXXX")
mkdir -p "$root/acquisition-a" "$root/acquisition-b"

date -u +'%Y-%m-%dT%H:%M:%SZ' >"$root/acquisition-a/timestamp.txt"
curl --fail --show-error --location -H 'Cache-Control: no-cache' -H 'Pragma: no-cache' \
  --dump-header "$root/acquisition-a/response-headers.txt" \
  --output "$root/acquisition-a/ajiasu.tar.gz" "$url"

date -u +'%Y-%m-%dT%H:%M:%SZ' >"$root/acquisition-b/timestamp.txt"
curl --fail --show-error --location -H 'Cache-Control: no-cache' -H 'Pragma: no-cache' \
  --dump-header "$root/acquisition-b/response-headers.txt" \
  --output "$root/acquisition-b/ajiasu.tar.gz" "$url"

file_a=$root/acquisition-a/ajiasu.tar.gz
file_b=$root/acquisition-b/ajiasu.tar.gz
size_a=$(stat -c %s "$file_a")
size_b=$(stat -c %s "$file_b")
sha_a=$(sha256sum "$file_a" | awk '{print $1}')
sha_b=$(sha256sum "$file_b" | awk '{print $1}')
tar -tzf "$file_a" >"$root/acquisition-a/archive-list.txt"
tar -tzf "$file_b" >"$root/acquisition-b/archive-list.txt"
[ "$size_a" = "$size_b" ] && [ "$sha_a" = "$sha_b" ] || { echo 'independent artifact measurements do not match' >&2; exit 1; }
cmp -s "$root/acquisition-a/archive-list.txt" "$root/acquisition-b/archive-list.txt" || { echo 'archive listings do not match' >&2; exit 1; }
printf 'root=%s\nsize=%s\nsha256=%s\n' "$root" "$size_a" "$sha_a"
```

Compare the two archive listings as well as the sizes and hashes, and retain the timestamps, response headers, egress/resolver identity, and results in the approved review system. Do not move the downloads into the repository.

## Refresh the Alpine base-image lock

Run the checked-in resolver from the repository root:

```powershell
pwsh -File runner/scripts/lock-base-image.ps1
```

The resolver first attempts `docker buildx imagetools inspect alpine:3.22` against the official registry tag, retrying up to three times and extracting its top-level multi-architecture digest. The Docker Hub Tag API is always queried: it must report an active tag, the same active top-level digest when the primary lookup succeeded, and active `linux/amd64` plus `linux/arm64` images. If the primary registry inspection fails after its retries, the validated Docker Hub Tag API digest is the explicit fallback. The script then atomically writes `runner/artifacts/alpine-3.22.lock`. The lock must contain exactly one line in this form:

```text
ALPINE_IMAGE=alpine:3.22@sha256:<64-lowercase-hex-characters>
```

Review the digest change, run `pwsh -File scripts/ci.ps1`, and obtain supply-chain review before committing it. Never hand-enter an unverified digest or substitute a mutable tag in a build.

## Runtime filesystem and privilege contract

The image runs as UID/GID `65532:65532`. Docker creates tmpfs mounts as root unless ownership is explicit, so use the ownership and mode options below; omitting `uid=65532,gid=65532` prevents the non-root Runner from writing its runtime state.

- Mount `/run/ajiasu` as tmpfs with `uid=65532,gid=65532,mode=0700,noexec,nosuid,size=1m`.
- The production deployment target is a read-only secret injection at `/run/ajiasu/ajiasu.conf`, readable only by the Runner identity and never shared by tenants. Phase 1 does not yet test that mount immutability; it remains a later deployment acceptance test.
- Mount `/var/lib/ajiasu` as a separate writable, per-connection tmpfs or volume with `uid=65532,gid=65532,mode=0700,noexec,nosuid`. The protected contract currently uses a `16m` tmpfs. Never share this cache across connections or tenants.
- Keep the root filesystem read-only and start with every Linux capability dropped (`--cap-drop ALL`).
- Do not mount `/dev/net/tun` or add `NET_ADMIN` by default. Add only `/dev/net/tun` and the minimum proven capability after the protected real-account test demonstrates that the selected AJiaSu mode requires them. Record that proof and deployment exception. Never use `privileged: true` or host networking as a substitute.

The current protected wrapper in `tests/contract/run-real-ajiasu.ps1` creates an ephemeral writable configuration file on the owned `/run/ajiasu` tmpfs, applies `umask 077`, verifies mode `0600`, and then exercises AJiaSu `login` and `list` with the isolated `/var/lib/ajiasu` cache. It verifies tmpfs ownership, configuration permissions, and the read-only root filesystem, and requests that Docker drop all Linux capabilities with `--cap-drop ALL`; it does not inspect the effective capability sets or prove read-only secret-mount immutability. Effective capability and secret-mount immutability assertions belong to later deployment acceptance tests, which must preserve the same isolation and permissions.

Run the real contract only in protected CI after the usage gate approves it. Map `AJIASU_USERNAME` and `AJIASU_PASSWORD` from masked CI secrets directly into the job environment, prevent command tracing and output capture, and run `pwsh -File tests/contract/run-real-ajiasu.ps1` without literal values in the command. In job cleanup, remove the variables (`Remove-Item Env:AJIASU_USERNAME, Env:AJIASU_PASSWORD -ErrorAction SilentlyContinue` in PowerShell 7, or `unset AJIASU_USERNAME AJIASU_PASSWORD` in Bash), delete the ephemeral workspace, and dispose of the runner according to the protected-CI policy.

## Rollback prerequisite and policy

Phase 1 has no published or signed Runner images, signature trust policy, approved digest inventory, or deployment mechanism. It therefore cannot execute an operational rollback yet. The following controls are production-release prerequisites, not currently actionable commands:

1. The release process must publish architecture-specific images by immutable digest, sign them, attach verifiable provenance, and retain an approved inventory that maps each digest to its artifact and base-image locks.
2. The organization must define the trusted signers, verification policy, registry retention, authorization, and deployment mechanism before the first production release.
3. Deployment automation must support selecting the preceding approved signed digest without using a mutable tag and must preserve the non-root, filesystem, config/cache isolation, and capability contract.
4. A rollback runbook must verify signature and provenance, reject unsigned or unapproved digests, check known advisories and runtime compatibility, run the health and protected checks permitted by the usage gate, and record the reason and result.

Until those controls exist and are tested, do not describe a digest change as a usable rollback path. If a later preceding image is unsigned, unverifiable, absent from the approved inventory, or cannot be mapped to reviewed locks, stop rather than weakening the policy.

## Credential handling prohibitions

AJiaSu usernames and passwords must never appear in:

- shell or PowerShell command history;
- Docker or Compose files, including environment blocks and command arguments;
- CI workflow definitions, step output, artifacts, or logs;
- repository files, patches, issues, test fixtures, or committed configuration;
- image layers, labels, build arguments, or registry metadata.

Use the approved secret manager or protected CI secret injection, redact process output, and provide credentials to the Runner only for the minimum startup interval. If a credential is exposed in any prohibited location, stop the operation, revoke or rotate it, remove the exposed material from active systems, and follow the organization's incident process.
