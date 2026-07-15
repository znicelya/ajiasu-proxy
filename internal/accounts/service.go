package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/secrets"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	pools   *database.Pools
	secrets secrets.Provider
	audit   audit.Service
	now     func() time.Time
	newID   func() (uuid.UUID, error)
}

func NewService(pools *database.Pools, provider secrets.Provider, auditService audit.Service) (*Service, error) {
	if pools == nil || provider == nil {
		return nil, ErrInvalidArgument
	}
	if auditService == nil {
		auditService = audit.NewService()
	}
	return &Service{pools: pools, secrets: provider, audit: auditService, now: func() time.Time { return time.Now().UTC() }, newID: uuid.NewV7}, nil
}

func (s *Service) Create(ctx context.Context, actor tenancy.TenantActor, cmd CreateCommand) (Account, error) {
	if !actor.Allows(tenancy.ActionManageResources) || validateCreate(cmd) != nil {
		return Account{}, chooseForbidden(actor, tenancy.ActionManageResources)
	}
	id, err := s.newID()
	if err != nil {
		return Account{}, ErrStorage
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Account, error) {
		return s.createInTx(ctx, tx, actor, id, cmd)
	})
}

func chooseForbidden(actor tenancy.TenantActor, action tenancy.Action) error {
	if !actor.Allows(action) {
		return ErrForbidden
	}
	return ErrInvalidArgument
}

