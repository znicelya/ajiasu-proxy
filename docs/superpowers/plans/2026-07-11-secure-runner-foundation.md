# Secure AJiaSu Runner Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish the repository and produce checksum-verified, multi-architecture AJiaSu Runner images with repeatable contract, smoke, and supply-chain checks.

**Architecture:** Keep AJiaSu artifact acquisition separate from the runtime image and express artifact metadata in a reviewed lock file. Test lifecycle integration against a deterministic fake executable in ordinary CI, while protected CI validates the unmodified official binary without exposing account credentials.

**Tech Stack:** Docker BuildKit/Buildx, Alpine 3.22, POSIX shell, PowerShell, GitHub Actions, Syft, Trivy

---

## File Map

```text
.
├── .dockerignore                         # limits build context
├── .editorconfig                         # repository text conventions
├── .gitattributes                        # stable LF handling for Linux scripts
├── .gitignore                            # generated files and local secrets
├── Dockerfile                            # secure Runner image
├── README.md                             # supported build and verification workflow
├── runner/
│   ├── artifacts/ajiasu-4.2.3.0.env      # official URLs, sizes, and SHA-256 values
│   ├── bin/runner-entrypoint.sh           # lifecycle and secret-file checks
│   ├── scripts/fetch-ajiasu.sh            # verified artifact extraction
│   ├── scripts/lock-base-image.ps1        # immutable Alpine manifest lock
│   ├── testdata/fake-ajiasu.sh            # deterministic CLI substitute
│   └── tests/
│       ├── fetch-ajiasu.test.sh            # artifact verification tests
│       ├── entrypoint.test.sh              # entrypoint contract tests
│       └── docker-smoke.test.ps1           # image/user/architecture smoke tests
├── tests/contract/
│   ├── ajiasu-contract.sh                  # common CLI contract assertions
│   └── run-real-ajiasu.ps1                 # protected real-binary test wrapper
├── scripts/ci.ps1                          # local/CI gate runner
├── docs/
│   ├── adr/0001-runner-security-boundary.md
│   ├── compliance/ajiasu-usage-gate.md
│   └── operations/runner-image.md
└── .github/workflows/runner-ci.yml
```

### Task 1: Import and Normalize the Repository Baseline

**Files:**

- Create: `.editorconfig`
- Create: `.gitattributes`
- Create: `.gitignore`
- Create: `.dockerignore`
- Create: `docs/adr/0001-runner-security-boundary.md`
- Add unchanged: `Dockerfile`
- Add unchanged: `README.md`

- [ ] **Step 1: Record the current untracked baseline**

Run:

```powershell
git status --short
Get-FileHash -Algorithm SHA256 Dockerfile,README.md
```

Expected: `Dockerfile` and `README.md` are untracked, and both hashes are printed for the execution log.

- [ ] **Step 2: Add text and ignore policies**

Create `.editorconfig`:

```ini
root = true

[*]
charset = utf-8
end_of_line = lf
insert_final_newline = true
trim_trailing_whitespace = true

[*.md]
trim_trailing_whitespace = false

[*.ps1]
end_of_line = crlf
```

Create `.gitattributes`:

```gitattributes
* text=auto
*.sh text eol=lf
*.env text eol=lf
*.md text eol=lf
*.ps1 text eol=crlf
```

Create `.gitignore`:

```gitignore
.env
.env.*
!.env.example
*.local
*.secret
*.pem
*.key
artifacts/
dist/
coverage/
target/
/bin/
.idea/
.vscode/
```

Create `.dockerignore`:

```dockerignore
.git
.github
.idea
.vscode
artifacts
coverage
dist
docs
target
*.local
*.secret
*.pem
*.key
```

- [ ] **Step 3: Document the Runner security boundary**

Create `docs/adr/0001-runner-security-boundary.md` with this decision:

```markdown
# ADR 0001: AJiaSu Runner Is the Privilege Boundary

Status: Accepted

The unmodified AJiaSu process runs only inside a dedicated Runner container or Pod. Control Plane, Web Console, Gateway, PostgreSQL, and Redis never receive Runner Linux capabilities or container-runtime access.

The Runner starts non-root. Any required `NET_ADMIN`, TUN device, route, or namespace privilege is granted explicitly by deployment configuration after a smoke test proves it is necessary. `privileged: true`, host PID, and host IPC are prohibited defaults.

Each active AJiaSu connection receives a separate cache/config directory and network namespace. A Runner never serves multiple tenants.
```

