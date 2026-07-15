use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
};

use ed25519_dalek::VerifyingKey;
use thiserror::Error;
use time::OffsetDateTime;
use uuid::Uuid;

use crate::route_grant::{GrantExpectation, RouteGrant};

pub const MAX_RELAY_FRAME: usize = 64 * 1024;

#[derive(Clone, Debug)]
pub struct RelayOpen {
    pub gateway_id: Uuid,
    pub runner_id: Uuid,
    pub generation: u64,
    pub assignment_id: Uuid,
    pub assignment_generation: u64,
    pub assignment_valid_until: OffsetDateTime,
    pub protocol: String,
    pub policy_hash: String,
    pub target_host: String,
    pub target_port: u16,
    pub grant: RouteGrant,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum RelayError {
    #[error("relay unauthorized")]
    Unauthorized,
    #[error("runner unavailable")]
    RunnerUnavailable,
    #[error("relay frame too large")]
    FrameTooLarge,
    #[error("invalid relay metadata")]
    InvalidMetadata,
}

#[derive(Clone, Default)]
pub struct RunnerSockets {
    inner: Arc<Mutex<HashMap<(Uuid, u64), PathBuf>>>,
}
impl RunnerSockets {
    pub fn insert(
        &self,
        runner_id: Uuid,
        generation: u64,
        path: PathBuf,
    ) -> Result<(), RelayError> {
        if runner_id.is_nil() || generation == 0 || !safe_socket_path(&path) {
            return Err(RelayError::InvalidMetadata);
        };
        self.inner
            .lock()
            .map_err(|_| RelayError::RunnerUnavailable)?
            .insert((runner_id, generation), path);
        Ok(())
    }
    pub fn remove(&self, runner_id: Uuid, generation: u64) {
        if let Ok(mut guard) = self.inner.lock() {
            guard.remove(&(runner_id, generation));
        }
    }
    pub fn authorize(
        &self,
        open: &RelayOpen,
        key: &VerifyingKey,
        now: OffsetDateTime,
    ) -> Result<PathBuf, RelayError> {
        if open.target_host.is_empty()
            || open.target_host.len() > 4096
            || open.target_port == 0
            || !matches!(open.protocol.as_str(), "http" | "connect" | "socks5")
        {
            return Err(RelayError::InvalidMetadata);
        };
        if open.assignment_id.is_nil()
            || open.assignment_generation != open.generation
            || open.assignment_valid_until <= now
        {
            return Err(RelayError::Unauthorized);
        }
        open.grant
            .verify(
                key,
                GrantExpectation {
                    gateway_id: open.gateway_id,
                    runner_id: open.runner_id,
                    generation: open.generation,
                    protocol: &open.protocol,
                    policy_hash: &open.policy_hash,
                    now,
                },
            )
            .map_err(|_| RelayError::Unauthorized)?;
        self.inner
            .lock()
            .map_err(|_| RelayError::RunnerUnavailable)?
            .get(&(open.runner_id, open.generation))
            .cloned()
            .ok_or(RelayError::RunnerUnavailable)
    }
}

pub fn validate_frame(payload: &[u8]) -> Result<(), RelayError> {
    if payload.len() > MAX_RELAY_FRAME {
        Err(RelayError::FrameTooLarge)
    } else {
        Ok(())
    }
}
fn safe_socket_path(path: &Path) -> bool {
    path.is_absolute()
        && path
            .extension()
            .is_some_and(|extension| extension == "sock")
        && !path.to_string_lossy().contains("..")
}
