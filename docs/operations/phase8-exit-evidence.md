# Phase 8 Exit Evidence

## Implemented evidence

- Versioned Helm chart with immutable image schema and compatibility metadata.
- Control Plane and Gateway Deployments, Agent DaemonSet, Services, migration
  and bootstrap Jobs, PDBs, topology spread, RBAC, NetworkPolicy, state
  storage, Runner Pod security contract, and external Secret mapping example.
- Production preflight, install, status, node drain, and explicit-revision
  rollback scripts.
- Go contract tests, rendered-manifest security checks, disposable Kind gate,
  and GitHub Actions workflow.
- Upgrade, node drain, Agent loss, Redis loss, rollback, and evidence runbook.

## Locally verified on 2026-07-17

- `go test ./tests/contract -run Phase8 -count=1`: passed.
- PowerShell parser validation for every Phase 8 script: passed.
- `git diff --check`: passed.
- `values.schema.json` JSON parsing: passed.

## Environment-gated evidence

The current workstation has kubectl 1.34.1 but no Helm, no Kind, and no active
Kubernetes API endpoint. Consequently `helm lint`, `helm template`, the Kind
server-side validation gate, rolling upgrade, node drain, Redis interruption,
and rollback rehearsals must be executed by `.github/workflows/helm-ci.yml` or
on a prepared operator host before the Phase 8 exit gate is signed.

The current Agent executable supports `docker` and `process` runtimes. The
chart's executable Kubernetes path therefore uses the Agent's Docker runtime
on trusted Kubernetes nodes; the hardened Runner Pod template is a contract,
not an enabled Kubernetes-native runtime. Enabling Kubernetes-native Runner
Pods requires a new Agent runtime and a non-node-local Runner relay transport.
This architectural dependency must be resolved before claiming the roadmap's
Runner Pod rescheduling criterion as complete.