- [ ] **Step 4: Verify normalization did not change the imported files**

Run the same `Get-FileHash` command from Step 1.

Expected: the Dockerfile and README hashes match Step 1.

- [ ] **Step 5: Commit the baseline**

```powershell
git add .editorconfig .gitattributes .gitignore .dockerignore docs/adr/0001-runner-security-boundary.md Dockerfile README.md
git commit -m "chore: import and normalize repository baseline"
```

### Task 2: Add Test-Driven Verified Artifact Acquisition

**Files:**

- Create: `runner/artifacts/ajiasu-4.2.3.0.env`
- Create: `runner/tests/fetch-ajiasu.test.sh`
- Create: `runner/scripts/fetch-ajiasu.sh`

- [ ] **Step 1: Write the failing fetch verification test**

Create `runner/tests/fetch-ajiasu.test.sh`:

```sh
#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/source" "$TMP/out"
printf '#!/bin/sh\necho fake-ajiasu\n' > "$TMP/source/ajiasu"
chmod 0755 "$TMP/source/ajiasu"
tar -C "$TMP/source" -czf "$TMP/ajiasu.tar.gz" ajiasu
GOOD_SHA=$(sha256sum "$TMP/ajiasu.tar.gz" | awk '{print $1}')

if AJIASU_URL="file://$TMP/ajiasu.tar.gz" AJIASU_SHA256=deadbeef \
  "$ROOT/runner/scripts/fetch-ajiasu.sh" amd64 "$TMP/out"; then
  echo 'expected checksum mismatch to fail' >&2
  exit 1
fi

AJIASU_URL="file://$TMP/ajiasu.tar.gz" AJIASU_SHA256="$GOOD_SHA" \
  "$ROOT/runner/scripts/fetch-ajiasu.sh" amd64 "$TMP/out"

test -x "$TMP/out/ajiasu"
test "$($TMP/out/ajiasu)" = 'fake-ajiasu'
```

- [ ] **Step 2: Run the test and verify it fails because the fetch script is absent**

Run:

```powershell
docker run --rm -v "${PWD}:/src" -w /src alpine:3.22 sh runner/tests/fetch-ajiasu.test.sh
```

Expected: FAIL with `runner/scripts/fetch-ajiasu.sh: not found`.

- [ ] **Step 3: Add the reviewed artifact lock**

Create `runner/artifacts/ajiasu-4.2.3.0.env`:

```sh
AJIASU_VERSION=4.2.3.0
AJIASU_AMD64_URL=https://www.91ajs.com/files/downloads/linux/ajiasu-amd64-4.2.3.0.tar.gz
AJIASU_AMD64_SIZE=1253814
AJIASU_AMD64_SHA256=c3d93551a9632c2e28f7a86e541363f3bbe81f01bae12358714ae96287304232
AJIASU_ARM64_URL=https://www.91ajs.com/files/downloads/linux/ajiasu-aarch64-4.2.3.0.tar.gz
AJIASU_ARM64_SIZE=1247256
AJIASU_ARM64_SHA256=ccfe5eeb977f75f9b00b39534792f6ae4a3581188b0cd29b428e966eae2517e6
```

- [ ] **Step 4: Implement verified acquisition**

Create `runner/scripts/fetch-ajiasu.sh`:

```sh
#!/bin/sh
set -eu

ARCH=${1:?target architecture is required}
OUT=${2:?output directory is required}
ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
. "$ROOT/runner/artifacts/ajiasu-4.2.3.0.env"

case "$ARCH" in
  amd64)
    URL=${AJIASU_URL:-$AJIASU_AMD64_URL}
    SHA=${AJIASU_SHA256:-$AJIASU_AMD64_SHA256}
    ;;
  arm64)
    URL=${AJIASU_URL:-$AJIASU_ARM64_URL}
    SHA=${AJIASU_SHA256:-$AJIASU_ARM64_SHA256}
    ;;
  *)
    echo "unsupported architecture: $ARCH" >&2
    exit 64
    ;;
esac

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$OUT"
curl --fail --location --proto '=https,file' --tlsv1.2 "$URL" -o "$TMP/ajiasu.tar.gz"
printf '%s  %s\n' "$SHA" "$TMP/ajiasu.tar.gz" | sha256sum -c -
tar -C "$TMP" -xzf "$TMP/ajiasu.tar.gz" ajiasu
install -m 0755 "$TMP/ajiasu" "$OUT/ajiasu"
```

