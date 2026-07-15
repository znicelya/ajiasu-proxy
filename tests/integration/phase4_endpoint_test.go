package integration_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/accounts"
	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/endpoints"
	"github.com/dnomd343/ajiasu-proxy/internal/nodes"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/keyring"
	"github.com/dnomd343/ajiasu-proxy/internal/reconciler"
	"github.com/dnomd343/ajiasu-proxy/internal/secrets"
	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
)

func TestPhase4FixedEndpointPersistsOperationWorkFinalizerAndReservation(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	admin := openPhase4Pool(t, postgres.AdminDSN)
	platform := openPhase4Pool(t, postgres.PlatformDSN)
	tenantPool := openPhase4Pool(t, postgres.TenantDSN)
	db := &database.Pools{Platform: platform, Tenant: tenantPool}
	nodeService, _ := nodes.NewService(db, audit.NewService())
	platformActor := phase4PlatformActor(t)
	enrollment, err := nodeService.CreateEnrollment(t.Context(), platformActor, nodes.CreateEnrollment{ExpectedNodeName: "endpoint-node", ValidFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	session, err := nodeService.Register(t.Context(), nodes.NodeRegistration{EnrollmentToken: enrollment.Token, AgentInstanceID: uuid.New(), RequestedNodeName: "endpoint-node", MinimumRevision: 1, MaximumRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Exec(t.Context(), `UPDATE nodes.nodes SET connectivity_state='online' WHERE id=$1`, session.Node.ID); err != nil {
		t.Fatal(err)
	}
	tenantID := uuid.New()
	now := time.Now().UTC()
	if _, err = admin.Exec(t.Context(), `INSERT INTO tenancy.tenants (id,slug,name,state,created_at,updated_at) VALUES ($1,'phase4-endpoint','Phase 4 Endpoint','active',$2,$2)`, tenantID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(t.Context(), `INSERT INTO tenancy.tenant_quotas (tenant_id,created_at,updated_at) VALUES ($1,$2,$2)`, tenantID, now); err != nil {
		t.Fatal(err)
	}
	ring, _ := keyring.NewAESGCM(bytes.Repeat([]byte{9}, 32))
	provider, _ := secrets.NewEnvelopeProvider(ring)
	accountService, _ := accounts.NewService(db, provider, audit.NewService())
	tenantActor := phase3Actor(t, tenantID)
	account, err := accountService.Create(t.Context(), tenantActor, accounts.CreateCommand{Name: "phase4-account", Credential: accounts.Credential{Username: "fake", Password: "fake-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	endpointService, _ := endpoints.NewService(db, audit.NewService())
	endpoint, operation, err := endpointService.Create(t.Context(), tenantActor, endpoints.CreateCommand{Name: "fixed-endpoint", AccountID: account.ID, NodeID: session.Node.ID, DesiredRunnerState: endpoints.DesiredRunning})
	if err != nil {
		t.Fatalf("Create endpoint: %v", err)
	}
	if operation.State != "queued" || endpoint.Status.ObservedState != endpoints.ObservedPending || endpoint.Status.RunnerID == nil {
		t.Fatalf("endpoint=%#v operation=%#v", endpoint, operation)
	}
	loaded, err := endpointService.Get(t.Context(), tenantActor, endpoint.ID)
	if err != nil || loaded.Status.RunnerID == nil || *loaded.Status.RunnerID != *endpoint.Status.RunnerID {
		t.Fatalf("Get endpoint=%#v err=%v", loaded, err)
	}
	var desired, work, finalizers, reservations int
	if err = admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM reconciler.runner_desired_states WHERE endpoint_id=$1),(SELECT count(*) FROM reconciler.work_items WHERE resource_id=$1),(SELECT count(*) FROM reconciler.finalizers WHERE resource_id=$1),(SELECT count(*) FROM accounts.account_capacity_reservations WHERE owner_id=$1)`, endpoint.ID).Scan(&desired, &work, &finalizers, &reservations); err != nil {
		t.Fatal(err)
	}
	if desired != 1 || work != 1 || finalizers != 1 || reservations != 1 {
		t.Fatalf("desired=%d work=%d finalizers=%d reservations=%d", desired, work, finalizers, reservations)
	}
	reconcileService, _ := reconciler.NewService(db, audit.NewService())
	observedAt := time.Now().UTC()
	if err = reconcileService.ApplyObservation(t.Context(), reconciler.Observation{NodeID: session.Node.ID, RunnerID: *endpoint.Status.RunnerID, OperationID: operation.ID, Generation: 1, State: reconciler.RunnerRunning, ReasonCode: "process_ready", ObservedAt: observedAt, Metadata: tenantActor.Metadata()}); err != nil {
		t.Fatalf("running observation: %v", err)
	}
	loaded, err = endpointService.Get(t.Context(), tenantActor, endpoint.ID)
	if err != nil || loaded.Status.ObservedState != endpoints.ObservedRunning {
		t.Fatalf("running endpoint=%#v err=%v", loaded, err)
	}
	deleting, deleteOperation, err := endpointService.RequestDelete(t.Context(), tenantActor, endpoint.ID, 1)
	if err != nil || deleting.LifecycleState != endpoints.LifecycleDeleting || deleteOperation.RequestedGeneration != 2 {
		t.Fatalf("RequestDelete() endpoint=%#v op=%#v err=%v", deleting, deleteOperation, err)
	}
	if err = reconcileService.ApplyObservation(t.Context(), reconciler.Observation{NodeID: session.Node.ID, RunnerID: *endpoint.Status.RunnerID, OperationID: deleteOperation.ID, Generation: 2, State: reconciler.RunnerStopped, ReasonCode: "runtime_absent", ObservedAt: time.Now().UTC(), Metadata: tenantActor.Metadata()}); err != nil {
		t.Fatalf("stopped observation: %v", err)
	}
	if _, err = endpointService.Get(t.Context(), tenantActor, endpoint.ID); err == nil {
		t.Fatal("deleted endpoint remained visible")
	}
	if err = admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM reconciler.runner_desired_states WHERE endpoint_id=$1),(SELECT count(*) FROM reconciler.finalizers WHERE resource_id=$1),(SELECT count(*) FROM accounts.account_capacity_reservations WHERE owner_id=$1)`, endpoint.ID).Scan(&desired, &finalizers, &reservations); err != nil {
		t.Fatal(err)
	}
	if desired != 0 || finalizers != 0 || reservations != 0 {
		t.Fatalf("cleanup desired=%d finalizers=%d reservations=%d", desired, finalizers, reservations)
	}
}
