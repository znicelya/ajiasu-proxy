package integration_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/audit"
	agentv1 "github.com/znicelya/ajiasu-proxy/internal/gen/agent/v1"
	"github.com/znicelya/ajiasu-proxy/internal/nodes"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/tenancy"
	"github.com/znicelya/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestPhase4NodeEnrollmentRegistrationAndMaintenance(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	platform := openPhase4Pool(t, postgres.PlatformDSN)
	service, err := nodes.NewService(&database.Pools{Platform: platform}, audit.NewService())
	if err != nil {
		t.Fatal(err)
	}
	actor := phase4PlatformActor(t)
	created, err := service.CreateEnrollment(t.Context(), actor, nodes.CreateEnrollment{ExpectedNodeName: "edge-cn-1", ValidFor: 15 * time.Minute})
	if err != nil || created.Token == "" || created.Enrollment.TokenPrefix == "" {
		t.Fatalf("CreateEnrollment() = %#v, %v", created, err)
	}
	registered, err := service.Register(t.Context(), nodes.NodeRegistration{EnrollmentToken: created.Token, AgentInstanceID: uuid.New(), RequestedNodeName: "edge-cn-1", MinimumRevision: 1, MaximumRevision: 2, AgentVersion: "phase4-test", Architecture: "amd64", RuntimeCapabilities: []string{"process"}})
	if err != nil || registered.ProtocolRevision != 2 || registered.SessionToken == "" || registered.Node.ID == uuid.Nil {
		t.Fatalf("Register() = %#v, %v", registered, err)
	}
	if _, err = service.Register(t.Context(), nodes.NodeRegistration{EnrollmentToken: created.Token, AgentInstanceID: uuid.New(), RequestedNodeName: "edge-cn-1", MinimumRevision: 1, MaximumRevision: 2}); !errors.Is(err, nodes.ErrTokenInvalid) {
		t.Fatalf("reused enrollment error = %v", err)
	}
	items, err := service.List(t.Context(), actor, time.Time{}, uuid.Nil, 10)
	if err != nil || len(items) != 1 || items[0].ID != registered.Node.ID {
		t.Fatalf("List() = %#v, %v", items, err)
	}
	updated, err := service.SetMaintenance(t.Context(), actor, registered.Node.ID, 1, nodes.MaintenanceCordoned)
	if err != nil || updated.MaintenanceState != nodes.MaintenanceCordoned || updated.SessionGeneration != 2 || updated.Version != 2 {
		t.Fatalf("SetMaintenance() = %#v, %v", updated, err)
	}
	if _, err = service.SetMaintenance(t.Context(), actor, registered.Node.ID, 1, nodes.MaintenanceDisabled); !errors.Is(err, nodes.ErrVersionConflict) {
		t.Fatalf("stale maintenance error = %v", err)
	}
	grpcEnrollment, err := service.CreateEnrollment(t.Context(), actor, nodes.CreateEnrollment{ExpectedNodeName: "grpc-edge", ValidFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	grpcService, _ := nodes.NewGRPCServer(service)
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	nodes.RegisterAgentControlServer(server, grpcService)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	request := &agentv1.RegisterNodeRequest{EnrollmentToken: grpcEnrollment.Token, AgentInstanceId: uuid.New().String(), RequestedNodeName: "grpc-edge", MinimumProtocolRevision: 1, MaximumProtocolRevision: 2, AgentVersion: "grpc-test", Architecture: "amd64"}
	response := new(agentv1.RegisterNodeResponse)
	if err = connection.Invoke(t.Context(), "/ajiasu.agent.v1.AgentControl/RegisterNode", request, response); err != nil {
		t.Fatalf("gRPC RegisterNode: %v", err)
	}
	if response.GetSelectedProtocolRevision() != 2 || response.GetNodeId() == "" || response.GetSessionToken() == "" {
		t.Fatalf("gRPC response = %#v", response)
	}
}

func phase4PlatformActor(t *testing.T) tenancy.PlatformActor {
	t.Helper()
	actor, err := tenancy.NewPlatformActor(tenancy.Subject{ActorID: uuid.New(), PlatformRoles: []tenancy.Role{tenancy.PlatformAdmin}}, tenancy.ActorMetadata{ActorType: "user", SourceIP: netip.MustParseAddr("127.0.0.1"), UserAgent: "phase4-test", RequestID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	return actor
}
func openPhase4Pool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = pool.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	return pool
}