Make scripts executable:

```powershell
git update-index --chmod=+x runner/scripts/fetch-ajiasu.sh runner/tests/fetch-ajiasu.test.sh
```

- [ ] **Step 5: Run the verification test**

```powershell
docker run --rm -v "${PWD}:/src" -w /src alpine:3.22 sh -c "apk add --no-cache curl tar coreutils && sh runner/tests/fetch-ajiasu.test.sh"
```

Expected: PASS; the bad checksum is rejected and the valid fixture is extracted as executable.

- [ ] **Step 6: Verify both official artifacts**

```powershell
docker run --rm -v "${PWD}:/src" -w /src alpine:3.22 sh -c "apk add --no-cache curl tar coreutils && runner/scripts/fetch-ajiasu.sh amd64 /tmp/amd64 && runner/scripts/fetch-ajiasu.sh arm64 /tmp/arm64"
```

Expected: both `sha256sum` checks report `OK`.

- [ ] **Step 7: Commit artifact verification**

```powershell
git add runner/artifacts runner/scripts/fetch-ajiasu.sh runner/tests/fetch-ajiasu.test.sh
git commit -m "build: verify ajiasu release artifacts"
```

### Task 3: Add the Runner Entrypoint Contract

**Files:**

- Create: `runner/testdata/fake-ajiasu.sh`
- Create: `runner/tests/entrypoint.test.sh`
- Create: `runner/bin/runner-entrypoint.sh`

- [ ] **Step 1: Write the fake AJiaSu executable**

Create `runner/testdata/fake-ajiasu.sh`:

```sh
#!/bin/sh
set -eu
printf 'ajiasu 4.2.3.0 (fake)\n'
printf 'Command: %s\n' "${1:-help}"
case "${1:-help}" in
  login) printf 'Login Result: OK\n' ;;
  list) printf 'vvn-test-1 ok Test Node #1\n' ;;
  connect) exec sleep "${FAKE_CONNECT_SECONDS:-1}" ;;
  *) printf 'usage: ajiasu {login|list|connect|logout}\n' ;;
esac
```

- [ ] **Step 2: Write the failing entrypoint tests**

Create `runner/tests/entrypoint.test.sh`:

```sh
#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

install -m 0755 "$ROOT/runner/testdata/fake-ajiasu.sh" "$TMP/ajiasu"
printf 'user example\npass secret\n' > "$TMP/ajiasu.conf"
chmod 0600 "$TMP/ajiasu.conf"

AJIASU_BIN="$TMP/ajiasu" AJIASU_CONFIG="$TMP/ajiasu.conf" \
  "$ROOT/runner/bin/runner-entrypoint.sh" login | grep -F 'Login Result: OK'

chmod 0644 "$TMP/ajiasu.conf"
if AJIASU_BIN="$TMP/ajiasu" AJIASU_CONFIG="$TMP/ajiasu.conf" \
  "$ROOT/runner/bin/runner-entrypoint.sh" login; then
  echo 'expected insecure config permissions to fail' >&2
  exit 1
fi
```

- [ ] **Step 3: Run the test and verify the entrypoint is absent**

```powershell
docker run --rm -v "${PWD}:/src" -w /src alpine:3.22 sh runner/tests/entrypoint.test.sh
```

Expected: FAIL with `runner/bin/runner-entrypoint.sh: not found`.

- [ ] **Step 4: Implement the minimal entrypoint**

Create `runner/bin/runner-entrypoint.sh`:

```sh
#!/bin/sh
set -eu

AJIASU_BIN=${AJIASU_BIN:-/usr/local/bin/ajiasu}
AJIASU_CONFIG=${AJIASU_CONFIG:-/run/ajiasu/ajiasu.conf}

test -x "$AJIASU_BIN" || { echo 'ajiasu executable is unavailable' >&2; exit 66; }
test -f "$AJIASU_CONFIG" || { echo 'ajiasu config is unavailable' >&2; exit 66; }

MODE=$(stat -c '%a' "$AJIASU_CONFIG")
case "$MODE" in
  400|600) ;;
  *) echo "ajiasu config permissions must be 0400 or 0600, got $MODE" >&2; exit 77 ;;
esac

export AJIASU_CONFIG
exec "$AJIASU_BIN" "$@"
```

