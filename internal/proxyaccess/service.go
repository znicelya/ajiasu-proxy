package proxyaccess

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"
)

var (
	ErrForbidden       = errors.New("proxy access operation is forbidden")
	ErrInvalidArgument = errors.New("invalid proxy access argument")
	ErrNotFound        = errors.New("proxy access resource was not found")
	ErrAlreadyExists   = errors.New("proxy access resource already exists")
	ErrVersionConflict = errors.New("proxy access version conflict")
	ErrPoolBinding     = errors.New("pool_binding_not_supported")
	ErrExpired         = errors.New("proxy credential expired")
	ErrRevoked         = errors.New("proxy credential revoked")
	ErrWrongPassword   = errors.New("proxy credential password is incorrect")
	ErrStorage         = errors.New("proxy access storage failure")
)

const (
	argonMemory  = 64 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
)

type Service struct {
	pools  *database.Pools
	audit  audit.Service
	now    func() time.Time
	random io.Reader
	newID  func() (uuid.UUID, error)
}

func NewService(pools *database.Pools, auditService audit.Service) (*Service, error) {
	if pools == nil || pools.Tenant == nil {
		return nil, ErrInvalidArgument
	}
	if auditService == nil {
		auditService = audit.NewService()
	}
	return &Service{pools: pools, audit: auditService, now: func() time.Time { return time.Now().UTC() }, random: rand.Reader, newID: uuid.NewV7}, nil
}

type ProxyCredential struct {
	ID               uuid.UUID  `json:"id"`
	TenantID         uuid.UUID  `json:"tenant_id"`
	EndpointID       uuid.UUID  `json:"endpoint_id"`
	PublicIdentifier string     `json:"public_identifier"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type IssuedCredential struct {
	ProxyCredential
	Password string `json:"password"`
}

type CreateCredentialCommand struct {
	EndpointID uuid.UUID
	ExpiresAt  *time.Time
}
type RotateCredentialCommand struct {
	CredentialID uuid.UUID
	ExpiresAt    *time.Time
}

type AccessProfile struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	EndpointID uuid.UUID `json:"endpoint_id"`
	Policy     Policy    `json:"policy"`
	PolicyHash string    `json:"policy_hash"`
	Version    int64     `json:"version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type UpsertAccessProfileCommand struct {
	EndpointID      uuid.UUID
	Policy          Policy
	ExpectedVersion *int64
}

func (s *Service) CreateCredential(ctx context.Context, actor tenancy.TenantActor, cmd CreateCredentialCommand) (IssuedCredential, error) {
	if !actor.Allows(tenancy.ActionManageResources) || cmd.EndpointID == uuid.Nil {
		return IssuedCredential{}, ErrForbidden
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (IssuedCredential, error) {
		if err := ensureFixedEndpoint(ctx, tx, actor.TenantID(), cmd.EndpointID); err != nil {
			return IssuedCredential{}, err
		}
		return s.issue(ctx, tx, actor, cmd.EndpointID, cmd.ExpiresAt, "proxy_credentials.issued")
	})
}

func (s *Service) RotateCredential(ctx context.Context, actor tenancy.TenantActor, cmd RotateCredentialCommand) (IssuedCredential, error) {
	if !actor.Allows(tenancy.ActionManageResources) || cmd.CredentialID == uuid.Nil {
		return IssuedCredential{}, ErrForbidden
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (IssuedCredential, error) {
		var endpointID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT endpoint_id FROM endpoints.proxy_credentials WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, actor.TenantID(), cmd.CredentialID).Scan(&endpointID); err != nil {
			return IssuedCredential{}, mapStorageError(err)
		}
		if err := ensureFixedEndpoint(ctx, tx, actor.TenantID(), endpointID); err != nil {
			return IssuedCredential{}, err
		}
		now := s.now().UTC()
		if _, err := tx.Exec(ctx, `UPDATE endpoints.proxy_credentials SET revoked_at=$3,updated_at=$3 WHERE tenant_id=$1 AND id=$2 AND revoked_at IS NULL`, actor.TenantID(), cmd.CredentialID, now); err != nil {
			return IssuedCredential{}, mapStorageError(err)
		}
		return s.issue(ctx, tx, actor, endpointID, cmd.ExpiresAt, "proxy_credentials.rotated")
	})
}

