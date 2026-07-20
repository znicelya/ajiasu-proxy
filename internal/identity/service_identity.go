package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/audit"
	"github.com/znicelya/ajiasu-proxy/internal/identity/dbgen"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	ServiceScopePlatform    ServiceScope = "platform"
	ServiceScopeTenant      ServiceScope = "tenant"
	serviceTokenPrefixBytes              = 9
	serviceTokenSecretBytes              = 32
	serviceTokenMaxLifetime              = 24 * time.Hour
	maxActiveServiceTokens               = 2
)

var (
	ErrServiceInvalidArgument      = errors.New("invalid service identity argument")
	ErrServiceAuthenticationFailed = errors.New("service token authentication failed")
	ErrServiceNotFound             = errors.New("service identity not found")
	ErrServiceTokenLimit           = errors.New("service identity active token limit reached")
	ErrServiceVersionConflict      = errors.New("service identity version conflict")
	ErrServiceStorage              = errors.New("service identity storage failure")
)

type ServiceScope string

func (s ServiceScope) Valid() bool { return s == ServiceScopePlatform || s == ServiceScopeTenant }

type ServiceActor struct {
	ActorID  uuid.UUID
	Scope    ServiceScope
	TenantID *uuid.UUID
	Role     tenancy.Role
	Metadata AuthenticationMetadata
}

func (a ServiceActor) validate() error {
	if a.ActorID == uuid.Nil || !a.Scope.Valid() || a.Metadata.validate() != nil ||
		a.Scope == ServiceScopePlatform && a.Role != tenancy.PlatformAdmin ||
		a.Scope == ServiceScopeTenant && a.Role != tenancy.TenantAdmin {
		return ErrServiceInvalidArgument
	}
	if a.Scope == ServiceScopePlatform && a.TenantID != nil || a.Scope == ServiceScopeTenant && (a.TenantID == nil || *a.TenantID == uuid.Nil) {
		return ErrServiceInvalidArgument
	}
	return nil
}

type ServiceIdentity struct {
	ID                   uuid.UUID
	Scope                ServiceScope
	TenantID             *uuid.UUID
	Name                 string
	DisabledAt           *time.Time
	Version              int64
	CreatedAt, UpdatedAt time.Time
}
type ServiceToken struct {
	ID, ServiceIdentityID uuid.UUID
	Scope                 ServiceScope
	TenantID              *uuid.UUID
	Prefix                string
	Role                  tenancy.Role
	SourceCIDR            *netip.Prefix
	ExpiresAt             time.Time
	RevokedAt             *time.Time
	CreatedAt             time.Time
}
type ServiceTokenCreated struct {
	ServiceToken
	Plaintext string
}
type ServicePrincipal struct {
	IdentityID uuid.UUID
	Name       string
	Scope      ServiceScope
	TenantID   *uuid.UUID
	Role       tenancy.Role
	TokenID    uuid.UUID
}

type CreateServiceIdentityCommand struct {
	Scope      ServiceScope
	TenantID   *uuid.UUID
	Name       string
	Role       tenancy.Role
	SourceCIDR *netip.Prefix
	ValidFor   time.Duration
}
type IssueServiceTokenCommand struct {
	IdentityID uuid.UUID
	Role       tenancy.Role
	SourceCIDR *netip.Prefix
	ValidFor   time.Duration
}
type AuthenticateServiceTokenCommand struct {
	Token    string
	Scope    ServiceScope
	TenantID *uuid.UUID
	Metadata AuthenticationMetadata
}

type ServiceIdentityService struct {
	pools *database.Pools
	audit audit.Service
	now   func() time.Time
	newID func() (uuid.UUID, error)
}

func NewServiceIdentityService(pools *database.Pools, auditService audit.Service) (*ServiceIdentityService, error) {
	if pools == nil || pools.Platform == nil || pools.Tenant == nil {
		return nil, ErrServiceStorage
	}
	if auditService == nil {
		auditService = audit.NewService()
	}
	return &ServiceIdentityService{pools: pools, audit: auditService, now: func() time.Time { return time.Now().UTC() }, newID: uuid.NewV7}, nil
}

