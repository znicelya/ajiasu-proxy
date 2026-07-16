use std::{
    collections::HashMap,
    sync::{Arc, RwLock},
    time::SystemTime,
};

use ed25519_dalek::{Signature, Verifier, VerifyingKey};
use thiserror::Error;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
use uuid::Uuid;

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Grant {
    pub gateway_id: Uuid,
    pub tenant_id: Uuid,
    pub endpoint_id: Uuid,
    pub runner_id: Uuid,
    pub generation: u64,
    pub policy_hash: String,
    pub expires_at: SystemTime,
    pub protocols: Vec<String>,
    pub signature: Vec<u8>,
}
impl Grant {
    fn signing_bytes(&self) -> Result<Vec<u8>, RouteError> {
        let mut protocols = self.protocols.clone();
        protocols.sort();
        Ok(format!(
            "gateway={}\ntenant={}\nendpoint={}\nrunner={}\ngeneration={}\nprotocols={}\npolicy_hash={}\nexpires_at={}\n",
            self.gateway_id,
            self.tenant_id,
            self.endpoint_id,
            self.runner_id,
            self.generation,
            protocols.join(","),
            self.policy_hash,
            system_time_rfc3339(self.expires_at)?,
        )
        .into_bytes())
    }

    pub fn verify(
        &self,
        key: &VerifyingKey,
        gateway_id: Uuid,
        now: SystemTime,
    ) -> Result<(), RouteError> {
        if self.gateway_id != gateway_id
            || self.gateway_id.is_nil()
            || self.tenant_id.is_nil()
            || self.endpoint_id.is_nil()
            || self.runner_id.is_nil()
            || self.generation == 0
            || self.protocols.is_empty()
            || self.policy_hash.is_empty()
            || self.expires_at <= now
        {
            return Err(RouteError::StaleGrant);
        }
        let signature =
            Signature::from_slice(&self.signature).map_err(|_| RouteError::StaleGrant)?;
        key.verify(&self.signing_bytes()?, &signature)
            .map_err(|_| RouteError::StaleGrant)
    }
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Credential {
    pub id: Uuid,
    pub public_identifier: String,
    pub verifier: String,
    pub expires_at: Option<SystemTime>,
    pub revoked: bool,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Route {
    pub tenant_id: Uuid,
    pub endpoint_id: Uuid,
    pub policy_hash: String,
    pub protocols: Vec<String>,
    pub runner_id: Uuid,
    pub generation: u64,
    pub assignment_id: Uuid,
    pub assignment_generation: u64,
    pub account_id: Uuid,
    pub node_id: Uuid,
    pub assignment_state: String,
    pub valid_until: SystemTime,
    pub grant: Grant,
    pub credentials: Vec<Credential>,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Snapshot {
    pub version: u64,
    pub routes: Vec<Route>,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Delta {
    pub version: u64,
    pub route: Route,
    pub revoked: bool,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum RouteError {
    #[error("route snapshot version is stale")]
    StaleVersion,
    #[error("route snapshot recovery is required")]
    SnapshotRequired,
    #[error("route assignment generation is stale")]
    StaleAssignment,
    #[error("route not found")]
    NotFound,
    #[error("route is not accepting new connections")]
    Unavailable,
    #[error("route grant is expired or mismatched")]
    StaleGrant,
    #[error("protocol is not enabled")]
    ProtocolDenied,
}

#[derive(Clone)]
pub struct RouteTable {
    inner: Arc<RwLock<State>>,
}
#[derive(Default)]
struct State {
    version: u64,
    routes: HashMap<(Uuid, Uuid), Route>,
}
impl RouteTable {
    pub fn new() -> Self {
        Self {
            inner: Arc::new(RwLock::new(State::default())),
        }
    }
    pub fn apply_snapshot(&self, snapshot: Snapshot) -> Result<(), RouteError> {
        let mut state = self.inner.write().map_err(|_| RouteError::StaleVersion)?;
        if snapshot.version == 0 || snapshot.version < state.version {
            return Err(RouteError::StaleVersion);
        }
        let mut routes = HashMap::new();
        for route in snapshot.routes {
            validate_committed_route(&route)?;
            let key = (route.tenant_id, route.endpoint_id);
            if state
                .routes
                .get(&key)
                .is_some_and(|current| route.assignment_generation < current.assignment_generation)
            {
                return Err(RouteError::StaleAssignment);
            }
            routes.insert(key, route);
        }
        if snapshot.version == state.version {
            if routes == state.routes {
                return Ok(());
            }
            return Err(RouteError::StaleVersion);
        }
        state.routes = routes;
        state.version = snapshot.version;
        Ok(())
    }
    pub fn apply_delta(&self, delta: Delta) -> Result<(), RouteError> {
        let mut state = self.inner.write().map_err(|_| RouteError::StaleVersion)?;
        if delta.version < state.version {
            return Err(RouteError::StaleVersion);
        }
        let key = (delta.route.tenant_id, delta.route.endpoint_id);
        if delta.version == state.version {
            if (delta.revoked && !state.routes.contains_key(&key))
                || (!delta.revoked && state.routes.get(&key) == Some(&delta.route))
            {
                return Ok(());
            }
            return Err(RouteError::StaleVersion);
        }
        if state.version != 0 && delta.version != state.version + 1 {
            return Err(RouteError::SnapshotRequired);
        }
        if state.routes.get(&key).is_some_and(|current| {
            delta.route.assignment_generation < current.assignment_generation
        }) {
            return Err(RouteError::StaleAssignment);
        }
        if delta.revoked {
            state.routes.remove(&key);
        } else {
            validate_committed_route(&delta.route)?;
            state.routes.insert(key, delta.route);
        }
        state.version = delta.version;
        Ok(())
    }
    pub fn select(
        &self,
        tenant_id: Uuid,
        endpoint_id: Uuid,
        protocol: &str,
        now: SystemTime,
    ) -> Result<Route, RouteError> {
        let state = self.inner.read().map_err(|_| RouteError::NotFound)?;
        let route = state
            .routes
            .get(&(tenant_id, endpoint_id))
            .cloned()
            .ok_or(RouteError::NotFound)?;
        if route.assignment_state != "assigned" || route.valid_until <= now {
            return Err(RouteError::Unavailable);
        }
        if !route.protocols.iter().any(|item| item == protocol)
            || !route.grant.protocols.iter().any(|item| item == protocol)
        {
            return Err(RouteError::ProtocolDenied);
        }
        if route.grant.gateway_id.is_nil()
            || route.grant.tenant_id != route.tenant_id
            || route.grant.endpoint_id != route.endpoint_id
            || route.grant.runner_id != route.runner_id
            || route.grant.generation != route.generation
            || route.assignment_generation != route.generation
            || route.grant.policy_hash != route.policy_hash
            || route.grant.expires_at <= now
        {
            return Err(RouteError::StaleGrant);
        }
        Ok(route)
    }
    pub fn version(&self) -> u64 {
        self.inner
            .read()
            .map(|state| state.version)
            .unwrap_or_default()
    }

    pub fn route_count(&self) -> usize {
        self.inner
            .read()
            .map(|state| state.routes.len())
            .unwrap_or_default()
    }

    pub fn credential_route(
        &self,
        public_identifier: &str,
        protocol: &str,
        now: SystemTime,
    ) -> Result<(Route, Credential), RouteError> {
        let state = self.inner.read().map_err(|_| RouteError::NotFound)?;
        let mut matched = None;
        for route in state.routes.values() {
            if route.assignment_state != "assigned"
                || route.valid_until <= now
                || !route.protocols.iter().any(|item| item == protocol)
            {
                continue;
            }
            for credential in &route.credentials {
                if credential.public_identifier == public_identifier
                    && !credential.revoked
                    && credential.expires_at.is_none_or(|expiry| expiry > now)
                {
                    if matched.is_some() {
                        return Err(RouteError::NotFound);
                    }
                    matched = Some((route.clone(), credential.clone()));
                }
            }
        }
        let (route, credential) = matched.ok_or(RouteError::NotFound)?;
        validate_committed_route(&route)?;
        if route.grant.expires_at <= now
            || route.grant.generation != route.assignment_generation
            || !route.grant.protocols.iter().any(|item| item == protocol)
        {
            return Err(RouteError::StaleGrant);
        }
        Ok((route, credential))
    }
}

fn validate_committed_route(route: &Route) -> Result<(), RouteError> {
    if route.tenant_id.is_nil()
        || route.endpoint_id.is_nil()
        || route.runner_id.is_nil()
        || route.assignment_id.is_nil()
        || route.account_id.is_nil()
        || route.node_id.is_nil()
        || route.generation == 0
        || route.assignment_generation != route.generation
        || route.grant.gateway_id.is_nil()
        || route.grant.runner_id != route.runner_id
        || route.grant.tenant_id != route.tenant_id
        || route.grant.endpoint_id != route.endpoint_id
        || route.grant.generation != route.generation
        || route.grant.policy_hash != route.policy_hash
        || !matches!(route.assignment_state.as_str(), "assigned" | "draining")
    {
        return Err(RouteError::StaleGrant);
    }
    Ok(())
}

fn system_time_rfc3339(value: SystemTime) -> Result<String, RouteError> {
    let duration = value
        .duration_since(SystemTime::UNIX_EPOCH)
        .map_err(|_| RouteError::StaleGrant)?;
    OffsetDateTime::from_unix_timestamp(duration.as_secs() as i64)
        .map_err(|_| RouteError::StaleGrant)?
        .replace_nanosecond(duration.subsec_nanos())
        .map_err(|_| RouteError::StaleGrant)?
        .format(&Rfc3339)
        .map_err(|_| RouteError::StaleGrant)
}

impl Default for RouteTable {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};
    fn route() -> Route {
        let gateway = Uuid::from_u128(1);
        let tenant = Uuid::from_u128(2);
        let endpoint = Uuid::from_u128(3);
        let runner = Uuid::from_u128(4);
        let expiry = SystemTime::now() + std::time::Duration::from_secs(60);
        Route {
            tenant_id: tenant,
            endpoint_id: endpoint,
            policy_hash: "hash".into(),
            protocols: vec!["connect".into()],
            runner_id: runner,
            generation: 2,
            assignment_id: Uuid::from_u128(5),
            assignment_generation: 2,
            account_id: Uuid::from_u128(6),
            node_id: Uuid::from_u128(7),
            assignment_state: "assigned".into(),
            valid_until: expiry,
            grant: Grant {
                gateway_id: gateway,
                tenant_id: tenant,
                endpoint_id: endpoint,
                runner_id: runner,
                generation: 2,
                policy_hash: "hash".into(),
                expires_at: expiry,
                protocols: vec!["connect".into()],
                signature: vec![1],
            },
            credentials: vec![],
        }
    }
    #[test]
    fn applies_atomically_and_rejects_reordering() {
        let table = RouteTable::new();
        let item = route();
        table
            .apply_snapshot(Snapshot {
                version: 3,
                routes: vec![item.clone()],
            })
            .unwrap();
        assert_eq!(
            table.apply_delta(Delta {
                version: 3,
                route: item.clone(),
                revoked: false
            }),
            Ok(())
        );
        assert!(
            table
                .select(
                    item.tenant_id,
                    item.endpoint_id,
                    "connect",
                    SystemTime::now()
                )
                .is_ok()
        );
    }

    #[test]
    fn requires_snapshot_for_gaps_and_blocks_draining_routes() {
        let table = RouteTable::new();
        let item = route();
        table
            .apply_snapshot(Snapshot {
                version: 4,
                routes: vec![item.clone()],
            })
            .unwrap();
        assert_eq!(
            table.apply_delta(Delta {
                version: 6,
                route: item.clone(),
                revoked: false,
            }),
            Err(RouteError::SnapshotRequired)
        );
        let mut draining = item.clone();
        draining.assignment_state = "draining".into();
        table
            .apply_delta(Delta {
                version: 5,
                route: draining,
                revoked: false,
            })
            .unwrap();
        assert_eq!(
            table.select(
                item.tenant_id,
                item.endpoint_id,
                "connect",
                SystemTime::now()
            ),
            Err(RouteError::Unavailable)
        );
        let established = item;
        assert_eq!(established.assignment_state, "assigned");
    }

    #[test]
    fn credential_selection_rejects_expired_or_protocol_mismatched_grants() {
        let table = RouteTable::new();
        let mut item = route();
        item.credentials.push(Credential {
            id: Uuid::from_u128(8),
            public_identifier: "credential-a".into(),
            verifier: "verifier".into(),
            expires_at: None,
            revoked: false,
        });
        table
            .apply_snapshot(Snapshot {
                version: 1,
                routes: vec![item.clone()],
            })
            .unwrap();
        assert!(
            table
                .credential_route("credential-a", "connect", SystemTime::now())
                .is_ok()
        );

        item.grant.protocols = vec!["http".into()];
        table
            .apply_snapshot(Snapshot {
                version: 2,
                routes: vec![item],
            })
            .unwrap();
        assert_eq!(
            table.credential_route("credential-a", "connect", SystemTime::now()),
            Err(RouteError::StaleGrant)
        );
    }

    #[test]
    fn verifies_signed_gateway_audience_and_expiry() {
        let signing = SigningKey::from_bytes(&[7; 32]);
        let now = SystemTime::UNIX_EPOCH + std::time::Duration::from_secs(1_800_000_000);
        let mut grant = Grant {
            gateway_id: Uuid::from_u128(1),
            tenant_id: Uuid::from_u128(2),
            endpoint_id: Uuid::from_u128(3),
            runner_id: Uuid::from_u128(4),
            generation: 5,
            policy_hash: "hash".into(),
            expires_at: now + std::time::Duration::from_secs(60),
            protocols: vec!["connect".into()],
            signature: Vec::new(),
        };
        grant.signature = signing
            .sign(&grant.signing_bytes().unwrap())
            .to_bytes()
            .to_vec();
        assert_eq!(
            grant.verify(&signing.verifying_key(), grant.gateway_id, now),
            Ok(())
        );
        assert_eq!(
            grant.verify(&signing.verifying_key(), Uuid::from_u128(99), now),
            Err(RouteError::StaleGrant)
        );
    }
}
