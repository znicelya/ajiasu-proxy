# AJiaSu Helm chart (Phase 8)

The chart intentionally requires immutable image digests and a pre-created
Secret materialized by External Secrets, Vault, or a CSI/KMS integration.
Secret values are never accepted directly in Helm values.

Example validation command:

```powershell
helm lint deploy/helm/ajiasu `
  --set images.controlPlane.digest=sha256:<64-hex> `
  --set images.gateway.digest=sha256:<64-hex> `
  --set images.agent.digest=sha256:<64-hex> `
  --set images.runner.digest=sha256:<64-hex> `
  --set secrets.existingSecret=ajiasu-runtime `
  --set gateway.config.certificateFingerprint=<fingerprint> `
  --set postgres.external.host=<postgres-host> `
  --set redis.external.host=<redis-host>
```

The runtime Secret must contain the keys configured under
`secrets.keys`. A production deployment should set
`global.environment=external-dependencies`, provide external PostgreSQL and
Redis hosts, and install the chart with a reviewed release manifest.

The current Agent binary supports the `docker` and `process` runtimes. The
Helm package therefore keeps the Docker runtime as the executable path and
ships the hardened Runner Pod template as the Kubernetes ownership contract.
Do not enable a Kubernetes-native Runner runtime until the Agent has a
network-capable Runner relay adapter; the existing relay contract is based on
node-local Unix sockets.