func (s *ServiceIdentityService) List(ctx context.Context, actor ServiceActor, after time.Time, afterID uuid.UUID, pageSize int32) ([]ServiceIdentity, error) {
	if err := actor.validate(); err != nil {
		return nil, err
	}
	if pageSize < 1 || pageSize > 200 {
		return nil, ErrServiceInvalidArgument
	}
	rows, err := inServiceTx(ctx, s, actor.Scope, actor.TenantID, actor.ActorID, func(ctx context.Context, tx pgx.Tx) ([]dbgen.IdentityServiceIdentity, error) {
		return dbgen.New(tx).ListServiceIdentities(ctx, dbgen.ListServiceIdentitiesParams{
			Scope:          pgtype.Text{String: string(actor.Scope), Valid: true},
			TenantID:       cloneUUID(actor.TenantID),
			AfterCreatedAt: after,
			AfterID:        afterID,
			PageSize:       pageSize,
		})
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	result := make([]ServiceIdentity, len(rows))
	for index := range rows {
		result[index] = serviceIdentityFromRow(rows[index])
	}
	return result, nil
}

func NewOpaqueServiceToken() (string, string, []byte, error) {
	prefixBytes := make([]byte, serviceTokenPrefixBytes)
	secret := make([]byte, serviceTokenSecretBytes)
	if _, err := rand.Read(prefixBytes); err != nil {
		return "", "", nil, err
	}
	if _, err := rand.Read(secret); err != nil {
		return "", "", nil, err
	}
	prefix := base64.RawURLEncoding.EncodeToString(prefixBytes)
	encoded := base64.RawURLEncoding.EncodeToString(secret)
	return "ajs_" + prefix + "_" + encoded, prefix, secret, nil
}
func ParseOpaqueServiceToken(token string) (string, []byte, error) {
	if len(token) != 4+12+1+43 || !strings.HasPrefix(token, "ajs_") {
		return "", nil, ErrServiceAuthenticationFailed
	}
	if token[16] != '_' {
		return "", nil, ErrServiceAuthenticationFailed
	}
	prefix, encodedSecret := token[4:16], token[17:]
	prefixRaw, err := base64.RawURLEncoding.DecodeString(prefix)
	if err != nil || len(prefixRaw) != serviceTokenPrefixBytes || base64.RawURLEncoding.EncodeToString(prefixRaw) != prefix {
		return "", nil, ErrServiceAuthenticationFailed
	}
	secret, err := base64.RawURLEncoding.DecodeString(encodedSecret)
	if err != nil || len(secret) != serviceTokenSecretBytes || base64.RawURLEncoding.EncodeToString(secret) != encodedSecret {
		return "", nil, ErrServiceAuthenticationFailed
	}
	return prefix, secret, nil
}

func (s *ServiceIdentityService) Create(ctx context.Context, actor ServiceActor, cmd CreateServiceIdentityCommand) (ServiceIdentity, ServiceTokenCreated, error) {
	if err := actor.validate(); err != nil {
		return ServiceIdentity{}, ServiceTokenCreated{}, err
	}
	if err := validateServiceScope(cmd.Scope, cmd.TenantID, cmd.Role); err != nil || cmd.Scope != actor.Scope || !sameTenant(cmd.TenantID, actor.TenantID) || strings.TrimSpace(cmd.Name) == "" || len(strings.TrimSpace(cmd.Name)) > 200 {
		return ServiceIdentity{}, ServiceTokenCreated{}, ErrServiceInvalidArgument
	}
	validFor, err := serviceValidity(cmd.ValidFor)
	if err != nil {
		return ServiceIdentity{}, ServiceTokenCreated{}, err
	}
	identityID, err := s.newID()
	if err != nil {
		return ServiceIdentity{}, ServiceTokenCreated{}, ErrServiceStorage
	}
	now := s.now().UTC()
	type result struct {
		identity  dbgen.IdentityServiceIdentity
		token     ServiceToken
		plaintext string
	}
	out, err := inServiceTx(ctx, s, actor.Scope, actor.TenantID, actor.ActorID, func(ctx context.Context, tx pgx.Tx) (result, error) {
		q := dbgen.New(tx)
		row, err := q.CreateServiceIdentity(ctx, dbgen.CreateServiceIdentityParams{ID: identityID, Scope: string(cmd.Scope), TenantID: cloneUUID(cmd.TenantID), Name: strings.TrimSpace(cmd.Name), CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return result{}, err
		}
		created, err := s.issueTokenTx(ctx, tx, row, cmd.Role, cmd.SourceCIDR, validFor, now)
		if err != nil {
			return result{}, err
		}
		if err := s.appendServiceAudit(ctx, tx, actor.Metadata, actor.ActorID, cmd.TenantID, identityID, "identity.service_identity.created", "created", now); err != nil {
			return result{}, err
		}
		if err := s.appendServiceAudit(ctx, tx, actor.Metadata, actor.ActorID, cmd.TenantID, created.ID, "identity.service_token.created", "initial", now); err != nil {
			return result{}, err
		}
		return result{identity: row, token: created.ServiceToken, plaintext: created.Plaintext}, nil
	})
	if err != nil {
		return ServiceIdentity{}, ServiceTokenCreated{}, mapServiceError(err)
	}
	return serviceIdentityFromRow(out.identity), ServiceTokenCreated{ServiceToken: out.token, Plaintext: out.plaintext}, nil
}

func (s *ServiceIdentityService) IssueToken(ctx context.Context, actor ServiceActor, cmd IssueServiceTokenCommand) (ServiceTokenCreated, error) {
	if err := actor.validate(); err != nil {
		return ServiceTokenCreated{}, err
	}
	if cmd.IdentityID == uuid.Nil {
		return ServiceTokenCreated{}, ErrServiceInvalidArgument
	}
	validFor, err := serviceValidity(cmd.ValidFor)
	if err != nil {
		return ServiceTokenCreated{}, err
	}
	now := s.now().UTC()
	out, err := inServiceTx(ctx, s, actor.Scope, actor.TenantID, actor.ActorID, func(ctx context.Context, tx pgx.Tx) (ServiceTokenCreated, error) {
		q := dbgen.New(tx)
		identityRow, err := q.GetServiceIdentityForUpdate(ctx, cmd.IdentityID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ServiceTokenCreated{}, ErrServiceNotFound
		}
		if err != nil {
			return ServiceTokenCreated{}, err
		}
		if identityRow.DisabledAt != nil || ServiceScope(identityRow.Scope) != actor.Scope || !sameTenant(identityRow.TenantID, actor.TenantID) {
			return ServiceTokenCreated{}, ErrServiceNotFound
		}
		if err := validateServiceScope(ServiceScope(identityRow.Scope), identityRow.TenantID, cmd.Role); err != nil {
			return ServiceTokenCreated{}, err
		}
		created, err := s.issueTokenTx(ctx, tx, identityRow, cmd.Role, cmd.SourceCIDR, validFor, now)
		if err != nil {
			return ServiceTokenCreated{}, err
		}
		if err := s.appendServiceAudit(ctx, tx, actor.Metadata, actor.ActorID, identityRow.TenantID, created.ID, "identity.service_token.created", "rotation", now); err != nil {
			return ServiceTokenCreated{}, err
		}
		return created, nil
	})
	if err != nil {
		return ServiceTokenCreated{}, mapServiceError(err)
	}
	return out, nil
}

func (s *ServiceIdentityService) issueTokenTx(ctx context.Context, tx pgx.Tx, identityRow dbgen.IdentityServiceIdentity, role tenancy.Role, sourceCIDR *netip.Prefix, validFor time.Duration, now time.Time) (ServiceTokenCreated, error) {
	q := dbgen.New(tx)
	active, err := q.CountActiveServiceTokens(ctx, dbgen.CountActiveServiceTokensParams{ServiceIdentityID: identityRow.ID, Now: now})
	if err != nil {
		return ServiceTokenCreated{}, err
	}
	if active >= maxActiveServiceTokens {
		return ServiceTokenCreated{}, ErrServiceTokenLimit
	}
	plaintext, prefix, secret, err := NewOpaqueServiceToken()
	if err != nil {
		return ServiceTokenCreated{}, err
	}
	verifier, err := HashPassword(secret)
	clear(secret)
	if err != nil {
		return ServiceTokenCreated{}, err
	}
	tokenID, err := s.newID()
	if err != nil {
		return ServiceTokenCreated{}, err
	}
	cidr := canonicalPrefix(sourceCIDR)
	row, err := q.CreateServiceToken(ctx, dbgen.CreateServiceTokenParams{ID: tokenID, ServiceIdentityID: identityRow.ID, Scope: identityRow.Scope, TenantID: cloneUUID(identityRow.TenantID), Prefix: prefix, Verifier: verifier, Role: string(role), SourceCidr: cidr, ExpiresAt: now.Add(validFor), CreatedAt: now})
	if err != nil {
		return ServiceTokenCreated{}, err
	}
	return ServiceTokenCreated{ServiceToken: serviceTokenFromRow(row), Plaintext: plaintext}, nil
}

func (s *ServiceIdentityService) RevokeToken(ctx context.Context, actor ServiceActor, identityID, tokenID uuid.UUID) error {
	if actor.validate() != nil || identityID == uuid.Nil || tokenID == uuid.Nil {
		return ErrServiceInvalidArgument
	}
	now := s.now().UTC()
	_, err := inServiceTx(ctx, s, actor.Scope, actor.TenantID, actor.ActorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		q := dbgen.New(tx)
		identityRow, err := q.GetServiceIdentityForUpdate(ctx, identityID)
		if errors.Is(err, pgx.ErrNoRows) {
			return struct{}{}, ErrServiceNotFound
		}
		if err != nil {
			return struct{}{}, err
		}
		if ServiceScope(identityRow.Scope) != actor.Scope || !sameTenant(identityRow.TenantID, actor.TenantID) {
			return struct{}{}, ErrServiceNotFound
		}
		var storedIdentity uuid.UUID
		if err := tx.QueryRow(ctx, "SELECT service_identity_id FROM identity.service_tokens WHERE id=$1", tokenID).Scan(&storedIdentity); errors.Is(err, pgx.ErrNoRows) {
			return struct{}{}, ErrServiceNotFound
		} else if err != nil {
			return struct{}{}, err
		}
		if storedIdentity != identityID {
			return struct{}{}, ErrServiceNotFound
		}
		if _, err := q.RevokeServiceToken(ctx, dbgen.RevokeServiceTokenParams{RevokedAt: now, ID: tokenID}); errors.Is(err, pgx.ErrNoRows) {
			return struct{}{}, ErrServiceNotFound
		} else if err != nil {
			return struct{}{}, err
		}
		if err := s.appendServiceAudit(ctx, tx, actor.Metadata, actor.ActorID, identityRow.TenantID, tokenID, "identity.service_token.revoked", "requested", now); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return mapServiceError(err)
}

func (s *ServiceIdentityService) Authenticate(ctx context.Context, cmd AuthenticateServiceTokenCommand) (ServicePrincipal, error) {
	if !cmd.Scope.Valid() || cmd.Metadata.validate() != nil || cmd.Scope == ServiceScopePlatform && cmd.TenantID != nil || cmd.Scope == ServiceScopeTenant && (cmd.TenantID == nil || *cmd.TenantID == uuid.Nil) {
		return ServicePrincipal{}, ErrServiceAuthenticationFailed
	}
	prefix, secret, err := ParseOpaqueServiceToken(cmd.Token)
	if err != nil {
		return ServicePrincipal{}, ErrServiceAuthenticationFailed
	}
	defer clear(secret)
	now := s.now().UTC()
	type outcome struct {
		principal     ServicePrincipal
		authenticated bool
	}
	result, err := inServiceTx(ctx, s, cmd.Scope, cmd.TenantID, cmd.Metadata.RequestID, func(ctx context.Context, tx pgx.Tx) (outcome, error) {
		rows, err := dbgen.New(tx).FindServiceTokenCandidates(ctx, dbgen.FindServiceTokenCandidatesParams{Prefix: prefix, Scope: string(cmd.Scope), TenantID: cloneUUID(cmd.TenantID)})
		if err != nil {
			return outcome{}, err
		}
		var matched *dbgen.FindServiceTokenCandidatesRow
		for i := range rows {
			ok, verifyErr := VerifyPassword(secret, rows[i].Verifier)
			if verifyErr != nil {
				return outcome{}, verifyErr
			}
			if ok && matched == nil {
				matched = &rows[i]
			}
		}
		reason := "invalid_credentials"
		var actorID *uuid.UUID
		aggregateID := cmd.Metadata.RequestID
		if matched != nil {
			principal, err := dbgen.New(tx).GetServiceIdentityForAuthentication(ctx, matched.ServiceIdentityID)
			if errors.Is(err, pgx.ErrNoRows) {
				matched = nil
			} else if err != nil {
				return outcome{}, err
			}
			var final dbgen.IdentityServiceToken
			if matched != nil {
				final, err = dbgen.New(tx).GetServiceTokenForAuthentication(ctx, dbgen.GetServiceTokenForAuthenticationParams{ID: matched.ID, ServiceIdentityID: matched.ServiceIdentityID, Scope: string(cmd.Scope), TenantID: cloneUUID(cmd.TenantID)})
				if errors.Is(err, pgx.ErrNoRows) {
					matched = nil
				} else if err != nil {
					return outcome{}, err
				}
			}
			if matched == nil {
				goto failed
			}
			actorID = &final.ServiceIdentityID
			aggregateID = final.ID
			switch {
			case principal.DisabledAt != nil:
				reason = "identity_disabled"
			case final.RevokedAt != nil:
				reason = "revoked"
			case !now.Before(final.ExpiresAt):
				reason = "expired"
			case final.SourceCidr != nil && !final.SourceCidr.Contains(cmd.Metadata.SourceIP.Unmap()):
				reason = "source_rejected"
			default:
				if err := s.appendServiceAuthAudit(ctx, tx, cmd.Metadata, actorID, cmd.TenantID, aggregateID, "success", "authenticated", now); err != nil {
					return outcome{}, err
				}
				return outcome{principal: ServicePrincipal{IdentityID: final.ServiceIdentityID, Name: principal.Name, Scope: ServiceScope(final.Scope), TenantID: cloneUUID(final.TenantID), Role: tenancy.Role(final.Role), TokenID: final.ID}, authenticated: true}, nil
			}
		}
	failed:
		if err := s.appendServiceAuthAudit(ctx, tx, cmd.Metadata, actorID, cmd.TenantID, aggregateID, "failure", reason, now); err != nil {
			return outcome{}, err
		}
		return outcome{}, nil
	})
	if err != nil {
		return ServicePrincipal{}, mapServiceError(err)
	}
	if !result.authenticated {
		return ServicePrincipal{}, ErrServiceAuthenticationFailed
	}
	return result.principal, nil
}

func (s *ServiceIdentityService) Disable(ctx context.Context, actor ServiceActor, id uuid.UUID, expectedVersion int64) error {
	if actor.validate() != nil || id == uuid.Nil || expectedVersion <= 0 {
		return ErrServiceInvalidArgument
	}
	now := s.now().UTC()
	_, err := inServiceTx(ctx, s, actor.Scope, actor.TenantID, actor.ActorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		q := dbgen.New(tx)
		row, err := q.GetServiceIdentityForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return struct{}{}, ErrServiceNotFound
		}
		if err != nil {
			return struct{}{}, err
		}
		if ServiceScope(row.Scope) != actor.Scope || !sameTenant(row.TenantID, actor.TenantID) {
			return struct{}{}, ErrServiceNotFound
		}
		if row.Version != expectedVersion {
			return struct{}{}, ErrServiceVersionConflict
		}
		if _, err := q.DisableServiceIdentity(ctx, dbgen.DisableServiceIdentityParams{DisabledAt: now, UpdatedAt: now, ID: id, ExpectedVersion: expectedVersion}); err != nil {
			return struct{}{}, err
		}
		if _, err := q.RevokeActiveServiceIdentityTokens(ctx, dbgen.RevokeActiveServiceIdentityTokensParams{RevokedAt: now, ServiceIdentityID: id}); err != nil {
			return struct{}{}, err
		}
		if err := s.appendServiceAudit(ctx, tx, actor.Metadata, actor.ActorID, row.TenantID, id, "identity.service_identity.disabled", "requested", now); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return mapServiceError(err)
}

func inServiceTx[T any](ctx context.Context, s *ServiceIdentityService, scope ServiceScope, tenantID *uuid.UUID, actorID uuid.UUID, fn func(context.Context, pgx.Tx) (T, error)) (T, error) {
	if scope == ServiceScopePlatform {
		return database.InPlatformTx(ctx, s.pools.Platform, actorID, fn)
	}
	if tenantID == nil {
		var zero T
		return zero, ErrServiceInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, *tenantID, actorID, fn)
}

func (s *ServiceIdentityService) appendServiceAudit(ctx context.Context, tx pgx.Tx, metadata AuthenticationMetadata, actorID uuid.UUID, tenantID *uuid.UUID, resourceID uuid.UUID, action, category string, now time.Time) error {
	return s.audit.Append(ctx, tx, audit.Event{TenantID: cloneUUID(tenantID), ActorType: "user", ActorID: &actorID, Action: action, ResourceType: "service_identity", ResourceID: &resourceID, Result: "success", SourceIP: metadata.SourceIP.Unmap(), UserAgent: strings.TrimSpace(metadata.UserAgent), RequestID: metadata.RequestID, Details: map[string]any{"category": category}}, audit.OutboxEvent{EventType: action, AggregateType: "service_identity", AggregateID: resourceID, PayloadVersion: 1, Payload: map[string]any{"category": category}, AvailableAt: now})
}
func (s *ServiceIdentityService) appendServiceAuthAudit(ctx context.Context, tx pgx.Tx, metadata AuthenticationMetadata, actorID, tenantID *uuid.UUID, aggregateID uuid.UUID, result, category string, now time.Time) error {
	return s.audit.Append(ctx, tx, audit.Event{TenantID: cloneUUID(tenantID), ActorType: "service_identity", ActorID: actorID, Action: "identity.service_token.authenticated", ResourceType: "service_token", ResourceID: nil, Result: result, SourceIP: metadata.SourceIP.Unmap(), UserAgent: strings.TrimSpace(metadata.UserAgent), RequestID: metadata.RequestID, Details: map[string]any{"category": category}}, audit.OutboxEvent{EventType: "identity.service_token.authenticated", AggregateType: "authentication_attempt", AggregateID: aggregateID, PayloadVersion: 1, Payload: map[string]any{"category": category, "result": result}, AvailableAt: now})
}

func validateServiceScope(scope ServiceScope, tenantID *uuid.UUID, role tenancy.Role) error {
	if !scope.Valid() || !role.Valid() {
		return ErrServiceInvalidArgument
	}
	if scope == ServiceScopePlatform {
		if tenantID != nil || role != tenancy.PlatformAdmin {
			return ErrServiceInvalidArgument
		}
	} else if tenantID == nil || *tenantID == uuid.Nil || !role.TenantScoped() {
		return ErrServiceInvalidArgument
	}
	return nil
}
func serviceValidity(value time.Duration) (time.Duration, error) {
	if value == 0 {
		return serviceTokenMaxLifetime, nil
	}
	if value <= 0 || value > serviceTokenMaxLifetime {
		return 0, ErrServiceInvalidArgument
	}
	return value, nil
}
func canonicalPrefix(value *netip.Prefix) *netip.Prefix {
	if value == nil {
		return nil
	}
	masked := value.Masked()
	return &masked
}
func sameTenant(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func cloneUUID(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func serviceIdentityFromRow(row dbgen.IdentityServiceIdentity) ServiceIdentity {
	return ServiceIdentity{ID: row.ID, Scope: ServiceScope(row.Scope), TenantID: cloneUUID(row.TenantID), Name: row.Name, DisabledAt: row.DisabledAt, Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func serviceTokenFromRow(row dbgen.IdentityServiceToken) ServiceToken {
	return ServiceToken{ID: row.ID, ServiceIdentityID: row.ServiceIdentityID, Scope: ServiceScope(row.Scope), TenantID: cloneUUID(row.TenantID), Prefix: row.Prefix, Role: tenancy.Role(row.Role), SourceCIDR: row.SourceCidr, ExpiresAt: row.ExpiresAt, RevokedAt: row.RevokedAt, CreatedAt: row.CreatedAt}
}
func mapServiceError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrServiceInvalidArgument, ErrServiceAuthenticationFailed, ErrServiceNotFound, ErrServiceTokenLimit, ErrServiceVersionConflict} {
		if errors.Is(err, known) {
			return known
		}
	}
	return fmt.Errorf("%w: %v", ErrServiceStorage, err)
}
