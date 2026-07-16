package gateways

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	gatewayv1 "github.com/dnomd343/ajiasu-proxy/internal/gen/gateway/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const gatewayServiceName = "ajiasu.gateway.v1.GatewayControl"

type SnapshotProvider func(context.Context, uuid.UUID) (Snapshot, error)

type GRPCServer struct {
	service  *Service
	registry *StreamRegistry
	snapshot SnapshotProvider
}

func NewGRPCServer(service *Service, registry *StreamRegistry, snapshot SnapshotProvider) (*GRPCServer, error) {
	if service == nil {
		return nil, ErrInvalidArgument
	}
	if registry == nil {
		registry = NewStreamRegistry()
	}
	if snapshot == nil {
		snapshot = func(context.Context, uuid.UUID) (Snapshot, error) {
			return Snapshot{Version: 1, GeneratedAt: time.Now().UTC()}, nil
		}
	}
	return &GRPCServer{service: service, registry: registry, snapshot: snapshot}, nil
}

func RegisterGatewayControlServer(registrar grpc.ServiceRegistrar, server *GRPCServer) {
	registrar.RegisterService(&gatewayServiceDesc, server)
}

func (s *GRPCServer) RegisterGateway(ctx context.Context, request *gatewayv1.RegisterGatewayRequest) (*gatewayv1.RegisterGatewayResponse, error) {
	instanceID, err := uuid.Parse(request.GetGatewayInstanceId())
	if err != nil || request.GetMinimumProtocolRevision() > ProtocolRevision || request.GetMaximumProtocolRevision() < ProtocolRevision {
		return nil, status.Error(codes.InvalidArgument, "gateway registration is invalid")
	}
	fingerprint := certificateFingerprint(ctx)
	if len(fingerprint) < 32 {
		return nil, status.Error(codes.Unauthenticated, "gateway certificate is required")
	}
	gateway, session, err := s.service.ConsumeEnrollment(ctx, request.GetEnrollmentToken(), request.GetRequestedGatewayName(), fingerprint, instanceID, ProtocolRevision)
	if err != nil {
		return nil, gatewayGRPCError(err)
	}
	return &gatewayv1.RegisterGatewayResponse{
		GatewayId:                gateway.ID.String(),
		SessionToken:             session.Token,
		SessionExpiresAt:         session.ExpiresAt.UTC().Format(time.RFC3339Nano),
		SelectedProtocolRevision: session.ProtocolRevision,
		HeartbeatIntervalSeconds: 10,
	}, nil
}

