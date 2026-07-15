package reconciler

import (
	"context"
	"strings"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ForceFinalized struct {
	NodeID              uuid.UUID `json:"node_id"`
	RunnerID            uuid.UUID `json:"runner_id"`
	EndpointID          uuid.UUID `json:"endpoint_id"`
	ReservationReleased bool      `json:"reservation_released"`
}

func (s *Service) ForceFinalize(ctx context.Context, actor tenancy.PlatformActor, nodeID, runnerID uuid.UUID, reason string) (ForceFinalized, error) {
	if !actor.Allows(tenancy.ActionManageNodes) {
		return ForceFinalized{}, ErrForbidden
	}
	reason = strings.TrimSpace(reason)
	if nodeID == uuid.Nil || runnerID == uuid.Nil || len(reason) == 0 || len(reason) > 128 {
		return ForceFinalized{}, ErrInvalidArgument
	}
	return database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (ForceFinalized, error) {
		var tenantID, endpointID, desiredNodeID, operationID uuid.UUID
		var reservationID *uuid.UUID
		var lifecycle string
		if err := tx.QueryRow(ctx, `SELECT d.tenant_id,d.endpoint_id,d.node_id,d.operation_id,d.capacity_reservation_id,e.lifecycle_state FROM reconciler.runner_desired_states d JOIN endpoints.proxy_endpoints e ON e.tenant_id=d.tenant_id AND e.id=d.endpoint_id WHERE d.runner_id=$1 FOR UPDATE`, runnerID).Scan(&tenantID, &endpointID, &desiredNodeID, &operationID, &reservationID, &lifecycle); err != nil {
			return ForceFinalized{}, mapError(err)
		}
		if desiredNodeID != nodeID {
			return ForceFinalized{}, ErrStale
		}
		now := s.now().UTC()
		if _, err := tx.Exec(ctx, `DELETE FROM reconciler.work_items WHERE operation_id=$1`, operationID); err != nil {
			return ForceFinalized{}, mapError(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM reconciler.runner_desired_states WHERE runner_id=$1`, runnerID); err != nil {
			return ForceFinalized{}, mapError(err)
		}
		released := false
		if reservationID != nil {
			if _, err := tx.Exec(ctx, `DELETE FROM accounts.account_capacity_reservations WHERE tenant_id=$1 AND id=$2`, tenantID, *reservationID); err != nil {
				return ForceFinalized{}, mapError(err)
			}
			released = true
		}
		if _, err := tx.Exec(ctx, `DELETE FROM reconciler.finalizers WHERE tenant_id=$1 AND resource_type='endpoint' AND resource_id=$2 AND finalizer='runner.cleanup'`, tenantID, endpointID); err != nil {
			return ForceFinalized{}, mapError(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE operations.operations SET state='cancelled',progress_category='force_finalized',result_code='force_finalized',safe_message='',completed_at=$1,updated_at=$1 WHERE id=$2`, now, operationID); err != nil {
			return ForceFinalized{}, mapError(err)
		}
		if lifecycle == "deleting" {
			if _, err := tx.Exec(ctx, `DELETE FROM endpoints.proxy_endpoints WHERE tenant_id=$1 AND id=$2`, tenantID, endpointID); err != nil {
				return ForceFinalized{}, mapError(err)
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE endpoints.proxy_endpoints SET desired_runner_state='stopped',version=version+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, tenantID, endpointID); err != nil {
				return ForceFinalized{}, mapError(err)
			}
			if _, err := tx.Exec(ctx, `UPDATE endpoints.endpoint_status SET observed_state='orphaned',reason_code='force_finalized',last_transition_at=$1,updated_at=$1 WHERE tenant_id=$2 AND endpoint_id=$3`, now, tenantID, endpointID); err != nil {
				return ForceFinalized{}, mapError(err)
			}
		}
		metadata := actor.Metadata()
		actorID := actor.ActorID()
		details := map[string]any{"node_id": nodeID.String(), "runner_id": runnerID.String(), "endpoint_id": endpointID.String(), "reason_category": reason, "reservation_released": released, "duplicate_login_risk_acknowledged": true}
		if err := s.audit.Append(ctx, tx, audit.Event{TenantID: &tenantID, ActorType: metadata.ActorType, ActorID: &actorID, Action: "reconciler.runner.force_finalized", ResourceType: "runner", ResourceID: &runnerID, Result: "success", SourceIP: metadata.SourceIP, UserAgent: metadata.UserAgent, RequestID: metadata.RequestID, Details: details}, audit.OutboxEvent{EventType: "reconciler.runner.force_finalized", AggregateType: "runner", AggregateID: runnerID, PayloadVersion: 1, Payload: details, AvailableAt: now}); err != nil {
			return ForceFinalized{}, err
		}
		return ForceFinalized{NodeID: nodeID, RunnerID: runnerID, EndpointID: endpointID, ReservationReleased: released}, nil
	})
}
