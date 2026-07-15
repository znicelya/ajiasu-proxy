package nodes

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/identity"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	pools *database.Pools
	audit audit.Service
	now   func() time.Time
	newID func() (uuid.UUID, error)
}

func NewService(pools *database.Pools, auditService audit.Service) (*Service, error) {
	if pools == nil || pools.Platform == nil {
		return nil, ErrInvalidArgument
	}
	if auditService == nil {
		auditService = audit.NewService()
	}
	return &Service{pools: pools, audit: auditService, now: func() time.Time { return time.Now().UTC() }, newID: uuid.NewV7}, nil
}

func (s *Service) CreateEnrollment(ctx context.Context, actor tenancy.PlatformActor, cmd CreateEnrollment) (EnrollmentCreated, error) {
	if !actorAllows(actor, tenancy.ActionManageNodes) || cmd.Validate() != nil {
		return EnrollmentCreated{}, choose(actor, tenancy.ActionManageNodes)
	}
	id, err := s.newID()
	if err != nil {
		return EnrollmentCreated{}, ErrStorage
	}
	token, prefix, secret, err := newNodeToken()
	if err != nil {
		return EnrollmentCreated{}, ErrStorage
	}
	verifier, err := identity.HashPassword(secret)
	clear(secret)
	if err != nil {
		return EnrollmentCreated{}, ErrStorage
	}
	now := s.now().UTC()
	expires := now.Add(cmd.ValidFor)
	var result EnrollmentCreated
	_, err = database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		if _, err := tx.Exec(ctx, `INSERT INTO nodes.node_enrollments (id,expected_node_name,token_prefix,token_verifier,created_by,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, strings.TrimSpace(cmd.ExpectedNodeName), prefix, verifier, actor.ActorID(), expires, now); err != nil {
			return struct{}{}, mapError(err)
		}
		metadata := actor.Metadata()
		tenantID := (*uuid.UUID)(nil)
		actorID := actor.ActorID()
		details := map[string]any{"enrollment_id": id.String(), "expires_at": expires.UTC().Format(time.RFC3339)}
		if err := s.audit.Append(ctx, tx, audit.Event{TenantID: tenantID, ActorType: metadata.ActorType, ActorID: &actorID, Action: "nodes.enrollment.created", ResourceType: "node_enrollment", ResourceID: &id, Result: "success", SourceIP: metadata.SourceIP, UserAgent: metadata.UserAgent, RequestID: metadata.RequestID, Details: details}, audit.OutboxEvent{EventType: "nodes.enrollment.created", AggregateType: "node_enrollment", AggregateID: id, PayloadVersion: 1, Payload: details, AvailableAt: now}); err != nil {
			return struct{}{}, err
		}
		result = EnrollmentCreated{Enrollment: Enrollment{ID: id, ExpectedNodeName: strings.TrimSpace(cmd.ExpectedNodeName), TokenPrefix: prefix, ExpiresAt: expires, CreatedAt: now}, Token: token}
		return struct{}{}, nil
	})
	if err != nil {
		return EnrollmentCreated{}, err
	}
	return result, nil
}

func (s *Service) RevokeEnrollment(ctx context.Context, actor tenancy.PlatformActor, enrollmentID uuid.UUID) error {
	if !actorAllows(actor, tenancy.ActionManageNodes) {
		return ErrForbidden
	}
	if enrollmentID == uuid.Nil {
		return ErrInvalidArgument
	}
	_, err := database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		now := s.now().UTC()
		tag, err := tx.Exec(ctx, `UPDATE nodes.node_enrollments SET revoked_at=$1 WHERE id=$2 AND consumed_at IS NULL AND revoked_at IS NULL`, now, enrollmentID)
		if err != nil {
			return struct{}{}, mapError(err)
		}
		if tag.RowsAffected() == 0 {
			return struct{}{}, ErrNotFound
		}
		metadata := actor.Metadata()
		actorID := actor.ActorID()
		details := map[string]any{"enrollment_id": enrollmentID.String()}
		if err := s.audit.Append(ctx, tx, audit.Event{ActorType: metadata.ActorType, ActorID: &actorID, Action: "nodes.enrollment.revoked", ResourceType: "node_enrollment", ResourceID: &enrollmentID, Result: "success", SourceIP: metadata.SourceIP, UserAgent: metadata.UserAgent, RequestID: metadata.RequestID, Details: details}, audit.OutboxEvent{EventType: "nodes.enrollment.revoked", AggregateType: "node_enrollment", AggregateID: enrollmentID, PayloadVersion: 1, Payload: details, AvailableAt: now}); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return err
}

func (s *Service) Register(ctx context.Context, registration NodeRegistration) (NodeSession, error) {
	prefix, secret, err := parseNodeToken(registration.EnrollmentToken)
	if err != nil {
		return NodeSession{}, ErrTokenInvalid
	}
	defer clear(secret)
	if registration.AgentInstanceID == uuid.Nil || len(strings.TrimSpace(registration.RequestedNodeName)) == 0 || registration.MinimumRevision > registration.MaximumRevision {
		return NodeSession{}, ErrInvalidArgument
	}
	selected := negotiate(registration.MinimumRevision, registration.MaximumRevision)
	if selected == 0 {
		return NodeSession{}, ErrInvalidArgument
	}
	nodeID, err := s.newID()
	if err != nil {
		return NodeSession{}, ErrStorage
	}
	sessionID, err := s.newID()
	if err != nil {
		return NodeSession{}, ErrStorage
	}
	sessionToken, sessionPrefix, sessionSecret, err := newNodeToken()
	if err != nil {
		return NodeSession{}, ErrStorage
	}
	sessionVerifier, err := identity.HashPassword(sessionSecret)
	clear(sessionSecret)
	if err != nil {
		return NodeSession{}, ErrStorage
	}
	now := s.now().UTC()
	expires := now.Add(24 * time.Hour)
	var result NodeSession
	_, err = database.InPlatformTx(ctx, s.pools.Platform, uuid.New(), func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		var enrollmentID uuid.UUID
		var expected string
		var verifier string
		var enrollmentExpires time.Time
		if err := tx.QueryRow(ctx, `SELECT id,expected_node_name,token_verifier,expires_at FROM nodes.node_enrollments WHERE token_prefix=$1 AND consumed_at IS NULL AND revoked_at IS NULL FOR UPDATE`, prefix).Scan(&enrollmentID, &expected, &verifier, &enrollmentExpires); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return struct{}{}, ErrTokenInvalid
			}
			return struct{}{}, mapError(err)
		}
		if s.now().UTC().After(enrollmentExpires) {
			return struct{}{}, ErrTokenExpired
		}
		matched, verifyErr := identity.VerifyPassword(secret, verifier)
		if verifyErr != nil || !matched {
			return struct{}{}, ErrTokenInvalid
		}
		if strings.TrimSpace(registration.RequestedNodeName) != expected {
			return struct{}{}, ErrInvalidArgument
		}
		if registration.RuntimeCapabilities == nil {
			registration.RuntimeCapabilities = []string{}
		}
		capabilities, _ := json.Marshal(registration.RuntimeCapabilities)
		if _, err := tx.Exec(ctx, `INSERT INTO nodes.nodes (id,name,normalized_name,architecture,agent_version,runtime_capabilities,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`, nodeID, expected, strings.ToLower(expected), strings.TrimSpace(registration.Architecture), strings.TrimSpace(registration.AgentVersion), capabilities, now); err != nil {
			return struct{}{}, mapError(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes.node_enrollments SET consumed_at=$1,consumed_node_id=$2 WHERE id=$3`, now, nodeID, enrollmentID); err != nil {
			return struct{}{}, mapError(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO nodes.node_sessions (id,node_id,agent_instance_id,token_prefix,token_verifier,protocol_revision,session_generation,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,1,$7,$8)`, sessionID, nodeID, registration.AgentInstanceID, sessionPrefix, sessionVerifier, selected, expires, now); err != nil {
			return struct{}{}, mapError(err)
		}
		result = NodeSession{Node: Node{ID: nodeID, Name: expected, MaintenanceState: MaintenanceActive, ConnectivityState: ConnectivityRegistering, MaxRunners: 10, ReservedHeadroom: 1, SessionGeneration: 1, Version: 1, CreatedAt: now, UpdatedAt: now}, SessionToken: sessionToken, SessionExpiresAt: expires, ProtocolRevision: selected}
		return struct{}{}, nil
	})
	if err != nil {
		return NodeSession{}, err
	}
	return result, nil
}

func (s *Service) List(ctx context.Context, actor tenancy.PlatformActor, after time.Time, afterID uuid.UUID, limit int32) ([]Node, error) {
	if !actorAllows(actor, tenancy.ActionReadNodes) {
		return nil, ErrForbidden
	}
	if limit < 1 || limit > 200 {
		return nil, ErrInvalidArgument
	}
	return database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]Node, error) {
		rows, err := tx.Query(ctx, `SELECT id,name,desired_labels,observed_labels,max_runners,reserved_headroom,active_runners,maintenance_state,connectivity_state,architecture,agent_version,runtime_capabilities,last_heartbeat_at,session_generation,version,created_at,updated_at FROM nodes.nodes WHERE (created_at,id)>($1,$2) ORDER BY created_at,id LIMIT $3`, after, afterID, limit)
		if err != nil {
			return nil, mapError(err)
		}
		defer rows.Close()
		items := make([]Node, 0)
		for rows.Next() {
			item, err := scanNode(rows)
			if err != nil {
				return nil, mapError(err)
			}
			items = append(items, item)
		}
		return items, mapError(rows.Err())
	})
}

func (s *Service) Get(ctx context.Context, actor tenancy.PlatformActor, nodeID uuid.UUID) (Node, error) {
	if !actorAllows(actor, tenancy.ActionReadNodes) {
		return Node{}, ErrForbidden
	}
	if nodeID == uuid.Nil {
		return Node{}, ErrInvalidArgument
	}
	return database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Node, error) {
		item, err := scanNode(tx.QueryRow(ctx, `SELECT id,name,desired_labels,observed_labels,max_runners,reserved_headroom,active_runners,maintenance_state,connectivity_state,architecture,agent_version,runtime_capabilities,last_heartbeat_at,session_generation,version,created_at,updated_at FROM nodes.nodes WHERE id=$1`, nodeID))
		return item, mapError(err)
	})
}

func (s *Service) ListEligible(ctx context.Context, actor tenancy.TenantActor) ([]EligibleNode, error) {
	if !actor.Allows(tenancy.ActionReadResources) {
		return nil, ErrForbidden
	}
	return database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]EligibleNode, error) {
		rows, err := tx.Query(ctx, `SELECT id,name,desired_labels,maintenance_state,connectivity_state,GREATEST(max_runners-reserved_headroom-active_runners,0) FROM nodes.nodes WHERE maintenance_state='active' AND connectivity_state='online' ORDER BY name,id`)
		if err != nil {
			return nil, mapError(err)
		}
		defer rows.Close()
		items := make([]EligibleNode, 0)
		for rows.Next() {
			var item EligibleNode
			var labels []byte
			var maintenance, connectivity string
			if err := rows.Scan(&item.ID, &item.Name, &labels, &maintenance, &connectivity, &item.AvailableRunners); err != nil {
				return nil, mapError(err)
			}
			_ = json.Unmarshal(labels, &item.Labels)
			if item.Labels == nil {
				item.Labels = map[string]string{}
			}
			item.MaintenanceState, item.ConnectivityState = MaintenanceState(maintenance), ConnectivityState(connectivity)
			items = append(items, item)
		}
		return items, mapError(rows.Err())
	})
}

func (s *Service) SetMaintenance(ctx context.Context, actor tenancy.PlatformActor, nodeID uuid.UUID, expectedVersion int64, state MaintenanceState) (Node, error) {
	if !actorAllows(actor, tenancy.ActionManageNodes) {
		return Node{}, ErrForbidden
	}
	if nodeID == uuid.Nil || expectedVersion < 1 || !validMaintenance(state) {
		return Node{}, ErrInvalidArgument
	}
	return database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Node, error) {
		now := s.now().UTC()
		row := tx.QueryRow(ctx, `UPDATE nodes.nodes SET maintenance_state=$1,session_generation=session_generation+1,version=version+1,updated_at=$2 WHERE id=$3 AND version=$4 RETURNING id,name,desired_labels,observed_labels,max_runners,reserved_headroom,active_runners,maintenance_state,connectivity_state,architecture,agent_version,runtime_capabilities,last_heartbeat_at,session_generation,version,created_at,updated_at`, state, now, nodeID, expectedVersion)
		item, err := scanNode(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return Node{}, ErrVersionConflict
		}
		return item, mapError(err)
	})
}

func (s *Service) AuthenticateSession(ctx context.Context, token string, nodeID, agentInstanceID uuid.UUID, revision uint32) (Node, error) {
	prefix, secret, err := parseNodeToken(token)
	if err != nil {
		return Node{}, ErrTokenInvalid
	}
	defer clear(secret)
	if nodeID == uuid.Nil || agentInstanceID == uuid.Nil || revision < 1 || revision > 2 {
		return Node{}, ErrTokenInvalid
	}
	return database.InPlatformTx(ctx, s.pools.Platform, nodeID, func(ctx context.Context, tx pgx.Tx) (Node, error) {
		var sessionNode, instance uuid.UUID
		var verifier string
		var sessionRevision uint32
		var generation int64
		var expires time.Time
		if err := tx.QueryRow(ctx, `SELECT node_id,agent_instance_id,token_verifier,protocol_revision,session_generation,expires_at FROM nodes.node_sessions WHERE token_prefix=$1 AND revoked_at IS NULL FOR UPDATE`, prefix).Scan(&sessionNode, &instance, &verifier, &sessionRevision, &generation, &expires); err != nil {
			return Node{}, ErrTokenInvalid
		}
		matched, verifyErr := identity.VerifyPassword(secret, verifier)
		if verifyErr != nil || !matched || sessionNode != nodeID || instance != agentInstanceID || sessionRevision != revision || s.now().UTC().After(expires) {
			return Node{}, ErrTokenInvalid
		}
		node, err := scanNode(tx.QueryRow(ctx, `SELECT id,name,desired_labels,observed_labels,max_runners,reserved_headroom,active_runners,maintenance_state,connectivity_state,architecture,agent_version,runtime_capabilities,last_heartbeat_at,session_generation,version,created_at,updated_at FROM nodes.nodes WHERE id=$1 FOR UPDATE`, nodeID))
		if err != nil {
			return Node{}, mapError(err)
		}
		if node.SessionGeneration != generation || node.MaintenanceState == MaintenanceDisabled {
			return Node{}, ErrNodeDisabled
		}
		now := s.now().UTC()
		if _, err = tx.Exec(ctx, `UPDATE nodes.node_sessions SET last_used_at=$1 WHERE token_prefix=$2 AND node_id=$3`, now, prefix, nodeID); err != nil {
			return Node{}, mapError(err)
		}
		return node, nil
	})
}

func (s *Service) RecordHeartbeat(ctx context.Context, nodeID uuid.UUID, labels map[string]string, maxRunners, activeRunners, reservedHeadroom int, observedAt time.Time) error {
	if nodeID == uuid.Nil || maxRunners < 1 || maxRunners > 1000 || activeRunners < 0 || activeRunners > maxRunners || reservedHeadroom < 0 || reservedHeadroom >= maxRunners || observedAt.IsZero() {
		return ErrInvalidArgument
	}
	encoded, _ := json.Marshal(labels)
	_, err := database.InPlatformTx(ctx, s.pools.Platform, nodeID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		tag, err := tx.Exec(ctx, `UPDATE nodes.nodes SET observed_labels=$1,max_runners=$2,active_runners=$3,reserved_headroom=$4,connectivity_state='online',last_heartbeat_at=$5,updated_at=$6 WHERE id=$7 AND maintenance_state<>'disabled'`, encoded, maxRunners, activeRunners, reservedHeadroom, observedAt.UTC(), s.now().UTC(), nodeID)
		if err != nil {
			return struct{}{}, mapError(err)
		}
		if tag.RowsAffected() == 0 {
			return struct{}{}, ErrNodeDisabled
		}
		return struct{}{}, nil
	})
	return err
}

func actorAllows(actor tenancy.PlatformActor, action tenancy.Action) bool {
	return actor.Allows(action)
}
func choose(actor tenancy.PlatformActor, action tenancy.Action) error {
	if !actorAllows(actor, action) {
		return ErrForbidden
	}
	return ErrInvalidArgument
}
func newNodeToken() (string, string, []byte, error) {
	prefixBytes := make([]byte, 9)
	secret := make([]byte, 32)
	if _, err := rand.Read(prefixBytes); err != nil {
		return "", "", nil, err
	}
	if _, err := rand.Read(secret); err != nil {
		return "", "", nil, err
	}
	prefix := base64.RawURLEncoding.EncodeToString(prefixBytes)
	encoded := base64.RawURLEncoding.EncodeToString(secret)
	return "nse_" + prefix + "_" + encoded, prefix, secret, nil
}
func parseNodeToken(token string) (string, []byte, error) {
	if len(token) != 4+12+1+43 || !strings.HasPrefix(token, "nse_") || token[16] != '_' {
		return "", nil, ErrTokenInvalid
	}
	prefix := token[4:16]
	if _, err := base64.RawURLEncoding.DecodeString(prefix); err != nil {
		return "", nil, ErrTokenInvalid
	}
	secret, err := base64.RawURLEncoding.DecodeString(token[17:])
	if err != nil || len(secret) != 32 {
		return "", nil, ErrTokenInvalid
	}
	return prefix, secret, nil
}
func negotiate(min, max uint32) uint32 {
	if max >= 2 && min <= 2 {
		return 2
	}
	if max >= 1 && min <= 1 {
		return 1
	}
	return 0
}

type scanner interface{ Scan(...any) error }

func scanNode(row scanner) (Node, error) {
	var n Node
	var desired, observed, capabilities []byte
	var maintenance, connectivity string
	if err := row.Scan(&n.ID, &n.Name, &desired, &observed, &n.MaxRunners, &n.ReservedHeadroom, &n.ActiveRunners, &maintenance, &connectivity, &n.Architecture, &n.AgentVersion, &capabilities, &n.LastHeartbeatAt, &n.SessionGeneration, &n.Version, &n.CreatedAt, &n.UpdatedAt); err != nil {
		return Node{}, err
	}
	n.MaintenanceState = MaintenanceState(maintenance)
	n.ConnectivityState = ConnectivityState(connectivity)
	n.DesiredLabels = map[string]string{}
	n.ObservedLabels = map[string]string{}
	if err := json.Unmarshal(desired, &n.DesiredLabels); err != nil {
		return Node{}, err
	}
	if err := json.Unmarshal(observed, &n.ObservedLabels); err != nil {
		return Node{}, err
	}
	_ = capabilities
	return n, nil
}
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		switch pgErr.SQLState() {
		case "23505":
			return ErrAlreadyExists
		case "23503", "23514", "22P02":
			return ErrInvalidArgument
		}
	}
	return fmt.Errorf("%w: %w", ErrStorage, err)
}
