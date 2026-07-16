use std::{collections::BTreeMap, path::PathBuf, sync::Arc};

use async_trait::async_trait;
use tokio::sync::RwLock;
use uuid::Uuid;

use super::{RunnerRecord, RunnerSpec, RunnerState, Runtime, RuntimeError};

#[derive(Clone)]
pub struct ProcessRuntime {
    #[allow(dead_code)]
    state_directory: PathBuf,
    runners: Arc<RwLock<BTreeMap<Uuid, RunnerRecord>>>,
}

impl ProcessRuntime {
    pub fn new(state_directory: PathBuf) -> Self {
        Self {
            state_directory,
            runners: Arc::new(RwLock::new(BTreeMap::new())),
        }
    }
}

#[async_trait]
impl Runtime for ProcessRuntime {
    async fn inventory(&self) -> Result<Vec<RunnerRecord>, RuntimeError> {
        Ok(self.runners.read().await.values().cloned().collect())
    }

    async fn create(
        &self,
        spec: RunnerSpec,
        _credential: &[u8],
    ) -> Result<RunnerRecord, RuntimeError> {
        let mut runners = self.runners.write().await;
        if let Some(existing) = runners.get(&spec.runner_id) {
            if existing.spec.generation > spec.generation {
                return Err(RuntimeError::StaleGeneration);
            }
            if existing.spec.generation == spec.generation {
                if existing.spec == spec {
                    return Ok(existing.clone());
                }
                return Err(RuntimeError::Conflict);
            }
        }
        let record = RunnerRecord {
            spec,
            state: RunnerState::Running,
        };
        runners.insert(record.spec.runner_id, record.clone());
        Ok(record)
    }

    async fn stop(&self, runner_id: Uuid, generation: u64) -> Result<(), RuntimeError> {
        let mut runners = self.runners.write().await;
        let Some(existing) = runners.get(&runner_id) else {
            return Ok(());
        };
        if existing.spec.generation > generation {
            return Err(RuntimeError::StaleGeneration);
        }
        runners.remove(&runner_id);
        Ok(())
    }

    async fn rebuild(
        &self,
        spec: RunnerSpec,
        credential: &[u8],
    ) -> Result<RunnerRecord, RuntimeError> {
        self.stop(spec.runner_id, spec.generation).await?;
        self.create(spec, credential).await
    }

    async fn garbage_collect(&self, desired: &[Uuid]) -> Result<Vec<Uuid>, RuntimeError> {
        let mut runners = self.runners.write().await;
        let removed = runners
            .keys()
            .copied()
            .filter(|runner_id| !desired.contains(runner_id))
            .collect::<Vec<_>>();
        for runner_id in &removed {
            runners.remove(runner_id);
        }
        Ok(removed)
    }
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use super::*;

    fn spec(runner_id: Uuid, operation_id: Uuid, generation: u64) -> RunnerSpec {
        RunnerSpec {
            runner_id,
            operation_id,
            tenant_id: Uuid::now_v7(),
            endpoint_id: Uuid::now_v7(),
            generation,
            labels: BTreeMap::new(),
        }
    }

    #[tokio::test]
    async fn duplicate_create_and_stop_are_idempotent() {
        let runtime = ProcessRuntime::new(PathBuf::from("/tmp/agent-test"));
        let runner_id = Uuid::now_v7();
        let operation_id = Uuid::now_v7();
        let desired = spec(runner_id, operation_id, 1);
        let first = runtime.create(desired.clone(), b"fake").await.unwrap();
        let duplicate = runtime.create(desired, b"fake").await.unwrap();
        assert_eq!(first, duplicate);
        assert_eq!(runtime.inventory().await.unwrap().len(), 1);
        runtime.stop(runner_id, 1).await.unwrap();
        runtime.stop(runner_id, 1).await.unwrap();
        assert!(runtime.inventory().await.unwrap().is_empty());
    }

    #[tokio::test]
    async fn stale_generation_cannot_replace_newer_runner() {
        let runtime = ProcessRuntime::new(PathBuf::from("/tmp/agent-test"));
        let runner_id = Uuid::now_v7();
        runtime
            .create(spec(runner_id, Uuid::now_v7(), 2), b"fake")
            .await
            .unwrap();
        let error = runtime
            .create(spec(runner_id, Uuid::now_v7(), 1), b"fake")
            .await
            .unwrap_err();
        assert!(matches!(error, RuntimeError::StaleGeneration));
        assert_eq!(runtime.inventory().await.unwrap()[0].spec.generation, 2);
    }

    #[tokio::test]
    async fn rebuild_and_garbage_collect_converge_inventory() {
        let runtime = ProcessRuntime::new(PathBuf::from("/tmp/agent-test"));
        let retained = Uuid::now_v7();
        let removed = Uuid::now_v7();
        runtime
            .create(spec(retained, Uuid::now_v7(), 1), b"fake")
            .await
            .unwrap();
        runtime
            .create(spec(removed, Uuid::now_v7(), 1), b"fake")
            .await
            .unwrap();
        let rebuilt = runtime
            .rebuild(spec(retained, Uuid::now_v7(), 2), b"fake")
            .await
            .unwrap();
        assert_eq!(rebuilt.spec.generation, 2);
        assert_eq!(
            runtime.garbage_collect(&[retained]).await.unwrap(),
            vec![removed]
        );
        assert_eq!(runtime.inventory().await.unwrap(), vec![rebuilt]);
    }
}
