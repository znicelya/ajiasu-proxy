# AJiaSu Enterprise Proxy Platform Foundation

This repository contains the secure Runner foundation and the Phase 7 Docker Compose production package for the AJiaSu enterprise proxy platform. The package provides a Control Plane, one host Agent, one active Gateway, PostgreSQL/Redis dependency profiles, mutual TLS, one-time enrollment, lifecycle operations, and recovery/upgrade gates.

The Runner packages the unmodified official AJiaSu Linux CLI for an intended containerized, one-connection isolation boundary. This Phase 1 image is a foundation for that boundary, not a claim that later orchestration or production isolation is already complete. The image starts as the non-root user `65532:65532`, uses a locked base-image digest, and verifies the official AJiaSu archive checksum and byte size before installation. Initial image support is limited to `linux/amd64` and `linux/arm64`.

## Security and compliance boundary

- Run one isolated Runner per active connection; never share a Runner across tenants.
- Start with a read-only root filesystem and `--cap-drop ALL`. Add a device or capability only after protected contract testing proves it is required.
- The legacy `network_mode: host` and `privileged: true` approach is unsupported for the enterprise platform. Host PID, host IPC, and broad container-runtime access are also prohibited defaults.
- Complete the [AJiaSu usage gate](docs/compliance/ajiasu-usage-gate.md) before real-account CI or production use. Fake contracts and binary-integrity checks do not authorize use of the service.
- Never store AJiaSu credentials in repository files, Compose files, command history, or CI logs.

See the [Runner security-boundary ADR](docs/adr/0001-runner-security-boundary.md) and [Runner image operations guide](docs/operations/runner-image.md) for the enforced runtime and maintenance procedures.

## Docker Compose package

Read the [Phase 7 Compose operations guide](docs/operations/docker-compose-phase7.md) before installation. Initialize with `scripts/compose-init.ps1`, start with `scripts/compose-up.ps1`, inspect bounded state with `scripts/compose-status.ps1`, and stop with `scripts/compose-down.ps1`. Backup, restore, upgrade, and rollback commands are documented in the same guide.

Agent is the only service with Docker socket access; that access is equivalent to Docker-host authority. Phase 7 supports exactly one active Gateway when exact aggregate limits are required. Real-account operation still requires the [AJiaSu usage gate](docs/compliance/ajiasu-usage-gate.md).

## Build the locked Runner image

The artifact lock currently selects AJiaSu `4.2.3.0` independently for each supported architecture. Build only with the checked-in Alpine digest lock:

Prerequisites are a running Docker engine with Docker Buildx and PowerShell 7 (`pwsh`). PowerShell 7 is required on both Windows and Linux to run the cross-platform CI script.

PowerShell 7:

```powershell
$lockLines = @(Get-Content -LiteralPath runner/artifacts/alpine-3.22.lock)
if ($lockLines.Count -ne 1 -or $lockLines[0] -notmatch '^ALPINE_IMAGE=(alpine:3\.22@sha256:[0-9a-f]{64})$') {
    throw 'invalid Alpine image lock'
}
$alpineImage = $Matches[1]
docker build --pull=false --build-arg "ALPINE_IMAGE=$alpineImage" -t ajiasu-runner:test .
```

Bash:

```bash
set -eu
lock_file=runner/artifacts/alpine-3.22.lock
[ "$(awk 'END { print NR }' "$lock_file")" -eq 1 ] || { echo 'invalid Alpine image lock line count' >&2; exit 1; }
lock_line=$(sed -n '1p' "$lock_file")
printf '%s\n' "$lock_line" | grep -Eq '^ALPINE_IMAGE=alpine:3\.22@sha256:[0-9a-f]{64}$' || { echo 'invalid Alpine image lock' >&2; exit 1; }
alpine_image=${lock_line#ALPINE_IMAGE=}
docker build --pull=false --build-arg "ALPINE_IMAGE=$alpine_image" -t ajiasu-runner:test .
```

Do not replace the locked digest with a mutable tag or bypass the archive checksum verification.

## Verify the foundation

Run the complete local gate from the repository root:

```powershell
pwsh -File scripts/ci.ps1
```

Use that same `pwsh` command from PowerShell 7 on Windows or Linux. It runs the artifact and entrypoint tests, builds the locked image, checks the non-root image contract, and runs the fake AJiaSu CLI contract. Real-account testing is a separate protected operation governed by the usage gate.

## Project documents

- [Approved enterprise platform design](docs/superpowers/specs/2026-07-11-enterprise-proxy-platform-design.md)
- [Enterprise platform roadmap](docs/superpowers/plans/2026-07-11-enterprise-proxy-platform-roadmap.md)
- [Runner security-boundary ADR](docs/adr/0001-runner-security-boundary.md)
- [AJiaSu usage gate](docs/compliance/ajiasu-usage-gate.md)
- [Runner image operations guide](docs/operations/runner-image.md)
- [Phase 7 Docker Compose operations guide](docs/operations/docker-compose-phase7.md)
- [Compose lifecycle details](docs/operations/compose-lifecycle.md)
- [Compose recovery and upgrade details](docs/operations/compose-recovery-upgrade.md)

## License

Repository code is licensed under the MIT License. AJiaSu is third-party software governed by its own license and service terms; repository licensing does not grant permission to operate AJiaSu.
