use std::collections::BTreeMap;

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use thiserror::Error;
use uuid::Uuid;

pub mod docker;
pub mod process;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct RunnerSpec {
    pub runner_id: Uuid,
    pub operation_id: Uuid,
    pub tenant_id: Uuid,
    pub endpoint_id: Uuid,
    pub generation: u64,
    pub labels: BTreeMap<String, String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct RunnerRecord {
    pub spec: RunnerSpec,
    pub state: RunnerState,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum RunnerState {
    Running,
    Stopped,
}

#[derive(Debug, Error)]
pub enum RuntimeError {
    #[error("runner command generation is stale")]
    StaleGeneration,
    #[error("runner specification conflicts with the active generation")]
    Conflict,
    #[error("container runtime is unavailable")]
    Unavailable,
}

#[async_trait]
pub trait Runtime: Send + Sync {
    async fn inventory(&self) -> Result<Vec<RunnerRecord>, RuntimeError>;
    async fn create(
        &self,
        spec: RunnerSpec,
        credential: &[u8],
    ) -> Result<RunnerRecord, RuntimeError>;
    async fn stop(&self, runner_id: Uuid, generation: u64) -> Result<(), RuntimeError>;
    async fn rebuild(
        &self,
        spec: RunnerSpec,
        credential: &[u8],
    ) -> Result<RunnerRecord, RuntimeError>;
    async fn garbage_collect(&self, desired: &[Uuid]) -> Result<Vec<Uuid>, RuntimeError>;
}
