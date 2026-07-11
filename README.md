# AJiaSu Enterprise Proxy Platform Foundation

This repository is the secure Runner and supply-chain foundation for a planned enterprise proxy platform. It is not a finished single-container VPN workflow or a production orchestration package. The broader platform will add a control plane, tenant isolation, policy, scheduling, gateways, observability, and deployment packaging in later phases.

The Runner packages the unmodified official AJiaSu Linux CLI in a dedicated security boundary. The image starts as the non-root user `65532:65532`, uses a locked base-image digest, and verifies the official AJiaSu archive checksum and byte size before installation. Initial image support is limited to `linux/amd64` and `linux/arm64`.

## Security and compliance boundary

- Run one isolated Runner per active connection; never share a Runner across tenants.
- Start with a read-only root filesystem and `--cap-drop ALL`. Add a device or capability only after protected contract testing proves it is required.
- The legacy `network_mode: host` and `privileged: true` approach is unsupported for the enterprise platform. Host PID, host IPC, and broad container-runtime access are also prohibited defaults.
- Complete the [AJiaSu usage gate](docs/compliance/ajiasu-usage-gate.md) before real-account CI or production use. Fake contracts and binary-integrity checks do not authorize use of the service.
- Never store AJiaSu credentials in repository files, Compose files, command history, or CI logs.

See the [Runner security-boundary ADR](docs/adr/0001-runner-security-boundary.md) and [Runner image operations guide](docs/operations/runner-image.md) for the enforced runtime and maintenance procedures.

## Build the locked Runner image

The artifact lock currently selects AJiaSu `4.2.3.0` independently for each supported architecture. Build only with the checked-in Alpine digest lock:

```powershell
$lock = Get-Content runner/artifacts/alpine-3.22.lock | ConvertFrom-StringData
docker build --build-arg "ALPINE_IMAGE=$($lock.ALPINE_IMAGE)" -t ajiasu-runner:test .
```

Do not replace the locked digest with a mutable tag or bypass the archive checksum verification.

## Verify the foundation

Run the complete local gate from the repository root:

```powershell
powershell -File scripts/ci.ps1
```

This runs the artifact and entrypoint tests, builds the locked image, checks the non-root image contract, and runs the fake AJiaSu CLI contract. Real-account testing is a separate protected operation governed by the usage gate.

## Project documents

- [Approved enterprise platform design](docs/superpowers/specs/2026-07-11-enterprise-proxy-platform-design.md)
- [Enterprise platform roadmap](docs/superpowers/plans/2026-07-11-enterprise-proxy-platform-roadmap.md)
- [Runner security-boundary ADR](docs/adr/0001-runner-security-boundary.md)
- [AJiaSu usage gate](docs/compliance/ajiasu-usage-gate.md)
- [Runner image operations guide](docs/operations/runner-image.md)

## License

Repository code is licensed under the MIT License. AJiaSu is third-party software governed by its own license and service terms; repository licensing does not grant permission to operate AJiaSu.
