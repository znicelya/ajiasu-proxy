package pools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
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

func NewService(db *database.Pools, auditService audit.Service) (*Service, error) {
	if db == nil {
		return nil, ErrInvalidArgument
	}
	if auditService == nil {
		auditService = audit.NewService()
	}
	return &Service{pools: db, audit: auditService, now: func() time.Time { return time.Now().UTC() }, newID: uuid.NewV7}, nil
}

func (s *Service) Create(ctx context.Context, actor tenancy.TenantActor, cmd CreateCommand) (Pool, error) {
	if !actor.Allows(tenancy.ActionManageResources) {
		return Pool{}, ErrForbidden
	}
	if !validName(cmd.Name) || !validStrategy(cmd.Strategy) || validateSelector(cmd.Selector) != nil {
		return Pool{}, ErrInvalidArgument
	}
	id, err := s.newID()
	if err != nil {
		return Pool{}, ErrStorage
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Pool, error) {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ajiasu-tenant:' || $1::uuid::text, 0))`, actor.TenantID()); err != nil {
			return Pool{}, mapError(err)
		}
		var max int
		if err := tx.QueryRow(ctx, `SELECT q.max_pools FROM tenancy.tenant_quotas q JOIN tenancy.tenants t ON t.id=q.tenant_id WHERE q.tenant_id=$1 AND t.state='active' FOR UPDATE OF q`, actor.TenantID()).Scan(&max); err != nil {
			return Pool{}, mapError(err)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pools.account_pools WHERE tenant_id=$1`, actor.TenantID()).Scan(&count); err != nil {
			return Pool{}, mapError(err)
		}
		if count >= max {
			return Pool{}, ErrQuotaExceeded
		}
		encoded, _ := json.Marshal(nonNil(cmd.Selector))
		now := s.now().UTC()
		row := tx.QueryRow(ctx, `INSERT INTO pools.account_pools (id,tenant_id,name,normalized_name,strategy,selector,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$7) RETURNING id,tenant_id,name,strategy,selector,state,version,created_at,updated_at`, id, actor.TenantID(), strings.TrimSpace(cmd.Name), strings.ToLower(strings.TrimSpace(cmd.Name)), cmd.Strategy, encoded, now)
		created, err := scanPool(row)
		if err != nil {
			return Pool{}, mapError(err)
		}
		if err = s.appendAudit(ctx, tx, actor, "pools.pool.created", "account_pool", id, map[string]any{"pool_id": id.String(), "version": created.Version, "strategy": string(created.Strategy)}, now); err != nil {
			return Pool{}, err
		}
		return created, nil
	})
}

func (s *Service) Get(ctx context.Context, actor tenancy.TenantActor, id uuid.UUID) (Pool, error) {
	if !actor.Allows(tenancy.ActionReadResources) {
		return Pool{}, ErrForbidden
	}
	if id == uuid.Nil {
		return Pool{}, ErrInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Pool, error) {
		item, err := scanPool(tx.QueryRow(ctx, `SELECT id,tenant_id,name,strategy,selector,state,version,created_at,updated_at FROM pools.account_pools WHERE tenant_id=$1 AND id=$2`, actor.TenantID(), id))
		return item, mapError(err)
	})
}

func (s *Service) List(ctx context.Context, actor tenancy.TenantActor, after time.Time, afterID uuid.UUID, limit int32) ([]Pool, error) {
	if !actor.Allows(tenancy.ActionReadResources) {
		return nil, ErrForbidden
	}
	if limit < 1 || limit > 200 {
		return nil, ErrInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]Pool, error) {
		rows, err := tx.Query(ctx, `SELECT id,tenant_id,name,strategy,selector,state,version,created_at,updated_at FROM pools.account_pools WHERE tenant_id=$1 AND (created_at,id)>($2,$3) ORDER BY created_at,id LIMIT $4`, actor.TenantID(), after, afterID, limit)
		if err != nil {
			return nil, mapError(err)
		}
		defer rows.Close()
		items := make([]Pool, 0)
		for rows.Next() {
			item, err := scanPool(rows)
			if err != nil {
				return nil, mapError(err)
			}
			items = append(items, item)
		}
		return items, mapError(rows.Err())
	})
}

func (s *Service) Update(ctx context.Context, actor tenancy.TenantActor, id uuid.UUID, cmd UpdateCommand) (Pool, error) {
	if !actor.Allows(tenancy.ActionManageResources) {
		return Pool{}, ErrForbidden
	}
	if id == uuid.Nil || cmd.ExpectedVersion < 1 || (cmd.Name == nil && cmd.Strategy == nil && cmd.Selector == nil && cmd.State == nil) {
		return Pool{}, ErrInvalidArgument
	}
	if cmd.Name != nil && !validName(*cmd.Name) {
		return Pool{}, ErrInvalidArgument
	}
	if cmd.Strategy != nil && !validStrategy(*cmd.Strategy) {
		return Pool{}, ErrInvalidArgument
	}
	if cmd.Selector != nil && validateSelector(*cmd.Selector) != nil {
		return Pool{}, ErrInvalidArgument
	}
	if cmd.State != nil && !validState(*cmd.State) {
		return Pool{}, ErrInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Pool, error) {
		current, err := scanPool(tx.QueryRow(ctx, `SELECT id,tenant_id,name,strategy,selector,state,version,created_at,updated_at FROM pools.account_pools WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, actor.TenantID(), id))
		if err != nil {
			return Pool{}, mapError(err)
		}
		if current.Version != cmd.ExpectedVersion || current.State == StateDeleting {
			return Pool{}, ErrVersionConflict
		}
		name := current.Name
		if cmd.Name != nil {
			name = strings.TrimSpace(*cmd.Name)
		}
		strategy := current.Strategy
		if cmd.Strategy != nil {
			strategy = *cmd.Strategy
		}
		selector := current.Selector
		if cmd.Selector != nil {
			selector = *cmd.Selector
		}
		state := current.State
		if cmd.State != nil {
			state = *cmd.State
		}
		encoded, _ := json.Marshal(nonNil(selector))
		now := s.now().UTC()
		updated, err := scanPool(tx.QueryRow(ctx, `UPDATE pools.account_pools SET name=$1,normalized_name=$2,strategy=$3,selector=$4,state=$5,version=version+1,updated_at=$6 WHERE tenant_id=$7 AND id=$8 AND version=$9 RETURNING id,tenant_id,name,strategy,selector,state,version,created_at,updated_at`, name, strings.ToLower(name), strategy, encoded, state, now, actor.TenantID(), id, cmd.ExpectedVersion))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Pool{}, ErrVersionConflict
			}
			return Pool{}, mapError(err)
		}
		if err = s.appendAudit(ctx, tx, actor, "pools.pool.updated", "account_pool", id, map[string]any{"pool_id": id.String(), "version": updated.Version, "state": string(updated.State)}, now); err != nil {
			return Pool{}, err
		}
		return updated, nil
	})
}

