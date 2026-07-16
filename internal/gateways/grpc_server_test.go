package gateways

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"io"
	"testing"
	"time"

	gatewayv1 "github.com/dnomd343/ajiasu-proxy/internal/gen/gateway/v1"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"
)

type scriptedGatewayStream struct {
	context  context.Context
	received []*gatewayv1.GatewayMessage
	sent     []*gatewayv1.ControlMessage
}

func (s *scriptedGatewayStream) Context() context.Context     { return s.context }
func (s *scriptedGatewayStream) SetHeader(metadata.MD) error  { return nil }
func (s *scriptedGatewayStream) SendHeader(metadata.MD) error { return nil }
func (s *scriptedGatewayStream) SetTrailer(metadata.MD)       {}
func (s *scriptedGatewayStream) SendMsg(value any) error {
	message, ok := value.(*gatewayv1.ControlMessage)
	if !ok {
		return ErrInvalidArgument
	}
	return s.Send(message)
}
func (s *scriptedGatewayStream) RecvMsg(value any) error {
	message, err := s.Recv()
	if err != nil {
		return err
	}
	target, ok := value.(*gatewayv1.GatewayMessage)
	if !ok {
		return ErrInvalidArgument
	}
	proto.Merge(target, message)
	return nil
}
func (s *scriptedGatewayStream) Send(message *gatewayv1.ControlMessage) error {
	s.sent = append(s.sent, message)
	return nil
}
func (s *scriptedGatewayStream) Recv() (*gatewayv1.GatewayMessage, error) {
	if len(s.received) == 0 {
		return nil, io.EOF
	}
	message := s.received[0]
	s.received = s.received[1:]
	return message, nil
}