func (s *GRPCServer) ControlStream(stream GatewayControlStreamServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "gateway hello is required")
	}
	gatewayID, err := uuid.Parse(hello.GetGatewayId())
	if err != nil {
		return status.Error(codes.Unauthenticated, "gateway session is invalid")
	}
	instanceID, err := uuid.Parse(hello.GetGatewayInstanceId())
	if err != nil {
		return status.Error(codes.Unauthenticated, "gateway session is invalid")
	}
	gateway, err := s.service.AuthenticateSession(stream.Context(), bearerToken(stream.Context()), gatewayID, instanceID, hello.GetProtocolRevision())
	if err != nil {
		return gatewayGRPCError(err)
	}
	if fingerprint := certificateFingerprint(stream.Context()); fingerprint == "" || !strings.EqualFold(fingerprint, gateway.CertificateFingerprint) {
		return status.Error(codes.Unauthenticated, "gateway certificate is invalid")
	}
	registration := newGatewayStream(stream)
	if _, err := s.registry.Replace(gatewayID, registration); err != nil {
		return status.Error(codes.Unavailable, "gateway stream is unavailable")
	}
	defer s.registry.Remove(gatewayID, registration)
	snapshot, err := s.snapshot(stream.Context(), gatewayID)
	if err != nil {
		return status.Error(codes.Unavailable, "gateway snapshot is unavailable")
	}
	if err := stream.Send(snapshotMessage(snapshot)); err != nil {
		return err
	}

	sendError := make(chan error, 1)
	go func() {
		for {
			select {
			case message := <-registration.messages:
				if err := stream.Send(message); err != nil {
					sendError <- err
					return
				}
			case <-registration.done:
				sendError <- nil
				return
			case <-stream.Context().Done():
				sendError <- stream.Context().Err()
				return
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				refresh, refreshErr := s.snapshot(stream.Context(), gatewayID)
				if refreshErr == nil {
					_ = registration.Send(snapshotMessage(refresh))
				}
			case <-registration.done:
				return
			case <-stream.Context().Done():
				return
			}
		}
	}()
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			select {
			case sendErr := <-sendError:
				return sendErr
			default:
				return err
			}
		}
		if message.GetHello() != nil {
			return status.Error(codes.InvalidArgument, "gateway hello must be first")
		}
		if heartbeat := message.GetHeartbeat(); heartbeat != nil {
			observedAt, parseErr := time.Parse(time.RFC3339Nano, heartbeat.GetObservedAt())
			if parseErr != nil || s.service.RecordHeartbeat(stream.Context(), gatewayID, observedAt) != nil {
				return status.Error(codes.InvalidArgument, "gateway heartbeat is invalid")
			}
		}
		if ack := message.GetSnapshotAck(); ack != nil && ack.GetFailureCode() == "snapshot_required" {
			recovery, snapshotErr := s.snapshot(stream.Context(), gatewayID)
			if snapshotErr != nil || s.registry.Send(gatewayID, snapshotMessage(recovery)) != nil {
				return status.Error(codes.Unavailable, "gateway snapshot recovery is unavailable")
			}
		}
	}
}

type gatewayStream struct {
	messages chan *gatewayv1.ControlMessage
	done     chan struct{}
	once     sync.Once
}

func newGatewayStream(GatewayControlStreamServer) *gatewayStream {
	return &gatewayStream{messages: make(chan *gatewayv1.ControlMessage, 64), done: make(chan struct{})}
}

func (s *gatewayStream) Send(message any) error {
	typed, ok := message.(*gatewayv1.ControlMessage)
	if !ok || typed == nil {
		return ErrInvalidArgument
	}
	select {
	case s.messages <- typed:
		return nil
	case <-s.done:
		return ErrSessionExpired
	default:
		return ErrSessionExpired
	}
}

func (s *gatewayStream) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func snapshotMessage(snapshot Snapshot) *gatewayv1.ControlMessage {
	routes := make([]*gatewayv1.Route, 0, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		credentials := make([]*gatewayv1.ProxyCredentialVerifier, 0, len(route.Credentials))
		for _, credential := range route.Credentials {
			expires := ""
			if credential.ExpiresAt != nil {
				expires = credential.ExpiresAt.UTC().Format(time.RFC3339Nano)
			}
			credentials = append(credentials, &gatewayv1.ProxyCredentialVerifier{CredentialId: credential.ID.String(), PublicIdentifier: credential.PublicIdentifier, Verifier: credential.Verifier, ExpiresAt: expires, Revoked: credential.Revoked})
		}
		routes = append(routes, &gatewayv1.Route{
			TenantId: route.TenantID.String(), EndpointId: route.EndpointID.String(), PolicyHash: route.PolicyHash, Protocols: route.Protocols, Credentials: credentials,
			Grant:        &gatewayv1.RouteGrant{GatewayId: route.Grant.GatewayID.String(), TenantId: route.Grant.TenantID.String(), EndpointId: route.Grant.EndpointID.String(), RunnerId: route.Grant.RunnerID.String(), Generation: route.Grant.Generation, Protocols: route.Grant.Protocols, PolicyHash: route.Grant.PolicyHash, ExpiresAt: route.Grant.ExpiresAt.UTC().Format(time.RFC3339Nano), Signature: route.Grant.Signature},
			AssignmentId: route.AssignmentID.String(), AssignmentGeneration: route.AssignmentGeneration, AccountId: route.AccountID.String(), NodeId: route.NodeID.String(), AssignmentState: route.AssignmentState, ValidUntil: route.ValidUntil.UTC().Format(time.RFC3339Nano),
		})
	}
	return &gatewayv1.ControlMessage{Body: &gatewayv1.ControlMessage_RouteSnapshot{RouteSnapshot: &gatewayv1.RouteSnapshot{SnapshotVersion: snapshot.Version, GeneratedAt: snapshot.GeneratedAt.UTC().Format(time.RFC3339Nano), Routes: routes}}}
}

