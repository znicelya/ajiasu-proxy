# Control Plane Phase 4 Operations

Phase 4 adds an authenticated Node Agent channel and a fixed-account/fixed-node
endpoint lifecycle. The control plane remains the source of desired state;
PostgreSQL work items and finalizers make retries and restarts safe.

## Enrollment and TLS

Create a platform-admin enrollment through `POST /api/v1/node-enrollments` and
deliver the returned token to the node once. Production requires
`AJIASU_AGENT_GRPC_INSECURE=false`, a TLS certificate and a private key. The
insecure mode is accepted only on a loopback development bind.

The Agent stores only its node ID, protocol revision, instance ID and an opaque
session token under `AJIASU_AGENT_STATE_DIRECTORY`. Delete that state only as an
audited re-enrollment action.

## Node maintenance

`active` accepts explicitly bound runners. `cordoned` and `draining` retain
existing runners but reject new placement. `disabled` revokes the node session
generation and rejects all commands. Heartbeats mark nodes stale/offline without
releasing account reservations or creating replacements.

## Endpoint recovery

Endpoint writes return an operation ID. Inspect it through the tenant operation
routes. A runner cleanup finalizer remains until the Agent reports stopped or
absent; only then is account capacity released and a deleting endpoint removed.
Expired work leases are reclaimed by the reconciler worker after a control-plane
restart.

If a node is permanently unreachable, platform administrators must use the
audited force-finalization procedure (to be enabled with the platform recovery
runbook) and acknowledge the risk of a duplicate login. Automatic failover is
intentionally excluded from Phase 4.

## Docker boundary and secrets

Only the Agent accesses the local Docker Engine. Runner containers use an exact
platform ownership label, a private tmpfs, a read-only root filesystem,
`no-new-privileges`, dropped capabilities, non-root UID 65532, and no host
network. Credential bytes are delivered just in time and are not persisted in
operations, work items, labels, environment variables, logs or audit records.

Phase 4 does not expose a proxy listener, proxy credentials, pool scheduling,
traffic policy, automatic migration/failover, or Console APIs.