func (s *Service) createInTx(ctx context.Context, tx pgx.Tx, actor tenancy.TenantActor, id uuid.UUID, cmd CreateCommand) (Account, error) {
	if cmd.MaxConcurrency == 0 {
		cmd.MaxConcurrency = 1
	}
	now := s.now().UTC()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ajiasu-tenant:' || $1::uuid::text, 0))`, actor.TenantID()); err != nil {
		return Account{}, mapError(err)
	}
	var maxAccounts int
	var tenantState string
	if err := tx.QueryRow(ctx, `SELECT q.max_accounts, t.state FROM tenancy.tenants t JOIN tenancy.tenant_quotas q ON q.tenant_id=t.id WHERE t.id=$1 FOR UPDATE OF q`, actor.TenantID()).Scan(&maxAccounts, &tenantState); err != nil {
		return Account{}, mapError(err)
	}
	if tenantState != "active" {
		return Account{}, ErrForbidden
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts.accounts WHERE tenant_id=$1`, actor.TenantID()).Scan(&count); err != nil {
		return Account{}, mapError(err)
	}
	if count >= maxAccounts {
		return Account{}, ErrQuotaExceeded
	}
	labels, _ := json.Marshal(nonNilLabels(cmd.Labels))
	var membership any
	if cmd.MembershipID != nil {
		membership = strings.TrimSpace(*cmd.MembershipID)
	}
	row := tx.QueryRow(ctx, `INSERT INTO accounts.accounts
		(id,tenant_id,name,normalized_name,membership_id,membership_expires_at,labels,max_concurrency,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
		RETURNING id,tenant_id,name,state,health,membership_id,membership_expires_at,labels,max_concurrency,version,created_at,updated_at`,
		id, actor.TenantID(), strings.TrimSpace(cmd.Name), strings.ToLower(strings.TrimSpace(cmd.Name)), membership, cmd.MembershipExpiresAt, labels, cmd.MaxConcurrency, now)
	account, err := scanAccount(row)
	if err != nil {
		return Account{}, mapError(err)
	}
	payload, _ := json.Marshal(cmd.Credential)
	sealed, err := s.secrets.Seal(ctx, secrets.Context{TenantID: actor.TenantID(), AccountID: id, Version: 1, Purpose: secrets.AccountCredentialPurpose}, payload)
	clear(payload)
	if err != nil {
		return Account{}, ErrDependencyUnavailable
	}
	if _, err := tx.Exec(ctx, `INSERT INTO accounts.account_credentials
		(tenant_id,account_id,version,provider,key_id,ciphertext,wrapped_dek,external_ref,created_by,created_at)
		VALUES ($1,$2,1,$3,$4,$5,$6,$7,$8,$9)`, actor.TenantID(), id, sealed.Provider, sealed.KeyID, sealed.Ciphertext, sealed.WrappedDEK, sealed.ExternalRef, actor.ActorID(), now); err != nil {
		return Account{}, mapError(err)
	}
	account.Credential = CredentialMetadata{Version: 1, Provider: sealed.Provider, CreatedAt: now}
	if err := s.appendAudit(ctx, tx, actor, "accounts.account.created", "account", id, map[string]any{"account_id": id.String(), "version": int64(1), "provider": sealed.Provider}, now); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (s *Service) Get(ctx context.Context, actor tenancy.TenantActor, id uuid.UUID) (Account, error) {
	if id == uuid.Nil || !actor.Allows(tenancy.ActionReadResources) {
		return Account{}, chooseForbidden(actor, tenancy.ActionReadResources)
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Account, error) {
		return getAccount(ctx, tx, actor.TenantID(), id)
	})
}

func (s *Service) List(ctx context.Context, actor tenancy.TenantActor, after time.Time, afterID uuid.UUID, limit int32) ([]Account, error) {
	if limit < 1 || limit > 200 || !actor.Allows(tenancy.ActionReadResources) {
		return nil, chooseForbidden(actor, tenancy.ActionReadResources)
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]Account, error) {
		rows, err := tx.Query(ctx, `SELECT a.id,a.tenant_id,a.name,a.state,a.health,a.membership_id,a.membership_expires_at,a.labels,a.max_concurrency,a.version,a.created_at,a.updated_at,
			c.version,c.provider,c.created_at,c.retired_at FROM accounts.accounts a JOIN accounts.account_credentials c ON c.tenant_id=a.tenant_id AND c.account_id=a.id AND c.retired_at IS NULL
			WHERE a.tenant_id=$1 AND (a.created_at,a.id)>($2,$3) ORDER BY a.created_at,a.id LIMIT $4`, actor.TenantID(), after, afterID, limit)
		if err != nil {
			return nil, mapError(err)
		}
		defer rows.Close()
		items := make([]Account, 0)
		for rows.Next() {
			item, err := scanAccountWithCredential(rows)
			if err != nil {
				return nil, mapError(err)
			}
			items = append(items, item)
		}
		return items, mapError(rows.Err())
	})
}

func (s *Service) Update(ctx context.Context, actor tenancy.TenantActor, id uuid.UUID, cmd UpdateCommand) (Account, error) {
	if id == uuid.Nil || cmd.ExpectedVersion < 1 || !actor.Allows(tenancy.ActionManageResources) {
		return Account{}, chooseForbidden(actor, tenancy.ActionManageResources)
	}
	if cmd.Name == nil && cmd.State == nil && cmd.MembershipID == nil && cmd.MembershipExpiresAt == nil && cmd.Labels == nil && cmd.MaxConcurrency == nil {
		return Account{}, ErrInvalidArgument
	}
	if cmd.Name != nil && (len(strings.TrimSpace(*cmd.Name)) == 0 || len(strings.TrimSpace(*cmd.Name)) > 200) {
		return Account{}, ErrInvalidArgument
	}
	if cmd.State != nil && !validState(*cmd.State) {
		return Account{}, ErrInvalidArgument
	}
	if cmd.Labels != nil && validateLabels(*cmd.Labels) != nil {
		return Account{}, ErrInvalidArgument
	}
	if cmd.MaxConcurrency != nil && (*cmd.MaxConcurrency < 1 || *cmd.MaxConcurrency > 32) {
		return Account{}, ErrInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Account, error) {
		current, err := getAccount(ctx, tx, actor.TenantID(), id)
		if err != nil {
			return Account{}, err
		}
		if current.Version != cmd.ExpectedVersion || current.State == StateDeleting {
			return Account{}, ErrVersionConflict
		}
		name := current.Name
		if cmd.Name != nil {
			name = strings.TrimSpace(*cmd.Name)
		}
		state := current.State
		if cmd.State != nil {
			state = *cmd.State
		}
		labels := current.Labels
		if cmd.Labels != nil {
			labels = *cmd.Labels
		}
		maxConcurrency := current.MaxConcurrency
		if cmd.MaxConcurrency != nil {
			maxConcurrency = *cmd.MaxConcurrency
		}
		membership := current.MembershipID
		if cmd.MembershipID != nil {
			v := strings.TrimSpace(*cmd.MembershipID)
			membership = &v
		}
		expires := current.MembershipExpiresAt
		if cmd.MembershipExpiresAt != nil {
			expires = cmd.MembershipExpiresAt
		}
		encoded, _ := json.Marshal(nonNilLabels(labels))
		now := s.now().UTC()
		row := tx.QueryRow(ctx, `UPDATE accounts.accounts SET name=$1,normalized_name=$2,state=$3,membership_id=$4,membership_expires_at=$5,labels=$6,max_concurrency=$7,version=version+1,updated_at=$8 WHERE tenant_id=$9 AND id=$10 AND version=$11 RETURNING id,tenant_id,name,state,health,membership_id,membership_expires_at,labels,max_concurrency,version,created_at,updated_at`, name, strings.ToLower(name), state, membership, expires, encoded, maxConcurrency, now, actor.TenantID(), id, cmd.ExpectedVersion)
		updated, err := scanAccount(row)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Account{}, ErrVersionConflict
			}
			return Account{}, mapError(err)
		}
		updated.Credential = current.Credential
		if err := s.appendAudit(ctx, tx, actor, "accounts.account.updated", "account", id, map[string]any{"account_id": id.String(), "version": updated.Version, "state": string(updated.State)}, now); err != nil {
			return Account{}, err
		}
		return updated, nil
	})
}

func (s *Service) RotateCredential(ctx context.Context, actor tenancy.TenantActor, id uuid.UUID, credential Credential) (CredentialMetadata, error) {
	if id == uuid.Nil || !actor.Allows(tenancy.ActionManageResources) || len(credential.Username) == 0 || len(credential.Username) > 512 || len(credential.Password) == 0 || len(credential.Password) > 8192 {
		return CredentialMetadata{}, chooseForbidden(actor, tenancy.ActionManageResources)
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (CredentialMetadata, error) {
		var state string
		var version int64
		if err := tx.QueryRow(ctx, `SELECT state, COALESCE((SELECT max(version) FROM accounts.account_credentials c WHERE c.tenant_id=a.tenant_id AND c.account_id=a.id),0) FROM accounts.accounts a WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, actor.TenantID(), id).Scan(&state, &version); err != nil {
			return CredentialMetadata{}, mapError(err)
		}
		if State(state) == StateDeleting {
			return CredentialMetadata{}, ErrVersionConflict
		}
		version++
		payload, _ := json.Marshal(credential)
		sealed, err := s.secrets.Seal(ctx, secrets.Context{TenantID: actor.TenantID(), AccountID: id, Version: version, Purpose: secrets.AccountCredentialPurpose}, payload)
		clear(payload)
		if err != nil {
			return CredentialMetadata{}, ErrDependencyUnavailable
		}
		now := s.now().UTC()
		if _, err = tx.Exec(ctx, `UPDATE accounts.account_credentials SET retired_at=$1 WHERE tenant_id=$2 AND account_id=$3 AND retired_at IS NULL`, now, actor.TenantID(), id); err != nil {
			return CredentialMetadata{}, mapError(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO accounts.account_credentials (tenant_id,account_id,version,provider,key_id,ciphertext,wrapped_dek,external_ref,created_by,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, actor.TenantID(), id, version, sealed.Provider, sealed.KeyID, sealed.Ciphertext, sealed.WrappedDEK, sealed.ExternalRef, actor.ActorID(), now); err != nil {
			return CredentialMetadata{}, mapError(err)
		}
		if err = s.appendAudit(ctx, tx, actor, "accounts.credential.rotated", "account_credential", id, map[string]any{"account_id": id.String(), "credential_version": version, "provider": sealed.Provider}, now); err != nil {
			return CredentialMetadata{}, err
		}
		return CredentialMetadata{Version: version, Provider: sealed.Provider, CreatedAt: now}, nil
	})
}

