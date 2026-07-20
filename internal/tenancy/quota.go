package tenancy

import (
	"context"
	"errors"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/audit"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Quota struct {
	TenantID           uuid.UUID `json:"tenant_id"`
	MaxAccounts        int       `json:"max_accounts"`
	MaxPools           int       `json:"max_pools"`
	MaxPoolMemberships int       `json:"max_pool_memberships"`
	Version            int64     `json:"version"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type UpdateQuota struct {
	ExpectedVersion    int64 `json:"expected_version"`
	MaxAccounts        *int  `json:"max_accounts,omitempty"`
	MaxPools           *int  `json:"max_pools,omitempty"`
	MaxPoolMemberships *int  `json:"max_pool_memberships,omitempty"`
}

func (s *Service) GetQuota(ctx context.Context, actor TenantActor) (Quota, error) {
	if !actor.Allows(ActionReadResources) {
		return Quota{}, ErrForbidden
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Quota, error) {
		return scanQuota(tx.QueryRow(ctx, `SELECT tenant_id,max_accounts,max_pools,max_pool_memberships,version,created_at,updated_at FROM tenancy.tenant_quotas WHERE tenant_id=$1`, actor.TenantID()))
	})
}

func (s *Service) UpdateQuota(ctx context.Context, actor TenantActor, cmd UpdateQuota) (Quota, error) {
	if !actor.Allows(ActionManageQuota) {
		return Quota{}, ErrForbidden
	}
	if cmd.ExpectedVersion < 1 || (cmd.MaxAccounts == nil && cmd.MaxPools == nil && cmd.MaxPoolMemberships == nil) {
		return Quota{}, ErrInvalidArgument
	}
	if cmd.MaxAccounts != nil && (*cmd.MaxAccounts < 1 || *cmd.MaxAccounts > 1000) {
		return Quota{}, ErrInvalidArgument
	}
	if cmd.MaxPools != nil && (*cmd.MaxPools < 1 || *cmd.MaxPools > 500) {
		return Quota{}, ErrInvalidArgument
	}
	if cmd.MaxPoolMemberships != nil && (*cmd.MaxPoolMemberships < 1 || *cmd.MaxPoolMemberships > 10000) {
		return Quota{}, ErrInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Quota, error) {
		current, err := scanQuota(tx.QueryRow(ctx, `SELECT tenant_id,max_accounts,max_pools,max_pool_memberships,version,created_at,updated_at FROM tenancy.tenant_quotas WHERE tenant_id=$1 FOR UPDATE`, actor.TenantID()))
		if err != nil {
			return Quota{}, mapStorageError(err)
		}
		if current.Version != cmd.ExpectedVersion {
			return Quota{}, ErrVersionConflict
		}
		maxAccounts := current.MaxAccounts
		if cmd.MaxAccounts != nil {
			maxAccounts = *cmd.MaxAccounts
		}
		maxPools := current.MaxPools
		if cmd.MaxPools != nil {
			maxPools = *cmd.MaxPools
		}
		maxMemberships := current.MaxPoolMemberships
		if cmd.MaxPoolMemberships != nil {
			maxMemberships = *cmd.MaxPoolMemberships
		}
		var accountCount, poolCount, membershipCount int
		if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM accounts.accounts WHERE tenant_id=$1),(SELECT count(*) FROM pools.account_pools WHERE tenant_id=$1),(SELECT count(*) FROM pools.account_pool_memberships WHERE tenant_id=$1)`, actor.TenantID()).Scan(&accountCount, &poolCount, &membershipCount); err != nil {
			return Quota{}, mapStorageError(err)
		}
		if maxAccounts < accountCount || maxPools < poolCount || maxMemberships < membershipCount {
			return Quota{}, ErrInvalidArgument
		}
		now := s.now().UTC()
		updated, err := scanQuota(tx.QueryRow(ctx, `UPDATE tenancy.tenant_quotas SET max_accounts=$1,max_pools=$2,max_pool_memberships=$3,version=version+1,updated_at=$4 WHERE tenant_id=$5 AND version=$6 RETURNING tenant_id,max_accounts,max_pools,max_pool_memberships,version,created_at,updated_at`, maxAccounts, maxPools, maxMemberships, now, actor.TenantID(), cmd.ExpectedVersion))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Quota{}, ErrVersionConflict
			}
			return Quota{}, mapStorageError(err)
		}
		m := actor.Metadata()
		tenantID := actor.TenantID()
		actorID := actor.ActorID()
		details := map[string]any{"tenant_id": tenantID.String(), "version": updated.Version, "max_accounts": updated.MaxAccounts, "max_pools": updated.MaxPools, "max_pool_memberships": updated.MaxPoolMemberships}
		if err = s.audit.Append(ctx, tx, audit.Event{TenantID: &tenantID, ActorType: m.ActorType, ActorID: &actorID, Action: "tenancy.quota.updated", ResourceType: "tenant_quota", ResourceID: &tenantID, Result: "success", SourceIP: m.SourceIP, UserAgent: m.UserAgent, RequestID: m.RequestID, Details: details}, audit.OutboxEvent{EventType: "tenancy.quota.updated", AggregateType: "tenant_quota", AggregateID: tenantID, PayloadVersion: 1, Payload: details, AvailableAt: now}); err != nil {
			return Quota{}, err
		}
		return updated, nil
	})
}

func scanQuota(row pgx.Row) (Quota, error) {
	var q Quota
	err := row.Scan(&q.TenantID, &q.MaxAccounts, &q.MaxPools, &q.MaxPoolMemberships, &q.Version, &q.CreatedAt, &q.UpdatedAt)
	if err != nil {
		return Quota{}, mapStorageError(err)
	}
	return q, nil
}