func TestGatewayGRPCRegistrationAndSessionAuthentication(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	pool, err := pgxpool.New(t.Context(), postgres.PlatformDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	service, err := NewService(&database.Pools{Platform: pool})
	if err != nil {
		t.Fatal(err)
	}
	baseTime := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return baseTime }
	const fingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	_, enrollmentToken, err := service.CreateEnrollment(t.Context(), "gateway-a", fingerprint, uuid.New(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewGRPCServer(service, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	instanceID := uuid.New()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-ajiasu-certificate-fingerprint", fingerprint))
	response, err := server.RegisterGateway(ctx, &gatewayv1.RegisterGatewayRequest{
		EnrollmentToken: enrollmentToken, GatewayInstanceId: instanceID.String(), RequestedGatewayName: "gateway-a",
		MinimumProtocolRevision: 1, MaximumProtocolRevision: 1, GatewayVersion: "test", Architecture: "amd64", ListenerProtocols: []string{"http", "connect", "socks5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	gatewayID, err := uuid.Parse(response.GetGatewayId())
	if err != nil || response.GetSelectedProtocolRevision() != 1 {
		t.Fatalf("registration response=%#v error=%v", response, err)
	}
	if _, err := service.AuthenticateSession(t.Context(), response.GetSessionToken(), gatewayID, instanceID, 1); err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return baseTime.Add(14 * time.Minute) }
	if err := service.RecordHeartbeat(t.Context(), gatewayID, baseTime.Add(14*time.Minute)); err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return baseTime.Add(20 * time.Minute) }
	if _, err := service.AuthenticateSession(t.Context(), response.GetSessionToken(), gatewayID, instanceID, 1); err != nil {
		t.Fatalf("heartbeat did not extend durable session: %v", err)
	}
	if _, err := server.RegisterGateway(ctx, &gatewayv1.RegisterGatewayRequest{EnrollmentToken: enrollmentToken, GatewayInstanceId: uuid.NewString(), RequestedGatewayName: "gateway-a", MinimumProtocolRevision: 1, MaximumProtocolRevision: 1}); err == nil {
		t.Fatal("consumed enrollment was accepted")
	}
}

func TestSnapshotMessagePreservesAssignmentAndGrantMetadata(t *testing.T) {
	now := time.Now().UTC()
	gatewayID, tenantID, endpointID, runnerID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	message := snapshotMessage(Snapshot{Version: 9, GeneratedAt: now, Routes: []Route{{
		TenantID: tenantID, EndpointID: endpointID, PolicyHash: "hash", Protocols: []string{"connect"},
		Grant:        RouteGrant{GatewayID: gatewayID, TenantID: tenantID, EndpointID: endpointID, RunnerID: runnerID, Generation: 4, Protocols: []string{"connect"}, PolicyHash: "hash", ExpiresAt: now.Add(time.Minute), Signature: []byte{1}},
		AssignmentID: uuid.New(), AssignmentGeneration: 4, AccountID: uuid.New(), NodeID: uuid.New(), AssignmentState: "assigned", ValidUntil: now.Add(time.Minute),
	}}})
	snapshot := message.GetRouteSnapshot()
	if snapshot == nil || snapshot.GetSnapshotVersion() != 9 || len(snapshot.GetRoutes()) != 1 || snapshot.GetRoutes()[0].GetGrant().GetRunnerId() != runnerID.String() {
		t.Fatalf("snapshot message=%#v", message)
	}
}

func TestGatewayRegistrationStreamsInitialSignedSnapshot(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	pool, err := pgxpool.New(t.Context(), postgres.PlatformDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	service, err := NewService(&database.Pools{Platform: pool})
	if err != nil {
		t.Fatal(err)
	}
	const fingerprint = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	_, enrollmentToken, err := service.CreateEnrollment(t.Context(), "gateway-stream", fingerprint, uuid.New(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewGRPCServer(service, nil, func(_ context.Context, gatewayID uuid.UUID) (Snapshot, error) {
		return BuildSnapshot(1, now, privateKey, []SnapshotInput{{
			GatewayID: gatewayID, TenantID: uuid.New(), EndpointID: uuid.New(), RunnerID: uuid.New(),
			AssignmentID: uuid.New(), AccountID: uuid.New(), NodeID: uuid.New(), Generation: 1,
			AssignmentGeneration: 1, AssignmentState: "assigned", PolicyHash: "hash",
			Protocols: []string{"connect"}, GrantExpiry: now.Add(time.Minute), ValidUntil: now.Add(time.Minute),
		}})
	})
	if err != nil {
		t.Fatal(err)
	}
	instanceID := uuid.New()
	registrationContext := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-ajiasu-certificate-fingerprint", fingerprint))
	registration, err := server.RegisterGateway(registrationContext, &gatewayv1.RegisterGatewayRequest{
		EnrollmentToken: enrollmentToken, GatewayInstanceId: instanceID.String(), RequestedGatewayName: "gateway-stream",
		MinimumProtocolRevision: 1, MaximumProtocolRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := &scriptedGatewayStream{
		context: metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+registration.GetSessionToken(), "x-ajiasu-certificate-fingerprint", fingerprint)),
		received: []*gatewayv1.GatewayMessage{{Body: &gatewayv1.GatewayMessage_Hello{Hello: &gatewayv1.GatewayHello{
			GatewayId: registration.GetGatewayId(), GatewayInstanceId: instanceID.String(), ProtocolRevision: 1,
		}}}},
	}
	if err := server.ControlStream(stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetRouteSnapshot() == nil || len(stream.sent[0].GetRouteSnapshot().GetRoutes()) != 1 {
		t.Fatalf("streamed messages=%#v", stream.sent)
	}
}

func TestGatewayCertificateFingerprintPrefersTLSPeerIdentity(t *testing.T) {
	rawCertificate := []byte("gateway-certificate")
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-ajiasu-certificate-fingerprint", "spoofed-metadata-fingerprint",
	))
	ctx = peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{Raw: rawCertificate}},
	}}})
	digest := sha256.Sum256(rawCertificate)
	if got, want := certificateFingerprint(ctx), hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("fingerprint=%q, want TLS peer digest %q", got, want)
	}
}