func (s *Service) BulkImport(ctx context.Context, actor tenancy.TenantActor, items []CreateCommand) ([]BulkResult, error) {
	if !actor.Allows(tenancy.ActionManageResources) {
		return nil, ErrForbidden
	}
	if len(items) == 0 || len(items) > 100 {
		return nil, ErrInvalidArgument
	}
	for _, item := range items {
		if validateCreate(item) != nil {
			return nil, ErrInvalidArgument
		}
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]BulkResult, error) {
		results := make([]BulkResult, len(items))
		success := 0
		for i, item := range items {
			id, err := s.newID()
			if err != nil {
				return nil, ErrStorage
			}
			savepoint := fmt.Sprintf("phase3_import_%d", i)
			if _, err = tx.Exec(ctx, "SAVEPOINT "+savepoint); err != nil {
				return nil, mapError(err)
			}
			_, err = s.createInTx(ctx, tx, actor, id, item)
			if err != nil {
				_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepoint)
				results[i] = BulkResult{Index: i, Code: bulkCode(err), Message: "item was not imported"}
			} else {
				success++
				results[i] = BulkResult{Index: i, AccountID: &id, Code: "created", Message: "account created"}
			}
			if _, releaseErr := tx.Exec(ctx, "RELEASE SAVEPOINT "+savepoint); releaseErr != nil {
				return nil, mapError(releaseErr)
			}
		}
		now := s.now().UTC()
		aggregate, err := s.newID()
		if err != nil {
			return nil, ErrStorage
		}
		if err = s.appendAudit(ctx, tx, actor, "accounts.bulk_import.completed", "bulk_import", aggregate, map[string]any{"item_count": len(items), "success_count": success, "failure_count": len(items) - success}, now); err != nil {
			return nil, err
		}
		return results, nil
	})
}