Make all three scripts executable and run the test again.

Expected: PASS; secure config is accepted and group/world-readable config is rejected.

- [ ] **Step 5: Commit the lifecycle contract**

```powershell
git add runner/bin runner/testdata runner/tests/entrypoint.test.sh
git commit -m "feat: add secure runner entrypoint contract"
```

### Task 4: Replace the Dockerfile with a Verified Non-Root Build

**Files:**

- Create: `runner/scripts/lock-base-image.ps1`
- Create during the task: `runner/artifacts/alpine-3.22.lock`
- Modify: `Dockerfile`
- Create: `runner/tests/docker-smoke.test.ps1`

- [ ] **Step 1: Write the failing image smoke test**

Create `runner/tests/docker-smoke.test.ps1`:

```powershell
$ErrorActionPreference = 'Stop'
$image = if ($env:RUNNER_IMAGE) { $env:RUNNER_IMAGE } else { 'ajiasu-runner:test' }
$user = docker image inspect $image --format '{{.Config.User}}'
if ($user -ne '65532:65532') { throw "expected user 65532:65532, got $user" }
$entrypoint = docker image inspect $image --format '{{json .Config.Entrypoint}}'
if ($entrypoint -ne '["/usr/local/bin/runner-entrypoint.sh"]') { throw "unexpected entrypoint: $entrypoint" }
$labels = docker image inspect $image --format '{{index .Config.Labels "org.opencontainers.image.version"}}'
if ($labels -ne '4.2.3.0') { throw "unexpected AJiaSu version label: $labels" }
```

- [ ] **Step 2: Prove the current image fails the non-root contract**

```powershell
docker build -t ajiasu-runner:test .
powershell -File runner/tests/docker-smoke.test.ps1
```

Expected: FAIL because the current image has no configured `65532:65532` user or secure entrypoint.

- [ ] **Step 3: Automate immutable Alpine locking**

Create `runner/scripts/lock-base-image.ps1`:

```powershell
$ErrorActionPreference = 'Stop'
$raw = docker buildx imagetools inspect alpine:3.22
$digestLine = $raw | Select-String '^Digest:\s+(sha256:[0-9a-f]{64})$'
if (-not $digestLine) { throw 'unable to resolve alpine:3.22 manifest digest' }
$digest = $digestLine.Matches[0].Groups[1].Value
"ALPINE_IMAGE=alpine:3.22@$digest" | Set-Content -Encoding ascii runner/artifacts/alpine-3.22.lock
Get-Content runner/artifacts/alpine-3.22.lock
```

Run it:

```powershell
powershell -File runner/scripts/lock-base-image.ps1
```

Expected: `runner/artifacts/alpine-3.22.lock` contains exactly one `ALPINE_IMAGE=alpine:3.22@sha256:<64 lowercase hex>` line. If Docker Hub is unreachable, stop this task and retry through the approved registry mirror; do not substitute an unverified digest.

- [ ] **Step 4: Replace the Dockerfile**

Replace `Dockerfile` with:

```dockerfile
# syntax=docker/dockerfile:1.7
ARG ALPINE_IMAGE

FROM --platform=$BUILDPLATFORM ${ALPINE_IMAGE} AS fetch
RUN apk add --no-cache ca-certificates coreutils curl tar
WORKDIR /src
COPY runner/artifacts/ajiasu-4.2.3.0.env runner/artifacts/ajiasu-4.2.3.0.env
COPY runner/scripts/fetch-ajiasu.sh runner/scripts/fetch-ajiasu.sh
ARG TARGETARCH
RUN runner/scripts/fetch-ajiasu.sh "$TARGETARCH" /out

FROM ${ALPINE_IMAGE}
LABEL org.opencontainers.image.title="AJiaSu Runner" \
      org.opencontainers.image.version="4.2.3.0" \
      org.opencontainers.image.description="Verified isolated runner for the official AJiaSu Linux CLI"
RUN addgroup -g 65532 -S runner && adduser -S -D -H -u 65532 -G runner runner \
    && apk add --no-cache ca-certificates \
    && mkdir -p /run/ajiasu /var/lib/ajiasu \
    && ln -s /run/ajiasu/ajiasu.conf /etc/ajiasu.conf \
    && chown -R 65532:65532 /run/ajiasu /var/lib/ajiasu
COPY --from=fetch --chown=65532:65532 /out/ajiasu /usr/local/bin/ajiasu
COPY --chown=65532:65532 runner/bin/runner-entrypoint.sh /usr/local/bin/runner-entrypoint.sh
USER 65532:65532
WORKDIR /var/lib/ajiasu
ENTRYPOINT ["/usr/local/bin/runner-entrypoint.sh"]
CMD ["connect"]
```

