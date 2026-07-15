use std::collections::BTreeMap;

use ajiasu_agent_protocol::v1::{
    AgentMessage, CommandAck, CommandAckCode, OperationResult, OperationResultCode, RunnerAction,
    RunnerCommand, RunnerObservation, RunnerState, agent_message,
};
use uuid::Uuid;

use crate::{
    inventory::now,
    runtime::{RunnerSpec, Runtime, RuntimeError},
    secret::SecretBytes,
};

pub async fn execute(
    runtime: &dyn Runtime,
    node_id: &str,
    command: RunnerCommand,
) -> Vec<AgentMessage> {
    let runner_id = match Uuid::parse_str(&command.runner_id) {
        Ok(id) => id,
        Err(_) => {
            return rejected(
                &command,
                CommandAckCode::Rejected,
                OperationResultCode::Rejected,
            );
        }
    };
    let operation_id = match Uuid::parse_str(&command.operation_id) {
        Ok(id) => id,
        Err(_) => {
            return rejected(
                &command,
                CommandAckCode::Rejected,
                OperationResultCode::Rejected,
            );
        }
    };
    let labels: BTreeMap<String, String> = command
        .runtime
        .as_ref()
        .map(|runtime| runtime.immutable_labels.clone().into_iter().collect())
        .unwrap_or_default();
    let spec = RunnerSpec {
        runner_id,
        operation_id,
        generation: command.desired_generation,
        labels,
    };
    let secret = SecretBytes::new(command.credential_configuration.clone());
    let _secret_length = secret.expose().len();
    let result = match RunnerAction::try_from(command.action).unwrap_or(RunnerAction::Unspecified) {
        RunnerAction::Create => runtime
            .create(spec, secret.expose())
            .await
            .map(|_| (RunnerState::Running, "created")),
        RunnerAction::Rebuild => runtime
            .rebuild(spec, secret.expose())
            .await
            .map(|_| (RunnerState::Running, "rebuilt")),
        RunnerAction::Stop => runtime
            .stop(runner_id, command.desired_generation)
            .await
            .map(|()| (RunnerState::Stopped, "stopped")),
        RunnerAction::GarbageCollect => runtime
            .garbage_collect(&[])
            .await
            .map(|_| (RunnerState::Stopped, "garbage_collected")),
        RunnerAction::Unspecified => Err(RuntimeError::Conflict),
    };
    drop(secret);
    match result {
        Ok((state, reason)) => vec![
            ack(&command, CommandAckCode::Accepted),
            observation(node_id, &command, state, reason),
            operation_result(&command, OperationResultCode::Succeeded),
        ],
        Err(RuntimeError::StaleGeneration) => {
            rejected(&command, CommandAckCode::Stale, OperationResultCode::Stale)
        }
        Err(RuntimeError::Conflict) => rejected(
            &command,
            CommandAckCode::Rejected,
            OperationResultCode::Rejected,
        ),
        Err(RuntimeError::Unavailable) => rejected(
            &command,
            CommandAckCode::Rejected,
            OperationResultCode::Failed,
        ),
    }
}

fn rejected(
    command: &RunnerCommand,
    ack_code: CommandAckCode,
    result_code: OperationResultCode,
) -> Vec<AgentMessage> {
    vec![
        ack(command, ack_code),
        operation_result(command, result_code),
    ]
}

fn ack(command: &RunnerCommand, code: CommandAckCode) -> AgentMessage {
    AgentMessage {
        body: Some(agent_message::Body::CommandAck(CommandAck {
            operation_id: command.operation_id.clone(),
            runner_id: command.runner_id.clone(),
            code: code as i32,
            observed_generation: command.desired_generation,
        })),
    }
}

fn operation_result(command: &RunnerCommand, code: OperationResultCode) -> AgentMessage {
    AgentMessage {
        body: Some(agent_message::Body::OperationResult(OperationResult {
            operation_id: command.operation_id.clone(),
            runner_id: command.runner_id.clone(),
            observed_generation: command.desired_generation,
            code: code as i32,
            safe_message: String::new(),
            completed_at: now(),
        })),
    }
}

fn observation(
    node_id: &str,
    command: &RunnerCommand,
    state: RunnerState,
    reason: &str,
) -> AgentMessage {
    AgentMessage {
        body: Some(agent_message::Body::RunnerObservation(RunnerObservation {
            node_id: node_id.to_owned(),
            tenant_id: command.tenant_id.clone(),
            endpoint_id: command.endpoint_id.clone(),
            runner_id: command.runner_id.clone(),
            operation_id: command.operation_id.clone(),
            observed_generation: command.desired_generation,
            state: state as i32,
            reason_code: reason.to_owned(),
            restart_count: 0,
            observed_at: now(),
        })),
    }
}