func (s *Service) RevokeCredential(ctx context.Context, actor tenancy.TenantActor, credentialID uuid.UUID) error {
	if !actor.Allows(tenancy.ActionManageResources) || credentialID == uuid.Nil {
		return ErrForbidden
	}
	_, err := database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		now := s.now().UTC()
		result, err := tx.Exec(ctx, `UPDATE endpoints.proxy_credentials SET revoked_at=$3,updated_at=$3 WHERE tenant_id=$1 AND id=$2 AND revoked_at IS NULL`, actor.TenantID(), credentialID, now)
		if err != nil {
			return struct{}{}, mapStorageError(err)
		}
		if result.RowsAffected() != 1 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, s.appendAudit(ctx, tx, actor, "proxy_credentials.revoked", credentialID, map[string]any{"credential_id": credentialID.String()})
	})
	return err
}

func (s *Service) Verify(ctx context.Context, tenantID uuid.UUID, publicIdentifier, password string, now time.Time) (ProxyCredential, error) {
	var credential ProxyCredential
	var verifier string
	err := s.pools.Tenant.QueryRow(ctx, `SELECT id,tenant_id,endpoint_id,public_identifier,verifier,expires_at,revoked_at,created_at,updated_at FROM endpoints.proxy_credentials WHERE tenant_id=$1 AND public_identifier=$2`, tenantID, strings.TrimSpace(publicIdentifier)).Scan(&credential.ID, &credential.TenantID, &credential.EndpointID, &credential.PublicIdentifier, &verifier, &credential.ExpiresAt, &credential.RevokedAt, &credential.CreatedAt, &credential.UpdatedAt)
	if err != nil {
		return ProxyCredential{}, ErrWrongPassword
	}
	if credential.RevokedAt != nil {
		return ProxyCredential{}, ErrRevoked
	}
	if credential.ExpiresAt != nil && !credential.ExpiresAt.After(now.UTC()) {
		return ProxyCredential{}, ErrExpired
	}
	if !verifyArgon2id(verifier, password) {
		return ProxyCredential{}, ErrWrongPassword
	}
	return credential, nil
}

func (s *Service) GetAccessProfile(ctx context.Context, actor tenancy.TenantActor, endpointID uuid.UUID) (AccessProfile, error) {
	if !actor.Allows(tenancy.ActionReadResources) || endpointID == uuid.Nil {
		return AccessProfile{}, ErrForbidden
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (AccessProfile, error) {
		return scanProfile(tx.QueryRow(ctx, `SELECT tenant_id,endpoint_id,policy_document,policy_hash,version,created_at,updated_at FROM endpoints.access_profiles WHERE tenant_id=$1 AND endpoint_id=$2`, actor.TenantID(), endpointID))
	})
}

