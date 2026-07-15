package gateways

import "sync"

// ControlServer is the transport-neutral control-plane boundary. The actual
// gRPC adapter is kept thin so tests can exercise enrollment and ordering
// without opening sockets.
type ControlServer struct {
	registry  *StreamRegistry
	mu        sync.Mutex
	snapshots map[string]*SnapshotApplier
}

func NewControlServer(registry *StreamRegistry) *ControlServer {
	if registry == nil {
		registry = NewStreamRegistry()
	}
	return &ControlServer{registry: registry, snapshots: map[string]*SnapshotApplier{}}
}
func (s *ControlServer) Applier(gatewayID string) *SnapshotApplier {
	s.mu.Lock()
	defer s.mu.Unlock()
	applier := s.snapshots[gatewayID]
	if applier == nil {
		applier = NewSnapshotApplier()
		s.snapshots[gatewayID] = applier
	}
	return applier
}
