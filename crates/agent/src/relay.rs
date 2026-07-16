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

#[derive(Clone)]
struct RunnerSocket {
    tenant_id: Uuid,
    endpoint_id: Uuid,
    path: PathBuf,
}

#[derive(Clone, Debug)]
pub struct RelayOpen {
    pub gateway_id: Uuid,
    pub runner_id: Uuid,
    pub generation: u64,
    pub assignment_id: Uuid,
    pub assignment_generation: u64,
    pub assignment_valid_until: OffsetDateTime,
    pub protocol: String,
    pub dns_mode: String,
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
    inner: Arc<Mutex<HashMap<(Uuid, u64), RunnerSocket>>>,
}
impl RunnerSockets {
    pub fn insert(
        &self,
        tenant_id: Uuid,
        endpoint_id: Uuid,
        runner_id: Uuid,
        generation: u64,
        path: PathBuf,
    ) -> Result<(), RelayError> {
        if tenant_id.is_nil()
            || endpoint_id.is_nil()
            || runner_id.is_nil()
            || generation == 0
            || !safe_socket_path(&path)
        {
            return Err(RelayError::InvalidMetadata);
        };
        self.inner
            .lock()
            .map_err(|_| RelayError::RunnerUnavailable)?
            .insert(
                (runner_id, generation),
                RunnerSocket {
                    tenant_id,
                    endpoint_id,
                    path,
                },
            );
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
            || !matches!(open.dns_mode.as_str(), "gateway" | "runner")
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
        let socket = self
            .inner
            .lock()
            .map_err(|_| RelayError::RunnerUnavailable)?
            .get(&(open.runner_id, open.generation))
            .cloned()
            .ok_or(RelayError::RunnerUnavailable)?;
        if socket.tenant_id != open.grant.tenant_id || socket.endpoint_id != open.grant.endpoint_id
        {
            return Err(RelayError::Unauthorized);
        }
        Ok(socket.path)
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

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};

    fn signed_open(
        signing: &SigningKey,
        tenant_id: Uuid,
        endpoint_id: Uuid,
        runner_id: Uuid,
        generation: u64,
        now: OffsetDateTime,
    ) -> RelayOpen {
        let mut grant = RouteGrant {
            gateway_id: Uuid::now_v7(),
            tenant_id,
            endpoint_id,
            runner_id,
            generation,
            protocols: vec!["connect".into()],
            policy_hash: "policy".into(),
            expires_at: now + time::Duration::minutes(1),
            signature: Vec::new(),
        };
        grant.signature = signing
            .sign(&grant.signing_bytes().unwrap())
            .to_bytes()
            .to_vec();
        RelayOpen {
            gateway_id: grant.gateway_id,
            runner_id,
            generation,
            assignment_id: Uuid::now_v7(),
            assignment_generation: generation,
            assignment_valid_until: now + time::Duration::minutes(1),
            protocol: "connect".into(),
            dns_mode: "runner".into(),
            policy_hash: "policy".into(),
            target_host: "example.com".into(),
            target_port: 443,
            grant,
        }
    }

    #[test]
    fn binds_signed_route_to_runner_tenant_endpoint_and_generation() {
        let signing = SigningKey::from_bytes(&[7; 32]);
        let sockets = RunnerSockets::default();
        let tenant_id = Uuid::now_v7();
        let endpoint_id = Uuid::now_v7();
        let runner_id = Uuid::now_v7();
        let path = std::env::temp_dir().join("ajiasu-runner.sock");
        sockets
            .insert(tenant_id, endpoint_id, runner_id, 4, path.clone())
            .unwrap();
        let now = OffsetDateTime::now_utc();
        let open = signed_open(&signing, tenant_id, endpoint_id, runner_id, 4, now);
        assert_eq!(
            sockets.authorize(&open, &signing.verifying_key(), now),
            Ok(path)
        );

        let cross_tenant = signed_open(&signing, Uuid::now_v7(), endpoint_id, runner_id, 4, now);
        assert_eq!(
            sockets.authorize(&cross_tenant, &signing.verifying_key(), now),
            Err(RelayError::Unauthorized)
        );
        let stale = signed_open(&signing, tenant_id, endpoint_id, runner_id, 3, now);
        assert_eq!(
            sockets.authorize(&stale, &signing.verifying_key(), now),
            Err(RelayError::RunnerUnavailable)
        );
    }
}