func (s *Service) UpsertAccessProfile(ctx context.Context, actor tenancy.TenantActor, cmd UpsertAccessProfileCommand) (AccessProfile, error) {
	if !actor.Allows(tenancy.ActionManageResources) || cmd.EndpointID == uuid.Nil {
		return AccessProfile{}, ErrForbidden
	}
	compiled, err := CompilePolicy(cmd.Policy)
	if err != nil {
		return AccessProfile{}, ErrInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (AccessProfile, error) {
		if err := ensureFixedEndpoint(ctx, tx, actor.TenantID(), cmd.EndpointID); err != nil {
			return AccessProfile{}, err
		}
		now := s.now().UTC()
		var current int64
		err := tx.QueryRow(ctx, `SELECT version FROM endpoints.access_profiles WHERE tenant_id=$1 AND endpoint_id=$2 FOR UPDATE`, actor.TenantID(), cmd.EndpointID).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			if cmd.ExpectedVersion != nil && *cmd.ExpectedVersion != 0 {
				return AccessProfile{}, ErrVersionConflict
			}
			current = 0
		} else if err != nil {
			return AccessProfile{}, mapStorageError(err)
		} else if cmd.ExpectedVersion != nil && *cmd.ExpectedVersion != current {
			return AccessProfile{}, ErrVersionConflict
		}
		version := current + 1
		policyJSON := compiled.CanonicalJSON
		var result AccessProfile
		err = tx.QueryRow(ctx, `INSERT INTO endpoints.access_profiles (tenant_id,endpoint_id,protocols,dns_mode,source_cidrs,target_allow_cidrs,target_deny_cidrs,target_allow_domains,target_deny_domains,allowed_ports,policy_document,policy_hash,max_connections,max_connection_rate,idle_timeout_seconds,max_bytes_per_connection,traffic_window_seconds,max_window_bytes,version,created_at,updated_at) VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7::jsonb,$8::jsonb,$9::jsonb,$10::jsonb,$11::jsonb,$12,$13,$14,$15,$16,$17,$18,$19,$20,$20) ON CONFLICT (tenant_id,endpoint_id) DO UPDATE SET protocols=EXCLUDED.protocols,dns_mode=EXCLUDED.dns_mode,source_cidrs=EXCLUDED.source_cidrs,target_allow_cidrs=EXCLUDED.target_allow_cidrs,target_deny_cidrs=EXCLUDED.target_deny_cidrs,target_allow_domains=EXCLUDED.target_allow_domains,target_deny_domains=EXCLUDED.target_deny_domains,allowed_ports=EXCLUDED.allowed_ports,policy_document=EXCLUDED.policy_document,policy_hash=EXCLUDED.policy_hash,max_connections=EXCLUDED.max_connections,max_connection_rate=EXCLUDED.max_connection_rate,idle_timeout_seconds=EXCLUDED.idle_timeout_seconds,max_bytes_per_connection=EXCLUDED.max_bytes_per_connection,traffic_window_seconds=EXCLUDED.traffic_window_seconds,max_window_bytes=EXCLUDED.max_window_bytes,version=EXCLUDED.version,updated_at=EXCLUDED.updated_at RETURNING tenant_id,endpoint_id,policy_document,policy_hash,version,created_at,updated_at`, actor.TenantID(), cmd.EndpointID, compiled.Policy.Protocols, compiled.Policy.DNSMode, mustJSON(compiled.Policy.SourceCIDRs), mustJSON(compiled.Policy.TargetAllowCIDRs), mustJSON(compiled.Policy.TargetDenyCIDRs), mustJSON(compiled.Policy.TargetAllowDomains), mustJSON(compiled.Policy.TargetDenyDomains), mustJSON(compiled.Policy.AllowedPorts), policyJSON, compiled.Hash, compiled.Policy.Limits.MaxConnections, compiled.Policy.Limits.MaxConnectionRate, compiled.Policy.Limits.IdleTimeoutSeconds, compiled.Policy.Limits.MaxBytesPerConnection, compiled.Policy.Limits.TrafficWindowSeconds, compiled.Policy.Limits.MaxWindowBytes, version, now).Scan(&result.TenantID, &result.EndpointID, &policyJSON, &result.PolicyHash, &result.Version, &result.CreatedAt, &result.UpdatedAt)
		if err != nil {
			return AccessProfile{}, mapStorageError(err)
		}
		if err := json.Unmarshal(policyJSON, &result.Policy); err != nil {
			return AccessProfile{}, ErrStorage
		}
		if err := s.appendAudit(ctx, tx, actor, "access_profiles.updated", cmd.EndpointID, map[string]any{"endpoint_id": cmd.EndpointID.String(), "version": version, "policy_hash": compiled.Hash}); err != nil {
			return AccessProfile{}, err
		}
		return result, nil
	})
}