- [ ] **Step 5: Build using the locked image reference**

```powershell
$lock = Get-Content runner/artifacts/alpine-3.22.lock | ConvertFrom-StringData
docker build --build-arg "ALPINE_IMAGE=$($lock.ALPINE_IMAGE)" -t ajiasu-runner:test .
```

Expected: artifact checksum reports `OK` and the image builds.

- [ ] **Step 6: Run the smoke test**

```powershell
powershell -File runner/tests/docker-smoke.test.ps1
```

Expected: PASS.

- [ ] **Step 7: Verify the image starts without broad privileges**

```powershell
"user invalid`npass invalid`ncache_dir /var/lib/ajiasu" | docker run --rm -i --read-only --cap-drop ALL --tmpfs /run/ajiasu:rw,noexec,nosuid,size=1m --entrypoint /bin/sh ajiasu-runner:test -c 'umask 077; cat > /run/ajiasu/ajiasu.conf; exec /usr/local/bin/runner-entrypoint.sh -h'
```

Expected: the executable prints help/version output without a container permission error. A real `connect` capability test is deferred to the protected contract job because it requires an authorized account and may prove that an explicit network capability is necessary.

- [ ] **Step 8: Commit the secure image**

```powershell
git add Dockerfile runner/scripts/lock-base-image.ps1 runner/artifacts/alpine-3.22.lock runner/tests/docker-smoke.test.ps1
git commit -m "build: harden ajiasu runner image"
```

### Task 5: Add CLI Contract Tests

**Files:**

- Create: `tests/contract/ajiasu-contract.sh`
- Create: `tests/contract/run-real-ajiasu.ps1`
- Create: `docs/compliance/ajiasu-usage-gate.md`

- [ ] **Step 1: Write the common contract test**

Create `tests/contract/ajiasu-contract.sh`:

```sh
#!/bin/sh
set -eu

AJIASU_BIN=${AJIASU_BIN:?AJIASU_BIN is required}
CONFIG=${AJIASU_CONFIG:?AJIASU_CONFIG is required}

help=$($AJIASU_BIN -h 2>&1 || true)
printf '%s' "$help" | grep -E 'login|list|connect'

login=$($AJIASU_BIN login 2>&1)
printf '%s' "$login" | grep -F 'Login Result: OK'

nodes=$($AJIASU_BIN list 2>&1)
printf '%s' "$nodes" | grep -E '(^|[[:space:]])vvn-'

test -s "$CONFIG"
```

- [ ] **Step 2: Run the contract against the fake binary**

```powershell
docker run --rm -v "${PWD}:/src" -w /src -e AJIASU_BIN=/src/runner/testdata/fake-ajiasu.sh -e AJIASU_CONFIG=/src/README.md alpine:3.22 sh tests/contract/ajiasu-contract.sh
```

Expected: PASS with login and node markers.

- [ ] **Step 3: Add the protected real-binary wrapper**

Create `tests/contract/run-real-ajiasu.ps1`:

```powershell
$ErrorActionPreference = 'Stop'
if (-not $env:AJIASU_USERNAME -or -not $env:AJIASU_PASSWORD) {
  throw 'AJIASU_USERNAME and AJIASU_PASSWORD are required in a protected environment'
}
$config = "user $env:AJIASU_USERNAME`npass $env:AJIASU_PASSWORD`ncache_dir /var/lib/ajiasu"
$config | docker run --rm -i --cap-drop ALL --tmpfs /run/ajiasu:rw,noexec,nosuid,size=1m --entrypoint /bin/sh ajiasu-runner:test -c 'umask 077; cat > /run/ajiasu/ajiasu.conf; exec /usr/local/bin/runner-entrypoint.sh login'
$config | docker run --rm -i --cap-drop ALL --tmpfs /run/ajiasu:rw,noexec,nosuid,size=1m --entrypoint /bin/sh ajiasu-runner:test -c 'umask 077; cat > /run/ajiasu/ajiasu.conf; exec /usr/local/bin/runner-entrypoint.sh list'
```

