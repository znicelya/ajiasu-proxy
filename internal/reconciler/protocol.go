package reconciler

import (
	"context"
	"strings"
	"time"

	agentv1 "github.com/znicelya/ajiasu-proxy/internal/gen/agent/v1"
	"github.com/google/uuid"
)

// ApplyAgentMessage validates the node-bound envelope and feeds only stable observations/results into storage.
func (s *Service) ApplyAgentMessage(ctx context.Context, nodeID uuid.UUID, message *agentv1.AgentMessage) error {
	if nodeID == uuid.Nil || message == nil {
		return ErrInvalidArgument
	}
	if snapshot := message.GetInventorySnapshot(); snapshot != nil {
		for _, runner := range snapshot.GetRunners() {
			if err := s.ApplyObservation(ctx, observationFromProto(nodeID, runner)); err != nil && err != ErrStale {
				return err
			}
		}
		return nil
	}
	if runner := message.GetRunnerObservation(); runner != nil {
		return s.ApplyObservation(ctx, observationFromProto(nodeID, runner))
	}
	if result := message.GetOperationResult(); result != nil {
		return s.applyOperationResult(ctx, nodeID, result)
	}
	if ack := message.GetCommandAck(); ack != nil {
		return s.applyCommandAck(ctx, nodeID, ack)
	}
	return nil
}

func observationFromProto(nodeID uuid.UUID, item *agentv1.RunnerObservation) Observation {
	runnerID, _ := uuid.Parse(item.GetRunnerId())
	operationID, _ := uuid.Parse(item.GetOperationId())
	observedAt, _ := time.Parse(time.RFC3339Nano, item.GetObservedAt())
	state := RunnerFailed
	switch item.GetState() {
	case agentv1.RunnerState_RUNNER_STATE_RUNNING:
		state = RunnerRunning
	case agentv1.RunnerState_RUNNER_STATE_STOPPED:
		state = RunnerStopped
	case agentv1.RunnerState_RUNNER_STATE_ORPHANED:
		state = RunnerOrphaned
	}
	reason := strings.TrimSpace(item.GetReasonCode())
	if reason == "" {
		reason = "agent_observation"
	}
	return Observation{NodeID: nodeID, RunnerID: runnerID, OperationID: operationID, Generation: int64(item.GetObservedGeneration()), State: state, ReasonCode: reason, RestartCount: int(item.GetRestartCount()), ObservedAt: observedAt}
}

func (s *Service) applyOperationResult(ctx context.Context, nodeID uuid.UUID, result *agentv1.OperationResult) error {
	operationID, err := uuid.Parse(result.GetOperationId())
	if err != nil || operationID == uuid.Nil {
		return ErrInvalidArgument
	}
	now := s.now().UTC()
	state, code := "failed", "agent_failed"
	if result.GetCode() == agentv1.OperationResultCode_OPERATION_RESULT_CODE_SUCCEEDED {
		state, code = "succeeded", "agent_succeeded"
	}
	if result.GetCode() == agentv1.OperationResultCode_OPERATION_RESULT_CODE_STALE {
		state, code = "cancelled", "stale_generation"
	}
	_, err = s.pools.Platform.Exec(ctx, `UPDATE operations.operations SET state=$1,result_code=$2,safe_message='',completed_at=CASE WHEN $1 IN ('succeeded','failed','cancelled') THEN $3 ELSE completed_at END,updated_at=$3 WHERE id=$4`, state, code, now, operationID)
	return err
}

func (s *Service) applyCommandAck(ctx context.Context, nodeID uuid.UUID, ack *agentv1.CommandAck) error {
	operationID, err := uuid.Parse(ack.GetOperationId())
	if err != nil || operationID == uuid.Nil {
		return ErrInvalidArgument
	}
	if ack.GetCode() != agentv1.CommandAckCode_COMMAND_ACK_CODE_ACCEPTED && ack.GetCode() != agentv1.CommandAckCode_COMMAND_ACK_CODE_DUPLICATE {
		return nil
	}
	_, err = s.pools.Platform.Exec(ctx, `UPDATE operations.operations SET state=CASE WHEN state='queued' THEN 'running' ELSE state END,started_at=COALESCE(started_at,$1),updated_at=$1 WHERE id=$2`, s.now().UTC(), operationID)
	return err
}
