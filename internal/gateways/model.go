package gateways

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const ProtocolRevision uint32 = 1

var (
	ErrInvalidArgument     = errors.New("invalid gateway argument")
	ErrEnrollmentExpired   = errors.New("gateway enrollment expired")
	ErrEnrollmentConsumed  = errors.New("gateway enrollment already consumed")
	ErrEnrollmentRevoked   = errors.New("gateway enrollment revoked")
	ErrCertificateMismatch = errors.New("gateway certificate does not match enrollment")
	ErrSessionExpired      = errors.New("gateway session expired")
	ErrSessionRevoked      = errors.New("gateway session revoked")
	ErrStaleVersion        = errors.New("gateway snapshot version is stale")
	ErrInvalidGrant        = errors.New("invalid route grant")
)

type Enrollment struct {
	ID                     uuid.UUID
	ExpectedGatewayName    string
	TokenPrefix            string
	TokenVerifier          string
	CertificateFingerprint string
	CreatedBy              uuid.UUID
	ExpiresAt              time.Time
	ConsumedAt             *time.Time
	ConsumedGatewayID      *uuid.UUID
	RevokedAt              *time.Time
	CreatedAt              time.Time
}
type Gateway struct {
	ID                     uuid.UUID
	Name                   string
	CertificateFingerprint string
	State                  string
	ConnectivityState      string
	SessionGeneration      int64
	Version                int64
	CreatedAt              time.Time
	UpdatedAt              time.Time
}
type Session struct {
	ID                uuid.UUID
	GatewayID         uuid.UUID
	GatewayInstanceID uuid.UUID
	TokenPrefix       string
	TokenVerifier     string
	ProtocolRevision  uint32
	SessionGeneration int64
	ExpiresAt         time.Time
	RevokedAt         *time.Time
	CreatedAt         time.Time
	LastUsedAt        *time.Time
	Token             string `json:"-"`
}

type RouteGrant struct {
	GatewayID  uuid.UUID `json:"gateway_id"`
	TenantID   uuid.UUID `json:"tenant_id"`
	EndpointID uuid.UUID `json:"endpoint_id"`
	RunnerID   uuid.UUID `json:"runner_id"`
	Generation uint64    `json:"generation"`
	Protocols  []string  `json:"protocols"`
	PolicyHash string    `json:"policy_hash"`
	ExpiresAt  time.Time `json:"expires_at"`
	Signature  []byte    `json:"signature"`
}

func (g RouteGrant) signingBytes() []byte {
	protocols := append([]string(nil), g.Protocols...)
	sort.Strings(protocols)
	var data strings.Builder
	fmt.Fprintf(&data, "gateway=%s\ntenant=%s\nendpoint=%s\nrunner=%s\ngeneration=%d\nprotocols=%s\npolicy_hash=%s\nexpires_at=%s\n", g.GatewayID, g.TenantID, g.EndpointID, g.RunnerID, g.Generation, strings.Join(protocols, ","), g.PolicyHash, g.ExpiresAt.UTC().Format(time.RFC3339Nano))
	return []byte(data.String())
}

func (g RouteGrant) Sign(privateKey ed25519.PrivateKey) RouteGrant {
	g.Signature = ed25519.Sign(privateKey, g.signingBytes())
	return g
}
func (g RouteGrant) Verify(publicKey ed25519.PublicKey, audience uuid.UUID, now time.Time) error {
	if g.GatewayID != audience || g.GatewayID == uuid.Nil || g.TenantID == uuid.Nil || g.EndpointID == uuid.Nil || g.RunnerID == uuid.Nil || g.Generation == 0 || len(g.Protocols) == 0 || g.PolicyHash == "" || !g.ExpiresAt.After(now.UTC()) || !ed25519.Verify(publicKey, g.signingBytes(), g.Signature) {
		return ErrInvalidGrant
	}
	seen := map[string]bool{}
	for _, protocol := range g.Protocols {
		if (protocol != "http" && protocol != "connect" && protocol != "socks5") || seen[protocol] {
			return ErrInvalidGrant
		}
		seen[protocol] = true
	}
	return nil
}

type Route struct {
	TenantID    uuid.UUID
	EndpointID  uuid.UUID
	PolicyHash  string
	Protocols   []string
	Credentials []CredentialVerifier
	Grant       RouteGrant
}
type CredentialVerifier struct {
	ID               uuid.UUID
	PublicIdentifier string
	Verifier         string
	ExpiresAt        *time.Time
	Revoked          bool
}
type Snapshot struct {
	Version     uint64
	GeneratedAt time.Time
	Routes      []Route
}
type Delta struct {
	Version uint64
	Route   Route
	Revoked bool
}

type Stream interface {
	Send(any) error
	Close() error
}
type StreamRegistry struct {
	mu          sync.Mutex
	streams     map[uuid.UUID]Stream
	generations map[uuid.UUID]uint64
}

func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{streams: map[uuid.UUID]Stream{}, generations: map[uuid.UUID]uint64{}}
}
func (r *StreamRegistry) Replace(gatewayID uuid.UUID, stream Stream) (uint64, error) {
	if gatewayID == uuid.Nil || stream == nil {
		return 0, ErrInvalidArgument
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old := r.streams[gatewayID]; old != nil {
		_ = old.Close()
	}
	generation := r.generations[gatewayID] + 1
	r.generations[gatewayID] = generation
	r.streams[gatewayID] = stream
	return generation, nil
}
func (r *StreamRegistry) Remove(gatewayID uuid.UUID, stream Stream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.streams[gatewayID]; current == stream {
		delete(r.streams, gatewayID)
	}
}
func (r *StreamRegistry) Send(gatewayID uuid.UUID, message any) error {
	r.mu.Lock()
	stream := r.streams[gatewayID]
	r.mu.Unlock()
	if stream == nil {
		return ErrSessionExpired
	}
	return stream.Send(message)
}

func EncodeGeneration(value uint64) []byte {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	return data[:]
}
