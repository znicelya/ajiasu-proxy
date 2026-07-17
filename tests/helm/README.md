# Phase 8 Helm gate

Run `./tests/helm/run.ps1` for chart rendering and security assertions. Use
`-Cluster` on a host with Helm, kubectl, and Kind to create a disposable
cluster, install only the rendered objects with server-side validation, and
delete the cluster in `finally`.

The cluster gate intentionally uses fixture Secret values and does not start
real control-plane images. End-to-end proxy traffic, upgrade, drain, Redis
loss, and rollback require the release images and are run by the protected
environment workflow described in the Phase 8 operations guide.
