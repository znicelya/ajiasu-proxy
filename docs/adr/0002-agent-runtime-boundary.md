# ADR 0002: Node Agent Owns the Runner Runtime Boundary

Status: Accepted

The Node Agent is the only platform component allowed to access the local container runtime. The Control Plane, Gateway, Console, PostgreSQL, and Redis never receive the Docker socket or equivalent host-runtime authority.

Every AJiaSu Runner is created from an immutable reviewed image and owns a distinct container, network namespace, tmpfs configuration directory, and cache directory. Host networking, broad host mounts, credential environment variables, and default privileged mode are prohibited.

The Agent accepts only authenticated, version-negotiated commands from the Control Plane. Commands are idempotent by Runner ID, operation ID, and desired generation. The Agent may inspect and garbage-collect only runtimes carrying the exact platform ownership and local node labels.

Plaintext AJiaSu credentials exist only in zeroized process memory and the target Runner's private tmpfs. They are forbidden in persistent Agent state, runtime labels, command arguments, environment variables, logs, traces, and operation results.