- [ ] **Step 4: Add the mandatory usage/legal gate**

Create `docs/compliance/ajiasu-usage-gate.md`:

```markdown
# AJiaSu Usage Gate

Before enabling protected real-account CI or production orchestration, the project owner must retain written evidence that the intended internal enterprise use, account concurrency, containerized execution, credential automation, and selected node behavior comply with the AJiaSu license and service terms.

If that evidence is absent or restrictive, execution stops after fake-contract and binary-integrity tests. No engineer may bypass the gate by embedding personal credentials in CI or source files.

The approval record must identify the reviewer, date, terms/version reviewed, permitted deployment scope, account concurrency rule, and any operational restrictions. The record belongs in the organization's compliance system; this repository stores only its non-secret reference identifier.
```

- [ ] **Step 5: Commit the contract harness**

```powershell
git add tests/contract docs/compliance/ajiasu-usage-gate.md
git commit -m "test: add ajiasu cli contract harness"
```

### Task 6: Add One-Command Verification and CI

**Files:**

- Create: `scripts/ci.ps1`
- Create: `.github/workflows/runner-ci.yml`

- [ ] **Step 1: Create the local gate runner**

Create `scripts/ci.ps1`:

```powershell
$ErrorActionPreference = 'Stop'
$lock = Get-Content runner/artifacts/alpine-3.22.lock | ConvertFrom-StringData

docker run --rm -v "${PWD}:/src" -w /src alpine:3.22 sh -c "apk add --no-cache curl tar coreutils && sh runner/tests/fetch-ajiasu.test.sh && sh runner/tests/entrypoint.test.sh"
docker build --build-arg "ALPINE_IMAGE=$($lock.ALPINE_IMAGE)" -t ajiasu-runner:test .
$env:RUNNER_IMAGE = 'ajiasu-runner:test'
powershell -File runner/tests/docker-smoke.test.ps1
docker run --rm -v "${PWD}:/src" -w /src -e AJIASU_BIN=/src/runner/testdata/fake-ajiasu.sh -e AJIASU_CONFIG=/src/README.md alpine:3.22 sh tests/contract/ajiasu-contract.sh
```

- [ ] **Step 2: Run the complete local gate**

```powershell
powershell -File scripts/ci.ps1
```

Expected: all shell tests, image build, image smoke test, and fake contract pass.

- [ ] **Step 3: Add GitHub Actions CI**

Create `.github/workflows/runner-ci.yml`:

```yaml
name: runner-ci

on:
  pull_request:
  push:
    branches: [master, main]

permissions:
  contents: read

jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Verify runner
        shell: pwsh
        run: ./scripts/ci.ps1

  multiarch:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3
      - name: Load locked base image
        id: lock
        shell: bash
        run: cat runner/artifacts/alpine-3.22.lock >> "$GITHUB_OUTPUT"
      - name: Build supported architectures
        uses: docker/build-push-action@v6
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: false
          build-args: ALPINE_IMAGE=${{ steps.lock.outputs.ALPINE_IMAGE }}

  scan:
    runs-on: ubuntu-latest
    needs: verify
    steps:
      - uses: actions/checkout@v4
      - name: Read locked base image
        id: lock
        shell: bash
        run: cat runner/artifacts/alpine-3.22.lock >> "$GITHUB_OUTPUT"
      - name: Build image
        run: docker build --build-arg ALPINE_IMAGE=${{ steps.lock.outputs.ALPINE_IMAGE }} -t ajiasu-runner:scan .
      - uses: anchore/sbom-action@v0
        with:
          image: ajiasu-runner:scan
          artifact-name: ajiasu-runner.spdx.json
      - uses: aquasecurity/trivy-action@0.28.0
        with:
          image-ref: ajiasu-runner:scan
          severity: CRITICAL,HIGH
          ignore-unfixed: true
          exit-code: '1'
```

- [ ] **Step 4: Validate workflow syntax and run the local gate again**

Run:

```powershell
docker run --rm -v "${PWD}:/work" -w /work rhysd/actionlint:latest
powershell -File scripts/ci.ps1
```

Expected: `actionlint` exits zero and local gates pass.

- [ ] **Step 5: Commit CI**

```powershell
git add scripts/ci.ps1 .github/workflows/runner-ci.yml
git commit -m "ci: add runner integrity and supply chain gates"
```

