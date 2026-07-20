package audit_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/audit"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAppendStoresWhitelistedAuditAndOutboxAtomically(t *testing.T) {
	db := startAuditDatabase(t)
	createBusinessFixture(t, db.admin)

	tenantID := uuid.New()
	actorID := uuid.New()
	resourceID := uuid.New()
	event, outbox := integrationAppendInput(tenantID, actorID, resourceID, time.Now().UTC())

	result, err := database.InTenantTx(t.Context(), db.tenant, tenantID, actorID, func(ctx context.Context, tx pgx.Tx) (string, error) {
		if _, err := tx.Exec(ctx, "INSERT INTO platform.audit_atomicity_fixture (id, marker) VALUES ($1, $2)", resourceID, "committed"); err != nil {
			return "", err
		}
		if err := audit.NewService().Append(ctx, tx, event, outbox); err != nil {
			return "", err
		}
		return "committed", nil
	})
	if err != nil {
		t.Fatalf("commit business, audit, and outbox transaction: %v", err)
	}
	if result != "committed" {
		t.Fatalf("transaction result = %q, want committed", result)
	}

	var businessRows, auditRows, outboxRows int
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM platform.audit_atomicity_fixture WHERE id = $1 AND marker = 'committed'", resourceID).Scan(&businessRows); err != nil {
		t.Fatalf("count committed business rows: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), `
SELECT count(*)
FROM audit.audit_events
WHERE request_id = $1
  AND tenant_id = $2
  AND actor_id = $3
  AND action = 'membership.updated'
  AND resource_type = 'membership'
  AND resource_id = $4
  AND result = 'success'
  AND details = '{"reason_category":"requested"}'::jsonb
`, event.RequestID, tenantID, actorID, resourceID).Scan(&auditRows); err != nil {
		t.Fatalf("count committed audit rows: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), `
SELECT count(*)
FROM platform.outbox_events
WHERE aggregate_type = 'membership'
  AND aggregate_id = $1
  AND event_type = 'tenancy.membership.updated'
  AND payload_version = 1
  AND payload = '{"status":"active"}'::jsonb
`, resourceID).Scan(&outboxRows); err != nil {
		t.Fatalf("count committed outbox rows: %v", err)
	}
	if businessRows != 1 || auditRows != 1 || outboxRows != 1 {
		t.Fatalf("committed row counts = business %d audit %d outbox %d, want 1/1/1", businessRows, auditRows, outboxRows)
	}
	var auditID, outboxID uuid.UUID
	if err := db.admin.QueryRow(t.Context(), "SELECT id FROM audit.audit_events WHERE request_id = $1", event.RequestID).Scan(&auditID); err != nil {
		t.Fatalf("read generated audit ID: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT id FROM platform.outbox_events WHERE aggregate_id = $1", resourceID).Scan(&outboxID); err != nil {
		t.Fatalf("read generated outbox ID: %v", err)
	}
	if auditID.Version() != uuid.Version(7) || outboxID.Version() != uuid.Version(7) {
		t.Fatalf("generated UUID versions = audit %d outbox %d, want 7/7", auditID.Version(), outboxID.Version())
	}
	if auditID == uuid.Nil || outboxID == uuid.Nil || auditID == outboxID {
		t.Fatalf("generated audit/outbox IDs must be nonzero and distinct: %s %s", auditID, outboxID)
	}
}

func TestAuditMigrationDownAndUpPreservesPlatformFoundation(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	testkit.MigrationsDownTo(t, postgres.AdminDSN, 1)

	admin := openAuditPool(t, postgres.AdminDSN)
	var foundationExists, auditExists, outboxExists bool
	if err := admin.QueryRow(t.Context(), `
SELECT to_regprocedure('platform.current_tenant_id()') IS NOT NULL,
       to_regclass('audit.audit_events') IS NOT NULL,
       to_regclass('platform.outbox_events') IS NOT NULL
`).Scan(&foundationExists, &auditExists, &outboxExists); err != nil {
		t.Fatalf("inspect partial audit migration rollback: %v", err)
	}
	if !foundationExists || auditExists || outboxExists {
		t.Fatalf("migration state after down-to-1: foundation=%t audit=%t outbox=%t", foundationExists, auditExists, outboxExists)
	}

	testkit.MigrationsUp(t, postgres.AdminDSN)
	if err := admin.QueryRow(t.Context(), `
SELECT to_regclass('audit.audit_events') IS NOT NULL,
       to_regclass('platform.outbox_events') IS NOT NULL
`).Scan(&auditExists, &outboxExists); err != nil {
		t.Fatalf("inspect audit migration reapply: %v", err)
	}
	if !auditExists || !outboxExists {
		t.Fatalf("migration state after reapply: audit=%t outbox=%t", auditExists, outboxExists)
	}
}

func TestAuditFailureRollsBackBusinessTransaction(t *testing.T) {
	db := startAuditDatabase(t)
	createBusinessFixture(t, db.admin)

	tenantID := uuid.New()
	otherTenantID := uuid.New()
	actorID := uuid.New()
	resourceID := uuid.New()
	event, outbox := integrationAppendInput(otherTenantID, actorID, resourceID, time.Now().UTC())

	_, err := database.InTenantTx(t.Context(), db.tenant, tenantID, actorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		if _, err := tx.Exec(ctx, "INSERT INTO platform.audit_atomicity_fixture (id, marker) VALUES ($1, $2)", resourceID, "must-roll-back"); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, audit.NewService().Append(ctx, tx, event, outbox)
	})
	if err == nil {
		t.Fatal("tenant transaction accepted an audit event for a different tenant")
	}
	assertNoAtomicRows(t, db.admin, resourceID, event.RequestID)
}

func TestOutboxFailureRollsBackBusinessTransactionAndAudit(t *testing.T) {
	db := startAuditDatabase(t)
	createBusinessFixture(t, db.admin)
	installRejectOutboxTrigger(t, db.admin)

	tenantID := uuid.New()
	actorID := uuid.New()
	resourceID := uuid.New()
	event, outbox := integrationAppendInput(tenantID, actorID, resourceID, time.Now().UTC())

	_, err := database.InTenantTx(t.Context(), db.tenant, tenantID, actorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		if _, err := tx.Exec(ctx, "INSERT INTO platform.audit_atomicity_fixture (id, marker) VALUES ($1, $2)", resourceID, "must-roll-back"); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, audit.NewService().Append(ctx, tx, event, outbox)
	})
	if err == nil {
		t.Fatal("transaction succeeded despite forced outbox insertion failure")
	}
	requirePGCode(t, err, "55000")
	assertNoAtomicRows(t, db.admin, resourceID, event.RequestID)
}

func TestAppendIsAtomicWhenExecutorIsPool(t *testing.T) {
	db := startAuditDatabase(t)
	installRejectOutboxTrigger(t, db.admin)

	actorID := uuid.New()
	resourceID := uuid.New()
	event, outbox := integrationAppendInput(uuid.Nil, actorID, resourceID, time.Now().UTC())
	event.TenantID = nil

	err := audit.NewService().Append(t.Context(), db.platform, event, outbox)
	if err == nil {
		t.Fatal("Append() with pool succeeded despite forced outbox failure")
	}
	requirePGCode(t, err, "55000")

	var auditRows, outboxRows int
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM audit.audit_events WHERE request_id = $1", event.RequestID).Scan(&auditRows); err != nil {
		t.Fatalf("count pool-atomic audit rows: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM platform.outbox_events WHERE aggregate_id = $1", resourceID).Scan(&outboxRows); err != nil {
		t.Fatalf("count pool-atomic outbox rows: %v", err)
	}
	if auditRows != 0 || outboxRows != 0 {
		t.Fatalf("rows after pool append failure = audit %d outbox %d, want 0/0", auditRows, outboxRows)
	}
}

func TestApplicationRoleCannotUpdateOrDeleteAudit(t *testing.T) {
	db := startAuditDatabase(t)

	tenantID := uuid.New()
	actorID := uuid.New()
	resourceID := uuid.New()
	event, outbox := integrationAppendInput(tenantID, actorID, resourceID, time.Now().UTC())
	appendTenantEvent(t, db.tenant, tenantID, actorID, event, outbox)

	for _, privilege := range []string{"UPDATE", "DELETE"} {
		var allowed bool
		if err := db.tenant.QueryRow(t.Context(), "SELECT has_table_privilege(current_user, 'audit.audit_events', $1)", privilege).Scan(&allowed); err != nil {
			t.Fatalf("inspect %s privilege: %v", privilege, err)
		}
		if allowed {
			t.Fatalf("application role unexpectedly has %s on audit.audit_events", privilege)
		}
	}

	if _, err := db.tenant.Exec(t.Context(), "UPDATE audit.audit_events SET result = 'tampered' WHERE request_id = $1", event.RequestID); err == nil {
		t.Fatal("application role updated audit row without UPDATE grant")
	} else {
		requirePGCode(t, err, "42501")
	}
	if _, err := db.tenant.Exec(t.Context(), "DELETE FROM audit.audit_events WHERE request_id = $1", event.RequestID); err == nil {
		t.Fatal("application role deleted audit row without DELETE grant")
	} else {
		requirePGCode(t, err, "42501")
	}

	if _, err := db.admin.Exec(t.Context(), "GRANT UPDATE, DELETE ON audit.audit_events TO ajiasu_app"); err != nil {
		t.Fatalf("temporarily grant audit mutation privileges: %v", err)
	}
	mutations := []struct {
		name string
		sql  string
	}{
		{name: "update", sql: "UPDATE audit.audit_events SET result = 'tampered' WHERE request_id = $1"},
		{name: "delete", sql: "DELETE FROM audit.audit_events WHERE request_id = $1"},
	}
	for _, mutation := range mutations {
		t.Run("trigger_"+mutation.name, func(t *testing.T) {
			_, err := database.InTenantTx(t.Context(), db.tenant, tenantID, actorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
				_, err := tx.Exec(ctx, mutation.sql, event.RequestID)
				return struct{}{}, err
			})
			if err == nil {
				t.Fatalf("audit %s bypassed immutability trigger", mutation.name)
			}
			requirePGCode(t, err, "55000")
		})
	}

	var result string
	if err := db.admin.QueryRow(t.Context(), "SELECT result FROM audit.audit_events WHERE request_id = $1", event.RequestID).Scan(&result); err != nil {
		t.Fatalf("read immutable audit row: %v", err)
	}
	if result != "success" {
		t.Fatalf("audit result after rejected mutations = %q, want success", result)
	}
}

func TestAuditRLSSeparatesTenantAndPlatformEvents(t *testing.T) {
	db := startAuditDatabase(t)
	service := audit.NewService()
	actorID := uuid.New()
	tenantA := uuid.New()
	tenantB := uuid.New()
	now := time.Now().UTC()

	eventA, outboxA := integrationAppendInput(tenantA, actorID, uuid.New(), now)
	eventB, outboxB := integrationAppendInput(tenantB, actorID, uuid.New(), now.Add(time.Second))
	platformEvent, platformOutbox := integrationAppendInput(uuid.Nil, actorID, uuid.New(), now.Add(2*time.Second))
	platformEvent.TenantID = nil
	platformEvent.Action = "platform.audit.read"
	platformOutbox.EventType = "platform.audit.read"

	appendTenantEvent(t, db.tenant, tenantA, actorID, eventA, outboxA)
	appendTenantEvent(t, db.tenant, tenantB, actorID, eventB, outboxB)
	if _, err := database.InPlatformTx(t.Context(), db.platform, actorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		return struct{}{}, service.Append(ctx, tx, platformEvent, platformOutbox)
	}); err != nil {
		t.Fatalf("append platform audit event: %v", err)
	}

	assertTenantAuditVisibility(t, db.tenant, tenantA, actorID, eventA.RequestID)
	assertTenantAuditVisibility(t, db.tenant, tenantB, actorID, eventB.RequestID)

	var withoutContext int
	if err := db.tenant.QueryRow(t.Context(), "SELECT count(*) FROM audit.audit_events").Scan(&withoutContext); err != nil {
		t.Fatalf("query audit without tenant context: %v", err)
	}
	if withoutContext != 0 {
		t.Fatalf("tenant audit rows visible without context = %d, want 0", withoutContext)
	}

	var platformCount int
	if err := db.platform.QueryRow(t.Context(), "SELECT count(*) FROM audit.audit_events").Scan(&platformCount); err != nil {
		t.Fatalf("query audit through platform pool: %v", err)
	}
	if platformCount != 3 {
		t.Fatalf("platform audit row count = %d, want 3", platformCount)
	}

	wrongTenantEvent, wrongTenantOutbox := integrationAppendInput(tenantB, actorID, uuid.New(), now.Add(3*time.Second))
	if _, err := database.InTenantTx(t.Context(), db.tenant, tenantA, actorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		return struct{}{}, service.Append(ctx, tx, wrongTenantEvent, wrongTenantOutbox)
	}); err == nil {
		t.Fatal("Tenant A transaction inserted Tenant B audit event")
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM audit.audit_events").Scan(&platformCount); err != nil {
		t.Fatalf("count audit rows after rejected cross-tenant append: %v", err)
	}
	if platformCount != 3 {
		t.Fatalf("audit row count after rejected cross-tenant append = %d, want 3", platformCount)
	}
}

func TestOutboxLeaseUsesSkipLocked(t *testing.T) {
	db := startAuditDatabase(t)
	actorID := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 3; i++ {
		event, outbox := integrationAppendInput(uuid.Nil, actorID, uuid.New(), now.Add(time.Duration(i)*time.Second))
		event.TenantID = nil
		event.Action = fmt.Sprintf("outbox.fixture.%d", i)
		outbox.EventType = event.Action
		appendPlatformEvent(t, db.platform, actorID, event, outbox)
	}

	tx1, err := db.platform.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin first lease transaction: %v", err)
	}
	defer tx1.Rollback(context.Background())
	owner1 := uuid.New()
	first, err := audit.LeaseOutbox(t.Context(), tx1, audit.LeaseRequest{
		OwnerID:       owner1,
		Limit:         1,
		LeaseDuration: time.Minute,
		Now:           now.Add(10 * time.Second),
	})
	if err != nil {
		t.Fatalf("lease first locked outbox row: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first lease count = %d, want 1", len(first))
	}
	if first[0].Attempts != 1 || first[0].LeaseOwner == nil || *first[0].LeaseOwner != owner1 {
		t.Fatalf("first lease metadata = attempts %d owner %v", first[0].Attempts, first[0].LeaseOwner)
	}
	if first[0].EventType != "outbox.fixture.0" {
		t.Fatalf("first deterministic lease event type = %q, want outbox.fixture.0", first[0].EventType)
	}

	tx2, err := db.platform.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin second lease transaction: %v", err)
	}
	defer tx2.Rollback(context.Background())
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	second, err := audit.LeaseOutbox(ctx, tx2, audit.LeaseRequest{
		OwnerID:       uuid.New(),
		Limit:         2,
		LeaseDuration: time.Minute,
		Now:           now.Add(10 * time.Second),
	})
	if err != nil {
		t.Fatalf("second lease should skip locked row without blocking: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second lease count = %d, want 2", len(second))
	}
	if second[0].EventType != "outbox.fixture.1" || second[1].EventType != "outbox.fixture.2" {
		t.Fatalf("second deterministic lease order = %q, %q", second[0].EventType, second[1].EventType)
	}
	for _, record := range second {
		if record.ID == first[0].ID {
			t.Fatalf("concurrent leases both returned outbox event %s", record.ID)
		}
		if record.Attempts != 1 {
			t.Fatalf("second worker leased event %s with attempts %d, want 1", record.ID, record.Attempts)
		}
	}
}

func TestOutboxCompletionAndReleaseRequireCurrentOwner(t *testing.T) {
	db := startAuditDatabase(t)
	actorID := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)

	for i := 0; i < 2; i++ {
		event, outbox := integrationAppendInput(uuid.Nil, actorID, uuid.New(), now.Add(time.Duration(i)*time.Second))
		event.TenantID = nil
		event.Action = fmt.Sprintf("outbox.owner.fixture.%d", i)
		outbox.EventType = event.Action
		appendPlatformEvent(t, db.platform, actorID, event, outbox)
	}

	ownerID := uuid.New()
	leased := leaseOutbox(t, db.platform, actorID, audit.LeaseRequest{
		OwnerID:       ownerID,
		Limit:         1,
		LeaseDuration: time.Minute,
		Now:           now.Add(10 * time.Second),
	})
	if len(leased) != 1 {
		t.Fatalf("lease for completion count = %d, want 1", len(leased))
	}
	processedAt := now.Add(20 * time.Second)
	if completed := completeOutbox(t, db.platform, actorID, leased[0].ID, uuid.New(), processedAt); completed {
		t.Fatal("wrong owner completed an outbox event")
	}
	if completed := completeOutbox(t, db.platform, actorID, leased[0].ID, ownerID, processedAt); !completed {
		t.Fatal("current owner could not complete an outbox event")
	}

	remaining := leaseOutbox(t, db.platform, actorID, audit.LeaseRequest{
		OwnerID:       ownerID,
		Limit:         10,
		LeaseDuration: time.Minute,
		Now:           processedAt.Add(time.Second),
	})
	if len(remaining) != 1 {
		t.Fatalf("lease after completion count = %d, want only the unprocessed event", len(remaining))
	}
	if remaining[0].ID == leased[0].ID {
		t.Fatalf("processed outbox event %s was leased again", leased[0].ID)
	}

	availableAt := processedAt.Add(2 * time.Minute)
	if released := releaseOutbox(t, db.platform, actorID, remaining[0].ID, uuid.New(), availableAt); released {
		t.Fatal("wrong owner released an outbox event")
	}
	if released := releaseOutbox(t, db.platform, actorID, remaining[0].ID, ownerID, availableAt); !released {
		t.Fatal("current owner could not release an outbox event")
	}

	tooEarly := leaseOutbox(t, db.platform, actorID, audit.LeaseRequest{
		OwnerID:       uuid.New(),
		Limit:         10,
		LeaseDuration: time.Minute,
		Now:           availableAt.Add(-time.Second),
	})
	if len(tooEarly) != 0 {
		t.Fatalf("released event leased before available_at: %d rows", len(tooEarly))
	}
	readyOwner := uuid.New()
	ready := leaseOutbox(t, db.platform, actorID, audit.LeaseRequest{
		OwnerID:       readyOwner,
		Limit:         10,
		LeaseDuration: time.Minute,
		Now:           availableAt,
	})
	if len(ready) != 1 || ready[0].ID != remaining[0].ID {
		t.Fatalf("released event lease at availability = %#v, want event %s", ready, remaining[0].ID)
	}
	if ready[0].Attempts != 2 {
		t.Fatalf("released event attempts after re-lease = %d, want 2", ready[0].Attempts)
	}

	expiredOwner := uuid.New()
	reclaimed := leaseOutbox(t, db.platform, actorID, audit.LeaseRequest{
		OwnerID:       expiredOwner,
		Limit:         10,
		LeaseDuration: time.Minute,
		Now:           availableAt.Add(time.Minute),
	})
	if len(reclaimed) != 1 || reclaimed[0].ID != remaining[0].ID || reclaimed[0].Attempts != 3 {
		t.Fatalf("expired lease reclaim = %#v, want event %s with attempt 3", reclaimed, remaining[0].ID)
	}
}

type auditDatabase struct {
	admin    *pgxpool.Pool
	tenant   *pgxpool.Pool
	platform *pgxpool.Pool
}

func startAuditDatabase(t *testing.T) auditDatabase {
	t.Helper()
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	return auditDatabase{
		admin:    openAuditPool(t, postgres.AdminDSN),
		tenant:   openAuditPool(t, postgres.TenantDSN),
		platform: openAuditPool(t, postgres.PlatformDSN),
	}
}

func openAuditPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open audit PostgreSQL pool: %v", err)
	}
	if err := pool.Ping(t.Context()); err != nil {
		pool.Close()
		t.Fatalf("ping audit PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func integrationAppendInput(tenantID, actorID, resourceID uuid.UUID, availableAt time.Time) (audit.Event, audit.OutboxEvent) {
	event := audit.Event{
		ActorType:    "user",
		ActorID:      &actorID,
		TenantID:     &tenantID,
		Action:       "membership.updated",
		ResourceType: "membership",
		ResourceID:   &resourceID,
		Result:       "success",
		SourceIP:     netip.MustParseAddr("203.0.113.20"),
		UserAgent:    "audit-integration-test/1.0",
		RequestID:    uuid.New(),
		Details:      map[string]any{"reason_category": "requested"},
	}
	return event, audit.OutboxEvent{
		EventType:      "tenancy.membership.updated",
		AggregateType:  "membership",
		AggregateID:    resourceID,
		PayloadVersion: 1,
		Payload:        map[string]any{"status": "active"},
		AvailableAt:    availableAt,
	}
}

func appendTenantEvent(t *testing.T, pool *pgxpool.Pool, tenantID, actorID uuid.UUID, event audit.Event, outbox audit.OutboxEvent) {
	t.Helper()
	if _, err := database.InTenantTx(t.Context(), pool, tenantID, actorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		return struct{}{}, audit.NewService().Append(ctx, tx, event, outbox)
	}); err != nil {
		t.Fatalf("append tenant audit event: %v", err)
	}
}

func appendPlatformEvent(t *testing.T, pool *pgxpool.Pool, actorID uuid.UUID, event audit.Event, outbox audit.OutboxEvent) {
	t.Helper()
	if _, err := database.InPlatformTx(t.Context(), pool, actorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		return struct{}{}, audit.NewService().Append(ctx, tx, event, outbox)
	}); err != nil {
		t.Fatalf("append platform audit event: %v", err)
	}
}

func createBusinessFixture(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	statements := []string{
		"CREATE TABLE platform.audit_atomicity_fixture (id uuid PRIMARY KEY, marker text NOT NULL)",
		"GRANT INSERT, SELECT ON platform.audit_atomicity_fixture TO ajiasu_app, ajiasu_platform",
	}
	for _, statement := range statements {
		if _, err := admin.Exec(t.Context(), statement); err != nil {
			t.Fatalf("create audit atomicity fixture: %v", err)
		}
	}
}

func installRejectOutboxTrigger(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	statements := []string{
		`CREATE FUNCTION platform.reject_outbox_fixture() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'forced outbox fixture failure' USING ERRCODE = '55000';
END
$$`,
		"CREATE TRIGGER reject_outbox_fixture BEFORE INSERT ON platform.outbox_events FOR EACH ROW EXECUTE FUNCTION platform.reject_outbox_fixture()",
	}
	for _, statement := range statements {
		if _, err := admin.Exec(t.Context(), statement); err != nil {
			t.Fatalf("install outbox rejection fixture: %v", err)
		}
	}
}

func assertNoAtomicRows(t *testing.T, admin *pgxpool.Pool, resourceID, requestID uuid.UUID) {
	t.Helper()
	var businessRows, auditRows, outboxRows int
	if err := admin.QueryRow(t.Context(), "SELECT count(*) FROM platform.audit_atomicity_fixture WHERE id = $1", resourceID).Scan(&businessRows); err != nil {
		t.Fatalf("count rolled-back business rows: %v", err)
	}
	if err := admin.QueryRow(t.Context(), "SELECT count(*) FROM audit.audit_events WHERE request_id = $1", requestID).Scan(&auditRows); err != nil {
		t.Fatalf("count rolled-back audit rows: %v", err)
	}
	if err := admin.QueryRow(t.Context(), "SELECT count(*) FROM platform.outbox_events WHERE aggregate_id = $1", resourceID).Scan(&outboxRows); err != nil {
		t.Fatalf("count rolled-back outbox rows: %v", err)
	}
	if businessRows != 0 || auditRows != 0 || outboxRows != 0 {
		t.Fatalf("rows after rollback = business %d audit %d outbox %d, want 0/0/0", businessRows, auditRows, outboxRows)
	}
}

func assertTenantAuditVisibility(t *testing.T, pool *pgxpool.Pool, tenantID, actorID, expectedRequestID uuid.UUID) {
	t.Helper()
	requestIDs, err := database.InTenantTx(t.Context(), pool, tenantID, actorID, func(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error) {
		rows, err := tx.Query(ctx, "SELECT request_id FROM audit.audit_events ORDER BY created_at, id")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var result []uuid.UUID
		for rows.Next() {
			var requestID uuid.UUID
			if err := rows.Scan(&requestID); err != nil {
				return nil, err
			}
			result = append(result, requestID)
		}
		return result, rows.Err()
	})
	if err != nil {
		t.Fatalf("query tenant audit visibility: %v", err)
	}
	if len(requestIDs) != 1 || requestIDs[0] != expectedRequestID {
		t.Fatalf("tenant %s visible request IDs = %v, want [%s]", tenantID, requestIDs, expectedRequestID)
	}
}

func leaseOutbox(t *testing.T, pool *pgxpool.Pool, actorID uuid.UUID, request audit.LeaseRequest) []audit.OutboxRecord {
	t.Helper()
	records, err := database.InPlatformTx(t.Context(), pool, actorID, func(ctx context.Context, tx pgx.Tx) ([]audit.OutboxRecord, error) {
		return audit.LeaseOutbox(ctx, tx, request)
	})
	if err != nil {
		t.Fatalf("lease outbox: %v", err)
	}
	return records
}

func completeOutbox(t *testing.T, pool *pgxpool.Pool, actorID, eventID, ownerID uuid.UUID, processedAt time.Time) bool {
	t.Helper()
	completed, err := database.InPlatformTx(t.Context(), pool, actorID, func(ctx context.Context, tx pgx.Tx) (bool, error) {
		return audit.CompleteOutbox(ctx, tx, eventID, ownerID, processedAt)
	})
	if err != nil {
		t.Fatalf("complete outbox event: %v", err)
	}
	return completed
}

func releaseOutbox(t *testing.T, pool *pgxpool.Pool, actorID, eventID, ownerID uuid.UUID, availableAt time.Time) bool {
	t.Helper()
	released, err := database.InPlatformTx(t.Context(), pool, actorID, func(ctx context.Context, tx pgx.Tx) (bool, error) {
		return audit.ReleaseOutbox(ctx, tx, eventID, ownerID, availableAt)
	})
	if err != nil {
		t.Fatalf("release outbox event: %v", err)
	}
	return released
}

func requirePGCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want PostgreSQL error code %s", err, code)
	}
	if pgErr.Code != code {
		t.Fatalf("PostgreSQL error code = %s (%v), want %s", pgErr.Code, pgErr, code)
	}
}
