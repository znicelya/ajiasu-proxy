use crate::runtime::{RunnerRecord, RunnerState as LocalRunnerState};
use ajiasu_agent_protocol::v1::{AgentMessage, InventorySnapshot, RunnerObservation, RunnerState};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

pub fn message(node_id: &str, records: &[RunnerRecord]) -> AgentMessage {
    AgentMessage {
        body: Some(
            ajiasu_agent_protocol::v1::agent_message::Body::InventorySnapshot(InventorySnapshot {
                node_id: node_id.to_owned(),
                observed_at: now(),
                runners: records
                    .iter()
                    .map(|record| RunnerObservation {
                        node_id: node_id.to_owned(),
                        tenant_id: record.spec.tenant_id.to_string(),
                        endpoint_id: record.spec.endpoint_id.to_string(),
                        runner_id: record.spec.runner_id.to_string(),
                        operation_id: record.spec.operation_id.to_string(),
                        observed_generation: record.spec.generation,
                        state: match record.state {
                            LocalRunnerState::Running => RunnerState::Running as i32,
                            LocalRunnerState::Stopped => RunnerState::Stopped as i32,
                        },
                        reason_code: "inventory".to_owned(),
                        restart_count: 0,
                        observed_at: now(),
                    })
                    .collect(),
            }),
        ),
    }
}

pub fn now() -> String {
    OffsetDateTime::now_utc()
        .format(&Rfc3339)
        .unwrap_or_default()
}
