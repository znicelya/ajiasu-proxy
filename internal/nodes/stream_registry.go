package nodes

import (
	"context"
	"errors"
	"sync"

	agentv1 "github.com/dnomd343/ajiasu-proxy/internal/gen/agent/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

var ErrStreamUnavailable = errors.New("agent stream is unavailable")

type streamRegistration struct {
	nodeID uuid.UUID
	queue  chan *agentv1.ControlMessage
	done   chan struct{}
}

// StreamRegistry owns the volatile delivery edge; commands remain durable in PostgreSQL.
type StreamRegistry struct {
	mu      sync.RWMutex
	streams map[uuid.UUID]*streamRegistration
}

func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{streams: make(map[uuid.UUID]*streamRegistration)}
}

func (r *StreamRegistry) Attach(nodeID uuid.UUID) (*streamRegistration, bool) {
	registration := &streamRegistration{nodeID: nodeID, queue: make(chan *agentv1.ControlMessage, 64), done: make(chan struct{})}
	r.mu.Lock()
	previous, replaced := r.streams[nodeID]
	r.streams[nodeID] = registration
	r.mu.Unlock()
	if previous != nil {
		close(previous.done)
	}
	return registration, replaced
}

func (r *StreamRegistry) Detach(nodeID uuid.UUID, registration *streamRegistration) {
	r.mu.Lock()
	if current := r.streams[nodeID]; current == registration {
		delete(r.streams, nodeID)
		close(registration.done)
	}
	r.mu.Unlock()
}

func (r *StreamRegistry) Deliver(ctx context.Context, nodeID uuid.UUID, message *agentv1.ControlMessage) error {
	if message == nil {
		return ErrStreamUnavailable
	}
	r.mu.RLock()
	registration := r.streams[nodeID]
	r.mu.RUnlock()
	if registration == nil {
		return ErrStreamUnavailable
	}
	cloned, ok := proto.Clone(message).(*agentv1.ControlMessage)
	if !ok {
		return ErrStreamUnavailable
	}
	select {
	case registration.queue <- cloned:
		return nil
	case <-registration.done:
		return ErrStreamUnavailable
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *StreamRegistry) Has(nodeID uuid.UUID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.streams[nodeID] != nil
}
