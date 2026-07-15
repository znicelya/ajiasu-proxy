# Compatibility Matrix

| Component | Current | Previous/rolling compatibility | Phase 6 constraint |
| --- | --- | --- | --- |
| Control Plane schema | 11 | Schema 10 is rollback-only | Runtime readiness requires exactly schema 11; do not mix schema-10 and schema-11 control-plane replicas. |
| Scheduler protocol | revision 1 | No previous revision | Unknown revisions and stale generations are rejected. |
| Gateway control protocol | revision 1 | revision 1 | Assignment metadata is additive; Gateways require a snapshot after any delta gap. |
| Agent control protocol | revision 2 | revision 1 | Runner generation remains monotonic; relay authorization also requires current assignment generation and validity. |
| OpenAPI | `/api/v1` | Additive changes only | Assignment get/reconcile routes are registered and checked against the OpenAPI document. |
| Redis | RESP2 with Lua scripting | Reconstruct from PostgreSQL after loss | Redis is coordination only; AUTH, SELECT, EVAL, GET, SET, DEL, INCR, and PEXPIRE are required. |
| PostgreSQL | 17 locked in CI | Migration rehearsal from schema 10 | Scheduler tables use forced RLS and PostgreSQL committed assignments are authoritative. |
| Gateway aggregate limits | one active Gateway for exact totals | Same as Phase 5 | Scheduler fencing does not yet provide global multi-Gateway traffic counters. |

Update this matrix whenever protocol revisions, schema readiness, Redis command
requirements, or the exact-limit Gateway constraint changes.
