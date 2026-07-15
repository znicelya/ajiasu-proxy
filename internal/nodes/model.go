package nodes

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrForbidden       = errors.New("node operation is forbidden")
	ErrInvalidArgument = errors.New("invalid node argument")
	ErrNotFound        = errors.New("node was not found")
	ErrAlreadyExists   = errors.New("node already exists")
	ErrTokenInvalid    = errors.New("node token is invalid")
	ErrTokenExpired    = errors.New("node token is expired")
	ErrNodeDisabled    = errors.New("node is disabled")
	ErrVersionConflict = errors.New("node version conflict")
	ErrStorage         = errors.New("node storage failure")
)

type MaintenanceState string

const (
	MaintenanceActive   MaintenanceState = "active"
	MaintenanceCordoned MaintenanceState = "cordoned"
	MaintenanceDraining MaintenanceState = "draining"
	MaintenanceDisabled MaintenanceState = "disabled"
)

type ConnectivityState string

const (
	ConnectivityRegistering ConnectivityState = "registering"
	ConnectivityOnline      ConnectivityState = "online"
	ConnectivityStale       ConnectivityState = "stale"
	ConnectivityOffline     ConnectivityState = "offline"
)

type Node struct {
	ID                uuid.UUID         `json:"id"`
	Name              string            `json:"name"`
	DesiredLabels     map[string]string `json:"desired_labels"`
	ObservedLabels    map[string]string `json:"observed_labels"`
	MaxRunners        int               `json:"max_runners"`
	ReservedHeadroom  int               `json:"reserved_headroom"`
	ActiveRunners     int               `json:"active_runners"`
	MaintenanceState  MaintenanceState  `json:"maintenance_state"`
	ConnectivityState ConnectivityState `json:"connectivity_state"`
	Architecture      string            `json:"architecture"`
	AgentVersion      string            `json:"agent_version"`
	LastHeartbeatAt   *time.Time        `json:"last_heartbeat_at,omitempty"`
	SessionGeneration int64             `json:"session_generation"`
	Version           int64             `json:"version"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}
type EligibleNode struct {
	ID                uuid.UUID         `json:"id"`
	Name              string            `json:"name"`
	Labels            map[string]string `json:"labels"`
	MaintenanceState  MaintenanceState  `json:"maintenance_state"`
	ConnectivityState ConnectivityState `json:"connectivity_state"`
	AvailableRunners  int               `json:"available_runners"`
}

type Enrollment struct {
	ID               uuid.UUID `json:"id"`
	ExpectedNodeName string    `json:"expected_node_name"`
	TokenPrefix      string    `json:"token_prefix"`
	ExpiresAt        time.Time `json:"expires_at"`
	CreatedAt        time.Time `json:"created_at"`
}

type EnrollmentCreated struct {
	Enrollment Enrollment `json:"enrollment"`
	Token      string     `json:"token"`
}

type CreateEnrollment struct {
	ExpectedNodeName string
	ValidFor         time.Duration
}
type NodeRegistration struct {
	EnrollmentToken     string
	AgentInstanceID     uuid.UUID
	RequestedNodeName   string
	MinimumRevision     uint32
	MaximumRevision     uint32
	AgentVersion        string
	Architecture        string
	RuntimeCapabilities []string
}
type NodeSession struct {
	Node             Node
	SessionToken     string
	SessionExpiresAt time.Time
	ProtocolRevision uint32
}

func (c CreateEnrollment) Validate() error {
	if len(strings.TrimSpace(c.ExpectedNodeName)) == 0 || len(strings.TrimSpace(c.ExpectedNodeName)) > 200 || c.ValidFor <= 0 || c.ValidFor > time.Hour {
		return ErrInvalidArgument
	}
	return nil
}
func validMaintenance(s MaintenanceState) bool {
	return s == MaintenanceActive || s == MaintenanceCordoned || s == MaintenanceDraining || s == MaintenanceDisabled
}
