# Phase 8 Helm and Kubernetes Operations

## Preconditions

Install Helm 3.17+, kubectl, and a supported Kubernetes cluster (1.27+).
PostgreSQL, Redis, OIDC, TLS material, and the external Secret provider are
operated outside this chart in production. Create the namespace with Pod
Security labels appropriate for the Agent's required node runtime socket, and
keep the Agent DaemonSet restricted to trusted nodes.

The runtime Secret must be materialized before installation. Its keys are
listed in `deploy/helm/ajiasu/values.yaml`; values must never be committed or
passed through shell history. Use the ExternalSecret example as a mapping
reference only.

## Install

1. Copy `deploy/helm/ajiasu/examples/values-production.yaml` and replace only
   non-secret placeholders.
2. Run `scripts/helm-preflight.ps1` with the four immutable component digests,
   the reviewed values file, namespace, release name, and Secret name.
3. Run `scripts/helm-install.ps1`. The migration hook must finish before
   application readiness is accepted.
4. Run `scripts/helm-status.ps1` and perform fixed and pooled smoke probes
   through the Gateway before admitting clients.

Only the Agent mounts the node runtime socket. This is a host-compromise
boundary, not a low-risk read-only integration. Runner workloads remain
non-root, resource bounded, tokenless, and network-denied.

## Upgrade

Review image digests, the release manifest, schema/protocol compatibility, and
a verified PostgreSQL/keyring backup. Run `helm upgrade --install` through the
install script with the new values. The migration hook and Deployment rollout
must complete before accepting traffic. Control Plane uses `maxUnavailable=0`;
Gateway readiness must converge to a current route snapshot.

## Node drain and Agent loss

Run `scripts/helm-drain.ps1 -Node NODE -Namespace NAMESPACE`. Cordon happens
before eviction. The Agent receives termination time to stop new assignments
and finalize owned Runners. Lease expiry and reconciliation reschedule eligible
Runners; quota and concurrency reservations remain authoritative in PostgreSQL.
Inspect operations and audit records for stuck or non-evictable workloads.

## Redis loss

Existing safe traffic may continue from committed assignment state. New pooled
allocations must be blocked while Redis fencing is unavailable. Do not delete
PostgreSQL assignment state or manually recreate leases. Restore Redis and wait
for health debounce and bounded rebuild before reopening allocations.

## Rollback

Rollback is manifest- and schema-aware, not merely an image tag change. Verify
the target Helm revision, schema compatibility, release manifest, PostgreSQL
backup, and matching keyring first. Run `scripts/helm-rollback.ps1` with an
explicit revision, then wait for migration/readiness and replay fixed/pool
smoke probes. If the schema is not backward compatible, restore the database
backup instead of mixing protocol generations in place.

## Evidence and incident artifacts

Record Helm history, rendered manifests with Secret data redacted, rollout
status, migration output, smoke results, and relevant audit operation IDs.
Never attach Secret objects, Pod environment dumps, proxy credentials, route
signing material, or raw Redis values to an incident ticket.