func bearerToken(ctx context.Context) string {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 {
		return ""
	}
	value := strings.TrimSpace(values[0])
	if len(value) < 8 || !strings.EqualFold(value[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(value[7:])
}

func certificateFingerprint(ctx context.Context) string {
	if remote, ok := peer.FromContext(ctx); ok {
		if tlsInfo, ok := remote.AuthInfo.(credentials.TLSInfo); ok {
			if len(tlsInfo.State.PeerCertificates) == 0 {
				return ""
			}
			digest := sha256.Sum256(tlsInfo.State.PeerCertificates[0].Raw)
			return hex.EncodeToString(digest[:])
		}
	}
	values := metadata.ValueFromIncomingContext(ctx, "x-ajiasu-certificate-fingerprint")
	if len(values) != 1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(values[0]))
}

func gatewayGRPCError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, "gateway request is invalid")
	case errors.Is(err, ErrEnrollmentExpired), errors.Is(err, ErrEnrollmentConsumed), errors.Is(err, ErrEnrollmentRevoked), errors.Is(err, ErrCertificateMismatch), errors.Is(err, ErrSessionExpired), errors.Is(err, ErrSessionRevoked):
		return status.Error(codes.Unauthenticated, "gateway session is invalid")
	default:
		return status.Error(codes.Unavailable, "gateway dependency is unavailable")
	}
}

type GatewayControlStreamServer interface {
	Send(*gatewayv1.ControlMessage) error
	Recv() (*gatewayv1.GatewayMessage, error)
	grpc.ServerStream
}

type gatewayControlStreamServer struct{ grpc.ServerStream }

func (s *gatewayControlStreamServer) Send(message *gatewayv1.ControlMessage) error {
	return s.ServerStream.SendMsg(message)
}

func (s *gatewayControlStreamServer) Recv() (*gatewayv1.GatewayMessage, error) {
	message := new(gatewayv1.GatewayMessage)
	if err := s.ServerStream.RecvMsg(message); err != nil {
		return nil, err
	}
	return message, nil
}

type gatewayControlServiceServer interface {
	RegisterGateway(context.Context, *gatewayv1.RegisterGatewayRequest) (*gatewayv1.RegisterGatewayResponse, error)
	ControlStream(GatewayControlStreamServer) error
}

func registerGatewayHandler(server any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	request := new(gatewayv1.RegisterGatewayRequest)
	if err := decode(request); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return server.(*GRPCServer).RegisterGateway(ctx, request)
	}
	info := &grpc.UnaryServerInfo{Server: server, FullMethod: "/" + gatewayServiceName + "/RegisterGateway"}
	handler := func(ctx context.Context, request any) (any, error) {
		return server.(*GRPCServer).RegisterGateway(ctx, request.(*gatewayv1.RegisterGatewayRequest))
	}
	return interceptor(ctx, request, info, handler)
}

func gatewayControlStreamHandler(server any, stream grpc.ServerStream) error {
	return server.(*GRPCServer).ControlStream(&gatewayControlStreamServer{ServerStream: stream})
}

var gatewayServiceDesc = grpc.ServiceDesc{
	ServiceName: gatewayServiceName,
	HandlerType: (*gatewayControlServiceServer)(nil),
	Methods:     []grpc.MethodDesc{{MethodName: "RegisterGateway", Handler: registerGatewayHandler}},
	Streams:     []grpc.StreamDesc{{StreamName: "ControlStream", Handler: gatewayControlStreamHandler, ServerStreams: true, ClientStreams: true}},
	Metadata:    "gateway/v1/gateway.proto",
}
