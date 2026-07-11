# Runner Image Operations

This guide covers controlled maintenance and operation of the AJiaSu Runner image. It does not replace the [AJiaSu usage gate](../compliance/ajiasu-usage-gate.md); real-account testing and production operation remain blocked until the required approval record exists.

## Update the AJiaSu artifact lock

Treat every upstream AJiaSu release as untrusted input until two independent acquisitions agree. Perform the following for both `linux/amd64` and `linux/arm64`:

1. Download the official archive twice in separate commands to separate clean directories. Do not create the second file by copying the first download or by reusing a local cache.
2. Record the byte size of each download and calculate SHA-256 independently for each file. The two sizes and the two SHA-256 values must match exactly. Stop and investigate any mismatch.
3. Confirm the archive contains the expected executable and no unexpected files. Do not execute it before the size and SHA-256 checks succeed.
4. Update the version, official URLs, byte sizes, and SHA-256 values in `runner/artifacts/ajiasu-<version>.env`. Update every script and Dockerfile reference that intentionally selects that lock; do not leave an old architecture on an implicit or mutable version.
5. Run the fake artifact, entrypoint, image, and CLI contracts with `powershell -File scripts/ci.ps1`.
6. After the usage gate permits protected testing, build the candidate image and run the real contract with `powershell -File tests/contract/run-real-ajiasu.ps1`. Supply credentials only through the approved protected environment.
7. Review `git diff --check`, `git diff -- runner/artifacts Dockerfile runner scripts tests`, and `git status --short`. A second reviewer must compare the recorded URLs, sizes, and both independently calculated hashes with the lock before approval.
8. Commit the reviewed lock, selector changes, and required contract updates together. Do not commit downloaded archives or credentials.

For a PowerShell workstation, independent measurements can be recorded with `Get-Item <archive> | Select-Object Length` and `Get-FileHash -Algorithm SHA256 <archive>`. Keep the review evidence in the approved change or artifact system, not as untracked binaries in the repository.

## Refresh the Alpine base-image lock

Run the checked-in resolver from the repository root:

```powershell
powershell -File runner/scripts/lock-base-image.ps1
```

The script resolves `alpine:3.22`, cross-checks the multi-architecture manifest through official registry sources, confirms active `linux/amd64` and `linux/arm64` images, and atomically writes `runner/artifacts/alpine-3.22.lock`. The lock must contain exactly one line in this form:

```text
ALPINE_IMAGE=alpine:3.22@sha256:<64-lowercase-hex-characters>
```

Review the digest change, run `powershell -File scripts/ci.ps1`, and obtain supply-chain review before committing it. Never hand-enter an unverified digest or substitute a mutable tag in a build.

## Runtime filesystem and privilege contract

The image runs as UID/GID `65532:65532`. Docker creates tmpfs mounts as root unless ownership is explicit, so use the ownership and mode options below; omitting `uid=65532,gid=65532` prevents the non-root Runner from writing its runtime state.

- Mount `/run/ajiasu` as tmpfs with `uid=65532,gid=65532,mode=0700,noexec,nosuid,size=1m`.
- Inject the per-connection configuration as a read-only secret file at `/run/ajiasu/ajiasu.conf`. It must be readable only by the Runner identity, must not come from the repository, and must not be shared by tenants.
- Mount `/var/lib/ajiasu` as a separate writable, per-connection tmpfs or volume with `uid=65532,gid=65532,mode=0700,noexec,nosuid`. The protected contract currently uses a `16m` tmpfs. Never share this cache across connections or tenants.
- Keep the root filesystem read-only and start with every Linux capability dropped (`--cap-drop ALL`).
- Do not mount `/dev/net/tun` or add `NET_ADMIN` by default. Add only `/dev/net/tun` and the minimum proven capability after the protected real-account test demonstrates that the selected AJiaSu mode requires them. Record that proof and deployment exception. Never use `privileged: true` or host networking as a substitute.

The protected contract wrapper in `tests/contract/run-real-ajiasu.ps1` is the executable reference for tmpfs ownership, permissions, read-only root filesystem, and dropped capabilities. Production orchestration may inject the configuration with its native secret mechanism, but it must preserve the same effective isolation and file permissions.

## Roll back an image

1. Identify the preceding approved, signed image digest for the same supported architecture. Do not roll back by mutable tag.
2. Verify the image signature and provenance using the organization's approved verifier and trust policy before deployment.
3. Confirm the digest maps to reviewed artifact and base-image locks, and check whether the rollback reintroduces a security advisory or an incompatible runtime contract.
4. Redeploy the exact digest, retaining the per-connection config/cache isolation, non-root identity, read-only root filesystem, and capability policy above.
5. Run health and protected contract checks allowed by the usage gate, record the rollback reason and digest in the incident/change system, and monitor before returning traffic.

If the preceding image is unsigned, its signature cannot be verified, or its locks cannot be reconstructed, stop rather than weakening the rollback policy.

## Credential handling prohibitions

AJiaSu usernames and passwords must never appear in:

- shell or PowerShell command history;
- Docker or Compose files, including environment blocks and command arguments;
- CI workflow definitions, step output, artifacts, or logs;
- repository files, patches, issues, test fixtures, or committed configuration;
- image layers, labels, build arguments, or registry metadata.

Use the approved secret manager or protected CI secret injection, redact process output, and provide credentials to the Runner only for the minimum startup interval. If a credential is exposed in any prohibited location, stop the operation, revoke or rotate it, remove the exposed material from active systems, and follow the organization's incident process.
