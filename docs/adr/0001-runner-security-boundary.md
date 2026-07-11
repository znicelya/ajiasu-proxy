# ADR 0001: AJiaSu Runner Is the Privilege Boundary

Status: Accepted

The unmodified AJiaSu process runs only inside a dedicated Runner container or Pod. Control Plane, Web Console, Gateway, PostgreSQL, and Redis never receive Runner Linux capabilities or container-runtime access.

The Runner starts non-root. Any required `NET_ADMIN`, TUN device, route, or namespace privilege is granted explicitly by deployment configuration after a smoke test proves it is necessary. `privileged: true`, host PID, and host IPC are prohibited defaults.

Each active AJiaSu connection receives a separate cache/config directory and network namespace. A Runner never serves multiple tenants.