func (s *Service) Reserve(ctx context.Context, actor tenancy.TenantActor, accountID, ownerID uuid.UUID, ttl time.Duration) (CapacityReservation, error) {
	if !actor.Allows(tenancy.ActionManageResources) || accountID == uuid.Nil || ownerID == uuid.Nil || ttl <= 0 {
		return CapacityReservation{}, ErrInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (CapacityReservation, error) {
		now := s.now().UTC()
		var max int
		var state string
		if err := tx.QueryRow(ctx, `SELECT max_concurrency,state FROM accounts.accounts WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, actor.TenantID(), accountID).Scan(&max, &state); err != nil {
			return CapacityReservation{}, mapError(err)
		}
		if State(state) != StateActive {
			return CapacityReservation{}, ErrCapacityExhausted
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts.account_capacity_reservations WHERE tenant_id=$1 AND account_id=$2 AND expires_at>$3`, actor.TenantID(), accountID, now).Scan(&count); err != nil {
			return CapacityReservation{}, mapError(err)
		}
		if count >= max {
			return CapacityReservation{}, ErrCapacityExhausted
		}
		id, err := s.newID()
		if err != nil {
			return CapacityReservation{}, ErrStorage
		}
		expires := now.Add(ttl)
		if _, err = tx.Exec(ctx, `INSERT INTO accounts.account_capacity_reservations (id,tenant_id,account_id,owner_id,created_at,expires_at) VALUES ($1,$2,$3,$4,$5,$6)`, id, actor.TenantID(), accountID, ownerID, now, expires); err != nil {
			return CapacityReservation{}, mapError(err)
		}
		return CapacityReservation{ID: id, AccountID: accountID, OwnerID: ownerID, ExpiresAt: expires}, nil
	})
}

