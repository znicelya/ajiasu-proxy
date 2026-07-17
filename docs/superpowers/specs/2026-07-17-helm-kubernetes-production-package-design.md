# Phase 8 Helm and Kubernetes Production Package Design

## 1. Goal

Phase 8 packages the stable Phase 7 component and configuration contracts as a
production-ready Helm chart. The chart must support external PostgreSQL and
Redis by default, optional development dependencies, safe rolling upgrades,
node drains, and auditable recovery from Agent or Redis loss. Kubernetes adds
placement and identity primitives; it must not change tenant, quota, lease,
proxy, or Runner semantics owned by Phases 2-6.

## 2. Scope and exclusions

Included: one versioned chart for Control Plane, Console contract, Gateway,
Agent DaemonSet, migration/bootstrap Jobs, optional Runner pod template,
Services, configuration, External Secrets/Vault/KMS integration points,
NetworkPolicy, RBAC, ServiceAccounts, Pod Security settings, PDBs, topology
spread, anti-affinity, probes, graceful termination, install/upgrade/drain/
rollback documentation, and ephemeral-cluster tests.

Excluded: an in-chart PostgreSQL/Redis HA operator, a new ingress controller,
cluster autoscaling, multi-region replication, transparent TLS interception,
and Phase 9 dashboards or the complete Console application.

## 3. Compatibility contract

The chart reuses Phase 7 image names, immutable digest requirements, ports,
`*_FILE` secret names, health endpoints, schema/protocol revision metadata,
and release-manifest fields. `values.schema.json` rejects mutable production
images, missing external dependency URLs, unsupported architectures, and
schema/protocol mismatches. `Chart.yaml` `appVersion` is informational; the
release manifest is authoritative.

## 4. Chart layout

```text
deploy/helm/ajiasu/
  Chart.yaml  values.yaml  values.schema.json
  templates/
    _helpers.tpl configmap.yaml secret-sync.yaml
    serviceaccounts.yaml role.yaml rolebinding.yaml
    control-plane-{deployment,service}.yaml
    gateway-{deployment,service}.yaml
    agent-daemonset.yaml runner-pod-template.yaml
    migration-job.yaml bootstrap-job.yaml
    pdb.yaml networkpolicy.yaml servicemonitor.yaml
  crds/                         # none required in Phase 8
  tests/                        # helm-unittest fixtures
```

The Console is represented only by a disabled, documented image/service
contract until Phase 9 ships the application. Runner Pods are created by the
Agent; the chart supplies a reviewed template/configuration, never a fixed
Runner Deployment.

Implementation constraint: the current Agent binary supports `docker` and
`process` runtimes and its relay registry is node-local Unix sockets. Phase 8
therefore deploys the proven Docker runtime on trusted Kubernetes nodes while
shipping the hardened Runner Pod template as a non-enabled contract. A
Kubernetes-native Runner runtime requires a follow-up Agent adapter and a
network-capable relay transport; it must not be enabled by values alone.

## 5. Values and dependency modes

`values.yaml` contains safe non-secret defaults. Production requires
`postgres.external`, `redis.external`, TLS settings, image digests, and a
secret-provider choice. Optional `dependencies.postgres` and
`dependencies.redis` are development-only and are rejected when
`environment=production`. Secret values are never put in Helm values;
External Secrets Operator, Vault Agent, or a CSI/KMS provider materializes the
same file paths used by Compose. A pre-install check fails if required files,
CA bundles, or keyring identity are absent.

## 6. Workloads and lifecycle

- Control Plane: Deployment with at least two replicas, PodDisruptionBudget,
  readiness gated on migration/schema and dependency reachability, and
  `preStop` drain hook.
- Gateway: Deployment with two replicas only when exact aggregate-limit mode
  permits it; otherwise chart validation requires one active replica. It uses
  a rolling strategy that preserves listener capacity and route freshness.
- Agent: DaemonSet with one identity per node, host Docker/container-runtime
  socket mounted only here, restricted host path, and a bounded termination
  drain that marks the node unschedulable before exit.
- Migrations/bootstrap: idempotent Jobs with advisory-lock serialization,
  checksum labels, TTL cleanup, and no long-lived service account token.
- Runner: Agent-owned Pod spec with `networkPolicy` isolation, non-root UID
  65532, read-only root, dropped capabilities, resource limits, and owner
  references/finalizers.

All workloads set `seccompProfile=RuntimeDefault`, `allowPrivilegeEscalation=false`,
`runAsNonRoot=true`, and explicit CPU/memory requests and limits. Probes expose
bounded categories and never secrets. Termination grace periods cover Gateway
drain and Agent finalization deadlines.

## 7. Networking and authorization

Services are ClusterIP by default; Gateway exposure is opt-in via an
operator-selected LoadBalancer/NodePort/Ingress integration. NetworkPolicies
allow only documented flows: clients to Gateway listeners, Gateway/Agent/
Control Plane control traffic, Control Plane to PostgreSQL/Redis/OIDC, and
Agent to the runtime socket. Runner Pods have no control-plane access and only
the narrowly required egress. Separate ServiceAccounts and least-privilege
Roles prevent workloads from listing Secrets or creating arbitrary Pods;
Agent receives only the Runner lifecycle verbs in its namespace.

## 8. Upgrade, drain, and failure semantics

Helm hooks run pre-upgrade validation and the migration Job before new Pods
become ready. `maxUnavailable=0` is required for Control Plane; Gateway uses
surge capacity and readiness gates. Previous and current protocol versions may
coexist only within the compatibility matrix. `helm rollback` is allowed only
after manifest/schema/keyring checks and a compatible database snapshot.

Node drain invokes an Agent drain operation, blocks new assignments, waits for
eligible Runners to reschedule, and reports stuck or non-evictable workloads.
Agent loss causes lease expiry and bounded reassignment; Redis loss preserves
existing safe traffic but blocks unsafe new pooled allocations. These outcomes
are observable and audited, not hidden by Kubernetes restart behavior.

## 9. Testing and exit criteria

CI renders the chart with strict schema validation, runs lint and template
security assertions, then installs into a disposable Kind or k3d cluster.
Tests cover fixed/pool HTTP, HTTPS CONNECT, and SOCKS5 paths; rolling upgrade
from the previous manifest; rollback; node drain; Agent deletion/restart;
Redis interruption/recovery; duplicate events; cross-tenant isolation; PDB
and topology behavior; and secret non-disclosure in rendered YAML, Pod specs,
events, and logs.

Phase 8 exits only when a clean temporary cluster passes those gates, protocol
versions roll without control-plane downtime, node drain preserves quota and
concurrency invariants, and all security policies are enforced by admission
and runtime checks.
