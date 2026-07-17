# Phase 8 Helm and Kubernetes Production Package Implementation Plan

## Execution rules

- Preserve Phase 7 configuration names, image digests, health semantics, and
  secret mount paths.
- Write a failing test before each behavior or security change.
- Keep chart, scripts, and test changes in coherent commits; never commit test
  credentials, rendered secrets, or mutable production image tags.
- Run `helm lint`, schema validation, template security checks, and the
  disposable-cluster gate before declaring completion.

## Task 1: Freeze chart, compatibility, and values contracts

Add `deploy/helm/ajiasu/Chart.yaml`, `values.yaml`, `values.schema.json`, and a
compatibility fixture referencing the Phase 7 release manifest. Define image,
port, probe, resource, dependency, exposure, security, and secret-provider
values. Add tests that reject missing digests, production bundled dependencies,
and schema/protocol mismatches.

## Task 2: Implement shared configuration and secret-provider integration

Create helpers, ConfigMaps, projected secret/file mounts, CA bundles, and
External Secrets/Vault/CSI examples. Ensure generated manifests contain no
secret literals and retain Compose-compatible `_FILE` paths. Add preflight
validation for keyring identity, required files, and dependency endpoints.

## Task 3: Add RBAC, ServiceAccounts, Pod Security, and NetworkPolicy

Define per-component identities and least-privilege Roles. Grant the Agent
only Runner lifecycle operations; deny other workloads Secret and Pod create
access. Add namespace-scoped NetworkPolicies and Pod Security labels, then
assert them with rendered-manifest tests.

## Task 4: Deploy Control Plane, Gateway, and migration/bootstrap Jobs

Implement Deployments, Services, probes, PDBs, topology spread, anti-affinity,
rolling strategies, preStop drains, and bounded termination. Add advisory-lock
migration and explicit bootstrap Jobs with hook ordering, retries, TTL, and
revision labels. Verify readiness requires the correct schema and dependency
state.

## Task 5: Implement Agent DaemonSet and Runner Pod template

Add node identity, runtime socket scoping, drain hooks, resource defaults,
security context, tolerations, and topology rules. Define the Agent-owned
Runner template with `network=none` equivalent policy, owner references,
finalizers, non-root execution, and cleanup guarantees. Test node loss and
reconciliation idempotency.

## Task 6: Add install, upgrade, drain, and rollback operator workflows

Provide documented `helm upgrade --install` values examples plus scripts or
Make targets for preflight, migration wait, node drain, status, and rollback.
Require a verified release manifest, compatible schema/keyring, and backup
metadata before rollback. Document Gateway single-replica mode when exact
aggregate limits are enabled.

## Task 7: Build the disposable-cluster integration harness

Create Kind/k3d setup and teardown, fake AJiaSu/Runner fixtures, and tests for
fixed and pooled HTTP/CONNECT/SOCKS5 traffic. Exercise rolling upgrade,
previous-version compatibility, node drain, Agent deletion, Redis loss,
duplicate events, and cross-tenant isolation. Capture only redacted logs and
manifests as CI artifacts.

## Task 8: Security, reliability, and release gates

Run chart lint/schema checks, kubeconform (or equivalent), admission-policy
tests, PDB/topology assertions, image-digest verification, and secret scanning.
Rehearse graceful shutdown, stuck drain, migration failure, dependency loss,
and rollback. Update operator docs, compatibility matrix, release manifest,
and Phase 8 exit evidence.

## Final verification

```text
helm lint deploy/helm/ajiasu
helm template ... --validate
rendered-manifest-security-tests
kind cluster integration gate
upgrade -> drain -> failure injection -> rollback gate
```

The phase is complete only after a clean temporary cluster demonstrates no
control-plane downtime across a compatible rolling upgrade, safe Runner
rescheduling during node drain, no quota/concurrency oversell, and passing
security-policy and secret-non-disclosure assertions.