func (s *Service) Release(ctx context.Context, actor tenancy.TenantActor, reservationID uuid.UUID) error {
	if !actor.Allows(tenancy.ActionManageResources) || reservationID == uuid.Nil {
		return ErrInvalidArgument
	}
	_, err := database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM accounts.account_capacity_reservations WHERE tenant_id=$1 AND id=$2`, actor.TenantID(), reservationID)
		if err != nil {
			return struct{}{}, mapError(err)
		}
		if tag.RowsAffected() == 0 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, nil
	})
	return err
}

func getAccount(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (Account, error) {
	row := tx.QueryRow(ctx, `SELECT a.id,a.tenant_id,a.name,a.state,a.health,a.membership_id,a.membership_expires_at,a.labels,a.max_concurrency,a.version,a.created_at,a.updated_at,c.version,c.provider,c.created_at,c.retired_at FROM accounts.accounts a JOIN accounts.account_credentials c ON c.tenant_id=a.tenant_id AND c.account_id=a.id AND c.retired_at IS NULL WHERE a.tenant_id=$1 AND a.id=$2`, tenantID, id)
	item, err := scanAccountWithCredential(row)
	return item, mapError(err)
}

type scanner interface{ Scan(...any) error }

func scanAccount(row scanner) (Account, error) {
	var a Account
	var state, health string
	var labels []byte
	err := row.Scan(&a.ID, &a.TenantID, &a.Name, &state, &health, &a.MembershipID, &a.MembershipExpiresAt, &labels, &a.MaxConcurrency, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return Account{}, err
	}
	a.State = State(state)
	a.Health = Health(health)
	if err = json.Unmarshal(labels, &a.Labels); err != nil {
		return Account{}, err
	}
	a.Labels = nonNilLabels(a.Labels)
	return a, nil
}
func scanAccountWithCredential(row scanner) (Account, error) {
	var a Account
	var state, health string
	var labels []byte
	err := row.Scan(&a.ID, &a.TenantID, &a.Name, &state, &health, &a.MembershipID, &a.MembershipExpiresAt, &labels, &a.MaxConcurrency, &a.Version, &a.CreatedAt, &a.UpdatedAt, &a.Credential.Version, &a.Credential.Provider, &a.Credential.CreatedAt, &a.Credential.RetiredAt)
	if err != nil {
		return Account{}, err
	}
	a.State = State(state)
	a.Health = Health(health)
	if err = json.Unmarshal(labels, &a.Labels); err != nil {
		return Account{}, err
	}
	a.Labels = nonNilLabels(a.Labels)
	return a, nil
}
func nonNilLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return map[string]string{}
	}
	return labels
}
func bulkCode(err error) string {
	switch {
	case errors.Is(err, ErrQuotaExceeded):
		return "quota_exceeded"
	case errors.Is(err, ErrAlreadyExists):
		return "already_exists"
	case errors.Is(err, ErrDependencyUnavailable):
		return "dependency_unavailable"
	default:
		return "rejected"
	}
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
	return s.audit.Append(ctx, tx, audit.Event{TenantID: ptrUUID(actor.TenantID()), ActorType: m.ActorType, ActorID: ptrUUID(actor.ActorID()), Action: action, ResourceType: resourceType, ResourceID: ptrUUID(resourceID), Result: "success", SourceIP: m.SourceIP, UserAgent: m.UserAgent, RequestID: m.RequestID, Details: details}, audit.OutboxEvent{EventType: action, AggregateType: resourceType, AggregateID: resourceID, PayloadVersion: 1, Payload: details, AvailableAt: now})
}
func ptrUUID(id uuid.UUID) *uuid.UUID { return &id }