### Task 7: Update Operator Documentation

**Files:**

- Modify: `README.md`
- Create: `docs/operations/runner-image.md`

- [ ] **Step 1: Write a documentation assertion test**

Run this before editing:

```powershell
$required = 'checksum','linux/amd64','linux/arm64','non-root','cap-drop','usage gate'
$text = (Get-Content -Raw README.md).ToLowerInvariant()
$missing = $required | Where-Object { -not $text.Contains($_) }
if (-not $missing) { throw 'expected the legacy README to miss enterprise runner guidance' }
$missing
```

Expected: at least one required phrase is printed.

- [ ] **Step 2: Replace legacy production guidance**

Update `README.md` so it:

- Identifies the repository as the enterprise-platform foundation rather than a finished single-container VPN workflow.
- States that only `linux/amd64` and `linux/arm64` are supported initially.
- Explains that the official archive is checksum-verified.
- Gives the exact locked build command from Task 4.
- Gives the exact `scripts/ci.ps1` verification command.
- Marks the old `network_mode: host` and `privileged: true` approach as unsupported for the enterprise platform.
- Links to the design, roadmap, Runner ADR, usage gate, and operations guide.

Create `docs/operations/runner-image.md` with:

- Artifact update procedure: download, record byte size, calculate SHA-256 twice from independent downloads, update lock, run fake and real contracts, review diff, commit.
- Base image lock procedure using `runner/scripts/lock-base-image.ps1`.
- Runtime mounts: read-only `/run/ajiasu/ajiasu.conf`, writable isolated `/var/lib/ajiasu`, and optional `/dev/net/tun` only after protected testing proves it necessary.
- Rollback procedure to the preceding signed image digest.
- A prohibition on credentials in command history, Compose files, CI logs, and repository files.

- [ ] **Step 3: Run the documentation assertion after editing**

```powershell
$required = 'checksum','linux/amd64','linux/arm64','non-root','cap-drop','usage gate'
$text = (Get-Content -Raw README.md).ToLowerInvariant()
$missing = $required | Where-Object { -not $text.Contains($_) }
if ($missing) { throw "missing README guidance: $($missing -join ', ')" }
```

Expected: exits zero.

- [ ] **Step 4: Run all gates**

```powershell
powershell -File scripts/ci.ps1
git diff --check
git status --short
```

Expected: tests pass, `git diff --check` reports no whitespace errors, and only intended documentation changes remain.

- [ ] **Step 5: Commit documentation**

```powershell
git add README.md docs/operations/runner-image.md
git commit -m "docs: document secure runner operations"
```

### Task 8: Phase Exit Verification

**Files:**

- Verify only; no planned file changes.

- [ ] **Step 1: Run repository and test gates**

```powershell
powershell -File scripts/ci.ps1
docker run --rm -v "${PWD}:/work" -w /work rhysd/actionlint:latest
git diff --check
```

Expected: all commands exit zero.

- [ ] **Step 2: Build both supported architectures**

```powershell
$lock = Get-Content runner/artifacts/alpine-3.22.lock | ConvertFrom-StringData
docker buildx build --build-arg "ALPINE_IMAGE=$($lock.ALPINE_IMAGE)" --platform linux/amd64,linux/arm64 --output type=cacheonly .
```

Expected: both platform builds complete and both AJiaSu checksum validations report `OK`.

- [ ] **Step 3: Inspect final image security settings**

```powershell
docker image inspect ajiasu-runner:test --format 'User={{.Config.User}} Entrypoint={{json .Config.Entrypoint}}'
docker history --no-trunc ajiasu-runner:test
```

Expected: user is `65532:65532`, entrypoint is the Runner script, and no layer contains AJiaSu credentials.

- [ ] **Step 4: Confirm Git state and phase commits**

```powershell
git status --short
git log --oneline --max-count=10
```

Expected: working tree is clean and the phase contains separate baseline, artifact, entrypoint, image, contract, CI, and documentation commits.

- [ ] **Step 5: Record the phase gate result**

Create an annotated tag only after the usage/compliance owner confirms whether protected real-account testing is permitted:

```powershell
git tag -a phase-1-runner-foundation -m "Secure AJiaSu Runner foundation verified"
```

Expected: `git show phase-1-runner-foundation --no-patch` identifies the verified phase commit. Do not push or publish the tag without the repository owner's explicit release authorization.
