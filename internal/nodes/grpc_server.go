package nodes

import (
	"context"
	"errors"
	agentv1 "github.com/dnomd343/ajiasu-proxy/internal/gen/agent/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"io"
	"strings"
	"time"
)

const agentServiceName = "ajiasu.agent.v1.AgentControl"

type GRPCServer struct {
	service        *Service
	registry       *StreamRegistry
	onAgentMessage func(context.Context, uuid.UUID, *agentv1.AgentMessage) error
}
type agentControlServiceServer interface {
	RegisterNode(context.Context, *agentv1.RegisterNodeRequest) (*agentv1.RegisterNodeResponse, error)
	ControlStream(AgentControlStreamServer) error
}

func NewGRPCServer(service *Service, registries ...*StreamRegistry) (*GRPCServer, error) {
	if service == nil {
		return nil, ErrInvalidArgument
	}
	registry := NewStreamRegistry()
	if len(registries) > 0 && registries[0] != nil {
		registry = registries[0]
	}
	return &GRPCServer{service: service, registry: registry}, nil
}

func (s *GRPCServer) Registry() *StreamRegistry { return s.registry }
func (s *GRPCServer) SetAgentMessageHandler(handler func(context.Context, uuid.UUID, *agentv1.AgentMessage) error) {
	s.onAgentMessage = handler
}
func RegisterAgentControlServer(registrar grpc.ServiceRegistrar, server *GRPCServer) {
	registrar.RegisterService(&agentServiceDesc, server)
}
func (s *GRPCServer) RegisterNode(ctx context.Context, req *agentv1.RegisterNodeRequest) (*agentv1.RegisterNodeResponse, error) {
	instance, err := uuid.Parse(req.GetAgentInstanceId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "registration is invalid")
	}
	result, err := s.service.Register(ctx, NodeRegistration{EnrollmentToken: req.GetEnrollmentToken(), AgentInstanceID: instance, RequestedNodeName: req.GetRequestedNodeName(), MinimumRevision: req.GetMinimumProtocolRevision(), MaximumRevision: req.GetMaximumProtocolRevision(), AgentVersion: req.GetAgentVersion(), Architecture: req.GetArchitecture(), RuntimeCapabilities: req.GetRuntimeCapabilities()})
	if err != nil {
		return nil, grpcError(err)
	}
	return &agentv1.RegisterNodeResponse{NodeId: result.Node.ID.String(), SessionToken: result.SessionToken, SessionExpiresAt: result.SessionExpiresAt.UTC().Format(time.RFC3339Nano), SelectedProtocolRevision: result.ProtocolRevision, HeartbeatIntervalSeconds: 10}, nil
}
func (s *GRPCServer) ControlStream(stream AgentControlStreamServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "hello is required")
	}
	nodeID, err := uuid.Parse(hello.GetNodeId())
	if err != nil {
		return status.Error(codes.Unauthenticated, "agent session is invalid")
	}
	instanceID, err := uuid.Parse(hello.GetAgentInstanceId())
	if err != nil {
		return status.Error(codes.Unauthenticated, "agent session is invalid")
	}
	token := bearerToken(stream.Context())
	if token == "" {
		return status.Error(codes.Unauthenticated, "agent session is required")
	}
	if _, err = s.service.AuthenticateSession(stream.Context(), token, nodeID, instanceID, hello.GetProtocolRevision()); err != nil {
		return grpcError(err)
	}
	registration, _ := s.registry.Attach(nodeID)
	defer s.registry.Detach(nodeID, registration)
	if err := s.registry.Deliver(stream.Context(), nodeID, &agentv1.ControlMessage{Body: &agentv1.ControlMessage_DesiredInventoryRequest{DesiredInventoryRequest: &agentv1.DesiredInventoryRequest{RequestId: uuid.NewString()}}}); err != nil {
		return status.Error(codes.Unavailable, "agent stream is unavailable")
	}
	serverErr := make(chan error, 1)
	go func() {
		for {
			select {
			case message := <-registration.queue:
				if message == nil {
					continue
				}
				if err := stream.Send(message); err != nil {
					clearControlMessage(message)
					serverErr <- err
					return
				}
				clearControlMessage(message)
			case <-registration.done:
				serverErr <- nil
				return
			case <-stream.Context().Done():
				serverErr <- stream.Context().Err()
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
			case sendErr := <-serverErr:
				if sendErr != nil {
					return sendErr
				}
			default:
			}
			return err
		}
		if message.GetHello() != nil {
			return status.Error(codes.InvalidArgument, "hello must be the first message")
		}
		if s.onAgentMessage != nil {
			if err := s.onAgentMessage(stream.Context(), nodeID, message); err != nil {
				return grpcError(err)
			}
		}
		heartbeat := message.GetHeartbeat()
		if heartbeat == nil {
			continue
		}
		observedAt, parseErr := time.Parse(time.RFC3339Nano, heartbeat.GetObservedAt())
		if parseErr != nil {
			return status.Error(codes.InvalidArgument, "heartbeat is invalid")
		}
		if err = s.service.RecordHeartbeat(stream.Context(), nodeID, heartbeat.GetObservedLabels(), int(heartbeat.GetMaximumRunners()), int(heartbeat.GetActiveRunners()), int(heartbeat.GetReservedHeadroom()), observedAt); err != nil {
			return grpcError(err)
		}
	}
}
func clearControlMessage(message *agentv1.ControlMessage) {
	if command := message.GetRunnerCommand(); command != nil {
		clear(command.CredentialConfiguration)
		command.CredentialConfiguration = nil
	}
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
func grpcError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, "request is invalid")
	case errors.Is(err, ErrTokenInvalid), errors.Is(err, ErrTokenExpired), errors.Is(err, ErrNodeDisabled):
		return status.Error(codes.Unauthenticated, "agent session is invalid")
	case errors.Is(err, ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, "node already exists")
	default:
		return status.Error(codes.Unavailable, "dependency unavailable")
	}
}

type AgentControlStreamServer interface {
	Send(*agentv1.ControlMessage) error
	Recv() (*agentv1.AgentMessage, error)
	grpc.ServerStream
}
type agentControlStreamServer struct{ grpc.ServerStream }

func (s *agentControlStreamServer) Send(message *agentv1.ControlMessage) error {
	return s.ServerStream.SendMsg(message)
}
func (s *agentControlStreamServer) Recv() (*agentv1.AgentMessage, error) {
	message := new(agentv1.AgentMessage)
	if err := s.ServerStream.RecvMsg(message); err != nil {
		return nil, err
	}
	return message, nil
}
func registerNodeHandler(srv any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	request := new(agentv1.RegisterNodeRequest)
	if err := decode(request); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(*GRPCServer).RegisterNode(ctx, request)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/" + agentServiceName + "/RegisterNode"}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(*GRPCServer).RegisterNode(ctx, req.(*agentv1.RegisterNodeRequest))
	}
	return interceptor(ctx, request, info, handler)
}
func controlStreamHandler(srv any, stream grpc.ServerStream) error {
	return srv.(*GRPCServer).ControlStream(&agentControlStreamServer{ServerStream: stream})
}

var agentServiceDesc = grpc.ServiceDesc{ServiceName: agentServiceName, HandlerType: (*agentControlServiceServer)(nil), Methods: []grpc.MethodDesc{{MethodName: "RegisterNode", Handler: registerNodeHandler}}, Streams: []grpc.StreamDesc{{StreamName: "ControlStream", Handler: controlStreamHandler, ServerStreams: true, ClientStreams: true}}, Metadata: "agent/v1/agent.proto"}