func (s *Service) AddMembership(ctx context.Context, actor tenancy.TenantActor, poolID uuid.UUID, cmd AddMembershipCommand) (Membership, error) {
	if !actor.Allows(tenancy.ActionManageResources) {
		return Membership{}, ErrForbidden
	}
	if poolID == uuid.Nil || cmd.AccountID == uuid.Nil {
		return Membership{}, ErrInvalidArgument
	}
	if cmd.Priority == 0 {
		cmd.Priority = 100
	}
	if cmd.Weight == 0 {
		cmd.Weight = 1
	}
	if cmd.Priority < 0 || cmd.Priority > 1000 || cmd.Weight < 1 || cmd.Weight > 100 {
		return Membership{}, ErrInvalidArgument
	}
	enabled := true
	if cmd.Enabled != nil {
		enabled = *cmd.Enabled
	}
	id, err := s.newID()
	if err != nil {
		return Membership{}, ErrStorage
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Membership, error) {
		var max int
		if err := tx.QueryRow(ctx, `SELECT max_pool_memberships FROM tenancy.tenant_quotas WHERE tenant_id=$1 FOR UPDATE`, actor.TenantID()).Scan(&max); err != nil {
			return Membership{}, mapError(err)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pools.account_pool_memberships WHERE tenant_id=$1`, actor.TenantID()).Scan(&count); err != nil {
			return Membership{}, mapError(err)
		}
		if count >= max {
			return Membership{}, ErrQuotaExceeded
		}
		var selectorJSON, labelsJSON []byte
		var poolState, accountState string
		if err := tx.QueryRow(ctx, `SELECT p.selector,p.state,a.labels,a.state FROM pools.account_pools p JOIN accounts.accounts a ON a.tenant_id=p.tenant_id WHERE p.tenant_id=$1 AND p.id=$2 AND a.id=$3 FOR UPDATE OF p,a`, actor.TenantID(), poolID, cmd.AccountID).Scan(&selectorJSON, &poolState, &labelsJSON, &accountState); err != nil {
			return Membership{}, mapError(err)
		}
		if State(poolState) == StateDeleting || accountState == "deleting" {
			return Membership{}, ErrVersionConflict
		}
		var selector, labels map[string]string
		if json.Unmarshal(selectorJSON, &selector) != nil || json.Unmarshal(labelsJSON, &labels) != nil {
			return Membership{}, ErrStorage
		}
		if !matches(selector, labels) {
			return Membership{}, ErrSelectorMismatch
		}
		now := s.now().UTC()
		created, err := scanMembership(tx.QueryRow(ctx, `INSERT INTO pools.account_pool_memberships (id,tenant_id,pool_id,account_id,priority,weight,enabled,expires_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) RETURNING id,tenant_id,pool_id,account_id,priority,weight,enabled,expires_at,created_at,updated_at`, id, actor.TenantID(), poolID, cmd.AccountID, cmd.Priority, cmd.Weight, enabled, cmd.ExpiresAt, now))
		if err != nil {
			return Membership{}, mapError(err)
		}
		if err = s.appendAudit(ctx, tx, actor, "pools.membership.added", "pool_membership", id, map[string]any{"pool_id": poolID.String(), "membership_id": id.String(), "account_id": cmd.AccountID.String()}, now); err != nil {
			return Membership{}, err
		}
		return created, nil
	})
}

func (s *Service) ListMemberships(ctx context.Context, actor tenancy.TenantActor, poolID uuid.UUID, after time.Time, afterID uuid.UUID, limit int32) ([]Membership, error) {
	if !actor.Allows(tenancy.ActionReadResources) {
		return nil, ErrForbidden
	}
	if poolID == uuid.Nil || limit < 1 || limit > 200 {
		return nil, ErrInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]Membership, error) {
		rows, err := tx.Query(ctx, `SELECT id,tenant_id,pool_id,account_id,priority,weight,enabled,expires_at,created_at,updated_at FROM pools.account_pool_memberships WHERE tenant_id=$1 AND pool_id=$2 AND (created_at,id)>($3,$4) ORDER BY created_at,id LIMIT $5`, actor.TenantID(), poolID, after, afterID, limit)
		if err != nil {
			return nil, mapError(err)
		}
		defer rows.Close()
		items := make([]Membership, 0)
		for rows.Next() {
			item, err := scanMembership(rows)
			if err != nil {
				return nil, mapError(err)
			}
			items = append(items, item)
		}
		return items, mapError(rows.Err())
	})
}

func (s *Service) RemoveMembership(ctx context.Context, actor tenancy.TenantActor, poolID, membershipID uuid.UUID) error {
	if !actor.Allows(tenancy.ActionManageResources) {
		return ErrForbidden
	}
	if poolID == uuid.Nil || membershipID == uuid.Nil {
		return ErrInvalidArgument
	}
	_, err := database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM pools.account_pool_memberships WHERE tenant_id=$1 AND pool_id=$2 AND id=$3`, actor.TenantID(), poolID, membershipID)
		if err != nil {
			return struct{}{}, mapError(err)
		}
		if tag.RowsAffected() == 0 {
			return struct{}{}, ErrNotFound
		}
		now := s.now().UTC()
		if err = s.appendAudit(ctx, tx, actor, "pools.membership.removed", "pool_membership", membershipID, map[string]any{"pool_id": poolID.String(), "membership_id": membershipID.String()}, now); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return err
}

func (s *Service) Capacity(ctx context.Context, actor tenancy.TenantActor, poolID uuid.UUID) (Capacity, error) {
	if !actor.Allows(tenancy.ActionReadResources) {
		return Capacity{}, ErrForbidden
	}
	if poolID == uuid.Nil {
		return Capacity{}, ErrInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Capacity, error) {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pools.account_pools WHERE tenant_id=$1 AND id=$2)`, actor.TenantID(), poolID).Scan(&exists); err != nil {
			return Capacity{}, mapError(err)
		}
		if !exists {
			return Capacity{}, ErrNotFound
		}
		now := s.now().UTC()
		var c Capacity
		c.PoolID = poolID
		err := tx.QueryRow(ctx, `WITH eligible AS (SELECT m.account_id,a.max_concurrency FROM pools.account_pool_memberships m JOIN pools.account_pools p ON p.tenant_id=m.tenant_id AND p.id=m.pool_id JOIN accounts.accounts a ON a.tenant_id=m.tenant_id AND a.id=m.account_id WHERE m.tenant_id=$1 AND m.pool_id=$2 AND m.enabled AND (m.expires_at IS NULL OR m.expires_at>$3) AND p.state='active' AND a.state='active' AND a.health NOT IN ('unhealthy','quarantined')), reserved AS (SELECT r.account_id,count(*)::int AS n FROM accounts.account_capacity_reservations r WHERE r.tenant_id=$1 AND r.expires_at>$3 GROUP BY r.account_id) SELECT (SELECT count(*) FROM pools.account_pool_memberships WHERE tenant_id=$1 AND pool_id=$2),count(*),COALESCE(sum(e.max_concurrency),0),COALESCE(sum(LEAST(e.max_concurrency,COALESCE(r.n,0))),0) FROM eligible e LEFT JOIN reserved r ON r.account_id=e.account_id`, actor.TenantID(), poolID, now).Scan(&c.TotalMembers, &c.EligibleMembers, &c.TotalConcurrency, &c.ReservedConcurrency)
		if err != nil {
			return Capacity{}, mapError(err)
		}
		c.AvailableConcurrency = c.TotalConcurrency - c.ReservedConcurrency
		return c, nil
	})
}

type scanner interface{ Scan(...any) error }

func scanPool(row scanner) (Pool, error) {
	var p Pool
	var selector []byte
	var strategy, state string
	err := row.Scan(&p.ID, &p.TenantID, &p.Name, &strategy, &selector, &state, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return Pool{}, err
	}
	p.Strategy = Strategy(strategy)
	p.State = State(state)
	if json.Unmarshal(selector, &p.Selector) != nil {
		return Pool{}, ErrStorage
	}
	p.Selector = nonNil(p.Selector)
	return p, nil
}
func scanMembership(row scanner) (Membership, error) {
	var m Membership
	err := row.Scan(&m.ID, &m.TenantID, &m.PoolID, &m.AccountID, &m.Priority, &m.Weight, &m.Enabled, &m.ExpiresAt, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}
func nonNil(v map[string]string) map[string]string {
	if v == nil {
		return map[string]string{}
	}
	return v
}
func matches(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
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
func (s *Service) appendAudit(ctx context.Context, tx pgx.Tx, actor tenancy.TenantActor, action, resourceType string, resourceID uuid.UUID, details map[string]any, now time.Time) error {
	m := actor.Metadata()
	tenantID := actor.TenantID()
	actorID := actor.ActorID()
	return s.audit.Append(ctx, tx, audit.Event{TenantID: &tenantID, ActorType: m.ActorType, ActorID: &actorID, Action: action, ResourceType: resourceType, ResourceID: &resourceID, Result: "success", SourceIP: m.SourceIP, UserAgent: m.UserAgent, RequestID: m.RequestID, Details: details}, audit.OutboxEvent{EventType: action, AggregateType: resourceType, AggregateID: resourceID, PayloadVersion: 1, Payload: details, AvailableAt: now})
}