func (s *Service) issue(ctx context.Context, tx pgx.Tx, actor tenancy.TenantActor, endpointID uuid.UUID, expiresAt *time.Time, action string) (IssuedCredential, error) {
	password, err := randomString(s.random, 32)
	if err != nil {
		return IssuedCredential{}, ErrStorage
	}
	publicID, err := randomIdentifier(s.random)
	if err != nil {
		return IssuedCredential{}, ErrStorage
	}
	verifier, err := hashArgon2id(password, s.random)
	if err != nil {
		return IssuedCredential{}, ErrStorage
	}
	id, err := s.newID()
	if err != nil {
		return IssuedCredential{}, ErrStorage
	}
	now := s.now().UTC()
	var item IssuedCredential
	err = tx.QueryRow(ctx, `INSERT INTO endpoints.proxy_credentials (id,tenant_id,endpoint_id,public_identifier,verifier,expires_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$7) RETURNING id,tenant_id,endpoint_id,public_identifier,expires_at,revoked_at,created_at,updated_at`, id, actor.TenantID(), endpointID, publicID, verifier, expiresAt, now).Scan(&item.ID, &item.TenantID, &item.EndpointID, &item.PublicIdentifier, &item.ExpiresAt, &item.RevokedAt, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return IssuedCredential{}, mapStorageError(err)
	}
	item.Password = password
	if err := s.appendAudit(ctx, tx, actor, action, item.ID, map[string]any{"credential_id": item.ID.String(), "endpoint_id": endpointID.String()}); err != nil {
		return IssuedCredential{}, err
	}
	return item, nil
}

func (s *Service) appendAudit(ctx context.Context, tx pgx.Tx, actor tenancy.TenantActor, action string, resourceID uuid.UUID, details map[string]any) error {
	metadata := actor.Metadata()
	tenantID, actorID := actor.TenantID(), actor.ActorID()
	return s.audit.Append(ctx, tx, audit.Event{ActorType: metadata.ActorType, ActorID: &actorID, TenantID: &tenantID, Action: action, ResourceType: "proxy_access", ResourceID: &resourceID, Result: "success", SourceIP: metadata.SourceIP, UserAgent: metadata.UserAgent, RequestID: metadata.RequestID, Details: details}, audit.OutboxEvent{EventType: "proxy_access.route_delta", AggregateType: "proxy_access", AggregateID: resourceID, PayloadVersion: 1, Payload: map[string]any{"action": action, "resource_id": resourceID.String()}, AvailableAt: s.now().UTC()})
}

func ensureFixedEndpoint(ctx context.Context, tx pgx.Tx, tenantID, endpointID uuid.UUID) error {
	var mode string
	if err := tx.QueryRow(ctx, `SELECT binding_mode FROM endpoints.proxy_endpoints WHERE tenant_id=$1 AND id=$2`, tenantID, endpointID).Scan(&mode); err != nil {
		return mapStorageError(err)
	}
	if mode != "fixed" {
		return ErrPoolBinding
	}
	return nil
}
func mapStorageError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return ErrStorage
}
func mustJSON(value any) []byte { data, _ := json.Marshal(value); return data }

func scanProfile(row pgx.Row) (AccessProfile, error) {
	var item AccessProfile
	var document []byte
	if err := row.Scan(&item.TenantID, &item.EndpointID, &document, &item.PolicyHash, &item.Version, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return AccessProfile{}, mapStorageError(err)
	}
	if err := json.Unmarshal(document, &item.Policy); err != nil {
		return AccessProfile{}, ErrStorage
	}
	return item, nil
}

func randomString(reader io.Reader, length int) (string, error) {
	data := make([]byte, length)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
func randomIdentifier(reader io.Reader) (string, error) {
	value, err := randomString(reader, 18)
	if err != nil {
		return "", err
	}
	return "px_" + value, nil
}

func hashArgon2id(password string, reader io.Reader) (string, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(reader, salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}
func verifyArgon2id(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory, iterations, parallel uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallel); err != nil || memory == 0 || memory > 256*1024 || iterations == 0 || iterations > 8 || parallel == 0 || parallel > 4 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) == 0 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, uint8(parallel), uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
